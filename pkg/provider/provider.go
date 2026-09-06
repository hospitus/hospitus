// Package provider defines the core interface that all hypervisor providers must implement.
// This abstraction allows HOSPITUS to support multiple virtualization technologies (jails, bhyve,
// QEMU, Podman, etc.) through a unified API.
package provider

import (
	"context"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
)

// Provider is the main interface that all hypervisor plugins must implement.
// Each virtualization technology (jail, bhyve, QEMU, etc.) provides its own
// implementation of this interface.
type Provider interface {
	// Metadata returns information about the provider
	Metadata() ProviderMetadata

	// Lifecycle management
	Initialize(ctx context.Context, config ProviderConfig) error
	Shutdown(ctx context.Context) error
	HealthCheck(ctx context.Context) error

	// Instance Operations
	CreateInstance(ctx context.Context, spec InstanceSpec) (InstanceHandle, error)
	DeleteInstance(ctx context.Context, handle InstanceHandle, force bool) error
	StartInstance(ctx context.Context, handle InstanceHandle) error
	StopInstance(ctx context.Context, handle InstanceHandle, opts StopOptions) error
	RestartInstance(ctx context.Context, handle InstanceHandle) error

	// State Queries
	GetInstanceState(ctx context.Context, handle InstanceHandle) (InstanceState, error)
	GetInstanceInfo(ctx context.Context, handle InstanceHandle) (InstanceInfo, error)
	ListInstances(ctx context.Context, filter InstanceFilter) ([]InstanceHandle, error)

	// Resource Management
	SetInstanceResources(ctx context.Context, handle InstanceHandle, resources ResourceSpec) error
	GetInstanceMetrics(ctx context.Context, handle InstanceHandle) (Metrics, error)

	// Storage Operations
	AttachDisk(ctx context.Context, handle InstanceHandle, disk DiskAttachment) error
	DetachDisk(ctx context.Context, handle InstanceHandle, diskID string) error

	// Network Operations
	AttachNetwork(ctx context.Context, handle InstanceHandle, network NetworkAttachment) error
	DetachNetwork(ctx context.Context, handle InstanceHandle, interfaceID string) error

	// Capabilities returns what features this provider supports
	Capabilities() ProviderCapabilities
}

// ProviderMetadata describes the provider
type ProviderMetadata struct {
	Name          string       // e.g., "jail", "bhyve", "qemu"
	Version       string       // Provider version
	Type          ProviderType // VM or Container
	Author        string
	Description   string
	Homepage      string
	License       string
	MinAPIVersion string // Minimum HOSPITUS API version required
	MaxAPIVersion string // Maximum HOSPITUS API version supported
}

// ProviderType distinguishes between VMs and containers
type ProviderType string

const (
	ProviderTypeVM        ProviderType = "vm"
	ProviderTypeContainer ProviderType = "container"
)

// ProviderCapabilities declares what features are supported by a provider
type ProviderCapabilities struct {
	// Core capabilities
	SupportsSnapshots     bool
	SupportsMigration     bool
	SupportsLiveMigration bool
	SupportsCloning       bool
	SupportsPause         bool
	SupportsConsole       bool
	SupportsVNC           bool
	SupportsSerial        bool

	// Resource capabilities
	SupportsGPUPassthrough bool
	SupportsUSBPassthrough bool
	SupportsPCIPassthrough bool
	SupportsNUMA           bool
	SupportsBallooning     bool // Memory ballooning

	// Network capabilities
	NetworkTypes         []NetworkType
	MaxNetworkInterfaces int

	// Storage capabilities
	DiskTypes           []DiskType
	MaxDisks            int
	SupportsHotplugDisk bool

	// Architecture support
	SupportedArchitectures []string // amd64, arm64, etc.
	SupportsCrossArch      bool     // Can run non-native arch (emulation)

	// Limits
	MaxCPUs     int
	MaxMemoryMB int64

	// Platform-specific features
	PlatformFeatures map[string]interface{}
}

