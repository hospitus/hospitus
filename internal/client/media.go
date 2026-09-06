package client

import (
	"context"
	"fmt"
	"net/url"

	"github.com/hospitus/hospitus/pkg/provider"
)

// InsertMedia inserts media into a VM's CD-ROM drive
func (c *Client) InsertMedia(ctx context.Context, instanceID string, spec provider.MediaSpec) error {
	path := fmt.Sprintf("/api/v1/instances/%s/media", url.PathEscape(instanceID))
	return c.doRequest(ctx, "POST", path, spec, nil)
}

// EjectMedia ejects media from a VM's CD-ROM drive
func (c *Client) EjectMedia(ctx context.Context, instanceID, deviceID string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/media/%s", url.PathEscape(instanceID), url.PathEscape(deviceID))
	return c.doRequest(ctx, "DELETE", path, nil, nil)
}

// ListMedia lists media devices and their status
func (c *Client) ListMedia(ctx context.Context, instanceID string) ([]provider.MediaInfo, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/media", url.PathEscape(instanceID))

	var result struct {
		Media []provider.MediaInfo `json:"media"`
	}
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}

	return result.Media, nil
}

// SetBootOrder sets the boot order for a VM
func (c *Client) SetBootOrder(ctx context.Context, instanceID string, order provider.BootOrder) error {
	path := fmt.Sprintf("/api/v1/instances/%s/boot-order", url.PathEscape(instanceID))
	return c.doRequest(ctx, "POST", path, order, nil)
}

// GetBootOrder gets the boot order for a VM
func (c *Client) GetBootOrder(ctx context.Context, instanceID string) (*provider.BootOrder, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/boot-order", url.PathEscape(instanceID))

	var result provider.BootOrder
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}

	return &result, nil
}
