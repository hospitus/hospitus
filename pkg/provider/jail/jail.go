// Package jail implements the HOSPITUS provider for FreeBSD jails.
//
// A jail is a ZFS dataset cloned from a base skeleton and started with jail(8);
// jexec(8) runs commands inside it. The provider covers native FreeBSD jails and
// Linux jails under the linuxulator, including foreign architectures through
// binmiscctl(8) and qemu-user-static.
package jail

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/hospitus/hospitus/pkg/config"
	"github.com/hospitus/hospitus/pkg/dataset"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
	"github.com/hospitus/hospitus/pkg/storage"
	"github.com/hospitus/hospitus/pkg/validation"
)

var (
	_ provider.InstanceHealthCheckProvider = (*JailProvider)(nil)
	// The capability flags above claim these; the compiler is what keeps the
	// claim honest.
	_ provider.SnapshotProvider = (*JailProvider)(nil)
	_ provider.CloneProvider    = (*JailProvider)(nil)
	_ provider.ConsoleProvider  = (*JailProvider)(nil)
)

// JailProvider implements the Provider interface for FreeBSD jails
type JailProvider struct {
	config         provider.ProviderConfig
	dataDir        string
	stateDir       string
	zfsParent      string          // Parent ZFS dataset for jails
	hospitusConfig *config.Config  // Hospitus configuration for networking
	networkManager *NetworkManager // Network manager for jail networking

	// storageBackend builds the ZFS commands behind volumes. Tests replace it to
	// assert on what would be created without a pool.
	storageBackend storage.Manager
	logger         *slog.Logger
	runner         execx.Runner
	locks          provider.InstanceLocks // Serializes operations on one instance
	createMu       sync.Mutex             // Serializes jail creation to prevent TOCTOU races
	dhcpMu         sync.Mutex             // Serializes DHCP IP allocation to prevent duplicate leases
}

// NewJailProvider creates a new jail provider
func NewJailProvider() *JailProvider {
	return &JailProvider{
		logger: logging.WithProvider("jail"),
		runner: execx.Default(),
	}
}

// cmd returns the command runner, defaulting to the real os/exec backend when
// the provider was constructed without one (e.g. a bare struct literal in tests
// that does not inject a fake).
func (p *JailProvider) cmd() execx.Runner {
	if p.runner == nil {
		return execx.Default()
	}
	return p.runner
}

// Metadata returns provider metadata
func (p *JailProvider) Metadata() provider.ProviderMetadata {
	return provider.ProviderMetadata{
		Name:          "jail",
		Version:       "1.0.0",
		Type:          provider.ProviderTypeContainer,
		Author:        "HOSPITUS Team",
		Description:   "FreeBSD jail container support with ZFS integration",
		Homepage:      "https://github.com/hospitus/hospitus",
		License:       "Apache-2.0",
		MinAPIVersion: "1.0.0",
		MaxAPIVersion: "1.0.0",
	}
}

// Capabilities returns what features this provider supports
func (p *JailProvider) Capabilities() provider.ProviderCapabilities {
	return provider.ProviderCapabilities{
		// A flag follows the interface: a handler reaches a capability by type
		// assertion, so a flag set without the interface behind it makes the
		// provider answer "supported" and then refuse. The assertions below
		// keep the two in step.
		SupportsSnapshots:     true, // SnapshotProvider, via ZFS
		SupportsMigration:     false,
		SupportsLiveMigration: false,
		SupportsCloning:       true, // CloneProvider, via ZFS
		SupportsPause:         false,
		SupportsConsole:       true, // ConsoleProvider, via jexec
		SupportsVNC:           false,
		SupportsSerial:        false,

		SupportsGPUPassthrough: false,
		SupportsUSBPassthrough: false,
		SupportsPCIPassthrough: false,
		SupportsNUMA:           false,
		SupportsBallooning:     false,

		NetworkTypes: []provider.NetworkType{
			provider.NetworkTypeBridge,
			provider.NetworkTypeVXLAN,
		},
		MaxNetworkInterfaces: 16,

		DiskTypes: []provider.DiskType{
			provider.DiskTypeZVOL,
		},
		MaxDisks:            8,
		SupportsHotplugDisk: false,

		SupportedArchitectures: []string{"amd64", "arm64", "riscv64", "i386", "armv7"},
		SupportsCrossArch:      true,

		MaxCPUs:     256,
		MaxMemoryMB: 1024 * 1024, // 1TB

		PlatformFeatures: map[string]interface{}{
			"vnet":         true, // Virtual network stack
			"hierarchical": true, // Nested jails
			"rctl":         true, // Resource limits
		},
	}
}

