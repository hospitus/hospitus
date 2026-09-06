// Package vfkit runs virtual machines on macOS through vfkit, a command-line
// front end to Apple's Virtualization.framework.
//
// Why a separate binary rather than the framework directly: a VM created
// through an in-process binding lives and dies with the process that created
// it, so restarting hospitusd would kill every VM. vfkit runs each VM in its own
// process with a pidfile and a REST control socket, which is the same shape the
// jail, bhyve, QEMU and Podman providers already have — an instance outlives the
// daemon that started it.
//
// Guests run at native speed only on their own architecture, since
// Virtualization.framework executes guest instructions on the host CPU. On
// Apple silicon that means arm64 guests; there is no emulation fallback here,
// unlike QEMU.
package vfkit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// Compile-time assertion: VFKitProvider implements the Provider interface.
var _ provider.Provider = (*VFKitProvider)(nil)

// VFKitProvider implements the Provider interface using vfkit.
type VFKitProvider struct {
	mu sync.RWMutex

	config   provider.ProviderConfig
	dataDir  string // VM directories: disks, EFI variable store, logs
	stateDir string // Runtime state: pidfiles, control sockets, saved configs
	imageDir string // Where downloaded images are stored

	vfkitBin   string
	qemuImg    string // Used to convert qcow2 images; the framework needs raw
	logger     *slog.Logger
	runner     execx.Runner
	restClient *restClient
}

// NewVFKitProvider creates a vfkit provider.
func NewVFKitProvider() *VFKitProvider {
	return &VFKitProvider{
		runner:     execx.Default(),
		restClient: newRESTClient(),
	}
}

// cmd returns the command runner, defaulting to the real os/exec backend when
// the provider was built as a bare struct literal (tests).
func (p *VFKitProvider) cmd() execx.Runner {
	if p.runner == nil {
		return execx.Default()
	}
	return p.runner
}

// rest returns the control-socket client.
func (p *VFKitProvider) rest() *restClient {
	// NewVFKitProvider sets this; the fallback exists for a zero-value provider,
	// which tests build. Returning a fresh client rather than storing one keeps
	// concurrent callers off an unsynchronized write to the field.
	if p.restClient == nil {
		return newRESTClient()
	}
	return p.restClient
}

// Metadata returns information about the provider.
func (p *VFKitProvider) Metadata() provider.ProviderMetadata {
	return provider.ProviderMetadata{
		Name:        "vfkit",
		Version:     "1.0.0",
		Type:        provider.ProviderTypeVM,
		Author:      "HOSPITUS Project",
		Description: "macOS virtual machines through Apple's Virtualization.framework",
		License:     "Apache-2.0",
	}
}

// Capabilities declares what this provider supports.
//
// The list is deliberately short. Virtualization.framework offers no snapshots
// and no migration, and vfkit exposes no disk hotplug. vfkit's REST API can
// pause a VM, but this provider does not implement PauseProvider, so pause is
// not advertised. Claiming otherwise would make the API accept operations that
// cannot work.
func (p *VFKitProvider) Capabilities() provider.ProviderCapabilities {
	return provider.ProviderCapabilities{
		SupportsPause:   false, // no PauseProvider implementation
		SupportsConsole: false, // no ConsoleProvider; the serial log is a file
		SupportsSerial:  true,

		// One of each is what this provider implements: vmConfig holds a single
		// DiskPath, buildArgs emits one block device and one network device,
		// and the hotplug methods are unsupported. Advertising eight made a
		// scheduler place work here that would come back with one.
		NetworkTypes:         []provider.NetworkType{provider.NetworkTypeNAT},
		MaxNetworkInterfaces: 1,

		DiskTypes: []provider.DiskType{provider.DiskTypeRaw},
		MaxDisks:  1,

		// The framework runs guest instructions on the host CPU, so the guest
		// architecture is the host's and nothing else.
		SupportedArchitectures: []string{runtime.GOARCH},
		SupportsCrossArch:      false,

		MaxCPUs:     16,
		MaxMemoryMB: 65536,

		PlatformFeatures: map[string]interface{}{
			"virtualization_framework": true,
			"rosetta":                  false,
		},
	}
}

