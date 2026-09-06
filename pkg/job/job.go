// Package job provides asynchronous job management for long-running operations.
//
// Jobs allow clients to submit operations that may take a long time (instance
// creation, image fetching, etc.) and poll for their status instead of blocking
// the HTTP connection.
//
// Usage:
//
//	// Submit a job
//	job, err := jm.Submit("instance.create", "Creating web server", map[string]string{
//	    "instance": "web",
//	}, func(ctx context.Context, j *job.Job) error {
//	    j.UpdateProgress(0.1, "Preparing...")
//	    // ... do work ...
//	    j.UpdateProgress(0.5, "Extracting image...")
//	    // ... more work ...
//	    j.UpdateProgress(1.0, "Done")
//	    return nil
//	})
//
//	// Poll for status
//	job, err := jm.GetJob(job.ID)
package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// JobStatus represents the current state of a job.
type JobStatus string

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "canceled"
)

// Job represents an asynchronous background task.
type Job struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`        // e.g., "instance.create", "image.fetch"
	Description string            `json:"description"` // human-readable description
	Status      JobStatus         `json:"status"`
	Progress    float64           `json:"progress"` // 0.0 to 1.0
	Message     string            `json:"message"`  // current status message
	Result      map[string]any    `json:"result,omitempty"`
	Error       string            `json:"error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`

	// Internal fields (not serialized to JSON)
	mu     sync.RWMutex
	cancel context.CancelFunc
	done   chan struct{}
}

// UpdateProgress updates the job's progress and message.
// This is safe to call from the job's execution function.
func (j *Job) UpdateProgress(progress float64, message string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	j.Progress = progress
	j.Message = message
}

// SetResult sets the job result data on completion.
func (j *Job) SetResult(result map[string]any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Result = result
}

// Done returns a channel that is closed when the job completes (success, failure, or cancellation).
func (j *Job) Done() <-chan struct{} {
	return j.done
}

// snapshotStatus returns the job's status under its own lock. Callers that hold
// only jm.mu (which does not protect per-job fields) must use this instead of
// reading j.Status directly, to avoid racing with executeJob.
func (j *Job) snapshotStatus() JobStatus {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.Status
}

