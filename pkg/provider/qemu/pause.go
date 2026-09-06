package qemu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Ensure QEMUProvider implements PauseProvider.
var _ provider.PauseProvider = (*QEMUProvider)(nil)

// PauseInstance pauses the guest's CPUs through QMP.
// qmpFor opens the VM's QMP socket.
//
// Pause and resume go through it rather than through SIGSTOP and SIGCONT on a
// PID read from a file. A QEMU process can exit while its pidfile remains, the
// kernel reuses the number, and the signal then freezes an unrelated host
// process — a check before signaling cannot close that window, because the
// process can die between the check and the signal. The socket belongs to the
// VM: if QEMU is gone, connecting fails.
func (p *QEMUProvider) qmpFor(vmName string) (*QMPClient, error) {
	client, err := NewQMPClient(filepath.Join(p.dataDir, vmName, "qmp.sock"))
	if err != nil {
		return nil, fmt.Errorf("failed to create QMP client: %w", err)
	}
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to QMP: %w", err)
	}
	return client, nil
}

func (p *QEMUProvider) PauseInstance(ctx context.Context, handle provider.InstanceHandle) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get VM state: %w", err)
	}
	if state != provider.StateRunning {
		return fmt.Errorf("VM is not running (current state: %s)", state)
	}

	client, err := p.qmpFor(vmName)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Stop(); err != nil {
		return fmt.Errorf("failed to pause VM %s: %w", vmName, err)
	}

	// Update config to record paused state. A config that cannot be read is as
	// bad as one that cannot be written: the VM would stay frozen while
	// GetInstanceState — which reads the flag from this file — kept reporting
	// it as running. Unfreeze and report the failure instead.
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	cfg, err := p.loadVMConfig(configPath)
	if err != nil {
		// Both causes: the caller has to be able to tell "paused and recorded"
		// from "paused and stuck that way".
		if resumeErr := client.Continue(); resumeErr != nil {
			return errors.Join(
				fmt.Errorf("failed to load VM config to record the paused state: %w", err),
				fmt.Errorf("the VM is still paused: %w", resumeErr))
		}
		return fmt.Errorf("failed to load VM config to record the paused state: %w", err)
	}
	if cfg.Spec.ProviderConfig == nil {
		cfg.Spec.ProviderConfig = make(map[string]interface{})
	}
	cfg.Spec.ProviderConfig["paused"] = true
	if saveErr := p.saveVMConfig(cfg, configPath); saveErr != nil {
		// Unfreeze since we couldn't record the state.
		if resumeErr := client.Continue(); resumeErr != nil {
			return errors.Join(
				fmt.Errorf("failed to save VM config: %w", saveErr),
				fmt.Errorf("the VM is still paused: %w", resumeErr))
		}
		return fmt.Errorf("failed to save VM config: %w", saveErr)
	}

	return nil
}

// ResumeInstance resumes a paused guest through QMP.
func (p *QEMUProvider) ResumeInstance(ctx context.Context, handle provider.InstanceHandle) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	client, err := p.qmpFor(vmName)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Continue(); err != nil {
		return fmt.Errorf("failed to resume VM %s: %w", vmName, err)
	}

	// Update config to clear paused state. The flag is what GetInstanceState
	// reports, so a config that cannot be read leaves a running VM described as
	// paused — say so rather than returning success.
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	cfg, err := p.loadVMConfig(configPath)
	if err != nil {
		return fmt.Errorf("VM resumed but its config could not be loaded to clear the paused flag (state may be stale): %w", err)
	}
	if cfg.Spec.ProviderConfig != nil {
		delete(cfg.Spec.ProviderConfig, "paused")
	}
	if err := p.saveVMConfig(cfg, configPath); err != nil {
		return fmt.Errorf("VM resumed but failed to update config (state may be stale): %w", err)
	}

	return nil
}

// readPID reads the QEMU process PID from the state directory.
func (p *QEMUProvider) readPID(vmName string) (int, error) {
	pidFile := filepath.Join(p.stateDir, fmt.Sprintf("%s.pid", vmName))
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, fmt.Errorf("failed to read PID file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid PID in file: %s", string(data))
	}
	return pid, nil
}
