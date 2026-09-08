package bhyve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// applyProviderConfig applies bhyve-specific settings from spec.ProviderConfig
// onto config. It is separated from CreateInstance for testability and returns
// an error for invalid settings (e.g. an insecure VNC host or unknown NIC driver).
func (p *BhyveProvider) applyProviderConfig(config *vmConfig, spec provider.InstanceSpec, vmName string) error {
	if spec.ProviderConfig == nil {
		return nil
	}
	// Passthrough. Each entry becomes `-s <slot>,passthru,<selector>`, so a
	// value with a comma in it would add options of its own.
	switch pt := spec.ProviderConfig["passthrough"].(type) {
	case []string:
		config.Passthrough = pt
	case []interface{}:
		for _, v := range pt {
			if s, ok := v.(string); ok {
				config.Passthrough = append(config.Passthrough, s)
			}
		}
	}
	for _, sel := range config.Passthrough {
		if err := validatePassthroughSelector("passthrough", sel); err != nil {
			return err
		}
	}

	// VNC Settings
	// Parse compact "vnc" format first (e.g., "0.0.0.0:5900", "127.0.0.1:5900", "5900")
	if vncStr, ok := spec.ProviderConfig["vnc"].(string); ok {
		config.VNCEnabled = true
		if strings.Contains(vncStr, ":") {
			parts := strings.SplitN(vncStr, ":", 2)
			config.VNCHost = parts[0]
			if port, err := strconv.Atoi(parts[1]); err == nil {
				config.VNCPort = port
			}
		} else {
			config.VNCHost = "127.0.0.1"
			if port, err := strconv.Atoi(vncStr); err == nil {
				config.VNCPort = port
			}
		}
	}

	switch vncEnabled := spec.ProviderConfig["vnc_enabled"].(type) {
	case bool:
		config.VNCEnabled = vncEnabled
	case string:
		config.VNCEnabled = vncEnabled == "true"
	}

	switch vncPort := spec.ProviderConfig["vnc_port"].(type) {
	case int:
		config.VNCPort = vncPort
	case float64:
		config.VNCPort = int(vncPort)
	case string:
		if port, err := strconv.Atoi(vncPort); err == nil {
			config.VNCPort = port
		}
	}

	// VNC host binding
	if vncHost, ok := spec.ProviderConfig["vnc_host"].(string); ok {
		config.VNCHost = vncHost
	}

	// SECURITY: Require explicit opt-in for non-localhost VNC binding.
	// bhyve's fbuf device does not support VNC password authentication,
	// so binding to 0.0.0.0 or a public IP without explicit approval
	// is a security risk.
	if config.VNCEnabled {
		vncInsecure, _ := spec.ProviderConfig["vnc_insecure"].(bool)
		if !vncInsecure {
			// Also check string representation
			if vncInsecureStr, ok := spec.ProviderConfig["vnc_insecure"].(string); ok {
				vncInsecure = vncInsecureStr == "true"
			}
		}
		if err := validateVNCHost(config.VNCHost, vncInsecure); err != nil {
			return fmt.Errorf("VNC security error: %w", err)
		}
		config.VNCInsecure = vncInsecure
		slog.Warn("VNC enabled without authentication — bhyve fbuf has no built-in password support. "+
			"Use vnc_host=127.0.0.1 (default) for local-only access or tunnel through SSH.",
			logging.FieldVM, vmName, "vnc_host", config.VNCHost, "vnc_port", config.VNCPort)
	}

	switch vncWidth := spec.ProviderConfig["vnc_width"].(type) {
	case int:
		config.VNCWidth = vncWidth
	case float64:
		config.VNCWidth = int(vncWidth)
	}

	switch vncHeight := spec.ProviderConfig["vnc_height"].(type) {
	case int:
		config.VNCHeight = vncHeight
	case float64:
		config.VNCHeight = int(vncHeight)
	}

	switch vncWait := spec.ProviderConfig["vnc_wait"].(type) {
	case bool:
		config.VNCWait = vncWait
	case string:
		config.VNCWait = vncWait == "true"
	}

	// TPM 2.0 support
	switch tpm := spec.ProviderConfig["tpm"].(type) {
	case bool:
		config.TPMEnabled = tpm
	case string:
		config.TPMEnabled = tpm == "true"
	}

	// VirtIO RNG entropy device
	switch rng := spec.ProviderConfig["virtio_rng"].(type) {
	case bool:
		config.VirtioRNG = rng
	case string:
		config.VirtioRNG = rng == "true"
	}

	// NIC driver override (e.g. "e1000" for Windows guests without VirtIO drivers)
	if nicDriver, ok := spec.ProviderConfig["nic_driver"].(string); ok && nicDriver != "" {
		// SECURITY: Only allow known-safe NIC drivers
		switch nicDriver {
		case "virtio-net", "e1000":
		default:
			return fmt.Errorf("unsupported NIC driver: %q (supported: virtio-net, e1000)", nicDriver)
		}
		config.NICDrivers = make([]string, len(config.TapDevs))
		for i := range config.NICDrivers {
			config.NICDrivers[i] = nicDriver
		}
	}

	switch ignoreMSR := spec.ProviderConfig["ignore_msr"].(type) {
	case bool:
		config.MSRIgnoreUnimplemented = ignoreMSR
	case string:
		config.MSRIgnoreUnimplemented = ignoreMSR == "true"
	}

	// USB tablet (standalone xhci,tablet device)
	switch usbTablet := spec.ProviderConfig["usb_tablet"].(type) {
	case bool:
		config.USBTablet = usbTablet
	case string:
		config.USBTablet = usbTablet == "true"
	}

	// USB device passthrough (PCI-level; accepts []string or []interface{})
	switch usbDevs := spec.ProviderConfig["usb_devices"].(type) {
	case []string:
		config.USBDevices = usbDevs
	case []interface{}:
		for _, d := range usbDevs {
			if s, ok := d.(string); ok {
				config.USBDevices = append(config.USBDevices, s)
			}
		}
	}
	for _, sel := range config.USBDevices {
		if err := validatePassthroughSelector("usb", sel); err != nil {
			return err
		}
	}

	// Default disk driver. The value is written into a slot specification, so
	// it is taken from the set bhyve understands rather than passed through.
	if diskDriver, ok := spec.ProviderConfig["disk_driver"].(string); ok && diskDriver != "" {
		if err := validateDiskDriver(diskDriver); err != nil {
			return err
		}
		config.DiskDriver = diskDriver
	}

	// VLAN IDs per tap ([]int or comma-separated string)
	switch vlanIDs := spec.ProviderConfig["vlan_ids"].(type) {
	case []int:
		config.VLANIDs = vlanIDs
	case []interface{}:
		for _, v := range vlanIDs {
			switch id := v.(type) {
			case int:
				config.VLANIDs = append(config.VLANIDs, id)
			case float64:
				config.VLANIDs = append(config.VLANIDs, int(id))
			}
		}
	case string:
		if vlanIDs != "" {
			config.VLANIDs = stringToIntSlice(vlanIDs)
		}
	}

	// IPv6 NAT
	switch ipv6 := spec.ProviderConfig["ipv6_enabled"].(type) {
	case bool:
		config.IPv6Enabled = ipv6
	case string:
		config.IPv6Enabled = ipv6 == "true"
	}
	if ipv6Prefix, ok := spec.ProviderConfig["ipv6_prefix"].(string); ok && ipv6Prefix != "" {
		config.IPv6Prefix = ipv6Prefix
	}
	return nil
}

// validateBootloader refuses a boot method that produces a VM which cannot
// start.
//
// Only UEFI is wired up: buildBhyveArgs adds a bootrom for it and for nothing
// else, so "bios", "grub" and "bhyveload" each created a VM that bhyve then
// refused with "no bootrom was configured" and exit status 4. Failing here
// keeps a VM that cannot boot from being created at all.
func validateBootloader(bootloader string) error {
	switch bootloader {
	case "", "uefi":
		return nil
	default:
		return fmt.Errorf("bootloader %q is not supported; hospitus boots bhyve VMs through UEFI", bootloader)
	}
}

