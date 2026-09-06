package vfkit

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// lookPath is exec.LookPath, indirected so tests can pretend a binary exists.
var lookPath = exec.LookPath

// CreateInstance prepares a VM: its directory, its disk and its saved config.
// Nothing is started here.
func (p *VFKitProvider) CreateInstance(ctx context.Context, spec provider.InstanceSpec) (_ provider.InstanceHandle, err error) {
	defer func() { err = provider.WrapError("vfkit", "create", spec.Name, err) }()

	if err := validation.ValidateInstanceName(spec.Name); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid VM name: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Only a genuine absence may continue. A stat that failed on I/O or on
	// permissions says nothing about whether the VM is there, and treating it
	// as absent lets prepareDisk truncate an existing disk and cleanup remove
	// the directory it lives in.
	if _, statErr := os.Stat(p.configPath(spec.Name)); statErr == nil {
		return provider.InstanceHandle{}, provider.ErrInstanceExists
	} else if !os.IsNotExist(statErr) {
		return provider.InstanceHandle{}, fmt.Errorf("checking for an existing VM: %w", statErr)
	}

	vmDir := p.vmDir(spec.Name)
	if err := os.MkdirAll(vmDir, 0o750); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create VM directory: %w", err)
	}

	diskPath := filepath.Join(vmDir, "disk.img")
	if err := p.prepareDisk(ctx, spec, diskPath); err != nil {
		p.cleanupVMDir(vmDir)
		return provider.InstanceHandle{}, err
	}

	mac, err := randomMAC()
	if err != nil {
		p.cleanupVMDir(vmDir)
		return provider.InstanceHandle{}, err
	}

	config := &vmConfig{
		Name:       spec.Name,
		CPUs:       orDefault(spec.CPUs, 1),
		MemoryMB:   orDefault64(spec.MemoryMB, 1024),
		DiskPath:   diskPath,
		EFIStore:   filepath.Join(vmDir, "efistore.nvram"),
		MACAddress: mac,
		Created:    time.Now().Format(time.RFC3339),
	}

	if err := p.saveConfig(config); err != nil {
		p.cleanupVMDir(vmDir)
		return provider.InstanceHandle{}, err
	}

	return provider.InstanceHandle{
		ID:       spec.Name,
		Provider: "vfkit",
		Metadata: map[string]interface{}{"disk": diskPath},
	}, nil
}

// cleanupVMDir removes a half-built VM directory. A failure here is not worth
// reporting over the failure that caused it.
func (p *VFKitProvider) cleanupVMDir(vmDir string) {
	if err := os.RemoveAll(vmDir); err != nil {
		p.logWarn(context.Background(), "failed to clean up a half-created VM directory",
			"dir", vmDir, logging.FieldError, err)
	}
}

// prepareDisk puts a raw disk in place.
//
// Virtualization.framework reads raw images only, so a qcow2 from the image
// catalog is converted rather than copied. Without a source image the VM gets an
// empty disk to install onto.
func (p *VFKitProvider) prepareDisk(ctx context.Context, spec provider.InstanceSpec, diskPath string) error {
	sizeGB := int64(20)
	if len(spec.Disks) > 0 && spec.Disks[0].SizeGB > 0 {
		sizeGB = int64(spec.Disks[0].SizeGB)
	}

	if spec.Image == "" {
		return p.createBlankDisk(diskPath, sizeGB)
	}

	source, err := p.resolveImage(spec.Image)
	if err != nil {
		return err
	}

	if strings.HasSuffix(source, ".raw") || strings.HasSuffix(source, ".img") {
		if err := copyFile(source, diskPath); err != nil {
			return fmt.Errorf("failed to copy image: %w", err)
		}
		return nil
	}

	if p.qemuImg == "" {
		return fmt.Errorf("image %q needs converting to raw and qemu-img was not found (install it with: brew install qemu)", spec.Image)
	}
	if output, err := p.cmd().CombinedOutput(ctx, p.qemuImg, "convert", "-O", "raw", source, diskPath); err != nil {
		return fmt.Errorf("failed to convert image to raw: %w (output: %s)", err, string(output))
	}
	return nil
}

