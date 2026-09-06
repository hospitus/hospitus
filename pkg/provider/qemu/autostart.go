package qemu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
)

// Auto-start allows VMs to be automatically started when the hospitusd daemon
// starts, typically after a system reboot.

// Ensure QEMUProvider implements AutoStartProvider
var _ provider.AutoStartProvider = (*QEMUProvider)(nil)

// SetAutoStart configures auto-start settings for a QEMU VM.
func (p *QEMUProvider) SetAutoStart(ctx context.Context, handle provider.InstanceHandle, config provider.AutoStartConfig) error {
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
		config.Priority = 50
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

// GetAutoStart returns the auto-start configuration for a QEMU VM.
func (p *QEMUProvider) GetAutoStart(ctx context.Context, handle provider.InstanceHandle) (*provider.AutoStartConfig, error) {
	vmName := handle.ID

	vmDir := filepath.Join(p.dataDir, vmName)
	autostartFile := filepath.Join(vmDir, "autostart.json")

	data, err := os.ReadFile(autostartFile)
	if err != nil {
		if os.IsNotExist(err) {
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

// ListAutoStartInstances returns all QEMU VMs configured for auto-start.
func (p *QEMUProvider) ListAutoStartInstances(ctx context.Context) ([]provider.InstanceHandle, error) {
	entries, err := os.ReadDir(p.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read data directory: %w", err)
	}

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
			Provider: "qemu",
		}

		config, err := p.GetAutoStart(ctx, handle)
		if err != nil {
			continue
		}

		if config.Enabled {
			vms = append(vms, vmWithPriority{
				handle:   handle,
				priority: config.Priority,
			})
		}
	}

	sort.Slice(vms, func(i, j int) bool {
		if vms[i].priority != vms[j].priority {
			return vms[i].priority < vms[j].priority
		}
		return vms[i].handle.ID < vms[j].handle.ID
	})

	handles := make([]provider.InstanceHandle, len(vms))
	for i, vm := range vms {
		handles[i] = vm.handle
	}

	return handles, nil
}

// StartAutoStartInstances starts all QEMU VMs configured for auto-start.
func (p *QEMUProvider) StartAutoStartInstances(ctx context.Context) error {
	handles, err := p.ListAutoStartInstances(ctx)
	if err != nil {
		return fmt.Errorf("failed to list auto-start instances: %w", err)
	}

	if len(handles) == 0 {
		return nil
	}

	var lastErr error
	for i, handle := range handles {
		config, err := p.GetAutoStart(ctx, handle)
		if err != nil {
			lastErr = err
			continue
		}

		if i > 0 && config.DelayMS > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(config.DelayMS) * time.Millisecond):
			}
		}

		state, err := p.GetInstanceState(ctx, handle)
		if err == nil && state == provider.StateRunning {
			continue
		}

		if err := p.StartInstance(ctx, handle); err != nil {
			lastErr = fmt.Errorf("failed to auto-start VM %s: %w", handle.ID, err)
		}
	}

	return lastErr
}
