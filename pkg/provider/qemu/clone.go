package qemu

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Compile-time assertion: QEMUProvider implements CloneProvider
var _ provider.CloneProvider = (*QEMUProvider)(nil)

// CloneInstance creates a new VM by cloning an existing one.
//
// For linked clones, uses qemu-img create with backing file (fast, space-efficient).
// For full clones, copies the disk image (independent, more space).
// The source VM must be stopped.
func (p *QEMUProvider) CloneInstance(ctx context.Context, source provider.InstanceHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error) {
	if err := validation.ValidateInstanceName(cloneName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid clone name: %w", err)
	}

	sourceName := source.ID

	// Check source VM is stopped
	state, err := p.GetInstanceState(ctx, source)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to get source VM state: %w", err)
	}
	if state != provider.StateStopped {
		return provider.InstanceHandle{}, fmt.Errorf("source VM must be stopped to clone (current state: %s)", state)
	}

	// Check clone doesn't already exist
	exists, err := p.vmExists(ctx, cloneName)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to check for existing VM: %w", err)
	}
	if exists {
		return provider.InstanceHandle{}, fmt.Errorf("VM %q already exists", cloneName)
	}

	// Load source config
	sourceConfigPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", sourceName))
	sourceConfig, err := p.loadVMConfig(sourceConfigPath)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to load source VM config: %w", err)
	}

	// Find source disk
	sourceVMDir := filepath.Join(p.dataDir, sourceName)
	sourceDisk := filepath.Join(sourceVMDir, "disk0.qcow2")
	if _, err := os.Stat(sourceDisk); os.IsNotExist(err) {
		return provider.InstanceHandle{}, fmt.Errorf("source disk not found: %s", sourceDisk)
	}

	// Create clone directory
	cloneVMDir := filepath.Join(p.dataDir, cloneName)
	if err := os.MkdirAll(cloneVMDir, 0o755); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create clone directory: %w", err)
	}

	cloneDisk := filepath.Join(cloneVMDir, "disk0.qcow2")
	if err := p.cloneDisk(ctx, sourceDisk, cloneDisk, opts.LinkedClone); err != nil {
		_ = os.RemoveAll(cloneVMDir)
		return provider.InstanceHandle{}, fmt.Errorf("failed to clone disk: %w", err)
	}

	cloneConfig, err := p.buildCloneConfig(sourceConfig, cloneName, cloneVMDir, cloneDisk, opts)
	if err != nil {
		_ = os.RemoveAll(cloneVMDir)
		return provider.InstanceHandle{}, fmt.Errorf("failed to build clone config: %w", err)
	}

	// Save clone config
	cloneConfigPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", cloneName))
	if err := p.saveVMConfig(cloneConfig, cloneConfigPath); err != nil {
		_ = os.RemoveAll(cloneVMDir)
		return provider.InstanceHandle{}, fmt.Errorf("failed to save clone config: %w", err)
	}

	return provider.InstanceHandle{
		ID:       cloneName,
		Provider: "qemu",
		Metadata: map[string]interface{}{
			"vm_dir":      cloneVMDir,
			"disk_path":   cloneDisk,
			"cloned_from": sourceName,
			"qmp_socket":  cloneConfig.QMPSocket,
		},
	}, nil
}