func (p *BhyveProvider) CreateInstance(ctx context.Context, spec provider.InstanceSpec) (handle provider.InstanceHandle, err error) {
	// Attach provider context so the API layer can tell a failed create from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("bhyve", "create", spec.Name, err) }()

	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, spec.Name)
	if lockErr != nil {
		return provider.InstanceHandle{}, lockErr
	}
	defer releaseLock()

	vmName := spec.Name

	// SECURITY: Validate instance name to prevent command injection and path traversal
	// This prevents attacks like:
	//   - Command injection: "test; rm -rf /"
	//   - Path traversal: "../../../etc/passwd"
	// The validation package ensures only safe alphanumeric names with dashes/underscores
	if err := validation.ValidateInstanceName(vmName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid instance name: %w", err)
	}

	// SECURITY: Validate resource limits to prevent resource exhaustion
	if err := validation.ValidateResourceLimits(spec.CPUs, spec.MemoryMB); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid resource limits: %w", err)
	}

	if err := validateBootloader(spec.Bootloader); err != nil {
		return provider.InstanceHandle{}, err
	}

	// Serialize creation to prevent TOCTOU race
	p.createMu.Lock()
	defer p.createMu.Unlock()

	// Check if VM already exists by looking for its directory
	// Why directory-based check?
	//   - Simple and reliable (filesystem is source of truth)
	//   - No need for a separate database/index
	//   - Survives daemon restarts
	vmDir := filepath.Join(p.dataDir, vmName)
	if _, err := os.Stat(vmDir); err == nil {
		// Directory exists. Check for vm.conf to determine if it's a real VM
		// or an incomplete leftover from a failed create (e.g. tap device failure).
		// Only directories with no vm.conf are safe to treat as orphans.
		if _, err := os.Stat(filepath.Join(vmDir, "vm.conf")); os.IsNotExist(err) {
			if rmErr := os.RemoveAll(vmDir); rmErr != nil {
				slog.Warn("failed to clean up VM directory", "path", vmDir, logging.FieldError, rmErr)
			}
		} else {
			return provider.InstanceHandle{}, provider.ErrInstanceExists
		}
	}

	// Create VM directory with 0755 permissions
	// This directory will contain:
	//   - vm.conf: persistent configuration
	//   - vm.state: runtime state
	//   - disk*.img: raw disk images (if not using ZVOLs)
	//   - console: null modem device
	//   - bhyve.log: bhyve process output
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create VM directory: %w", err)
	}

	// Determine architecture (default to host arch)
	arch := runtime.GOARCH
	if archStr, ok := spec.ProviderConfig["arch"].(string); ok && archStr != "" {
		arch = archStr
	}
	if spec.Arch != "" {
		arch = spec.Arch
	}

	// Resolve image path if specified
	var cloudImage string
	var isoPath string
	if spec.Image != "" && spec.Image != "none" {
		// One parser, not two: this was a second copy of parseImageSource, and
		// the two could drift over the default type or the split.
		imageType, reference, err := p.parseImageSource(spec.Image)
		if err != nil {
			if rmErr := os.RemoveAll(vmDir); rmErr != nil {
				slog.Warn("failed to clean up VM directory", "path", vmDir, logging.FieldError, rmErr)
			}
			return provider.InstanceHandle{}, fmt.Errorf("invalid image %q: %w", spec.Image, err)
		}

		if imageType == "iso" || strings.HasSuffix(strings.ToLower(reference), ".iso") || spec.OSType == "iso" {
			resolved, err := p.resolveISOPath(reference)
			if err != nil {
				if rmErr := os.RemoveAll(vmDir); rmErr != nil {
					slog.Warn("failed to clean up VM directory", "path", vmDir, logging.FieldError, rmErr)
				}
				return provider.InstanceHandle{}, err
			}
			isoPath = resolved
		} else {
			resolved, err := p.resolveCloudImagePath(reference, arch)
			if err != nil {
				if rmErr := os.RemoveAll(vmDir); rmErr != nil {
					slog.Warn("failed to clean up VM directory", "path", vmDir, logging.FieldError, rmErr)
				}
				return provider.InstanceHandle{}, err
			}
			cloudImage = resolved
		}
	}

	// A cloud image needs somewhere to be written. Give it a root disk when
	// the caller named none.
	//
	// The image is deployed further down only when a disk already exists, so
	// without this "hospitus bhyve create --image ubuntu-24.04-amd64" leaves the
	// VM in a UEFI shell with nothing to boot. A manifest names
	// storage.root_disk itself; the command line has no such field.
	//
	// 20 GiB matches the default a manifest uses.
	if cloudImage != "" && len(spec.Disks) == 0 {
		spec.Disks = []provider.DiskSpec{{
			Type:     provider.DiskTypeZVOL,
			SizeGB:   defaultCloudImageDiskGB,
			Bootable: true,
		}}
		slog.Info("no disk given for a cloud image; adding a root disk",
			logging.FieldVM, spec.Name, "size_gb", defaultCloudImageDiskGB)
	}

	// Create disk images
	// We support three disk types:
	//   1. ZVOL (ZFS volume) - recommended for production
	//   2. RAW (regular file) - simpler but fewer features
	//   3. PHYSICAL (device passthrough) - use existing physical disk
	//
	// Why prefer ZVOLs?
	//   - Native ZFS snapshots and clones
	//   - Compression support
	//   - Data integrity (checksums)
	//   - Better performance (no filesystem overhead)
	diskPaths := make([]string, len(spec.Disks))
	var createdZVOLs []string // track for cleanup on error
	for i, disk := range spec.Disks {
		var diskPath string

		// A disk that names no type gets a ZVOL, which is what the guide
		// promises and what the list above recommends.
		//
		// "--disk 20:data" parses to a size and a device name only, leaving the
		// type empty; without this it falls to the raw-image branch below and
		// the VM gets a disk0.img where the documentation says to look under
		// /dev/zvol. The provider refuses to initialize without its ZFS parent,
		// so a volume can always be made here.
		if disk.Type == "" {
			disk.Type = provider.DiskTypeZVOL
		}

		switch disk.Type {
		case provider.DiskTypePhysical:
			// Physical device passthrough
			// Use case: Pass through a physical SSD/HDD directly to the VM
			//           (e.g., dual-boot Windows/FreeBSD on separate SSDs)
			//
			// Security considerations:
			//   - Validate that the path is a block device
			//   - Must be in /dev/ directory
			//   - Check that device exists and is accessible
			//
			// Example use case (from CBSD):
			//   FreeBSD on /dev/ada0, Windows on /dev/ada1
			//   Pass /dev/ada1 to VM to boot existing Windows installation

			// SECURITY: the device must exist, be a device, and be named in
			// the operator's allow-list. AttachDisk and ImportInstance apply
			// the same guard, so a VM cannot acquire one later either.
			if err := p.validatePhysicalDisk(disk.Path); err != nil {
				p.cleanupZVOLs(ctx, createdZVOLs)
				if rmErr := os.RemoveAll(vmDir); rmErr != nil {
					slog.Warn("failed to clean up VM directory", "path", vmDir, logging.FieldError, rmErr)
				}
				return provider.InstanceHandle{}, err
			}

			// Use the device path directly
			diskPath = disk.Path

			// Optionally create a symlink in VM directory for reference
			// This helps track which physical devices are assigned to which VMs
			symlinkPath := filepath.Join(vmDir, fmt.Sprintf("disk%d.dev", i))
			if err := os.Symlink(disk.Path, symlinkPath); err != nil {
				// Non-fatal error - log but continue
				slog.Warn("Failed to create symlink for physical disk",
					logging.FieldVM, vmName,
					logging.FieldError, err)
			}
		case provider.DiskTypeZVOL:
			// Create ZFS volume
			// ZVOL naming: <pool>/<parent>/<vm-name>/disk<N>
			// Example: zroot/hospitus/bhyve/my-vm/disk0
			//
			// Why this structure?
			//   - Allows per-VM ZFS properties (quota, compression)
			//   - Easy to snapshot entire VM (zfs snapshot zroot/hospitus/bhyve/my-vm@backup)
			//   - Clear hierarchy in `zfs list`
			zvolName := fmt.Sprintf("%s/%s/disk%d", p.zfsParent, vmName, i)

			// A caller may hand over a volume it has already filled. Cloning
			// does: it copies the source disk with zfs send/receive and then
			// asks for an instance around it. Creating a second, empty volume
			// here left that copy orphaned under the source VM and gave the
			// clone a blank disk — reported as a success, with a full-size
			// leak on every attempt.
			// Confined to this provider's parent: without it, any existing
			// volume on the host — another VM's, or one of the operator's —
			// could be named in a spec and adopted as this VM's disk.
			if disk.Path != "" && strings.HasPrefix(disk.Path, "/dev/zvol/"+p.zfsParent+"/") &&
				p.zvolSizeGB(ctx, strings.TrimPrefix(disk.Path, "/dev/zvol/")) > 0 {
				diskPath = disk.Path
				diskPaths[i] = diskPath
				continue
			}

			if err := p.createZVOL(ctx, zvolName, int64(disk.SizeGB)); err != nil {
				p.cleanupZVOLs(ctx, createdZVOLs)
				if rmErr := os.RemoveAll(vmDir); rmErr != nil {
					slog.Warn("failed to clean up VM directory", "path", vmDir, logging.FieldError, rmErr)
				}
				return provider.InstanceHandle{}, fmt.Errorf("failed to create ZVOL: %w", err)
			}
			createdZVOLs = append(createdZVOLs, zvolName)
			// ZVOLs appear as block devices in /dev/zvol/
			diskPath = fmt.Sprintf("/dev/zvol/%s", zvolName)
		default:
			// Create raw disk image using truncate(1)
			// Why truncate instead of dd?
			//   - Instant (sparse file allocation)
			//   - No need to write zeros
			//   - Filesystem will allocate blocks on demand
			//
			// Trade-off: File appears as SizeGB but uses minimal space initially.
			// This is fine for most use cases but can surprise users checking df.
			// A caller may hand over an image it has already filled, the way
			// the ZVOL branch above accepts a volume. Cloning does: it copies
			// the source's image into the new VM's directory first. Creating a
			// fresh one here gave the clone a blank disk and reported success.
			// Same confinement as the ZVOL branch, and the same one AttachDisk
			// applies: an adopted image becomes a device the guest reads.
			if disk.Path != "" {
				within, pathErr := validation.PathWithinAny(disk.Path, vmDir, p.dataDir, p.imageDir)
				if pathErr == nil && within {
					if info, statErr := os.Stat(disk.Path); statErr == nil && !info.IsDir() && info.Size() > 0 {
						diskPaths[i] = disk.Path
						continue
					}
				}
			}

			diskPath = filepath.Join(vmDir, fmt.Sprintf("disk%d.img", i))
			sizeBytes := int64(disk.SizeGB) * 1024 * 1024 * 1024
			if err := p.createRawDisk(ctx, diskPath, sizeBytes); err != nil {
				p.cleanupZVOLs(ctx, createdZVOLs)
				if rmErr := os.RemoveAll(vmDir); rmErr != nil {
					slog.Warn("failed to clean up VM directory", "path", vmDir, logging.FieldError, rmErr)
				}
				return provider.InstanceHandle{}, fmt.Errorf("failed to create disk: %w", err)
			}
		}

		diskPaths[i] = diskPath
	}

	// Deploy cloud image to the first disk if specified
	if cloudImage != "" && len(diskPaths) > 0 {
		if err := p.deployCloudImage(ctx, cloudImage, diskPaths[0]); err != nil {
			p.cleanupZVOLs(ctx, createdZVOLs)
			if rmErr := os.RemoveAll(vmDir); rmErr != nil {
				slog.Warn("failed to clean up VM directory", "path", vmDir, logging.FieldError, rmErr)
			}
			return provider.InstanceHandle{}, fmt.Errorf("failed to deploy cloud image: %w", err)
		}
		// For legacy FreeBSD ZFS images (non-BASIC-CLOUDINIT), inject nuageinit_enable
		// and dual console before first boot. Skip when cloud-init ISO is present —
		// BASIC-CLOUDINIT images already have nuageinit enabled and the seed ISO
		// is the correct provisioning mechanism.
		if strings.HasPrefix(diskPaths[0], "/dev/zvol/") && spec.CloudInit == nil {
			if err := p.injectFreeBSDGuestConfig(ctx, diskPaths[0]); err != nil {
				slog.Warn("FreeBSD guest config injection failed (non-fatal)",
					logging.FieldVM, vmName, logging.FieldError, err)
			}
		}
	}

	// Determine disk drivers for each disk
	// This supports virtio-blk (default), ahci-hd (compatibility), nvme (performance)
	diskDrivers := make([]string, len(spec.Disks))
	for i := range spec.Disks {
		driver := p.getBhyveDiskDriver(spec, i)
		diskDrivers[i] = string(driver)
	}

	// If we have an ISO, attach it as an additional disk
	if isoPath != "" {
		diskPaths = append(diskPaths, isoPath)
		diskDrivers = append(diskDrivers, "ahci-cd")
	}

	// Generate cloud-init ISO if cloud-init config is provided
	// Cloud-init uses NoCloud datasource with ISO containing meta-data and user-data
	if spec.CloudInit != nil {
		ciISOPath, err := p.generateCloudInitISO(ctx, vmName, spec.CloudInit)
		if err != nil {
			p.cleanupZVOLs(ctx, createdZVOLs)
			if rmErr := os.RemoveAll(vmDir); rmErr != nil {
				slog.Warn("failed to clean up VM directory", "path", vmDir, logging.FieldError, rmErr)
			}
			return provider.InstanceHandle{}, fmt.Errorf("failed to generate cloud-init ISO: %w", err)
		}
		if ciISOPath != "" {
			// Append cloud-init ISO as additional disk (CD-ROM)
			diskPaths = append(diskPaths, ciISOPath)
			// Cloud-init ISO uses AHCI CD-ROM driver for maximum compatibility
			diskDrivers = append(diskDrivers, "ahci-cd")
		}
	}

	// Create network interfaces (tap devices)
	// bhyve uses FreeBSD tap(4) devices for network I/O:
	//   - tap device = virtual Ethernet interface
	//   - VM writes to tap device → appears on host network
	//   - Host writes to tap device → appears in VM
	//
	// Naming convention: tap_<vm-name>_<index>
	// Example: tap_my-vm_0, tap_my-vm_1
	//
	// Why custom names instead of letting system auto-assign?
	//   - Easier debugging (can identify which VM owns which tap)
	//   - Persistent across reboots
	//   - Clear association in ifconfig output
	tapDevs := make([]string, len(spec.Networks))
	bridges := make([]string, len(spec.Networks))
	// Recorded per NIC: NATEnabled is VM-wide and cannot say which interface of
	// a mixed VM is the NAT one.
	netTypes := make([]string, len(spec.Networks))
	var createdTaps []string // track for cleanup on error
	natEnabled := false

	// cleanup rolls back every artifact created so far when creation fails partway
	// through. It closes over createdTaps/natEnabled, so it always reflects the
	// current progress. Using one closure for all error paths guarantees taps, NAT
	// rules, ZVOLs and the VM directory are torn down consistently.
	cleanup := func() {
		if natEnabled {
			p.teardownNATForVM(ctx, vmName)
		}
		p.cleanupTaps(ctx, createdTaps)
		p.cleanupZVOLs(ctx, createdZVOLs)
		if rmErr := os.RemoveAll(vmDir); rmErr != nil {
			slog.Warn("failed to clean up VM directory", "path", vmDir, logging.FieldError, rmErr)
		}
	}

	for i := range spec.Networks {
		network := &spec.Networks[i]
		tapDev, err := p.createTapDevice(ctx, vmName, i)
		if err != nil {
			cleanup()
			return provider.InstanceHandle{}, fmt.Errorf("failed to create tap device: %w", err)
		}
		createdTaps = append(createdTaps, tapDev)
		tapDevs[i] = tapDev
		netTypes[i] = string(network.Type)

		// Attach to bridge if specified (for bridged networking)
		// Bridge networking allows VM to appear on the same network as host:
		//   Host: em0 → bridge0 → tap_my-vm_0 → VM
		//
		// For NAT mode, attach to the shared NAT bridge and set up PF masquerade.
		switch network.Type {
		case provider.NetworkTypeBridge:
			if network.Bridge != "" {
				if err := p.ensureBridge(ctx, network.Bridge); err != nil {
					cleanup()
					return provider.InstanceHandle{}, fmt.Errorf("failed to ensure bridge: %w", err)
				}
				if err := p.attachToBridge(ctx, tapDev, network.Bridge); err != nil {
					cleanup()
					return provider.InstanceHandle{}, fmt.Errorf("failed to attach to bridge: %w", err)
				}
				// Recorded so start can put the tap back after a reboot: bridge
				// membership is host state, and nothing else remembers it.
				bridges[i] = network.Bridge
			}
		case provider.NetworkTypeNAT:
			if err := p.setupNATForVM(ctx, vmName, tapDev); err != nil {
				cleanup()
				return provider.InstanceHandle{}, fmt.Errorf("failed to setup NAT: %w", err)
			}
			natEnabled = true
		}
	}

	// Create console device path (null modem)
	// nmdm devices are created dynamically by FreeBSD when accessed
	// Convention: /dev/nmdm-<vmname>A for VM side, /dev/nmdm-<vmname>B for host side
	var consolePath string
	if p.hasNMDM {
		consolePath = fmt.Sprintf("/dev/nmdm-%sA", vmName)
	}

	// Determine boot mode: UEFI vs legacy BIOS
	// Default to UEFI because:
	//   - Modern OSes expect UEFI (Windows 10+, recent Linux)
	//   - Secure Boot support (if we add it later)
	//   - Better compatibility with GPT disks
	//
	// Fall back to BIOS if:
	//   - User explicitly requests it (Bootloader = "bios")
	//   - UEFI firmware not detected during Initialize()
	uefiBoot := spec.Bootloader == "" || spec.Bootloader == "uefi"

	config := &vmConfig{
		Name:        vmName,
		CPUs:        spec.CPUs,
		MemoryMB:    spec.MemoryMB,
		DiskPaths:   diskPaths,
		DiskDrivers: diskDrivers,
		TapDevs:     tapDevs,
		Bridges:     bridges,
		NetTypes:    netTypes,
		Console:     consolePath,
		UEFIBoot:    uefiBoot,
		NATEnabled:  natEnabled,
	}

	// Create per-VM writable UEFI vars file for NVRAM persistence
	// Without this, bhyve shares read-only firmware vars and UEFI
	// cannot save boot entries discovered from the disk.
	if uefiBoot && p.hasUEFI {
		varsPath := filepath.Join(vmDir, "uefi_vars.fd")
		config.UEFIVars = varsPath
		if err := p.copyUEFIVars(varsPath); err != nil {
			cleanup()
			return provider.InstanceHandle{}, fmt.Errorf("failed to create UEFI vars: %w", err)
		}
	}

	// Extract additional settings from ProviderConfig
	if err := p.applyProviderConfig(config, spec, vmName); err != nil {
		cleanup()
		return provider.InstanceHandle{}, err
	}

	// Derive TPM socket path from vmDir so it survives config round-trips
	if config.TPMEnabled {
		config.TPMSockPath = filepath.Join(vmDir, "tpm.sock")
	}

	// Set up VLAN tagging for tap devices that have a VLAN ID assigned.
	for i, tapDev := range tapDevs {
		if i < len(config.VLANIDs) && config.VLANIDs[i] != 0 {
			if err := p.setupVLANForTap(ctx, tapDev, config.VLANIDs[i]); err != nil {
				cleanup()
				return provider.InstanceHandle{}, fmt.Errorf("failed to setup VLAN %d for tap %s: %w", config.VLANIDs[i], tapDev, err)
			}
		}
	}

	// Set up IPv6 NAT if requested (requires NAT mode).
	if config.IPv6Enabled && natEnabled {
		if err := p.initFirewall(ctx); err != nil {
			cleanup()
			return provider.InstanceHandle{}, fmt.Errorf("failed to init firewall for IPv6 NAT: %w", err)
		}
		if err := p.setupIPv6NATForVM(ctx, vmName, config.IPv6Prefix); err != nil {
			cleanup()
			return provider.InstanceHandle{}, fmt.Errorf("failed to setup IPv6 NAT: %w", err)
		}
	}

	if err := p.saveVMConfig(vmDir, config); err != nil {
		cleanup()
		return provider.InstanceHandle{}, fmt.Errorf("failed to save VM config: %w", err)
	}

	// Create state file
	state := &vmState{
		Name:    vmName,
		CPUs:    spec.CPUs,
		Memory:  spec.MemoryMB,
		State:   provider.StateStopped,
		Console: consolePath,
	}
	if err := p.saveVMState(vmDir, state); err != nil {
		cleanup()
		return provider.InstanceHandle{}, fmt.Errorf("failed to save VM state: %w", err)
	}

	return provider.InstanceHandle{
		ID:       vmName,
		Provider: "bhyve",
		Metadata: map[string]interface{}{
			"name": vmName,
			"path": vmDir,
		},
	}, nil
}

