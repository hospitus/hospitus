package qemu

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// CreateInstance creates a new QEMU VM
func (p *QEMUProvider) CreateInstance(ctx context.Context, spec provider.InstanceSpec) (_ provider.InstanceHandle, err error) {
	// Attach provider context so the API layer can tell a failed create from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("qemu", "create", spec.Name, err) }()

	vmName := spec.Name

	if err := validation.ValidateInstanceName(vmName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid instance name: %w", err)
	}

	if err := validation.ValidateResourceLimits(spec.CPUs, spec.MemoryMB); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid resource limits: %w", err)
	}

	// Lock to prevent TOCTOU race between existence check and VM directory creation.
	// This serializes concurrent CreateInstance calls for the same provider.
	p.createMu.Lock()
	defer p.createMu.Unlock()

	// Check if VM already exists (now protected by mutex)
	exists, err := p.vmExists(ctx, vmName)
	if err != nil {
		return provider.InstanceHandle{}, err
	}
	if exists {
		return provider.InstanceHandle{}, provider.ErrInstanceExists
	}

	// Determine architecture (default to host arch)
	arch := runtime.GOARCH
	if spec.Arch != "" && spec.Arch != "native" {
		arch = spec.Arch
	}
	if archStr, ok := spec.ProviderConfig["arch"].(string); ok && archStr != "" && archStr != "native" {
		arch = archStr
	}

	// Validate architecture against detected capabilities
	caps := p.Capabilities()
	validArch := false
	for _, a := range caps.SupportedArchitectures {
		if a == arch {
			validArch = true
			break
		}
	}
	if !validArch {
		return provider.InstanceHandle{}, fmt.Errorf("unsupported architecture %q: supported architectures: %v", arch, caps.SupportedArchitectures)
	}

	// Get QEMU binary for this architecture
	qemuBin := p.qemuBinaries[arch]
	if qemuBin == "" {
		return provider.InstanceHandle{}, fmt.Errorf("no QEMU binary found for architecture %s", arch)
	}

	// Create VM directory
	vmDir := filepath.Join(p.dataDir, vmName)

	// QEMU's control sockets live in that directory, and a Unix socket path is
	// limited to a little over 100 bytes. Check it here rather than letting the
	// VM be created, its disk copied, and only then have QEMU refuse to start
	// with a message about a path the operator never typed.
	if err := checkSocketPathLimit(vmDir); err != nil {
		return provider.InstanceHandle{}, err
	}

	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create VM directory: %w", err)
	}

	// Resolve image path if specified
	var backingFile string
	var isoPath string
	if spec.Image != "" {
		// Check if it's an ISO image
		if strings.HasSuffix(strings.ToLower(spec.Image), ".iso") || spec.OSType == "iso" {
			resolved, err := p.resolveISOPath(spec.Image)
			if err != nil {
				if rmErr := os.RemoveAll(vmDir); rmErr != nil {
					p.logger.Warn("failed to clean up VM directory", "path", vmDir)
				}
				return provider.InstanceHandle{}, err
			}
			isoPath = resolved
		} else {
			// Cloud image - must exist
			resolved, err := p.resolveCloudImagePath(spec.Image, arch)
			if err != nil {
				if rmErr := os.RemoveAll(vmDir); rmErr != nil {
					p.logger.Warn("failed to clean up VM directory", "path", vmDir)
				}
				return provider.InstanceHandle{}, err
			}
			backingFile = resolved
		}
	}

	var diskPath string

	switch {
	case len(spec.Disks) > 0 && spec.Disks[0].Type == provider.DiskTypePhysical:
		// Physical device passthrough: boot an existing /dev disk directly
		// (e.g. an external disk with an installed OS). The device is used
		// as-is and is never created or destroyed by Hospitus.
		devPath := spec.Disks[0].Path
		if err := validatePhysicalDisk(devPath); err != nil {
			if rmErr := os.RemoveAll(vmDir); rmErr != nil {
				p.logger.Warn("failed to clean up VM directory", "path", vmDir)
			}
			return provider.InstanceHandle{}, err
		}
		diskPath = devPath

	case backingFile != "" || isoPath != "" || len(spec.Disks) > 0:
		// Determine disk size (default 10GB)
		diskSizeGB := 10
		if len(spec.Disks) > 0 && spec.Disks[0].SizeGB > 0 {
			diskSizeGB = spec.Disks[0].SizeGB
		}

		diskPath = filepath.Join(vmDir, "disk0.qcow2")

		// For ISO boot, create empty disk; for cloud image, create disk with backing file.
		if err := p.createDiskImage(ctx, diskPath, diskSizeGB, provider.DiskTypeQCOW2, backingFile); err != nil {
			if rmErr := os.RemoveAll(vmDir); rmErr != nil {
				p.logger.Warn("failed to clean up VM directory", "path", vmDir)
			}
			return provider.InstanceHandle{}, fmt.Errorf("failed to create disk image: %w", err)
		}

		// Record the created disk path on the spec so it is persisted in the
		// saved config and available to GetInstanceInfo and snapshot operations.
		if len(spec.Disks) == 0 {
			spec.Disks = []provider.DiskSpec{{Type: provider.DiskTypeQCOW2, SizeGB: diskSizeGB, Path: diskPath}}
		} else {
			spec.Disks[0].Path = diskPath
		}
	}

	// Generate cloud-init ISO if needed (for cloud images or when cloud-init config is provided)
	var cloudInitISO string
	if p.needsCloudInit(spec) && backingFile != "" {
		cloudInitISO = filepath.Join(vmDir, "cloud-init.iso")
		if err := p.generateCloudInitISO(ctx, spec, cloudInitISO); err != nil {
			// Clean up on failure
			if rmErr := os.RemoveAll(vmDir); rmErr != nil {
				p.logger.Warn("failed to clean up VM directory", "path", vmDir)
			}
			return provider.InstanceHandle{}, fmt.Errorf("failed to generate cloud-init ISO: %w", err)
		}
	}

	// If we have an ISO for installation, add it as cdrom
	if isoPath != "" {
		// Store ISO path in spec for buildQEMUConfig to use
		if spec.ProviderConfig == nil {
			spec.ProviderConfig = make(map[string]interface{})
		}
		spec.ProviderConfig["install_iso"] = isoPath
	}

	// Build QEMU command line and persist it. The port allocation inside
	// buildQEMUConfig and the write that records it are held under one lock, so
	// a concurrent create or start cannot read the state files in between and
	// hand out the same port twice.
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	p.hostPortMu.Lock()
	qemuConfig := p.buildQEMUConfig(spec, qemuBin, diskPath, cloudInitISO, vmDir, arch)
	saveErr := p.saveVMConfig(qemuConfig, configPath)
	p.hostPortMu.Unlock()
	if err := saveErr; err != nil {
		// Clean up on failure
		if rmErr := os.RemoveAll(vmDir); rmErr != nil {
			p.logger.Warn("failed to clean up VM directory", "path", vmDir)
		}
		return provider.InstanceHandle{}, fmt.Errorf("failed to save VM configuration: %w", err)
	}

	handle := provider.InstanceHandle{
		ID:       vmName,
		Provider: "qemu",
		Metadata: map[string]interface{}{
			"vm_dir":     vmDir,
			"qemu_bin":   qemuBin,
			"arch":       arch,
			"disk_path":  diskPath,
			"qmp_socket": qemuConfig.QMPSocket,
		},
	}

	return handle, nil
}

