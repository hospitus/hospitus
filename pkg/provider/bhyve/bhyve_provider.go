// Package bhyve implements the HOSPITUS provider for FreeBSD's bhyve hypervisor.
//
// A VM is a bhyve(8) process on the host, its disks are ZFS volumes, and its
// console is an nmdm(4) pair. The provider detects at Initialize what the host
// can do — hardware virtualization, UEFI firmware, a ZFS parent dataset — and
// refuses to start what it cannot support rather than failing later.
//
// See bhyve(8) and https://wiki.freebsd.org/bhyve.
package bhyve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/hospitus/hospitus/pkg/dataset"
	"github.com/hospitus/hospitus/pkg/firewall"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
	"github.com/hospitus/hospitus/pkg/storage"
)

type BhyveProvider struct {
	config    provider.ProviderConfig
	dataDir   string // Where we store VM data (disks, config)
	stateDir  string // Where we store runtime state (PIDs, logs)
	imageDir  string // Where downloaded images are stored
	zfsParent string // Parent ZFS dataset for VM storage (e.g., "zroot/hospitus/bhyve")

	// storageBackend builds the ZFS commands for VM disks. Tests replace it to
	// assert on what would be created without a pool.
	storageBackend storage.Manager

	// Hardware Virtualization Capabilities
	// bhyve requires either Intel VT-x or AMD-V to function.
	// We detect these during Initialize() via sysctl.
	hasVMX bool // Intel VT-x (hw.vmm.vmx.initialized=1)
	hasSVM bool // AMD-V (hw.vmm.svm.num_asids>0)

	// UEFI Firmware Support
	// Modern operating systems expect UEFI boot. We detect UEFI firmware
	// by checking common installation paths. If found, we default to UEFI;
	// otherwise we fall back to legacy BIOS boot.
	hasUEFI      bool   // Whether UEFI firmware was found
	uefiPath     string // Path to UEFI firmware file
	uefiVarsPath string // Path to source UEFI vars file (for per-VM copies)

	// Console Access
	// bhyve supports null modem devices (nmdm) for serial console access.
	// This is useful for headless VMs or debugging boot issues.
	hasNMDM bool // Whether 'cu' exists and the nmdm module is loadable

	// Software TPM Support
	// Windows 11 and some Linux VMs require TPM 2.0.
	// bhyve delegates TPM emulation to a running swtpm(8) process.
	hasSwtpm       bool   // Whether swtpm binary is available
	swtpmPath      string // Full path to swtpm binary
	swtpmSetupPath string // Full path to swtpm_setup binary (for initializing TPM state)

	// VirtIO Driver Support
	// bhyve always supports VirtIO paravirtualized drivers for better performance.
	// We set this to true unconditionally since it's a core bhyve feature.
	hasVirtIO bool // Always true for bhyve

	// Serialize operations that act on the same instance
	locks provider.InstanceLocks

	// Serialize instance creation to prevent TOCTOU race conditions
	createMu sync.Mutex

	// Serialize vm.state load/save pairs so the bhyve monitor goroutine's
	// state reconciliation cannot interleave with the running-state write.
	stateMu sync.Mutex

	// Firewall manager for NAT networking (lazy-initialized via firewallOnce)
	firewallMgr  *firewall.Manager
	firewallOnce sync.Once
	firewallErr  error

	// runner runs external commands; injectable so tests can use a fake.
	runner execx.Runner

	// sendReceive streams a ZFS snapshot into an existing volume. It is a field
	// rather than a plain method because it wires a pipe between two processes,
	// which execx does not model, so a test has no other way past it.
	sendReceive func(ctx context.Context, snapshot, dest string) error
}

// cmd returns the command runner, defaulting to the real os/exec backend when
// the provider was constructed without one (e.g. a bare struct literal in tests
// that does not inject a fake).
func (p *BhyveProvider) cmd() execx.Runner {
	if p.runner == nil {
		return execx.Default()
	}
	return p.runner
}

// vmConfig represents the persistent configuration for a bhyve VM.
//
// Design Decision: Simple key=value format
// We use a simple text-based format instead of JSON/YAML because:
//  1. Easy to parse without dependencies
//  2. Easy to edit manually for debugging
//  3. FreeBSD tradition (see rc.conf, loader.conf)
//  4. Robust to partial corruption (can fix manually)
//
// File format example:
//
//	name=my-vm
//	cpus=4
//	memory=8192
//	disks=/dev/zvol/zroot/hospitus/my-vm/disk0,/tmp/disk1.img
//	taps=tap_my-vm_0,tap_my-vm_1
//	console=/var/lib/hospitus/bhyve/my-vm/console
//	uefi=true
type vmConfig struct {
	Name        string   // VM name (must be unique, used for bhyve -vm parameter)
	CPUs        int      // Number of virtual CPUs
	MemoryMB    int64    // Memory in megabytes
	DiskPaths   []string // Paths to disk images or ZVOLs
	DiskDrivers []string // Disk driver for each disk (virtio-blk, ahci-hd, nvme, ahci-cd)
	BootOrder   []int    // Boot order for disks (indices into DiskPaths, first = primary boot)
	TapDevs     []string // tap(4) network devices
	Bridges     []string // Host bridge each tap belongs to ("" for NAT or no bridge)
	// NetTypes is the requested network type per tap. NATEnabled is VM-wide, so
	// it cannot say which NIC of a mixed VM is the NAT one; without this, a
	// bridge NIC with no bridge named was read back as NAT.
	NetTypes    []string
	NICDrivers  []string               // NIC driver per tap device (virtio-net, e1000). Default: virtio-net.
	NICMACs     []string               // Guest MAC per tap device, assigned by hospitus and recorded here.
	Console     string                 // Path to console device (nmdm or socket)
	UEFIBoot    bool                   // Whether to use UEFI boot (vs legacy BIOS)
	UEFIVars    string                 // Path to per-VM writable UEFI vars file (NVRAM)
	AutoStart   map[string]interface{} // Auto-start configuration (enabled, order)
	Passthrough []string               // PCI passthrough devices (e.g., "9/0/0")

	// VNC Configuration
	// bhyve supports framebuffer via fbuf device for graphical console access
	VNCEnabled bool // Whether VNC is enabled (adds fbuf device)
	VNCPort    int  // VNC port (default: 5900)
	VNCWidth   int  // Framebuffer width (default: 1024)
	VNCHeight  int  // Framebuffer height (default: 768)
	VNCWait    bool // Wait for VNC connection before booting
	VNCHost    string
	// VNCInsecure records the operator's opt-in to a non-loopback VNC bind.
	// Persisted because StartInstance and ImportInstance validate the policy
	// from vm.conf, where the spec that carried the opt-in is long gone.
	VNCInsecure bool // VNC bind address (default: 127.0.0.1)

	// TPM 2.0 support via swtpm
	// Required by Windows 11 and some Linux workloads.
	// bhyve delegates TPM emulation to a running swtpm process; the socket
	// path is stored here so StartInstance can wire up the -l tpm,swtpm,... argument.
	TPMEnabled  bool   // Whether a TPM 2.0 device is emulated
	TPMSockPath string // UNIX socket path swtpm listens on (derived from vmDir)

	// Entropy device
	// Provides a VirtIO random-number generator to the guest.
	// Improves boot-time entropy especially for Windows and server OSes.
	VirtioRNG bool

	PCISlots map[string]string // Map of device names to PCI slots

	// Resource limits
	ReadBPS   int64 // Read bytes per second
	WriteBPS  int64 // Write bytes per second
	ReadIOPS  int64 // Read IOPS
	WriteIOPS int64 // Write IOPS

	DiskSectors []int // Sector size for each disk

	// MSR handling
	// bhyve -w: silently ignore accesses to unimplemented Model Specific Registers.
	// Required for Windows guests on AMD hardware (SVM), where the Windows HAL
	// probes AMD-specific MSRs (e.g. performance counters) that bhyve's SVM backend
	// does not emulate; without -w, bhyve injects a #GP, causing PHASE0_EXCEPTION.
	MSRIgnoreUnimplemented bool

	// NAT networking
	// When true, this VM was created with type="nat" and PF NAT rules were added.
	// Stored so DeleteInstance knows to call teardownNATForVM.
	NATEnabled bool

	// IPv6 NAT networking
	// When true, PF nat6 rules are added for this VM so it can reach the internet via IPv6.
	IPv6Enabled bool
	// IPv6Prefix is the ULA /64 used for the NAT bridge (default: fd10:0:0:1::/64).
	IPv6Prefix string

	// USB tablet emulation
	// Adds an xhci,tablet device independently of VNC.  Useful for graphical desktops
	// that need proper pointer integration without enabling a VNC framebuffer.
	USBTablet bool

	// USB host device passthrough (stored as "bus.dev.func" strings).
	// These map to PCI passthrough of USB controllers.  See bhyve(8) -s passthru.
	USBDevices []string

	// VLAN IDs per tap device (0 = no tagging).
	// When non-zero, a vlan(4) sub-interface is created on top of the tap and
	// joined to a per-VLAN bridge (hospitus-vlan<id>), providing L2 isolation.
	VLANIDs []int

	// DiskDriver overrides the default virtio-blk driver for all data disks
	// when no per-disk driver is specified (DiskDrivers[i] == "").
	// Valid values: virtio-blk (default), virtio, ahci-hd, ahci, nvme.
	//
	// Not virtio-scsi: bhyve offers it, but getBhyveDiskDriver has no case for
	// it, so a VM asking for it would silently get the default instead.
	DiskDriver string
}

