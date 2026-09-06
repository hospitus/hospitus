package datastore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hospitus/hospitus/pkg/job"
)

// CreateJob inserts a new job record into the database.
func (ds *Datastore) CreateJob(ctx context.Context, j *job.Job) error {
	// Read a consistent, lock-free snapshot of the job's fields. A submitted job
	// may already be running in a worker by the time it is persisted, so reading
	// the live *job.Job here races with its execution goroutine.
	rec := j.Snapshot()

	metadataJSON := "{}"
	if len(rec.Metadata) > 0 {
		data, err := json.Marshal(rec.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal job metadata: %w", err)
		}
		metadataJSON = string(data)
	}

	query := `
		INSERT INTO jobs (id, type, description, status, progress, message, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := ds.db.ExecContext(ctx, query,
		rec.ID,
		rec.Type,
		rec.Description,
		string(rec.Status),
		rec.Progress,
		rec.Message,
		metadataJSON,
		rec.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create job %s: %w", rec.ID, err)
	}
	return nil
}

// GetJob retrieves a job by ID.
func (ds *Datastore) GetJob(ctx context.Context, id string) (*job.Job, error) {
	query := `
		SELECT id, type, description, status, progress, message, result, error, metadata,
		       created_at, started_at, completed_at
		FROM jobs
		WHERE id = ?
	`
	var j job.Job
	var statusStr, metadataJSON string
	var resultJSON, errorStr sql.NullString
	var startedAt, completedAt sql.NullTime

	err := ds.db.QueryRowContext(ctx, query, id).Scan(
		&j.ID,
		&j.Type,
		&j.Description,
		&statusStr,
		&j.Progress,
		&j.Message,
		&resultJSON,
		&errorStr,
		&metadataJSON,
		&j.CreatedAt,
		&startedAt,
		&completedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get job %s: %w", id, err)
	}

	j.Status = job.JobStatus(statusStr)

	if resultJSON.Valid {
		if err := json.Unmarshal([]byte(resultJSON.String), &j.Result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal job result: %w", err)
		}
	}
	if errorStr.Valid {
		j.Error = errorStr.String
	}
	if metadataJSON != "" && metadataJSON != "null" {
		if err := json.Unmarshal([]byte(metadataJSON), &j.Metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal job metadata: %w", err)
		}
	}
	if startedAt.Valid {
		j.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		j.CompletedAt = &completedAt.Time
	}

	return &j, nil
}

// ListJobs retrieves all jobs, optionally filtered by status.
func (ds *Datastore) ListJobs(ctx context.Context, status *job.JobStatus) ([]*job.Job, error) {
	var query string
	var args []interface{}

	if status != nil {
		query = `
			SELECT id, type, description, status, progress, message, result, error, metadata,
			       created_at, started_at, completed_at
			FROM jobs
			WHERE status = ?
			ORDER BY created_at DESC
		`
		args = append(args, string(*status))
	} else {
		query = `
			SELECT id, type, description, status, progress, message, result, error, metadata,
			       created_at, started_at, completed_at
			FROM jobs
			ORDER BY created_at DESC
		`
	}

	rows, err := ds.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*job.Job
	for rows.Next() {
		var j job.Job
		var statusStr, metadataJSON string
		var resultJSON, errorStr sql.NullString
		var startedAt, completedAt sql.NullTime

		if err := rows.Scan(
			&j.ID,
			&j.Type,
			&j.Description,
			&statusStr,
			&j.Progress,
			&j.Message,
			&resultJSON,
			&errorStr,
			&metadataJSON,
			&j.CreatedAt,
			&startedAt,
			&completedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan job row: %w", err)
		}

		j.Status = job.JobStatus(statusStr)

		if resultJSON.Valid {
			if err := json.Unmarshal([]byte(resultJSON.String), &j.Result); err != nil {
				return nil, fmt.Errorf("job %s has an unreadable result column: %w", j.ID, err)
			}
		}
		if errorStr.Valid {
			j.Error = errorStr.String
		}
		if metadataJSON != "" && metadataJSON != "null" {
			if err := json.Unmarshal([]byte(metadataJSON), &j.Metadata); err != nil {
				return nil, fmt.Errorf("job %s has an unreadable metadata column: %w", j.ID, err)
			}
		}
		if startedAt.Valid {
			j.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			j.CompletedAt = &completedAt.Time
		}

		jobs = append(jobs, &j)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating job rows: %w", err)
	}

	return jobs, nil
}

// UpdateJob persists the current state of a job to the database.
func (ds *Datastore) UpdateJob(ctx context.Context, j *job.Job) error {
	// Read a consistent, lock-free snapshot of the job's fields; reading the
	// live *job.Job directly races with the job's execution goroutine.
	rec := j.Snapshot()

	resultJSON := sql.NullString{}
	if rec.Result != nil {
		data, err := json.Marshal(rec.Result)
		if err != nil {
			return fmt.Errorf("failed to marshal job result: %w", err)
		}
		resultJSON.String = string(data)
		resultJSON.Valid = true
	}

	errorStr := sql.NullString{String: rec.Error, Valid: rec.Error != ""}

	metadataJSON := "{}"
	if len(rec.Metadata) > 0 {
		data, err := json.Marshal(rec.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal job metadata: %w", err)
		}
		metadataJSON = string(data)
	}

	query := `
		UPDATE jobs
		SET status = ?, progress = ?, message = ?, result = ?, error = ?,
		    metadata = ?, started_at = ?, completed_at = ?
		WHERE id = ?
	`
	result, err := ds.db.ExecContext(ctx, query,
		string(rec.Status),
		rec.Progress,
		rec.Message,
		resultJSON,
		errorStr,
		metadataJSON,
		rec.StartedAt,
		rec.CompletedAt,
		rec.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update job %s: %w", rec.ID, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check job update result: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("job %s not found", rec.ID)
	}

	return nil
}

// DeleteJob removes a job from the database.
func (ds *Datastore) DeleteJob(ctx context.Context, id string) error {
	query := `DELETE FROM jobs WHERE id = ?`
	result, err := ds.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete job %s: %w", id, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check job delete result: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("job %s not found", id)
	}

	return nil
}

// FailInterruptedJobs closes out the jobs a previous daemon left behind.
//
// A job lives in the worker pool's memory; the row only records where it got
// to. The pool goes with the daemon, so every pending or running row is left
// claiming work nobody is doing, and GET /jobs kept reporting it as running.
func (ds *Datastore) FailInterruptedJobs(ctx context.Context) (int, error) {
	query := `
		UPDATE jobs
		SET status = 'failed',
		    error = 'interrupted: the daemon stopped while this job was running',
		    completed_at = ?
		WHERE status IN ('pending', 'running')
	`
	result, err := ds.db.ExecContext(ctx, query, time.Now())
	if err != nil {
		return 0, fmt.Errorf("failed to close out interrupted jobs: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to check interrupted job count: %w", err)
	}
	return int(rows), nil
}

// CleanupOldJobs removes completed, failed, or canceled jobs older than maxAge.
func (ds *Datastore) CleanupOldJobs(ctx context.Context, maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge)

	query := `
		DELETE FROM jobs
		WHERE status IN ('completed', 'failed', 'canceled')
		  AND completed_at IS NOT NULL
		  AND completed_at < ?
	`
	result, err := ds.db.ExecContext(ctx, query, cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup old jobs: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to check cleanup result: %w", err)
	}

	return int(rows), nil
}
