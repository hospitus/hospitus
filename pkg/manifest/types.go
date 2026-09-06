// Package manifest defines the Unified Workload Manifest (UWM) types for TOML-based
// workload definitions. These types allow users to declare workloads in a provider-agnostic
// way that can be deployed to Jail, bhyve, QEMU, or Podman.
package manifest

import (
	"time"
)

// APIVersion is the current manifest API version
const APIVersion = "hospitus.io/v1"

// WorkloadManifest represents a single workload definition (hospitus-workload.toml)
type WorkloadManifest struct {
	Workload          WorkloadMeta              `toml:"workload"`
	Provider          ProviderSpec              `toml:"provider"`
	Image             ImageSpec                 `toml:"image"`
	Resources         ResourceSpec              `toml:"resources"`
	Networks          []NetworkSpec             `toml:"networks"`
	Storage           StorageSpec               `toml:"storage"`
	CloudInit         *CloudInitSpec            `toml:"cloud_init"`
	Lifecycle         LifecycleSpec             `toml:"lifecycle"`
	Environment       map[string]string         `toml:"environment"`
	ProviderOverrides map[string]ProviderConfig `toml:"provider_overrides"`
}

// StackManifest represents a multi-instance stack (web-stack.toml)
type StackManifest struct {
	Stack     StackMeta        `toml:"stack"`
	Instances []InstanceConfig `toml:"instances"`
}

// WorkloadMeta contains workload metadata
type WorkloadMeta struct {
	APIVersion  string            `toml:"api_version"`
	Name        string            `toml:"name"`
	Description string            `toml:"description"`
	Labels      map[string]string `toml:"labels"`
	Annotations map[string]string `toml:"annotations"`
}

// StackMeta contains stack metadata
type StackMeta struct {
	APIVersion string `toml:"api_version"`
	Name       string `toml:"name"`
}

// ProviderSpec defines the target provider
type ProviderSpec struct {
	// Type is REQUIRED: "jail" | "bhyve" | "qemu" | "podman"
	Type string `toml:"type"`
}

// ProviderType constants
const (
	ProviderTypeJail   = "jail"
	ProviderTypeBhyve  = "bhyve"
	ProviderTypeQEMU   = "qemu"
	ProviderTypePodman = "podman"
)

// ImageSpec defines the base image/system
type ImageSpec struct {
	// Source format: "type:reference"
	// - "freebsd:14.3-RELEASE" → jail/bhyve
	// - "oci:nginx:latest" → podman
	// - "cloud:ubuntu-24.04" → bhyve/qemu
	// - "iso:FreeBSD-14.3-amd64-dvd1" → bhyve/qemu
	Source string `toml:"source"`

	// Arch: amd64, arm64, riscv64
	Arch string `toml:"arch"`
}

// ImageType constants for image source prefixes
const (
	ImageTypeFreeBSD = "freebsd"
	ImageTypeOCI     = "oci"
	ImageTypeCloud   = "cloud"
	ImageTypeISO     = "iso"
)

// ResourceSpec defines compute resources
type ResourceSpec struct {
	CPU    int    `toml:"cpu"`
	Memory string `toml:"memory"` // "4Gi", "512Mi"
}

// NetworkSpec defines a network interface
type NetworkSpec struct {
	Name        string   `toml:"name"`
	Type        string   `toml:"type"`         // bridge | nat | macvlan | vxlan | none
	Bridge      string   `toml:"bridge"`       // optional, auto-created if absent
	BridgeFlags []string `toml:"bridge_flags"` // optional, e.g. ["private"]
	VLAN        int      `toml:"vlan"`         // optional VLAN tag
	MAC         string   `toml:"mac"`          // optional MAC address
	IPPool      string   `toml:"ip_pool"`      // optional IP pool for bridge auto-creation
	MTU         int      `toml:"mtu"`          // optional MTU

	IP    *IPConfig    `toml:"ip"`
	Ports []PortConfig `toml:"ports"`
}