// vmState represents the runtime state of a VM.
//
// Why separate from vmConfig?
// Configuration (vmConfig) changes rarely and survives reboots.
// State (vmState) changes frequently and is ephemeral:
//   - PID is only valid while VM is running
//   - State transitions: stopped → starting → running → stopping → stopped
//
// We could use a single file, but separating them allows:
//   - Atomic state updates without touching config
//   - Different update frequencies
//   - Clearer separation of concerns
type vmState struct {
	Name    string                 // VM name (duplicated for safety)
	CPUs    int                    // CPUs (cached from config)
	Memory  int64                  // Memory (cached from config)
	State   provider.InstanceState // Current lifecycle state
	PID     int                    // Process ID (0 if not running)
	Console string                 // Console device path
}

// NewBhyveProvider creates a new bhyve provider instance
func NewBhyveProvider() *BhyveProvider {
	return &BhyveProvider{runner: execx.Default()}
}

// Metadata returns provider metadata
func (p *BhyveProvider) Metadata() provider.ProviderMetadata {
	return provider.ProviderMetadata{
		Name:        "bhyve",
		Version:     "1.0.0",
		Type:        provider.ProviderTypeVM,
		Description: "FreeBSD bhyve native hypervisor with hardware virtualization",
		Author:      "HOSPITUS Project",
		License:     "Apache-2.0",
		Homepage:    "https://github.com/hospitus/hospitus",
	}
}

// Capabilities returns provider capabilities
func (p *BhyveProvider) Capabilities() provider.ProviderCapabilities {
	caps := provider.ProviderCapabilities{
		SupportsSnapshots:     true,  // Via ZFS snapshots
		SupportsMigration:     false, // bhyve doesn't support live migration
		SupportsLiveMigration: false,
		SupportsCloning:       true, // Via ZFS clones
		SupportsPause:         true, // Via SIGSTOP/SIGCONT (checkpointing uses bhyvectl --suspend)
		SupportsConsole:       true, // Via nmdm serial console
		SupportsVNC:           true, // Via VNC framebuffer
		SupportsSerial:        true, // Serial console via nmdm
		SupportsCrossArch:     false,

		// Resource capabilities
		SupportsGPUPassthrough: true, // Via PCI passthrough
		SupportsUSBPassthrough: true, // Via USB controller passthrough
		SupportsPCIPassthrough: true, // Via bhyve passthru
		SupportsNUMA:           false,
		SupportsBallooning:     false,

		NetworkTypes: []provider.NetworkType{
			provider.NetworkTypeBridge,
			provider.NetworkTypeNAT,
		},
		MaxNetworkInterfaces: 8,

		DiskTypes: []provider.DiskType{
			provider.DiskTypeRaw,
			provider.DiskTypeZVOL,     // ZFS volume
			provider.DiskTypePhysical, // Physical device passthrough
		},
		MaxDisks:            26,
		SupportsHotplugDisk: false,

		SupportedArchitectures: []string{"amd64"},

		MaxCPUs:     16,
		MaxMemoryMB: 524288,

		PlatformFeatures: map[string]interface{}{
			"vmx":     p.hasVMX,
			"svm":     p.hasSVM,
			"uefi":    p.hasUEFI,
			"virtio":  p.hasVirtIO,
			"console": p.hasNMDM,
		},
	}

	return caps
}

