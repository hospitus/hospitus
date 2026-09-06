package client

import (
	"context"
	"fmt"
	"net/url"
)

// ServiceActionRequest contains parameters for a service action
type ServiceActionRequest struct {
	ServiceName string `json:"service_name"`
	Action      string `json:"action"` // enable, disable, start, stop, restart, reload
}

// ServiceActionResult contains the result of a service action
type ServiceActionResult struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// ServiceInfo contains information about a service
type ServiceInfo struct {
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Running  bool   `json:"running"`
	RCScript string `json:"rc_script,omitempty"`
}

// ServiceStatus is an alias for ServiceInfo: the status of a service is
// described by the same fields. Kept as a named alias so existing callers
// referencing client.ServiceStatus continue to compile.
type ServiceStatus = ServiceInfo

// ServiceAction performs an action on a service in an instance
func (c *Client) ServiceAction(ctx context.Context, instanceID string, req ServiceActionRequest) (*ServiceActionResult, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/services/%s/%s",
		url.PathEscape(instanceID),
		url.PathEscape(req.ServiceName),
		url.PathEscape(req.Action))

	var result ServiceActionResult
	if err := c.doRequest(ctx, "POST", path, nil, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// GetServiceStatus gets the status of a service in an instance
func (c *Client) GetServiceStatus(ctx context.Context, instanceID, serviceName string) (*ServiceStatus, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/services/%s",
		url.PathEscape(instanceID),
		url.PathEscape(serviceName))

	var result ServiceStatus
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// ListServices lists services in an instance
func (c *Client) ListServices(ctx context.Context, instanceID, filter string) ([]ServiceInfo, error) {
	path := fmt.Sprintf("/api/v1/instances/%s/services", url.PathEscape(instanceID))
	if filter != "" {
		path += "?filter=" + url.QueryEscape(filter)
	}

	var result []ServiceInfo
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}

	return result, nil
}
