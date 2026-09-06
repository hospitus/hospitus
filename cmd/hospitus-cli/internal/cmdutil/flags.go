package cmdutil

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// ParseSize parses a size string (e.g., "10G", "500M") to bytes
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" || s == "NONE" || s == "0" {
		return 0, nil
	}

	var multiplier int64 = 1
	var numStr string

	switch {
	case strings.HasSuffix(s, "T") || strings.HasSuffix(s, "TB"):
		multiplier = 1024 * 1024 * 1024 * 1024
		numStr = strings.TrimSuffix(strings.TrimSuffix(s, "TB"), "T")
	case strings.HasSuffix(s, "G") || strings.HasSuffix(s, "GB"):
		multiplier = 1024 * 1024 * 1024
		numStr = strings.TrimSuffix(strings.TrimSuffix(s, "GB"), "G")
	case strings.HasSuffix(s, "M") || strings.HasSuffix(s, "MB"):
		multiplier = 1024 * 1024
		numStr = strings.TrimSuffix(strings.TrimSuffix(s, "MB"), "M")
	case strings.HasSuffix(s, "K") || strings.HasSuffix(s, "KB"):
		multiplier = 1024
		numStr = strings.TrimSuffix(strings.TrimSuffix(s, "KB"), "K")
	case strings.HasSuffix(s, "B"):
		multiplier = 1
		numStr = strings.TrimSuffix(s, "B")
	default:
		numStr = s
	}

	numStr = strings.TrimSpace(numStr)
	val, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size format: %s", s)
	}
	if val < 0 {
		return 0, fmt.Errorf("size cannot be negative: %s", s)
	}
	if val > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("size too large: %s", s)
	}

	return val * multiplier, nil
}

// Validation limits for resource flags
const (
	MinCPUs     = 1
	MaxCPUs     = 1024
	MinMemoryMB = 64
	MaxMemoryMB = 4 * 1024 * 1024 // 4 TB
	MinPort     = 1
	MaxPort     = 65535
)

// ValidateCPUs validates the CPU count is within acceptable range
func ValidateCPUs(cpus int) error {
	if cpus < MinCPUs || cpus > MaxCPUs {
		return fmt.Errorf("CPU count must be between %d and %d, got %d", MinCPUs, MaxCPUs, cpus)
	}
	return nil
}

// ValidateMemory validates the memory size is within acceptable range
func ValidateMemory(memoryMB int64) error {
	if memoryMB < MinMemoryMB || memoryMB > MaxMemoryMB {
		return fmt.Errorf("memory must be between %d MB and %d MB (4 TB), got %d MB",
			MinMemoryMB, MaxMemoryMB, memoryMB)
	}
	return nil
}

// ValidatePort validates a port number is within valid range
func ValidatePort(port int) error {
	if port < MinPort || port > MaxPort {
		return fmt.Errorf("port must be between %d and %d, got %d", MinPort, MaxPort, port)
	}
	return nil
}

// ValidateResourceFlags validates CPU and memory flags from a command
func ValidateResourceFlags(cmd *cobra.Command) error {
	cpus, err := cmd.Flags().GetInt(FlagCPUs)
	if err != nil {
		return fmt.Errorf("failed to get CPUs flag: %w", err)
	}
	if err := ValidateCPUs(cpus); err != nil {
		return err
	}

	memory, err := cmd.Flags().GetInt64(FlagMemory)
	if err != nil {
		return fmt.Errorf("failed to get memory flag: %w", err)
	}
	if err := ValidateMemory(memory); err != nil {
		return err
	}

	return nil
}

