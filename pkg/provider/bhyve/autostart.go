package bhyve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
)

// Auto-start allows VMs to be automatically started when the hospitusd daemon
// starts, typically after a system reboot. VMs are started in priority order
// with configurable delays between starts.

// Ensure BhyveProvider implements AutoStartProvider
var _ provider.AutoStartProvider = (*BhyveProvider)(nil)

// SetAutoStart configures auto-start settings for a bhyve VM.
//
// The configuration is stored in the VM's directory as autostart.json.
// When enabled, the VM will be started when hospitusd starts.
//
// Example:
//
//	config := provider.AutoStartConfig{
//	    Enabled:  true,
//	    Priority: 10,  // Start early (lower = earlier)
//	    DelayMS:  5000, // Wait 5 seconds before starting
//	}
//	p.SetAutoStart(ctx, handle, config)
func (p *BhyveProvider) SetAutoStart(ctx context.Context, handle provider.InstanceHandle, config provider.AutoStartConfig) error {
	vmName := handle.ID

	// Validate VM exists
	vmDir := filepath.Join(p.dataDir, vmName)
	if _, err := os.Stat(vmDir); os.IsNotExist(err) {
		return fmt.Errorf("VM %s does not exist", vmName)
	}

	// Validate priority range
	if config.Priority < 0 || config.Priority > 100 {
		return fmt.Errorf("priority must be between 0 and 100, got %d", config.Priority)
	}

	// Set defaults
	if config.Priority == 0 && config.Enabled {
		config.Priority = 50 // Default priority
	}

	// Save auto-start configuration
	autostartFile := filepath.Join(vmDir, "autostart.json")
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal autostart config: %w", err)
	}

	if err := os.WriteFile(autostartFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write autostart config: %w", err)
	}

	return nil
}

// GetAutoStart returns the auto-start configuration for a bhyve VM.
//
// Returns a disabled config if auto-start is not configured for the VM.
func (p *BhyveProvider) GetAutoStart(ctx context.Context, handle provider.InstanceHandle) (*provider.AutoStartConfig, error) {
	vmName := handle.ID

	vmDir := filepath.Join(p.dataDir, vmName)
	autostartFile := filepath.Join(vmDir, "autostart.json")

	data, err := os.ReadFile(autostartFile)
	if err != nil {
		if os.IsNotExist(err) {
			// No auto-start configured - return disabled config
			return &provider.AutoStartConfig{
				Enabled:  false,
				Priority: 50,
				DelayMS:  0,
			}, nil
		}
		return nil, fmt.Errorf("failed to read autostart config: %w", err)
	}

	var config provider.AutoStartConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse autostart config: %w", err)
	}

	return &config, nil
}

// ListAutoStartInstances returns all bhyve VMs configured for auto-start.
//
// The list is sorted by priority (lowest first). VMs with the same priority
// are sorted by name.
func (p *BhyveProvider) ListAutoStartInstances(ctx context.Context) ([]provider.InstanceHandle, error) {
	// List all VM directories
	entries, err := os.ReadDir(p.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read data directory: %w", err)
	}

	// Collect VMs with auto-start enabled
	type vmWithPriority struct {
		handle   provider.InstanceHandle
		priority int
	}

	var vms []vmWithPriority

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		vmName := entry.Name()
		handle := provider.InstanceHandle{
			ID:       vmName,
			Provider: "bhyve",
		}

		config, err := p.GetAutoStart(ctx, handle)
		if err != nil {
			logging.WithComponent("bhyve").Warn("skipping VM with unreadable autostart config",
				logging.FieldVM, vmName, logging.FieldError, err)
			continue // Skip VMs with invalid config
		}

		if config.Enabled {
			vms = append(vms, vmWithPriority{
				handle:   handle,
				priority: config.Priority,
			})
		}
	}

	// Sort by priority (lowest first), then by name
	sort.Slice(vms, func(i, j int) bool {
		if vms[i].priority != vms[j].priority {
			return vms[i].priority < vms[j].priority
		}
		return vms[i].handle.ID < vms[j].handle.ID
	})

	// Extract handles
	handles := make([]provider.InstanceHandle, len(vms))
	for i, vm := range vms {
		handles[i] = vm.handle
	}

	return handles, nil
}

// StartAutoStartInstances starts all bhyve VMs configured for auto-start.
//
// VMs are started in priority order with configured delays. If a VM fails
// to start, an error is logged but the process continues with the next VM.
//
// This method is typically called by hospitusd during startup.
func (p *BhyveProvider) StartAutoStartInstances(ctx context.Context) error {
	handles, err := p.ListAutoStartInstances(ctx)
	if err != nil {
		return fmt.Errorf("failed to list auto-start instances: %w", err)
	}

	if len(handles) == 0 {
		return nil
	}

	var lastErr error
	for i, handle := range handles {
		// Get auto-start config for delay
		config, err := p.GetAutoStart(ctx, handle)
		if err != nil {
			lastErr = err
			continue
		}

		// Apply delay (except for first VM)
		if i > 0 && config.DelayMS > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(config.DelayMS) * time.Millisecond):
			}
		}

		// Check if already running
		state, err := p.GetInstanceState(ctx, handle)
		if err == nil && state == provider.StateRunning {
			continue // Already running
		}

		// Start the VM
		if err := p.StartInstance(ctx, handle); err != nil {
			lastErr = fmt.Errorf("failed to auto-start VM %s: %w", handle.ID, err)
			// Continue with next VM
		}
	}

	return lastErr
}

// AutoStartInfo provides detailed auto-start information for a VM.
type AutoStartInfo struct {
	provider.AutoStartConfig
	VMName string `json:"vm_name"`
	State  string `json:"state"`
}

// GetAutoStartInfo returns detailed auto-start information for a VM.
func (p *BhyveProvider) GetAutoStartInfo(ctx context.Context, handle provider.InstanceHandle) (*AutoStartInfo, error) {
	config, err := p.GetAutoStart(ctx, handle)
	if err != nil {
		return nil, err
	}

	state, _ := p.GetInstanceState(ctx, handle)

	return &AutoStartInfo{
		AutoStartConfig: *config,
		VMName:          handle.ID,
		State:           string(state),
	}, nil
}
