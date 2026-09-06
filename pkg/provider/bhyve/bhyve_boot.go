package bhyve

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
)

// SetCPUPriority sets the CPU scheduling priority for a running bhyve VM
// via renice(8). Nice values range from -20 (highest priority) to 19
// (lowest); negative values require root.
func (p *BhyveProvider) SetCPUPriority(ctx context.Context, handle provider.InstanceHandle, priority int) error {
	vmName := handle.ID

	// SECURITY: Validate priority range
	if priority < -20 || priority > 19 {
		return fmt.Errorf("priority must be between -20 and 19 (got: %d)", priority)
	}

	pid, err := p.getVMPID(ctx, vmName)
	if err != nil {
		return fmt.Errorf("failed to get VM PID: %w", err)
	}

	if pid == 0 {
		return fmt.Errorf("VM %s is not running", vmName)
	}

	// Set priority using renice
	output, err := p.cmd().CombinedOutput(ctx, "renice", "-n", fmt.Sprintf("%d", priority), "-p", fmt.Sprintf("%d", pid))
	if err != nil {
		return fmt.Errorf("failed to set CPU priority: %w (output: %s)", err, string(output))
	}

	return nil
}

// GetCPUPriority returns the current CPU scheduling priority for a bhyve VM.
//
// Returns the nice value of the bhyve process.
func (p *BhyveProvider) GetCPUPriority(ctx context.Context, handle provider.InstanceHandle) (int, error) {
	vmName := handle.ID

	pid, err := p.getVMPID(ctx, vmName)
	if err != nil {
		return 0, fmt.Errorf("failed to get VM PID: %w", err)
	}

	if pid == 0 {
		return 0, fmt.Errorf("VM %s is not running", vmName)
	}

	// Get nice value using ps
	output, err := p.cmd().CombinedOutput(ctx, "ps", "-o", "nice=", "-p", fmt.Sprintf("%d", pid))
	if err != nil {
		return 0, fmt.Errorf("failed to get process priority: %w", err)
	}

	niceStr := strings.TrimSpace(string(output))
	nice, err := strconv.Atoi(niceStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse nice value: %w", err)
	}

	return nice, nil
}

// getVMPID returns the process ID of a running bhyve VM.
func (p *BhyveProvider) getVMPID(ctx context.Context, vmName string) (int, error) {
	// Match the bhyve process whose final argument is exactly vmName; a bare
	// pgrep substring match would return (and this function would then renice /
	// operate on) a different VM whose name merely contains vmName.
	pids := bhyvePIDsForVM(ctx, vmName)
	if len(pids) == 0 {
		return 0, nil
	}
	return pids[0], nil
}

// setBootOrderByIndex records the boot order in the VM's config as 0-based
// indices into DiskPaths (first = primary boot device).
func (p *BhyveProvider) setBootOrderByIndex(ctx context.Context, handle provider.InstanceHandle, bootOrder []int) error {
	vmName := handle.ID

	// Load VM configuration
	vmDir := filepath.Join(p.dataDir, vmName)
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM configuration: %w", err)
	}

	// Validate boot order
	if len(bootOrder) > len(config.DiskPaths) {
		return fmt.Errorf("boot order length (%d) exceeds number of disks (%d)", len(bootOrder), len(config.DiskPaths))
	}

	// Validate indices
	for _, idx := range bootOrder {
		if idx < 0 || idx >= len(config.DiskPaths) {
			return fmt.Errorf("invalid boot order index: %d (must be 0-%d)", idx, len(config.DiskPaths)-1)
		}
	}

	// Update boot order
	config.BootOrder = bootOrder

	// Save configuration
	if err := p.saveVMConfig(vmDir, config); err != nil {
		return fmt.Errorf("failed to save VM configuration: %w", err)
	}

	return nil
}

// getBootOrderByIndex returns the configured boot order for VM disks as disk indices.
func (p *BhyveProvider) getBootOrderByIndex(ctx context.Context, handle provider.InstanceHandle) ([]int, error) {
	vmName := handle.ID

	// Load VM configuration
	vmDir := filepath.Join(p.dataDir, vmName)
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load VM configuration: %w", err)
	}

	return config.BootOrder, nil
}
