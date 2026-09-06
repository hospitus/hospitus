package job

import (
	"sync"
	"testing"
)

// TestJobSnapshotRaceFree verifies Snapshot reads a job's fields under its lock
// so persistence (datastore.UpdateJob) does not race with executeJob mutating
// the same job. Run with -race (audit HIGH datastore/jobs.go:214).
func TestJobSnapshotRaceFree(t *testing.T) {
	j := &Job{ID: "1", Status: JobStatusRunning, done: make(chan struct{})}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			j.UpdateProgress(float64(i)/1000, "working")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			rec := j.Snapshot()
			_ = rec.Progress
			_ = rec.Message
		}
	}()
	wg.Wait()
}
