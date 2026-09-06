// Package qemu implements the QEMU provider for HOSPITUS.
// QEMU is a cross-platform hypervisor that works on FreeBSD, Linux, macOS, and Windows.
// It provides hardware emulation and virtualization with:
// - KVM acceleration on Linux
// - NVMM acceleration on FreeBSD/NetBSD
// - HVF (Hypervisor.framework) acceleration on macOS
// - Software emulation as fallback on all platforms
package qemu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
	"github.com/hospitus/hospitus/pkg/validation"
)

var validQEMUMachines = map[string]bool{
	"q35": true, "pc": true, "pc-q35-8.1": true, "virt": true,
}

var validQEMUCPUs = map[string]bool{
	"qemu64": true, "qemu32": true, "host": true, "max": true,
	"cortex-a72": true, "cortex-a57": true, "cortex-a53": true,
	"a64fx": true, "neoverse-n1": true,
	"rv64": true, "sifive-u54": true,
}

// QEMUProvider implements the Provider interface for QEMU/KVM
type QEMUProvider struct {
	config       provider.ProviderConfig
	dataDir      string            // Base data dir with /qemu subdirectory
	stateDir     string            // Base state dir with /qemu subdirectory
	imageDir     string            // Directory where cloud images are stored
	hasKVM       bool              // KVM available (Linux only)
	hasHVF       bool              // HVF (Hypervisor.framework) available (macOS only)
	hasNVMM      bool              // NVMM available (FreeBSD/NetBSD)
	qemuBinaries map[string]string // arch -> binary path
	uefiFirmware map[string]string // arch -> UEFI firmware path
	portMu       sync.Mutex        // Guards findAvailablePort to prevent TOCTOU races
	hostPortMu   sync.Mutex        // Serializes host-port allocation with the config save that records it
	createMu     sync.Mutex        // Serializes VM creation to prevent TOCTOU races
	logger       *slog.Logger
	runner       execx.Runner
}

// NewQEMUProvider creates a new QEMU provider
func NewQEMUProvider() *QEMUProvider {
	return &QEMUProvider{
		qemuBinaries: make(map[string]string),
		uefiFirmware: make(map[string]string),
		logger:       logging.WithProvider("qemu"),
		runner:       execx.Default(),
	}
}

// cmd returns the command runner, defaulting to the real os/exec backend when
// the provider was constructed without one (e.g. a bare struct literal in tests
// that does not inject a fake).
func (p *QEMUProvider) cmd() execx.Runner {
	if p.runner == nil {
		return execx.Default()
	}
	return p.runner
}

// Metadata returns provider metadata
func (p *QEMUProvider) Metadata() provider.ProviderMetadata {
	return provider.ProviderMetadata{
		Name:          "qemu",
		Version:       "1.0.0",
		Type:          provider.ProviderTypeVM,
		Author:        "HOSPITUS Team",
		Description:   "QEMU universal hypervisor with KVM/HVF acceleration support",
		Homepage:      "https://github.com/hospitus/hospitus",
		License:       "Apache-2.0",
		MinAPIVersion: "1.0.0",
		MaxAPIVersion: "1.0.0",
	}
}

// Capabilities returns what features this provider supports
func (p *QEMUProvider) Capabilities() provider.ProviderCapabilities {
	return provider.ProviderCapabilities{
		SupportsSnapshots:     true,  // Offline, via qemu-img snapshot; the VM must be stopped
		SupportsMigration:     false, // Not implemented
		SupportsLiveMigration: false, // Not implemented
		SupportsCloning:       true,  // Via qemu-img create/convert
		SupportsPause:         true,  // Via SIGSTOP/SIGCONT on the QEMU process
		SupportsConsole:       true,  // Via serial Unix socket
		SupportsVNC:           true,
		SupportsSerial:        true,

		SupportsGPUPassthrough: p.hasKVM || p.hasNVMM, // Requires hardware accel
		SupportsUSBPassthrough: true,
		SupportsPCIPassthrough: p.hasKVM || p.hasNVMM, // Requires hardware accel
		SupportsNUMA:           true,
		SupportsBallooning:     true, // Memory ballooning

		NetworkTypes: []provider.NetworkType{
			provider.NetworkTypeBridge,
			provider.NetworkTypeNAT,
		},
		MaxNetworkInterfaces: 8,

		DiskTypes: []provider.DiskType{
			provider.DiskTypeRaw,
			provider.DiskTypeQCOW2,
			provider.DiskTypeVHD,
			provider.DiskTypeVMDK,
		},
		MaxDisks:            26, // a-z
		SupportsHotplugDisk: true,

		SupportedArchitectures: []string{"amd64", "arm64", "i386", "riscv64"},
		SupportsCrossArch:      true, // Emulation

		MaxCPUs:     288,
		MaxMemoryMB: 4 * 1024 * 1024, // 4TB

		PlatformFeatures: map[string]interface{}{
			"kvm":       p.hasKVM,
			"hvf":       p.hasHVF,
			"nvmm":      p.hasNVMM,
			"emulation": true,
			"qmp":       true, // QEMU Machine Protocol
		},
	}
}