// DeleteInstance deletes a bhyve VM
func (p *BhyveProvider) DeleteInstance(ctx context.Context, handle provider.InstanceHandle, force bool) (err error) {
	// Attach provider context so the API layer can tell a failed delete from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("bhyve", "delete", handle.ID, err) }()
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}

	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()

	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Load config before deleting directory (need tap devices list)
	config, configErr := p.loadVMConfig(vmDir)

	// Stop VM if running
	state, err := p.GetInstanceState(ctx, handle)
	if err == nil && state == provider.StateRunning {
		if err := p.StopInstance(ctx, handle, provider.StopOptions{Force: force}); err != nil {
			slog.Warn("Failed to stop VM before delete",
				logging.FieldVM, vmName,
				logging.FieldError, err)
		}
	}

	// Destroy VM (ignore errors if VM doesn't exist)
	if err := p.cmd().Run(ctx, "bhyvectl", "--destroy", fmt.Sprintf("--vm=%s", vmName)); err != nil {
		// Only log if this isn't just a "VM doesn't exist" error
		slog.Info("bhyvectl destroy result",
			logging.FieldVM, vmName,
			logging.FieldError, err)
	}

	// Kill any orphaned bhyve process still holding tap FDs open.
	// bhyvectl --destroy normally takes care of this, but it can fail if the
	// VM object no longer exists in the kernel (e.g. after a daemon restart).
	p.killOrphanBhyveProcess(ctx, vmName)

	// Kill any lingering swtpm process for this VM
	if configErr == nil && config.TPMEnabled {
		p.stopSwtpm(vmName, vmDir)
	}

	// Remove NAT firewall rules if this VM was using NAT networking
	if configErr == nil && config.NATEnabled {
		p.teardownNATForVM(ctx, vmName)
		if config.IPv6Enabled {
			p.teardownIPv6NATForVM(ctx, vmName)
		}
	}

	// Destroy VLAN interfaces for each tap
	if configErr == nil {
		for i, tap := range config.TapDevs {
			if i < len(config.VLANIDs) && config.VLANIDs[i] != 0 {
				p.teardownVLANForTap(ctx, tap, config.VLANIDs[i])
			}
		}
	}

	// Destroy tap devices (best-effort, loaded from config)
	if configErr == nil {
		for _, tap := range config.TapDevs {
			if tap == "" {
				continue
			}
			if err := p.destroyTapSafe(ctx, tap); err != nil {
				slog.Warn("Failed to destroy tap device",
					logging.FieldVM, vmName,
					"tap", tap,
					logging.FieldError, err)
			}
		}
	}

	// Destroy ZFS datasets (best-effort)
	zvolParent := fmt.Sprintf("%s/%s", p.zfsParent, vmName)
	if err := p.cmd().Run(ctx, "zfs", "destroy", "-r", zvolParent); err != nil {
		slog.Warn("Failed to destroy ZFS dataset",
			logging.FieldVM, vmName,
			"dataset", zvolParent,
			logging.FieldError, err)
	}

	// Remove VM directory
	if err := os.RemoveAll(vmDir); err != nil {
		return fmt.Errorf("failed to remove VM directory: %w", err)
	}

	return nil
}