// Initialize initializes the provider and detects runtime capabilities.
//
// Initialization Philosophy:
// We fail fast if critical requirements aren't met, but gracefully degrade
// for optional features. This allows the provider to register even if some
// features are unavailable, which helps with:
//   - Debugging (can see what's missing in Capabilities())
//   - Partial functionality (can still create VMs without UEFI)
//   - Better error messages (specific missing dependencies)
//
// Capability Detection Order:
//  1. Platform check (must be FreeBSD - hard requirement)
//  2. Directory creation (need somewhere to store data)
//  3. Hardware virtualization (required for bhyve to work)
//  4. UEFI firmware (optional, graceful degradation)
//  5. Console support (optional, graceful degradation)
//
// Why not lazy initialization?
// We could defer capability detection until first VM creation, but early
// detection allows:
//   - Fast failure at daemon startup
//   - Clear error messages before user tries to create VMs
//   - Capability information available via API immediately
func (p *BhyveProvider) Initialize(ctx context.Context, config provider.ProviderConfig) error {
	// Platform Check: bhyve only works on FreeBSD
	// Why check runtime.GOOS instead of build tags?
	//   - Build tags would prevent compilation on other platforms
	//   - runtime.GOOS allows cross-compilation and shows better errors
	//   - Users get "provider not available" vs "command not found"
	if runtime.GOOS != "freebsd" {
		return provider.ErrProviderNotAvailable
	}

	// Use dedicated subdirectory to avoid conflicts with other providers
	// This matches QEMU's pattern and prevents name collisions
	p.config = config
	p.dataDir = filepath.Join(config.DataDir, "bhyve")
	p.stateDir = filepath.Join(config.StateDir, "bhyve")

	// Image directory - shared across providers, not under bhyve/ subdir
	// Images are stored at the global /var/lib/hospitus/images level
	if imageDir, ok := config.Settings["image_dir"].(string); ok {
		p.imageDir = imageDir
	} else {
		p.imageDir = filepath.Join(config.DataDir, "images")
	}

	// Get ZFS parent dataset from settings or use default
	if zfsParent, ok := config.Settings["zfs_parent"].(string); ok {
		p.zfsParent = zfsParent
	} else {
		// Default derives from the configurable parent (HOSPITUS_ZFS_PARENT).
		p.zfsParent = dataset.Child("bhyve")
	}

	// Ensure ZFS parent dataset exists
	if err := p.ensureZFSDataset(ctx, p.zfsParent); err != nil {
		return fmt.Errorf("failed to ensure ZFS parent dataset: %w", err)
	}

	// Create directories with 0755 permissions
	// Why 0755? Allows owner (root/hospitus) full access, others can read/execute
	// This lets non-root users read VM configs but not modify them
	if err := os.MkdirAll(p.dataDir, 0o755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}
	if err := os.MkdirAll(p.stateDir, 0o755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Detect hardware virtualization support (Intel VT-x or AMD-V)
	// This is a hard requirement - bhyve won't work without it
	// On failure, we return an error (not ErrProviderNotAvailable) to give
	// users specific guidance on what's missing
	if err := p.detectVirtualizationSupport(ctx); err != nil {
		return fmt.Errorf("failed to detect virtualization support: %w", err)
	}

	// Detect UEFI firmware (optional feature)
	// If not found, we can still create VMs with legacy BIOS boot
	// This is graceful degradation - provider works, just with limitations
	p.detectUEFI()

	// Check for null modem device support (optional)
	// Used for serial console access. If 'cu' command doesn't exist,
	// console features won't work but VMs can still run
	p.hasNMDM = p.checkCommand("cu") && p.ensureNMDMModule(ctx)

	// VirtIO is always available on bhyve (it's built-in)
	// We set this unconditionally since all bhyve versions support it
	p.hasVirtIO = true

	// Detect swtpm for TPM 2.0 emulation (optional)
	// Required for Windows 11 and Linux VMs that need TPM.
	if path, err := exec.LookPath("swtpm"); err == nil {
		p.hasSwtpm = true
		p.swtpmPath = path
		slog.Info("swtpm found", "path", path)
	} else {
		slog.Info("swtpm not found — TPM-enabled VMs will fail to start (pkg install swtpm)")
	}
	if path, err := exec.LookPath("swtpm_setup"); err == nil {
		p.swtpmSetupPath = path
	}

	return nil
}

// Shutdown shuts down the provider
func (p *BhyveProvider) Shutdown(ctx context.Context) error {
	// Stop all running VMs
	handles, err := p.ListInstances(ctx, provider.InstanceFilter{})
	if err != nil {
		return fmt.Errorf("failed to list instances: %w", err)
	}

	for _, handle := range handles {
		state, err := p.GetInstanceState(ctx, handle)
		if err != nil {
			continue
		}

		if state == provider.StateRunning {
			if err := p.StopInstance(ctx, handle, provider.StopOptions{Force: true}); err != nil {
				slog.Warn("Failed to stop instance during cleanup",
					logging.FieldInstance, handle.ID,
					logging.FieldError, err)
			}
		}
	}

	return nil
}

// HealthCheck checks if the provider is healthy and available
func (p *BhyveProvider) HealthCheck(ctx context.Context) error {
	// Check if bhyve command exists
	if !p.checkCommand("bhyve") {
		return fmt.Errorf("bhyve command not found")
	}

	// Check if bhyvectl command exists
	if !p.checkCommand("bhyvectl") {
		return fmt.Errorf("bhyvectl command not found")
	}

	// Check if running on FreeBSD
	if runtime.GOOS != "freebsd" {
		return provider.ErrProviderNotAvailable
	}

	// Check for hardware virtualization support
	if !p.hasVMX && !p.hasSVM {
		return fmt.Errorf("no hardware virtualization support detected (need Intel VT-x or AMD-V)")
	}

	// Check if vmm module is loaded
	if err := p.cmd().Run(ctx, "kldstat", "-q", "-m", "vmm"); err != nil {
		return fmt.Errorf("vmm kernel module not loaded (run: kldload vmm)")
	}

	return nil
}

// detectVirtualizationSupport verifies the host can run bhyve VMs by checking
// that the vmm(4) kernel module is loaded and, as a fallback, that the CPU
// advertises hardware virtualization (Intel VT-x / AMD-V). It returns an error
// describing the missing prerequisite when the host is not capable.
func (p *BhyveProvider) detectVirtualizationSupport(ctx context.Context) error {
	// First check if vmm module is loaded - this is the most reliable indicator
	if err := p.cmd().Run(ctx, "kldstat", "-q", "-m", "vmm"); err != nil {
		return fmt.Errorf("vmm kernel module not loaded (run: kldload vmm)")
	}

	// Check for Intel VT-x support via VMX sysctls
	// FreeBSD exposes hw.vmm.vmx.* when Intel VT-x is available
	output, err := p.cmd().Output(ctx, "sysctl", "-n", "hw.vmm.vmx.initialized")
	if err == nil && strings.TrimSpace(string(output)) == "1" {
		p.hasVMX = true
		return nil
	}

	// Fallback: check if any vmx capability exists (FreeBSD 15+)
	if err := p.cmd().Run(ctx, "sysctl", "hw.vmm.vmx.cap.halt_exit"); err == nil {
		p.hasVMX = true
		return nil
	}

	// Check for AMD-V support via SVM sysctls
	// FreeBSD exposes hw.vmm.svm.* when AMD-V is available
	output, err = p.cmd().Output(ctx, "sysctl", "-n", "hw.vmm.svm.num_asids")
	if err == nil {
		asids := strings.TrimSpace(string(output))
		// num_asids > 0 indicates SVM is available and functional
		if asids != "" && asids != "0" {
			p.hasSVM = true
			return nil
		}
	}

	// Last resort: try to run bhyve -h to see if it works
	if err := p.cmd().Run(ctx, "bhyve", "-h"); err == nil {
		// bhyve command exists and runs, assume virtualization works
		p.hasVMX = true // Assume Intel if we can't detect specifically
		return nil
	}

	return fmt.Errorf("no hardware virtualization support detected (vmm loaded but no VT-x/AMD-V)")
}

func (p *BhyveProvider) detectUEFI() {
	// Common UEFI firmware locations on FreeBSD
	// Prefer CODE.fd + VARS.fd pair (separate files) over combined BHYVE_UEFI.fd
	// Combined files don't support per-VM writable NVRAM.
	uefiPairs := []struct {
		code string
		vars string
	}{
		{
			"/usr/local/share/uefi-firmware/BHYVE_UEFI_CODE.fd",
			"/usr/local/share/uefi-firmware/BHYVE_UEFI_VARS.fd",
		},
	}
	// Fallback to combined firmware files
	combinedPaths := []string{
		"/usr/local/share/uefi-firmware/BHYVE_UEFI.fd",
		"/usr/local/share/bhyve-firmware/BHYVE_UEFI.fd",
	}

	// Try pairs first (preferred for per-VM NVRAM)
	for _, pair := range uefiPairs {
		if _, err := os.Stat(pair.code); err == nil {
			p.hasUEFI = true
			p.uefiPath = pair.code
			if _, err := os.Stat(pair.vars); err == nil {
				p.uefiVarsPath = pair.vars
			}
			return
		}
	}

	// Fallback to combined firmware
	for _, path := range combinedPaths {
		if _, err := os.Stat(path); err == nil {
			p.hasUEFI = true
			p.uefiPath = path
			return
		}
	}
}

// copyUEFIVars creates a per-VM writable UEFI vars file from the template.
// UEFI firmware needs writable NVRAM to store boot entries discovered from disks.
func (p *BhyveProvider) copyUEFIVars(destPath string) (err error) {
	varsSrc := p.uefiVarsPath
	if varsSrc == "" {
		// Fallback: try BHYVE_UEFI.fd (combined code+vars) or skip
		return fmt.Errorf("no UEFI vars template available; install edk2-bhyve package")
	}
	src, err := os.Open(varsSrc)
	if err != nil {
		return fmt.Errorf("failed to open UEFI vars template %s: %w", varsSrc, err)
	}
	defer src.Close()

	dst, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create UEFI vars file %s: %w", destPath, err)
	}
	// Capture the Close error: buffered write failures on NVRAM surface here,
	// and a corrupt vars file would break UEFI boot entry persistence.
	defer func() {
		if cerr := dst.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("failed to close UEFI vars file %s: %w", destPath, cerr)
		}
	}()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("failed to copy UEFI vars: %w", err)
	}
	return nil
}