// Common flag names
const (
	FlagImage      = "image"
	FlagCPUs       = "cpus"
	FlagMemory     = "memory"
	FlagDisk       = "disk"
	FlagNetwork    = "network"
	FlagVNET       = "vnet"
	FlagBridge     = "bridge"
	FlagIP         = "ip"
	FlagOutput     = "output"
	FlagForce      = "force"
	FlagAll        = "all"
	FlagOSType     = "os-type"
	FlagOSVersion  = "os-version"
	FlagArch       = "arch"
	FlagBootloader = "bootloader"
	FlagCloudInit  = "cloud-init"
	// FlagStart starts the instance right after creating it. "auto-start" is
	// what a reader takes for CBSD's astart — starting at host boot — so that
	// name belongs to the boot flag below, not to this one.
	FlagPull        = "pull"
	FlagStart       = "start"
	FlagDescription = "description"
	FlagVersion     = "version" // FreeBSD version shortcut

	// Advanced networking
	FlagVLAN        = "vlan"
	FlagBridgeFlags = "bridge-flag"
	FlagIPv6        = "ipv6"
	FlagIPv6Prefix  = "ipv6-prefix"

	// USB / input devices
	FlagUSBTablet  = "usb-tablet"
	FlagUSBDevices = "usb-device"

	// PCI passthrough (GPU, NIC, storage controllers)
	FlagPassthrough = "passthrough"

	// Storage driver
	FlagDiskDriver = "disk-driver"

	// Advanced resource limits
	FlagMaxProc   = "max-proc"
	FlagReadBPS   = "read-bps"
	FlagWriteBPS  = "write-bps"
	FlagReadIOPS  = "read-iops"
	FlagWriteIOPS = "write-iops"

	// Advanced storage
	FlagSectorSize = "sector-size"

	// Auto-start on boot flags
	// The boot flags are CBSD's astart: start this instance when the host
	// boots, not when the command returns.
	FlagBootAutoStart         = "auto-start"
	FlagBootAutoStartPriority = "auto-start-priority"
	FlagBootAutoStartDelay    = "auto-start-delay"

	// Jail security parameters
	FlagAllowRawSockets    = "allow-raw-sockets"
	FlagAllowSysVIPC       = "allow-sysvipc"
	FlagAllowMount         = "allow-mount"
	FlagAllowMountDevfs    = "allow-mount-devfs"
	FlagAllowMountNullfs   = "allow-mount-nullfs"
	FlagAllowMountTmpfs    = "allow-mount-tmpfs"
	FlagAllowMountZFS      = "allow-mount-zfs"
	FlagAllowVMM           = "allow-vmm"
	FlagAllowMlock         = "allow-mlock"
	FlagAllowReservedPorts = "allow-reserved-ports"

	// Jail lifecycle hooks
	FlagExecPrestart  = "exec-prestart"
	FlagExecPoststart = "exec-poststart"
	FlagExecPrestop   = "exec-prestop"
	FlagExecPoststop  = "exec-poststop"
	FlagExecClean     = "exec-clean"

	// Jail behavior
	FlagDevfsRuleset = "devfs-ruleset"
	FlagPersist      = "persist"
	FlagChildrenMax  = "children-max"
	FlagSecurelevel  = "securelevel"
)

// AddCoreResourceFlags adds the resource flags every provider reads: CPU
// count, memory, and disk specification.
func AddCoreResourceFlags(cmd *cobra.Command) {
	cmd.Flags().IntP(FlagCPUs, "c", 1, "Number of CPUs")
	cmd.Flags().Int64P(FlagMemory, "m", 512, "Memory in MB")
	cmd.Flags().StringSliceP(FlagDisk, "d", nil, "Disk spec: <sizeGB>[:<name>], or physical:/dev/xxx (repeatable)")
}

// AddResourceLimitFlags adds the rctl-style limit flags (jail and bhyve).
func AddResourceLimitFlags(cmd *cobra.Command) {
	cmd.Flags().Int(FlagMaxProc, 0, "Maximum number of processes")
	cmd.Flags().String(FlagReadBPS, "", "Read bytes per second limit (e.g., 10M)")
	cmd.Flags().String(FlagWriteBPS, "", "Write bytes per second limit (e.g., 10M)")
	cmd.Flags().Int64(FlagReadIOPS, 0, "Read IOPS limit")
	cmd.Flags().Int64(FlagWriteIOPS, 0, "Write IOPS limit")
}

// AddResourceFlags adds resource-related flags (CPU, memory, disk) plus limits
func AddResourceFlags(cmd *cobra.Command) {
	AddCoreResourceFlags(cmd)
	AddResourceLimitFlags(cmd)
}

// AddNetworkFlags adds network-related flags. VNET is jail-only, so the jail
// create command registers --vnet itself.
func AddNetworkFlags(cmd *cobra.Command) {
	cmd.Flags().StringP(FlagBridge, "b", "", "Bridge interface")
	cmd.Flags().Int(FlagVLAN, 0, "VLAN tag for the interface")
	cmd.Flags().StringSlice(FlagBridgeFlags, nil, "Bridge member flags (e.g., 'private')")
	cmd.Flags().StringP(FlagIP, "i", "", "IP address (or 'dhcp')")

	// No --network here: nothing parses it. Several interfaces come from
	// repeated [[networks]] tables in a manifest.
}

// AddAdvancedStorageFlags adds advanced storage flags
func AddAdvancedStorageFlags(cmd *cobra.Command) {
	cmd.Flags().Int(FlagSectorSize, 0, "Logical/physical sector size (e.g., 512 or 4096)")
}

// AddOSFlags adds OS-related flags. The -V FreeBSD version shortcut is
// jail-only, so the jail create command registers it itself.
func AddOSFlags(cmd *cobra.Command) {
	cmd.Flags().String(FlagOSType, "freebsd", "OS type (freebsd, linux, windows)")
	cmd.Flags().String(FlagOSVersion, "", "OS version (e.g., 14.1-RELEASE)")
	cmd.Flags().String(FlagArch, "native", "Architecture (native, amd64, arm64, riscv64, i386)")
}

