package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Health checks use podman inspect to query the container's health status,
// falling back to the container's running state if no healthcheck is configured.

// Ensure PodmanProvider implements InstanceHealthCheckProvider
var _ provider.InstanceHealthCheckProvider = (*PodmanProvider)(nil)

// CheckInstanceHealth performs a health check on a container.
//
// It checks:
//  1. Whether the container exists and is running
//  2. The container's built-in healthcheck status (if configured)
//
// For containers without a healthcheck, the running state alone determines
// the health status.
func (p *PodmanProvider) CheckInstanceHealth(ctx context.Context, handle provider.InstanceHandle) (*provider.InstanceHealth, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	containerName := handle.ID
	health := &provider.InstanceHealth{
		Timestamp: time.Now(),
		Checks:    make([]provider.HealthCheck, 0),
	}

	// Check 1: Container exists and is running
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		health.Status = provider.HealthStatusUnknown
		health.Message = fmt.Sprintf("failed to check container state: %v", err)
		health.Checks = append(health.Checks, provider.HealthCheck{
			Name:    "container_running",
			Status:  provider.HealthStatusUnknown,
			Message: err.Error(),
		})
		return health, nil
	}

	if state != provider.StateRunning {
		health.Status = provider.HealthStatusUnhealthy
		health.Message = "container is not running"
		health.Checks = append(health.Checks, provider.HealthCheck{
			Name:    "container_running",
			Status:  provider.HealthStatusUnhealthy,
			Message: fmt.Sprintf("container state: %s", state),
		})
		return health, nil
	}

	health.Checks = append(health.Checks, provider.HealthCheck{
		Name:    "container_running",
		Status:  provider.HealthStatusHealthy,
		Message: "container is running",
	})

	// Check 2: Built-in healthcheck status (if configured)
	// An inspect that failed says nothing about whether a healthcheck exists.
	// Mapping it to "none" made the container come back Healthy on the strength
	// of the running check alone.
	hasHealthCheck, hcErr := p.hasHealthCheck(ctx, containerName)
	healthCheckResult := provider.HealthCheck{Name: "healthcheck"}

	switch {
	case hcErr != nil:
		healthCheckResult.Status = provider.HealthStatusUnknown
		healthCheckResult.Message = fmt.Sprintf("cannot tell whether a healthcheck is configured: %v", hcErr)
		health.Checks = append(health.Checks, healthCheckResult)
		health.Status = provider.HealthStatusUnknown
		return health, nil
	case hasHealthCheck:
		status, err := p.getHealthCheckStatus(ctx, containerName)
		if err != nil {
			healthCheckResult.Status = provider.HealthStatusUnknown
			healthCheckResult.Message = fmt.Sprintf("failed to get healthcheck status: %v", err)
		} else {
			switch status {
			case "healthy":
				healthCheckResult.Status = provider.HealthStatusHealthy
				healthCheckResult.Message = "healthcheck passed"
			case "unhealthy":
				healthCheckResult.Status = provider.HealthStatusUnhealthy
				healthCheckResult.Message = "healthcheck failed"
			case "starting":
				healthCheckResult.Status = provider.HealthStatusDegraded
				healthCheckResult.Message = "healthcheck still starting"
			default:
				healthCheckResult.Status = provider.HealthStatusUnknown
				healthCheckResult.Message = fmt.Sprintf("unknown healthcheck status: %s", status)
			}
		}
	default:
		healthCheckResult.Status = provider.HealthStatusUnknown
		healthCheckResult.Message = "no healthcheck configured"
	}

	health.Checks = append(health.Checks, healthCheckResult)

	// Overall status: healthy if running and no healthcheck, or running +
	// healthcheck healthy.
	//
	// A status that could not be determined maps to Unknown, not Unhealthy: a
	// failed *query* says nothing about the container, and orchestration's
	// auto-restart acts on Unhealthy.
	switch {
	case !hasHealthCheck || healthCheckResult.Status == provider.HealthStatusHealthy:
		health.Status = provider.HealthStatusHealthy
		health.Message = "container is healthy"
	case healthCheckResult.Status == provider.HealthStatusDegraded:
		health.Status = provider.HealthStatusDegraded
		health.Message = healthCheckResult.Message
	case healthCheckResult.Status == provider.HealthStatusUnknown:
		health.Status = provider.HealthStatusUnknown
		health.Message = healthCheckResult.Message
	default:
		health.Status = provider.HealthStatusUnhealthy
		health.Message = healthCheckResult.Message
	}

	return health, nil
}

// hasHealthCheck checks whether a container has a healthcheck configured.
func (p *PodmanProvider) hasHealthCheck(ctx context.Context, containerName string) (bool, error) {
	output, err := p.cmd().Output(ctx, p.podmanBin, "inspect", "--format", "{{.Config.Healthcheck}}", containerName)
	if err != nil {
		// "false" and "I could not find out" are different answers, and the
		// caller reported the container healthy on the first.
		return false, fmt.Errorf("inspecting %s for a healthcheck: %w", containerName, err)
	}
	return healthcheckConfigured(string(output)), nil
}

// healthcheckConfigured reports whether `podman inspect` output for
// {{.Config.Healthcheck}} denotes a configured healthcheck. The raw output
// carries a trailing newline, so it must be trimmed before comparing against
// the empty/nil sentinels — otherwise "<nil>\n" != "<nil>" made every
// container appear to have a healthcheck.
func healthcheckConfigured(raw string) bool {
	result := strings.TrimSpace(raw)
	return result != "<nil>" && result != "" && result != "[]" && result != "{}"
}

// getHealthCheckStatus returns the current healthcheck status of a container.
func (p *PodmanProvider) getHealthCheckStatus(ctx context.Context, containerName string) (string, error) {
	output, err := p.cmd().Output(ctx, p.podmanBin, "inspect", "--format", "json", containerName)
	if err != nil {
		return "", fmt.Errorf("failed to inspect container: %w", err)
	}

	var inspectData []map[string]interface{}
	if err := json.Unmarshal(output, &inspectData); err != nil {
		return "", fmt.Errorf("failed to parse inspect output: %w", err)
	}

	if len(inspectData) == 0 {
		return "", fmt.Errorf("no container data found")
	}

	state, ok := inspectData[0]["State"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("no state data found")
	}

	health, ok := state["Health"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("no health data found")
	}

	status, ok := health["Status"].(string)
	if !ok {
		return "", fmt.Errorf("no health status found")
	}

	return status, nil
}
