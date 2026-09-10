package provider

import (
	"context"
	"time"
)

// ResourceLimit is one FreeBSD rctl rule.
type ResourceLimit struct {
	Resource string `json:"resource"` // e.g., "memoryuse", "cputime", "maxproc"
	Action   string `json:"action"`   // e.g., "deny", "log", "devctl"
	Amount   string `json:"amount"`   // e.g., "2G", "3600", "100"
}

// VNETConfig represents VNET configuration for a jail.
type VNETConfig struct {
	Enabled      bool     `json:"enabled"`       // Enable VNET
	Interfaces   []string `json:"interfaces"`    // Network interfaces to add to jail
	Bridge       string   `json:"bridge"`        // Bridge to attach epair to
	IPv4Address  string   `json:"ipv4_address"`  // IPv4 address for jail interface
	IPv6Address  string   `json:"ipv6_address"`  // IPv6 address for jail interface
	DefaultRoute string   `json:"default_route"` // Default gateway
}

// RctlProvider is implemented by providers that can apply FreeBSD rctl rules
// to an instance.
//
// Optional, like every other capability here: the handler reaches it by type
// assertion and answers 501 when it is absent.
type RctlProvider interface {
	GetResourceLimits(ctx context.Context, handle InstanceHandle) ([]ResourceLimit, error)
	SetResourceLimits(ctx context.Context, handle InstanceHandle, limits []ResourceLimit) error
	RemoveResourceLimits(ctx context.Context, handle InstanceHandle) error
}

// VNETProvider is implemented by providers that can give an instance its own
// network stack.
type VNETProvider interface {
	GetVNETStatus(ctx context.Context, handle InstanceHandle) (*VNETConfig, error)
	EnableVNET(ctx context.Context, handle InstanceHandle, config VNETConfig) error
	DisableVNET(ctx context.Context, handle InstanceHandle) error
}

// NamedVolume is a storage volume with a life of its own, mounted into an
// instance rather than being part of it.
type NamedVolume struct {
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	ZFSDataset     string            `json:"zfs_dataset"`
	Mountpoint     string            `json:"mountpoint"`
	Quota          int64             `json:"quota,omitempty"`
	Reservation    int64             `json:"reservation,omitempty"`
	Compression    string            `json:"compression,omitempty"`
	ReadOnly       bool              `json:"read_only,omitempty"`
	Created        time.Time         `json:"created"`
	UsedBytes      int64             `json:"used_bytes,omitempty"`
	AvailableBytes int64             `json:"available_bytes,omitempty"`
	MountedTo      []VolumeMountInfo `json:"mounted_to,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
}

// VolumeMountInfo is one instance a volume is mounted into.
type VolumeMountInfo struct {
	JailName  string `json:"jail_name"`
	MountPath string `json:"mount_path"`
	ReadOnly  bool   `json:"read_only"`
}

// NamedVolumeProvider is implemented by providers that can mount a named
// volume into an instance.
type NamedVolumeProvider interface {
	ListVolumes(ctx context.Context) ([]NamedVolume, error)
	MountVolumeToJail(ctx context.Context, volumeName, jailName, mountPath string, readOnly bool) error
	UnmountVolumeFromJail(ctx context.Context, volumeName, jailName string) error
}