// StartInstance starts a QEMU VM
func (p *QEMUProvider) StartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	// Attach provider context so the API layer can tell a failed start from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("qemu", "start", handle.ID, err) }()
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}

	vmName := handle.ID

	// Check if already running
	running, err := p.isVMRunning(ctx, vmName)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("VM %s is already running", vmName)
	}

	// Load VM configuration
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	vmConfig, err := p.loadVMConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load VM configuration: %w", err)
	}

	// The host ports are settled here rather than at create: the arguments are
	// stored once and replayed, so a port that was free when the VM was created
	// can belong to another VM by the time it starts.
	//
	// The probe and the write are held under one lock. Guarding only the probe
	// let two VMs starting at the same moment read the same state, pick the same
	// free port, and the second QEMU die with "Could not set up host forwarding
	// rule".
	oldSSH, _ := storedPort(vmConfig, "ssh_port")
	oldVNC, _ := storedPort(vmConfig, "vnc_port")
	p.hostPortMu.Lock()
	changed := p.refreshHostPorts(vmConfig, vmName)
	var saveErr error
	if changed {
		saveErr = p.saveVMConfig(vmConfig, configPath)
	}
	p.hostPortMu.Unlock()
	if saveErr != nil {
		// QEMU is about to be launched on the refreshed ports, so a state file
		// still advertising the old ones is worse than not starting: every
		// later lookup — and the next allocation — would be wrong.
		newSSH, _ := storedPort(vmConfig, "ssh_port")
		newVNC, _ := storedPort(vmConfig, "vnc_port")
		return fmt.Errorf("failed to record the new host ports (ssh %d→%d, vnc %d→%d): %w",
			oldSSH, newSSH, oldVNC, newVNC, saveErr)
	}

	// VM directory where QEMU writes its PID file
	vmDir := filepath.Join(p.dataDir, vmName)
	qemuPidFile := filepath.Join(vmDir, "qemu.pid")

	// Remove stale PID file if exists
	if err := os.Remove(qemuPidFile); err != nil && !os.IsNotExist(err) {
		p.logger.Warn("failed to remove stale QEMU pid file", "path", qemuPidFile)
	}

	// Through the injected runner, like every other external tool this
	// provider calls: StartInstance was the one command line no test could
	// observe. QEMU is invoked with -daemonize, so it forks, closes its
	// standard streams and exits — CombinedOutput returns as soon as it does.
	output, err := p.cmd().CombinedOutput(ctx, vmConfig.QEMUBin, vmConfig.Args...)
	if err != nil {
		if msg := strings.TrimSpace(string(output)); msg != "" {
			return provider.NewProviderError("qemu", "start", vmName,
				fmt.Errorf("failed to start QEMU: %w\nQEMU error: %s", err, msg))
		}
		return provider.NewProviderError("qemu", "start", vmName,
			fmt.Errorf("failed to start QEMU: %w", err))
	}

	// Wait for QEMU to write its PID file (up to 5 seconds)
	var pid int
	for i := 0; i < 50; i++ {
		pidData, err := os.ReadFile(qemuPidFile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(pidData)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if pid == 0 {
		return fmt.Errorf("QEMU failed to start: no PID file created")
	}

	// Create PID file in state dir for lifecycle operations (stop, pause, resume)
	statePidFile := filepath.Join(p.stateDir, fmt.Sprintf("%s.pid", vmName))
	if err := os.WriteFile(statePidFile, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		// Kill the VM — without the PID file, future lifecycle ops will fail
		process, _ := os.FindProcess(pid)
		if process != nil {
			_ = process.Kill()
		}
		return fmt.Errorf("failed to write PID state file: %w", err)
	}

	// Verify the process is still running
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find QEMU process: %w", err)
	}

	// Check process is alive using signal 0
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return fmt.Errorf("QEMU process died shortly after start")
	}

	return nil
}

