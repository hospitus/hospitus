package job

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestManager creates a JobManager configured for fast, isolated unit tests.
// CleanupInterval is set very high to avoid interfering with assertions.
func newTestManager(workers, queueSize int) *JobManager {
	return NewJobManager(JobManagerConfig{
		Workers:         workers,
		QueueSize:       queueSize,
		CleanupInterval: 24 * time.Hour,
		MaxJobAge:       24 * time.Hour,
	}, slog.Default())
}

// TestSubmit_Success verifies that a job that completes normally ends with
// status=completed and that the done channel is closed.
func TestSubmit_Success(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(5 * time.Second)

	j, err := jm.Submit("test.op", "success job", nil, func(ctx context.Context, job *Job) error {
		job.UpdateProgress(0.5, "halfway")
		job.UpdateProgress(1.0, "done")
		return nil
	})
	if err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	select {
	case <-j.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for job to complete")
	}

	if j.Status != JobStatusCompleted {
		t.Errorf("expected status=%s, got %s", JobStatusCompleted, j.Status)
	}
	if j.Progress != 1.0 {
		t.Errorf("expected progress=1.0, got %f", j.Progress)
	}
	if j.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
	if j.StartedAt == nil {
		t.Error("expected StartedAt to be set")
	}
}

// TestSubmit_AfterShutdown verifies that submitting a job after Shutdown is
// refused with a sentinel a caller can match.
func TestSubmit_AfterShutdown(t *testing.T) {
	jm := newTestManager(1, 10)
	jm.Shutdown(500 * time.Millisecond)

	_, err := jm.Submit("test.op", "after shutdown", nil, func(ctx context.Context, job *Job) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error when submitting after shutdown, got nil")
	}
	if !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Submit after shutdown = %v, want ErrShuttingDown", err)
	}
}

// TestJobIDUniqueness submits 100 jobs concurrently and verifies that every
// generated job ID (from crypto/rand) is unique.
func TestJobIDUniqueness(t *testing.T) {
	jm := newTestManager(4, 200)
	defer jm.Shutdown(5 * time.Second)

	const n = 100
	ids := make(map[string]struct{}, n)
	submitted := 0
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := jm.Submit("test.id", "uniqueness check", nil, func(ctx context.Context, job *Job) error {
				return nil
			})
			if err != nil {
				// A full queue is expected under this much concurrency; anything
				// else means Submit is broken and the test must say so.
				if !errors.Is(err, ErrQueueFull) {
					t.Errorf("Submit: %v", err)
				}
				return
			}
			mu.Lock()
			ids[j.ID] = struct{}{}
			submitted++
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Count what Submit actually accepted: asserting n here would contradict the
	// queue-full tolerance above, which returns without recording an ID.
	if len(ids) != submitted {
		t.Errorf("%d jobs submitted but only %d unique IDs (duplicates present)", submitted, len(ids))
	}
	if submitted == 0 {
		t.Fatal("every submission was refused; the test proved nothing")
	}
}