// ProviderConfig is passed to Initialize()
type ProviderConfig struct {
	// Common settings
	DataDir  string // Where to store provider data
	StateDir string // Where to store runtime state
	LogLevel string
	Logger   *slog.Logger // Structured logger for provider lifecycle and operations

	// Platform-specific settings
	Settings map[string]interface{}

	// Resource limits
	MaxInstances int
	MaxCPUs      int
	MaxMemoryMB  int64
}

// InstanceHandle is an opaque reference to a VM/container
type InstanceHandle struct {
	ID       string                 // Unique identifier
	Provider string                 // Provider name
	Metadata map[string]interface{} // Provider-specific data
}

// WithLogger returns a derived context carrying a structured logger for provider operations.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return logging.WithContext(ctx, logger)
}

// LoggerFromContext returns the structured logger stored in context, if any.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	return logging.FromContext(ctx)
}

// InstanceSpec defines desired instance configuration
type InstanceSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	// Resources
	CPUs      int   `json:"cpus,omitempty"`
	MemoryMB  int64 `json:"memory_mb,omitempty"`
	MaxProc   int   `json:"max_proc,omitempty"`
	ReadBPS   int64 `json:"read_bps,omitempty"`
	WriteBPS  int64 `json:"write_bps,omitempty"`
	ReadIOPS  int64 `json:"read_iops,omitempty"`
	WriteIOPS int64 `json:"write_iops,omitempty"`

	// Boot configuration
	Image      string           `json:"image,omitempty"`      // OS image or template
	OSType     string           `json:"os_type,omitempty"`    // OS type: freebsd, linux, windows, etc.
	OSVersion  string           `json:"os_version,omitempty"` // OS version: 13.2-RELEASE, 14.0-CURRENT, ubuntu-22.04, etc.
	Arch       string           `json:"arch,omitempty"`       // Architecture: amd64, arm64, riscv64, i386
	CloudInit  *CloudInitConfig `json:"cloud_init,omitempty"`
	Bootloader string           `json:"bootloader,omitempty"` // grub, uefi, etc.

	// Disks
	Disks []DiskSpec `json:"disks,omitempty"`

	// Networks
	Networks []NetworkSpec `json:"networks,omitempty"`

	// Provider-specific settings
	ProviderConfig map[string]interface{} `json:"provider_config,omitempty"`

	// Metadata
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// CloudInitConfig for cloud-init configuration
type CloudInitConfig struct {
	UserData string `json:"user_data,omitempty"`
	MetaData string `json:"meta_data,omitempty"`
	Network  string `json:"network,omitempty"`
}

// InstanceState represents the current state
type InstanceState string

const (
	StateUnknown   InstanceState = "unknown"
	StateCreating  InstanceState = "creating"
	StateStopped   InstanceState = "stopped"
	StateStarting  InstanceState = "starting"
	StateRunning   InstanceState = "running"
	StatePaused    InstanceState = "paused"
	StateStopping  InstanceState = "stopping"
	StateMigrating InstanceState = "migrating"
	StateDeleting  InstanceState = "deleting"
	StateError     InstanceState = "error"
)

// InstanceInfo provides detailed instance information
type InstanceInfo struct {
	Handle InstanceHandle
	State  InstanceState
	Spec   InstanceSpec

	// Runtime information
	PID       int // Host process ID (if applicable)
	StartedAt time.Time
	Uptime    time.Duration

	// Network
	IPAddresses  []net.IP
	MACAddresses []string

	// Metrics
	CPUUsage       float64 // Percentage
	MemoryUsageMB  int64
	DiskUsageGB    float64
	NetworkRxBytes int64
	NetworkTxBytes int64
}

// InstanceFilter for filtering instances
type InstanceFilter struct {
	States []InstanceState
	Labels map[string]string
}

// ResourceSpec defines resource allocation
type ResourceSpec struct {
	CPUs      int   `json:"cpus,omitempty"`
	MemoryMB  int64 `json:"memory_mb,omitempty"`
	DiskGB    int   `json:"disk_gb,omitempty"`
	MaxProc   int   `json:"max_proc,omitempty"`
	ReadBPS   int64 `json:"read_bps,omitempty"`
	WriteBPS  int64 `json:"write_bps,omitempty"`
	ReadIOPS  int64 `json:"read_iops,omitempty"`
	WriteIOPS int64 `json:"write_iops,omitempty"`
}

