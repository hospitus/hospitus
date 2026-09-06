package bhyve

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

var (
	_ provider.CheckpointProvider = (*BhyveProvider)(nil)
	_ provider.PauseProvider      = (*BhyveProvider)(nil)
	_ provider.RenameProvider     = (*BhyveProvider)(nil)
)

const (
	checkpointSubDir = "checkpoints"
	checkpointExt    = ".ckpt"
)

// CheckpointInstance suspends a running bhyve VM to disk using bhyvectl --suspend.
//
// The checkpoint file is saved to <vmDir>/checkpoints/<name>.ckpt.
// After checkpointing, the VM is stopped — bhyvectl --suspend terminates bhyve.
func (p *BhyveProvider) CheckpointInstance(ctx context.Context, handle provider.InstanceHandle, name string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()
	if err := validation.ValidateSnapshotName(name); err != nil {
		return fmt.Errorf("invalid checkpoint name: %w", err)
	}

	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	st, err := p.loadVMState(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM state: %w", err)
	}
	if st.State != provider.StateRunning {
		return fmt.Errorf("VM must be running to checkpoint (current state: %s)", st.State)
	}

	ckptDir := filepath.Join(vmDir, checkpointSubDir)
	if err := os.MkdirAll(ckptDir, 0o755); err != nil {
		return fmt.Errorf("failed to create checkpoints directory: %w", err)
	}

	ckptFile := filepath.Join(ckptDir, name+checkpointExt)

	// Checkpointing needs a kernel built with BHYVE_SNAPSHOT, which GENERIC is
	// not. Without it bhyvectl has no --suspend and answers with its usage
	// text, so the caller would otherwise read a wall of flags instead of the
	// one fact that matters.
	if !p.bhyvectlSupportsCheckpoint(ctx) {
		return fmt.Errorf("this host's bhyvectl has no --suspend: checkpointing needs a kernel " +
			"built with BHYVE_SNAPSHOT, which GENERIC is not")
	}

	// bhyvectl --suspend saves state and terminates the VM process.
	if output, err := p.cmd().CombinedOutput(ctx, "bhyvectl", fmt.Sprintf("--suspend=%s", ckptFile), fmt.Sprintf("--vm=%s", vmName)); err != nil {
		return fmt.Errorf("failed to checkpoint VM: %w (output: %s)", err, string(output))
	}

	st.State = provider.StateStopped
	st.PID = 0
	if err := p.saveVMState(vmDir, st); err != nil {
		return fmt.Errorf("failed to update VM state after checkpoint: %w", err)
	}

	logger := logging.WithComponent("bhyve")
	logger.Info("VM checkpointed", logging.FieldVM, vmName, "checkpoint", name)
	return nil
}

// RestoreCheckpoint resumes a stopped bhyve VM from a checkpoint file.
//
// Passes -r <checkpointFile> to bhyve so it restores memory and device state.
func (p *BhyveProvider) RestoreCheckpoint(ctx context.Context, handle provider.InstanceHandle, name string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	_, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()
	if err := validation.ValidateSnapshotName(name); err != nil {
		return fmt.Errorf("invalid checkpoint name: %w", err)
	}

	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	ckptFile := filepath.Join(vmDir, checkpointSubDir, name+checkpointExt)
	if _, err := os.Stat(ckptFile); err != nil {
		return fmt.Errorf("checkpoint %q not found: %w", name, err)
	}

	st, err := p.loadVMState(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM state: %w", err)
	}
	if st.State != provider.StateStopped {
		return fmt.Errorf("VM must be stopped to restore a checkpoint (current state: %s)", st.State)
	}

	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	args, err := p.buildBhyveArgs(config)
	if err != nil {
		return fmt.Errorf("failed to build bhyve arguments: %w", err)
	}
	// Insert "-r <ckptFile>" before the VM name (always the last argument).
	vmArg := args[len(args)-1]
	args = append(args[:len(args)-1], "-r", ckptFile, vmArg)

	logPath := filepath.Join(vmDir, "bhyve.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("failed to create log file: %w", err)
	}

	cmd := exec.Command("bhyve", args...)
	cmd.Dir = vmDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to restore checkpoint: %w", err)
	}

	go func() {
		_ = cmd.Wait()
		logFile.Close()
	}()

	st.State = provider.StateRunning
	st.PID = cmd.Process.Pid
	if err := p.saveVMState(vmDir, st); err != nil {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		return fmt.Errorf("failed to save VM state after restore: %w", err)
	}

	logger := logging.WithComponent("bhyve")
	logger.Info("VM restored from checkpoint", logging.FieldVM, vmName, "checkpoint", name)
	return nil
}