// TestCancelPendingJob cancels a job that is still pending in the queue
// (i.e. the worker goroutine has not yet started executing it).
// Verification: the job ends as canceled AND the job function is never called.
//
// Strategy: 1 worker + QueueSize=2.
//   - job1 blocks the single worker.
//   - job2 sits pending in the queue.
//   - CancelJob(job2) is called while job1 still holds the worker.
//   - Releasing job1 causes the worker to pick up job2, which
//     executeJob skips immediately because Status == Canceled.
func TestCancelPendingJob(t *testing.T) {
	jm := newTestManager(1, 2)
	defer jm.Shutdown(5 * time.Second)

	workerBusy := make(chan struct{})
	releaseWorker := make(chan struct{})

	// job1 blocks the single worker goroutine.
	_, err := jm.Submit("blocker", "blocks the worker", nil, func(ctx context.Context, job *Job) error {
		close(workerBusy) // signal: worker is busy with job1
		<-releaseWorker   // block until test says "go"
		return nil
	})
	if err != nil {
		t.Fatalf("Submit job1: %v", err)
	}

	// Wait until the worker has actually started job1.
	select {
	case <-workerBusy:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not pick up job1 in time")
	}

	// job2 enters the queue while the worker is busy → Status=pending.
	// fnCalled is written from the worker goroutine (if the fn ever runs) and
	// read from the test goroutine, so it must be accessed atomically.
	var fnCalled atomic.Bool
	j2, err := jm.Submit("target", "to be canceled while pending", nil, func(ctx context.Context, job *Job) error {
		fnCalled.Store(true)
		return nil
	})
	if err != nil {
		t.Fatalf("Submit job2: %v", err)
	}

	// Cancel job2 before the worker can pick it up.
	if err := jm.CancelJob(j2.ID); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	// Release the worker; it will finish job1 then process job2.
	close(releaseWorker)

	// executeJob closes j2.Done() even for pre-canceled jobs.
	select {
	case <-j2.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("job2 done channel was never closed after worker release")
	}

	if j2.Status != JobStatusCancelled {
		t.Errorf("expected status=%s, got %s", JobStatusCancelled, j2.Status)
	}
	if fnCalled.Load() {
		t.Error("job fn must not be called for a pre-canceled pending job")
	}
}

