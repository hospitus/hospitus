package bhyve

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Compile-time assertion: BhyveProvider implements InstanceHealthCheckProvider
var _ provider.InstanceHealthCheckProvider = (*BhyveProvider)(nil)

// CheckInstanceHealth performs a health check on a bhyve VM.
//
// It checks:
//  1. Whether the VM exists and its state file is consistent
//  2. Whether the VM process is alive (signal 0)
//  3. Whether the VNC port is responsive (if configured)
func (p *BhyveProvider) CheckInstanceHealth(ctx context.Context, handle provider.InstanceHandle) (*provider.InstanceHealth, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	health := &provider.InstanceHealth{
		Timestamp: time.Now(),
		Checks:    make([]provider.HealthCheck, 0),
	}

	// Check 1: VM state consistency
	state, err := p.loadVMState(vmDir)
	if err != nil {
		health.Status = provider.HealthStatusUnknown
		health.Message = fmt.Sprintf("failed to load VM state: %v", err)
		health.Checks = append(health.Checks, provider.HealthCheck{
			Name:    "vm_state",
			Status:  provider.HealthStatusUnknown,
			Message: err.Error(),
		})
		return health, nil
	}

	if state.State != provider.StateRunning {
		health.Status = provider.HealthStatusUnhealthy
		health.Message = "VM is not running"
		health.Checks = append(health.Checks, provider.HealthCheck{
			Name:    "vm_state",
			Status:  provider.HealthStatusUnhealthy,
			Message: fmt.Sprintf("VM state: %s", state.State),
		})
		return health, nil
	}

	health.Checks = append(health.Checks, provider.HealthCheck{
		Name:    "vm_state",
		Status:  provider.HealthStatusHealthy,
		Message: "VM state is running",
	})

	// Check 2: Process liveness via signal 0
	processCheck := provider.HealthCheck{Name: "process_alive"}
	switch {
	case state.PID <= 0:
		processCheck.Status = provider.HealthStatusUnhealthy
		processCheck.Message = "no PID recorded"

	// Signal 0 alone only proves *a* process holds the number; after bhyve
	// exits the kernel reuses it, and an unrelated process would report the
	// VM as healthy.
	case !p.pidIsBhyveVM(ctx, state.PID, vmName):
		processCheck.Status = provider.HealthStatusUnhealthy
		processCheck.Message = fmt.Sprintf("process %d is no longer this VM", state.PID)

	default:
		processCheck.Status = provider.HealthStatusHealthy
		processCheck.Message = fmt.Sprintf("process %d is alive", state.PID)
	}
	health.Checks = append(health.Checks, processCheck)

	// Check 3: VNC port responsive (only when VNC is actually enabled)
	config, err := p.loadVMConfig(vmDir)
	if err == nil && config.VNCEnabled && config.VNCPort > 0 {
		vncCheck := provider.HealthCheck{Name: "vnc_port"}
		vncHost := config.VNCHost
		if vncHost == "" {
			vncHost = "127.0.0.1"
		}
		addr := net.JoinHostPort(vncHost, fmt.Sprintf("%d", config.VNCPort))
		dialer := &net.Dialer{Timeout: 2 * time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			vncCheck.Status = provider.HealthStatusDegraded
			vncCheck.Message = fmt.Sprintf("VNC port %d not reachable", config.VNCPort)
		} else {
			conn.Close()
			vncCheck.Status = provider.HealthStatusHealthy
			vncCheck.Message = fmt.Sprintf("VNC port %d is responding", config.VNCPort)
		}
		health.Checks = append(health.Checks, vncCheck)
	}

	// Determine overall status
	health.Status = provider.HealthStatusHealthy
	health.Message = "VM is healthy"

	for _, check := range health.Checks {
		switch check.Status {
		case provider.HealthStatusUnhealthy:
			health.Status = provider.HealthStatusUnhealthy
			health.Message = check.Message
			return health, nil
		case provider.HealthStatusDegraded:
			health.Status = provider.HealthStatusDegraded
			health.Message = check.Message
		}
	}

	return health, nil
}
