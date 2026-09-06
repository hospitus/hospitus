package jail

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/dataset"
	"github.com/hospitus/hospitus/pkg/logging"
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

// Volume represents a named storage volume
type Volume struct {
	// Name is the volume name (unique identifier)
	Name string `json:"name"`

	// Description is a human-readable description
	Description string `json:"description,omitempty"`

	// ZFSDataset is the full ZFS dataset path
	ZFSDataset string `json:"zfs_dataset"`

	// Mountpoint is the filesystem path where the volume is mounted
	Mountpoint string `json:"mountpoint"`

	// Quota is the storage quota in bytes (0 = unlimited)
	Quota int64 `json:"quota,omitempty"`

	// Reservation is the guaranteed storage in bytes (0 = none)
	Reservation int64 `json:"reservation,omitempty"`

	// Compression is the ZFS compression algorithm (lz4, zstd, gzip, off)
	Compression string `json:"compression,omitempty"`

	// ReadOnly makes the volume read-only when mounted
	ReadOnly bool `json:"read_only,omitempty"`

	// Created is the creation timestamp
	Created time.Time `json:"created"`

	// UsedBytes is the current usage in bytes
	UsedBytes int64 `json:"used_bytes,omitempty"`

	// AvailableBytes is the available space in bytes
	AvailableBytes int64 `json:"available_bytes,omitempty"`

	// MountedTo lists jails this volume is mounted to
	MountedTo []VolumeMountInfo `json:"mounted_to,omitempty"`

	// Labels are key-value pairs for organization
	Labels map[string]string `json:"labels,omitempty"`
}

// VolumeMountInfo represents a volume mount in a jail
type VolumeMountInfo struct {
	// JailName is the name of the jail
	JailName string `json:"jail_name"`

	// MountPath is the path inside the jail
	MountPath string `json:"mount_path"`

	// ReadOnly indicates if mounted read-only
	ReadOnly bool `json:"read_only"`
}

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

	// The dataset itself is the storage backend's job: it builds the zfs(8)
	// arguments, creates the parents and reports an existing volume. What stays
	// here is what ZFS knows nothing about — the description, the labels and the
	// mount bookkeeping this provider keeps alongside.
	backend, err := p.storage()
	if err != nil {
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

// GetVolume retrieves a volume by name
func (p *JailProvider) GetVolume(ctx context.Context, name string) (*Volume, error) {
	volumesParent := p.getVolumesParent()
	volumeDataset := fmt.Sprintf("%s/%s", volumesParent, name)

	// Check if dataset exists
	if err := p.cmd().Run(ctx, "zfs", "list", "-H", volumeDataset); err != nil {
		return nil, fmt.Errorf("volume %s not found", name)
	}

	// Try loading metadata
	volume, err := p.loadVolumeMetadata(name)
	if err != nil {
		// Build volume info from ZFS
		volume = &Volume{
			Name:       name,
			ZFSDataset: volumeDataset,
		}
	}

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

	// Add to fstab for persistence
	fstabPath := filepath.Join(p.stateDir, "fstab", jailName)
	if err := p.addToJailFstab(fstabPath, volume.Mountpoint, targetPath, readOnly); err != nil {
		p.logWarn(ctx, "failed to add volume mount to jail fstab", "volume", volumeName, "jail", jailName, "mount_path", mountPath, logging.FieldError, err)
	}

	// Update volume metadata
	volume.MountedTo = append(volume.MountedTo, VolumeMountInfo{
		JailName:  jailName,
		MountPath: mountPath,
		ReadOnly:  readOnly,
	})
	if err := p.saveVolumeMetadata(volume); err != nil {
		p.logWarn(ctx, "failed to persist volume metadata after mount", "volume", volumeName, "jail", jailName, logging.FieldError, err)
	}

	return nil
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

	targetPath := filepath.Join(jailConfig.Path, strings.TrimPrefix(mountInfo.MountPath, "/"))

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

	// Remove from fstab
	fstabPath := filepath.Join(p.stateDir, "fstab", jailName)
	// best-effort; fstab cleanup failure is non-fatal for unmount
	_ = p.removeFromJailFstab(fstabPath, volume.Mountpoint)

	// Update volume metadata
	volume.MountedTo = append(volume.MountedTo[:mountIndex], volume.MountedTo[mountIndex+1:]...)
	if err := p.saveVolumeMetadata(volume); err != nil {
		p.logWarn(ctx, "failed to persist volume metadata after unmount", "volume", volumeName, "jail", jailName, logging.FieldError, err)
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

	multiplier := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		multiplier = 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "M"):
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "G"):
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "T"):
		multiplier = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}

	val, _ := strconv.ParseInt(s, 10, 64)
	return val * multiplier
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