func (p *BhyveProvider) checkCommand(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// ensureNMDMModule ensures the nmdm kernel module is loaded.
// On FreeBSD, nmdm devices are created dynamically when accessed.
func (p *BhyveProvider) ensureNMDMModule(ctx context.Context) bool {
	// Check if module is already loaded
	if p.cmd().Run(ctx, "kldstat", "-q", "-n", "nmdm") == nil {
		return true
	}

	// Try to load the module
	if err := p.cmd().Run(ctx, "kldload", "nmdm"); err != nil {
		slog.Warn("Failed to load nmdm module", logging.FieldError, err)
		slog.Info("Console access will be unavailable")
		return false
	}

	return true
}

// ensureZFSDataset ensures a ZFS dataset exists, creating it if necessary
func (p *BhyveProvider) ensureZFSDataset(ctx context.Context, datasetName string) error {
	// Check if dataset already exists
	if err := p.cmd().Run(ctx, "zfs", "list", "-H", datasetName); err == nil {
		// Dataset exists
		return nil
	}

	// Create dataset with parent datasets if needed (-p flag)
	if err := p.cmd().Run(ctx, "zfs", "create", "-p", datasetName); err != nil {
		return fmt.Errorf("failed to create ZFS dataset %s: %w", datasetName, err)
	}
	return nil
}

// createZVOL creates the block device backing a VM disk.
//
// The dataset layout is built by the storage backend, which knows that a sized
// volume is a zvol and that parents have to exist first. Reusing an orphan left
// by a failed create stays here: it is a bhyve recovery decision, not something
// a storage backend should assume for every caller.
func (p *BhyveProvider) createZVOL(ctx context.Context, name string, sizeGB int64) error {
	if p.zvolExists(ctx, name) {
		return nil
	}

	backend, err := p.storage()
	if err != nil {
		return err
	}

	if _, err := backend.CreateVolume(ctx, name, storage.VolumeOptions{
		Size: fmt.Sprintf("%dG", sizeGB),
	}); err != nil {
		return fmt.Errorf("failed to create ZVOL %s: %w", name, err)
	}
	return nil
}

// storage returns the storage backend, building one on the VM parent dataset
// when the provider was constructed without going through Initialize.
//
// It reports an error rather than a nil Manager where ZFS does not exist: bhyve
// is FreeBSD-only and never registers elsewhere, but a nil backend reached by
// any other path would panic instead of explaining itself.
func (p *BhyveProvider) storage() (storage.Manager, error) {
	if p.storageBackend != nil {
		return p.storageBackend, nil
	}

	backend, err := storage.NewPlatformBackend()
	if err != nil {
		return nil, err
	}

	// A zvol name is already a full dataset path, so the parent only decides
	// where a bare name would land; pointing it at the VM parent keeps both
	// forms resolving to the same place.
	if err := backend.Initialize(context.Background(), storage.Config{
		Backend:          "zfs",
		ZFSParentDataset: p.zfsParent,
	}); err != nil {
		return nil, fmt.Errorf("failed to initialize storage backend: %w", err)
	}

	p.storageBackend = backend
	return backend, nil
}

// zvolExists reports whether a volume of that name is already present, which
// happens when an earlier create failed partway through.
func (p *BhyveProvider) zvolExists(ctx context.Context, name string) bool {
	return p.cmd().Run(ctx, "zfs", "list", "-t", "volume", "-H", name) == nil
}

func (p *BhyveProvider) createRawDisk(ctx context.Context, path string, sizeBytes int64) error {
	if err := p.cmd().Run(ctx, "truncate", "-s", strconv.FormatInt(sizeBytes, 10), path); err != nil {
		return fmt.Errorf("failed to create raw disk: %w", err)
	}
	return nil
}

// tapDeviceName builds the interface name for one of a VM's taps.
//
// FreeBSD caps an interface name at 15 characters. Truncating a long VM name to
// fit collided: two VMs sharing a nine-character prefix got the same tap, and
// creating or deleting either one took the other's interface with it. A digest
// of the full name keeps them distinct.
func tapDeviceName(vmName string, index int) string {
	name := fmt.Sprintf("tap_%s_%d", vmName, index)
	if len(name) <= 15 {
		return name
	}
	sum := sha256.Sum256([]byte(vmName))
	return fmt.Sprintf("tap%s_%d", hex.EncodeToString(sum[:])[:8], index)
}

func (p *BhyveProvider) createTapDevice(ctx context.Context, vmName string, index int) (string, error) {
	tapName := tapDeviceName(vmName, index)

	// Destroy stale tap device if it already exists (e.g. from a previous failed run).
	// Remove from bridge first — ifconfig destroy hangs if tap is a bridge member or
	// a bhyve process still holds the tap FD open. Discover the actual owning bridge
	// (a tap may live on hospitus0, hospitus-nat or a per-VLAN bridge) rather than
	// assuming hospitus0, otherwise the tap stays a member and destroy can hang.
	if p.cmd().Run(ctx, "ifconfig", tapName) == nil {
		if bridge := p.findBridgeForMember(ctx, tapName); bridge != "" {
			if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", bridge, "deletem", tapName); err != nil {
				slog.Warn("failed to remove stale tap from bridge",
					logging.FieldVM, vmName, "tap", tapName, "bridge", bridge,
					logging.FieldError, err, "output", strings.TrimSpace(string(out)))
			}
		}
		p.killOrphanBhyveProcess(ctx, vmName)
		if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", tapName, "destroy"); err != nil {
			slog.Warn("failed to destroy stale tap device",
				logging.FieldVM, vmName, "tap", tapName,
				logging.FieldError, err, "output", strings.TrimSpace(string(out)))
		}
	}

	// Create tap device
	if output, err := p.cmd().CombinedOutput(ctx, "ifconfig", "tap", "create", "name", tapName); err != nil {
		return "", fmt.Errorf("failed to create tap device %s: %w (output: %s)", tapName, err, string(output))
	}

	// Bring up the interface
	if err := p.cmd().Run(ctx, "ifconfig", tapName, "up"); err != nil {
		return "", fmt.Errorf("failed to bring up tap device: %w", err)
	}

	return tapName, nil
}

func (p *BhyveProvider) ensureBridge(ctx context.Context, bridge string) error {
	// Check if bridge already exists
	if err := p.cmd().Run(ctx, "ifconfig", bridge); err == nil {
		return nil
	}

	if output, err := p.cmd().CombinedOutput(ctx, "ifconfig", "bridge", "create", "name", bridge); err != nil {
		return fmt.Errorf("failed to create bridge %s: %w (output: %s)", bridge, err, output)
	}

	// Bring bridge up
	if err := p.cmd().Run(ctx, "ifconfig", bridge, "up"); err != nil {
		return fmt.Errorf("failed to bring up bridge %s: %w", bridge, err)
	}

	return nil
}

// findBridgeForMember returns the name of the bridge that currently has member
// as a member interface, or "" if none does. It enumerates bridges via
// "ifconfig -g bridge" and inspects each for a "member: <member>" line.
func (p *BhyveProvider) findBridgeForMember(ctx context.Context, member string) string {
	out, err := p.cmd().Output(ctx, "ifconfig", "-g", "bridge")
	if err != nil {
		return ""
	}
	for _, bridge := range strings.Fields(string(out)) {
		bridgeOut, err := p.cmd().Output(ctx, "ifconfig", bridge)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(bridgeOut), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "member:" && fields[1] == member {
				return bridge
			}
		}
	}
	return ""
}

func (p *BhyveProvider) attachToBridge(ctx context.Context, tapDev, bridge string) error {
	output, err := p.cmd().CombinedOutput(ctx, "ifconfig", bridge, "addm", tapDev)
	if err != nil {
		// Attaching what is already attached is the desired end state, not a
		// failure. Start re-applies NAT so a VM survives a reboot, and on a host
		// that never rebooted the tap is still a member.
		if strings.Contains(string(output), "already a member of this bridge") {
			return nil
		}
		return fmt.Errorf("failed to attach tap %s to bridge %s: %w (output: %s)", tapDev, bridge, err, output)
	}
	return nil
}

// lpcPCISlot is the fixed PCI slot reserved for the LPC device (required for
// UEFI). All other PCI devices must use a lower slot; bhyve buses expose slots
// 0-31, so no device may be assigned slot 31 or above.
const lpcPCISlot = 31