// StartInstance starts a bhyve VM
func (p *BhyveProvider) StartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	// Attach provider context so the API layer can tell a failed start from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("bhyve", "start", handle.ID, err) }()
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}

	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()

	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Load VM configuration
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	// Merge any runtime passthrough devices registered via AttachPassthroughDevice
	// so they are emitted on the bhyve command line and guest memory is wired.
	if err := p.mergeRuntimePassthrough(vmDir, config); err != nil {
		return fmt.Errorf("failed to apply passthrough devices: %w", err)
	}

	// The configuration on disk is not necessarily one CreateInstance wrote:
	// it may have been imported, or edited. Check what it hands the guest
	// before handing it, rather than trusting the file.
	if err := p.validateRuntimeConfig(config); err != nil {
		return err
	}

	// Check if already running
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return err
	}
	if state == provider.StateRunning {
		return fmt.Errorf("VM is already running")
	}

	// Start swtpm TPM daemon before launching bhyve
	if config.TPMEnabled {
		if err := p.startSwtpm(ctx, vmName, vmDir, config); err != nil {
			return fmt.Errorf("failed to start swtpm: %w", err)
		}
		// A start that fails further down must not leave the daemon behind: it
		// holds the TPM state lock, and every retry would stack one more swtpm
		// on a socket no VM will ever read.
		defer func() {
			if err != nil {
				p.stopSwtpm(vmName, vmDir)
			}
		}()
	}

	// Recreate the tap devices the config records. tap(4) interfaces are host
	// state a reboot takes away, and create is the only other place that makes
	// them, so a VM that ran yesterday would otherwise fail here: NAT reports
	// "BRDGADD tap_x_0: No such file or directory" and bhyve cannot open a tap
	// that is not there.
	if err := p.restoreTapDevices(ctx, config); err != nil {
		return err
	}

	// Set up VLAN tagging for tap devices that have a VLAN ID assigned.
	// This creates vlan(4) sub-interfaces and per-VLAN bridges at every start
	// because ifconfig state is not persisted across reboots.
	for i, tapDev := range config.TapDevs {
		if i < len(config.VLANIDs) && config.VLANIDs[i] != 0 {
			if err := p.setupVLANForTap(ctx, tapDev, config.VLANIDs[i]); err != nil {
				return fmt.Errorf("failed to setup VLAN %d for tap %s: %w", config.VLANIDs[i], tapDev, err)
			}
		}
	}

	// Re-apply IPv4 NAT, for the same reason as the VLANs above: the NAT bridge,
	// the PF rules and dnsmasq are host state that a reboot takes away, while
	// creation is the only other place that sets them up. Without this a NAT VM
	// starts onto a bridge that no longer exists, with no DHCP to answer it.
	// The IPv6 half is re-applied just below.
	if config.NATEnabled {
		for _, tapDev := range config.TapDevs {
			if err := p.setupNATForVM(ctx, vmName, tapDev); err != nil {
				return fmt.Errorf("failed to restore NAT for tap %s: %w", tapDev, err)
			}
		}
	}

	// Re-enable IPv6 NAT if configured (bridge IPv6 addr and PF rules must be re-applied after restart).
	if config.IPv6Enabled && config.NATEnabled {
		if err := p.initFirewall(ctx); err != nil {
			return fmt.Errorf("failed to init firewall for IPv6 NAT: %w", err)
		}
		if err := p.setupIPv6NATForVM(ctx, vmName, config.IPv6Prefix); err != nil {
			return fmt.Errorf("failed to setup IPv6 NAT: %w", err)
		}
	}

	// Give every NIC an address hospitus knows, before the command is built. A VM
	// created before this has none recorded, so it gets one here and keeps it.
	if assigned, macErr := ensureNICMACs(config); macErr != nil {
		return fmt.Errorf("failed to assign a MAC address: %w", macErr)
	} else if assigned {
		if err := p.saveVMConfig(vmDir, config); err != nil {
			return fmt.Errorf("failed to record the NIC addresses: %w", err)
		}
	}

	// Build bhyve command
	args, err := p.buildBhyveArgs(config)
	if err != nil {
		return fmt.Errorf("failed to build bhyve arguments: %w", err)
	}

	// Record what we are about to run. bhyve replaces its argv with the title
	// "bhyve: <name>", so ps and procstat show nothing afterwards: a VM that
	// starts wrong — a passthru device that never made it onto the line, a
	// disk on the wrong driver — leaves no trace of the command behind it.
	// Debug for the same reason as the jail argv: invaluable when a VM boots
	// into nothing — it is what showed a passthru device landing in the wrong
	// slot and a UEFI guest handed a virtio-blk it cannot read — and noise in
	// the terminal of someone starting a VM.
	slog.Debug("executing bhyve start command",
		logging.FieldVM, vmName, "args", strings.Join(args, " "))

	// Start bhyve in background
	cmd := exec.Command("bhyve", args...)
	cmd.Dir = vmDir

	// Create log file
	logPath := filepath.Join(vmDir, "bhyve.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("failed to create log file: %w", err)
	}
	// NOTE: do NOT defer logFile.Close() here — bhyve keeps writing after
	// StartInstance returns. Close only after the process exits.

	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("failed to start bhyve: %w", err)
	}

	// Persist the running state BEFORE launching the monitor goroutine. If the
	// order were reversed, a bhyve that exits immediately could have its monitor
	// reconcile the state to "stopped" and then be overwritten by this "running"
	// write, leaving a dead VM marked running. The per-VM state mutex serializes
	// this write against the goroutine's reconciliation.
	vmState := &vmState{
		Name:    vmName,
		CPUs:    config.CPUs,
		Memory:  config.MemoryMB,
		State:   provider.StateRunning,
		PID:     cmd.Process.Pid,
		Console: config.Console,
	}
	p.stateMu.Lock()
	saveErr := p.saveVMState(vmDir, vmState)
	p.stateMu.Unlock()
	if saveErr != nil {
		// State save failed — kill the orphaned bhyve process to avoid leaking it
		if killErr := syscall.Kill(cmd.Process.Pid, syscall.SIGKILL); killErr != nil && killErr != syscall.ESRCH {
			slog.Warn("Failed to kill orphaned bhyve process after state save failure",
				logging.FieldVM, vmName,
				"pid", cmd.Process.Pid,
				logging.FieldError, killErr)
		}
		logFile.Close()
		return fmt.Errorf("failed to save VM state: %w", saveErr)
	}

	// Watch the process: relaunch it when the guest asked for a reboot, and
	// otherwise close the log, release the vmm object and reconcile the
	// persisted state so a crashed VM does not remain "running" until the next
	// GetInstanceState poll.
	exited := make(chan error, 1)
	go p.superviseBhyve(vmName, vmDir, args, cmd, logFile, exited)

	// Give bhyve long enough to reject its own configuration before calling
	// this a success.
	//
	// bhyve rejects a configuration it cannot run within milliseconds — a host
	// without the UEFI firmware writes "no bootrom was configured" and exits 4.
	// A VM that boots is still running after this wait; one that cannot has
	// already gone, and start reports the failure rather than a success.
	select {
	case waitErr := <-exited:
		return fmt.Errorf("bhyve exited immediately: %w%s", waitErr, bhyveLogTail(vmDir))
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(bhyveStartGrace):
	}

	return nil
}