// Initialize initializes the QEMU provider
func (p *QEMUProvider) Initialize(ctx context.Context, config provider.ProviderConfig) error {
	if config.Logger != nil {
		p.logger = config.Logger.With(logging.FieldProvider, "qemu")
	} else if p.logger == nil {
		p.logger = logging.WithProvider("qemu")
	}
	p.config = config
	// Use qemu subdirectory to avoid conflicts with other providers
	p.dataDir = filepath.Join(config.DataDir, "qemu")
	p.stateDir = filepath.Join(config.StateDir, "qemu")
	// Image directory: prefer <data-dir>/images, falling back to the default
	// /var/lib/hospitus/images when the former does not exist.
	imageLocations := []string{
		filepath.Join(config.DataDir, "images"),
		"/var/lib/hospitus/images",
	}
	for _, loc := range imageLocations {
		if _, err := os.Stat(loc); err == nil {
			p.imageDir = loc
			break
		}
	}
	if p.imageDir == "" {
		p.imageDir = filepath.Join(config.DataDir, "images")
	}

	// Create directories
	if err := os.MkdirAll(p.dataDir, 0o755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}
	if err := os.MkdirAll(p.stateDir, 0o755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Detect hardware acceleration availability
	switch runtime.GOOS {
	case "linux":
		// KVM on Linux
		_, err := os.Stat("/dev/kvm")
		p.hasKVM = err == nil
	case "freebsd":
		// NVMM on FreeBSD — hardware-accelerated virtualization for QEMU
		// (ported from NetBSD). Check if the nvmm kernel module is loaded.
		cmd := exec.CommandContext(ctx, "kldstat", "-q", "-m", "nvmm")
		p.hasNVMM = cmd.Run() == nil
	case "darwin":
		// HVF (Hypervisor.framework) on macOS
		// Check if sysctl returns success for HVF capability
		cmd := exec.Command("sysctl", "-n", "kern.hv_support")
		output, err := cmd.Output()
		if err == nil && strings.TrimSpace(string(output)) == "1" {
			p.hasHVF = true
		}
	}

	// Find QEMU binaries for different architectures
	p.qemuBinaries = map[string]string{
		"amd64":   p.findBinary("qemu-system-x86_64"),
		"arm64":   p.findBinary("qemu-system-aarch64"),
		"i386":    p.findBinary("qemu-system-i386"),
		"riscv64": p.findBinary("qemu-system-riscv64"),
	}

	// Find UEFI firmware for different architectures
	p.uefiFirmware = p.detectUEFIFirmware()

	return nil
}

// detectUEFIFirmware finds UEFI firmware files for different architectures
func (p *QEMUProvider) detectUEFIFirmware() map[string]string {
	firmware := make(map[string]string)

	// Common UEFI firmware locations across platforms
	// Order: macOS Homebrew ARM, macOS Homebrew Intel, FreeBSD, Linux
	candidates := map[string][]string{
		"amd64": {
			// macOS Homebrew (Apple Silicon)
			"/opt/homebrew/share/qemu/edk2-x86_64-code.fd",
			// macOS Homebrew (Intel)
			"/usr/local/share/qemu/edk2-x86_64-code.fd",
			// FreeBSD
			"/usr/local/share/qemu/edk2-x86_64-code.fd",
			// Linux (various distributions)
			"/usr/share/OVMF/OVMF_CODE.fd",
			"/usr/share/edk2/ovmf/OVMF_CODE.fd",
			"/usr/share/qemu/OVMF.fd",
		},
		"arm64": {
			// macOS Homebrew (Apple Silicon)
			"/opt/homebrew/share/qemu/edk2-aarch64-code.fd",
			// macOS Homebrew (Intel)
			"/usr/local/share/qemu/edk2-aarch64-code.fd",
			// FreeBSD
			"/usr/local/share/qemu/edk2-aarch64-code.fd",
			// Linux
			"/usr/share/AAVMF/AAVMF_CODE.fd",
			"/usr/share/edk2/aarch64/QEMU_EFI.fd",
			"/usr/share/qemu/edk2-aarch64-code.fd",
		},
		"i386": {
			"/opt/homebrew/share/qemu/edk2-i386-code.fd",
			"/usr/local/share/qemu/edk2-i386-code.fd",
		},
		"riscv64": {
			"/opt/homebrew/share/qemu/edk2-riscv-code.fd",
			"/usr/local/share/qemu/edk2-riscv-code.fd",
		},
	}

	for arch, paths := range candidates {
		for _, path := range paths {
			if _, err := os.Stat(path); err == nil {
				firmware[arch] = path
				break
			}
		}
	}

	return firmware
}

// Shutdown shuts down the QEMU provider
func (p *QEMUProvider) Shutdown(ctx context.Context) error {
	// Nothing to clean up for QEMU provider
	return nil
}

// HealthCheck checks if the provider is available and functional
func (p *QEMUProvider) HealthCheck(ctx context.Context) error {
	// Check if at least one QEMU binary is available
	found := false
	for _, binary := range p.qemuBinaries {
		if binary != "" {
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("no QEMU binaries found")
	}

	// Check if qemu-img is available (for disk management)
	if _, err := exec.LookPath("qemu-img"); err != nil {
		return fmt.Errorf("qemu-img not found: %w", err)
	}

	return nil
}

// defaultMachineType returns the QEMU machine the given architecture boots on.
// x86 guests use q35; every other target QEMU emulates here uses the generic
// "virt" board, which is what UEFI firmware and virtio devices expect.
func defaultMachineType(arch string) string {
	switch arch {
	case "amd64", "i386":
		return "q35"
	default:
		return "virt"
	}
}

// emulatedCPUModel returns a CPU model the given architecture implements under
// TCG. "qemu64" is an x86 model and is meaningless to qemu-system-aarch64, so a
// single default cannot serve every target.
func emulatedCPUModel(arch string) string {
	switch arch {
	case "arm64":
		return "cortex-a72"
	case "riscv64":
		return "rv64"
	default:
		return "qemu64"
	}
}

// vmConfig represents QEMU VM configuration
type vmConfig struct {
	Name      string                `json:"name"`
	QEMUBin   string                `json:"qemu_bin"`
	Args      []string              `json:"args"`
	QMPSocket string                `json:"qmp_socket"`
	Spec      provider.InstanceSpec `json:"spec"`
}

// buildQEMUConfig builds QEMU command line configuration
func (p *QEMUProvider) buildQEMUConfig(spec provider.InstanceSpec, qemuBin, diskPath, cloudInitISO, vmDir, arch string) *vmConfig {
	config := &vmConfig{
		Name:    spec.Name,
		QEMUBin: qemuBin,
		Args:    []string{},
		Spec:    spec,
	}

	// Ensure ProviderConfig map is initialized for storing runtime info
	if config.Spec.ProviderConfig == nil {
		config.Spec.ProviderConfig = make(map[string]interface{})
	}

	// Basic VM configuration
	config.Args = append(config.Args,
		"-name", spec.Name,
		"-m", fmt.Sprintf("%d", spec.MemoryMB),
		"-smp", fmt.Sprintf("%d", spec.CPUs))

	// Machine type. The default depends on the guest architecture: q35 is an x86
	// machine and qemu-system-aarch64 does not implement it, so defaulting to it
	// unconditionally made every arm64 guest fail to start.
	machineType := defaultMachineType(arch)
	if m, ok := spec.ProviderConfig["machine"].(string); ok && m != "" {
		if validQEMUMachines[m] {
			machineType = m
		} else {
			p.logger.Warn("unknown QEMU machine type, using the architecture default instead",
				"machine", m, "arch", arch, "using", machineType)
		}
	}
	config.Args = append(config.Args, "-machine", machineType)

	// CPU and acceleration configuration.
	//
	// KVM, NVMM and HVF all run guest instructions on the host CPU, so none of
	// them can accelerate a guest of a different architecture. Requesting one
	// anyway makes QEMU refuse to start — which is what an amd64 cloud image did
	// on an Apple Silicon host, since every accelerator check only looked at the
	// host. Emulation is the correct fallback there, slow but working.
	switch {
	case arch == runtime.GOARCH && p.hasKVM:
		// Linux: KVM acceleration
		config.Args = append(config.Args, "-enable-kvm", "-cpu", "host")
	case arch == runtime.GOARCH && p.hasNVMM:
		// FreeBSD/NetBSD: NVMM hardware acceleration
		config.Args = append(config.Args, "-accel", "nvmm", "-cpu", "host")
	case arch == runtime.GOARCH && p.hasHVF:
		// macOS: HVF (Hypervisor.framework) acceleration
		config.Args = append(config.Args, "-accel", "hvf", "-cpu", "host")
	default:
		// Emulation. An explicit CPU model wins; otherwise pick one the target
		// architecture actually implements.
		cpuModel := emulatedCPUModel(arch)
		if cpu, ok := spec.ProviderConfig["cpu"].(string); ok && cpu != "" {
			if validQEMUCPUs[cpu] {
				cpuModel = cpu
			} else {
				p.logger.Warn("unknown QEMU CPU model, using the architecture default instead",
					"cpu", cpu, "arch", arch, "using", cpuModel)
			}
		}
		if arch != runtime.GOARCH {
			p.logger.Info("guest architecture differs from the host; running under emulation",
				"guest_arch", arch, "host_arch", runtime.GOARCH, "cpu", cpuModel)
		}
		config.Args = append(config.Args, "-accel", "tcg", "-cpu", cpuModel)
	}

	// UEFI firmware for cloud images (most modern images require UEFI)
	bootMode := "uefi"
	if bm, ok := spec.ProviderConfig["boot_mode"].(string); ok && bm != "" {
		bootMode = bm
	}
	if bootMode != "uefi" && bootMode != "bios" {
		p.logger.Warn("unknown boot mode, falling back to uefi", "boot_mode", bootMode)
		bootMode = "uefi"
	}
	if bootMode == "uefi" {
		if firmware, ok := p.uefiFirmware[arch]; ok {
			// UEFI code (read-only)
			config.Args = append(config.Args, "-drive",
				fmt.Sprintf("if=pflash,format=raw,readonly=on,file=%s", firmware))
			// UEFI vars (per-VM copy for persistence)
			varsPath := filepath.Join(vmDir, "efivars.fd")
			if _, err := os.Stat(varsPath); os.IsNotExist(err) {
				// Create UEFI vars file from template if it doesn't exist
				if verr := p.createUEFIVarsFile(arch, varsPath); verr != nil {
					p.logger.Warn("failed to create UEFI vars file", "arch", arch, "path", varsPath, "error", verr)
				}
			}
			if _, err := os.Stat(varsPath); err == nil {
				config.Args = append(config.Args, "-drive",
					fmt.Sprintf("if=pflash,format=raw,file=%s", varsPath))
			}
		}
	}

	// Add disk with virtio for best performance. Physical device passthrough
	// (a /dev node) and .raw/.img files use the raw format; everything else
	// defaults to qcow2.
	if diskPath != "" {
		diskFormat := "qcow2"
		if strings.HasSuffix(diskPath, ".raw") || strings.HasSuffix(diskPath, ".img") || strings.HasPrefix(diskPath, "/dev/") {
			diskFormat = "raw"
		}
		config.Args = append(config.Args, "-drive",
			fmt.Sprintf("file=%s,format=%s,if=virtio,cache=writeback,index=0", diskPath, diskFormat))
	}

	// Add cloud-init ISO as CD-ROM if present
	if cloudInitISO != "" {
		config.Args = append(config.Args, "-drive",
			fmt.Sprintf("file=%s,format=raw,media=cdrom,readonly=on", cloudInitISO))
	}

	// Add installation ISO if present (for fresh installs)
	if installISO, ok := spec.ProviderConfig["install_iso"].(string); ok && installISO != "" {
		// Validate path: must exist and reside within allowed directories
		cleanPath := filepath.Clean(installISO)
		if _, err := os.Stat(cleanPath); err != nil {
			p.logger.Warn("install_iso not found, skipping", "path", cleanPath, logging.FieldError, err)
		} else if !p.isPathAllowed(cleanPath) {
			p.logger.Warn("install_iso path not allowed, skipping", "path", cleanPath)
		} else {
			// Boot from CD-ROM for installation
			config.Args = append(config.Args, "-drive",
				fmt.Sprintf("file=%s,format=raw,media=cdrom,readonly=on", cleanPath),
				"-boot", "d")
		}
	}

	// Add networks - support multiple NICs
	if len(spec.Networks) > 0 {
		for i := range spec.Networks {
			network := &spec.Networks[i]
			netID := fmt.Sprintf("net%d", i)
			netdevArgs := p.buildNetdevArgs(netID, *network, spec.ProviderConfig)
			config.Args = append(config.Args, "-netdev", netdevArgs)

			// Add NIC device with MAC address if specified
			deviceArgs := fmt.Sprintf("virtio-net-pci,netdev=%s", netID)
			if network.MAC != "" {
				deviceArgs += fmt.Sprintf(",mac=%s", network.MAC)
			}
			config.Args = append(config.Args, "-device", deviceArgs)
		}
	} else {
		// Default NAT network with SSH port forwarding
		sshPort := p.sshPortFor(spec.Name)
		config.Args = append(config.Args,
			"-netdev", fmt.Sprintf("user,id=net0,hostfwd=tcp::%d-:22", sshPort),
			"-device", "virtio-net-pci,netdev=net0")
		// Store the SSH port for user reference
		config.Spec.ProviderConfig["ssh_port"] = sshPort
	}

	// Serial console via Unix socket for interactive access
	serialSocket := filepath.Join(vmDir, "serial.sock")
	config.Args = append(config.Args, "-serial", fmt.Sprintf("unix:%s,server=on,wait=off", serialSocket))

	// Guest agent channel.
	//
	// ExecCommand and InstanceAddresses ask QEMU for guest-exec and
	// guest-network-get-interfaces over QMP, and QEMU relays both to the agent
	// through a virtio-serial port named org.qemu.guest_agent.0. Without the
	// port every such call fails whatever the guest runs, so the channel is
	// always offered; a guest with no agent installed simply leaves it unused.
	qgaSocket := filepath.Join(vmDir, "qga.sock")
	config.Args = append(config.Args,
		"-chardev", fmt.Sprintf("socket,path=%s,server=on,wait=off,id=qga0", qgaSocket),
		"-device", "virtio-serial",
		"-device", "virtserialport,chardev=qga0,name=org.qemu.guest_agent.0")
	config.Spec.ProviderConfig["qga_socket"] = qgaSocket

	// VNC display. Allocated through the same state-file-aware allocator the
	// SSH port uses, so a port a stopped VM's record holds is not handed out
	// again after a daemon restart.
	vncPort := p.allocatePort(spec.Name, "vnc_port", vncBasePort)
	vncDisplay := vncPort - vncBasePort
	config.Args = append(config.Args, "-vnc", fmt.Sprintf(":%d", vncDisplay))

	// Store VNC port in config for later retrieval
	config.Spec.ProviderConfig["vnc_port"] = vncPort

	// Display option (for local use)
	display := "none"
	if d, ok := spec.ProviderConfig["display"].(string); ok && d != "" {
		display = d
	}
	// SECURITY: Only allow known-safe display types to prevent command injection
	allowedDisplays := map[string]bool{
		"none":         true,
		"gtk":          true,
		"sdl":          true,
		"spice-app":    true,
		"spice":        true,
		"cocoa":        true,
		"egl-headless": true,
		"curses":       true,
	}
	if !allowedDisplays[display] {
		p.logger.Warn("unsupported QEMU display type, falling back to none",
			"display", display, "jail", spec.Name)
		display = "none"
	}
	config.Args = append(config.Args, "-display", display)

	// QMP socket for control
	config.QMPSocket = filepath.Join(vmDir, "qmp.sock")
	config.Args = append(config.Args, "-qmp",
		fmt.Sprintf("unix:%s,server=on,wait=off", config.QMPSocket))

	// Monitor socket for console access
	monitorPath := filepath.Join(vmDir, "monitor.sock")
	config.Args = append(config.Args, "-monitor",
		fmt.Sprintf("unix:%s,server=on,wait=off", monitorPath))

	// PID file
	pidPath := filepath.Join(vmDir, "qemu.pid")
	// Daemonize
	config.Args = append(config.Args, "-pidfile", pidPath, "-daemonize")

	return config
}

// vncBasePort is the first VNC display port (QEMU display :0). A VM's vnc_port
// is stored as an absolute port; the display number is port minus this base.
const vncBasePort = 5900

// sshPortFor picks the host port that forwards to a VM's SSH.
//
// Probing what is bound right now is not enough on its own: two VMs that were
// never running at the same moment both got 2222, and the second refused to
// start with "Could not set up host forwarding rule 'tcp::2222-:22'". A VM's
// port is therefore kept in its own state file and reused, and a new VM skips
// the ports the other state files already claim.
func (p *QEMUProvider) sshPortFor(vmName string, reserved ...int) int {
	return p.allocatePort(vmName, "ssh_port", 2222, reserved...)
}

// allocatePort picks a host port for a VM, keeping the one it already has
// unless another VM holds it.
//
// The ports live in each VM's state file because the QEMU arguments are built
// once and replayed at every start: probing the host alone gave 2222 to two
// VMs that were never running at the same moment, and the second refused to
// start with "Could not set up host forwarding rule 'tcp::2222-:22'". The VNC
// display collided the same way, on 5900.
// Ports in reserved are treated as held by another VM even when no state file
// records them: a clone knows from the configuration it was built from which
// ports its source is using, and that is more reliable than the source's state
// file, which an older VM may never have recorded a VNC display in.
func (p *QEMUProvider) allocatePort(vmName, key string, base int, reserved ...int) int {
	taken := make(map[int]bool)
	for _, port := range reserved {
		taken[port] = true
	}
	own, hasOwn := 0, false

	entries, err := os.ReadDir(p.stateDir)
	if err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			config, err := p.loadVMConfig(filepath.Join(p.stateDir, name))
			if err != nil {
				continue
			}
			port, ok := storedPort(config, key)
			if !ok {
				continue
			}
			if strings.TrimSuffix(name, ".json") == vmName {
				own, hasOwn = port, true
				continue
			}
			taken[port] = true
		}
	}

	// Keep the port the operator was told about, unless another VM has since
	// been given the same one — or an unrelated process holds it, in which
	// case starting on it would fail exactly the way this refresh exists to
	// prevent.
	if hasOwn && !taken[own] && p.portIsFree(own) {
		return own
	}

	for port := base; port < base+100; port++ {
		if !taken[port] && p.portIsFree(port) {
			return port
		}
	}
	return base
}