// StopInstance stops a QEMU VM
func (p *QEMUProvider) StopInstance(ctx context.Context, handle provider.InstanceHandle, opts provider.StopOptions) (err error) {
	// Attach provider context so the API layer can tell a failed stop from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("qemu", "stop", handle.ID, err) }()
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}

	vmName := handle.ID

	// Check if running
	running, err := p.isVMRunning(ctx, vmName)
	if err != nil {
		return err
	}
	if !running {
		return nil // Already stopped
	}

	// Get PID
	pidFile := filepath.Join(p.stateDir, fmt.Sprintf("%s.pid", vmName))
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		return fmt.Errorf("invalid PID: %w", err)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process: %w", err)
	}

	// Try graceful shutdown first via QMP
	if !opts.Force {
		qmpSocket := p.qmpSocketFor(handle.ID)
		if qmpSocket != "" {
			qmp, err := NewQMPClient(qmpSocket)
			if err == nil {
				if qmp.Connect() == nil {
					defer qmp.Close()
					if qmp.SystemPowerdown() == nil {
						// Wait for shutdown, honoring the context and polling at a
						// sub-second interval instead of a 1s goroutine loop.
						timeout := opts.Timeout
						if timeout == 0 {
							timeout = 30 * time.Second
						}
						if p.waitForProcessExit(ctx, process, timeout) {
							// Clean up PID file
							if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
								p.logger.Warn("failed to remove pid file", "path", pidFile)
							}
							return nil
						}
					}
				}
			}
		}
	}

	// A canceled request is not a reason to kill a guest. waitForProcessExit
	// reports "did not exit" for both a timeout and a cancellation, and the
	// escalation below cannot tell them apart: a client that hung up during a
	// graceful shutdown had its VM killed outright.
	if ctx.Err() != nil {
		return fmt.Errorf("stop of %s was interrupted while the guest was shutting down; it is still running: %w", vmName, ctx.Err())
	}

	// Re-verify the PID is still this VM's QEMU process immediately before
	// killing (defense in depth against PID reuse between the running check
	// and here). If it no longer is, the VM is already gone.
	if !p.pidBelongsToVM(ctx, pid, vmName) {
		if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
			p.logger.Warn("failed to remove pid file", "path", pidFile)
		}
		return nil
	}

	// Force kill if graceful shutdown failed or was not requested
	if err := process.Kill(); err != nil {
		return fmt.Errorf("failed to kill process: %w", err)
	}

	// Kill only queues the signal. isVMRunning decides a VM is stopped from the
	// absence of this file, so removing it before the process is gone let
	// StartInstance pass its "already running" check while the old QEMU still
	// held the disk and the sockets.
	// Liveness, not identity: pidBelongsToVM answers false both for "the
	// process is gone" and for "the probe did not run", and only the first is
	// grounds for removing the PID file.
	gone := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			gone = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !gone {
		return fmt.Errorf("QEMU (pid %d) for %s did not exit after SIGKILL", pid, vmName)
	}

	// Clean up PID file
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		p.logger.Warn("failed to remove pid file", "path", pidFile)
	}

	return nil
}