// bhyveRebootExitCode is what bhyve(8) returns when the guest asked to restart.
// Powering off is 1, halting 2, a triple fault 3; only 0 means "start me
// again".
const bhyveRebootExitCode = 0

// bhyveRebootBudget bounds how many restarts are honored inside
// bhyveRebootWindow, so a guest resetting in a tight loop does not spin.
const (
	bhyveRebootBudget = 5
	bhyveRebootWindow = time.Minute
)

// superviseBhyve owns a running bhyve process for the life of the VM.
//
// bhyve does not reboot a guest itself: it exits 0 and expects its supervisor to
// destroy the vmm object and run it again. Without that, a reboot inside the
// guest left the VM switched off with its /dev/vmm entry still allocated.
//
// The first exit is reported on firstExit so StartInstance can tell a
// configuration bhyve rejects from a VM that is running.
func (p *BhyveProvider) superviseBhyve(vmName, vmDir string, args []string, cmd *exec.Cmd, logFile *os.File, firstExit chan<- error) {
	reportedFirst := false
	var rebootTimes []time.Time

	for {
		waitErr := cmd.Wait()
		if !reportedFirst {
			firstExit <- waitErr
			reportedFirst = true
		}

		// The vmm object outlives the process, holding the guest's wired
		// memory; the next start fails with "Device busy" until it is gone.
		p.destroyVMMObject(vmName)

		if waitErr != nil {
			slog.Warn("bhyve process exited abnormally",
				logging.FieldVM, vmName, logging.FieldError, waitErr)
		} else {
			slog.Info("bhyve process exited", logging.FieldVM, vmName)
		}

		if !p.shouldRelaunchAfterExit(vmName, vmDir, waitErr, &rebootTimes) {
			logFile.Close()
			p.markStoppedAfterExit(vmName, vmDir)
			return
		}

		slog.Info("guest requested a reboot; restarting bhyve", logging.FieldVM, vmName)
		next := exec.Command("bhyve", args...)
		next.Dir = vmDir
		next.Stdout = logFile
		next.Stderr = logFile
		if err := next.Start(); err != nil {
			slog.Error("could not restart the VM after a guest reboot",
				logging.FieldVM, vmName, logging.FieldError, err)
			logFile.Close()
			p.markStoppedAfterExit(vmName, vmDir)
			return
		}

		p.stateMu.Lock()
		if st, err := p.loadVMState(vmDir); err == nil {
			st.State = provider.StateRunning
			st.PID = next.Process.Pid
			if err := p.saveVMState(vmDir, st); err != nil {
				slog.Warn("could not record the PID of the restarted VM",
					logging.FieldVM, vmName, logging.FieldError, err)
			}
		}
		p.stateMu.Unlock()

		cmd = next
	}
}

