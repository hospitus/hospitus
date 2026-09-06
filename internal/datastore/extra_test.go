package datastore

import (
	"context"
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/job"
)

// --- Job persistence tests ---

func makeTestJob(id string) *job.Job {
	return &job.Job{
		ID:          id,
		Type:        "test.op",
		Description: "test job " + id,
		Status:      job.JobStatusPending,
		Progress:    0.0,
		Message:     "queued",
		CreatedAt:   time.Now(),
		Metadata:    map[string]string{"key": "val"},
	}
}

func TestJobCRUD(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("NewDatastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	// GetJob on non-existent returns nil, nil (not an error)
	missing, err := ds.GetJob(ctx, "no-such-job")
	if err != nil {
		t.Fatalf("GetJob(missing): unexpected error: %v", err)
	}
	if missing != nil {
		t.Error("expected nil job for missing ID")
	}

	j := makeTestJob("job-1")
	if err := ds.CreateJob(ctx, j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// GetJob
	got, err := ds.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.ID != "job-1" || got.Type != "test.op" {
		t.Errorf("unexpected job: %+v", got)
	}

	// ListJobs (no filter)
	all, err := ds.ListJobs(ctx, nil)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 job, got %d", len(all))
	}

	// ListJobs with status filter
	st := job.JobStatusPending
	filtered, err := ds.ListJobs(ctx, &st)
	if err != nil {
		t.Fatalf("ListJobs(pending): %v", err)
	}
	if len(filtered) != 1 {
		t.Errorf("expected 1 pending job, got %d", len(filtered))
	}

	runStatus := job.JobStatusRunning
	running, err := ds.ListJobs(ctx, &runStatus)
	if err != nil {
		t.Fatalf("ListJobs(running): %v", err)
	}
	if len(running) != 0 {
		t.Errorf("expected 0 running jobs, got %d", len(running))
	}

	// UpdateJob
	j.Status = job.JobStatusRunning
	j.Progress = 0.5
	j.Message = "in progress"
	now := time.Now()
	j.StartedAt = &now
	if err := ds.UpdateJob(ctx, j); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}

	updated, err := ds.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob after update: %v", err)
	}
	if updated.Status != job.JobStatusRunning {
		t.Errorf("expected running status, got %s", updated.Status)
	}

	// DeleteJob — deleting non-existent returns error
	if err := ds.DeleteJob(ctx, "job-1"); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	// Second delete of same ID should return error
	if err := ds.DeleteJob(ctx, "job-1"); err == nil {
		t.Error("expected error deleting non-existent job")
	}
	// GetJob after delete returns nil, nil
	after, err := ds.GetJob(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetJob after delete: unexpected error: %v", err)
	}
	if after != nil {
		t.Error("expected nil job after delete")
	}
}

func TestCleanupOldJobs(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("NewDatastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	// Create a completed job with old timestamp
	j := makeTestJob("old-job")
	j.Status = job.JobStatusCompleted
	past := time.Now().Add(-48 * time.Hour)
	j.CreatedAt = past
	completed := past.Add(time.Minute)
	j.CompletedAt = &completed
	if err := ds.CreateJob(ctx, j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	// update to set completed_at
	if err := ds.UpdateJob(ctx, j); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}

	// Recent pending job should not be cleaned up
	j2 := makeTestJob("new-job")
	if err := ds.CreateJob(ctx, j2); err != nil {
		t.Fatalf("CreateJob j2: %v", err)
	}

	deleted, err := ds.CleanupOldJobs(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldJobs: %v", err)
	}
	// old-job should be deleted (it's completed and > 24h old)
	if deleted < 1 {
		t.Errorf("expected at least 1 deleted job, got %d", deleted)
	}

	// new-job (pending) should still exist
	_, err = ds.GetJob(ctx, "new-job")
	if err != nil {
		t.Errorf("new-job should still exist: %v", err)
	}
}

// TestCleanupOldJobsRemovesCanceledJobs covers a terminal status the sweep
// skipped: the query named 'cancelled' while the job package writes "canceled",
// so canceled jobs accumulated for ever.
func TestCleanupOldJobsRemovesCanceledJobs(t *testing.T) {
	ds, err := NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatalf("NewDatastore: %v", err)
	}
	defer ds.Close()

	ctx := context.Background()

	j := makeTestJob("canceled-job")
	j.Status = job.JobStatusCancelled
	past := time.Now().Add(-48 * time.Hour)
	j.CreatedAt = past
	completed := past.Add(time.Minute)
	j.CompletedAt = &completed
	if err := ds.CreateJob(ctx, j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := ds.UpdateJob(ctx, j); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}

	deleted, err := ds.CleanupOldJobs(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldJobs: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted %d jobs, want the canceled one gone", deleted)
	}
}