// portIsFree reports whether nothing on the host holds the port.
//
// The answer is only as good as the moment it is given: the probe releases the
// port before the caller uses it, so another process can still claim it in
// between.
func (p *QEMUProvider) portIsFree(port int) bool {
	p.portMu.Lock()
	defer p.portMu.Unlock()

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// storedPort reads a port a VM was given, whatever numeric type it came back
// from JSON as.
func storedPort(config *vmConfig, key string) (int, bool) {
	if config == nil || config.Spec.ProviderConfig == nil {
		return 0, false
	}
	switch port := config.Spec.ProviderConfig[key].(type) {
	case int:
		return port, true
	case float64:
		return int(port), true
	}
	return 0, false
}

// findAvailablePort finds an available TCP port starting from startPort.
//
// The reservation is inherently best-effort: portIsFree holds the port only for
// the duration of its probe and releases it before the caller uses it, so a
// concurrent process could still claim it in the window between.
func (p *QEMUProvider) findAvailablePort(startPort int) int {
	for port := startPort; port < startPort+100; port++ {
		if p.portIsFree(port) {
			return port
		}
	}
	return startPort // Fallback
}

// qemuArchName maps a Hospitus architecture name to the name QEMU uses in its
// firmware filenames (edk2-<name>-{code,vars}.fd).
func qemuArchName(arch string) string {
	switch arch {
	case "amd64", "x86_64":
		return "x86_64"
	case "arm64", "aarch64":
		return "aarch64"
	default:
		return arch
	}
}

// qemuVarsArchName maps an architecture to the name QEMU uses in its VARS
// firmware filename, which is not always the one used for the CODE file:
// upstream ships edk2-x86_64-code.fd next to edk2-i386-vars.fd, and
// edk2-aarch64-code.fd next to edk2-arm-vars.fd. Deriving the vars name from
// the code name found no template at all on macOS, FreeBSD or any Linux
// installing QEMU's own firmware, so UEFI guests booted without persistent
// variables.
func qemuVarsArchName(arch string) string {
	switch arch {
	case "amd64", "x86_64", "i386":
		return "i386"
	case "arm64", "aarch64", "arm":
		return "arm"
	default:
		return qemuArchName(arch)
	}
}

// uefiVarsCandidates returns the VARS template search paths for arch, symmetric
// to detectUEFIFirmware. When the resolved CODE firmware path is known, the
// matching vars file (…-code.fd → …-vars.fd, OVMF_CODE.fd → OVMF_VARS.fd) is
// tried first, then the name QEMU actually ships.
func uefiVarsCandidates(arch, codePath string) []string {
	va := qemuVarsArchName(arch)
	var candidates []string
	if codePath != "" {
		v := strings.Replace(codePath, "-code.fd", "-vars.fd", 1)
		v = strings.Replace(v, "_CODE.fd", "_VARS.fd", 1)
		if v != codePath {
			candidates = append(candidates, v)
		}
		// Same directory, QEMU's own vars name for this architecture.
		candidates = append(candidates, filepath.Join(filepath.Dir(codePath), "edk2-"+va+"-vars.fd"))
	}
	return append(candidates,
		"/usr/local/share/qemu/edk2-"+va+"-vars.fd",
		"/opt/homebrew/share/qemu/edk2-"+va+"-vars.fd",
		"/usr/share/qemu/edk2-"+va+"-vars.fd",
		"/usr/share/OVMF/OVMF_VARS.fd",
		"/usr/share/AAVMF/AAVMF_VARS.fd",
	)
}

// createUEFIVarsFile creates a UEFI variables file for a VM
func (p *QEMUProvider) createUEFIVarsFile(arch, destPath string) error {
	// Find the template vars file, symmetric to detectUEFIFirmware.
	candidates := uefiVarsCandidates(arch, p.uefiFirmware[arch])

	var templatePath string
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			templatePath = path
			break
		}
	}

	if templatePath == "" {
		// No template found, skip UEFI vars
		return nil
	}

	// Copy template to destination
	src, err := os.Open(templatePath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

// saveVMConfig saves VM configuration to file.
//
// The write goes through a temp file and a rename: allocatePort and
// ListInstances parse every state file concurrently with writers, and a bare
// WriteFile let a reader see a half-written config (silently treating that
// VM's ports as free) and a crash mid-write corrupt it permanently. The temp
// name does not end in ".json" so directory scans never pick it up.
func (p *QEMUProvider) saveVMConfig(config *vmConfig, path string) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// loadVMConfig loads VM configuration from file
func (p *QEMUProvider) loadVMConfig(path string) (*vmConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config vmConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// vmExists checks if a VM configuration exists
func (p *QEMUProvider) vmExists(ctx context.Context, name string) (bool, error) {
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", name))
	_, err := os.Stat(configPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// isVMRunning checks if a VM is currently running
func (p *QEMUProvider) isVMRunning(ctx context.Context, name string) (bool, error) {
	pidFile := filepath.Join(p.stateDir, fmt.Sprintf("%s.pid", name))
	pidData, err := os.ReadFile(pidFile)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil || pid <= 0 {
		return false, nil
	}

	// Check if process exists by sending signal 0
	// On Unix, signal 0 doesn't actually send a signal but checks if the process exists
	// and if we have permission to send signals to it
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, nil
	}

	// Use syscall.Signal(0) to properly check process existence
	// os.Signal(nil) does NOT work - it returns an error instead of checking
	if err := process.Signal(syscall.Signal(0)); err != nil {
		// Process doesn't exist or we don't have permission
		// Clean up stale PID file
		if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
			p.logger.Warn("failed to remove stale pid file", "path", pidFile)
		}
		return false, nil
	}

	// Signal(0) only proves *a* process with this PID exists. After a QEMU
	// crash and PID reuse it could be an unrelated process — and the root
	// daemon would happily kill it in StopInstance. Verify the identity.
	if !p.pidBelongsToVM(ctx, pid, name) {
		if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
			p.logger.Warn("failed to remove stale pid file", "path", pidFile)
		}
		return false, nil
	}

	return true, nil
}

// pidBelongsToVM reports whether pid is a running qemu process for vmName. It
// inspects the process command line (ps -p <pid> -o command=) and requires the
// qemu binary plus a "-name <vm>" argument pair matching the VM as a whole
// argument, guarding against acting on a reused PID — and against "web"
// matching the middle of "web2". Instance names are validated to contain no
// whitespace, so splitting the ps line on spaces preserves them.
func (p *QEMUProvider) pidBelongsToVM(ctx context.Context, pid int, vmName string) bool {
	out, err := p.cmd().Output(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || !strings.Contains(strings.ToLower(fields[0]), "qemu") {
		return false
	}
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "-name" && fields[i+1] == vmName {
			return true
		}
	}
	return false
}

// findBinary finds a QEMU binary in PATH
func (p *QEMUProvider) findBinary(name string) string {
	binary, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return binary
}

// buildNetdevArgs builds the -netdev argument string for a network
func (p *QEMUProvider) buildNetdevArgs(netID string, network provider.NetworkSpec, providerConfig map[string]interface{}) string {
	switch network.Type {
	case provider.NetworkTypeBridge:
		// Bridge mode - attach to existing bridge
		bridge := network.Bridge
		if bridge == "" {
			bridge = "br0"
		}
		return fmt.Sprintf("bridge,id=%s,br=%s", netID, bridge)

	case provider.NetworkTypeNAT:
		// User-mode networking with port forwarding
		args := fmt.Sprintf("user,id=%s", netID)

		// Add port forwards from provider config
		if portForwards, ok := providerConfig["port_forwards"].([]interface{}); ok {
			for _, pf := range portForwards {
				pfMap, ok := pf.(map[string]interface{})
				if !ok {
					continue
				}
				hostPort := 0
				guestPort := 0
				protocol := "tcp"

				switch h := pfMap["host"].(type) {
				case float64:
					hostPort = int(h)
				case int:
					hostPort = h
				}
				switch c := pfMap["container"].(type) {
				case float64:
					guestPort = int(c)
				case int:
					guestPort = c
				}
				if proto, ok := pfMap["protocol"].(string); ok && proto != "" {
					protocol = proto
				}
				// SECURITY: Only allow tcp/udp protocols
				if protocol != "tcp" && protocol != "udp" {
					p.logger.Warn("invalid port-forward protocol, falling back to tcp",
						"protocol", protocol)
					protocol = "tcp"
				}

				if hostPort > 0 && guestPort > 0 {
					// Check if host port is available, otherwise find a new one
					actualPort := p.findAvailablePort(hostPort)
					// Update the port_forwards with actual port for user reference
					pfMap["actual_host_port"] = actualPort
					// Format: hostfwd=tcp::2222-:22
					args += fmt.Sprintf(",hostfwd=%s::%d-:%d", protocol, actualPort, guestPort)
				}
			}
		}

		return args

	case provider.NetworkTypeNone:
		return fmt.Sprintf("user,id=%s,restrict=yes", netID)

	default:
		// Default to user mode NAT
		return fmt.Sprintf("user,id=%s", netID)
	}
}

// Compile-time assertion: QEMUProvider implements SnapshotProvider
var _ provider.SnapshotProvider = (*QEMUProvider)(nil)

// Snapshot support implementation

// CreateSnapshot creates a snapshot of an instance.
//
// QEMU snapshots are created using qemu-img snapshot command.
// The instance must be stopped for snapshot creation to ensure consistency.
//
// SECURITY: Snapshot name is validated to prevent command injection.
func (p *QEMUProvider) CreateSnapshot(ctx context.Context, handle provider.InstanceHandle, name string) (provider.SnapshotHandle, error) {
	// SECURITY: Validate snapshot name
	if err := validation.ValidateSnapshotName(name); err != nil {
		return provider.SnapshotHandle{}, fmt.Errorf("invalid snapshot name: %w", err)
	}

	info, err := p.GetInstanceInfo(ctx, handle)
	if err != nil {
		return provider.SnapshotHandle{}, fmt.Errorf("failed to get instance info: %w", err)
	}

	// Check if instance is stopped (required for consistent snapshots)
	if info.State != provider.StateStopped {
		return provider.SnapshotHandle{}, fmt.Errorf("instance must be stopped to create snapshot (current state: %s)", info.State)
	}

	// Find the primary disk
	if len(info.Spec.Disks) == 0 {
		return provider.SnapshotHandle{}, fmt.Errorf("instance has no disks")
	}

	primaryDisk := info.Spec.Disks[0]
	diskPath := primaryDisk.Path

	// Verify disk exists
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		return provider.SnapshotHandle{}, fmt.Errorf("disk image not found: %s", diskPath)
	}

	// Create snapshot using qemu-img snapshot command
	// SECURITY: name is validated above, diskPath comes from our own datastore
	output, err := p.cmd().CombinedOutput(ctx, "qemu-img", "snapshot", "-c", name, diskPath)
	if err != nil {
		return provider.SnapshotHandle{}, fmt.Errorf("failed to create snapshot: %w (output: %s)", err, string(output))
	}

	// Create snapshot handle
	snapshotHandle := provider.SnapshotHandle{
		ID:       fmt.Sprintf("%s_%s", handle.ID, name),
		Instance: handle.ID,
		Metadata: map[string]interface{}{
			"disk_path": diskPath,
			"name":      name,
			"created":   time.Now().Format(time.RFC3339),
		},
	}

	return snapshotHandle, nil
}

