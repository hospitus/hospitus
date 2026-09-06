package client

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// InstanceMetrics contains resource usage metrics for an instance
type InstanceMetrics struct {
	Timestamp       string  `json:"timestamp"`
	CPUUsagePercent float64 `json:"cpu_usage_percent"`
	MemoryUsedMB    int64   `json:"memory_used_mb"`
	MemoryTotalMB   int64   `json:"memory_total_mb"`
	DiskReadBytes   int64   `json:"disk_read_bytes"`
	DiskWriteBytes  int64   `json:"disk_write_bytes"`
	NetRxBytes      int64   `json:"net_rx_bytes"`
	NetTxBytes      int64   `json:"net_tx_bytes"`
}

// metricsTimeout bounds a metrics request. It is longer than DefaultTimeout
// because "podman stats" takes about 47 seconds per container on FreeBSD, so a
// 30-second budget never let "hospitus podman stats" return. Jail and bhyve
// metrics answer immediately and are unaffected.
const metricsTimeout = 2 * time.Minute

// GetInstanceMetrics retrieves resource usage metrics for an instance
func (c *Client) GetInstanceMetrics(ctx context.Context, instanceID string) (*InstanceMetrics, error) {
	var result InstanceMetrics

	path := fmt.Sprintf("/api/v1/instances/%s/metrics", url.PathEscape(instanceID))
	if err := c.doRequestWithTimeout(ctx, "GET", path, nil, &result, metricsTimeout); err != nil {
		return nil, err
	}

	return &result, nil
}

// InstanceHealth contains health check results for an instance
type InstanceHealth struct {
	Status    string        `json:"status"`
	Message   string        `json:"message,omitempty"`
	Checks    []HealthCheck `json:"checks,omitempty"`
	Timestamp string        `json:"timestamp"`
}

// HealthCheck represents an individual health check result
type HealthCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// GetInstanceHealth performs a health check on an instance
func (c *Client) GetInstanceHealth(ctx context.Context, instanceID string) (*InstanceHealth, error) {
	var result InstanceHealth

	path := fmt.Sprintf("/api/v1/instances/%s/health", url.PathEscape(instanceID))
	if err := c.doRequest(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}

	return &result, nil
}
