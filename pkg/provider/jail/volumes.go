package jail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/dataset"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/storage"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Volumes are named ZFS datasets that can be mounted into jails.
// They provide:
//   - Persistent storage that survives jail recreation
//   - Quota management
//   - Sharing between multiple jails
//   - Snapshots and clones
//
// Architecture:
//
//	zroot/hospitus/volumes/           # Volumes parent dataset
//	zroot/hospitus/volumes/mydata/    # Named volume
//	zroot/hospitus/volumes/shared/    # Shared volume
//
// Volumes are mounted into jails using nullfs mounts.

// Volume and VolumeMountInfo are aliases: the types moved to pkg/provider so
// the API can reach volume mounting through the NamedVolumeProvider interface
// instead of the concrete *JailProvider.
type Volume = provider.NamedVolume

type VolumeMountInfo = provider.VolumeMountInfo

// VolumeMount represents a volume mount specification
type VolumeMount struct {
	// VolumeName is the name of the volume to mount
	VolumeName string `json:"volume_name"`

	// MountPath is the path inside the jail where to mount
	MountPath string `json:"mount_path"`

	// ReadOnly mounts the volume read-only
	ReadOnly bool `json:"read_only,omitempty"`
}

// VolumeCreateOptions contains options for creating a volume
type VolumeCreateOptions struct {
	// Description is a human-readable description
	Description string

	// Quota is the storage quota (e.g., "10G", "500M")
	Quota string

	// Reservation is guaranteed storage (e.g., "1G")
	Reservation string

	// Compression algorithm (lz4, zstd, gzip, off)
	Compression string

	// Labels are key-value pairs
	Labels map[string]string
}

// getVolumesParent returns the ZFS parent dataset for volumes
func (p *JailProvider) getVolumesParent() string {
	// Use same pool as jails but under "volumes"
	parts := strings.Split(p.zfsParent, "/")
	if len(parts) < 2 {
		return dataset.Child("volumes")
	}
	parts[len(parts)-1] = "volumes"
	return strings.Join(parts, "/")
}

// storage returns the storage backend for volume datasets, built on the same
// parent this provider has always used so an existing pool keeps serving the
// volumes already on it.
func (p *JailProvider) storage() (storage.Manager, error) {
	if p.storageBackend != nil {
		return p.storageBackend, nil
	}

	backend, err := storage.NewPlatformBackend()
	if err != nil {
		return nil, err
	}
	if err := backend.Initialize(context.Background(), storage.Config{
		Backend:          "zfs",
		ZFSParentDataset: p.getVolumesParent(),
	}); err != nil {
		return nil, fmt.Errorf("failed to initialize storage backend: %w", err)
	}

	p.storageBackend = backend
	return backend, nil
}

// CreateVolume creates a new named volume
func (p *JailProvider) CreateVolume(ctx context.Context, name string, opts VolumeCreateOptions) (*Volume, error) {
	// Validate name. The volume name becomes a ZFS dataset component and a
	// metadata filename, so enforce the strict instance-name rules (rejects
	// slashes, whitespace, path traversal, and injection characters).
	if err := validation.ValidateInstanceName(name); err != nil {
		return nil, fmt.Errorf("invalid volume name: %w", err)
	}

	volumesParent := p.getVolumesParent()
	volumeDataset := fmt.Sprintf("%s/%s", volumesParent, name)

	compression := opts.Compression
	if compression == "" {
		compression = "lz4" // Default
	}
	if !validZFSCompression[compression] {
		return nil, fmt.Errorf("invalid compression %q: expected one of off, on, lz4, gzip, gzip-1..9, zle, lzjb, zstd, zstd-fast", compression)
	}

	// The dataset itself is the storage backend's job: it builds the zfs(8)
	// arguments, creates the parents and reports an existing volume. What stays
	// here is what ZFS knows nothing about — the description, the labels and the
	// mount bookkeeping this provider keeps alongside.
	backend, err := p.storage()
	if err != nil {
		return nil, err
	}

	// Quota and reservation become "zfs set quota=<v>"; compression is checked
	// against what ZFS actually accepts. An unvalidated value is another
	// argument on a root command line.
	if err := validateZFSSize(opts.Quota, "quota"); err != nil {
		return nil, err
	}
	if err := validateZFSSize(opts.Reservation, "reservation"); err != nil {
		return nil, err
	}

	volumeOpts := storage.VolumeOptions{
		Quota:      opts.Quota,
		Properties: map[string]string{"compression": compression},
	}
	if opts.Reservation != "" {
		volumeOpts.Properties["reservation"] = opts.Reservation
	}

	created, err := backend.CreateVolume(ctx, name, volumeOpts)
	if err != nil {
		return nil, err
	}

	mountpoint := created.Path
	if mountpoint == "" {
		mountpoint, err = p.getZFSMountpoint(ctx, volumeDataset)
		if err != nil {
			p.destroyZFSDatasetCleanup(ctx, volumeDataset)
			return nil, fmt.Errorf("failed to get volume mountpoint: %w", err)
		}
	}

	// Get quota in bytes
	var quotaBytes int64
	if opts.Quota != "" {
		quotaBytes = parseSize(opts.Quota)
	}

	// Get reservation in bytes
	var reservationBytes int64
	if opts.Reservation != "" {
		reservationBytes = parseSize(opts.Reservation)
	}

	volume := &Volume{
		Name:        name,
		Description: opts.Description,
		ZFSDataset:  volumeDataset,
		Mountpoint:  mountpoint,
		Quota:       quotaBytes,
		Reservation: reservationBytes,
		Compression: compression,
		Created:     time.Now(),
		Labels:      opts.Labels,
	}

	if err := p.saveVolumeMetadata(volume); err != nil {
		p.logWarn(ctx, "failed to save volume metadata", "volume", name, logging.FieldError, err)
	}

	return volume, nil
}