// ListCheckpoints returns all .ckpt files in <vmDir>/checkpoints/.
func (p *BhyveProvider) ListCheckpoints(_ context.Context, handle provider.InstanceHandle) ([]provider.CheckpointInfo, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmDir := filepath.Join(p.dataDir, handle.ID)
	ckptDir := filepath.Join(vmDir, checkpointSubDir)

	if _, err := os.Stat(ckptDir); os.IsNotExist(err) {
		return []provider.CheckpointInfo{}, nil
	}

	entries, err := os.ReadDir(ckptDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoints directory: %w", err)
	}

	var checkpoints []provider.CheckpointInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), checkpointExt) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		checkpoints = append(checkpoints, provider.CheckpointInfo{
			Name:      strings.TrimSuffix(entry.Name(), checkpointExt),
			CreatedAt: info.ModTime(),
			SizeMB:    float64(info.Size()) / (1024 * 1024),
		})
	}

	return checkpoints, nil
}

// DeleteCheckpoint removes a checkpoint file.
func (p *BhyveProvider) DeleteCheckpoint(_ context.Context, handle provider.InstanceHandle, name string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validation.ValidateSnapshotName(name); err != nil {
		return fmt.Errorf("invalid checkpoint name: %w", err)
	}

	ckptFile := filepath.Join(p.dataDir, handle.ID, checkpointSubDir, name+checkpointExt)
	if _, err := os.Stat(ckptFile); os.IsNotExist(err) {
		return fmt.Errorf("checkpoint %q not found", name)
	}

	if err := os.Remove(ckptFile); err != nil {
		return fmt.Errorf("failed to delete checkpoint: %w", err)
	}

	return nil
}

// PauseInstance sends SIGSTOP to the bhyve process, freezing execution.
func (p *BhyveProvider) PauseInstance(ctx context.Context, handle provider.InstanceHandle) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	st, err := p.loadVMState(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM state: %w", err)
	}
	if st.State != provider.StateRunning {
		return fmt.Errorf("VM is not running (current state: %s)", st.State)
	}
	// Freezing a recycled PID would stop a process that has nothing to do with
	// this VM, and leave it stopped.
	if !p.pidIsBhyveVM(ctx, st.PID, vmName) {
		return fmt.Errorf("process %d is no longer this VM; refusing to signal it", st.PID)
	}

	if err := syscall.Kill(st.PID, syscall.SIGSTOP); err != nil {
		return fmt.Errorf("failed to pause VM process (pid %d): %w", st.PID, err)
	}

	st.State = provider.StatePaused
	if err := p.saveVMState(vmDir, st); err != nil {
		// Unfreeze the process since we could not record the state change.
		_ = syscall.Kill(st.PID, syscall.SIGCONT)
		return fmt.Errorf("failed to save VM state: %w", err)
	}

	return nil
}

// ResumeInstance sends SIGCONT to a paused bhyve process.
func (p *BhyveProvider) ResumeInstance(ctx context.Context, handle provider.InstanceHandle) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	st, err := p.loadVMState(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM state: %w", err)
	}
	if st.State != provider.StatePaused {
		return fmt.Errorf("VM is not paused (current state: %s)", st.State)
	}
	if !p.pidIsBhyveVM(ctx, st.PID, vmName) {
		return fmt.Errorf("process %d is no longer this VM; refusing to signal it", st.PID)
	}

	if err := syscall.Kill(st.PID, syscall.SIGCONT); err != nil {
		return fmt.Errorf("failed to resume VM process (pid %d): %w", st.PID, err)
	}

	st.State = provider.StateRunning
	if err := p.saveVMState(vmDir, st); err != nil {
		return fmt.Errorf("failed to save VM state: %w", err)
	}

	return nil
}

// RenameInstance renames a stopped bhyve VM.
//
// Steps:
//  1. Verify the VM is stopped.
//  2. Rename the ZFS parent dataset (which renames all child datasets/ZVOLs).
//  3. Rename the VM directory on disk.
//  4. Update vm.conf (name, disk paths, tap names, console path).
//  5. Update vm.state (name, console path).
//
// The caller (API handler) is responsible for updating the datastore record.
// repointIntoNewVMDir rewrites the paths a VM keeps inside its own directory so
// they follow a rename.
//
// Anything outside that directory — a zvol, an ISO in the image directory — is
// left alone: it did not move.
func repointIntoNewVMDir(config *vmConfig, oldVMDir, newVMDir string) {
	move := func(path string) string {
		if path == "" {
			return path
		}
		if path == oldVMDir {
			return newVMDir
		}
		prefix := oldVMDir + string(filepath.Separator)
		if strings.HasPrefix(path, prefix) {
			return filepath.Join(newVMDir, strings.TrimPrefix(path, prefix))
		}
		return path
	}

	config.UEFIVars = move(config.UEFIVars)
	config.TPMSockPath = move(config.TPMSockPath)
	for i, disk := range config.DiskPaths {
		config.DiskPaths[i] = move(disk)
	}
}

