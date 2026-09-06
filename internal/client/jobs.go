package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hospitus/hospitus/pkg/job"
)

// JobListResponse is the response from GET /api/v1/jobs.
type JobListResponse struct {
	Jobs  []*job.Job `json:"jobs"`
	Count int        `json:"count"`
}

// ListJobs returns all jobs, optionally filtered by status.
// status may be "pending", "running", "completed", "failed", "canceled", or "" for all.
func (c *Client) ListJobs(ctx context.Context, status string) (*JobListResponse, error) {
	path := "/api/v1/jobs"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}

	var resp JobListResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	return &resp, nil
}

// GetJob returns the status of a specific job by ID.
func (c *Client) GetJob(ctx context.Context, id string) (*job.Job, error) {
	var j job.Job
	if err := c.doRequest(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(id), nil, &j); err != nil {
		return nil, fmt.Errorf("get job %s: %w", id, err)
	}

	return &j, nil
}

// CancelJob cancels a pending or running job.
func (c *Client) CancelJob(ctx context.Context, id string) error {
	if err := c.doRequest(ctx, http.MethodPost, "/api/v1/jobs/"+url.PathEscape(id)+"/cancel", nil, nil); err != nil {
		return fmt.Errorf("cancel job %s: %w", id, err)
	}

	return nil
}

// DeleteJob removes a completed, failed, or canceled job.
func (c *Client) DeleteJob(ctx context.Context, id string) error {
	if err := c.doRequest(ctx, http.MethodDelete, "/api/v1/jobs/"+url.PathEscape(id), nil, nil); err != nil {
		return fmt.Errorf("delete job %s: %w", id, err)
	}

	return nil
}

// JobStats returns job manager statistics (counts by status).
func (c *Client) JobStats(ctx context.Context) (map[string]int, error) {
	var stats map[string]int
	if err := c.doRequest(ctx, http.MethodGet, "/api/v1/jobs/stats", nil, &stats); err != nil {
		return nil, fmt.Errorf("job stats: %w", err)
	}

	return stats, nil
}