// RestartInstance restarts a QEMU VM
func (p *QEMUProvider) RestartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	// Attach provider context so the API layer can tell a failed restart from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("qemu", "restart", handle.ID, err) }()
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}

	// Stop then start
	if err := p.StopInstance(ctx, handle, provider.StopOptions{}); err != nil {
		return err
	}

	// Small delay to ensure clean shutdown
	time.Sleep(1 * time.Second)

	return p.StartInstance(ctx, handle)
}

// DeleteInstance deletes a QEMU VM
func (p *QEMUProvider) DeleteInstance(ctx context.Context, handle provider.InstanceHandle, force bool) (err error) {
	// Attach provider context so the API layer can tell a failed delete from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("qemu", "delete", handle.ID, err) }()
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}

	vmName := handle.ID

	// Stop the VM BEFORE taking createMu. Graceful shutdown can block up to the
	// stop timeout, and holding createMu across it would stall every concurrent
	// create/delete for the whole duration. The config file still exists here, so
	// a racing CreateInstance would fail its uniqueness check rather than corrupt.
	running, err := p.isVMRunning(ctx, vmName)
	if err != nil {
		return err
	}
	if running {
		if err := p.StopInstance(ctx, handle, provider.StopOptions{Force: force}); err != nil {
			return err
		}
	}

	// Serialize only the state-file removal against CreateInstance (TOCTOU).
	p.createMu.Lock()
	defer p.createMu.Unlock()

	// Get VM directory
	// The directory is derived from the validated name, never from handle
	// metadata: the API lets a client rewrite provider_config into the handle,
	// and a path taken from there would be removed here as root.
	vmDir := filepath.Join(p.dataDir, vmName)

	// Remove VM directory (includes disk images)
	if err := os.RemoveAll(vmDir); err != nil {
		return fmt.Errorf("failed to remove VM directory: %w", err)
	}

	// Remove configuration file
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove configuration file: %w", err)
	}

	// Remove PID file if exists
	pidFile := filepath.Join(p.stateDir, fmt.Sprintf("%s.pid", vmName))
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		p.logger.Warn("failed to remove pid file", "path", pidFile)
	}

	return nil
}

// GetInstanceState returns the current state of a VM
func (p *QEMUProvider) GetInstanceState(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceState, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return "", fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	// Check if VM configuration exists
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return provider.StateUnknown, provider.ErrInstanceNotFound
	}

	// Check if running
	running, err := p.isVMRunning(ctx, vmName)
	if err != nil {
		return provider.StateUnknown, err
	}

	if running {
		// Surface the paused state persisted by PauseInstance. Without this the
		// "paused" flag was written to config but never reflected back to callers.
		if cfg, err := p.loadVMConfig(configPath); err == nil && cfg != nil {
			if paused, ok := cfg.Spec.ProviderConfig["paused"].(bool); ok && paused {
				return provider.StatePaused, nil
			}
		}
		return provider.StateRunning, nil
	}

	return provider.StateStopped, nil
}

