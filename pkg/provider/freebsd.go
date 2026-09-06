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

// Route represents a network route
type Route struct {
	// Destination network (CIDR) or "default"
	Destination string `json:"destination"`
	// Gateway IP address
	Gateway string `json:"gateway"`
	// Metric for route preference (lower = preferred)
	Metric int `json:"metric,omitempty"`
}

type NetworkInterface struct {
	// Name is the interface name inside the jail (e.g., "eth0", "lan", "wan")
	// If empty, uses default naming (epairNb)
	Name string `json:"name,omitempty"`

	// Bridge is the bridge to connect this interface to
	Bridge string `json:"bridge"`

	// BridgeFlags are the flags applied to the bridge member (e.g., "private")
	BridgeFlags []string `json:"bridge_flags,omitempty"`

	// VLAN tag for the interface (0 = untagged)
	VLAN int `json:"vlan,omitempty"`

	// IPv4 configuration
	IPv4Address string `json:"ipv4_address,omitempty"` // CIDR notation: 10.0.0.2/24
	IPv4Gateway string `json:"ipv4_gateway,omitempty"` // Default gateway for this interface

	// IPv6 configuration
	IPv6Address string `json:"ipv6_address,omitempty"` // CIDR notation: fd00::2/64
	IPv6Gateway string `json:"ipv6_gateway,omitempty"` // Default IPv6 gateway

	// DHCP configuration
	DHCPv4 bool `json:"dhcpv4,omitempty"` // Use DHCP for IPv4
	DHCPv6 bool `json:"dhcpv6,omitempty"` // Use DHCPv6 for IPv6
	SLAAC  bool `json:"slaac,omitempty"`  // Use SLAAC for IPv6

	// Interface options
	MTU         int      `json:"mtu,omitempty"`         // Interface MTU (0 = default)
	MAC         string   `json:"mac,omitempty"`         // Custom MAC address
	Description string   `json:"description,omitempty"` // Interface description
	Primary     bool     `json:"primary,omitempty"`     // Is this the primary/default interface
	Routes      []Route  `json:"routes,omitempty"`      // Additional routes via this interface
	DNSServers  []string `json:"dns_servers,omitempty"` // DNS servers for this interface

	// Internal - populated after creation
	HostInterface string `json:"host_interface,omitempty"` // epairNa on host side
	JailInterface string `json:"jail_interface,omitempty"` // epairNb in jail
}

// NetworkInterfaceProvider is the optional capability for managing an
// instance's network interfaces.
//
// Asserted by the handlers instead of *jail.JailProvider: the concrete type
// meant anything wrapping a jail provider — a decorator, a test double — was
// told 501 for operations it implements.
type NetworkInterfaceProvider interface {
	ListNetworkInterfaces(ctx context.Context, handle InstanceHandle) ([]NetworkInterface, error)
	AddNetworkInterface(ctx context.Context, handle InstanceHandle, iface NetworkInterface) (*NetworkInterface, error)
	RemoveNetworkInterface(ctx context.Context, handle InstanceHandle, interfaceName string) error
}

// ServiceInfo represents information about a service
type ServiceInfo struct {
	// Name is the service name (e.g., "nginx", "postgresql")
	Name string `json:"name"`

	// Enabled indicates if the service is enabled in rc.conf
	Enabled bool `json:"enabled"`

	// Running indicates if the service is currently running
	Running bool `json:"running"`

	// Description is a human-readable description
	Description string `json:"description,omitempty"`

	// RCScript is the path to the rc.d script
	RCScript string `json:"rc_script,omitempty"`
}

// ServiceProvider is the optional capability for managing the services inside
// an instance.
//
// Asserted by the handlers instead of *jail.JailProvider, for the same reason
// as NetworkInterfaceProvider: the concrete type meant anything wrapping a
// jail provider was told 501 for operations it implements.
type ServiceProvider interface {
	ListServices(ctx context.Context, handle InstanceHandle) ([]ServiceInfo, error)
	ListEnabledServices(ctx context.Context, handle InstanceHandle) ([]ServiceInfo, error)
	ListRunningServices(ctx context.Context, handle InstanceHandle) ([]ServiceInfo, error)
	GetServiceStatus(ctx context.Context, handle InstanceHandle, serviceName string) (*ServiceInfo, error)

	StartService(ctx context.Context, handle InstanceHandle, serviceName string) error
	StopService(ctx context.Context, handle InstanceHandle, serviceName string) error
	RestartService(ctx context.Context, handle InstanceHandle, serviceName string) error
	ReloadService(ctx context.Context, handle InstanceHandle, serviceName string) error
	EnableService(ctx context.Context, handle InstanceHandle, serviceName string) error
	DisableService(ctx context.Context, handle InstanceHandle, serviceName string) error
}
