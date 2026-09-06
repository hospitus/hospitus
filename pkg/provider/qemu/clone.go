package qemu

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
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

	if err := p.cloneExtraDisks(ctx, sourceConfig, sourceVMDir, cloneVMDir, opts.LinkedClone); err != nil {
		_ = os.RemoveAll(cloneVMDir)
		return provider.InstanceHandle{}, err
	}

	cloneConfig, err := p.buildCloneConfig(sourceConfig, sourceVMDir, cloneName, cloneVMDir, cloneDisk, opts)
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

	// The resolver's answers are what gets used, not merely checked: calling it
	// and then reading the handle's own fields left a handle that resolves to
	// one snapshot and one disk while carrying the names of another, and
	// qemu-img extracted the second.
	snapshotName, sourceDisk, err := p.snapshotTargetFor(snapshot)
	if err != nil {
		return provider.InstanceHandle{}, err
	}
	sourceName := snapshot.Instance

	// Load source config
	sourceConfigPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", sourceName))
	sourceConfig, err := p.loadVMConfig(sourceConfigPath)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to load source VM config: %w", err)
	}

	// sourceDisk comes from the resolver above, which confined it and bound it
	// to this instance; rebuilding it here from the name would have re-admitted
	// exactly what the resolver refuses.
	if _, err := os.Stat(sourceDisk); os.IsNotExist(err) {
		return provider.InstanceHandle{}, fmt.Errorf("source disk not found: %s", sourceDisk)
	}

	// Create clone directory
	cloneVMDir := filepath.Join(p.dataDir, cloneName)
	if err := os.MkdirAll(cloneVMDir, 0o755); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create clone directory: %w", err)
	}

	// Extract snapshot to a new disk using qemu-img convert
	// snapshotName also comes from the resolver, which already ran it through
	// ValidateSnapshotName — the name is interpolated into qemu-img's
	// "-l snapshot.name=..." value, a comma-separated option list.
	cloneDisk := filepath.Join(cloneVMDir, "disk0.qcow2")

	if output, err := p.cmd().CombinedOutput(ctx, "qemu-img", "convert",
		"-l", fmt.Sprintf("snapshot.name=%s", snapshotName),
		"-O", "qcow2", sourceDisk, cloneDisk); err != nil {
		_ = os.RemoveAll(cloneVMDir)
		return provider.InstanceHandle{}, fmt.Errorf("failed to extract snapshot: %w (output: %s)", err, string(output))
	}

	// The snapshot only ever covered disk0 — "qemu-img convert -l" reads one
	// image — so the other disks are copied from their current state; naming
	// files that do not exist would be worse.
	//
	// Only from a stopped VM, though: copying a qcow2 a running QEMU is
	// writing to yields a torn image. Cloning from a snapshot does not require
	// the source to be stopped, unlike CloneInstance, so the state is checked
	// here rather than assumed.
	sourceVMDir := filepath.Join(p.dataDir, sourceName)
	sourceState, err := p.GetInstanceState(ctx, provider.InstanceHandle{ID: sourceName})
	if err != nil {
		_ = os.RemoveAll(cloneVMDir)
		return provider.InstanceHandle{}, fmt.Errorf("failed to get source VM state: %w", err)
	}
	if sourceState == provider.StateStopped {
		if err := p.cloneExtraDisks(ctx, sourceConfig, sourceVMDir, cloneVMDir, opts.LinkedClone); err != nil {
			_ = os.RemoveAll(cloneVMDir)
			return provider.InstanceHandle{}, err
		}
	} else {
		// Counted the way cloneExtraDisks counts them: a VM whose other disks
		// all live outside its own directory copies nothing, and refusing it
		// for being up would have been refusing it for no reason.
		extra, err := p.extraDisksToCopy(sourceConfig, sourceVMDir)
		if err != nil {
			_ = os.RemoveAll(cloneVMDir)
			return provider.InstanceHandle{}, err
		}
		if extra > 0 {
			_ = os.RemoveAll(cloneVMDir)
			return provider.InstanceHandle{}, fmt.Errorf(
				"source VM %s has %d disks beyond the snapshot and must be stopped to copy them (current state: %s)",
				sourceName, extra, sourceState)
		}
	}

	cloneConfig, err := p.buildCloneConfig(sourceConfig, sourceVMDir, cloneName, cloneVMDir, cloneDisk, opts)
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
		// "-F" tells qemu the backing file's format and stops it from probing,
		// so declaring qcow2 for a raw image writes a clone that reads its
		// backing store wrongly. The name says .qcow2 by convention; what the
		// file is is a question for qemu-img.
		backingFormat, err := p.detectImageFormat(ctx, sourceDisk)
		if err != nil {
			return err
		}

		// Linked clone: create a new qcow2 with the source as backing file
		if output, err := p.cmd().CombinedOutput(ctx, "qemu-img", "create",
			"-f", "qcow2",
			"-b", sourceDisk,
			"-F", backingFormat,
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
func (p *QEMUProvider) buildCloneConfig(source *vmConfig, sourceVMDir, cloneName, cloneVMDir, cloneDisk string, opts provider.CloneOptions) (*vmConfig, error) {
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

	// Every disk, not only the first: rewriteCloneArgs below rewrites every
	// argument naming the source directory, so a multi-disk source produced a
	// clone whose command line pointed at its own directory while its
	// persisted spec still named the source's files.
	for i := range cloneSpec.Disks {
		if i == 0 {
			cloneSpec.Disks[i].Path = cloneDisk
			continue
		}
		// The same containment test cloneExtraDisks uses, so the two agree on
		// which disks the clone owns: a lexical prefix replace here and a
		// resolved check there would rewrite a path nothing had copied, or
		// leave a copied file unreferenced.
		within, err := validation.PathWithinAny(cloneSpec.Disks[i].Path, sourceVMDir)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve disk %s: %w", cloneSpec.Disks[i].Path, err)
		}
		if !within {
			continue
		}
		cloneSpec.Disks[i].Path = strings.Replace(
			cloneSpec.Disks[i].Path, sourceVMDir, cloneVMDir, 1)
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

	// The rewrite points those arguments at the clone's directory; the files
	// themselves have to follow. Only disk0.qcow2 was ever copied, so a source
	// using cloud-init or UEFI produced a clone whose QEMU failed at start on a
	// path that named nothing.
	for _, name := range []string{"cloud-init.iso", "efivars.fd"} {
		src := filepath.Join(sourceVMDir, name)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspecting %s for the clone: %w", name, err)
		}
		if err := copyFile(src, filepath.Join(cloneVMDir, name)); err != nil {
			return nil, fmt.Errorf("copying %s into the clone: %w", name, err)
		}
	}

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

// copyFile streams one per-VM file into the clone's directory.
//
// Streamed rather than read whole: cloud-init.iso is small, but this is the
// same helper shape the vfkit provider needed for multi-gigabyte images and
// there is no reason to buffer here either.
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
	// On a path boundary, not on a bare prefix: "/vms/web" also prefixes
	// "/vms/web2", so cloning "web" rewrote paths belonging to a sibling VM
	// and pointed the clone at files it does not own. The same boundary
	// RenameInstance uses.
	sourcePrefix := sourceVMDir + string(filepath.Separator)
	clonePrefix := cloneVMDir + string(filepath.Separator)
	for i, arg := range out {
		out[i] = strings.ReplaceAll(arg, sourcePrefix, clonePrefix)
	}
	for i, arg := range out {
		if arg == "-name" && i+1 < len(out) {
			out[i+1] = cloneName
			break
		}
	}
	return out
}