// validZFSCompression lists what "zfs set compression=" accepts. The value ends
// up on a root command line, so an unknown one is refused rather than passed on.
var validZFSCompression = func() map[string]bool {
	m := map[string]bool{
		"off": true, "on": true, "lz4": true, "gzip": true, "zle": true,
		"lzjb": true, "zstd": true, "zstd-fast": true,
	}
	for i := 1; i <= 9; i++ {
		m[fmt.Sprintf("gzip-%d", i)] = true
	}
	return m
}()

// zfsSizeRegex matches the size syntax zfs(8) accepts: a number, optionally
// fractional, with an optional unit suffix. "none" turns the property off.
var zfsSizeRegex = regexp.MustCompile(`^(?:\d+(?:\.\d+)?[KMGTPEkmgtpe]?[Bb]?)$`)

// validateZFSSize rejects anything that is not a plain ZFS size.
func validateZFSSize(value, field string) error {
	if value == "" || value == "none" {
		return nil
	}
	if !zfsSizeRegex.MatchString(value) {
		return fmt.Errorf("invalid %s %q: expected a size such as 10G, 500M or none", field, value)
	}
	return nil
}

// GetVolume retrieves a volume by name
func (p *JailProvider) GetVolume(ctx context.Context, name string) (*Volume, error) {
	// The name becomes a dataset argument to zfs(8) and a metadata file path.
	if err := validation.ValidateInstanceName(name); err != nil {
		return nil, fmt.Errorf("invalid volume name %q: %w", name, err)
	}
	volumesParent := p.getVolumesParent()
	volumeDataset := fmt.Sprintf("%s/%s", volumesParent, name)

	// Check if dataset exists
	if err := p.cmd().Run(ctx, "zfs", "list", "-H", volumeDataset); err != nil {
		return nil, fmt.Errorf("volume %s not found", name)
	}

	// A missing metadata file is ordinary — the volume is described from ZFS.
	// An unreadable one is not: it would yield a blank record with no MountedTo,
	// and DeleteVolume's "still mounted" guard would then pass on a volume a
	// jail is using.
	volume, err := p.loadVolumeMetadata(name)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		volume = &Volume{
			Name:       name,
			ZFSDataset: volumeDataset,
		}
	default:
		return nil, fmt.Errorf("volume %s has unreadable metadata; refusing to describe it from ZFS alone: %w", name, err)
	}
	// The existence check above was made against volumeDataset. Stale or
	// corrupted metadata carrying a different ZFSDataset would send every query
	// below — and every operation the caller performs afterwards — to another
	// dataset entirely.
	volume.Name = name
	volume.ZFSDataset = volumeDataset

	// Update from ZFS properties
	if err := p.updateVolumeFromZFS(ctx, volume); err != nil {
		return nil, err
	}

	return volume, nil
}