// resolveImage finds a downloaded image by name.
func (p *VFKitProvider) resolveImage(image string) (string, error) {
	if filepath.IsAbs(image) {
		if _, err := os.Stat(image); err != nil {
			return "", fmt.Errorf("image %q not found", image)
		}
		// The file is copied or converted into the VM disk as root, so it must
		// be one of ours; any other path would hand the guest a host file.
		within, err := validation.PathWithinAny(image, p.imageDir, p.dataDir)
		if err != nil {
			return "", fmt.Errorf("resolving image path: %w", err)
		}
		if !within {
			return "", fmt.Errorf("image path not allowed: %s must be under %s or %s", image, p.imageDir, p.dataDir)
		}
		return image, nil
	}

	// Confined like the absolute branch above: enough "../" segments in a
	// relative name walk out of the catalog, and prepareDisk then copies
	// whatever was found into the guest disk.
	for _, ext := range []string{"", ".raw", ".img", ".qcow2"} {
		candidate := filepath.Join(p.imageDir, "cloud", image+ext)
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		within, err := validation.PathWithinAny(candidate, p.imageDir)
		if err != nil {
			return "", fmt.Errorf("resolving image path: %w", err)
		}
		if !within {
			return "", fmt.Errorf("image %q resolves outside the image directory %s", image, p.imageDir)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("image %q not found in %s; download it with 'hospitus image fetch %s'",
		image, filepath.Join(p.imageDir, "cloud"), image)
}

func (p *VFKitProvider) createBlankDisk(path string, sizeGB int64) error {
	// 0600 explicitly: os.Create opens at 0666 before umask, so on a host with
	// a permissive umask the guest's disk would be world-readable.
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create disk: %w", err)
	}
	defer file.Close()

	// A sparse file: the bytes are only committed as the guest writes them.
	if err := file.Truncate(sizeGB * 1024 * 1024 * 1024); err != nil {
		return fmt.Errorf("failed to size disk: %w", err)
	}
	return nil
}

// buildArgs assembles the vfkit command line for a VM.
func (p *VFKitProvider) buildArgs(config *vmConfig) []string {
	args := []string{
		"--cpus", strconv.Itoa(config.CPUs),
		"--memory", strconv.FormatInt(config.MemoryMB, 10),
		// create makes the variable store on first boot and reuses it after, so
		// boot entries written by the guest survive a restart.
		"--bootloader", "efi,variable-store=" + config.EFIStore + ",create",
		"--device", "virtio-blk,path=" + config.DiskPath,
		"--device", "virtio-net,nat,mac=" + config.MACAddress,
		"--device", "virtio-rng",
		"--device", "virtio-serial,logFilePath=" + filepath.Join(p.vmDir(config.Name), "serial.log"),
		"--restful-uri", "unix://" + p.restSocket(config.Name),
		"--pidfile", p.pidPath(config.Name),
	}

	for _, path := range config.CloudInit {
		args = append(args, "--cloud-init", path)
	}
	return args
}

// StartInstance boots the VM as its own process.
func (p *VFKitProvider) StartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	defer func() { err = provider.WrapError("vfkit", "start", handle.ID, err) }()

	p.mu.Lock()
	defer p.mu.Unlock()

	config, err := p.loadConfig(handle.ID)
	if err != nil {
		return provider.ErrInstanceNotFound
	}

	if p.isRunning(handle.ID) {
		return nil
	}

	// A socket left by a previous run would make vfkit fail to bind.
	if err := os.Remove(p.restSocket(handle.ID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clear the stale control socket: %w", err)
	}

	// Detach from the daemon: the VM must outlive whatever started it, so it
	// gets its own process group and does not inherit the request's context.
	cmd := exec.Command(p.vfkitBin, p.buildArgs(config)...) //nolint:gosec // arguments are built from a validated config
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	logFile, err := os.OpenFile(filepath.Join(p.vmDir(handle.ID), "vfkit.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open the VM log: %w", err)
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start vfkit: %w", err)
	}

	// Reap the child without supervising it: hospitusd is not its process manager,
	// but leaving it unwaited would make it a zombie.
	reaped := make(chan struct{})
	go func() { _ = cmd.Wait(); close(reaped) }()

	if err := p.waitForControlSocket(ctx, handle.ID); err != nil {
		// The caller is told the start failed, so the process it started must
		// not survive: left running, a later retry found the VM up and the
		// operator had a machine nobody meant to have started.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			// Kill only queues the signal. Returning before the process is
			// gone let an immediate retry start a second vfkit on the same
			// disk, both writing it.
			select {
			case <-reaped:
			case <-time.After(10 * time.Second):
			}
		}
		_ = os.Remove(p.pidPath(handle.ID))
		return err
	}
	return nil
}

