package job

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// TestUpdateHookFiresOnCompletion verifies the JobManager invokes its update
// hook with the terminal job state, so the API layer can persist completion
// (audit HIGH job_handlers.go:176: job state never persisted after creation).
func TestUpdateHookFiresOnCompletion(t *testing.T) {
	jm := NewJobManager(JobManagerConfig{Workers: 1, QueueSize: 4}, slog.Default())
	defer jm.Shutdown(2 * time.Second)

	var mu sync.Mutex
	var lastStatus JobStatus
	got := make(chan struct{}, 1)
	jm.SetUpdateHook(func(j *Job) {
		mu.Lock()
		lastStatus = j.Snapshot().Status
		mu.Unlock()
		select {
		case got <- struct{}{}:
		default:
		}
	})

	if _, err := jm.Submit("test", "hook", nil, func(ctx context.Context, j *Job) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("update hook was not invoked on completion")
	}
	mu.Lock()
	defer mu.Unlock()
	if lastStatus != JobStatusCompleted {
		t.Errorf("hook saw status %q, want %q", lastStatus, JobStatusCompleted)
	}
}