// DeleteSnapshot deletes a snapshot.
//
// SECURITY: Uses validated snapshot ID from handle.
func (p *QEMUProvider) DeleteSnapshot(ctx context.Context, snapshot provider.SnapshotHandle) error {
	// Extract snapshot name and disk path from metadata
	name, ok := snapshot.Metadata["name"].(string)
	if !ok {
		return fmt.Errorf("invalid snapshot metadata: missing name")
	}

	diskPath, ok := snapshot.Metadata["disk_path"].(string)
	if !ok {
		return fmt.Errorf("invalid snapshot metadata: missing disk_path")
	}

	// Verify disk exists
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		return fmt.Errorf("disk image not found: %s", diskPath)
	}

	// Delete snapshot using qemu-img snapshot command
	// SECURITY: name was validated during creation
	output, err := p.cmd().CombinedOutput(ctx, "qemu-img", "snapshot", "-d", name, diskPath)
	if err != nil {
		return fmt.Errorf("failed to delete snapshot: %w (output: %s)", err, string(output))
	}

	return nil
}

// RestoreSnapshot restores an instance to a snapshot state.
//
// This operation will overwrite the current disk state with the snapshot.
// The instance must be stopped before restoration.
//
// SECURITY: All paths and names are validated.
func (p *QEMUProvider) RestoreSnapshot(ctx context.Context, handle provider.InstanceHandle, snapshot provider.SnapshotHandle) error {
	// Verify snapshot belongs to this instance
	if snapshot.Instance != handle.ID {
		return fmt.Errorf("snapshot does not belong to instance %s", handle.ID)
	}

	info, err := p.GetInstanceInfo(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get instance info: %w", err)
	}

	// Check if instance is stopped
	if info.State != provider.StateStopped {
		return fmt.Errorf("instance must be stopped to restore snapshot (current state: %s)", info.State)
	}

	// Extract snapshot name and disk path from metadata
	name, ok := snapshot.Metadata["name"].(string)
	if !ok {
		return fmt.Errorf("invalid snapshot metadata: missing name")
	}

	diskPath, ok := snapshot.Metadata["disk_path"].(string)
	if !ok {
		return fmt.Errorf("invalid snapshot metadata: missing disk_path")
	}

	// Verify disk exists
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		return fmt.Errorf("disk image not found: %s", diskPath)
	}

	// Restore snapshot using qemu-img snapshot command
	// SECURITY: name was validated during creation
	output, err := p.cmd().CombinedOutput(ctx, "qemu-img", "snapshot", "-a", name, diskPath)
	if err != nil {
		return fmt.Errorf("failed to restore snapshot: %w (output: %s)", err, string(output))
	}

	return nil
}