func (p *BhyveProvider) RenameInstance(ctx context.Context, handle provider.InstanceHandle, newName string) error {
	// Both names, and before the lock: taking one on an unvalidated name
	// creates an entry for it, and the old name goes straight into a path.
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if err := validation.ValidateInstanceName(newName); err != nil {
		return fmt.Errorf("invalid new name: %w", err)
	}

	ctx, releaseLock, lockErr := p.locks.AcquireAll(ctx, handle.ID, newName)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()

	oldName := handle.ID
	oldVMDir := filepath.Join(p.dataDir, oldName)
	newVMDir := filepath.Join(p.dataDir, newName)

	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get VM state: %w", err)
	}
	if state != provider.StateStopped {
		return fmt.Errorf("VM must be stopped to rename (current state: %s)", state)
	}

	if _, err := os.Stat(newVMDir); err == nil {
		return fmt.Errorf("VM %q already exists", newName)
	}

	config, err := p.loadVMConfig(oldVMDir)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	// Rename ZFS parent dataset (e.g., zroot/hospitus/bhyve/<oldName>).
	// zfs rename is recursive, so all child datasets (ZVOLs) are renamed too.
	oldZFSParent := fmt.Sprintf("%s/%s", p.zfsParent, oldName)
	newZFSParent := fmt.Sprintf("%s/%s", p.zfsParent, newName)

	zfsExists := p.cmd().Run(ctx, "zfs", "list", "-H", oldZFSParent) == nil
	if zfsExists {
		if output, err := p.cmd().CombinedOutput(ctx, "zfs", "rename", oldZFSParent, newZFSParent); err != nil {
			return fmt.Errorf("failed to rename ZFS dataset: %w (output: %s)", err, string(output))
		}
		// Update disk paths to reflect the renamed ZFS parent.
		for i, path := range config.DiskPaths {
			config.DiskPaths[i] = strings.ReplaceAll(path,
				"/dev/zvol/"+oldZFSParent,
				"/dev/zvol/"+newZFSParent)
		}
	}

	// Regenerated, not substituted: tapDeviceName hashes any name that would
	// exceed the 15-character interface limit, so a textual replacement breaks
	// that contract — and it also rewrites any other place the old name happens
	// to appear inside the device name.
	for i := range config.TapDevs {
		config.TapDevs[i] = tapDeviceName(newName, i)
	}

	// Update console device path.
	if config.Console != "" {
		config.Console = strings.ReplaceAll(config.Console, oldName, newName)
	}

	// The rest of the paths inside the VM's directory have to follow it too.
	// The UEFI variable store bites first: UEFI is the default, so a bootrom
	// argument naming the old directory left every renamed VM unable to start.
	repointIntoNewVMDir(config, oldVMDir, newVMDir)

	config.Name = newName

	// Rename the VM directory.
	if err := os.Rename(oldVMDir, newVMDir); err != nil {
		// Roll back ZFS rename if we already did it.
		if zfsExists {
			_ = p.cmd().Run(ctx, "zfs", "rename", newZFSParent, oldZFSParent)
		}
		return fmt.Errorf("failed to rename VM directory: %w", err)
	}

	// Persist updated config in the new directory.
	if err := p.saveVMConfig(newVMDir, config); err != nil {
		// Best-effort rollback.
		_ = os.Rename(newVMDir, oldVMDir)
		if zfsExists {
			_ = p.cmd().Run(ctx, "zfs", "rename", newZFSParent, oldZFSParent)
		}
		return fmt.Errorf("failed to save updated VM config: %w", err)
	}

	// Update state file. The two steps above undo their work on failure; this
	// one did not, so a VM whose state could not be written was left with the
	// new directory, the new dataset and a vm.conf naming the new VM, while
	// vm.state still named the old one.
	undoRename := func() {
		_ = os.Rename(newVMDir, oldVMDir)
		if zfsExists {
			_ = p.cmd().Run(ctx, "zfs", "rename", newZFSParent, oldZFSParent)
		}
	}
	st, err := p.loadVMState(newVMDir)
	if err != nil {
		undoRename()
		return fmt.Errorf("failed to load VM state: %w", err)
	}
	st.Name = newName
	st.Console = config.Console
	if err := p.saveVMState(newVMDir, st); err != nil {
		undoRename()
		return fmt.Errorf("failed to update VM state: %w", err)
	}

	logger := logging.WithComponent("bhyve")
	logger.Info("VM renamed", "old_name", oldName, "new_name", newName)
	return nil
}

// bhyvectlSupportsCheckpoint reports whether this host's bhyvectl can suspend a
// VM to disk.
//
// The capability comes from the kernel, not the tool: bhyvectl only grows
// --suspend and --resume when the kernel is built with BHYVE_SNAPSHOT. FreeBSD
// ships GENERIC without it, so on a stock host checkpointing cannot work at
// all — and bhyvectl says so only by printing its usage.
func (p *BhyveProvider) bhyvectlSupportsCheckpoint(ctx context.Context) bool {
	// --help exits non-zero on some versions; the output is what matters.
	out, _ := p.cmd().CombinedOutput(ctx, "bhyvectl", "--help")
	return strings.Contains(string(out), "--suspend")
}
