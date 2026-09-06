package client

import (
	"context"
	"fmt"
	"net/url"
)

// CreateVolumeRequest contains parameters for creating a volume
type CreateVolumeRequest struct {
	Name        string `json:"name"`
	Size        string `json:"size,omitempty"`        // e.g., "10G", "500M"
	Quota       string `json:"quota,omitempty"`       // ZFS quota
	Reservation string `json:"reservation,omitempty"` // ZFS reservation
	Compression string `json:"compression,omitempty"` // lz4, zstd, gzip, off
	Description string `json:"description,omitempty"`
}

// VolumeInfo contains information about a volume
type VolumeInfo struct {
	Name string `json:"name"`
	// Size is the volume's refquota — the space its own data may occupy.
	Size int64 `json:"size,omitempty"`
	// Bytes, as the daemon reports them. A string here decodes nothing: every
	// response fails with "cannot unmarshal number into ... of type string".
	Used        int64  `json:"used"`
	Available   int64  `json:"available"`
	Quota       int64  `json:"quota,omitempty"`
	Reservation int64  `json:"reservation,omitempty"`
	Compression string `json:"compression,omitempty"`
	MountPoint  string `json:"mountpoint,omitempty"`
	Dataset     string `json:"dataset,omitempty"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// CreateVolume creates a new volume
func (c *Client) CreateVolume(ctx context.Context, req CreateVolumeRequest) (*VolumeInfo, error) {
	var result VolumeInfo
	if err := c.doRequest(ctx, "POST", "/api/v1/volumes", req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// DeleteVolume deletes a volume
func (c *Client) DeleteVolume(ctx context.Context, volumeName string) error {
	path := fmt.Sprintf("/api/v1/volumes/%s", url.PathEscape(volumeName))
	return c.doRequest(ctx, "DELETE", path, nil, nil)
}

// ListVolumes lists all volumes
func (c *Client) ListVolumes(ctx context.Context) ([]VolumeInfo, error) {
	var result []VolumeInfo
	if err := c.doRequest(ctx, "GET", "/api/v1/volumes", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetVolume gets information about a specific volume
func (c *Client) GetVolume(ctx context.Context, volumeName string) (*VolumeInfo, error) {
	path := fmt.Sprintf("/api/v1/volumes/%s", url.PathEscape(volumeName))
	var result VolumeInfo
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// AttachVolume attaches a volume to an instance
func (c *Client) AttachVolume(ctx context.Context, instanceID, volumeName, mountPoint string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/volumes/%s",
		url.PathEscape(instanceID),
		url.PathEscape(volumeName))
	req := struct {
		MountPoint string `json:"mount_point"`
	}{MountPoint: mountPoint}
	return c.doRequest(ctx, "POST", path, req, nil)
}

// DetachVolume detaches a volume from an instance
func (c *Client) DetachVolume(ctx context.Context, instanceID, volumeName string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/volumes/%s",
		url.PathEscape(instanceID),
		url.PathEscape(volumeName))
	return c.doRequest(ctx, "DELETE", path, nil, nil)
}