// shouldRelaunchAfterExit reports whether this exit was a guest reboot that
// should bring the VM back, rather than a shutdown, a crash, or a stop the
// operator asked for.
func (p *BhyveProvider) shouldRelaunchAfterExit(vmName, vmDir string, waitErr error, rebootTimes *[]time.Time) bool {
	if !isBhyveRebootExit(waitErr) {
		return false
	}

	// A stop, a delete or a restart already moved the record off "running".
	p.stateMu.Lock()
	st, err := p.loadVMState(vmDir)
	p.stateMu.Unlock()
	if err != nil || st.State != provider.StateRunning {
		return false
	}

	now := time.Now()
	recent := make([]time.Time, 0, len(*rebootTimes)+1)
	for _, t := range *rebootTimes {
		if now.Sub(t) < bhyveRebootWindow {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	*rebootTimes = recent
	if len(*rebootTimes) > bhyveRebootBudget {
		slog.Error("guest rebooted too often; leaving it stopped",
			logging.FieldVM, vmName,
			"reboots", len(*rebootTimes), "within", bhyveRebootWindow)
		return false
	}
	return true
}

// isBhyveRebootExit reports whether a finished bhyve asked to be started again.
func isBhyveRebootExit(waitErr error) bool {
	if waitErr == nil {
		// Exit status 0.
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode() == bhyveRebootExitCode
	}
	return false
}

// markStoppedAfterExit reconciles the persisted state once the VM is really
// down.
func (p *BhyveProvider) markStoppedAfterExit(vmName, vmDir string) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	st, err := p.loadVMState(vmDir)
	if err != nil {
		return
	}
	if st.State != provider.StateRunning && st.State != provider.StatePaused {
		return
	}
	st.State = provider.StateStopped
	st.PID = 0
	if err := p.saveVMState(vmDir, st); err != nil {
		slog.Warn("Failed to reconcile VM state after bhyve exit",
			logging.FieldVM, vmName, logging.FieldError, err)
	}
}

// destroyVMMObject releases /dev/vmm/<name>. It is best-effort: destroying one
// that is already gone is not worth reporting.
func (p *BhyveProvider) destroyVMMObject(vmName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = p.cmd().Run(ctx, "bhyvectl", "--destroy", fmt.Sprintf("--vm=%s", vmName))
}

// bhyveStartGrace is how long StartInstance waits before reporting success.
// bhyve rejects a configuration it cannot use immediately; this is far longer
// than that and far shorter than a boot.
const bhyveStartGrace = 750 * time.Millisecond

// bhyveLogTail returns the last line bhyve wrote, for an error message.
//
// The exit status alone says nothing useful — "exit status 4" leaves the
// reader to go looking for the log — while the line above it usually names the
// problem outright.
func bhyveLogTail(vmDir string) string {
	data, err := os.ReadFile(filepath.Join(vmDir, "bhyve.log"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return ""
	}
	return " (" + last + ")"
}

// defaultCloudImageDiskGB is the root disk a VM gets when it is created from a
// cloud image without one being asked for. It matches the default a manifest
// declares in storage.root_disk, so the two paths produce the same VM.
const defaultCloudImageDiskGB = 20

// StopInstance stops a bhyve VM
func (p *BhyveProvider) StopInstance(ctx context.Context, handle provider.InstanceHandle, opts provider.StopOptions) (err error) {
	// Attach provider context so the API layer can tell a failed stop from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("bhyve", "stop", handle.ID, err) }()
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}

	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()

	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	state, err := p.loadVMState(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM state: %w", err)
	}

	// Ask the host, not just the record: either can be stale in both
	// directions after a crash or a host reboot.
	actual, stateErr := p.GetInstanceState(ctx, handle)
	if stateErr == nil {
		// GetInstanceState reconciles the record against the host and writes a
		// corrected PID back to disk. The copy read above predates that, so
		// everything below would act on the PID the reconciliation just
		// replaced — the recovery case it exists for.
		reloaded, reloadErr := p.loadVMState(vmDir)
		if reloadErr != nil {
			// Falling back to the stale copy is the very failure this reload
			// exists to prevent: it would signal the old PID, or save a stopped
			// record while bhyve is still running.
			return fmt.Errorf("failed to reload VM state after reconciliation: %w", reloadErr)
		}
		state = reloaded
		switch actual {
		case provider.StateStopped:
			// Already down: the caller got what it asked for. Reporting an
			// error here made every rollback and retry look like a failure.
			return nil
		case provider.StatePaused:
			// A paused VM must be resumed before it can act on the signals.
			if state.PID > 0 && p.pidIsBhyveVM(ctx, state.PID, vmName) {
				if contErr := syscall.Kill(state.PID, syscall.SIGCONT); contErr != nil && contErr != syscall.ESRCH {
					slog.Warn("could not resume a paused VM before stopping it",
						logging.FieldVM, vmName, logging.FieldError, contErr)
				}
			}
		}
	}

	// Graceful shutdown (if not force): SIGTERM to the bhyve process triggers an
	// ACPI power-button event to the guest, letting it shut down cleanly. Wait up
	// to opts.Timeout for the process to exit before escalating to a force kill.
	// The previous "--force-poweroff" is an immediate hard poweroff and ignored
	// opts.Timeout entirely.
	if !opts.Force {
		if !p.pidIsBhyveVM(ctx, state.PID, vmName) {
			// Nothing of ours to ask nicely; skip to the teardown below, which
			// destroys the VM object without signaling anyone.
			opts.Force = true
		} else if err := syscall.Kill(state.PID, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
			slog.Warn("Graceful shutdown signal failed, escalating to force kill", "vm", vmName, logging.FieldError, err)
			opts.Force = true
		} else {
			timeout := opts.Timeout
			if timeout <= 0 {
				timeout = defaultGracefulStopTimeout
			}
			if !waitForProcessExit(ctx, state.PID, timeout) {
				slog.Warn("Graceful shutdown timed out, escalating to force kill", "vm", vmName, "timeout", timeout)
				opts.Force = true
			}
		}
	}

	// Force stop if requested or graceful failed
	if opts.Force {
		// bhyvectl --destroy exits non-zero when the VM object is already gone
		// from the kernel — the ordinary case when bhyve has exited on its own.
		// Returning here abandoned the force stop before it killed the process
		// or wrote the stopped state, which is precisely what force is for.
		// DeleteInstance and the cleanup below both treat this as best-effort.
		if err := p.cmd().Run(ctx, "bhyvectl", "--destroy", fmt.Sprintf("--vm=%s", vmName)); err != nil {
			slog.Info("bhyvectl destroy result", logging.FieldVM, vmName, logging.FieldError, err)
		}

		// Kill the process only while it is still the one we started.
		if p.pidIsBhyveVM(ctx, state.PID, vmName) {
			if err := syscall.Kill(state.PID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
				slog.Warn("Failed to kill process for VM",
					logging.FieldVM, vmName,
					"pid", state.PID,
					logging.FieldError, err)
			}
		}
	}

	// Release the kernel's VM object now the process is gone.
	//
	// bhyve exiting on its own leaves /dev/vmm/<name> behind, and the next
	// start fails on it with "bhyve: vm_openf: Device busy".
	//
	// Best-effort: on the force path it is already destroyed, and destroying
	// something twice is not worth reporting.
	_ = p.cmd().Run(ctx, "bhyvectl", "--destroy", fmt.Sprintf("--vm=%s", vmName))

	// Update state
	state.State = provider.StateStopped
	state.PID = 0
	if err := p.saveVMState(vmDir, state); err != nil {
		return fmt.Errorf("failed to save VM state: %w", err)
	}

	// Stop swtpm if it was started for this VM
	config, configErr := p.loadVMConfig(vmDir)
	if configErr == nil && config.TPMEnabled {
		p.stopSwtpm(vmName, vmDir)
	}

	return nil
}

// RestartInstance restarts a bhyve VM
func (p *BhyveProvider) RestartInstance(ctx context.Context, handle provider.InstanceHandle) (err error) {
	// Attach provider context so the API layer can tell a failed restart from an
	// internal fault, and show the caller why it failed.
	defer func() { err = provider.WrapError("bhyve", "restart", handle.ID, err) }()
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}

	ctx, releaseLock, lockErr := p.locks.Acquire(ctx, handle.ID)
	if lockErr != nil {
		return lockErr
	}
	defer releaseLock()

	if err := p.StopInstance(ctx, handle, provider.StopOptions{Timeout: 30 * time.Second}); err != nil {
		return fmt.Errorf("failed to stop VM: %w", err)
	}

	if err := p.StartInstance(ctx, handle); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	return nil
}

// GetInstanceState returns the current state of a VM
func (p *BhyveProvider) GetInstanceState(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceState, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return "", fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	state, err := p.loadVMState(vmDir)
	if err != nil {
		return provider.StateUnknown, fmt.Errorf("failed to load VM state: %w", err)
	}

	// Reconcile the record against the host, in both directions. A record that
	// says running is trusted only while its process is; a record that says
	// stopped is trusted only while no process for the VM exists.
	//
	// Both directions matter. Reconciling one way makes a misread permanent:
	// once "stopped" is written the VM is never looked for again, so a running
	// VM has no stop that finds it, no destroy that reaches it, and a next
	// start that collides with the process still holding /dev/vmm.
	switch {
	case (state.State == provider.StateRunning || state.State == provider.StatePaused) && state.PID > 0:
		// A SIGSTOP'd process still exists, so this covers paused VMs too.
		if !p.pidIsBhyveVM(ctx, state.PID, vmName) {
			state.State = provider.StateStopped
			state.PID = 0
			p.rememberVMState(vmDir, vmName, state)
		}

	case state.State == provider.StateStopped:
		if pids := p.bhyvePIDsForVM(ctx, vmName); len(pids) > 0 {
			slog.Info("VM is running although its record said stopped; correcting the record",
				logging.FieldVM, vmName, "pid", pids[0])
			state.State = provider.StateRunning
			state.PID = pids[0]
			p.rememberVMState(vmDir, vmName, state)
		}
	}

	return state.State, nil
}

// rememberVMState writes a corrected record back. A failure here is worth
// saying but not worth failing the query for: the answer just given is right,
// and the next call recomputes it.
func (p *BhyveProvider) rememberVMState(vmDir, vmName string, state *vmState) {
	if err := p.saveVMState(vmDir, state); err != nil {
		slog.Warn("Failed to save updated VM state",
			logging.FieldVM, vmName,
			logging.FieldError, err)
	}
}

// allowedPhysicalDisks returns the operator-configured list of host devices
// permitted for passthrough (provider setting "allowed_physical_disks"). An
// empty list means passthrough is denied.
func (p *BhyveProvider) allowedPhysicalDisks() []string {
	switch v := p.config.Settings["allowed_physical_disks"].(type) {
	case []string:
		return v
	case []interface{}:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return strings.Fields(v)
	}
	return nil
}

// physicalDiskAllowed reports whether a device (by its literal path or its
// symlink-resolved form) is on the allow-list. Default-deny: an empty list
// permits nothing.
func physicalDiskAllowed(allowed []string, candidate, resolvedCandidate string) bool {
	for _, a := range allowed {
		if a == candidate || a == resolvedCandidate {
			return true
		}
	}
	return false
}

// bhyveDiskType classifies a bhyve disk path: ZFS volumes, raw physical device
// passthrough, or an image file.
func bhyveDiskType(path string) provider.DiskType {
	switch {
	case strings.HasPrefix(path, "/dev/zvol/"):
		return provider.DiskTypeZVOL
	case strings.HasPrefix(path, "/dev/"):
		return provider.DiskTypePhysical
	default:
		return provider.DiskTypeRaw
	}
}