func (p *BhyveProvider) buildBhyveArgs(config *vmConfig) ([]string, error) {
	// The last gate before a configuration becomes a bhyve command line.
	// StartInstance validates earlier so it can refuse before starting a swtpm
	// and creating taps; RestoreCheckpoint did not validate at all, so a
	// hand-edited vm.conf could attach an arbitrary /dev device on restore.
	// Checking here means no path can reach bhyve without passing this.
	if err := p.validateRuntimeConfig(config); err != nil {
		return nil, err
	}

	args := []string{}

	// Memory
	args = append(args, "-m", fmt.Sprintf("%dM", config.MemoryMB))

	// CPUs with explicit topology.
	// Without a topology spec, bhyve defaults to N sockets × 1 core × 1 thread,
	// which presents N separate physical CPUs to the guest. Windows and Linux HALs
	// both handle a single-socket multi-core topology much more reliably, especially
	// during early kernel init where ACPI MADT parsing happens. Always force
	// sockets=1,cores=N,threads=1 so the guest sees one normal multi-core CPU.
	cpuArg := fmt.Sprintf("cpus=%d,sockets=1,cores=%d,threads=1", config.CPUs, config.CPUs)

	// Yield the vCPU thread on HLT — required for well-behaved guests (Windows,
	// modern Linux). Without -H bhyve will spin-wait and saturate a host CPU.
	args = append(args, "-c", cpuArg, "-H")

	// For UEFI-booted VMs, disable legacy MPTable generation.
	// UEFI guests use ACPI exclusively for MP/CPU enumeration; having both
	// MPTable and ACPI can cause early kernel crashes (e.g. PHASE0_EXCEPTION
	// 0x78 in Windows). CBSD always passes -Y for UEFI VMs.
	if config.UEFIBoot {
		args = append(args, "-Y")
	}

	// Silently ignore accesses to unimplemented Model Specific Registers.
	// Required for Windows guests on AMD hardware: the Windows HAL probes
	// AMD-specific MSRs (performance counters, etc.) that bhyve's SVM backend
	// does not implement. Without -w, bhyve injects a #GP fault, which causes
	// PHASE0_EXCEPTION (0x78) during early kernel init. Safe on Intel too.
	if config.MSRIgnoreUnimplemented {
		args = append(args, "-w")
	}

	// Wire guest memory if passthrough is used (PCI passthrough or USB device passthrough)
	if len(config.Passthrough) > 0 || len(config.USBDevices) > 0 {
		args = append(args, "-S")
	}

	// UEFI boot
	if config.UEFIBoot && p.hasUEFI {
		if config.UEFIVars != "" {
			args = append(args, "-l", fmt.Sprintf("bootrom,%s,%s", p.uefiPath, config.UEFIVars))
		} else {
			args = append(args, "-l", fmt.Sprintf("bootrom,%s", p.uefiPath))
		}
	}

	// TPM 2.0 via swtpm (LPC device — not PCI)
	// swtpm must already be running and listening on TPMSockPath before bhyve starts.
	if config.TPMEnabled && config.TPMSockPath != "" {
		args = append(args, "-l", fmt.Sprintf("tpm,swtpm,%s", config.TPMSockPath))
	}

	// Disks (VirtIO block devices, AHCI, NVMe, or VirtIO-SCSI)
	// DiskDriver sets the default for all disks when no per-disk driver is specified.
	defaultDiskDriver := "virtio-blk"
	if config.DiskDriver != "" {
		defaultDiskDriver = config.DiskDriver
	}
	for i, diskPath := range config.DiskPaths {
		driver := defaultDiskDriver
		if i < len(config.DiskDrivers) && config.DiskDrivers[i] != "" {
			driver = config.DiskDrivers[i]
		}
		slot := i + 4
		slotStr := fmt.Sprintf("%d:0", slot)
		diskOpts := fmt.Sprintf("%s,%s,%s", slotStr, driver, diskPath)

		// Append sector size if non-zero for this disk index.
		// bhyve virtio-blk and ahci-hd support sectorsize=N (default 512).
		if i < len(config.DiskSectors) && config.DiskSectors[i] != 0 {
			diskOpts += fmt.Sprintf(",sectorsize=%d", config.DiskSectors[i])
		}

		args = append(args, "-s", diskOpts)
		if config.PCISlots != nil {
			config.PCISlots[fmt.Sprintf("disk%d", i)] = slotStr
		}
	}

	// Network interfaces
	// Default driver is virtio-net; use e1000 for Windows guests (the Windows
	// installer and WinPE do not ship VirtIO drivers).
	for i, tapDev := range config.TapDevs {
		driver := "virtio-net"
		if i < len(config.NICDrivers) && config.NICDrivers[i] != "" {
			driver = config.NICDrivers[i]
		}
		slot := len(config.DiskPaths) + i + 4
		slotStr := fmt.Sprintf("%d:0", slot)
		device := fmt.Sprintf("%s,%s,%s", slotStr, driver, tapDev)
		// Give the NIC the address hospitus recorded. Left to bhyve, the address is
		// generated at start and cannot be read back: bhyve rewrites its own
		// process title to "bhyve: <name>", so the arguments are gone, and the
		// VM's address can then only be found in the DHCP leases — under
		// whatever hostname the guest happens to report.
		if i < len(config.NICMACs) && config.NICMACs[i] != "" {
			device += ",mac=" + config.NICMACs[i]
		}
		args = append(args, "-s", device)
		if config.PCISlots != nil {
			config.PCISlots[fmt.Sprintf("nic%d", i)] = slotStr
		}
	}

	for i, pciSlot := range config.Passthrough {
		slot := len(config.DiskPaths) + len(config.TapDevs) + 4 + i
		slotStr := fmt.Sprintf("%d:0", slot)
		args = append(args, "-s", fmt.Sprintf("%s,passthru,%s", slotStr, pciSlot))
		if config.PCISlots != nil {
			config.PCISlots[fmt.Sprintf("passthru%d", i)] = slotStr
		}
	}

	// USB device passthrough (PCI-level USB controller passthrough)
	// Stored as "bus.dev.func" or "bus/dev/func" strings, mapped to bhyve passthru.
	ptOffset := len(config.DiskPaths) + len(config.TapDevs) + len(config.Passthrough) + 4
	for i, usbDev := range config.USBDevices {
		slot := ptOffset + i
		slotStr := fmt.Sprintf("%d:0", slot)
		args = append(args, "-s", fmt.Sprintf("%s,passthru,%s", slotStr, usbDev))
		if config.PCISlots != nil {
			config.PCISlots[fmt.Sprintf("usb%d", i)] = slotStr
		}
	}

	// Next available slot index after all passthrough
	nextSlot := ptOffset + len(config.USBDevices)

	// VirtIO RNG — provides entropy to the guest; helpful for Windows and servers
	if config.VirtioRNG {
		slotStr := fmt.Sprintf("%d:0", nextSlot)
		args = append(args, "-s", fmt.Sprintf("%s,virtio-rnd", slotStr))
		nextSlot++
	}

	// USB tablet (standalone, without VNC)
	// Adds an xhci,tablet device for precise pointer integration on graphical desktops
	// that do not use VNC.  If VNC is also enabled the tablet is added below instead.
	if config.USBTablet && !config.VNCEnabled {
		args = append(args, "-s", fmt.Sprintf("%d:0,xhci,tablet", nextSlot))
		nextSlot++
	}

	// Console (null modem)
	if config.Console != "" && p.hasNMDM {
		args = append(args, "-l", fmt.Sprintf("com1,%s", config.Console))
	}

	// VNC framebuffer device
	if config.VNCEnabled {
		// Build fbuf options
		host := config.VNCHost
		if host == "" {
			host = "127.0.0.1"
		}
		port := config.VNCPort
		if port == 0 {
			port = 5900
		}
		width := config.VNCWidth
		if width == 0 {
			width = 1024
		}
		height := config.VNCHeight
		if height == 0 {
			height = 768
		}

		fbufOpts := fmt.Sprintf("fbuf,tcp=%s:%d,w=%d,h=%d", host, port, width, height)
		if config.VNCWait {
			fbufOpts += ",wait"
		}
		// xhci tablet for mouse integration (always added with VNC regardless of USBTablet flag)
		args = append(args, "-s", fmt.Sprintf("%d:0,%s", nextSlot, fbufOpts),
			"-s", fmt.Sprintf("%d:0,xhci,tablet", nextSlot+1))
		nextSlot += 2
	}

	// Guard against PCI slot exhaustion: slots are assigned sequentially from 4,
	// and slot 31 is reserved for the LPC device. nextSlot now points one past
	// the highest slot used, so the highest assigned slot is nextSlot-1.
	if highest := nextSlot - 1; highest >= lpcPCISlot {
		return nil, fmt.Errorf("too many PCI devices: slot %d reaches or exceeds the LPC slot %d (max %d attachable PCI devices)", highest, lpcPCISlot, lpcPCISlot-4)
	}

	// Host bridge (required)
	args = append(args, "-s", "0:0,hostbridge")

	// LPC device (required for UEFI)
	if config.UEFIBoot {
		args = append(args, "-s", fmt.Sprintf("%d,lpc", lpcPCISlot))
	}

	// VM name
	args = append(args, config.Name)

	return args, nil
}

