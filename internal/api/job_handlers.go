package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hospitus/hospitus/pkg/job"
)

// handleJobs handles job listing and creation.
//
// GET  /api/v1/jobs          - List all jobs (optional ?status= filter)
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListJobs(w, r)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleJobDetail handles operations on a specific job.
//
// GET    /api/v1/jobs/{id}          - Get job status
// POST   /api/v1/jobs/{id}/cancel   - Cancel a running job
// DELETE /api/v1/jobs/{id}          - Delete a completed/failed job
func (s *Server) handleJobDetail(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		s.writeError(w, http.StatusBadRequest, "Job ID is required")
		return
	}

	// Check for sub-actions: /api/v1/jobs/{id}/cancel
	if strings.HasSuffix(r.URL.Path, "/cancel") {
		if r.Method != http.MethodPost {
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.handleCancelJob(w, r, jobID)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGetJob(w, r, jobID)
	case http.MethodDelete:
		s.handleDeleteJob(w, r, jobID)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleListJobs returns a list of jobs, optionally filtered by status.
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Check optional status filter
	statusParam := r.URL.Query().Get("status")
	var statusFilter *job.JobStatus
	if statusParam != "" {
		st := job.JobStatus(statusParam)
		statusFilter = &st
	}

	// The two sources are merged, not chosen between. Falling back only on an
	// error or an empty result meant a running job that had already been
	// written to the datastore was listed from its stale persisted row, and a
	// job still only in memory disappeared as soon as the datastore held one
	// row of its own.
	//
	// Marshal lock-free snapshots, never live *job.Job values: an in-memory
	// job's fields are mutated by its worker under the job's mutex, and
	// encoding the live struct races with it.
	byID := make(map[string]job.JobRecord)
	order := make([]string, 0)

	persisted, err := s.datastore.ListJobs(ctx, statusFilter)
	if err != nil {
		s.logger.Warn("Failed to list jobs from datastore; reporting the in-memory ones", "error", err)
	}
	for _, j := range persisted {
		record := j.Snapshot()
		if _, seen := byID[record.ID]; !seen {
			order = append(order, record.ID)
		}
		byID[record.ID] = record
	}

	// Unfiltered: asking the manager for the caller's status hid exactly the
	// jobs the merge exists for. A job the datastore still has as "running"
	// and the manager knows has finished was not returned by a filtered
	// ListJobs, so the stale row survived and was reported as running. The
	// filter below decides, on the live status.
	for _, j := range s.jobManager.ListJobs(nil) {
		record := j.Snapshot()
		if _, seen := byID[record.ID]; !seen {
			order = append(order, record.ID)
		}
		byID[record.ID] = record
	}

	records := make([]job.JobRecord, 0, len(order))
	for _, id := range order {
		record := byID[id]
		// Filtered again on the merged record: a job the datastore still has
		// as "running" may have finished, and the live snapshot that replaced
		// it no longer matches the status the caller asked for.
		if statusFilter != nil && record.Status != *statusFilter {
			continue
		}
		records = append(records, record)
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"jobs":  records,
		"count": len(records),
	})
}

// handleGetJob returns the status of a specific job.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request, jobID string) {
	ctx := r.Context()

	// The in-memory manager holds the live status and progress; the datastore
	// only records the submit-time and terminal snapshots. Consult the manager
	// first and keep the datastore as the fallback for jobs that predate a
	// daemon restart. Marshal a Snapshot(), not the live *job.Job, so encoding
	// does not race with the worker mutating the job.
	if j, err := s.jobManager.GetJob(jobID); err == nil {
		s.writeJSON(w, http.StatusOK, j.Snapshot())
		return
	}

	j, err := s.datastore.GetJob(ctx, jobID)
	if err != nil || j == nil {
		if err == nil {
			err = fmt.Errorf("job not found: %s", jobID)
		}
		s.writeLoggedError(w, http.StatusNotFound, "Job not found", err)
		return
	}

	s.writeJSON(w, http.StatusOK, j.Snapshot())
}

// handleCancelJob cancels a pending or running job.
func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request, jobID string) {
	ctx := r.Context()

	// Distinguish an unknown job (404) from one that exists but cannot be
	// canceled (409). Only the in-memory manager can cancel; a job found only
	// in the datastore predates a daemon restart and is no longer running.
	j, err := s.jobManager.GetJob(jobID)
	if err != nil {
		if stored, dsErr := s.datastore.GetJob(ctx, jobID); dsErr != nil || stored == nil {
			s.writeLoggedError(w, http.StatusNotFound, "Job not found", err)
			return
		}
		s.writeError(w, http.StatusConflict, "Job is not running")
		return
	}

	if err := s.jobManager.CancelJob(jobID); err != nil {
		s.writeLoggedError(w, http.StatusConflict, "Failed to cancel job", err)
		return
	}

	// Persist the cancellation
	if updateErr := s.datastore.UpdateJob(ctx, j); updateErr != nil {
		s.logger.Warn("Failed to persist job cancellation", "job_id", jobID, "error", updateErr)
	}

	s.writeJSON(w, http.StatusOK, map[string]string{
		"message": "Job canceled successfully",
		"job_id":  jobID,
	})
}

// handleDeleteJob removes a completed, failed, or canceled job.
func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request, jobID string) {
	ctx := r.Context()

	if err := s.datastore.DeleteJob(ctx, jobID); err != nil {
		// Also try in-memory. When that also fails, the job is not deletable;
		// surface both failures so the log reflects the in-memory failure (the
		// one that actually determined the 404), not only the datastore error.
		if memErr := s.jobManager.DeleteJob(jobID); memErr != nil {
			s.writeLoggedError(w, http.StatusNotFound, "Job not found or cannot be deleted",
				fmt.Errorf("datastore delete failed: %w; in-memory delete failed: %w", err, memErr))
			return
		}
	} else {
		// Also remove from in-memory if present
		_ = s.jobManager.DeleteJob(jobID)
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleJobStats returns job manager statistics.
func (s *Server) handleJobStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	stats := s.jobManager.Stats()
	s.writeJSON(w, http.StatusOK, stats)
}

// submitJob is a helper to submit a job and persist it to the datastore.
func (s *Server) submitJob(ctx context.Context, jobType, description string, metadata map[string]string, fn job.JobFunc) (*job.Job, error) {
	j, err := s.jobManager.Submit(jobType, description, metadata, fn)
	if err != nil {
		return nil, err
	}

	// Persist initial job state
	if persistErr := s.datastore.CreateJob(ctx, j); persistErr != nil {
		s.logger.Warn("Failed to persist job to datastore", "job_id", j.ID, "error", persistErr)
	}

	return j, nil
}

// updateJob is a helper to persist job state changes.
func (s *Server) updateJob(ctx context.Context, j *job.Job) {
	if err := s.datastore.UpdateJob(ctx, j); err != nil {
		s.logger.Warn("Failed to update job in datastore", "job_id", j.ID, "error", err)
	}
}
