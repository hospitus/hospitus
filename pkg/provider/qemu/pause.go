package qemu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/hospitus/hospitus/pkg/provider"
)

// Ensure QEMUProvider implements PauseProvider.
var _ provider.PauseProvider = (*QEMUProvider)(nil)

// PauseInstance sends SIGSTOP to the QEMU process, freezing execution.
func (p *QEMUProvider) PauseInstance(ctx context.Context, handle provider.InstanceHandle) error {
	vmName := handle.ID

	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get VM state: %w", err)
	}
	if state != provider.StateRunning {
		return fmt.Errorf("VM is not running (current state: %s)", state)
	}

	pid, err := p.readPID(vmName)
	if err != nil {
		return err
	}

	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		return fmt.Errorf("failed to pause QEMU process (pid %d): %w", pid, err)
	}

	// Update config to record paused state. A config that cannot be read is as
	// bad as one that cannot be written: the VM would stay frozen while
	// GetInstanceState — which reads the flag from this file — kept reporting
	// it as running. Unfreeze and report the failure instead.
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	cfg, err := p.loadVMConfig(configPath)
	if err != nil {
		_ = syscall.Kill(pid, syscall.SIGCONT)
		return fmt.Errorf("failed to load VM config to record the paused state: %w", err)
	}
	if cfg.Spec.ProviderConfig == nil {
		cfg.Spec.ProviderConfig = make(map[string]interface{})
	}
	cfg.Spec.ProviderConfig["paused"] = true
	if saveErr := p.saveVMConfig(cfg, configPath); saveErr != nil {
		// Unfreeze since we couldn't record the state
		_ = syscall.Kill(pid, syscall.SIGCONT)
		return fmt.Errorf("failed to save VM config: %w", saveErr)
	}

	return nil
}

// ResumeInstance sends SIGCONT to a paused QEMU process.
func (p *QEMUProvider) ResumeInstance(ctx context.Context, handle provider.InstanceHandle) error {
	vmName := handle.ID

	pid, err := p.readPID(vmName)
	if err != nil {
		return err
	}

	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		return fmt.Errorf("failed to resume QEMU process (pid %d): %w", pid, err)
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