// NetworkType constants
const (
	NetworkTypeBridge  = "bridge"
	NetworkTypeNAT     = "nat"
	NetworkTypeMacvlan = "macvlan"
	NetworkTypeVXLAN   = "vxlan"
	NetworkTypeNone    = "none"
)

// IPConfig defines IP configuration
type IPConfig struct {
	Mode    string   `toml:"mode"` // dhcp | static | none
	Address string   `toml:"address"`
	Gateway string   `toml:"gateway"`
	DNS     []string `toml:"dns"`
}

// IPMode constants
const (
	IPModeDHCP   = "dhcp"
	IPModeStatic = "static"
	IPModeNone   = "none"
)

// PortConfig defines port forwarding
type PortConfig struct {
	Host      int    `toml:"host"`
	Container int    `toml:"container"`
	Protocol  string `toml:"protocol"` // tcp | udp
}

// StorageSpec defines storage configuration
type StorageSpec struct {
	RootDisk *RootDiskSpec `toml:"root_disk"`
	Volumes  []VolumeSpec  `toml:"volumes"`
}

// RootDiskSpec defines the root/boot disk
type RootDiskSpec struct {
	Size string `toml:"size"` // "20Gi"
	Type string `toml:"type"` // auto | zvol | qcow2 | raw | physical
	Path string `toml:"path"` // For physical device passthrough (e.g., /dev/ada2)
}

// DiskType constants
const (
	DiskTypeAuto     = "auto"
	DiskTypeZVOL     = "zvol"
	DiskTypeQCOW2    = "qcow2"
	DiskTypeRaw      = "raw"
	DiskTypePhysical = "physical" // Physical device passthrough (e.g., /dev/ada2)
)

// VolumeSpec defines an additional volume
type VolumeSpec struct {
	Name      string     `toml:"name"`
	Size      string     `toml:"size"` // "100Gi" (optional for host mounts)
	MountPath string     `toml:"mount_path"`
	HostPath  string     `toml:"host_path"` // For bind/nullfs mounts
	ReadOnly  bool       `toml:"read_only"`
	ZFS       *ZFSConfig `toml:"zfs"`
}

// ZFSConfig defines ZFS-specific options
type ZFSConfig struct {
	Compression string `toml:"compression"` // lz4, gzip, zstd, off
	Quota       string `toml:"quota"`       // "100Gi"
}

// LifecycleSpec defines lifecycle behavior
type LifecycleSpec struct {
	StopTimeout int              `toml:"stop_timeout"` // Seconds to wait for graceful stop
	Autostart   *AutostartConfig `toml:"autostart"`
	Hooks       *HooksConfig     `toml:"hooks"`
	HealthCheck *HealthCheckSpec `toml:"health_check"`
	Restart     *RestartSpec     `toml:"restart"`
}

// RestartSpec asks for an instance to be restarted when its health check says
// it is unhealthy.
//
// Without it the orchestrator's AutoRestarter is registered, wired into the
// health callbacks, and unable to act: no policy was ever set for a deployed
// instance, so every transition returned before reaching a restart.
type RestartSpec struct {
	// Enabled turns automatic restarts on for this instance.
	Enabled bool `toml:"enabled"`

	// MaxRestarts caps how many times it may be restarted (0 = no cap).
	MaxRestarts int `toml:"max_restarts"`

	// Delay is the minimum time between two restarts (e.g. "30s").
	Delay string `toml:"delay"`

	// ResetAfter clears the counter once the instance has been healthy for
	// this long (e.g. "10m"). Empty means the counter never resets, which
	// makes MaxRestarts a lifetime limit.
	ResetAfter string `toml:"reset_after"`
}

