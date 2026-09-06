package client

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
)

// SnapshotInfo represents snapshot information
type SnapshotInfo struct {
	Name      string    `json:"Name"`
	CreatedAt time.Time `json:"CreatedAt"`
	SizeMB    int64     `json:"SizeMB"`
}

// ListSnapshots lists snapshots for an instance
func (c *Client) ListSnapshots(ctx context.Context, instanceID string) ([]SnapshotInfo, error) {
	var result struct {
		Snapshots []SnapshotInfo `json:"snapshots"`
	}

	path := fmt.Sprintf("/api/v1/instances/%s/snapshots", url.PathEscape(instanceID))
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}

	return result.Snapshots, nil
}

// CreateSnapshot creates a new snapshot
func (c *Client) CreateSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	body := map[string]string{"name": snapshotName}
	path := fmt.Sprintf("/api/v1/instances/%s/snapshots", url.PathEscape(instanceID))
	return c.doRequest(ctx, "POST", path, body, nil)
}

// DeleteSnapshot deletes a snapshot
func (c *Client) DeleteSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/snapshots/%s",
		url.PathEscape(instanceID), url.PathEscape(snapshotName))
	return c.doRequest(ctx, "DELETE", path, nil, nil)
}

// RestoreSnapshot restores an instance to a snapshot
func (c *Client) RestoreSnapshot(ctx context.Context, instanceID, snapshotName string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/snapshots/%s/restore",
		url.PathEscape(instanceID), url.PathEscape(snapshotName))
	return c.doRequest(ctx, "POST", path, nil, nil)
}

// Snapshot creates a new snapshot for an instance
func (c *Client) Snapshot(ctx context.Context, instanceID string) error {
	timestamp := time.Now().Format("20060102-150405")
	snapshotName := "hospitus-manual-" + timestamp
	return c.CreateSnapshot(ctx, instanceID, snapshotName)
}

// CloneOptions contains options for cloning operations
type CloneOptions struct {
	Name        string            `json:"name"`
	Linked      bool              `json:"linked"`
	CPUs        int               `json:"cpus,omitempty"`
	MemoryMB    int64             `json:"memory_mb,omitempty"`
	ResetMAC    bool              `json:"reset_mac"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// CloneResult contains the result of a clone operation
type CloneResult struct {
	SourceInstance string              `json:"source_instance"`
	SourceSnapshot string              `json:"source_snapshot,omitempty"`
	CloneInstance  string              `json:"clone_instance"`
	Clone          *datastore.Instance `json:"clone"`
	Linked         bool                `json:"linked"`
	Message        string              `json:"message"`
}

// CloneInstance creates a clone from an existing instance
func (c *Client) CloneInstance(ctx context.Context, instanceID string, opts CloneOptions) (*CloneResult, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/clone", url.PathEscape(instanceID))

	var result CloneResult
	if err := c.doRequest(ctx, "POST", path, opts, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// CloneFromSnapshot creates a clone from a snapshot
func (c *Client) CloneFromSnapshot(ctx context.Context, instanceID, snapshotName string, opts CloneOptions) (*CloneResult, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/snapshots/%s/clone",
		url.PathEscape(instanceID), url.PathEscape(snapshotName))

	var result CloneResult
	if err := c.doRequest(ctx, "POST", path, opts, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// CheckpointInfo represents a bhyve checkpoint.
type CheckpointInfo struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// ListCheckpoints lists checkpoints for an instance.
func (c *Client) ListCheckpoints(ctx context.Context, instanceID string) ([]CheckpointInfo, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/checkpoints", url.PathEscape(instanceID))
	// The server wraps the list: {"instance": ..., "checkpoints": [...], "count": n}.
	var result struct {
		Checkpoints []CheckpointInfo `json:"checkpoints"`
	}
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}
	return result.Checkpoints, nil
}

// CreateCheckpoint creates a bhyve checkpoint.
func (c *Client) CreateCheckpoint(ctx context.Context, instanceID, name string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/checkpoint", url.PathEscape(instanceID))
	return c.doRequest(ctx, "POST", path, map[string]string{"name": name}, nil)
}

// RestoreCheckpoint restores from a bhyve checkpoint.
func (c *Client) RestoreCheckpoint(ctx context.Context, instanceID, name string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/checkpoint/%s/restore",
		url.PathEscape(instanceID), url.PathEscape(name))
	return c.doRequest(ctx, "POST", path, nil, nil)
}

// DeleteCheckpoint deletes a bhyve checkpoint.
func (c *Client) DeleteCheckpoint(ctx context.Context, instanceID, name string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/checkpoint/%s",
		url.PathEscape(instanceID), url.PathEscape(name))
	return c.doRequest(ctx, "DELETE", path, nil, nil)
}
