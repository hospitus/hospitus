// Package storage defines a backend-agnostic interface for volumes, snapshots
// and clones, and implements it for ZFS.
//
// It serves the volume endpoints, the zvols behind bhyve VM disks, and the
// datasets behind jail volumes. What each provider keeps is what ZFS knows
// nothing about: bhyve reuses an orphan zvol left by a failed create, and the
// jail provider tracks descriptions, labels and which jails a volume is mounted
// into.
//
// Only the ZFS backend exists, and only on FreeBSD and Linux — NewPlatformBackend
// returns ErrNoPlatformBackend elsewhere, and callers report the feature as
// unavailable rather than failing per operation. The directory and LVM backends
// the Backend field names are not implemented.
package storage

import (
	"context"
	"errors"
	"math"
	"strconv"
	"time"
)

// ErrNoPlatformBackend is returned by NewPlatformBackend where no storage
// backend exists: ZFS is a FreeBSD and Linux feature.
var ErrNoPlatformBackend = errors.New("no ZFS storage backend on this platform")

// Manager is the interface for storage management operations.
// Only the ZFS implementation exists today.
type Manager interface {
	// Initialize sets up the storage manager
	Initialize(ctx context.Context, config Config) error

	// Shutdown cleans up resources
	Shutdown(ctx context.Context) error

	// Volume operations
	VolumeManager

	// Snapshot operations
	SnapshotManager

	// Clone operations
	CloneManager

	// Quota management
	QuotaManager
}

// Config holds storage manager configuration
type Config struct {
	// DataDir is where storage state is stored
	DataDir string

	// Backend specifies the storage backend. Only "zfs" is implemented;
	// "directory" and "lvm" are accepted names with no backend behind them.
	Backend string

	// ZFSParentDataset is the parent ZFS dataset (for ZFS backend)
	// Example: "zroot/hospitus/jails"
	ZFSParentDataset string

	// DirectoryRoot is the root directory for directory backend
	// Example: "/var/lib/hospitus/storage"
	DirectoryRoot string

	// DefaultQuota is the default quota for new volumes
	DefaultQuota string // e.g., "10G"

	// Compression enables compression (for ZFS)
	Compression string // "lz4", "gzip", "off"

	// AutoSnapshot enables automatic snapshots
	AutoSnapshot bool

	// AutoSnapshotInterval defines how often to create auto-snapshots
	AutoSnapshotInterval time.Duration

	// AutoSnapshotRetention defines how many auto-snapshots to keep
	AutoSnapshotRetention int
}

// VolumeManager handles volume/dataset operations
type VolumeManager interface {
	// CreateVolume creates a new storage volume
	// Returns the volume path (e.g., mountpoint for ZFS, directory path)
	CreateVolume(ctx context.Context, name string, opts VolumeOptions) (*Volume, error)

	// DeleteVolume deletes a storage volume and all its contents
	DeleteVolume(ctx context.Context, name string, opts DeleteOptions) error

	// GetVolume returns information about a volume
	GetVolume(ctx context.Context, name string) (*Volume, error)

	// ListVolumes returns all managed volumes
	ListVolumes(ctx context.Context) ([]Volume, error)

	// VolumeExists checks if a volume exists
	VolumeExists(ctx context.Context, name string) bool

	// GetVolumePath returns the filesystem path for a volume
	GetVolumePath(ctx context.Context, name string) (string, error)

	// ResizeVolume resizes a volume (if supported by backend)
	ResizeVolume(ctx context.Context, name string, newSize string) error

	// MountVolume mounts a volume (if applicable)
	MountVolume(ctx context.Context, name string) error

	// UnmountVolume unmounts a volume (if applicable)
	UnmountVolume(ctx context.Context, name string) error

	// ReplicateVolume replicates a volume to a remote target
	ReplicateVolume(ctx context.Context, name string, target string, opts ReplicationOptions) error
}

// ReplicationOptions specifies options for volume replication
type ReplicationOptions struct {
	// IncrementalFrom names the snapshot to send changes since, e.g.
	// "tank/hospitus/web@daily-1". Empty means a full send.
	//
	// This replaced a boolean: a flag cannot say what to send from, so the
	// backend had nothing to pass on and "incremental" quietly performed a
	// full send.
	IncrementalFrom string
	Recursive       bool
	Compressed      bool
}

// VolumeOptions specifies options for creating a volume
type VolumeOptions struct {
	// Size is the volume size (e.g., "10G", "500M")
	Size string

	// Quota limits the maximum size
	Quota string

	// Properties are backend-specific properties
	// For ZFS: {"compression": "lz4", "atime": "off"}
	Properties map[string]string

	// Mountpoint overrides the default mountpoint
	Mountpoint string

	// CopyFrom copies content from an existing path
	CopyFrom string
}