// ListVolumes lists all volumes
func (p *JailProvider) ListVolumes(ctx context.Context) ([]Volume, error) {
	volumesParent := p.getVolumesParent()

	// Check if parent exists
	if err := p.cmd().Run(ctx, "zfs", "list", "-H", volumesParent); err != nil {
		return []Volume{}, nil
	}

	// List child datasets
	output, err := p.cmd().Output(ctx, "zfs", "list", "-H", "-r", "-t", "filesystem", "-o", "name", volumesParent)
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}

	var volumes []Volume
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")

	for _, line := range lines {
		ds := strings.TrimSpace(line)
		if ds == "" || ds == volumesParent {
			continue
		}

		// Extract volume name
		name := strings.TrimPrefix(ds, volumesParent+"/")
		if strings.Contains(name, "/") {
			continue // Skip nested datasets
		}

		volume, err := p.GetVolume(ctx, name)
		if err != nil {
			continue
		}

		volumes = append(volumes, *volume)
	}

	return volumes, nil
}

// DeleteVolume deletes a volume
func (p *JailProvider) DeleteVolume(ctx context.Context, name string, force bool) error {
	volume, err := p.GetVolume(ctx, name)
	if err != nil {
		return err
	}

	// Check if mounted to any jails
	if len(volume.MountedTo) > 0 && !force {
		jails := make([]string, len(volume.MountedTo))
		for i, m := range volume.MountedTo {
			jails[i] = m.JailName
		}
		return fmt.Errorf("volume is mounted to jails: %s. Use force=true to delete anyway", strings.Join(jails, ", "))
	}

	// Unmount from all jails first
	for _, mount := range volume.MountedTo {
		if err := p.UnmountVolumeFromJail(ctx, name, mount.JailName); err != nil {
			if !force {
				return fmt.Errorf("failed to unmount from %s: %w", mount.JailName, err)
			}
			p.logWarn(ctx, "failed to unmount volume from jail during forced delete", "volume", name, "jail", mount.JailName, logging.FieldError, err)
		}
	}

	// Destroy the dataset through the backend, which owns the zfs(8) call.
	backend, err := p.storage()
	if err != nil {
		return err
	}
	if err := backend.DeleteVolume(ctx, name, storage.DeleteOptions{Force: force, Recursive: force}); err != nil {
		return fmt.Errorf("failed to destroy ZFS dataset: %w", err)
	}

	// Remove metadata
	metadataPath := filepath.Join(p.stateDir, "volumes", fmt.Sprintf("%s.json", name))
	os.Remove(metadataPath)

	return nil
}

// ResizeVolume changes the quota of a volume
func (p *JailProvider) ResizeVolume(ctx context.Context, name, newQuota string) error {
	// CreateVolume checks its quota with validateZFSSize; this path passed the
	// value straight into a root "zfs set quota=".
	if err := validateZFSSize(newQuota, "quota"); err != nil {
		return err
	}
	volume, err := p.GetVolume(ctx, name)
	if err != nil {
		return err
	}

	if output, err := p.cmd().CombinedOutput(ctx, "zfs", "set", fmt.Sprintf("quota=%s", newQuota), volume.ZFSDataset); err != nil {
		return fmt.Errorf("failed to set quota: %w (output: %s)", err, string(output))
	}

	// Update metadata (best-effort; persisted metadata is a cache, resize
	// already applied, but a failure to persist should be surfaced).
	volume.Quota = parseSize(newQuota)
	if err := p.saveVolumeMetadata(volume); err != nil {
		p.logWarn(ctx, "failed to persist volume metadata after resize", "volume", name, logging.FieldError, err)
	}

	return nil
}

// jailMountTarget resolves where a mount inside a jail lands on the host, and
// refuses one that leaves the jail.
//
// A lexical check is not enough: everything below the jail root is writable by
// the jail's own root, who can make any component of the path a symlink out.
// The comparison is made on the resolved path, and the deepest existing
// ancestor is resolved so a mount point that does not exist yet is still
// checked against the directory it would be created in.
func jailMountTarget(jailRoot, mountPath string) (string, error) {
	target := filepath.Join(jailRoot, strings.TrimPrefix(mountPath, "/"))

	within, err := validation.PathWithin(target, jailRoot)
	if err != nil {
		return "", fmt.Errorf("cannot resolve mount path %q: %w", mountPath, err)
	}
	if !within {
		return "", fmt.Errorf("mount path %q escapes the jail root", mountPath)
	}
	return target, nil
}