// JobRecord is a lock-free copy of a Job's persistable fields. The persistence
// layer and the HTTP handlers read a JobRecord (via Snapshot) instead of
// touching a live Job, whose fields are mutated in place by executeJob under
// the job's mutex. The JSON tags mirror Job's so a marshaled record has the
// same wire shape as a marshaled Job.
type JobRecord struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Description string            `json:"description"`
	Status      JobStatus         `json:"status"`
	Progress    float64           `json:"progress"`
	Message     string            `json:"message"`
	Result      map[string]any    `json:"result,omitempty"`
	Error       string            `json:"error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Snapshot returns a consistent copy of the job's persistable fields taken
// under the job's read lock, so persistence code (e.g. datastore.UpdateJob)
// does not race with the job's execution goroutine.
func (j *Job) Snapshot() JobRecord {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return JobRecord{
		ID:          j.ID,
		Type:        j.Type,
		Description: j.Description,
		Status:      j.Status,
		Progress:    j.Progress,
		Message:     j.Message,
		Result:      j.Result,
		Error:       j.Error,
		CreatedAt:   j.CreatedAt,
		StartedAt:   j.StartedAt,
		CompletedAt: j.CompletedAt,
		Metadata:    j.Metadata,
	}
}

// JobFunc is the function type for job execution.
// The function receives a context that is canceled if the job is canceled,
// and a pointer to the Job for progress updates.
type JobFunc func(ctx context.Context, job *Job) error

// JobManager manages asynchronous background jobs.
// Errors the manager returns, exported so a caller can tell them apart with
// errors.Is rather than by matching on the message.
var (
	ErrJobNotFound       = errors.New("job not found")
	ErrQueueFull         = errors.New("job queue is full")
	ErrJobNotCancellable = errors.New("job cannot be canceled")
)

type JobManager struct {
	mu           sync.RWMutex
	jobs         map[string]*Job
	queue        chan *jobTask
	workers      int
	wg           sync.WaitGroup
	logger       *slog.Logger
	stopCh       chan struct{}
	shutdownOnce sync.Once
	shuttingDown bool       // guarded by mu; set once Shutdown closes the queue
	onUpdate     func(*Job) // optional hook fired when a job reaches a terminal state

	// ctx is the parent of every running job's context, so Shutdown can reach
	// the work a job is doing instead of only refusing to start new work.
	ctx       context.Context
	cancelAll context.CancelFunc
}

// SetUpdateHook registers a callback invoked when a job finishes (completed,
// failed, or canceled). It lets callers persist the terminal job state. Set it
// once before submitting jobs.
func (jm *JobManager) SetUpdateHook(fn func(*Job)) {
	jm.onUpdate = fn
}

func (jm *JobManager) notifyUpdate(job *Job) {
	if jm.onUpdate != nil {
		jm.onUpdate(job)
	}
}

type jobTask struct {
	job *Job
	fn  JobFunc
}

// JobManagerConfig configures the JobManager.
type JobManagerConfig struct {
	Workers         int           // Number of worker goroutines (default: 4)
	QueueSize       int           // Job queue capacity (default: 100)
	CleanupInterval time.Duration // How often to clean up old jobs (default: 5m)
	MaxJobAge       time.Duration // Remove completed/failed jobs older than this (default: 24h)
}

// DefaultJobManagerConfig returns a JobManagerConfig with sensible defaults.
func DefaultJobManagerConfig() JobManagerConfig {
	return JobManagerConfig{
		Workers:         4,
		QueueSize:       100,
		CleanupInterval: 5 * time.Minute,
		MaxJobAge:       24 * time.Hour,
	}
}

// NewJobManager creates a new job manager and starts worker goroutines.
func NewJobManager(cfg JobManagerConfig, logger *slog.Logger) *JobManager {
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 100
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}
	if cfg.MaxJobAge <= 0 {
		cfg.MaxJobAge = 24 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}

	ctx, cancelAll := context.WithCancel(context.Background())
	jm := &JobManager{
		jobs:      make(map[string]*Job),
		queue:     make(chan *jobTask, cfg.QueueSize),
		workers:   cfg.Workers,
		logger:    logger,
		stopCh:    make(chan struct{}),
		ctx:       ctx,
		cancelAll: cancelAll,
	}

	// Start workers
	for i := 0; i < jm.workers; i++ {
		jm.wg.Add(1)
		go jm.worker()
	}

	// Start cleanup goroutine
	go jm.cleanupLoop(cfg.CleanupInterval, cfg.MaxJobAge)

	return jm
}

// Submit creates and queues a new job for background execution.
func (jm *JobManager) Submit(jobType, description string, metadata map[string]string, fn JobFunc) (*Job, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("failed to generate job ID: %w", err)
	}
	id := "job-" + hex.EncodeToString(b)

	job := &Job{
		ID:          id,
		Type:        jobType,
		Description: description,
		Status:      JobStatusPending,
		Progress:    0,
		CreatedAt:   time.Now(),
		Metadata:    metadata,
		done:        make(chan struct{}),
	}

	// Hold the manager lock across the shutdown check, the map insert, and the
	// queue send. Shutdown takes the same lock before closing jm.queue, so the
	// send can never race with the close (no "send on closed channel" panic).
	jm.mu.Lock()
	if jm.shuttingDown {
		jm.mu.Unlock()
		return nil, fmt.Errorf("job manager is shutting down")
	}
	jm.jobs[id] = job

	// Non-blocking queue submission
	select {
	case jm.queue <- &jobTask{job: job, fn: fn}:
		jm.mu.Unlock()
		jm.logger.Info("Job submitted", "job_id", id, "type", jobType, "description", description)
	default:
		// Queue is full
		delete(jm.jobs, id)
		jm.mu.Unlock()
		return nil, fmt.Errorf("%w, try again later", ErrQueueFull)
	}

	return job, nil
}

// GetJob returns a job by ID.
func (jm *JobManager) GetJob(id string) (*Job, error) {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	job, ok := jm.jobs[id]
	if !ok {
		return nil, fmt.Errorf("job not found: %s", id)
	}
	return job, nil
}

// ListJobs returns all jobs, optionally filtered by status.
func (jm *JobManager) ListJobs(status *JobStatus) []*Job {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	var result []*Job
	for _, job := range jm.jobs {
		if status == nil || job.snapshotStatus() == *status {
			result = append(result, job)
		}
	}
	return result
}

// CancelJob cancels a pending or running job.
func (jm *JobManager) CancelJob(id string) error {
	jm.mu.RLock()
	job, ok := jm.jobs[id]
	jm.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrJobNotFound, id)
	}

	job.mu.Lock()
	currentStatus := job.Status
	if currentStatus != JobStatusRunning && currentStatus != JobStatusPending {
		job.mu.Unlock()
		return fmt.Errorf("%w: status is %s", ErrJobNotCancellable, currentStatus)
	}

	job.Status = JobStatusCancelled
	job.Message = "Canceled by user"
	now := time.Now()
	job.CompletedAt = &now
	// cancel func is set by executeJob; may be nil if job hasn't started yet
	cancelFn := job.cancel
	job.mu.Unlock()

	if cancelFn != nil {
		cancelFn()
	}

	// Cancellation is a terminal state, and the hook is what mirrors a terminal
	// state into the datastore. Without this a canceled job stays "running"
	// there for as long as the record survives.
	jm.notifyUpdate(job)

	jm.logger.Info("Job canceled", "job_id", id)
	return nil
}

// DeleteJob removes a completed, failed, or canceled job from the manager.
func (jm *JobManager) DeleteJob(id string) error {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	job, ok := jm.jobs[id]
	if !ok {
		return fmt.Errorf("job not found: %s", id)
	}

	if st := job.snapshotStatus(); st == JobStatusRunning || st == JobStatusPending {
		return fmt.Errorf("cannot delete active job: status is %s", st)
	}

	delete(jm.jobs, id)
	return nil
}

// Stats returns job manager statistics.
func (jm *JobManager) Stats() map[string]int {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	stats := make(map[string]int)
	for _, job := range jm.jobs {
		stats[string(job.snapshotStatus())]++
	}
	stats["total"] = len(jm.jobs)
	stats["queue_depth"] = len(jm.queue)
	return stats
}

// Shutdown gracefully stops the job manager, waiting for running jobs to complete.
// It is safe to call more than once.
func (jm *JobManager) Shutdown(timeout time.Duration) {
	// Signal workers/cleanup loop to stop and close the queue exactly once.
	// The lock excludes Submit's queue send, and shutdownOnce makes the close
	// idempotent so a second Shutdown does not double-close the channels.
	jm.shutdownOnce.Do(func() {
		jm.mu.Lock()
		jm.shuttingDown = true
		close(jm.stopCh)
		close(jm.queue)
		jm.mu.Unlock()
	})

	// Reaches the work itself: a job whose function is mid-command has a context
	// derived from this one, and one still queued never gets its own.
	jm.cancelAll()

	// Cancel all pending/running jobs
	jm.mu.RLock()
	for _, job := range jm.jobs {
		job.mu.Lock()
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			if job.cancel != nil {
				job.cancel()
			}
		}
		job.mu.Unlock()
	}
	jm.mu.RUnlock()

	// Wait for all workers to finish, with timeout
	done := make(chan struct{})
	go func() {
		jm.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		jm.logger.Info("Job manager shut down cleanly")
	case <-time.After(timeout):
		jm.logger.Warn("Job manager shutdown timed out", "timeout", timeout)
	}
}

func (jm *JobManager) worker() {
	defer jm.wg.Done()
	for {
		select {
		case task, ok := <-jm.queue:
			if !ok {
				return
			}
			jm.executeJob(task.job, task.fn)
		case <-jm.stopCh:
			// Drain remaining queued tasks before exiting
			for {
				select {
				case task, ok := <-jm.queue:
					if !ok {
						return
					}
					jm.executeJob(task.job, task.fn)
				default:
					return
				}
			}
		}
	}
}

func (jm *JobManager) executeJob(job *Job, fn JobFunc) {
	now := time.Now()
	job.mu.Lock()
	// Skip execution if job was canceled before it started
	if job.Status == JobStatusCancelled {
		job.mu.Unlock()
		close(job.done)
		return
	}
	job.Status = JobStatusRunning
	job.StartedAt = &now
	job.Message = "Starting..."
	ctx, cancel := context.WithCancel(jm.ctx)
	job.cancel = cancel
	job.mu.Unlock()
	// Always release the context when the job finishes, even on normal
	// completion, so the cancel func stored above does not leak.
	defer cancel()

	defer func() {
		// Recover from panics in job functions
		if r := recover(); r != nil {
			completedAt := time.Now()
			job.mu.Lock()
			job.Status = JobStatusFailed
			job.Error = fmt.Sprintf("panic: %v", r)
			job.Message = "Failed (panic)"
			job.CompletedAt = &completedAt
			job.mu.Unlock()
			jm.logger.Error("Job panicked", "job_id", job.ID, "panic", r)
			jm.notifyUpdate(job)
		}
		close(job.done)
	}()

	err := fn(ctx, job)

	completedAt := time.Now()
	job.mu.Lock()

	// A job explicitly canceled via CancelJob must never be reported as
	// completed, even when the job function ignored ctx cancellation and
	// returned nil. CancelJob already set the status and CompletedAt.
	if job.Status == JobStatusCancelled {
		job.Message = "Canceled"
		if job.CompletedAt == nil {
			job.CompletedAt = &completedAt
		}
		job.mu.Unlock()
		jm.logger.Info("Job canceled", "job_id", job.ID, "type", job.Type)
		jm.notifyUpdate(job)
		return
	}

	job.CompletedAt = &completedAt
	if err != nil {
		if ctx.Err() == context.Canceled {
			job.Status = JobStatusCancelled
			job.Message = "Canceled"
		} else {
			job.Status = JobStatusFailed
			job.Error = err.Error()
			job.Message = "Failed"
		}
		jm.logger.Error("Job failed", "job_id", job.ID, "type", job.Type, "error", err)
	} else {
		job.Status = JobStatusCompleted
		job.Progress = 1.0
		job.Message = "Completed"
		jm.logger.Info("Job completed", "job_id", job.ID, "type", job.Type)
	}
	job.mu.Unlock()
	jm.notifyUpdate(job)
}

func (jm *JobManager) cleanupLoop(interval, maxAge time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cleaned := jm.cleanupOldJobs(maxAge)
			if cleaned > 0 {
				jm.logger.Debug("Cleaned up old jobs", "count", cleaned)
			}
		case <-jm.stopCh:
			return
		}
	}
}

func (jm *JobManager) cleanupOldJobs(maxAge time.Duration) int {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	count := 0
	for id, job := range jm.jobs {
		job.mu.RLock()
		completedAt := job.CompletedAt
		status := job.Status
		job.mu.RUnlock()

		if (status == JobStatusCompleted || status == JobStatusFailed || status == JobStatusCancelled) &&
			completedAt != nil && completedAt.Before(cutoff) {
			delete(jm.jobs, id)
			count++
		}
	}
	return count
}
