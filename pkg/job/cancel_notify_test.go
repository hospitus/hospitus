package job

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestCancelNotifiesTheUpdateHook covers what mirrors a job into the datastore.
//
// The hook is documented as firing when a job reaches a terminal state, and
// cancellation is one. Without it a canceled job stays recorded as running for
// as long as its row survives.
func TestCancelNotifiesTheUpdateHook(t *testing.T) {
	jm := NewJobManager(JobManagerConfig{Workers: 1, QueueSize: 4}, nil)
	defer jm.Shutdown(2 * time.Second)

	var mu sync.Mutex
	var seen []JobStatus
	jm.SetUpdateHook(func(j *Job) {
		// The hook gets the live job, and executeJob still holds its lock to
		// write other fields; read through Snapshot rather than off the struct.
		status := j.Snapshot().Status
		mu.Lock()
		seen = append(seen, status)
		mu.Unlock()
	})

	started := make(chan struct{})
	release := make(chan struct{})
	job, err := jm.Submit("test.cancel", "a job that waits", nil, func(ctx context.Context, j *Job) error {
		close(started)
		<-release
		return ctx.Err()
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the job never started")
	}

	if err := jm.CancelJob(job.ID); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := append([]JobStatus(nil), seen...)
		mu.Unlock()
		for _, s := range got {
			if s == JobStatusCancelled {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Errorf("the hook never saw the cancellation; it saw %v", seen)
}

// TestManagerErrorsAreMatchable covers the sentinels a caller needs to tell one
// refusal from another without reading the message.
func TestManagerErrorsAreMatchable(t *testing.T) {
	jm := NewJobManager(JobManagerConfig{Workers: 1, QueueSize: 1}, nil)
	defer jm.Shutdown(time.Second)

	if err := jm.CancelJob("no-such-job"); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("CancelJob on a missing job = %v, want ErrJobNotFound", err)
	}
}

// TestShutdownGivesRunningJobsTheirGracePeriod covers what Shutdown promises: a
// running job gets the timeout to finish.
//
// The existing drain test passes either way, because its job function ignores
// the context and just sleeps. This one honors the context, so it fails if
// Shutdown cancels before waiting.
func TestShutdownIsGracefulForContextAwareJobs(t *testing.T) {
	jm := NewJobManager(JobManagerConfig{Workers: 1, QueueSize: 4}, nil)

	started := make(chan struct{})
	job, err := jm.Submit("test.graceful", "a job that watches its context", nil,
		func(ctx context.Context, j *Job) error {
			close(started)
			select {
			case <-time.After(150 * time.Millisecond):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the job never started")
	}

	jm.Shutdown(5 * time.Second)

	if got := job.Snapshot().Status; got != JobStatusCompleted {
		t.Errorf("job ended as %s, want %s: Shutdown canceled it instead of waiting",
			got, JobStatusCompleted)
	}
}