// DeleteOptions specifies options for deleting a volume
type DeleteOptions struct {
	// Force forces deletion even if volume is busy
	Force bool

	// Recursive deletes child volumes/snapshots
	Recursive bool

	// KeepSnapshots keeps snapshots but deletes the volume
	KeepSnapshots bool
}

// Volume represents a storage volume
type Volume struct {
	// Name is the volume name (e.g., "webserver" for zroot/hospitus/jails/webserver)
	Name string

	// FullName is the full backend name (e.g., "zroot/hospitus/jails/webserver")
	FullName string

	// Path is the filesystem mountpoint
	Path string

	// Backend is the storage backend type ("zfs", "directory", "lvm")
	Backend string

	// Size is the current size in bytes
	Size int64

	// Used is the used space in bytes
	Used int64

	// Available is the available space in bytes
	Available int64

	// Quota is the quota in bytes (0 = no quota)
	Quota int64

	// Created is when the volume was created
	Created time.Time

	// Properties are backend-specific properties
	Properties map[string]string

	// SnapshotCount is the number of snapshots
	SnapshotCount int

	// State indicates the volume state ("mounted", "unmounted", "degraded")
	State string
}

// SnapshotManager handles snapshot operations
type SnapshotManager interface {
	// CreateSnapshot creates a snapshot of a volume
	CreateSnapshot(ctx context.Context, volumeName, snapshotName string, opts SnapshotOptions) (*Snapshot, error)

	// DeleteSnapshot deletes a snapshot
	DeleteSnapshot(ctx context.Context, volumeName, snapshotName string) error

	// ListSnapshots lists all snapshots for a volume
	ListSnapshots(ctx context.Context, volumeName string) ([]Snapshot, error)

	// GetSnapshot returns information about a specific snapshot
	GetSnapshot(ctx context.Context, volumeName, snapshotName string) (*Snapshot, error)

	// RollbackSnapshot rolls back a volume to a snapshot
	// Warning: This destroys data created after the snapshot
	RollbackSnapshot(ctx context.Context, volumeName, snapshotName string, opts RollbackOptions) error

	// SnapshotExists checks if a snapshot exists
	SnapshotExists(ctx context.Context, volumeName, snapshotName string) bool
}

// SnapshotOptions specifies options for creating a snapshot
type SnapshotOptions struct {
	// Recursive creates snapshots of child volumes
	Recursive bool

	// Description is an optional description
	Description string

	// Retention specifies when to auto-delete (0 = never)
	Retention time.Duration

	// Tags are optional tags for the snapshot
	Tags map[string]string
}

// RollbackOptions specifies options for rollback
type RollbackOptions struct {
	// Force forces rollback even if there are newer snapshots
	Force bool

	// DestroyNewer destroys snapshots newer than the target
	DestroyNewer bool
}

// Snapshot represents a point-in-time snapshot
type Snapshot struct {
	// Name is the snapshot name
	Name string

	// FullName is the full snapshot identifier
	// For ZFS: "zroot/hospitus/jails/webserver@snap1"
	FullName string

	// VolumeName is the parent volume name
	VolumeName string

	// Created is when the snapshot was created
	Created time.Time

	// Size is the space used by this snapshot (may be 0 for thin snapshots)
	Size int64

	// Referenced is the amount of data referenced
	Referenced int64

	// Description is an optional description
	Description string

	// Tags are optional tags
	Tags map[string]string
}

// CloneManager handles clone operations
type CloneManager interface {
	// CreateClone creates a clone from a snapshot
	// A clone is a writable copy that shares data with the source
	CreateClone(ctx context.Context, snapshotFullName, cloneName string, opts CloneOptions) (*Volume, error)

	// PromoteClone promotes a clone to an independent volume
	// The original volume becomes dependent on the clone
	PromoteClone(ctx context.Context, cloneName string) error

	// GetCloneOrigin returns the snapshot a clone was created from
	GetCloneOrigin(ctx context.Context, cloneName string) (string, error)

	// ListClones lists all clones of a volume or snapshot
	ListClones(ctx context.Context, volumeName string) ([]Volume, error)
}

// CloneOptions specifies options for creating a clone
type CloneOptions struct {
	// Mountpoint overrides the default mountpoint
	Mountpoint string

	// Properties are backend-specific properties to set on the clone
	Properties map[string]string
}

// QuotaManager handles quota operations
type QuotaManager interface {
	// SetQuota sets the quota for a volume
	SetQuota(ctx context.Context, volumeName string, quota string) error

	// GetQuota returns the current quota for a volume
	GetQuota(ctx context.Context, volumeName string) (int64, error)

	// SetReservation sets the guaranteed space for a volume
	SetReservation(ctx context.Context, volumeName string, reservation string) error

	// GetReservation returns the current reservation for a volume
	GetReservation(ctx context.Context, volumeName string) (int64, error)
}