// StopOptions configures how to stop an instance
type StopOptions struct {
	Force   bool          `json:"force"`
	Timeout time.Duration `json:"timeout"`
}

// DiskSpec defines a disk attachment
type DiskSpec struct {
	ID         string    `json:"id,omitempty"`
	Type       DiskType  `json:"type"`
	Path       string    `json:"path"`
	SizeGB     int       `json:"size_gb"`
	Bootable   bool      `json:"bootable,omitempty"`
	ReadOnly   bool      `json:"read_only,omitempty"`
	Cache      CacheMode `json:"cache,omitempty"`
	DeviceName string    `json:"device_name,omitempty"`
	SectorSize int       `json:"sector_size,omitempty"`
}

// DiskType defines disk image format
type DiskType string

const (
	DiskTypeRaw      DiskType = "raw"
	DiskTypeQCOW2    DiskType = "qcow2"
	DiskTypeZVOL     DiskType = "zvol"     // ZFS volume
	DiskTypeVHD      DiskType = "vhd"      // Hyper-V
	DiskTypeVMDK     DiskType = "vmdk"     // VMware
	DiskTypePhysical DiskType = "physical" // Physical device passthrough (e.g., /dev/ada2)
)

// CacheMode for disk caching
type CacheMode string

const (
	CacheModeNone         CacheMode = "none"
	CacheModeWriteback    CacheMode = "writeback"
	CacheModeWritethrough CacheMode = "writethrough"
)

// DiskAttachment for attaching disks
type DiskAttachment struct {
	Disk       DiskSpec `json:"disk"`
	MountPoint string   `json:"mount_point"` // For containers
}

// NetworkSpec defines network configuration
type NetworkSpec struct {
	ID          string      `json:"id,omitempty"`
	Type        NetworkType `json:"type"`
	Bridge      string      `json:"bridge,omitempty"`
	BridgeFlags []string    `json:"bridge_flags,omitempty"`
	VLAN        int         `json:"vlan,omitempty"`
	MAC         string      `json:"mac,omitempty"`
	IPv4        string      `json:"ipv4,omitempty"`
	IPv6        string      `json:"ipv6,omitempty"`
	IPPool      string      `json:"ip_pool,omitempty"` // For auto-creating bridges with specific subnets
	MTU         int         `json:"mtu,omitempty"`
}

// NetworkType defines network attachment type
type NetworkType string

const (
	NetworkTypeBridge  NetworkType = "bridge"
	NetworkTypeNAT     NetworkType = "nat"
	NetworkTypeMacvlan NetworkType = "macvlan"
	NetworkTypeVXLAN   NetworkType = "vxlan"
	NetworkTypeNone    NetworkType = "none"
)

// NetworkAttachment for attaching networks
type NetworkAttachment struct {
	Network NetworkSpec
}

// Metrics provides resource usage statistics
type Metrics struct {
	Timestamp       time.Time
	CPUUsagePercent float64
	MemoryUsedMB    int64
	MemoryTotalMB   int64
	DiskReadBytes   int64
	DiskWriteBytes  int64

	// DiskReadBytesPerSec and DiskWriteBytesPerSec are rates, not totals. Some
	// sources report only rates — FreeBSD's rctl gives readbps/writebps — and
	// storing those in the cumulative fields above makes a consumer's delta
	// between two samples meaningless.
	DiskReadBytesPerSec  int64
	DiskWriteBytesPerSec int64
	NetRxBytes           int64
	NetTxBytes           int64
}

// InstanceAddressProvider is an optional interface for reporting the addresses
// an instance currently holds. Implementations must also implement Provider
// separately.
//
// An address obtained by DHCP is never in the stored spec, which records what
// was declared when the instance was created. GetInstanceInfo already reports
// one, but it reads everything else too — for bhyve, a zfs list per disk — so
// the instance list cannot afford it once per row. This asks for the address
// alone.
//
// Callers are expected to ask only about a running instance: a lease or an ARP
// entry outlives the instance that held it.
type InstanceAddressProvider interface {
	InstanceAddresses(ctx context.Context, handle InstanceHandle) ([]net.IP, error)
}