// Initialize initializes the jail provider
func (p *JailProvider) Initialize(ctx context.Context, providerConfig provider.ProviderConfig) error {
	p.config = providerConfig
	p.dataDir = filepath.Join(providerConfig.DataDir, "jails")
	p.stateDir = filepath.Join(providerConfig.StateDir, "jails")
	if providerConfig.Logger != nil {
		p.logger = providerConfig.Logger.With(logging.FieldProvider, "jail")
	} else if p.logger == nil {
		p.logger = logging.WithProvider("jail")
	}

	// Get ZFS parent dataset from settings or use default
	if zfsParent, ok := providerConfig.Settings["zfs_parent"].(string); ok {
		p.zfsParent = zfsParent
	} else {
		// Default derives from the configurable parent (HOSPITUS_ZFS_PARENT).
		p.zfsParent = dataset.Child("jails")
	}

	// Load Hospitus configuration for networking
	hospitusConfig, err := config.LoadConfigWithDefaults()
	if err != nil {
		return fmt.Errorf("failed to load hospitus configuration: %w", err)
	}
	p.hospitusConfig = hospitusConfig

	// Create network manager
	p.networkManager = NewNetworkManager(p.hospitusConfig, p.logger)

	// Create directories
	if err := os.MkdirAll(p.dataDir, 0o755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}
	if err := os.MkdirAll(p.stateDir, 0o755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Ensure ZFS parent dataset exists
	if err := p.ensureZFSDataset(ctx, p.zfsParent); err != nil {
		return fmt.Errorf("failed to ensure ZFS parent dataset: %w", err)
	}

	return nil
}

// Shutdown shuts down the jail provider
func (p *JailProvider) Shutdown(ctx context.Context) error {
	// Nothing to clean up for jail provider
	return nil
}

// HealthCheck checks if the provider is available and functional
func (p *JailProvider) HealthCheck(ctx context.Context) error {
	// Check if running on FreeBSD
	if runtime.GOOS != "freebsd" {
		return provider.ErrProviderNotAvailable
	}

	// Check if jail command exists
	if _, err := exec.LookPath("jail"); err != nil {
		return fmt.Errorf("jail command not found: %w", err)
	}

	// Check if jls command exists
	if _, err := exec.LookPath("jls"); err != nil {
		return fmt.Errorf("jls command not found: %w", err)
	}

	// Check if jexec command exists
	if _, err := exec.LookPath("jexec"); err != nil {
		return fmt.Errorf("jexec command not found: %w", err)
	}

	// Check if ZFS is available
	if _, err := exec.LookPath("zfs"); err != nil {
		return fmt.Errorf("zfs command not found: %w", err)
	}

	// Check if kernel has jail support
	if err := p.cmd().Run(ctx, "sysctl", "-n", "security.jail.jailed"); err != nil {
		return fmt.Errorf("kernel jail support not available: %w", err)
	}

	return nil
}

// AttachDisk attaches a disk to a jail (mounts a ZFS dataset or directory)
func (p *JailProvider) AttachDisk(ctx context.Context, handle provider.InstanceHandle, disk provider.DiskAttachment) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", handle.ID))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load jail config: %w", err)
	}

	// TrimPrefix removes only a leading "/", so "../../etc" survives it and
	// filepath.Join resolves the result outside the jail. Everything below then
	// happens there, as root: the directory is created, a nullfs mount is laid
	// over it, and the escaped path is written into the jail's fstab — where
	// DetachDisk reads it back and unmounts it just as happily.
	targetPath := filepath.Join(jailConfig.Path, strings.TrimPrefix(disk.MountPoint, "/"))
	within, err := validation.PathWithin(targetPath, jailConfig.Path)
	if err != nil {
		return fmt.Errorf("cannot resolve mount point %q: %w", disk.MountPoint, err)
	}
	if !within {
		return fmt.Errorf("mount point %q resolves to %s, outside the jail root %s",
			disk.MountPoint, targetPath, jailConfig.Path)
	}

	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		return fmt.Errorf("failed to create mount point: %w", err)
	}

	running, err := p.isJailRunning(ctx, handle.ID)
	if err != nil {
		return err
	}

	if running {
		mountOpts := "rw"
		if disk.Disk.ReadOnly {
			mountOpts = "ro"
		}

		if output, err := p.cmd().CombinedOutput(ctx, "mount", "-t", "nullfs", "-o", mountOpts, disk.Disk.Path, targetPath); err != nil {
			return fmt.Errorf("failed to mount disk: %w (output: %s)", err, string(output))
		}
	}

	fstabPath := filepath.Join(p.stateDir, "fstab", handle.ID)
	if err := p.addToJailFstab(fstabPath, disk.Disk.Path, targetPath, disk.Disk.ReadOnly); err != nil {
		return fmt.Errorf("failed to add to fstab: %w", err)
	}

	return nil
}