// MountVolumeToJail mounts a volume into a jail
func (p *JailProvider) MountVolumeToJail(ctx context.Context, volumeName, jailName, mountPath string, readOnly bool) error {
	// SECURITY: validate names (used to build ZFS/config paths) and the mount
	// path (must be an absolute path with no ".." so it cannot escape the jail
	// root — CWE-22).
	if err := validation.ValidateInstanceName(volumeName); err != nil {
		return fmt.Errorf("invalid volume name: %w", err)
	}
	if err := validation.ValidateInstanceName(jailName); err != nil {
		return fmt.Errorf("invalid jail name: %w", err)
	}
	if !filepath.IsAbs(mountPath) {
		return fmt.Errorf("mount path must be absolute: %q", mountPath)
	}
	if err := validation.ValidateFilePath(mountPath, true); err != nil {
		return fmt.Errorf("invalid mount path: %w", err)
	}

	volume, err := p.GetVolume(ctx, volumeName)
	if err != nil {
		return err
	}

	// Load jail config
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load jail config: %w", err)
	}

	targetPath, err := jailMountTarget(jailConfig.Path, mountPath)
	if err != nil {
		return err
	}

	// UnmountVolumeFromJail takes no mount path and removes the first matching
	// entry, while removeFromJailFstab drops every entry for this source. A
	// second mount into the same jail would therefore leave a live mount that
	// no metadata records.
	for _, m := range volume.MountedTo {
		if m.JailName != jailName {
			continue
		}
		if m.MountPath == mountPath {
			return fmt.Errorf("volume %s is already mounted at %s in jail %s", volumeName, mountPath, jailName)
		}
		return fmt.Errorf("volume %s is already mounted in jail %s at %s; unmount it first",
			volumeName, jailName, m.MountPath)
	}

	// Create target directory
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		return fmt.Errorf("failed to create mount point: %w", err)
	}

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return err
	}

	if running {
		// Mount using nullfs
		mountOpts := "rw"
		if readOnly {
			mountOpts = "ro"
		}

		if output, err := p.cmd().CombinedOutput(ctx, "mount", "-t", "nullfs", "-o", mountOpts, volume.Mountpoint, targetPath); err != nil {
			return fmt.Errorf("failed to mount volume: %w (output: %s)", err, string(output))
		}
	}

	// The bookkeeping is what DeleteVolume consults before refusing to remove a
	// volume a jail still has mounted. Reporting success after failing to write
	// it leaves a live mount that nothing records, so the safety check passes
	// on a volume that is in use. Undo the mount rather than report success.
	fstabPath := filepath.Join(p.stateDir, "fstab", jailName)
	if err := p.addToJailFstab(fstabPath, volume.Mountpoint, targetPath, readOnly); err != nil {
		p.undoMount(ctx, running, targetPath)
		return fmt.Errorf("failed to record the mount in the jail fstab: %w", err)
	}

	volume.MountedTo = append(volume.MountedTo, VolumeMountInfo{
		JailName:  jailName,
		MountPath: mountPath,
		ReadOnly:  readOnly,
	})
	if err := p.saveVolumeMetadata(volume); err != nil {
		_ = p.removeFromJailFstab(fstabPath, volume.Mountpoint)
		p.undoMount(ctx, running, targetPath)
		return fmt.Errorf("failed to persist volume metadata after mount: %w", err)
	}

	return nil
}

// undoMount unmounts a target that was just mounted, on a failure path where
// the caller is about to report an error.
func (p *JailProvider) undoMount(ctx context.Context, mounted bool, targetPath string) {
	if !mounted {
		return
	}
	if out, err := p.cmd().CombinedOutput(context.WithoutCancel(ctx), "umount", targetPath); err != nil {
		p.logWarn(ctx, "could not undo a mount after the bookkeeping failed",
			"target", targetPath, "output", strings.TrimSpace(string(out)), logging.FieldError, err)
	}
}