// ReconfigureProvider is an optional interface for applying a changed spec to an
// instance that already exists. Implementations must also implement Provider
// separately.
//
// A provider that keeps its own on-disk configuration reads that, and not the
// datastore, when it next starts an instance. Without this, a change the API
// records is reported as applied and never reaches the instance: it comes back
// with the resources and parameters it had.
//
// The instance is expected to be stopped; a running one keeps what it was
// started with until it is restarted.
type ReconfigureProvider interface {
	Reconfigure(ctx context.Context, handle InstanceHandle, spec InstanceSpec) error
}

// SnapshotProvider is an optional interface for snapshot support.
// Implementations must also implement Provider separately.
type SnapshotProvider interface {
	CreateSnapshot(ctx context.Context, handle InstanceHandle, name string) (SnapshotHandle, error)
	DeleteSnapshot(ctx context.Context, snapshot SnapshotHandle) error
	RestoreSnapshot(ctx context.Context, handle InstanceHandle, snapshot SnapshotHandle) error
	ListSnapshots(ctx context.Context, handle InstanceHandle) ([]SnapshotInfo, error)
}

// CloneProvider is an optional interface for cloning support.
// Implementations must also implement Provider separately.
type CloneProvider interface {
	// CloneInstance creates a new instance from an existing one
	CloneInstance(ctx context.Context, source InstanceHandle, cloneName string, opts CloneOptions) (InstanceHandle, error)

	// CloneFromSnapshot creates a new instance from a snapshot
	CloneFromSnapshot(ctx context.Context, snapshot SnapshotHandle, cloneName string, opts CloneOptions) (InstanceHandle, error)
}

// CloneOptions configures cloning behavior
type CloneOptions struct {
	// LinkedClone creates a clone that shares storage with the source (faster, less space)
	// FullClone creates an independent copy (slower, more space)
	LinkedClone bool

	// CustomizeResources allows changing CPU/memory during clone
	CPUs     int
	MemoryMB int64

	// Network configuration for the clone
	ResetMAC bool // Generate new MAC addresses

	// Metadata
	Labels      map[string]string
	Annotations map[string]string
}

// SnapshotHandle is an opaque reference to a snapshot
type SnapshotHandle struct {
	ID       string
	Instance string // Instance ID
	Metadata map[string]interface{}
}

// SnapshotInfo provides snapshot information
type SnapshotInfo struct {
	Handle    SnapshotHandle
	Name      string
	CreatedAt time.Time
	SizeMB    int64
}

// ConsoleProvider is an optional interface for console access.
// Implementations must also implement Provider separately.
type ConsoleProvider interface {
	// GetConsole returns a connection to the instance console
	GetConsole(ctx context.Context, handle InstanceHandle) (ConsoleConnection, error)

	// ResizeConsole updates the terminal dimensions for an instance session
	ResizeConsole(ctx context.Context, handle InstanceHandle, width, height int) error
}

// ConsoleConnection represents a console connection
type ConsoleConnection interface {
	Read(p []byte) (n int, err error)
	Write(p []byte) (n int, err error)
	Close() error
}

// ExportImportProvider is an optional interface for export/import support.
// Implementations must also implement Provider separately.
type ExportImportProvider interface {
	// ExportInstance exports an instance to a tarball for migration
	// The tarball contains the instance configuration, metadata and state,
	// and any file-backed disk images inside the instance directory.
	// ZVOL-backed disks are not streamed into the archive.
	//
	// If the instance is running, it should be stopped before export.
	ExportInstance(ctx context.Context, handle InstanceHandle, exportPath string, opts ExportOptions) error

	// ImportInstance imports an instance from a tarball
	// Returns the handle of the imported instance
	ImportInstance(ctx context.Context, importPath string, opts ImportOptions) (InstanceHandle, error)
}

// ExportOptions configures export behavior
type ExportOptions struct {
	// Compress enables gzip compression (recommended for network transfers)
	Compress bool

	// StopInstance stops the instance before export (required for consistency)
	StopInstance bool

	// IncludeSnapshots exports all snapshots (if provider supports snapshots)
	IncludeSnapshots bool
}