// GetInstanceInfo returns detailed information about a VM
func (p *QEMUProvider) GetInstanceInfo(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceInfo, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return provider.InstanceInfo{}, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	// Load configuration
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	vmConfig, err := p.loadVMConfig(configPath)
	if err != nil {
		return provider.InstanceInfo{}, provider.ErrInstanceNotFound
	}

	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return provider.InstanceInfo{}, err
	}

	info := provider.InstanceInfo{
		Handle: handle,
		State:  state,
		Spec:   vmConfig.Spec,
	}

	// Get runtime info if running
	if state == provider.StateRunning {
		// Get PID
		pidFile := filepath.Join(p.stateDir, fmt.Sprintf("%s.pid", vmName))
		if pidData, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(pidData))); err == nil {
				info.PID = pid
			}
		}

		// Best-effort: get IP addresses via guest agent
		if qmpSocket := p.qmpSocketFor(handle.ID); qmpSocket != "" {
			if ips := queryGuestIPs(qmpSocket); len(ips) > 0 {
				info.IPAddresses = ips
			}
		}
	}

	return info, nil
}

// ListInstances lists all VMs
func (p *QEMUProvider) ListInstances(ctx context.Context, filter provider.InstanceFilter) ([]provider.InstanceHandle, error) {
	// List all configuration files
	pattern := filepath.Join(p.stateDir, "*.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to list configuration files: %w", err)
	}

	handles := make([]provider.InstanceHandle, 0, len(files))
	for _, file := range files {
		// Extract VM name from filename
		vmName := strings.TrimSuffix(filepath.Base(file), ".json")

		handle := provider.InstanceHandle{
			ID:       vmName,
			Provider: "qemu",
			Metadata: map[string]interface{}{
				"vm_dir": filepath.Join(p.dataDir, vmName),
				// Populate qmp_socket so QMP-backed operations (stop, snapshots,
				// port-forwards) work on handles obtained from ListInstances;
				// otherwise they silently no-op for lack of the socket path.
				"qmp_socket": p.qmpSocketFor(vmName),
			},
		}

		// Apply filter if specified
		if len(filter.States) > 0 {
			state, err := p.GetInstanceState(ctx, handle)
			if err != nil {
				continue
			}

			match := false
			for _, filterState := range filter.States {
				if state == filterState {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}

		handles = append(handles, handle)
	}

	return handles, nil
}

// waitForProcessExit polls signal-0 on the process until it exits or the
// timeout/context elapses. Returns true if the process exited in time.
func (p *QEMUProvider) waitForProcessExit(ctx context.Context, process *os.Process, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := process.Signal(syscall.Signal(0)); err != nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-ticker.C:
		}
	}
}

// qmpSocketFor returns the QMP socket path for a VM: the one recorded in its
// config, or the conventional <dataDir>/<vmName>/qmp.sock as a fallback.
func (p *QEMUProvider) qmpSocketFor(vmName string) string {
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	if config, err := p.loadVMConfig(configPath); err == nil && config != nil && config.QMPSocket != "" {
		return config.QMPSocket
	}
	return filepath.Join(p.dataDir, vmName, "qmp.sock")
}

// SetInstanceResources updates resource limits for a VM
func (p *QEMUProvider) SetInstanceResources(ctx context.Context, handle provider.InstanceHandle, resources provider.ResourceSpec) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	// QEMU requires VM restart to change CPU/memory
	// Could implement via QMP for some resources
	return provider.ErrUnsupportedOperation
}