// zfsDestroyCleanup attempts to destroy a ZFS dataset during cleanup and logs any errors.
// This is used for rollback operations where we don't want to mask the original error.
func (p *BhyveProvider) zfsDestroyCleanup(ctx context.Context, datasetName string) {
	if err := p.cmd().Run(ctx, "zfs", "destroy", datasetName); err != nil {
		slog.Warn("Failed to destroy ZFS dataset during cleanup",
			"dataset", datasetName,
			logging.FieldError, err)
	}
}

// validateVNCHost validates a VNC bind address for security.
// bhyve's fbuf device has no VNC password/authentication support,
// so binding to any address other than loopback requires explicit
// opt-in via the vnc_insecure flag.
func validateVNCHost(host string, vncInsecure bool) error {
	if host == "" || host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return nil
	}

	// Reject 0.0.0.0 and :: without explicit opt-in
	if (host == "0.0.0.0" || host == "::") && !vncInsecure {
		return fmt.Errorf("VNC bind to %s requires vnc_insecure=true — bhyve fbuf has no authentication and binding to all interfaces is dangerous", host)
	}

	// Reject any non-loopback address without explicit opt-in
	if !vncInsecure {
		return fmt.Errorf("VNC bind to non-localhost address %s requires vnc_insecure=true", host)
	}

	return nil
}

// checkConfigValues refuses values that would not survive the vm.conf format.
//
// The file is flat "key=value" lines with comma-separated lists, so a newline
// in any value writes an extra key that parseVMConfig reads back as real, and a
// comma inside a list element splits it into two. Walked by reflection rather
// than field by field: the struct has forty-odd fields, and a check that has to
// be remembered for each new one is a check that will be missed.
func checkConfigValues(config *vmConfig) error {
	v := reflect.ValueOf(*config)
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		name := t.Field(i).Name
		switch f := v.Field(i); f.Kind() {
		case reflect.String:
			if strings.ContainsAny(f.String(), "\r\n") {
				return fmt.Errorf("%s contains a line break, which vm.conf cannot represent", name)
			}
		case reflect.Slice:
			if f.Type().Elem().Kind() != reflect.String {
				continue
			}
			for j := 0; j < f.Len(); j++ {
				if strings.ContainsAny(f.Index(j).String(), "\r\n,") {
					return fmt.Errorf("%s[%d] contains a line break or a comma, which vm.conf cannot represent", name, j)
				}
			}
		case reflect.Map:
			iter := f.MapRange()
			for iter.Next() {
				for _, part := range []reflect.Value{iter.Key(), iter.Value()} {
					if part.Kind() == reflect.String && strings.ContainsAny(part.String(), "\r\n=") {
						return fmt.Errorf("%s contains a line break or an equals sign, which vm.conf cannot represent", name)
					}
				}
			}
		}
	}
	return nil
}

