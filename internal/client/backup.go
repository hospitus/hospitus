package client

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// BackupInfo represents a backup entry returned by the API.
type BackupInfo struct {
	ID           string    `json:"id"`
	InstanceID   string    `json:"instance_id"`
	Type         string    `json:"type"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
	SizeBytes    int64     `json:"size"`
	SnapshotName string    `json:"snapshot_name"`
	Destination  string    `json:"destination,omitempty"`
	Compressed   bool      `json:"compressed"`
	Encrypted    bool      `json:"encrypted"`
	Error        string    `json:"error,omitempty"`
	Verified     bool      `json:"verified"`
}

// ListBackups lists all backups, optionally filtered by instanceID (empty = all).
func (c *Client) ListBackups(ctx context.Context, instanceID string) ([]BackupInfo, error) {
	var result []BackupInfo
	var path string
	if instanceID != "" {
		path = fmt.Sprintf("/api/v1/instances/%s/backups", url.PathEscape(instanceID))
	} else {
		path = "/api/v1/backups"
	}
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetBackup returns details of a specific backup.
func (c *Client) GetBackup(ctx context.Context, backupID string) (*BackupInfo, error) {
	var result BackupInfo
	path := fmt.Sprintf("/api/v1/backups/%s", url.PathEscape(backupID))
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreateBackup triggers a new backup for an instance.
func (c *Client) CreateBackup(ctx context.Context, instanceID, backupType string) (*BackupInfo, error) {
	body := map[string]string{"type": backupType}
	var result BackupInfo
	path := fmt.Sprintf("/api/v1/instances/%s/backups", url.PathEscape(instanceID))
	if err := c.doRequest(ctx, "POST", path, body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// DeleteBackup deletes a backup by ID.
func (c *Client) DeleteBackup(ctx context.Context, backupID string) error {
	path := fmt.Sprintf("/api/v1/backups/%s", url.PathEscape(backupID))
	return c.doRequest(ctx, "DELETE", path, nil, nil)
}

// RestoreBackup restores a backup, optionally to a different instance.
func (c *Client) RestoreBackup(ctx context.Context, backupID, targetInstanceID string) error {
	body := map[string]string{}
	if targetInstanceID != "" {
		body["target_instance_id"] = targetInstanceID
	}
	path := fmt.Sprintf("/api/v1/backups/%s/restore", url.PathEscape(backupID))
	return c.doRequest(ctx, "POST", path, body, nil)
}

// VerifyBackup runs an integrity check on a stored backup.
func (c *Client) VerifyBackup(ctx context.Context, backupID string) error {
	path := fmt.Sprintf("/api/v1/backups/%s/verify", url.PathEscape(backupID))
	return c.doRequest(ctx, "POST", path, nil, nil)
}

// BackupConfigRequest configures automatic and full/incremental backups for one
// instance.
//
// A full or incremental backup needs a destination: a local directory the daemon
// writes the stream into, or host:path for a remote pool reached over ssh.
type BackupConfigRequest struct {
	Enabled     bool   `json:"enabled"`
	Schedule    string `json:"schedule,omitempty"`
	Destination string `json:"destination,omitempty"`
	Compression string `json:"compression,omitempty"`
	Retention   struct {
		KeepLast    int `json:"keep_last,omitempty"`
		KeepHourly  int `json:"keep_hourly,omitempty"`
		KeepDaily   int `json:"keep_daily,omitempty"`
		KeepWeekly  int `json:"keep_weekly,omitempty"`
		KeepMonthly int `json:"keep_monthly,omitempty"`
	} `json:"retention,omitempty"`
}

// SetBackupConfig writes the backup configuration of an instance.
func (c *Client) SetBackupConfig(ctx context.Context, instanceID string, req BackupConfigRequest) error {
	path := fmt.Sprintf("/api/v1/instances/%s/backups/config", instanceID)
	return c.doRequest(ctx, "PUT", path, req, nil)
}

// GetBackupConfig reads the backup configuration of an instance.
func (c *Client) GetBackupConfig(ctx context.Context, instanceID string) (map[string]interface{}, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/backups/config", instanceID)
	var config map[string]interface{}
	if err := c.doRequest(ctx, "GET", path, nil, &config); err != nil {
		return nil, err
	}
	return config, nil
}