// Backend-specific interfaces

// ZFSManager extends Manager with ZFS-specific operations
type ZFSManager interface {
	Manager

	// GetDataset returns ZFS dataset information
	GetDataset(ctx context.Context, name string) (*ZFSDataset, error)

	// SetProperty sets a ZFS property
	SetProperty(ctx context.Context, dataset, property, value string) error

	// GetProperty gets a ZFS property value
	GetProperty(ctx context.Context, dataset, property string) (string, error)

	// SendReceive sends a dataset to another pool/host
	SendReceive(ctx context.Context, source, destination string, opts SendReceiveOptions) error

	// Scrub initiates a scrub on the pool
	Scrub(ctx context.Context, pool string) error
}

// ZFSDataset contains ZFS-specific dataset information
type ZFSDataset struct {
	Volume

	// Pool is the ZFS pool name
	Pool string

	// Type is the dataset type ("filesystem", "volume", "snapshot")
	Type string

	// Origin is the clone origin (empty for non-clones)
	Origin string

	// Compression is the compression algorithm
	Compression string

	// Mountpoint is where the dataset is mounted
	Mountpoint string

	// Mounted indicates if the dataset is currently mounted
	Mounted bool

	// Recordsize is the ZFS record size
	Recordsize int

	// Clones are datasets cloned from this one
	Clones []string
}

// SendReceiveOptions specifies options for ZFS send/receive
type SendReceiveOptions struct {
	// Incremental sends only changes since a previous snapshot
	Incremental string

	// Recursive sends child datasets
	Recursive bool

	// Compressed sends compressed stream
	Compressed bool

	// Raw sends encrypted datasets in raw form
	Raw bool
}

// Error types

// StorageError represents a storage operation error
type StorageError struct {
	Op      string // Operation that failed
	Volume  string // Volume/dataset name
	Backend string // Backend type
	Err     error  // Underlying error
}

func (e *StorageError) Error() string {
	if e.Volume != "" {
		return e.Op + " " + e.Volume + " (" + e.Backend + "): " + e.Err.Error()
	}
	return e.Op + " (" + e.Backend + "): " + e.Err.Error()
}

func (e *StorageError) Unwrap() error {
	return e.Err
}

// Common errors
var (
	ErrVolumeNotFound    = &StorageError{Op: "volume lookup", Err: errNotFound}
	ErrVolumeExists      = &StorageError{Op: "volume create", Err: errExists}
	ErrSnapshotNotFound  = &StorageError{Op: "snapshot lookup", Err: errNotFound}
	ErrSnapshotExists    = &StorageError{Op: "snapshot create", Err: errExists}
	ErrQuotaExceeded     = &StorageError{Op: "write", Err: errQuotaExceeded}
	ErrBackendNotSupport = &StorageError{Op: "operation", Err: errNotSupported}
	ErrVolumeBusy        = &StorageError{Op: "volume operation", Err: errBusy}
)

var (
	errNotFound      = errorString("not found")
	errExists        = errorString("already exists")
	errQuotaExceeded = errorString("quota exceeded")
	errNotSupported  = errorString("not supported by backend")
	errBusy          = errorString("volume is busy")
)

type errorString string

func (e errorString) Error() string { return string(e) }

// Helper functions

// ParseSize parses a size string (e.g., "10G", "500M") to bytes
func ParseSize(size string) (int64, error) {
	if size == "" {
		return 0, nil
	}

	var multiplier int64 = 1
	numStr := size

	if len(size) > 1 {
		suffix := size[len(size)-1]
		switch suffix {
		case 'K', 'k':
			multiplier = 1024
			numStr = size[:len(size)-1]
		case 'M', 'm':
			multiplier = 1024 * 1024
			numStr = size[:len(size)-1]
		case 'G', 'g':
			multiplier = 1024 * 1024 * 1024
			numStr = size[:len(size)-1]
		case 'T', 't':
			multiplier = 1024 * 1024 * 1024 * 1024
			numStr = size[:len(size)-1]
		}
	}

	value, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil || value < 0 {
		return 0, &StorageError{Op: "parse size", Err: errorString("invalid number: " + numStr)}
	}
	// multiplier is always >= 1, so the division is safe and guards against
	// silent int64 overflow on the multiplication below.
	if value > math.MaxInt64/multiplier {
		return 0, &StorageError{Op: "parse size", Err: errorString("size overflow: " + size)}
	}

	return value * multiplier, nil
}

// FormatSize formats bytes to a human-readable string
func FormatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + " B"
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	value := float64(bytes) / float64(div)
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + string("KMGTPE"[exp]) + "iB"
}