func (p *BhyveProvider) saveVMConfig(vmDir string, config *vmConfig) error {
	if err := checkConfigValues(config); err != nil {
		return fmt.Errorf("refusing to write vm.conf: %w", err)
	}

	// Convert boot order to string
	bootOrderStr := ""
	if len(config.BootOrder) > 0 {
		bootOrderParts := make([]string, len(config.BootOrder))
		for i, idx := range config.BootOrder {
			bootOrderParts[i] = strconv.Itoa(idx)
		}
		bootOrderStr = strings.Join(bootOrderParts, ",")
	}

	// Simple key=value format for config
	lines := []string{
		fmt.Sprintf("name=%s", config.Name),
		fmt.Sprintf("cpus=%d", config.CPUs),
		fmt.Sprintf("memory=%d", config.MemoryMB),
		fmt.Sprintf("disks=%s", strings.Join(config.DiskPaths, ",")),
		fmt.Sprintf("disk_drivers=%s", strings.Join(config.DiskDrivers, ",")),
		fmt.Sprintf("boot_order=%s", bootOrderStr),
		fmt.Sprintf("taps=%s", strings.Join(config.TapDevs, ",")),
		fmt.Sprintf("bridges=%s", strings.Join(config.Bridges, ",")),
		fmt.Sprintf("net_types=%s", strings.Join(config.NetTypes, ",")),
		fmt.Sprintf("nic_drivers=%s", strings.Join(config.NICDrivers, ",")),
		fmt.Sprintf("nic_macs=%s", strings.Join(config.NICMACs, ",")),
		fmt.Sprintf("console=%s", config.Console),
		fmt.Sprintf("uefi=%t", config.UEFIBoot),
		fmt.Sprintf("uefi_vars=%s", config.UEFIVars),
		fmt.Sprintf("passthrough=%s", strings.Join(config.Passthrough, ",")),
		// VNC settings
		fmt.Sprintf("vnc_enabled=%t", config.VNCEnabled),
		fmt.Sprintf("vnc_port=%d", config.VNCPort),
		fmt.Sprintf("vnc_width=%d", config.VNCWidth),
		fmt.Sprintf("vnc_height=%d", config.VNCHeight),
		fmt.Sprintf("vnc_wait=%t", config.VNCWait),
		fmt.Sprintf("vnc_host=%s", config.VNCHost),
		fmt.Sprintf("vnc_insecure=%t", config.VNCInsecure),
		// Resource limits
		fmt.Sprintf("read_bps=%d", config.ReadBPS),
		fmt.Sprintf("write_bps=%d", config.WriteBPS),
		fmt.Sprintf("read_iops=%d", config.ReadIOPS),
		fmt.Sprintf("write_iops=%d", config.WriteIOPS),
		// Hardware extras
		fmt.Sprintf("tpm_enabled=%t", config.TPMEnabled),
		fmt.Sprintf("tpm_sock_path=%s", config.TPMSockPath),
		fmt.Sprintf("virtio_rng=%t", config.VirtioRNG),
		fmt.Sprintf("ignore_msr=%t", config.MSRIgnoreUnimplemented),
		fmt.Sprintf("nat_enabled=%t", config.NATEnabled),
		// IPv6 NAT
		fmt.Sprintf("ipv6_enabled=%t", config.IPv6Enabled),
		fmt.Sprintf("ipv6_prefix=%s", config.IPv6Prefix),
		// USB features
		fmt.Sprintf("usb_tablet=%t", config.USBTablet),
		fmt.Sprintf("usb_devices=%s", strings.Join(config.USBDevices, ",")),
		// VLAN IDs per tap (comma-separated integers; 0 = no tagging)
		fmt.Sprintf("vlan_ids=%s", intSliceToString(config.VLANIDs)),
		// Default disk driver
		fmt.Sprintf("disk_driver=%s", config.DiskDriver),
	}

	// Persist PCISlots map
	for k, v := range config.PCISlots {
		lines = append(lines, fmt.Sprintf("pci_slot_%s=%s", k, v))
	}

	// Persist DiskSectors
	if len(config.DiskSectors) > 0 {
		sectors := make([]string, len(config.DiskSectors))
		for i, s := range config.DiskSectors {
			sectors[i] = strconv.Itoa(s)
		}
		lines = append(lines, fmt.Sprintf("disk_sectors=%s", strings.Join(sectors, ",")))
	}

	// Write through a temporary file: a truncated vm.conf leaves the VM
	// unloadable, since its disks, taps and passthrough devices all live here.
	configPath := filepath.Join(vmDir, "vm.conf")
	tmp, err := os.CreateTemp(vmDir, "vm.conf.*")
	if err != nil {
		return fmt.Errorf("failed to create the temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Removing a file that was renamed into place is expected to fail.
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.WriteString(strings.Join(lines, "\n")); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write the VM config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to set config permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close the VM config: %w", err)
	}
	if err := os.Rename(tmpName, configPath); err != nil {
		return fmt.Errorf("failed to install the VM config: %w", err)
	}
	return nil
}

func (p *BhyveProvider) loadVMConfig(vmDir string) (*vmConfig, error) {
	configPath := filepath.Join(vmDir, "vm.conf")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	config := parseVMConfig(data)

	if err := p.validateVMConfigPaths(config, vmDir); err != nil {
		return nil, err
	}
	return config, nil
}

// parseVMConfig parses a bhyve vm.conf (key=value lines) into a vmConfig.
// It is pure (no file or system access) so it can be unit-tested directly.
func parseVMConfig(data []byte) *vmConfig {
	config := &vmConfig{}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]
		value := parts[1]

		switch key {
		case "name":
			config.Name = value
		case "cpus":
			if cpus, err := strconv.Atoi(value); err == nil {
				config.CPUs = cpus
			} else {
				slog.Warn("Invalid cpus value, defaulting to 1", "value", value)
				config.CPUs = 1
			}
		case "memory":
			if mem, err := strconv.ParseInt(value, 10, 64); err == nil {
				config.MemoryMB = mem
			} else {
				slog.Warn("Invalid memory value, defaulting to 512", "value", value)
				config.MemoryMB = 512
			}
		case "disks":
			if value != "" {
				config.DiskPaths = strings.Split(value, ",")
			}
		case "disk_drivers":
			if value != "" {
				config.DiskDrivers = strings.Split(value, ",")
			}
		case "boot_order":
			if value != "" {
				parts := strings.Split(value, ",")
				config.BootOrder = make([]int, len(parts))
				for i, p := range parts {
					if order, err := strconv.Atoi(p); err == nil {
						config.BootOrder[i] = order
					} else {
						slog.Warn("Invalid boot_order value, defaulting to 0", "value", p, "index", i)
						config.BootOrder[i] = 0
					}
				}
			}
		case "taps":
			if value != "" {
				config.TapDevs = strings.Split(value, ",")
			}
		case "bridges":
			if value != "" {
				config.Bridges = strings.Split(value, ",")
			}
		case "net_types":
			if value != "" {
				config.NetTypes = strings.Split(value, ",")
			}
		case "nic_macs":
			if value != "" {
				config.NICMACs = strings.Split(value, ",")
			}
		case "nic_drivers":
			if value != "" {
				drivers := strings.Split(value, ",")
				// SECURITY: Filter to known-safe NIC drivers only
				for i, d := range drivers {
					switch d {
					case "virtio-net", "e1000":
						// valid
					default:
						slog.Warn("Ignoring unsupported NIC driver in config, using virtio-net",
							"driver", d, "vm", config.Name)
						drivers[i] = ""
					}
				}
				config.NICDrivers = drivers
			}
		case "console":
			config.Console = value
		case "uefi":
			config.UEFIBoot = value == "true"
		case "uefi_vars":
			config.UEFIVars = value
		case "passthrough":
			if value != "" {
				config.Passthrough = strings.Split(value, ",")
			}
		// VNC settings
		case "vnc_enabled":
			config.VNCEnabled = value == "true"
		case "vnc_port":
			if port, err := strconv.Atoi(value); err == nil {
				config.VNCPort = port
			}
		case "vnc_width":
			if width, err := strconv.Atoi(value); err == nil {
				config.VNCWidth = width
			}
		case "vnc_height":
			if height, err := strconv.Atoi(value); err == nil {
				config.VNCHeight = height
			}
		case "vnc_wait":
			config.VNCWait = value == "true"
		case "vnc_host":
			config.VNCHost = value
		case "vnc_insecure":
			config.VNCInsecure = value == "true"
		case "read_bps":
			if v, err := strconv.ParseInt(value, 10, 64); err == nil {
				config.ReadBPS = v
			} else {
				slog.Debug("bhyve config: invalid read_bps value", "value", value, logging.FieldError, err)
			}
		case "write_bps":
			if v, err := strconv.ParseInt(value, 10, 64); err == nil {
				config.WriteBPS = v
			} else {
				slog.Debug("bhyve config: invalid write_bps value", "value", value, logging.FieldError, err)
			}
		case "read_iops":
			if v, err := strconv.ParseInt(value, 10, 64); err == nil {
				config.ReadIOPS = v
			} else {
				slog.Debug("bhyve config: invalid read_iops value", "value", value, logging.FieldError, err)
			}
		case "write_iops":
			if v, err := strconv.ParseInt(value, 10, 64); err == nil {
				config.WriteIOPS = v
			} else {
				slog.Debug("bhyve config: invalid write_iops value", "value", value, logging.FieldError, err)
			}
		case "tpm_enabled":
			config.TPMEnabled = value == "true"
		case "tpm_sock_path":
			config.TPMSockPath = value
		case "virtio_rng":
			config.VirtioRNG = value == "true"
		case "ignore_msr":
			config.MSRIgnoreUnimplemented = value == "true"
		case "nat_enabled":
			config.NATEnabled = value == "true"
		case "ipv6_enabled":
			config.IPv6Enabled = value == "true"
		case "ipv6_prefix":
			config.IPv6Prefix = value
		case "usb_tablet":
			config.USBTablet = value == "true"
		case "usb_devices":
			if value != "" {
				config.USBDevices = strings.Split(value, ",")
			}
		case "vlan_ids":
			if value != "" {
				config.VLANIDs = stringToIntSlice(value)
			}
		case "disk_driver":
			config.DiskDriver = value
		case "disk_sectors":
			if value != "" {
				parts := strings.Split(value, ",")
				config.DiskSectors = make([]int, len(parts))
				for i, p := range parts {
					sectors, err := strconv.Atoi(p)
					if err != nil {
						slog.Warn("invalid disk_sectors entry, defaulting to 0", "value", p, logging.FieldError, err)
						continue
					}
					config.DiskSectors[i] = sectors
				}
			}
		default:
			if strings.HasPrefix(key, "pci_slot_") {
				if config.PCISlots == nil {
					config.PCISlots = make(map[string]string)
				}
				config.PCISlots[strings.TrimPrefix(key, "pci_slot_")] = value
			}
		}
	}

	return config
}