// AddOutputFlag adds the --output flag for format selection
func AddOutputFlag(cmd *cobra.Command) {
	cmd.Flags().StringP(FlagOutput, "o", "table", "Output format (table, json)")
}

// AddForceFlag adds the --force/-f flag for forcing operations
func AddForceFlag(cmd *cobra.Command) {
	cmd.Flags().BoolP(FlagForce, "f", false, "Force operation (e.g., force destroy even if busy)")
}

// AddYesFlag adds the --yes/-y flag for bypassing confirmations
func AddYesFlag(cmd *cobra.Command) {
	cmd.Flags().BoolP("yes", "y", false, "Automatic yes to prompts (bypass confirmation)")
}

// AddBootAutoStartFlags adds boot auto-start related flags
func AddBootAutoStartFlags(cmd *cobra.Command) {
	cmd.Flags().Bool(FlagBootAutoStart, false, "Start this instance when the host boots")
	cmd.Flags().Int(FlagBootAutoStartPriority, 50, "Boot order (0-100, lower starts first)")
	cmd.Flags().Int(FlagBootAutoStartDelay, 0, "Delay in milliseconds before starting at boot")
}

// AddJailSecurityFlags adds jail security related flags
func AddJailSecurityFlags(cmd *cobra.Command) {
	cmd.Flags().Bool(FlagAllowRawSockets, false, "Allow raw sockets (ping, traceroute)")
	cmd.Flags().Bool(FlagAllowSysVIPC, false, "Allow System V IPC (required for PostgreSQL)")
	cmd.Flags().Bool(FlagAllowMount, false, "Allow mounting filesystems")
	cmd.Flags().Bool(FlagAllowMountDevfs, false, "Allow mounting devfs")
	cmd.Flags().Bool(FlagAllowMountNullfs, false, "Allow mounting nullfs")
	cmd.Flags().Bool(FlagAllowMountTmpfs, false, "Allow mounting tmpfs")
	cmd.Flags().Bool(FlagAllowMountZFS, false, "Allow mounting ZFS datasets")
	cmd.Flags().Bool(FlagAllowVMM, false, "Allow vmm operations (bhyve in jail)")
	cmd.Flags().Bool(FlagAllowMlock, false, "Allow locking memory")
	cmd.Flags().Bool(FlagAllowReservedPorts, false, "Allow binding to reserved ports (<1024)")
}

// AddJailHookFlags adds jail lifecycle hook flags
func AddJailHookFlags(cmd *cobra.Command) {
	cmd.Flags().String(FlagExecPrestart, "", "Command to run before starting jail")
	cmd.Flags().String(FlagExecPoststart, "", "Command to run after starting jail")
	cmd.Flags().String(FlagExecPrestop, "", "Command to run before stopping jail")
	cmd.Flags().String(FlagExecPoststop, "", "Command to run after stopping jail")
	cmd.Flags().Bool(FlagExecClean, false, "Run commands in clean environment")
}

// AddJailBehaviorFlags adds jail behavior flags
func AddJailBehaviorFlags(cmd *cobra.Command) {
	cmd.Flags().Int(FlagDevfsRuleset, 4, "DevFS ruleset number")
	cmd.Flags().Bool(FlagPersist, false, "Keep jail running even if no processes")
	cmd.Flags().Int(FlagChildrenMax, 0, "Maximum number of child jails (0=unlimited)")
	cmd.Flags().Int(FlagSecurelevel, -1, "Securelevel of the jail (-1 to 3)")
}

// AddBaseCreateFlags adds the create flags every provider reads: image,
// cloud-init (providers that refuse it read it to say so), immediate start,
// and description.
func AddBaseCreateFlags(cmd *cobra.Command) {
	cmd.Flags().String(FlagImage, "", "Base image or template")
	cmd.Flags().String(FlagCloudInit, "", "Cloud-init configuration file")
	cmd.Flags().Bool(FlagStart, false, "Start the instance once it is created")
	cmd.Flags().String(FlagDescription, "", "Instance description")
}

// AddCommonCreateFlags adds the common create flags for jail and bhyve. The
// qemu create command reads only a subset and composes it from the smaller
// helpers instead, so an unwired flag is refused at parse time.
func AddCommonCreateFlags(cmd *cobra.Command) {
	AddResourceFlags(cmd)
	AddNetworkFlags(cmd)
	AddAdvancedStorageFlags(cmd)
	AddOSFlags(cmd)
	AddBootAutoStartFlags(cmd)
	AddBaseCreateFlags(cmd)

	cmd.Flags().String(FlagBootloader, "", "Boot method (uefi)")
	cmd.Flags().Bool(FlagPull, false, "Download a missing image instead of asking (required when not on a terminal)")
}