// Initialize prepares the provider: locate vfkit, create the directories.
func (p *VFKitProvider) Initialize(ctx context.Context, config provider.ProviderConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if config.Logger != nil {
		p.logger = config.Logger.With(logging.FieldProvider, "vfkit")
	} else if p.logger == nil {
		p.logger = logging.WithProvider("vfkit")
	}

	if runtime.GOOS != "darwin" {
		return fmt.Errorf("vfkit requires macOS: Virtualization.framework does not exist on %s", runtime.GOOS)
	}

	p.config = config
	p.dataDir = filepath.Join(config.DataDir, "vfkit")
	p.stateDir = filepath.Join(config.StateDir, "vfkit")
	p.imageDir = filepath.Join(config.DataDir, "images")

	bin, err := lookPath("vfkit")
	if err != nil {
		return fmt.Errorf("vfkit not found: %w (install it with: brew install vfkit)", err)
	}
	p.vfkitBin = bin

	// qemu-img converts a downloaded qcow2 image into the raw disk the
	// framework requires. Its absence is not fatal: a raw image needs no
	// conversion, so the failure belongs to the create that needs it.
	if img, imgErr := lookPath("qemu-img"); imgErr == nil {
		p.qemuImg = img
	}

	for _, dir := range []string{p.dataDir, p.stateDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	p.logger.Info("vfkit provider initialized", "binary", p.vfkitBin, "data_dir", p.dataDir)
	return nil
}

// Shutdown releases provider resources. Running VMs are left alone: they are
// separate processes and outliving the daemon is the point.
func (p *VFKitProvider) Shutdown(_ context.Context) error {
	return nil
}

// HealthCheck reports whether the provider can run a VM here.
func (p *VFKitProvider) HealthCheck(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("vfkit requires macOS")
	}
	if p.vfkitBin == "" {
		return fmt.Errorf("vfkit binary not found")
	}
	if err := p.cmd().Run(ctx, p.vfkitBin, "--version"); err != nil {
		return fmt.Errorf("vfkit is not runnable: %w", err)
	}
	return nil
}

// vmConfig is what a VM needs to be started again after a daemon restart.
type vmConfig struct {
	Name       string   `json:"name"`
	CPUs       int      `json:"cpus"`
	MemoryMB   int64    `json:"memory_mb"`
	DiskPath   string   `json:"disk_path"`
	EFIStore   string   `json:"efi_store"`
	MACAddress string   `json:"mac_address"`
	CloudInit  []string `json:"cloud_init,omitempty"`
	Created    string   `json:"created"`
}

func (p *VFKitProvider) vmDir(name string) string { return filepath.Join(p.dataDir, name) }
func (p *VFKitProvider) configPath(name string) string {
	return filepath.Join(p.stateDir, name+".json")
}

func (p *VFKitProvider) pidPath(name string) string {
	return filepath.Join(p.stateDir, name+".pid")
}

// restSocket returns the control socket for a VM.
//
// It lives in the state directory rather than the VM directory to keep the path
// short: a Unix socket path is capped at 104 bytes, and a deep data directory
// would otherwise make every VM fail to start.
func (p *VFKitProvider) restSocket(name string) string {
	return filepath.Join(p.stateDir, name+".sock")
}

func (p *VFKitProvider) saveConfig(config *vmConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode VM config: %w", err)
	}
	return os.WriteFile(p.configPath(config.Name), data, 0o600)
}

func (p *VFKitProvider) loadConfig(name string) (*vmConfig, error) {
	data, err := os.ReadFile(p.configPath(name))
	if err != nil {
		return nil, err
	}

	var config vmConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to decode VM config for %s: %w", name, err)
	}
	return &config, nil
}

func (p *VFKitProvider) logWarn(ctx context.Context, msg string, args ...any) {
	logger := p.logger
	if logger == nil {
		logger = logging.FromContext(ctx)
	}
	if logger != nil {
		logger.Warn(msg, args...)
	}
}
