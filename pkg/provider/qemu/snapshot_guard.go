package qemu

import (
	"fmt"
	"path/filepath"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// snapshotTargetFor resolves the disk image and snapshot name a handle names,
// bound to the instance the same handle declares.
//
// The handle is supplied by the caller, not by CreateSnapshot. isPathAllowed
// confines a path to the data and image directories, which is not enough on its
// own: every VM's disk lives under those roots, so one VM's handle could still
// name another's disk. That matters because "qemu-img snapshot -a" overwrites
// the disk it is given with the snapshot's contents, and "-d" deletes a
// snapshot out of it.
func (p *QEMUProvider) snapshotTargetFor(snapshot provider.SnapshotHandle) (name, diskPath string, err error) {
	name, ok := snapshot.Metadata["name"].(string)
	if !ok || name == "" {
		return "", "", fmt.Errorf("invalid snapshot metadata: missing name")
	}
	// Validated on the way out as well as on the way in: qemu-img reads a
	// leading dash as an option.
	if err := validation.ValidateSnapshotName(name); err != nil {
		return "", "", fmt.Errorf("invalid snapshot name: %w", err)
	}

	diskPath, ok = snapshot.Metadata["disk_path"].(string)
	if !ok || diskPath == "" {
		return "", "", fmt.Errorf("invalid snapshot metadata: missing disk_path")
	}
	if err := validation.ValidateInstanceName(snapshot.Instance); err != nil {
		return "", "", fmt.Errorf("invalid snapshot instance %q: %w", snapshot.Instance, err)
	}
	if !p.isPathAllowed(diskPath) {
		return "", "", fmt.Errorf("refusing disk %s: outside the managed directories", diskPath)
	}

	// Resolved once, here, and used for both the check and the command: the
	// guard followed symlinks and then the caller re-resolved the raw name, so
	// a link swapped between the two aimed qemu-img at a file that was never
	// cleared.
	//
	// A window remains between this resolution and qemu-img opening the path,
	// and it cannot be closed from here: qemu-img takes a name, not a
	// descriptor, so no amount of checking in this process makes the open
	// atomic. What bounds it is the directory, not the check — <dataDir> and
	// everything under it belong to the daemon's own user, so swapping a
	// component of this path already requires the privileges the guard is
	// protecting. Do not relax those permissions on the strength of this
	// function.
	resolved, err := filepath.EvalSymlinks(diskPath)
	if err != nil {
		return "", "", fmt.Errorf("resolving disk %s: %w", diskPath, err)
	}

	// And inside this instance's own directory, not merely inside the roots: a
	// link under one instance's directory pointing into another's passed the
	// check above on its own name.
	within, err := validation.PathWithinAny(resolved, filepath.Join(p.dataDir, snapshot.Instance))
	if err != nil {
		return "", "", fmt.Errorf("resolving disk %s: %w", diskPath, err)
	}
	if !within {
		return "", "", fmt.Errorf("disk %s does not belong to the instance %q it is presented for",
			diskPath, snapshot.Instance)
	}

	// The id too, when the handle carries one. CreateSnapshot and
	// ListSnapshots both mint "<instance>_<name>", so an id naming a different
	// pair is a handle whose fields disagree — the case the resolver exists to
	// catch, and the one field it was not reading.
	if snapshot.ID != "" {
		if want := snapshot.Instance + "_" + name; snapshot.ID != want {
			return "", "", fmt.Errorf("snapshot id %q does not match %q", snapshot.ID, want)
		}
	}

	return name, resolved, nil
}