// AttachNetwork attaches a network interface to a running VM via QMP.
func (p *QEMUProvider) AttachNetwork(ctx context.Context, handle provider.InstanceHandle, network provider.NetworkAttachment) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	// The attachment's own fields reach the QMP arguments below — the bridge
	// name, the id that becomes a netdev — so they are checked before the
	// connection is even opened.
	if err := validation.ValidateNetworkSpec(network.Network); err != nil {
		return fmt.Errorf("invalid network attachment: %w", err)
	}

	qmpSocket := p.qmpSocketFor(handle.ID)
	if qmpSocket == "" {
		return fmt.Errorf("QMP socket not found for VM %s", handle.ID)
	}

	qmp, err := NewQMPClient(qmpSocket)
	if err != nil {
		return fmt.Errorf("failed to create QMP client: %w", err)
	}
	if err := qmp.Connect(); err != nil {
		return fmt.Errorf("failed to connect to QMP: %w", err)
	}
	defer qmp.Close()

	netdevID := fmt.Sprintf("net-%s", network.Network.ID)
	deviceID := fmt.Sprintf("virtio-net-%s", network.Network.ID)

	// Add the netdev backend
	netdevProps := map[string]interface{}{}
	if network.Network.Bridge != "" {
		// Bridge mode - use tap device
		// The ID arrives on a caller-supplied NetworkAttachment and nothing
		// bounds its length, so slicing it took the provider down on any name
		// shorter than eight bytes. The tap name is capped, not assumed.
		shortID := network.Network.ID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		ifname := fmt.Sprintf("tap-%s", shortID)
		if err := validation.ValidateInterfaceName(ifname); err != nil {
			return fmt.Errorf("network id %q yields an unusable tap name: %w", network.Network.ID, err)
		}
		netdevProps["ifname"] = ifname
		netdevProps["br"] = network.Network.Bridge
		if err := qmp.NetdevAdd("tap", netdevID, netdevProps); err != nil {
			return fmt.Errorf("failed to add netdev: %w", err)
		}
	} else {
		// User mode NAT
		if err := qmp.NetdevAdd("user", netdevID, netdevProps); err != nil {
			return fmt.Errorf("failed to add netdev: %w", err)
		}
	}

	// Add the virtio-net device
	deviceProps := map[string]interface{}{
		"id":     deviceID,
		"netdev": netdevID,
	}
	if network.Network.MAC != "" {
		deviceProps["mac"] = network.Network.MAC
	}

	if err := qmp.DeviceAdd("virtio-net-pci", deviceProps); err != nil {
		_ = qmp.NetdevDel(netdevID)
		return fmt.Errorf("failed to attach network device: %w", err)
	}

	return nil
}

// Compile-time assertion: QEMUProvider implements RenameProvider
var _ provider.RenameProvider = (*QEMUProvider)(nil)