// UnmountVolumeFromJail unmounts a volume from a jail
func (p *JailProvider) UnmountVolumeFromJail(ctx context.Context, volumeName, jailName string) error {
	volume, err := p.GetVolume(ctx, volumeName)
	if err != nil {
		return err
	}

	// Find mount info
	var mountInfo *VolumeMountInfo
	var mountIndex int
	for i, m := range volume.MountedTo {
		if m.JailName == jailName {
			mountInfo = &volume.MountedTo[i]
			mountIndex = i
			break
		}
	}

	if mountInfo == nil {
		return fmt.Errorf("volume %s is not mounted to jail %s", volumeName, jailName)
	}

	// Load jail config
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", jailName))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load jail config: %w", err)
	}

	// Through jailMountTarget, exactly as the mount path does: TrimPrefix drops
	// only a leading "/", so a recorded path carrying a traversal would have
	// umount run outside the jail root.
	targetPath, err := jailMountTarget(jailConfig.Path, mountInfo.MountPath)
	if err != nil {
		return err
	}

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return err
	}

	if running {
		// Unmount
		if output, err := p.cmd().CombinedOutput(ctx, "umount", targetPath); err != nil {
			return fmt.Errorf("failed to unmount volume: %w (output: %s)", err, string(output))
		}
	}

	// Symmetrical with the mount path: the bookkeeping is what DeleteVolume
	// consults, so reporting a successful unmount while the records still say
	// the volume is mounted leaves it undeletable — and the reverse, dropping
	// the record while the mount lives on, defeats the same check.
	fstabPath := filepath.Join(p.stateDir, "fstab", jailName)
	if err := p.removeFromJailFstab(fstabPath, volume.Mountpoint); err != nil {
		return fmt.Errorf("unmounted %s but could not update the jail fstab: %w", targetPath, err)
	}

	volume.MountedTo = append(volume.MountedTo[:mountIndex], volume.MountedTo[mountIndex+1:]...)
	if err := p.saveVolumeMetadata(volume); err != nil {
		return fmt.Errorf("unmounted %s but could not persist the volume metadata: %w", targetPath, err)
	}

	return nil
}

// updateVolumeFromZFS updates volume info from ZFS properties
func (p *JailProvider) updateVolumeFromZFS(ctx context.Context, volume *Volume) error {
	mountpoint, err := p.getZFSMountpoint(ctx, volume.ZFSDataset)
	if err == nil {
		volume.Mountpoint = mountpoint
	}

	// Get used space
	if output, err := p.cmd().Output(ctx, "zfs", "get", "-Hp", "-o", "value", "used", volume.ZFSDataset); err == nil {
		volume.UsedBytes, _ = strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	}

	// Get available space
	if output, err := p.cmd().Output(ctx, "zfs", "get", "-Hp", "-o", "value", "available", volume.ZFSDataset); err == nil {
		volume.AvailableBytes, _ = strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	}

	if output, err := p.cmd().Output(ctx, "zfs", "get", "-Hp", "-o", "value", "quota", volume.ZFSDataset); err == nil {
		val := strings.TrimSpace(string(output))
		if val != "none" && val != "0" {
			volume.Quota, _ = strconv.ParseInt(val, 10, 64)
		}
	}

	if output, err := p.cmd().Output(ctx, "zfs", "get", "-Hp", "-o", "value", "compression", volume.ZFSDataset); err == nil {
		volume.Compression = strings.TrimSpace(string(output))
	}

	return nil
}

// saveVolumeMetadata saves volume metadata to a JSON file
func (p *JailProvider) saveVolumeMetadata(volume *Volume) error {
	volumeDir := filepath.Join(p.stateDir, "volumes")
	if err := os.MkdirAll(volumeDir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(volume, "", "  ")
	if err != nil {
		return err
	}

	metadataPath := filepath.Join(volumeDir, fmt.Sprintf("%s.json", volume.Name))
	return os.WriteFile(metadataPath, data, 0o600)
}

// loadVolumeMetadata loads volume metadata from a JSON file
func (p *JailProvider) loadVolumeMetadata(name string) (*Volume, error) {
	metadataPath := filepath.Join(p.stateDir, "volumes", fmt.Sprintf("%s.json", name))

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, err
	}

	var volume Volume
	if err := json.Unmarshal(data, &volume); err != nil {
		return nil, err
	}

	return &volume, nil
}