// validateVMConfigPaths rejects disk, tap, and console paths from a loaded
// config that fall outside the allowed locations (path-traversal hardening).
//
// Writable disk images are restricted to:
//   - /dev/zvol/... (ZFS volumes)
//   - vmDir/...     (image files, symlinks)
//   - /dev/<device> (physical passthrough, validated as block device at creation)
//
// Read-only CD-ROM (ahci-cd) entries may be anywhere on the filesystem since
// they are never written to; we only reject paths containing ".." traversal.
func (p *BhyveProvider) validateVMConfigPaths(config *vmConfig, vmDir string) error {
	for i, disk := range config.DiskPaths {
		disk = strings.TrimSpace(disk)
		if disk == "" {
			continue
		}
		// Determine if this slot is a CD-ROM (read-only ISO).
		isCDROM := i < len(config.DiskDrivers) && config.DiskDrivers[i] == "ahci-cd"
		if isCDROM {
			// Only reject obvious traversal attacks; ISOs can live anywhere.
			if strings.Contains(disk, "..") {
				slog.Warn("cdrom path contains traversal, rejecting", "vmDir", vmDir, "disk", disk)
				return fmt.Errorf("disk path outside allowed locations: %s", disk)
			}
			continue
		}
		isZVOL := strings.HasPrefix(disk, "/dev/zvol/")
		isVMDir := strings.HasPrefix(disk, vmDir+string(filepath.Separator))
		isPhysical := strings.HasPrefix(disk, "/dev/") && !strings.Contains(disk, "..")
		if !isZVOL && !isVMDir && !isPhysical {
			slog.Warn("disk path outside allowed locations, rejecting", "vmDir", vmDir, "disk", disk)
			return fmt.Errorf("disk path outside allowed locations: %s", disk)
		}
	}
	// Tap devices should not contain path separators
	for _, tap := range config.TapDevs {
		if strings.Contains(tap, "/") || strings.Contains(tap, "..") {
			slog.Warn("invalid tap device name, rejecting", "vmDir", vmDir, "tap", tap)
			return fmt.Errorf("invalid tap device name: %s", tap)
		}
	}
	// Bridge names reach ifconfig the same way tap names do.
	for _, bridge := range config.Bridges {
		if strings.Contains(bridge, "/") || strings.Contains(bridge, "..") {
			slog.Warn("invalid bridge name, rejecting", "vmDir", vmDir, "bridge", bridge)
			return fmt.Errorf("invalid bridge name: %s", bridge)
		}
	}
	// Console path must be under the state directory or /dev/nmdm. Guard against
	// an empty stateDir: strings.HasPrefix(x, "") is always true, which would
	// silently accept any console path, so the stateDir allowance only applies
	// when stateDir is actually configured.
	if config.Console != "" {
		allowed := strings.HasPrefix(config.Console, "/dev/nmdm") ||
			(p.stateDir != "" && strings.HasPrefix(config.Console, p.stateDir))
		if !allowed {
			slog.Warn("console path outside state directory, rejecting", "vmDir", vmDir, "console", config.Console)
			return fmt.Errorf("console path outside allowed locations: %s", config.Console)
		}
	}

	return nil
}

// intSliceToString converts a slice of ints to a comma-separated string.
func intSliceToString(ints []int) string {
	if len(ints) == 0 {
		return ""
	}
	parts := make([]string, len(ints))
	for i, v := range ints {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

// stringToIntSlice converts a comma-separated string to a slice of ints.
func stringToIntSlice(s string) []int {
	parts := strings.Split(s, ",")
	result := make([]int, 0, len(parts))
	for _, p := range parts {
		if v, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			result = append(result, v)
		}
	}
	return result
}

func (p *BhyveProvider) saveVMState(vmDir string, state *vmState) error {
	lines := []string{
		fmt.Sprintf("name=%s", state.Name),
		fmt.Sprintf("cpus=%d", state.CPUs),
		fmt.Sprintf("memory=%d", state.Memory),
		fmt.Sprintf("state=%s", state.State),
		fmt.Sprintf("pid=%d", state.PID),
		fmt.Sprintf("console=%s", state.Console),
	}

	statePath := filepath.Join(vmDir, "vm.state")
	return os.WriteFile(statePath, []byte(strings.Join(lines, "\n")), 0o600)
}

func (p *BhyveProvider) loadVMState(vmDir string) (*vmState, error) {
	statePath := filepath.Join(vmDir, "vm.state")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read state: %w", err)
	}

	state := &vmState{}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]
		value := parts[1]

		switch key {
		case "name":
			state.Name = value
		case "cpus":
			if cpus, err := strconv.Atoi(value); err == nil {
				state.CPUs = cpus
			} else {
				slog.Warn("Invalid cpus value in state, defaulting to 1", "value", value)
				state.CPUs = 1
			}
		case "memory":
			if mem, err := strconv.ParseInt(value, 10, 64); err == nil {
				state.Memory = mem
			} else {
				slog.Warn("Invalid memory value in state, defaulting to 512", "value", value)
				state.Memory = 512
			}
		case "state":
			state.State = provider.InstanceState(value)
		case "pid":
			if pid, err := strconv.Atoi(value); err == nil {
				state.PID = pid
			} else {
				slog.Warn("Invalid pid value in state, defaulting to 0", "value", value)
				state.PID = 0
			}
		case "console":
			state.Console = value
		}
	}

	return state, nil
}

type DiskDriver string

const (
	DiskDriverVirtioBlk DiskDriver = "virtio-blk" // VirtIO block device (default, good performance)
	DiskDriverAHCIHD    DiskDriver = "ahci-hd"    // AHCI hard disk (better compatibility)
	DiskDriverNVMe      DiskDriver = "nvme"       // NVMe (best performance)
)

// getBhyveDiskDriver returns the bhyve disk driver string for a disk spec.
//
// Disk driver selection affects performance and compatibility:
//   - virtio-blk: Good performance, requires virtio drivers in guest
//   - ahci-hd: Better compatibility (AHCI is standard), slightly slower
//   - nvme: Best performance, requires NVMe drivers (FreeBSD 12+, Linux 4.4+)
//
// IMPORTANT: UEFI firmware (OVMF) has NO virtio-blk driver. The boot disk (disk0)
// MUST use ahci-hd or nvme when UEFI boot is enabled, otherwise the VM will fail to
// boot with "no bootable disk found". virtio-blk is fine for data disks (disk1+) since
// the guest kernel's virtio driver loads after UEFI hands off to the OS.
//
// The driver can be specified in spec.ProviderConfig["disk_driver"] or defaults based
// on boot mode (ahci-hd for UEFI disk0, virtio-blk otherwise).
func (p *BhyveProvider) getBhyveDiskDriver(spec provider.InstanceSpec, diskIndex int) DiskDriver {
	// Check provider config for disk driver override (applies to all disks)
	if driverStr, ok := spec.ProviderConfig["disk_driver"].(string); ok {
		switch driverStr {
		case "ahci-hd", "ahci":
			return DiskDriverAHCIHD
		case "nvme":
			return DiskDriverNVMe
		case "virtio-blk", "virtio":
			// Honor the override, but say what it costs: the VM will start,
			// stay "running" in the UEFI shell, and never boot. Nothing else
			// reports that, which makes it an expensive mistake to find.
			if diskIndex == 0 && bootsWithUEFI(spec) {
				slog.Warn("boot disk uses virtio-blk under UEFI, which OVMF cannot read; "+
					"the VM will start and fail to find a bootable disk",
					logging.FieldVM, spec.Name, "remedy", "set disk_driver to ahci-hd or nvme")
			}
			return DiskDriverVirtioBlk
		}
	}

	// Check for per-disk driver configuration
	driverKey := fmt.Sprintf("disk_%d_driver", diskIndex)
	if driverStr, ok := spec.ProviderConfig[driverKey].(string); ok {
		switch driverStr {
		case "ahci-hd", "ahci":
			return DiskDriverAHCIHD
		case "nvme":
			return DiskDriverNVMe
		case "virtio-blk", "virtio":
			return DiskDriverVirtioBlk
		}
	}

	// Auto-detect: physical disks default to ahci-hd for Windows/BIOS compatibility
	if diskIndex < len(spec.Disks) && spec.Disks[diskIndex].Type == provider.DiskTypePhysical {
		return DiskDriverAHCIHD
	}

	// UEFI boot: disk0 (boot disk) must use ahci-hd — OVMF has no virtio-blk driver.
	// Data disks (disk1+) can use virtio-blk since the guest kernel handles them.
	if diskIndex == 0 && bootsWithUEFI(spec) {
		return DiskDriverAHCIHD
	}

	// Default to virtio-blk for best balance of performance and compatibility
	return DiskDriverVirtioBlk
}

// bootsWithUEFI reports whether the VM boots through OVMF rather than a
// bootloader that reads the disk itself.
func bootsWithUEFI(spec provider.InstanceSpec) bool {
	bootloader, ok := spec.ProviderConfig["bootloader"].(string)
	return ok && bootloader == "uefi"
}