// DetachDisk detaches a disk from a jail
func (p *JailProvider) DetachDisk(ctx context.Context, handle provider.InstanceHandle, diskID string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", handle.ID))
	jailConfig, err := p.loadJailConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load jail config: %w", err)
	}

	fstabPath := filepath.Join(p.stateDir, "fstab", handle.ID)
	content, err := os.ReadFile(fstabPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var mountPoint string
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, diskID+"\t") {
			parts := strings.Split(line, "\t")
			if len(parts) >= 2 {
				mountPoint = parts[1]
			}
			break
		}
	}

	if mountPoint == "" {
		return fmt.Errorf("disk %s not found in jail fstab", diskID)
	}

	// The fstab records the target as the host sees it (jail root already
	// joined); only an entry written as a jail-relative path needs joining.
	targetPath := mountPoint
	if !strings.HasPrefix(mountPoint, jailConfig.Path+"/") && mountPoint != jailConfig.Path {
		targetPath = filepath.Join(jailConfig.Path, strings.TrimPrefix(mountPoint, "/"))
	}

	running, err := p.isJailRunning(ctx, handle.ID)
	if err != nil {
		return err
	}

	if running {
		if output, err := p.cmd().CombinedOutput(ctx, "umount", "-f", targetPath); err != nil {
			return fmt.Errorf("failed to unmount disk: %w (output: %s)", err, string(output))
		}
	}

	if err := p.removeFromJailFstab(fstabPath, diskID); err != nil {
		return fmt.Errorf("failed to remove from fstab: %w", err)
	}

	return nil
}

// AttachNetwork attaches a network interface to a jail
func (p *JailProvider) AttachNetwork(ctx context.Context, handle provider.InstanceHandle, network provider.NetworkAttachment) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	iface := NetworkInterface{
		Bridge:      network.Network.Bridge,
		IPv4Address: network.Network.IPv4,
		IPv6Address: network.Network.IPv6,
		MAC:         network.Network.MAC,
		MTU:         network.Network.MTU,
	}
	_, err := p.AddNetworkInterface(ctx, handle, iface)
	return err
}

// DetachNetwork detaches a network interface from a jail
func (p *JailProvider) DetachNetwork(ctx context.Context, handle provider.InstanceHandle, interfaceID string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	return p.RemoveNetworkInterface(ctx, handle, interfaceID)
}

// ResizeConsole updates the terminal dimensions for a jail session.
func (p *JailProvider) ResizeConsole(ctx context.Context, handle provider.InstanceHandle, width, height int) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	return nil
}

func (p *JailProvider) getLogger(ctx context.Context) *slog.Logger {
	if logger := provider.LoggerFromContext(ctx); logger != nil {
		return logger.With(logging.FieldProvider, "jail")
	}
	if p.logger != nil {
		return p.logger
	}
	return logging.WithProvider("jail")
}

func (p *JailProvider) logInfo(ctx context.Context, msg string, args ...any) {
	p.getLogger(ctx).Info(msg, args...)
}

func (p *JailProvider) logWarn(ctx context.Context, msg string, args ...any) {
	p.getLogger(ctx).Warn(msg, args...)
}

func (p *JailProvider) logError(ctx context.Context, msg string, args ...any) {
	p.getLogger(ctx).Error(msg, args...)
}

func (p *JailProvider) logDebug(ctx context.Context, msg string, args ...any) {
	p.getLogger(ctx).Debug(msg, args...)
}