// ImportOptions configures import behavior
type ImportOptions struct {
	// NewName optionally renames the instance during import
	NewName string

	// ResetMAC generates new MAC addresses for network interfaces
	ResetMAC bool

	// NewIP sets a new IP address for the imported instance (e.g., "10.0.0.100/24")
	// If empty, the original IP is preserved (or DHCP if ResetMAC is set)
	NewIP string

	// StartAfterImport automatically starts the instance after import
	StartAfterImport bool
}

// AutoStartProvider is an optional interface for auto-start support
// Providers implementing this interface can automatically start instances
// when the hospitusd daemon starts.
type AutoStartProvider interface {
	// SetAutoStart configures auto-start settings for an instance
	SetAutoStart(ctx context.Context, handle InstanceHandle, config AutoStartConfig) error

	// GetAutoStart returns the auto-start configuration for an instance
	GetAutoStart(ctx context.Context, handle InstanceHandle) (*AutoStartConfig, error)

	// ListAutoStartInstances returns all instances configured for auto-start
	// sorted by priority (lowest priority value starts first)
	ListAutoStartInstances(ctx context.Context) ([]InstanceHandle, error)

	// StartAutoStartInstances starts all instances configured for auto-start
	// in priority order with configured delays
	StartAutoStartInstances(ctx context.Context) error
}

// AutoStartConfig defines auto-start behavior for an instance
type AutoStartConfig struct {
	// Enabled indicates whether auto-start is enabled for this instance
	Enabled bool `json:"enabled"`

	// Priority determines the start order (lower values start first)
	// Default: 50, Range: 0-100
	Priority int `json:"priority"`

	// DelayMS is the delay in milliseconds before starting this instance
	// This is applied after the previous instance has started
	DelayMS int `json:"delay_ms"`
}

// ExecProvider is an optional interface for executing commands inside instances.
// This is primarily useful for containers (jails) where commands can be run
// directly inside the isolated environment.
type ExecProvider interface {
	// ExecCommand executes a command inside an instance and returns the result.
	// For interactive commands, use ExecInteractive instead.
	ExecCommand(ctx context.Context, handle InstanceHandle, opts ExecOptions) (*ExecResult, error)

	// ExecInteractive executes an interactive command inside an instance.
	// This attaches stdin/stdout/stderr for interactive use.
	// Returns when the command exits.
	ExecInteractive(ctx context.Context, handle InstanceHandle, opts ExecOptions) error
}

// ExecOptions configures command execution
type ExecOptions struct {
	// Command is the command to execute (e.g., "/bin/sh", "ps")
	Command string `json:"command"`

	// Args are the command arguments
	Args []string `json:"args,omitempty"`

	// Env contains additional environment variables
	Env map[string]string `json:"env,omitempty"`

	// WorkingDir sets the working directory for the command
	WorkingDir string `json:"working_dir,omitempty"`

	// User specifies the user to run the command as (default: root)
	User string `json:"user,omitempty"`

	// Timeout is the maximum time to wait for the command (0 = no timeout)
	Timeout int `json:"timeout,omitempty"`
}

// ExecResult contains the result of a command execution
type ExecResult struct {
	// ExitCode is the command's exit code
	ExitCode int `json:"exit_code"`

	// Stdout contains the standard output
	Stdout string `json:"stdout,omitempty"`

	// Stderr contains the standard error
	Stderr string `json:"stderr,omitempty"`
}

// ExecStreamingProvider is an optional interface for streaming command output.
// This allows real-time output display for long-running commands.
type ExecStreamingProvider interface {
	ExecProvider

	// ExecCommandStream executes a command and streams output to the provided writers.
	// Returns the exit code when the command completes.
	ExecCommandStream(ctx context.Context, handle InstanceHandle, opts ExecOptions, stdout, stderr io.Writer) (int, error)
}

