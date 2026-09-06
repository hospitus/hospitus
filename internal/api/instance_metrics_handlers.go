package api

import (
	"fmt"
	"net/http"

	"github.com/hospitus/hospitus/pkg/provider"
)

// handleInstanceMetrics returns resource usage metrics for an instance.
//
// GET /api/v1/instances/{id}/metrics
//
// Response:
//
//	{
//	  "timestamp": "2024-01-01T00:00:00Z",
//	  "cpu_usage_percent": 5.2,
//	  "memory_used_mb": 128,
//	  "memory_total_mb": 1024,
//	  "disk_read_bytes": 12345,
//	  "disk_write_bytes": 67890,
//	  "net_rx_bytes": 11111,
//	  "net_tx_bytes": 22222
//	}
func (s *Server) handleInstanceMetrics(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

	// Get instance from datastore
	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	// Get metrics from provider
	metrics, err := prov.GetInstanceMetrics(ctx, instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to get metrics", err)
		return
	}

	// Build response
	response := map[string]interface{}{
		"timestamp":         metrics.Timestamp,
		"cpu_usage_percent": metrics.CPUUsagePercent,
		"memory_used_mb":    metrics.MemoryUsedMB,
		"memory_total_mb":   metrics.MemoryTotalMB,
		"disk_read_bytes":   metrics.DiskReadBytes,
		"disk_write_bytes":  metrics.DiskWriteBytes,
		"net_rx_bytes":      metrics.NetRxBytes,
		"net_tx_bytes":      metrics.NetTxBytes,
	}

	s.writeJSON(w, http.StatusOK, response)
}

// handleInstanceHealth performs a health check on an instance.
//
// GET /api/v1/instances/{id}/health
//
// Response:
//
//	{
//	  "status": "healthy|unhealthy|degraded|unknown",
//	  "message": "description",
//	  "checks": [...],
//	  "timestamp": "2024-01-01T00:00:00Z"
//	}
func (s *Server) handleInstanceHealth(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

	// Get instance from datastore
	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	// Check if provider supports health checks
	healthProv, ok := prov.(provider.InstanceHealthCheckProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, "Provider does not support instance health checks")
		return
	}

	// Perform health check
	health, err := healthProv.CheckInstanceHealth(ctx, instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to check health", err)
		return
	}

	s.writeJSON(w, http.StatusOK, health)
}