// waitForControlSocket blocks until the VM answers on its control socket, so a
// start that failed is reported as a failure rather than as a running VM.
func (p *VFKitProvider) waitForControlSocket(ctx context.Context, name string) error {
	deadline := time.Now().Add(15 * time.Second)
	socket := p.restSocket(name)

	for time.Now().Before(deadline) {
		if _, err := p.rest().State(ctx, socket); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}

	return fmt.Errorf("vfkit did not answer on its control socket within 15s; see %s",
		filepath.Join(p.vmDir(name), "vfkit.log"))
}

// StopInstance asks the VM to shut down, and kills it if it will not.
func (p *VFKitProvider) StopInstance(ctx context.Context, handle provider.InstanceHandle, opts provider.StopOptions) (err error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	defer func() { err = provider.WrapError("vfkit", "stop", handle.ID, err) }()

	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopLocked(ctx, handle, opts)
}

// stopLocked is StopInstance with the provider lock already held, so a caller
// that has to stop a VM as part of a longer transition — DeleteInstance —
// keeps the whole transition serialized instead of dropping the lock between
// the stop and what follows it.
func (p *VFKitProvider) stopLocked(ctx context.Context, handle provider.InstanceHandle, opts provider.StopOptions) error {
	if !p.isRunning(handle.ID) {
		return nil
	}

	socket := p.restSocket(handle.ID)
	wanted := "Stop"
	if opts.Force {
		wanted = "HardStop"
	}

	if err := p.rest().SetState(ctx, socket, wanted); err != nil {
		// The control socket is the polite route; a VM that will not answer it
		// still has to be stoppable.
		p.logWarn(ctx, "control socket refused the stop; signaling the process",
			"vm", handle.ID, logging.FieldError, err)
		return p.signalStop(handle.ID, opts.Force)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !p.isRunning(handle.ID) {
			p.removeSocket(ctx, handle.ID)
			return nil
		}
		// A bare sleep ignores cancellation: the caller was already gone and
		// this held the provider lock for the rest of the timeout.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}

	return p.signalStop(handle.ID, true)
}

func (p *VFKitProvider) signalStop(name string, force bool) error {
	pid, err := p.readPID(name)
	if err != nil {
		return nil // Nothing to stop.
	}

	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	if err := syscall.Kill(pid, signal); err != nil {
		return fmt.Errorf("failed to signal vfkit (pid %d): %w", pid, err)
	}

	// Kill only queues the signal. DeleteInstance removes the disk directory
	// as soon as this returns, so a vfkit still writing would have its image
	// unlinked underneath it. Wait for the process to actually be gone.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			p.removeSocket(context.Background(), name)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("vfkit (pid %d) did not exit after %s", pid, signal)
}

// removeSocket clears the control socket of a stopped VM so the next start can
// bind it again.
func (p *VFKitProvider) removeSocket(ctx context.Context, name string) {
	if err := os.Remove(p.restSocket(name)); err != nil && !os.IsNotExist(err) {
		p.logWarn(ctx, "failed to remove the VM control socket", "vm", name, logging.FieldError, err)
	}
}

// RestartInstance stops the VM and starts it again.
func (p *VFKitProvider) RestartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	defer func() { err = provider.WrapError("vfkit", "restart", handle.ID, err) }()

	if err := p.StopInstance(ctx, handle, provider.StopOptions{}); err != nil {
		return err
	}
	return p.StartInstance(ctx, handle)
}