// HealthCheckSpec defines health check configuration
type HealthCheckSpec struct {
	// Command to execute inside the instance to check health
	Command []string `toml:"command"`

	// Interval between health checks (e.g., "30s", "1m")
	Interval string `toml:"interval"`

	// Timeout for each health check (e.g., "10s")
	Timeout string `toml:"timeout"`

	// Retries before marking as unhealthy
	Retries int `toml:"retries"`

	// StartPeriod is grace period before health checks start (e.g., "60s")
	StartPeriod string `toml:"start_period"`
}

// AutostartConfig defines auto-start behavior
type AutostartConfig struct {
	Enabled  bool   `toml:"enabled"`
	Priority int    `toml:"priority"` // 0-100, lower = earlier
	Delay    string `toml:"delay"`    // "5s"
}

// HooksConfig defines lifecycle hooks (executed on host or inside instance)
type HooksConfig struct {
	// Simple string hooks (executed on host as shell scripts)
	PreStart  string `toml:"pre_start"`
	PostStart string `toml:"post_start"`
	PreStop   string `toml:"pre_stop"`

	// Structured hooks with commands array (executed inside instance)
	PreCreate  []HookSpec `toml:"pre_create"`
	PostCreate []HookSpec `toml:"post_create"`
}

// HookSpec defines a structured hook with type and commands
type HookSpec struct {
	// Type must be "exec": the commands run inside the instance. Apply skips
	// every hook of another type, so the validator refuses one rather than let
	// it be dropped in silence.
	Type string `toml:"type"`

	// Commands to execute (for exec type, runs inside the instance)
	Commands []string `toml:"commands"`

	// OnFailure: "continue" | "stop" (default: stop)
	OnFailure string `toml:"on_failure"`
}

// CloudInitSpec defines cloud-init configuration for VMs.
// Compatible with both cloud-init (Linux) and nuageinit (FreeBSD 14.1+).
// Only valid for bhyve and qemu providers.
type CloudInitSpec struct {
	// Enabled activates cloud-init ISO generation. It is a pointer so the parser
	// can distinguish "unset" (nil, default to true when the section has content)
	// from an explicit `enabled = false` (which must be honored).
	Enabled *bool `toml:"enabled"`

	// Hostname override (defaults to workload name)
	Hostname string `toml:"hostname"`

	// Users defines users to create
	Users []CloudInitUser `toml:"users"`

	// Packages to install on first boot
	Packages []string `toml:"packages"`

	// RunCMD commands to execute on first boot
	RunCMD []string `toml:"runcmd"`

	// SSHAuthorizedKeys for the default user
	SSHAuthorizedKeys []string `toml:"ssh_authorized_keys"`

	// UserData is raw cloud-config YAML (advanced usage, appended to generated config)
	UserData string `toml:"user_data"`

	// UserDataFile is the path to an external user-data file. It replaces
	// inline user_data; like it, the content is appended after the generated
	// cloud-config rather than replacing it.
	UserDataFile string `toml:"user_data_file"`
}

// CloudInitUser defines a user to create via cloud-init
type CloudInitUser struct {
	// Name is the username (required)
	Name string `toml:"name"`

	// SSHAuthorizedKeys for this user
	SSHAuthorizedKeys []string `toml:"ssh_authorized_keys"`

	// Sudo permission string, e.g., "ALL=(ALL) NOPASSWD:ALL"
	Sudo string `toml:"sudo"`

	// Doas is a FreeBSD doas rule (nuageinit); %u is replaced with username.
	// e.g., "permit nopass %u as root"
	Doas string `toml:"doas"`

	// Shell path, e.g., "/bin/bash"
	Shell string `toml:"shell"`

	// Groups to add the user to
	Groups []string `toml:"groups"`

	// LockPasswd disables password login (Linux cloud-init only).
	// Not supported by nuageinit — use plain_text_passwd + ssh_authorized_keys for FreeBSD VMs.
	LockPasswd bool `toml:"lock_passwd"`

	// PlainTextPasswd sets a plain-text password (nuageinit: plain_text_passwd).
	// Useful for console access on FreeBSD VMs when no SSH key is configured.
	PlainTextPasswd string `toml:"plain_text_passwd"`
}