// GetInstanceInfo returns detailed information about a VM
func (p *BhyveProvider) GetInstanceInfo(ctx context.Context, handle provider.InstanceHandle) (provider.InstanceInfo, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return provider.InstanceInfo{}, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Load VM configuration
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return provider.InstanceInfo{}, fmt.Errorf("failed to load VM config: %w", err)
	}

	state, err := p.loadVMState(vmDir)
	if err != nil {
		return provider.InstanceInfo{}, fmt.Errorf("failed to load VM state: %w", err)
	}

	spec := provider.InstanceSpec{
		Name:     vmName,
		CPUs:     config.CPUs,
		MemoryMB: config.MemoryMB,
	}
	// Populate disks and networks from the loaded config so callers that read
	// back the spec — notably CloneInstance/CloneFromSnapshot — see the VM's
	// actual storage and NICs instead of an empty spec.
	for i, path := range config.DiskPaths {
		d := provider.DiskSpec{Path: path, Type: bhyveDiskType(path)}
		if i < len(config.DiskDrivers) {
			d.DeviceName = config.DiskDrivers[i]
		}
		// Ask ZFS for the size. vm.conf records paths and not sizes, so the
		// disks reported here had none, and cloning a VM built the new volume
		// from that zero: "cannot create ...: volume size cannot be zero", on
		// a VM whose disk was plainly twenty gigabytes.
		if d.Type == provider.DiskTypeZVOL {
			d.SizeGB = p.zvolSizeGB(ctx, strings.TrimPrefix(path, "/dev/zvol/"))
		}
		spec.Disks = append(spec.Disks, d)
	}
	// CloneInstance and CloneFromSnapshot read this spec back, and CreateInstance
	// switches on Type. Reporting every NIC as a bridge with an empty Bridge sent
	// a NAT VM into the bridge branch, where it got neither NAT nor a bridge —
	// the config already records what each tap really is.
	for i, tap := range config.TapDevs {
		n := provider.NetworkSpec{ID: tap, Type: provider.NetworkTypeBridge}
		if i < len(config.NetTypes) && config.NetTypes[i] != "" {
			n.Type = provider.NetworkType(config.NetTypes[i])
		} else if config.NATEnabled {
			// Written before net_types existed: NATEnabled is all such a config
			// records, so a mixed VM reads back as entirely NAT.
			n.Type = provider.NetworkTypeNAT
		}
		if i < len(config.Bridges) && config.Bridges[i] != "" {
			n.Bridge = config.Bridges[i]
		}
		if i < len(config.VLANIDs) {
			n.VLAN = config.VLANIDs[i]
		}
		if i < len(config.NICMACs) {
			n.MAC = config.NICMACs[i]
		}
		spec.Networks = append(spec.Networks, n)
	}

	info := provider.InstanceInfo{
		Handle: handle,
		Spec:   spec,
		State:  state.State,
		PID:    state.PID,
	}

	if ips, _ := p.lookupIPsByVM(ctx, vmName); len(ips) > 0 {
		info.IPAddresses = ips
	}

	return info, nil
}

// cleanupZVOLs destroys a list of ZFS volumes created during a failed CreateInstance.
func (p *BhyveProvider) cleanupZVOLs(ctx context.Context, zvols []string) {
	for _, zvol := range zvols {
		if err := p.cmd().Run(ctx, "zfs", "destroy", zvol); err != nil {
			slog.Error("Failed to cleanup ZVOL after create failure — dataset may be orphaned",
				"zvol", zvol, "error", err)
		}
	}
}

// cleanupTaps destroys a list of tap devices created during a failed CreateInstance.
func (p *BhyveProvider) cleanupTaps(ctx context.Context, taps []string) {
	for _, tap := range taps {
		_ = p.destroyTapSafe(ctx, tap) // best-effort cleanup of orphaned taps
	}
}

// destroyTapSafe removes a tap device from all bridges and then destroys it.
//
// On FreeBSD, ifconfig <tap> destroy hangs indefinitely when:
//   - The tap is still a member of a bridge
//   - A bhyve process still holds the tap file descriptor open
//
// This helper scans all bridges for the tap and removes it from each one
// before destroying. Callers should kill the bhyve process first (via
// bhyvectl --destroy or SIGKILL) so the file descriptor is released.
func (p *BhyveProvider) destroyTapSafe(ctx context.Context, tap string) error {
	// Find and remove the tap from every bridge it belongs to.
	// ifconfig -a lists all interfaces; bridge members appear as "member: <iface>" lines.
	if out, err := p.cmd().Output(ctx, "ifconfig", "-a"); err == nil {
		var currentBridge string
		for _, line := range strings.Split(string(out), "\n") {
			// Interface header: "hospitus0: flags=..."
			if line != "" && line[0] != ' ' && line[0] != '\t' {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) >= 1 {
					currentBridge = strings.TrimSpace(parts[0])
				}
				continue
			}
			// Bridge member line: "\tmember: tap_foo_0 flags=..."
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "member: "+tap) {
				if out, err := p.cmd().CombinedOutput(ctx, "ifconfig", currentBridge, "deletem", tap); err != nil {
					slog.Warn("failed to remove tap from bridge during cleanup",
						"tap", tap, "bridge", currentBridge,
						logging.FieldError, err, "output", strings.TrimSpace(string(out)))
				}
			}
		}
	}

	if err := p.cmd().Run(ctx, "ifconfig", tap, "destroy"); err != nil {
		slog.Error("Failed to destroy tap device — interface may be orphaned",
			"tap", tap, logging.FieldError, err)
		return err
	}
	return nil
}

// commandIsBhyveVM reports whether a process is the bhyve process running
// vmName. pgrep -f matches any VM whose command line merely contains vmName as
// a substring — as a prefix of another VM's name, or inside a disk path — so
// callers must confirm the process before signaling it.
//
// bhyve is seen in two shapes, and only the second one lasts. It is executed
// with the VM name as its final argument:
//
//	bhyve -m 4096M ... -s 31,lpc diag-vm
//
// and then replaces its argv with a title, which is what ps reports for every
// running VM:
//
//	bhyve: diag-vm (bhyve)
//
// Matching only the final argument therefore answers "not my VM" for every VM
// that is actually running. That answer does not just misreport state: the
// caller writes "stopped" back to the state file and clears the PID, so a live
// VM is lost — no stop finds it, no destroy reaches it, and the next start
// collides with the one still holding /dev/vmm.
func commandIsBhyveVM(command, vmName string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 || !strings.Contains(strings.ToLower(fields[0]), "bhyve") {
		return false
	}

	// Title form: the name follows "bhyve:", and "(bhyve)" trails it.
	if fields[0] == "bhyve:" {
		return len(fields) > 1 && fields[1] == vmName
	}

	// Command-line form, before bhyve sets its title.
	return fields[len(fields)-1] == vmName
}

// pidIsBhyveVM reports whether pid is still the bhyve process running vmName.
//
// A PID recorded when the VM started is not a lasting identity: once bhyve
// exits the number is free, and the kernel hands it to whatever starts next.
// Acting on it unchecked signals a stranger — SIGKILL from a force stop landed
// on an unrelated process, and a signal-0 liveness probe reported a VM as
// running because something else had inherited its number.
func (p *BhyveProvider) pidIsBhyveVM(ctx context.Context, pid int, vmName string) bool {
	if pid <= 0 {
		return false
	}
	out, err := p.cmd().Output(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=")
	if err != nil {
		return false
	}
	return commandIsBhyveVM(string(out), vmName)
}

// bhyvePIDsForVM returns the PIDs of bhyve processes belonging exactly to
// vmName, filtering pgrep's substring matches through the process command line.
func (p *BhyveProvider) bhyvePIDsForVM(ctx context.Context, vmName string) []int {
	// SECURITY: QuoteMeta prevents regex injection via VM names containing regex metacharacters
	out, err := p.cmd().Output(ctx, "pgrep", "-f", fmt.Sprintf("bhyve.*%s", regexp.QuoteMeta(vmName)))
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid <= 0 {
			continue
		}
		cmdOut, err := p.cmd().Output(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=")
		if err != nil {
			continue
		}
		if commandIsBhyveVM(string(cmdOut), vmName) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// defaultGracefulStopTimeout is used when StopOptions.Timeout is unset (<= 0).
const defaultGracefulStopTimeout = 30 * time.Second

// waitForProcessExit polls signal-0 on pid until it disappears (ESRCH) or the
// timeout/context elapses. It returns true if the process exited within the
// window, false otherwise. Uses a 100ms poll so a fast shutdown is noticed
// promptly without busy-waiting.
func waitForProcessExit(ctx context.Context, pid int, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-ticker.C:
		}
	}
}

// pidIsCommand reports whether pid is still held by the named program.
//
// Signal 0 only proves some process owns the number; once the original exits,
// the kernel hands it to whatever starts next.
func (p *BhyveProvider) pidIsCommand(ctx context.Context, pid int, want string) bool {
	if pid <= 0 {
		return false
	}
	out, err := p.cmd().Output(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "command=")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(out))
	// Exact: a substring match would take "swtpm-wrapper", or anything else
	// whose name merely contains it, for the process we recorded.
	return len(fields) > 0 && filepath.Base(fields[0]) == want
}