// InstanceHealthCheckProvider is an optional interface for checking instance health.
// Providers implementing this interface can perform health checks on individual instances.
type InstanceHealthCheckProvider interface {
	// CheckInstanceHealth performs a health check on an instance and returns detailed status.
	// This checks if the instance is running and responsive.
	CheckInstanceHealth(ctx context.Context, handle InstanceHandle) (*InstanceHealth, error)
}

// InstanceHealth contains the result of an instance health check
type InstanceHealth struct {
	// Status is the overall health status
	Status HealthStatus `json:"status"`

	// Message provides a human-readable description
	Message string `json:"message,omitempty"`

	// Checks contains individual check results
	Checks []HealthCheck `json:"checks,omitempty"`

	// Timestamp when the health check was performed
	Timestamp time.Time `json:"timestamp"`
}

// HealthStatus represents the overall health state
type HealthStatus string

const (
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
	HealthStatusDegraded  HealthStatus = "degraded"
	HealthStatusUnknown   HealthStatus = "unknown"
)

// HealthCheck represents an individual health check result
type HealthCheck struct {
	Name    string       `json:"name"`
	Status  HealthStatus `json:"status"`
	Message string       `json:"message,omitempty"`
}

// MediaProvider is an optional interface for managing removable media (CD-ROM, ISO, floppy).
// This is essential for ISO-based OS installation workflows where:
//  1. Boot from installation ISO
//  2. Install OS to disk
//  3. Eject ISO
//  4. Change boot order to disk
//  5. Reboot from installed disk
//
// QEMU changes media on a running VM via QMP; bhyve only updates the
// stopped VM's configuration.
type MediaProvider interface {
	// InsertMedia inserts media (ISO, etc.) into a drive slot.
	// QEMU applies this to a running VM via QMP; bhyve updates the
	// configuration of a stopped VM.
	InsertMedia(ctx context.Context, handle InstanceHandle, media MediaSpec) error

	// EjectMedia ejects media from a drive slot.
	// QEMU applies this to a running VM via QMP; bhyve updates the
	// configuration of a stopped VM.
	EjectMedia(ctx context.Context, handle InstanceHandle, deviceID string) error

	// ListMedia returns all currently attached media devices.
	ListMedia(ctx context.Context, handle InstanceHandle) ([]MediaInfo, error)

	// SetBootOrder changes the boot device order.
	// For QEMU: updates boot priority via QMP or config.
	// For bhyve: records the boot order in the VM's config; grub and
	// bhyveload bootloaders are refused.
	SetBootOrder(ctx context.Context, handle InstanceHandle, order BootOrder) error

	// GetBootOrder returns the current boot device order.
	GetBootOrder(ctx context.Context, handle InstanceHandle) (*BootOrder, error)
}

// MediaType defines the type of removable media
type MediaType string

const (
	MediaTypeCDROM  MediaType = "cdrom"  // CD/DVD-ROM (ISO images)
	MediaTypeFloppy MediaType = "floppy" // Floppy disk (rare, but useful for drivers)
	MediaTypeUSB    MediaType = "usb"    // USB storage device
)

// MediaSpec defines media to be inserted
type MediaSpec struct {
	// DeviceID is an optional identifier for the drive slot (e.g., "ide0-cd0", "virtio-cd0")
	// If empty, the provider will use the first available slot.
	DeviceID string `json:"device_id,omitempty"`

	// Type is the media type (cdrom, floppy, usb)
	Type MediaType `json:"type"`

	// Path is the path to the media file (ISO, img, etc.)
	Path string `json:"path"`

	// ReadOnly indicates if the media should be read-only (default: true for CD-ROM)
	ReadOnly bool `json:"read_only"`

	// Bootable marks this media as bootable
	Bootable bool `json:"bootable,omitempty"`
}

// MediaInfo describes currently attached media
type MediaInfo struct {
	// DeviceID is the identifier for the drive slot
	DeviceID string `json:"device_id"`

	Type MediaType `json:"type"`

	// Path is the path to the media file (empty if ejected)
	Path string `json:"path,omitempty"`

	// Inserted indicates if media is currently inserted
	Inserted bool `json:"inserted"`

	// Locked indicates if the media is locked (cannot be ejected)
	Locked bool `json:"locked"`

	// Bootable indicates if this device is in the boot order
	Bootable bool `json:"bootable"`
}