// RenameInstance renames a stopped QEMU VM.
//
// Steps:
//  1. Validate new name and check VM is stopped.
//  2. Rename data directory (dataDir/old → dataDir/new).
//  3. Rename state config file (stateDir/old.json → stateDir/new.json).
//  4. Update config (name, QMP socket path, spec name, QEMU -name arg).
//  5. Clean up old PID file if present.
func (p *QEMUProvider) RenameInstance(ctx context.Context, handle provider.InstanceHandle, newName string) error {
	if err := validation.ValidateInstanceName(newName); err != nil {
		return fmt.Errorf("invalid new name: %w", err)
	}

	oldName := handle.ID

	// Check VM is stopped
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get VM state: %w", err)
	}
	if state != provider.StateStopped {
		return fmt.Errorf("VM must be stopped to rename (current state: %s)", state)
	}

	// Check new name doesn't already exist
	exists, err := p.vmExists(ctx, newName)
	if err != nil {
		return fmt.Errorf("failed to check for existing VM: %w", err)
	}
	if exists {
		return fmt.Errorf("VM %q already exists", newName)
	}

	// Load existing config
	oldConfigPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", oldName))
	config, err := p.loadVMConfig(oldConfigPath)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	oldVMDir := filepath.Join(p.dataDir, oldName)
	newVMDir := filepath.Join(p.dataDir, newName)

	// Rename data directory
	if err := os.Rename(oldVMDir, newVMDir); err != nil {
		return fmt.Errorf("failed to rename VM directory: %w", err)
	}

	// Update config fields
	config.Name = newName
	config.Spec.Name = newName
	config.QMPSocket = filepath.Join(newVMDir, "qmp.sock")

	// Update -name arg in QEMU args
	for i, arg := range config.Args {
		if arg == "-name" && i+1 < len(config.Args) {
			config.Args[i+1] = newName
			break
		}
	}

	// Update any paths in args that reference the old directory. Only rewrite on a
	// path boundary: a blind ReplaceAll(oldVMDir, newVMDir) would also corrupt a
	// different VM whose directory shares the prefix (e.g. ".../web" vs ".../web2").
	oldPrefix := oldVMDir + string(os.PathSeparator)
	newPrefix := newVMDir + string(os.PathSeparator)
	for i, arg := range config.Args {
		switch {
		case arg == oldVMDir:
			config.Args[i] = newVMDir
		case strings.Contains(arg, oldPrefix):
			config.Args[i] = strings.ReplaceAll(arg, oldPrefix, newPrefix)
		}
	}

	// The recorded spec has to follow too. Only the arguments were rewritten,
	// so the disks kept naming the old directory: the VM ran, but snapshots
	// reported "disk image not found" and info showed a path that no longer
	// existed.
	for i, disk := range config.Spec.Disks {
		switch {
		case disk.Path == oldVMDir:
			config.Spec.Disks[i].Path = newVMDir
		case strings.HasPrefix(disk.Path, oldPrefix):
			config.Spec.Disks[i].Path = newPrefix + strings.TrimPrefix(disk.Path, oldPrefix)
		}
	}
	// Every recorded string under the VM's directory, found by prefix rather
	// than by a list of keys: install_iso was the only one named, so
	// qga_socket kept pointing at the old directory and ExecCommand dialed a
	// socket that no longer existed. A list would have missed the next one
	// too.
	for key, value := range config.Spec.ProviderConfig {
		str, ok := value.(string)
		if !ok {
			continue
		}
		switch {
		case str == oldVMDir:
			config.Spec.ProviderConfig[key] = newVMDir
		case strings.HasPrefix(str, oldPrefix):
			config.Spec.ProviderConfig[key] = newPrefix + strings.TrimPrefix(str, oldPrefix)
		}
	}

	// Save config to new path
	newConfigPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", newName))
	if err := p.saveVMConfig(config, newConfigPath); err != nil {
		// Best-effort rollback: rename directory back
		_ = os.Rename(newVMDir, oldVMDir)
		return fmt.Errorf("failed to save renamed config: %w", err)
	}

	// Remove old config file. Not a warning: ListInstances globs *.json, so a
	// leftover file makes the VM appear twice — once under a name whose
	// directory no longer exists — and the next rename of the old name would
	// find a config that should be gone.
	if err := os.Remove(oldConfigPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("renamed to %s but the old config file %s could not be removed: %w",
			newName, oldConfigPath, err)
	}

	// Clean up old PID file (VM is stopped, so this is just stale cleanup)
	oldPidFile := filepath.Join(p.stateDir, fmt.Sprintf("%s.pid", oldName))
	if err := os.Remove(oldPidFile); err != nil && !os.IsNotExist(err) {
		p.logger.Warn("failed to remove old pid file", "path", oldPidFile)
	}

	return nil
}

// DetachNetwork detaches a network interface from a running VM via QMP.
func (p *QEMUProvider) DetachNetwork(ctx context.Context, handle provider.InstanceHandle, interfaceID string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	qmpSocket := p.qmpSocketFor(handle.ID)
	if qmpSocket == "" {
		return fmt.Errorf("QMP socket not found for VM %s", handle.ID)
	}

	qmp, err := NewQMPClient(qmpSocket)
	if err != nil {
		return fmt.Errorf("failed to create QMP client: %w", err)
	}
	if err := qmp.Connect(); err != nil {
		return fmt.Errorf("failed to connect to QMP: %w", err)
	}
	defer qmp.Close()

	deviceID := fmt.Sprintf("virtio-net-%s", interfaceID)
	netdevID := fmt.Sprintf("net-%s", interfaceID)

	// Remove the device first
	if err := qmp.DeviceDel(deviceID); err != nil {
		return fmt.Errorf("failed to remove network device: %w", err)
	}

	// Remove the netdev backend
	if err := qmp.NetdevDel(netdevID); err != nil {
		return fmt.Errorf("failed to remove netdev: %w", err)
	}

	return nil
}

var _ provider.InstanceAddressProvider = (*QEMUProvider)(nil)

// InstanceAddresses reports the addresses the guest agent knows about.
func (p *QEMUProvider) InstanceAddresses(_ context.Context, handle provider.InstanceHandle) ([]net.IP, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	qmpSocket := p.qmpSocketFor(handle.ID)
	if qmpSocket == "" {
		return nil, nil
	}
	return queryGuestIPs(qmpSocket), nil
}