// ListSnapshots lists all snapshots for an instance.
//
// SECURITY: Parses qemu-img output carefully to avoid injection.
func (p *QEMUProvider) ListSnapshots(ctx context.Context, handle provider.InstanceHandle) ([]provider.SnapshotInfo, error) {
	info, err := p.GetInstanceInfo(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("failed to get instance info: %w", err)
	}

	// Find the primary disk
	if len(info.Spec.Disks) == 0 {
		return nil, fmt.Errorf("instance has no disks")
	}

	primaryDisk := info.Spec.Disks[0]
	diskPath := primaryDisk.Path

	// Verify disk exists
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("disk image not found: %s", diskPath)
	}

	// List snapshots using qemu-img snapshot command
	output, err := p.cmd().CombinedOutput(ctx, "qemu-img", "snapshot", "-l", diskPath)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w (output: %s)", err, string(output))
	}

	// Parse output
	// Example output:
	// Snapshot list:
	// ID        TAG                 VM SIZE                DATE       VM CLOCK
	// 1         snap1                     0 2024-01-01 12:00:00   00:00:00.000
	snapshots := []provider.SnapshotInfo{}
	lines := strings.Split(string(output), "\n")

	// Skip header lines
	for i, line := range lines {
		if i < 2 || strings.TrimSpace(line) == "" {
			continue
		}

		// Parse line
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		// Extract snapshot name (TAG field)
		snapshotName := fields[1]

		// Create snapshot info
		snapshotInfo := provider.SnapshotInfo{
			Handle: provider.SnapshotHandle{
				ID:       fmt.Sprintf("%s_%s", handle.ID, snapshotName),
				Instance: handle.ID,
				Metadata: map[string]interface{}{
					"disk_path": diskPath,
					"name":      snapshotName,
				},
			},
			Name:      snapshotName,
			CreatedAt: time.Now(), // QEMU doesn't provide easy access to creation time from qemu-img
			SizeMB:    0,          // QEMU internal snapshots don't have separate size
		}

		snapshots = append(snapshots, snapshotInfo)
	}

	return snapshots, nil
}