// cloneExtraDisks copies the source VM's disks beyond the first into the
// clone's directory.
//
// buildCloneConfig rewrites their paths into that directory, and
// rewriteCloneArgs rewrites the command line the same way, so the files have to
// be there: a multi-disk source used to produce a clone whose spec and
// arguments named files that did not exist.
//
// A disk outside the source's own directory is left alone — it names a shared
// or external image, which neither rewrite touches.
func (p *QEMUProvider) cloneExtraDisks(ctx context.Context, source *vmConfig, sourceVMDir, cloneVMDir string, linked bool) error {
	for i, disk := range source.Spec.Disks {
		if i == 0 {
			continue
		}
		// A failed containment check is not a "no": treating it as one let a
		// path the resolver could not answer for reach the clone untouched,
		// still naming the source's file.
		within, err := validation.PathWithinAny(disk.Path, sourceVMDir)
		if err != nil {
			return fmt.Errorf("cannot resolve disk %s: %w", disk.Path, err)
		}
		if !within {
			continue
		}
		dest := strings.Replace(disk.Path, sourceVMDir, cloneVMDir, 1)
		if dest == disk.Path {
			// Contained but unchanged by the rewrite — the two directories
			// resolve to the same place, or the prefix is not literal. Copying
			// a file onto itself would destroy it.
			return fmt.Errorf("disk %s does not move into the clone directory", disk.Path)
		}
		// A disk kept in a subdirectory of the VM's own has no parent in the
		// clone yet, and qemu-img does not create one.
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", dest, err)
		}
		if err := p.cloneDisk(ctx, disk.Path, dest, linked); err != nil {
			return fmt.Errorf("failed to clone disk %s: %w", disk.Path, err)
		}
	}
	return nil
}

// extraDisksToCopy counts the disks cloneExtraDisks would copy: the ones past
// the first that live inside the source VM's own directory.
func (p *QEMUProvider) extraDisksToCopy(source *vmConfig, sourceVMDir string) (int, error) {
	n := 0
	for i, disk := range source.Spec.Disks {
		if i == 0 {
			continue
		}
		// A path that cannot be resolved is not a path outside the directory:
		// counting it as one would let a running VM through the check below
		// and hand cloneExtraDisks a torn copy.
		within, err := validation.PathWithinAny(disk.Path, sourceVMDir)
		if err != nil {
			return 0, fmt.Errorf("cannot resolve disk %s: %w", disk.Path, err)
		}
		if within {
			n++
		}
	}
	return n, nil
}
