package qemu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Auto-start allows VMs to be automatically started when the hospitusd daemon
// starts, typically after a system reboot.

// Ensure QEMUProvider implements AutoStartProvider
var _ provider.AutoStartProvider = (*QEMUProvider)(nil)

// SetAutoStart configures auto-start settings for a QEMU VM.
func (p *QEMUProvider) SetAutoStart(ctx context.Context, handle provider.InstanceHandle, config provider.AutoStartConfig) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
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

	// Written through a temporary file and renamed, like saveVMConfig: an
	// interrupted in-place write leaves invalid JSON, GetAutoStart then reports
	// a parse error, and ListAutoStartInstances skips the VM — which does not
	// start at the next boot.
	tmp, err := os.CreateTemp(filepath.Dir(autostartFile), ".autostart-*.json")
	if err != nil {
		return fmt.Errorf("failed to write autostart config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write autostart config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to flush autostart config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close autostart config: %w", err)
	}
	if err := os.Rename(tmpName, autostartFile); err != nil {
		return fmt.Errorf("failed to install autostart config: %w", err)
	}
	syncDir(filepath.Dir(autostartFile))

	return nil
}

// GetAutoStart returns the auto-start configuration for a QEMU VM.
func (p *QEMUProvider) GetAutoStart(ctx context.Context, handle provider.InstanceHandle) (*provider.AutoStartConfig, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
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
	// JSON leaves an omitted priority at zero, which the contract in
	// pkg/provider reserves for "not specified". Left as it was, a config
	// written without the field started ahead of priority 1 rather than at the
	// documented default.
	if config.Priority == 0 {
		config.Priority = defaultAutoStartPriority
	}

	return &config, nil
}

// defaultAutoStartPriority is the priority a VM gets when its configuration
// names none. pkg/provider documents 1-100 with 50 as the default, and reserves
// zero for "not specified".
const defaultAutoStartPriority = 50

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
	// Collected rather than returned at once: one unreadable VM should not hide
	// the others, but the caller has to learn that it was skipped.
	var listErr error

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		vmName := entry.Name()
		handle := provider.InstanceHandle{
			ID:       vmName,
			Provider: "qemu",
		}

		// A corrupt or unreadable autostart.json used to remove the VM from the
		// list without a word, so the daemon reported success while a VM the
		// operator had enabled never started.
		config, err := p.GetAutoStart(ctx, handle)
		if err != nil {
			p.logger.Warn("cannot read the auto-start configuration; the VM will not be started",
				"vm", vmName, logging.FieldError, err)
			listErr = errors.Join(listErr, fmt.Errorf("auto-start config for %s: %w", vmName, err))
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

	return handles, listErr
}

// StartAutoStartInstances starts all QEMU VMs configured for auto-start.
func (p *QEMUProvider) StartAutoStartInstances(ctx context.Context) error {
	// The listing error names the VMs whose configuration could not be read; the
	// handles it returns alongside are the ones that could. Returning early on
	// it — as this did once ListAutoStartInstances started reporting it — meant
	// one corrupt autostart.json stopped every other VM from starting.
	handles, listErr := p.ListAutoStartInstances(ctx)
	if listErr != nil {
		p.logger.Warn("some auto-start configurations could not be read; "+
			"starting the VMs that could", logging.FieldError, listErr)
	}

	if len(handles) == 0 {
		return listErr
	}

	// The listing error travels with the start errors: both say a VM the
	// operator enabled did not come up.
	lastErr := listErr
	for i, handle := range handles {
		config, err := p.GetAutoStart(ctx, handle)
		if err != nil {
			// Joined, not replaced: a later start failure used to hide the VM
			// whose configuration could not be read at all.
			lastErr = errors.Join(lastErr, err)
			continue
		}

		if i > 0 && config.DelayMS > 0 {
			select {
			case <-ctx.Done():
				// Joined, not returned bare: lastErr holds the listing error
				// and every start failure so far, and a daemon shut down
				// mid-stagger used to report only the cancellation.
				return errors.Join(lastErr, ctx.Err())
			case <-time.After(time.Duration(config.DelayMS) * time.Millisecond):
			}
		}

		state, err := p.GetInstanceState(ctx, handle)
		if err == nil && state == provider.StateRunning {
			continue
		}

		if err := p.StartInstance(ctx, handle); err != nil {
			lastErr = errors.Join(lastErr, fmt.Errorf("failed to auto-start VM %s: %w", handle.ID, err))
		}
	}

	return lastErr
}