// isPathAllowed validates that a path resides within an allowed directory tree.
// This prevents path traversal attacks where user-supplied paths could access
// arbitrary files on the system.
func (p *QEMUProvider) isPathAllowed(path string) bool {
	// The allowed roots are the QEMU data directory and the images directory.
	// PathWithinAny resolves symlinks on both sides: a symlink placed under a
	// root cannot point its real target outside it, and a root reached through a
	// link (macOS resolves /var to /private/var) still matches.
	allowed, err := validation.PathWithinAny(path, p.dataDir, p.imageDir)
	if err != nil {
		return false
	}
	return allowed
}

// sshHostForward matches the host-forwarding rule that carries a VM's SSH.
//
// The guest port is anchored: unanchored, "-:22" also matched inside "-:2222"
// and "-:220", so a user's own forward to guest 2222 was rewritten to a port
// from the SSH pool and their host port silently replaced.
var sshHostForward = regexp.MustCompile(`hostfwd=tcp::(\d+)-:22(,|$)`)

// vncDisplay matches the display number QEMU is told to serve VNC on.
var vncDisplay = regexp.MustCompile(`^:(\d+)$`)

// refreshSSHForward points the VM's SSH forward at a port that is actually
// available, and reports whether it changed anything.
//
// buildQEMUConfig settles the port once and the arguments are then replayed at
// every start, so two VMs created while neither was running both carried
// "hostfwd=tcp::2222-:22" and the second refused to start:
//
//	Could not set up host forwarding rule 'tcp::2222-:22'
func (p *QEMUProvider) refreshHostPorts(config *vmConfig, vmName string, reserved ...int) bool {
	changed := p.refreshSSHForward(config, vmName, reserved...)
	if p.refreshVNCDisplay(config, vmName, reserved...) {
		changed = true
	}
	return p.refreshUserForwards(config) || changed
}

