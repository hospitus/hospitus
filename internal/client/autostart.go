package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/hospitus/hospitus/pkg/provider"
)

// AutoStartInfo represents the auto-start configuration for an instance.
type AutoStartInfo struct {
	ID        string                   `json:"id"`
	Provider  string                   `json:"provider"`
	AutoStart provider.AutoStartConfig `json:"autostart"`
}

// autoStartListResponse is the JSON response from GET /api/v1/autostart.
type autoStartListResponse struct {
	Instances []AutoStartInfo `json:"instances"`
	Count     int             `json:"count"`
}

// ListAutoStart returns all instances configured for auto-start.
func (c *Client) ListAutoStart(ctx context.Context) ([]AutoStartInfo, error) {
	var resp autoStartListResponse
	if err := c.doRequest(ctx, http.MethodGet, "/api/v1/autostart", nil, &resp); err != nil {
		return nil, fmt.Errorf("list auto-start: %w", err)
	}
	return resp.Instances, nil
}

// GetAutoStart returns the auto-start configuration for a specific instance.
// providerName is the provider name (e.g. "jail", "bhyve", "qemu").
func (c *Client) GetAutoStart(ctx context.Context, providerName, instanceID string) (*AutoStartInfo, error) {
	var info AutoStartInfo
	path := fmt.Sprintf("/api/v1/autostart/%s/%s", url.PathEscape(providerName), url.PathEscape(instanceID))
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &info); err != nil {
		return nil, fmt.Errorf("get auto-start for %s/%s: %w", providerName, instanceID, err)
	}
	return &info, nil
}

// SetAutoStart enables or updates auto-start for an instance.
func (c *Client) SetAutoStart(ctx context.Context, providerName, instanceID string, cfg provider.AutoStartConfig) (*AutoStartInfo, error) {
	var info AutoStartInfo
	path := fmt.Sprintf("/api/v1/autostart/%s/%s", url.PathEscape(providerName), url.PathEscape(instanceID))
	if err := c.doRequest(ctx, http.MethodPut, path, cfg, &info); err != nil {
		return nil, fmt.Errorf("set auto-start for %s/%s: %w", providerName, instanceID, err)
	}
	return &info, nil
}

// DisableAutoStart disables auto-start for an instance.
func (c *Client) DisableAutoStart(ctx context.Context, providerName, instanceID string) error {
	path := fmt.Sprintf("/api/v1/autostart/%s/%s", url.PathEscape(providerName), url.PathEscape(instanceID))
	if err := c.doRequest(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return fmt.Errorf("disable auto-start for %s/%s: %w", providerName, instanceID, err)
	}
	return nil
}
