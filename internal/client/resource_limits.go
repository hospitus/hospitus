package client

import (
	"context"
	"fmt"
	"net/url"
)

// ResourceLimit represents a FreeBSD rctl resource limit
type ResourceLimit struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
	Amount   string `json:"amount"`
}

// GetResourceLimits retrieves current resource limits for a jail
func (c *Client) GetResourceLimits(ctx context.Context, instanceID string) ([]ResourceLimit, error) {
	var result struct {
		Limits []ResourceLimit `json:"limits"`
	}

	path := fmt.Sprintf("/api/v1/instances/%s/freebsd/rctl", url.PathEscape(instanceID))
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}

	return result.Limits, nil
}

// SetResourceLimits sets resource limits for a jail
func (c *Client) SetResourceLimits(ctx context.Context, instanceID string, limits []ResourceLimit) error {
	body := map[string]interface{}{
		"limits": limits,
	}

	path := fmt.Sprintf("/api/v1/instances/%s/freebsd/rctl", url.PathEscape(instanceID))
	return c.doRequest(ctx, "POST", path, body, nil)
}

// RemoveResourceLimits removes all resource limits from a jail
func (c *Client) RemoveResourceLimits(ctx context.Context, instanceID string) error {
	path := fmt.Sprintf("/api/v1/instances/%s/freebsd/rctl", url.PathEscape(instanceID))
	return c.doRequest(ctx, "DELETE", path, nil, nil)
}