// refreshUserForwards rebuilds the hostfwd= fragments of the user-mode netdev
// from the port forwards recorded in the config, and reports whether it changed
// anything.
//
// AddPortForward persists a rule and, on a stopped VM, says it is "applied on
// next start" — but the QEMU arguments are built once at create and replayed
// verbatim, so nothing ever put the rule on the command line. A rule added
// through QMP to a running VM disappeared at the next restart the same way, and
// ListPortForwards reported forwards QEMU did not have.
//
// The SSH forward is left to refreshSSHForward, which owns the port it uses.
func (p *QEMUProvider) refreshUserForwards(config *vmConfig) bool {
	forwards := portForwardsFromConfig(config.Spec.ProviderConfig)

	for i, arg := range config.Args {
		if i == 0 || config.Args[i-1] != "-netdev" || !strings.HasPrefix(arg, "user,") {
			continue
		}
		// Only the first user-mode NIC carries the forwards: it is the netdev
		// AddPortForward names when it talks to QMP ("net0").
		fields := strings.Split(arg, ",")
		kept := make([]string, 0, len(fields)+len(forwards))
		for _, field := range fields {
			if strings.HasPrefix(field, "hostfwd=") && !sshHostForward.MatchString(field) {
				continue
			}
			kept = append(kept, field)
		}
		for _, pf := range forwards {
			if pf.GuestPort == 22 && pf.Protocol == "tcp" {
				continue // refreshSSHForward owns this one
			}
			kept = append(kept, fmt.Sprintf("hostfwd=%s::%d-:%d", pf.Protocol, pf.HostPort, pf.GuestPort))
		}

		rebuilt := strings.Join(kept, ",")
		if rebuilt == arg {
			return false
		}
		config.Args[i] = rebuilt
		return true
	}
	return false
}