// TestCancelRunningJob cancels a job while it is actively executing.
// The job function blocks on ctx.Done() so it detects cancellation immediately.
func TestCancelRunningJob(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(5 * time.Second)

	jobRunning := make(chan struct{})

	j, err := jm.Submit("cancel.running", "cancel while running", nil, func(ctx context.Context, job *Job) error {
		close(jobRunning) // signal: we are now executing
		select {
		case <-ctx.Done():
			return ctx.Err() // propagate cancellation
		case <-time.After(10 * time.Second):
			return nil // safety valve; should never be reached
		}
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait until the job is actually running.
	select {
	case <-jobRunning:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not start running in time")
	}

	// Cancel the running job.
	if err := jm.CancelJob(j.ID); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	// The context cancellation should wake the job function.
	select {
	case <-j.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("job did not finish after cancellation")
	}

	if j.Status != JobStatusCancelled {
		t.Errorf("expected status=%s, got %s", JobStatusCancelled, j.Status)
	}
}

// TestShutdownDrain submits 5 jobs each sleeping 50 ms, then calls
// Shutdown(1s). Verifies that every job completes (not canceled) because
// the workers drain the queue and the sleep does not check ctx.Done().
func TestShutdownDrain(t *testing.T) {
	jm := newTestManager(2, 20)

	const n = 5
	jobs := make([]*Job, n)
	var submitErr error

	for i := range jobs {
		jobs[i], submitErr = jm.Submit("drain.test", "drain job", nil, func(ctx context.Context, job *Job) error {
			time.Sleep(50 * time.Millisecond)
			return nil
		})
		if submitErr != nil {
			t.Fatalf("Submit job %d: %v", i, submitErr)
		}
	}

	// 1 s timeout is ≫ 3 × 50 ms required for 5 jobs on 2 workers.
	jm.Shutdown(1 * time.Second)

	for i, j := range jobs {
		if j.Status != JobStatusCompleted {
			t.Errorf("job %d: expected status=%s, got %s", i, JobStatusCompleted, j.Status)
		}
	}
}

// TestSubmit_QueueFull verifies that submitting a job when the internal queue
// is at capacity returns an error whose message contains "queue".
//
// Strategy: 1 worker + QueueSize=1 → total capacity = 2 (1 in flight + 1 queued).
//   - job1 blocks the worker.
//   - job2 fills the queue slot.
//   - job3 should be rejected.
func TestSubmit_QueueFull(t *testing.T) {
	jm := newTestManager(1, 1)
	defer jm.Shutdown(5 * time.Second)

	workerBusy := make(chan struct{})
	releaseAll := make(chan struct{})

	// job1: occupies the single worker.
	_, err := jm.Submit("blocker1", "fill worker slot", nil, func(ctx context.Context, job *Job) error {
		close(workerBusy)
		<-releaseAll
		return nil
	})
	if err != nil {
		t.Fatalf("Submit job1: %v", err)
	}

	// Ensure the worker is actually busy before filling the queue.
	select {
	case <-workerBusy:
	case <-time.After(2 * time.Second):
		t.Fatal("worker not busy in time")
	}

	// job2: fills the single queue slot.
	_, err = jm.Submit("blocker2", "fill queue slot", nil, func(ctx context.Context, job *Job) error {
		<-releaseAll
		return nil
	})
	if err != nil {
		t.Fatalf("Submit job2: %v", err)
	}

	// job3: must be rejected — queue is full.
	_, err = jm.Submit("overflow", "should be rejected", nil, func(ctx context.Context, job *Job) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error when queue is full, got nil")
	}
	if !strings.Contains(err.Error(), "queue") {
		t.Errorf("expected 'queue' in error message, got: %v", err)
	}

	// Unblock everything so deferred Shutdown can drain cleanly.
	close(releaseAll)
}

// TestDefaultJobManagerConfig verifies DefaultJobManagerConfig returns sane defaults.
func TestDefaultJobManagerConfig(t *testing.T) {
	cfg := DefaultJobManagerConfig()
	if cfg.Workers <= 0 {
		t.Errorf("expected positive Workers, got %d", cfg.Workers)
	}
	if cfg.QueueSize <= 0 {
		t.Errorf("expected positive QueueSize, got %d", cfg.QueueSize)
	}
	if cfg.CleanupInterval <= 0 {
		t.Errorf("expected positive CleanupInterval, got %v", cfg.CleanupInterval)
	}
	if cfg.MaxJobAge <= 0 {
		t.Errorf("expected positive MaxJobAge, got %v", cfg.MaxJobAge)
	}
}

// TestJob_SetResult verifies SetResult stores the result map under a mutex.
func TestJob_SetResult(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(2 * time.Second)

	done := make(chan struct{})
	j, err := jm.Submit("result.test", "set result", nil, func(ctx context.Context, job *Job) error {
		job.SetResult(map[string]any{"key": "value", "count": 42})
		close(done)
		return nil
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	<-done
	<-j.Done()

	j.mu.RLock()
	result := j.Result
	j.mu.RUnlock()

	if result == nil {
		t.Fatal("expected Result to be set, got nil")
	}
	if result["key"] != "value" {
		t.Errorf("expected key=value, got %v", result["key"])
	}
}

// TestJob_SetError verifies SetError stores the error string under a mutex.

// TestGetJob_Found verifies GetJob returns the correct job by ID.
func TestGetJob_Found(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(2 * time.Second)

	j, err := jm.Submit("get.test", "get job", nil, func(ctx context.Context, job *Job) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	got, err := jm.GetJob(j.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.ID != j.ID {
		t.Errorf("expected ID=%s, got %s", j.ID, got.ID)
	}
}

// TestGetJob_NotFound verifies GetJob returns an error for unknown IDs.
func TestGetJob_NotFound(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(2 * time.Second)

	_, err := jm.GetJob("no-such-id")
	if err == nil {
		t.Fatal("expected error for missing job, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

// TestListJobs_NoFilter verifies ListJobs(nil) returns all jobs.
func TestListJobs_NoFilter(t *testing.T) {
	jm := newTestManager(2, 20)
	defer jm.Shutdown(2 * time.Second)

	const n = 3
	jobs := make([]*Job, n)
	for i := range jobs {
		var submitErr error
		jobs[i], submitErr = jm.Submit("list.test", "list job", nil, func(ctx context.Context, job *Job) error {
			return nil
		})
		if submitErr != nil {
			t.Fatalf("Submit %d: %v", i, submitErr)
		}
	}
	for _, j := range jobs {
		<-j.Done()
	}

	all := jm.ListJobs(nil)
	if len(all) < n {
		t.Errorf("expected at least %d jobs, got %d", n, len(all))
	}
}

// TestListJobs_FilterByStatus verifies ListJobs with a status filter returns only matching jobs.
func TestListJobs_FilterByStatus(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(2 * time.Second)

	j, err := jm.Submit("filter.test", "filter job", nil, func(ctx context.Context, job *Job) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-j.Done()

	completedStatus := JobStatusCompleted
	completed := jm.ListJobs(&completedStatus)
	for _, item := range completed {
		if item.Status != JobStatusCompleted {
			t.Errorf("ListJobs filter returned job with status=%s", item.Status)
		}
	}

	pendingStatus := JobStatusPending
	pending := jm.ListJobs(&pendingStatus)
	// after shutdown drain there should be zero pending
	if len(pending) != 0 {
		t.Errorf("expected 0 pending jobs, got %d", len(pending))
	}
}

// TestDeleteJob_Completed verifies that a completed job can be deleted.
func TestDeleteJob_Completed(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(2 * time.Second)

	j, err := jm.Submit("delete.test", "delete job", nil, func(ctx context.Context, job *Job) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-j.Done()

	if err := jm.DeleteJob(j.ID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}

	_, err = jm.GetJob(j.ID)
	if err == nil {
		t.Fatal("expected job to be gone after delete, but GetJob succeeded")
	}
}

// TestDeleteJob_NotFound verifies DeleteJob errors on unknown IDs.
func TestDeleteJob_NotFound(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(2 * time.Second)

	err := jm.DeleteJob("ghost-id")
	if err == nil {
		t.Fatal("expected error for missing job, got nil")
	}
}

// TestDeleteJob_ActiveReturnsError verifies that deleting a running job is rejected.
func TestDeleteJob_ActiveReturnsError(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(2 * time.Second)

	running := make(chan struct{})
	release := make(chan struct{})

	j, err := jm.Submit("delete.active", "active job", nil, func(ctx context.Context, job *Job) error {
		close(running)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	<-running
	err = jm.DeleteJob(j.ID)
	close(release)

	if err == nil {
		t.Fatal("expected error when deleting active job, got nil")
	}
	if !errors.Is(err, ErrJobActive) {
		t.Errorf("expected 'active' in error message, got: %v", err)
	}
}

// TestStats verifies Stats returns correct counts after job completion.
func TestStats(t *testing.T) {
	jm := newTestManager(2, 20)
	defer jm.Shutdown(2 * time.Second)

	// Submit one successful and one failing job.
	j1, _ := jm.Submit("stats.ok", "good job", nil, func(ctx context.Context, job *Job) error {
		return nil
	})
	j2, _ := jm.Submit("stats.fail", "bad job", nil, func(ctx context.Context, job *Job) error {
		return errors.New("intentional failure")
	})
	<-j1.Done()
	<-j2.Done()

	stats := jm.Stats()

	if stats[string(JobStatusCompleted)] < 1 {
		t.Errorf("expected ≥1 completed, got %d", stats[string(JobStatusCompleted)])
	}
	if stats[string(JobStatusFailed)] < 1 {
		t.Errorf("expected ≥1 failed, got %d", stats[string(JobStatusFailed)])
	}
	if stats["total"] < 2 {
		t.Errorf("expected total ≥2, got %d", stats["total"])
	}
}

// TestCancelJob_NotFound verifies CancelJob returns an error for unknown IDs.
func TestCancelJob_NotFound(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(2 * time.Second)

	err := jm.CancelJob("does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown job ID, got nil")
	}
}

// TestCancelJob_AlreadyCompleted verifies CancelJob rejects cancellation of finished jobs.
func TestCancelJob_AlreadyCompleted(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(2 * time.Second)

	j, err := jm.Submit("cancel.done", "completed job", nil, func(ctx context.Context, job *Job) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-j.Done()

	err = jm.CancelJob(j.ID)
	if err == nil {
		t.Fatal("expected error when canceling a completed job, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be canceled") {
		t.Errorf("expected 'cannot be canceled' in error, got: %v", err)
	}
}

// TestExecuteJob_Panic verifies that a panicking job function is recovered and
// the job is marked as failed with a "panic:" prefix in the error field.
func TestExecuteJob_Panic(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(2 * time.Second)

	j, err := jm.Submit("panic.test", "panicking job", nil, func(ctx context.Context, job *Job) error {
		panic("simulated panic")
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	<-j.Done()

	if j.Status != JobStatusFailed {
		t.Errorf("expected status=%s, got %s", JobStatusFailed, j.Status)
	}
	if !strings.HasPrefix(j.Error, "panic:") {
		t.Errorf("expected error to start with 'panic:', got: %q", j.Error)
	}
}

// TestCleanupOldJobs verifies that expired completed jobs are removed from the store.
func TestCleanupOldJobs(t *testing.T) {
	jm := newTestManager(1, 10)
	defer jm.Shutdown(2 * time.Second)

	j, err := jm.Submit("cleanup.test", "old job", nil, func(ctx context.Context, job *Job) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-j.Done()

	// Back-date CompletedAt so it appears ancient.
	past := time.Now().Add(-48 * time.Hour)
	j.mu.Lock()
	j.CompletedAt = &past
	j.mu.Unlock()

	cleaned := jm.cleanupOldJobs(24 * time.Hour)
	if cleaned == 0 {
		t.Error("expected at least one job to be cleaned up")
	}

	_, err = jm.GetJob(j.ID)
	if err == nil {
		t.Error("expected job to be removed after cleanup, but GetJob still found it")
	}
}

// TestUpdateProgress_Clamp covers the < 0 and > 1 clamp branches.
func TestUpdateProgress_Clamp(t *testing.T) {
	j := &Job{done: make(chan struct{})}

	j.UpdateProgress(-0.5, "below zero")
	if j.Progress != 0 {
		t.Errorf("expected 0 for negative input, got %v", j.Progress)
	}

	j.UpdateProgress(1.5, "above one")
	if j.Progress != 1 {
		t.Errorf("expected 1 for >1 input, got %v", j.Progress)
	}
}

// TestNewJobManager_Defaults covers the default-value branches in NewJobManager.
func TestNewJobManager_Defaults(t *testing.T) {
	jm := NewJobManager(JobManagerConfig{
		Workers:         0, // triggers default
		QueueSize:       0, // triggers default
		CleanupInterval: 0, // triggers default
		MaxJobAge:       0, // triggers default
	}, slog.Default())
	defer jm.Shutdown(5 * time.Second)

	if jm.workers != 4 {
		t.Errorf("expected default 4 workers, got %d", jm.workers)
	}
}

// TestConcurrentListAndStatusMutation exercises the C-1 data race: workers
// mutate job.Status under job.mu while ListJobs/Stats/DeleteJob read it.
// Run with -race; it must be clean.
func TestConcurrentListAndStatusMutation(t *testing.T) {
	jm := newTestManager(4, 200)
	defer jm.Shutdown(5 * time.Second)

	var wg sync.WaitGroup

	// Readers hammer ListJobs and Stats while jobs change state.
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = jm.ListJobs(nil)
					running := JobStatusRunning
					_ = jm.ListJobs(&running)
					_ = jm.Stats()
				}
			}
		}()
	}

	// Submitters create jobs that transition pending->running->completed.
	for i := 0; i < 100; i++ {
		_, err := jm.Submit("test", "race", nil, func(ctx context.Context, j *Job) error {
			j.UpdateProgress(0.5, "working")
			return nil
		})
		if err != nil {
			// queue full under load is acceptable; keep going
			continue
		}
	}

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}