// addToJailFstab records a mount in the jail's fstab.
//
// mountPoint is the target as the host sees it — the jail root joined with the
// path inside the jail — which is what jail(8) expects from mount.fstab and what
// configureLinuxMounts writes. A jail-relative path here would name a directory
// of the host's own, and the "umount -a -F" run during cleanup would aim at it.
func (p *JailProvider) addToJailFstab(fstabPath, source, mountPoint string, readOnly bool) error {
	if err := os.MkdirAll(filepath.Dir(fstabPath), 0o755); err != nil {
		return err
	}

	opts := "rw"
	if readOnly {
		opts = "ro"
	}

	entry := fmt.Sprintf("%s\t%s\tnullfs\t%s\t0\t0\n", source, mountPoint, opts)

	// Every start re-adds the mounts it just made, so without this the file
	// gains a duplicate line per restart and grows without bound.
	if existing, err := os.ReadFile(fstabPath); err == nil && strings.Contains(string(existing), entry) {
		return nil
	}

	f, err := os.OpenFile(fstabPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(entry)
	return err
}

// removeFromJailFstab removes an entry from jail's fstab
func (p *JailProvider) removeFromJailFstab(fstabPath, source string) error {
	content, err := os.ReadFile(fstabPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	lines := strings.Split(string(content), "\n")
	var newLines []string

	for _, line := range lines {
		if !strings.HasPrefix(line, source+"\t") {
			newLines = append(newLines, line)
		}
	}

	return os.WriteFile(fstabPath, []byte(strings.Join(newLines, "\n")), 0o600)
}

// parseSize parses a size string (e.g., "10G", "500M") to bytes
func parseSize(s string) int64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" || s == "NONE" || s == "0" {
		return 0
	}

	// Aligned with validateZFSSize, which accepts a fractional value and an
	// optional "B": "1.5G" and "10GB" pass validation, so parsing them as 1 and
	// 10 bytes would silently report the wrong size.
	s = strings.TrimSuffix(s, "B")

	multiplier := float64(1)
	if s != "" {
		switch s[len(s)-1] {
		case 'K':
			multiplier = 1 << 10
		case 'M':
			multiplier = 1 << 20
		case 'G':
			multiplier = 1 << 30
		case 'T':
			multiplier = 1 << 40
		case 'P':
			multiplier = 1 << 50
		case 'E':
			multiplier = 1 << 60
		}
		if multiplier != 1 {
			s = s[:len(s)-1]
		}
	}

	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(val * multiplier)
}

// SnapshotVolume creates a snapshot of a volume
func (p *JailProvider) SnapshotVolume(ctx context.Context, volumeName, snapshotName string) error {
	// SECURITY: validate names before they are interpolated into the ZFS
	// snapshot path passed to zfs(8).
	if err := validation.ValidateInstanceName(volumeName); err != nil {
		return fmt.Errorf("invalid volume name: %w", err)
	}
	if err := validation.ValidateSnapshotName(snapshotName); err != nil {
		return fmt.Errorf("invalid snapshot name: %w", err)
	}

	volume, err := p.GetVolume(ctx, volumeName)
	if err != nil {
		return err
	}

	snapshotPath := fmt.Sprintf("%s@%s", volume.ZFSDataset, snapshotName)

	if output, err := p.cmd().CombinedOutput(ctx, "zfs", "snapshot", snapshotPath); err != nil {
		return fmt.Errorf("failed to create snapshot: %w (output: %s)", err, string(output))
	}

	return nil
}

// CloneVolume creates a new volume from a snapshot
func (p *JailProvider) CloneVolume(ctx context.Context, volumeName, snapshotName, newVolumeName string) (*Volume, error) {
	// SECURITY: validate names before they are interpolated into ZFS paths.
	if err := validation.ValidateInstanceName(volumeName); err != nil {
		return nil, fmt.Errorf("invalid volume name: %w", err)
	}
	if err := validation.ValidateSnapshotName(snapshotName); err != nil {
		return nil, fmt.Errorf("invalid snapshot name: %w", err)
	}
	if err := validation.ValidateInstanceName(newVolumeName); err != nil {
		return nil, fmt.Errorf("invalid new volume name: %w", err)
	}

	volume, err := p.GetVolume(ctx, volumeName)
	if err != nil {
		return nil, err
	}

	snapshotPath := fmt.Sprintf("%s@%s", volume.ZFSDataset, snapshotName)
	newDataset := fmt.Sprintf("%s/%s", p.getVolumesParent(), newVolumeName)

	if output, err := p.cmd().CombinedOutput(ctx, "zfs", "clone", snapshotPath, newDataset); err != nil {
		return nil, fmt.Errorf("failed to clone volume: %w (output: %s)", err, string(output))
	}

	return p.GetVolume(ctx, newVolumeName)
}

// The API reaches named volumes through this interface, not through
// *JailProvider. Asserted here so a signature change breaks the build rather
// than turning a live endpoint into 501.
var _ provider.NamedVolumeProvider = (*JailProvider)(nil)