// refreshVNCDisplay points the VM's VNC display at a free port, and reports
// whether it changed anything. QEMU refuses to start otherwise:
//
//	-vnc :0: Failed to find an available port: Address already in use
func (p *QEMUProvider) refreshVNCDisplay(config *vmConfig, vmName string, reserved ...int) bool {
	for i, arg := range config.Args {
		if i == 0 || config.Args[i-1] != "-vnc" || !vncDisplay.MatchString(arg) {
			continue
		}
		port := p.allocatePort(vmName, "vnc_port", vncBasePort, reserved...)
		display := fmt.Sprintf(":%d", port-vncBasePort)
		if arg == display {
			return false
		}
		config.Args[i] = display
		if config.Spec.ProviderConfig == nil {
			config.Spec.ProviderConfig = map[string]interface{}{}
		}
		config.Spec.ProviderConfig["vnc_port"] = port
		return true
	}
	return false
}

// refreshSSHForward moves the VM's own SSH forward onto a free port.
//
// It acts only when the config records an ssh_port, i.e. when Hospitus allocated
// the forward itself. A forward to guest port 22 that the operator asked for is
// theirs, not the SSH rule this provider manages, and reallocating its host
// port would silently move a port they were told to use.
func (p *QEMUProvider) refreshSSHForward(config *vmConfig, vmName string, reserved ...int) bool {
	if _, recorded := storedPort(config, "ssh_port"); !recorded {
		return false
	}
	for i, arg := range config.Args {
		match := sshHostForward.FindStringSubmatch(arg)
		if match == nil {
			continue
		}
		port := p.sshPortFor(vmName, reserved...)
		if match[1] == strconv.Itoa(port) {
			return false
		}
		config.Args[i] = sshHostForward.ReplaceAllString(arg, fmt.Sprintf("hostfwd=tcp::%d-:22${2}", port))
		if config.Spec.ProviderConfig == nil {
			config.Spec.ProviderConfig = map[string]interface{}{}
		}
		config.Spec.ProviderConfig["ssh_port"] = port
		return true
	}
	return false
}