// CloneFromSnapshot creates a new VM from a snapshot of an existing VM.
//
// Uses qemu-img convert to extract the snapshot state into a new disk.
func (p *QEMUProvider) CloneFromSnapshot(ctx context.Context, snapshot provider.SnapshotHandle, cloneName string, opts provider.CloneOptions) (provider.InstanceHandle, error) {
	if err := validation.ValidateInstanceName(cloneName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid clone name: %w", err)
	}

	// Check clone doesn't already exist
	exists, err := p.vmExists(ctx, cloneName)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to check for existing VM: %w", err)
	}
	if exists {
		return provider.InstanceHandle{}, fmt.Errorf("VM %q already exists", cloneName)
	}

	sourceName := snapshot.Instance

	// Load source config
	sourceConfigPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", sourceName))
	sourceConfig, err := p.loadVMConfig(sourceConfigPath)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to load source VM config: %w", err)
	}

	// Find source disk
	sourceVMDir := filepath.Join(p.dataDir, sourceName)
	sourceDisk := filepath.Join(sourceVMDir, "disk0.qcow2")
	if _, err := os.Stat(sourceDisk); os.IsNotExist(err) {
		return provider.InstanceHandle{}, fmt.Errorf("source disk not found: %s", sourceDisk)
	}

	// Create clone directory
	cloneVMDir := filepath.Join(p.dataDir, cloneName)
	if err := os.MkdirAll(cloneVMDir, 0o755); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create clone directory: %w", err)
	}

	// Extract snapshot to a new disk using qemu-img convert
	cloneDisk := filepath.Join(cloneVMDir, "disk0.qcow2")
	snapshotName := snapshot.Metadata["snapshot_name"]
	if snapshotName == nil {
		// Parse from snapshot ID: "vmname_snapshotname"
		parts := strings.SplitN(snapshot.ID, "_", 2)
		if len(parts) == 2 {
			snapshotName = parts[1]
		} else {
			_ = os.RemoveAll(cloneVMDir)
			return provider.InstanceHandle{}, fmt.Errorf("cannot determine snapshot name from handle")
		}
	}

	if output, err := p.cmd().CombinedOutput(ctx, "qemu-img", "convert",
		"-l", fmt.Sprintf("snapshot.name=%s", snapshotName),
		"-O", "qcow2", sourceDisk, cloneDisk); err != nil {
		_ = os.RemoveAll(cloneVMDir)
		return provider.InstanceHandle{}, fmt.Errorf("failed to extract snapshot: %w (output: %s)", err, string(output))
	}

	cloneConfig, err := p.buildCloneConfig(sourceConfig, cloneName, cloneVMDir, cloneDisk, opts)
	if err != nil {
		_ = os.RemoveAll(cloneVMDir)
		return provider.InstanceHandle{}, fmt.Errorf("failed to build clone config: %w", err)
	}

	// Save clone config
	cloneConfigPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", cloneName))
	if err := p.saveVMConfig(cloneConfig, cloneConfigPath); err != nil {
		_ = os.RemoveAll(cloneVMDir)
		return provider.InstanceHandle{}, fmt.Errorf("failed to save clone config: %w", err)
	}

	return provider.InstanceHandle{
		ID:       cloneName,
		Provider: "qemu",
		Metadata: map[string]interface{}{
			"vm_dir":        cloneVMDir,
			"disk_path":     cloneDisk,
			"cloned_from":   sourceName,
			"from_snapshot": snapshotName,
			"qmp_socket":    cloneConfig.QMPSocket,
		},
	}, nil
}

// cloneDisk copies or creates a linked clone of a disk image.
func (p *QEMUProvider) cloneDisk(ctx context.Context, sourceDisk, destDisk string, linked bool) error {
	if linked {
		// Linked clone: create a new qcow2 with the source as backing file
		if output, err := p.cmd().CombinedOutput(ctx, "qemu-img", "create",
			"-f", "qcow2",
			"-b", sourceDisk,
			"-F", "qcow2",
			destDisk); err != nil {
			return fmt.Errorf("qemu-img create failed: %w (output: %s)", err, string(output))
		}
		return nil
	}

	// Full clone: convert to independent copy
	if output, err := p.cmd().CombinedOutput(ctx, "qemu-img", "convert",
		"-O", "qcow2", sourceDisk, destDisk); err != nil {
		return fmt.Errorf("qemu-img convert failed: %w (output: %s)", err, string(output))
	}
	return nil
}

// buildCloneConfig creates a new vmConfig for a clone, updating names and paths.
func (p *QEMUProvider) buildCloneConfig(source *vmConfig, cloneName, cloneVMDir, cloneDisk string, opts provider.CloneOptions) (*vmConfig, error) {
	sourceVMDir := filepath.Join(p.dataDir, source.Name)

	// Deep-copy the spec via JSON round-trip. A failure here would otherwise
	// leave cloneSpec zero-valued, producing a clone with no CPU/memory/disk
	// configuration silently persisted to the datastore.
	specBytes, err := json.Marshal(source.Spec)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal source spec for clone: %w", err)
	}
	var cloneSpec provider.InstanceSpec
	if err := json.Unmarshal(specBytes, &cloneSpec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cloned spec: %w", err)
	}

	cloneSpec.Name = cloneName

	// The host ports belong to the source, not to the copy. Left in place, the
	// clone's state file claimed them, and at the source's next start
	// allocatePort saw its own port taken and moved the source off the port the
	// operator had been told about. refreshHostPorts below gives the clone its
	// own.
	delete(cloneSpec.ProviderConfig, "ssh_port")
	delete(cloneSpec.ProviderConfig, "vnc_port")

	// Apply resource overrides
	if opts.CPUs > 0 {
		cloneSpec.CPUs = opts.CPUs
	}
	if opts.MemoryMB > 0 {
		cloneSpec.MemoryMB = opts.MemoryMB
	}

	// Apply label/annotation overrides (previously silently dropped).
	if len(opts.Labels) > 0 {
		if cloneSpec.Labels == nil {
			cloneSpec.Labels = make(map[string]string, len(opts.Labels))
		}
		for k, v := range opts.Labels {
			cloneSpec.Labels[k] = v
		}
	}
	if len(opts.Annotations) > 0 {
		if cloneSpec.Annotations == nil {
			cloneSpec.Annotations = make(map[string]string, len(opts.Annotations))
		}
		for k, v := range opts.Annotations {
			cloneSpec.Annotations[k] = v
		}
	}

	// Update disk paths in spec
	if len(cloneSpec.Disks) > 0 {
		cloneSpec.Disks[0].Path = cloneDisk
	}

	// Honor ResetMAC: give the clone fresh MACs so it does not collide with the
	// source on the same L2 segment. Clear the spec MACs and rewrite the mac=
	// fragments baked into the QEMU args.
	if opts.ResetMAC {
		for i := range cloneSpec.Networks {
			cloneSpec.Networks[i].MAC = ""
		}
	}

	// Build new args by rewriting source-dir paths and setting -name explicitly.
	newArgs := rewriteCloneArgs(source.Args, sourceVMDir, cloneVMDir, cloneName)

	if opts.ResetMAC {
		if err := rewriteMACsInArgs(newArgs); err != nil {
			return nil, fmt.Errorf("failed to reset clone MAC addresses: %w", err)
		}
	}

	// Update memory/CPU args if overridden
	if opts.MemoryMB > 0 {
		for i, arg := range newArgs {
			if arg == "-m" && i+1 < len(newArgs) {
				newArgs[i+1] = fmt.Sprintf("%d", opts.MemoryMB)
				break
			}
		}
	}
	if opts.CPUs > 0 {
		for i, arg := range newArgs {
			if arg == "-smp" && i+1 < len(newArgs) {
				newArgs[i+1] = fmt.Sprintf("%d", opts.CPUs)
				break
			}
		}
	}

	clone := &vmConfig{
		Name:      cloneName,
		QEMUBin:   source.QEMUBin,
		Args:      newArgs,
		QMPSocket: filepath.Join(cloneVMDir, "qmp.sock"),
		Spec:      cloneSpec,
	}

	// Give the clone host ports of its own, on the arguments it will be started
	// with. Without this its command line still carries the source's forwards.
	p.hostPortMu.Lock()
	p.assignCloneHostPorts(clone, cloneName)
	p.hostPortMu.Unlock()

	return clone, nil
}