// ProviderConfig holds provider-specific overrides
// Each provider has its own structure
type ProviderConfig struct {
	// OS overrides
	OSType    string `toml:"os_type"`
	OSVersion string `toml:"os_version"`

	// Jail-specific
	Parameters map[string]interface{} `toml:"parameters"`

	// bhyve-specific
	Bootloader  string   `toml:"bootloader"`
	ConsoleType string   `toml:"console_type"`
	Passthrough []string `toml:"passthrough"`
	// VNC accepts "host:port" (e.g. "127.0.0.1:5911") or just a port number.
	// Setting this enables VNC for the VM at creation time.
	VNC string `toml:"vnc"`
	// DiskDriver sets the default disk driver for all disks ("ahci-hd", "virtio-blk", "nvme").
	// When bootloader = "uefi", the boot disk (disk0) automatically uses "ahci-hd" unless
	// overridden here, because OVMF firmware cannot see virtio-blk devices.
	DiskDriver string `toml:"disk_driver"`

	// QEMU-specific
	Machine string `toml:"machine"`
	CPU     string `toml:"cpu"`

	// Podman-specific
	Command []string `toml:"command"`
}

// InstanceConfig defines an instance within a stack
type InstanceConfig struct {
	Name              string                    `toml:"name"`
	Provider          string                    `toml:"provider"`
	Image             ImageSpec                 `toml:"image"`
	Resources         ResourceSpec              `toml:"resources"`
	Networks          []NetworkSpec             `toml:"networks"`
	Storage           StorageSpec               `toml:"storage"`
	CloudInit         *CloudInitSpec            `toml:"cloud_init"`
	Lifecycle         LifecycleSpec             `toml:"lifecycle"`
	Environment       map[string]string         `toml:"environment"`
	ProviderOverrides map[string]ProviderConfig `toml:"provider_overrides"`
	DependsOn         *DependsOnConfig          `toml:"depends_on"`
}

// DependsOnConfig defines dependencies between instances in a stack
type DependsOnConfig struct {
	Services  []string `toml:"services"`
	Condition string   `toml:"condition"` // "healthy" | "started"
}

// DependsOnCondition constants
const (
	DependsOnConditionHealthy = "healthy"
	DependsOnConditionStarted = "started"
)

// ParsedManifest is the result of parsing a manifest file
type ParsedManifest struct {
	// One of these will be set
	Workload *WorkloadManifest
	Stack    *StackManifest

	// Source file info
	FilePath   string
	ParsedAt   time.Time
	APIVersion string
}

// IsStack returns true if this is a stack manifest
func (p *ParsedManifest) IsStack() bool {
	return p.Stack != nil
}

// IsWorkload returns true if this is a single workload manifest
func (p *ParsedManifest) IsWorkload() bool {
	return p.Workload != nil
}

// GetName returns the workload or stack name
func (p *ParsedManifest) GetName() string {
	if p.Workload != nil {
		return p.Workload.Workload.Name
	}
	if p.Stack != nil {
		return p.Stack.Stack.Name
	}
	return ""
}

// ValidationError represents a manifest validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}

// ValidationErrors is a collection of validation errors
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return "no errors"
	}
	if len(e) == 1 {
		return e[0].Error()
	}
	msg := "multiple validation errors:\n"
	for _, err := range e {
		msg += "  - " + err.Error() + "\n"
	}
	return msg
}

// HasErrors returns true if there are validation errors
func (e ValidationErrors) HasErrors() bool {
	return len(e) > 0
}

// HasWarnings returns false - all errors are treated as errors for now
func (e ValidationErrors) HasWarnings() bool {
	return false
}

// Errors returns all errors
func (e ValidationErrors) Errors() ValidationErrors {
	return e
}

// Warnings returns an empty slice - no warnings for now
func (e ValidationErrors) Warnings() ValidationErrors {
	return nil
}
