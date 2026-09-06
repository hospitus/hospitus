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
		mu.Lock()
		seen = append(seen, j.Status)
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