// DeleteInstance removes the VM and everything it owns.
func (p *VFKitProvider) DeleteInstance(ctx context.Context, handle provider.InstanceHandle, force bool) (err error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	defer func() { err = provider.WrapError("vfkit", "delete", handle.ID, err) }()

	// The running check, the forced stop and the removal are one transition.
	// Taking the lock only for the removal left a window in which a concurrent
	// StartInstance brought the VM back up between the stop and the delete, and
	// its files then vanished underneath it.
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isRunning(handle.ID) {
		if !force {
			return fmt.Errorf("VM is running; stop it first or pass force")
		}
		if err := p.stopLocked(ctx, handle, provider.StopOptions{Force: true}); err != nil {
			return err
		}
	}

	p.removeSocket(ctx, handle.ID)
	for _, path := range []string{p.pidPath(handle.ID), p.configPath(handle.ID)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove %s: %w", path, err)
		}
	}

	if err := os.RemoveAll(p.vmDir(handle.ID)); err != nil {
		return fmt.Errorf("failed to remove the VM directory: %w", err)
	}
	return nil
}

// GetInstanceState reports whether the VM is running, and what it says about
// itself when it is.
func (p *VFKitProvider) GetInstanceState(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceState, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return "", fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if _, err := p.loadConfig(handle.ID); err != nil {
		return provider.StateUnknown, provider.ErrInstanceNotFound
	}

	if !p.isRunning(handle.ID) {
		return provider.StateStopped, nil
	}

	state, err := p.rest().State(ctx, p.restSocket(handle.ID))
	if err != nil {
		// The process is alive but not answering yet: it is still coming up.
		return provider.StateStarting, nil
	}

	switch strings.ToLower(normalizeVMState(state.State)) {
	case "running":
		return provider.StateRunning, nil
	case "paused":
		return provider.StatePaused, nil
	case "stopped":
		return provider.StateStopped, nil
	case "starting":
		return provider.StateStarting, nil
	case "stopping":
		return provider.StateStopping, nil
	default:
		return provider.StateUnknown, nil
	}
}

// normalizeVMState strips the prefix vfkit puts on its state names. The REST API
// answers "VirtualMachineStateRunning" where its documentation shows "Running",
// and comparing the documented form matched nothing, so every running VM read as
// an unknown state.
func normalizeVMState(state string) string {
	return strings.TrimPrefix(state, "VirtualMachineState")
}

// GetInstanceInfo returns what is known about a VM.
func (p *VFKitProvider) GetInstanceInfo(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceInfo, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return provider.InstanceInfo{}, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	config, err := p.loadConfig(handle.ID)
	if err != nil {
		return provider.InstanceInfo{}, provider.ErrInstanceNotFound
	}

	state, _ := p.GetInstanceState(ctx, handle)

	info := provider.InstanceInfo{
		Handle: handle,
		State:  state,
		Spec: provider.InstanceSpec{
			Name:     config.Name,
			CPUs:     config.CPUs,
			MemoryMB: config.MemoryMB,
		},
		MACAddresses: []string{config.MACAddress},
	}
	if pid, err := p.readPID(handle.ID); err == nil {
		info.PID = pid
	}
	return info, nil
}

// ListInstances returns the VMs this provider knows about.
func (p *VFKitProvider) ListInstances(_ context.Context, _ provider.InstanceFilter) ([]provider.InstanceHandle, error) {
	entries, err := os.ReadDir(p.stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read the state directory: %w", err)
	}

	var handles []provider.InstanceHandle
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".json")
		if name == entry.Name() {
			continue // Not a config file.
		}
		handles = append(handles, provider.InstanceHandle{ID: name, Provider: "vfkit"})
	}
	return handles, nil
}

// isRunning reports whether the VM's process is alive.
func (p *VFKitProvider) isRunning(name string) bool {
	pid, err := p.readPID(name)
	if err != nil {
		return false
	}
	// Signal 0 tests for existence without touching the process.
	return syscall.Kill(pid, 0) == nil
}

func (p *VFKitProvider) readPID(name string) (int, error) {
	data, err := os.ReadFile(p.pidPath(name))
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("unreadable pidfile for %s: %w", name, err)
	}
	return pid, nil
}

// randomMAC generates a locally administered unicast address, so two VMs on the
// same host never collide.
func randomMAC() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate a MAC address: %w", err)
	}
	buf[0] = (buf[0] | 0x02) &^ 0x01 // Locally administered, unicast.

	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		buf[0], buf[1], buf[2], buf[3], buf[4], buf[5]), nil
}

// copyFile streams rather than buffering: this copies a VM disk image, which
// is routinely several GiB, and reading it whole put all of that in the
// daemon's heap at once.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func orDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func orDefault64(value, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}