// assignCloneHostPorts allocates the clone's own SSH and VNC ports and writes
// them into both its arguments and its spec.
//
// refreshSSHForward acts only on a config that already records an ssh_port,
// which the clone deliberately does not: the record is what marks a forward as
// one Hospitus allocated. So the SSH port is seeded here, from the source's
// arguments, before the refresh runs.
func (p *QEMUProvider) assignCloneHostPorts(clone *vmConfig, cloneName string) {
	if clone.Spec.ProviderConfig == nil {
		clone.Spec.ProviderConfig = map[string]interface{}{}
	}
	// The arguments are still the source's, so they name the ports the source
	// holds. Seed them as placeholders the refresh moves off, and reserve them
	// so the clone cannot be handed one of them back: the source's state file
	// is not a reliable record of its VNC display.
	var reserved []int
	for i, arg := range clone.Args {
		if match := sshHostForward.FindStringSubmatch(arg); match != nil {
			if port, err := strconv.Atoi(match[1]); err == nil {
				clone.Spec.ProviderConfig["ssh_port"] = port
				reserved = append(reserved, port)
			}
			continue
		}
		if i > 0 && clone.Args[i-1] == "-vnc" {
			if display := vncDisplay.FindStringSubmatch(arg); display != nil {
				if n, err := strconv.Atoi(display[1]); err == nil {
					reserved = append(reserved, vncBasePort+n)
				}
			}
		}
	}
	p.refreshHostPorts(clone, cloneName, reserved...)
}

// rewriteMACsInArgs replaces every "mac=<addr>" fragment inside the QEMU
// arguments with a freshly generated MAC, in place. Each occurrence gets its own
// address so multi-NIC clones do not collide.
func rewriteMACsInArgs(args []string) error {
	for i, arg := range args {
		if !strings.Contains(arg, "mac=") {
			continue
		}
		fields := strings.Split(arg, ",")
		for j, field := range fields {
			if !strings.HasPrefix(field, "mac=") {
				continue
			}
			newMAC, err := generateMAC()
			if err != nil {
				return err
			}
			fields[j] = "mac=" + newMAC
		}
		args[i] = strings.Join(fields, ",")
	}
	return nil
}

// generateMAC returns a random locally-administered unicast MAC using the
// 52:54:00 QEMU/KVM OUI prefix.
func generateMAC() (string, error) {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", buf[0], buf[1], buf[2]), nil
}

// rewriteCloneArgs produces the QEMU argument list for a clone. It rewrites
// only references to the source VM directory (disk/firmware paths) and sets
// -name to the clone name. It deliberately does NOT do a blind
// strings.ReplaceAll of the source VM name across every argument: the VM name
// is arbitrary user text and can appear by accident in unrelated args (MAC
// addresses, netdev ids, cpu models, disk formats), which such a replace would
// silently corrupt.
func rewriteCloneArgs(args []string, sourceVMDir, cloneVMDir, cloneName string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i, arg := range out {
		out[i] = strings.ReplaceAll(arg, sourceVMDir, cloneVMDir)
	}
	for i, arg := range out {
		if arg == "-name" && i+1 < len(out) {
			out[i+1] = cloneName
			break
		}
	}
	return out
}