// killOrphanBhyveProcess kills any bhyve process for the given VM name.
// This is needed when bhyvectl --destroy fails (VM not in kernel) but the
// bhyve process is still running and holding tap FDs open.
func (p *BhyveProvider) killOrphanBhyveProcess(ctx context.Context, vmName string) {
	// Bound pgrep with a timeout so a hung process table cannot block the
	// caller (e.g. DeleteInstance) indefinitely.
	pgrepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, pid := range p.bhyvePIDsForVM(pgrepCtx, vmName) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// startSwtpm starts a software TPM daemon for the given VM and waits for its socket.
//
// swtpm(8) provides a TPM 2.0 emulation backend that bhyve connects to via a
// UNIX domain socket using the "-l tpm,swtpm,<path>" LPC device argument.
//
// On first use the TPM state must be initialized with swtpm_setup(8):
//
//	swtpm_setup --tpm2 --tpmstate <vmDir>/tpm
//
// The daemon is then started with the --server data socket:
//
//	swtpm socket --tpm2 --tpmstate dir=<vmDir>/tpm \
//	  --server type=unixio,path=<vmDir>/tpm.sock \
//	  --pid file=<vmDir>/swtpm.pid --daemon
//
// Why --server and not --ctrl?
// bhyve's tpm_swtpm_execute_cmd() sends raw TPM2 command bytes via send() and
// expects raw TPM2 response bytes via recv(). The --ctrl socket implements the
// PTM (Pass-Through Management) protocol used by QEMU, which wraps commands in
// PTM headers; sending raw bytes to a --ctrl socket returns a 4-byte PTM error,
// causing bhyve to log "tpm_swtpm_execute_cmd: rsp read failed (bytes read: 4)".
// The --server socket accepts raw TPM2 bytes and is the correct interface for bhyve.
// swtpmSocketTimeout bounds how long startSwtpm waits for the swtpm UNIX socket
// to appear; swtpmPollInterval is how often it re-checks in the meantime.
const (
	swtpmSocketTimeout = 3 * time.Second
	swtpmPollInterval  = 50 * time.Millisecond
)

func (p *BhyveProvider) startSwtpm(ctx context.Context, vmName, vmDir string, config *vmConfig) error {
	if !p.hasSwtpm {
		return fmt.Errorf("swtpm not found — install it with: pkg install swtpm")
	}

	tpmStateDir := filepath.Join(vmDir, "tpm")
	if err := os.MkdirAll(tpmStateDir, 0o700); err != nil {
		return fmt.Errorf("failed to create TPM state dir: %w", err)
	}

	// Initialize TPM state on first use.
	// swtpm_setup creates the NVRAM and EK keys; without it swtpm starts but
	// immediately fails on the first TPM2 command, causing bhyve to log
	// "tpm_swtpm_execute_cmd: rsp read failed".
	entries, err := os.ReadDir(tpmStateDir)
	if err != nil || len(entries) == 0 {
		if p.swtpmSetupPath == "" {
			return fmt.Errorf("swtpm_setup not found — install it with: pkg install swtpm")
		}
		if out, err := p.cmd().CombinedOutput(ctx, p.swtpmSetupPath, "--tpm2", "--tpmstate", tpmStateDir, "--createek", "--allow-signing", "--decryption", "--create-ek-cert", "--create-platform-cert", "--lock-nvram"); err != nil {
			return fmt.Errorf("swtpm_setup failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
		}
		slog.Info("swtpm TPM state initialized", logging.FieldVM, vmName, "dir", tpmStateDir)
	}

	sockPath := config.TPMSockPath
	pidFile := filepath.Join(vmDir, "swtpm.pid")

	// Stop a swtpm left over from an earlier start of this VM. Removing its
	// socket below would otherwise strand it: it keeps running, holding the TPM
	// state, on a path nothing can reach any more.
	p.stopSwtpm(vmName, vmDir)

	// Remove stale socket and lock file if present (left over from crashed VM)
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove stale TPM socket", "path", sockPath, logging.FieldError, err)
	}
	if err := os.Remove(filepath.Join(tpmStateDir, ".lock")); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove stale TPM lock", "path", tpmStateDir, logging.FieldError, err)
	}

	// --server (not --ctrl): bhyve sends raw TPM2 bytes; --ctrl is for QEMU's
	// PTM protocol. not-need-init lets bhyve send commands without an explicit
	// TPM_Startup first.
	if out, err := p.cmd().CombinedOutput(ctx, p.swtpmPath,
		"socket",
		"--tpm2",
		"--tpmstate", fmt.Sprintf("dir=%s", tpmStateDir),
		"--server", fmt.Sprintf("type=unixio,path=%s", sockPath),
		"--pid", fmt.Sprintf("file=%s", pidFile),
		"--flags", "not-need-init",
		"--daemon"); err != nil {
		return fmt.Errorf("swtpm failed to start: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	// Wait for the socket to appear, honoring context cancellation.
	timer := time.NewTimer(swtpmSocketTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(swtpmPollInterval)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(sockPath); err == nil {
			slog.Info("swtpm started", logging.FieldVM, vmName, "sock", sockPath)
			return nil
		}
		select {
		case <-ctx.Done():
			p.stopSwtpm(vmName, vmDir)
			return ctx.Err()
		case <-timer.C:
			// Socket never appeared — clean up
			p.stopSwtpm(vmName, vmDir)
			return fmt.Errorf("swtpm socket did not appear at %s within timeout", sockPath)
		case <-ticker.C:
		}
	}
}

// stopSwtpm terminates the swtpm process for a VM using its PID file.
func (p *BhyveProvider) stopSwtpm(vmName, vmDir string) {
	// Its own bounded context, not the caller's. This is only ever reached as
	// cleanup, and several callers reach it precisely because the request was
	// canceled — the identity probe below would then fail at once and leave the
	// swtpm running, holding the TPM state lock.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pidFile := filepath.Join(vmDir, "swtpm.pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
		// A stale pidfile plus PID reuse means the number can belong to
		// anything by now; SIGTERM on it would kill an unrelated host process.
		// The same identity check bhyve's own PID gets before a signal.
		if !p.pidIsCommand(ctx, pid, "swtpm") {
			slog.Debug("swtpm pid file names another process; not signaling",
				logging.FieldVM, vmName, "pid", pid)
		} else if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
			slog.Warn("Failed to stop swtpm", logging.FieldVM, vmName, "pid", pid, logging.FieldError, err)
		}
	}
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove swtpm pid file", "path", pidFile, logging.FieldError, err)
	}
}

// ListInstances lists all VMs
func (p *BhyveProvider) ListInstances(ctx context.Context, filter provider.InstanceFilter) ([]provider.InstanceHandle, error) {
	entries, err := os.ReadDir(p.dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read data directory: %w", err)
	}

	var handles []provider.InstanceHandle
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		vmName := entry.Name()

		handle := provider.InstanceHandle{
			ID:       vmName,
			Provider: "bhyve",
			Metadata: map[string]interface{}{
				"name": vmName,
				"path": filepath.Join(p.dataDir, vmName),
			},
		}

		// Apply state filter if specified
		if len(filter.States) > 0 {
			state, err := p.GetInstanceState(ctx, handle)
			if err != nil {
				continue
			}

			// Check if current state matches any of the filter states
			found := false
			for _, filterState := range filter.States {
				if state == filterState {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		handles = append(handles, handle)
	}

	return handles, nil
}

// SetInstanceResources updates VM resources (requires VM restart)
func (p *BhyveProvider) SetInstanceResources(ctx context.Context, handle provider.InstanceHandle, resources provider.ResourceSpec) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Every other mutator in this file takes the per-instance lock. Without it,
	// a resize that read vm.conf before StartInstance wrote its MAC assignments
	// saved a config with those assignments gone.
	// The derived context is discarded: everything below is file work that does
	// not take one.
	_, release, err := p.locks.Acquire(ctx, vmName)
	if err != nil {
		return err
	}
	defer release()

	// Load current config
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	// The same limits CreateInstance and the clone paths enforce: a resize was
	// the one way to put a value into vm.conf that they would have refused.
	cpus, memory := config.CPUs, config.MemoryMB
	if resources.CPUs > 0 {
		cpus = resources.CPUs
	}
	if resources.MemoryMB > 0 {
		memory = resources.MemoryMB
	}
	if err := validation.ValidateResourceLimits(cpus, memory); err != nil {
		return fmt.Errorf("invalid resources: %w", err)
	}
	config.CPUs, config.MemoryMB = cpus, memory

	// Save updated config
	if err := p.saveVMConfig(vmDir, config); err != nil {
		return fmt.Errorf("failed to save VM config: %w", err)
	}

	return nil
}

// GetInstanceMetrics is implemented in metrics.go

// AttachNetwork is not supported for bhyve: there is no NIC hot-plug and
// nothing is persisted — the call always returns ErrUnsupportedOperation.
func (p *BhyveProvider) AttachNetwork(ctx context.Context, handle provider.InstanceHandle, network provider.NetworkAttachment) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	return provider.ErrUnsupportedOperation
}

// DetachNetwork detaches a network interface from a VM (requires VM restart)
func (p *BhyveProvider) DetachNetwork(ctx context.Context, handle provider.InstanceHandle, interfaceID string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	return provider.ErrUnsupportedOperation
}

// Helper functions