// BootDevice represents a bootable device type
type BootDevice string

const (
	BootDeviceHardDisk BootDevice = "disk"    // Hard disk / primary storage
	BootDeviceCDROM    BootDevice = "cdrom"   // CD/DVD-ROM
	BootDeviceNetwork  BootDevice = "network" // PXE network boot
	BootDeviceFloppy   BootDevice = "floppy"  // Floppy disk
	BootDeviceUSB      BootDevice = "usb"     // USB device
)

// BootOrder defines the boot device priority
type BootOrder struct {
	// Devices is the ordered list of boot devices (first = highest priority)
	Devices []BootDevice `json:"devices"`

	// OnceDevice optionally boots once from this device, then reverts to Devices order
	// This is useful for one-time installation boots.
	OnceDevice BootDevice `json:"once_device,omitempty"`
}

// CheckpointProvider is an optional interface for providers that support VM checkpointing.
// A checkpoint suspends a running VM to disk so it can be resumed later.
type CheckpointProvider interface {
	// CheckpointInstance suspends the running VM and saves its state to disk.
	// The VM is stopped after checkpointing.
	CheckpointInstance(ctx context.Context, handle InstanceHandle, name string) error

	// RestoreCheckpoint resumes a VM from a previously saved checkpoint.
	// The VM must be stopped before restoring.
	RestoreCheckpoint(ctx context.Context, handle InstanceHandle, name string) error

	// ListCheckpoints returns all checkpoints available for an instance.
	ListCheckpoints(ctx context.Context, handle InstanceHandle) ([]CheckpointInfo, error)

	// DeleteCheckpoint removes a checkpoint file.
	DeleteCheckpoint(ctx context.Context, handle InstanceHandle, name string) error
}

// CheckpointInfo describes a saved VM checkpoint.
type CheckpointInfo struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	SizeMB    float64   `json:"size_mb"`
}

// PauseProvider is an optional interface for providers that support pausing instances.
// Pausing freezes execution (SIGSTOP, or the container runtime's pause
// facility) without terminating the instance process.
type PauseProvider interface {
	// PauseInstance freezes execution of a running instance.
	PauseInstance(ctx context.Context, handle InstanceHandle) error

	// ResumeInstance unfreezes a paused instance.
	ResumeInstance(ctx context.Context, handle InstanceHandle) error
}

// RenameProvider is an optional interface for providers that support renaming instances.
type RenameProvider interface {
	// RenameInstance renames a stopped instance.
	// Returns an error if the instance is not stopped or the new name is already taken.
	RenameInstance(ctx context.Context, handle InstanceHandle, newName string) error
}

// UpgradeProvider is an optional interface for providers that support upgrading
// the OS base system of an instance (e.g. FreeBSD jail upgrade via freebsd-update(8)).
type UpgradeProvider interface {
	// UpgradeInstance upgrades the base system of a stopped instance to targetRelease.
	// This is a long-running operation; callers should run it inside an async job.
	UpgradeInstance(ctx context.Context, handle InstanceHandle, targetRelease string) error
}

// PortForward represents a port forwarding rule.
type PortForward struct {
	Protocol  string `json:"protocol"`   // "tcp" or "udp"
	HostPort  int    `json:"host_port"`  // Port on the host
	GuestPort int    `json:"guest_port"` // Port inside the VM
}

// PortForwardProvider is an optional interface for providers that support
// dynamic port forwarding (e.g. QEMU user-mode networking via hostfwd).
type PortForwardProvider interface {
	// AddPortForward adds a port forwarding rule.
	// For running instances this takes effect immediately; for stopped instances
	// the rule is persisted and applied on next start.
	AddPortForward(ctx context.Context, handle InstanceHandle, pf PortForward) error

	// RemovePortForward removes a port forwarding rule by host port and protocol.
	RemovePortForward(ctx context.Context, handle InstanceHandle, protocol string, hostPort int) error

	// ListPortForwards returns all port forwarding rules for the instance.
	ListPortForwards(ctx context.Context, handle InstanceHandle) ([]PortForward, error)
}
