package bhyve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

var _ provider.ExportImportProvider = (*BhyveProvider)(nil)

func (p *BhyveProvider) AttachDisk(ctx context.Context, handle provider.InstanceHandle, disk provider.DiskAttachment) error {
	vmName := handle.ID

	// Determine disk path based on disk type
	var diskPath string
	switch disk.Disk.Type {
	case provider.DiskTypePhysical:
		diskPath = disk.Disk.Path
		// SECURITY: same allow-list CreateInstance applies. Without it a VM
		// could acquire the host's system disk after the fact.
		if err := p.validatePhysicalDisk(diskPath); err != nil {
			return err
		}
	case provider.DiskTypeRaw, provider.DiskTypeQCOW2:
		if disk.Disk.Path != "" {
			diskPath = disk.Disk.Path
			// SECURITY: an image file must be one of ours. Anything else is
			// either a host file the guest would read or a disk belonging to
			// another VM.
			within, err := validation.PathWithinAny(diskPath, filepath.Join(p.dataDir, vmName), p.imageDir)
			if err != nil {
				return fmt.Errorf("resolving disk path: %w", err)
			}
			if !within {
				return fmt.Errorf("disk %s must be under the VM directory or the image directory (%s)", diskPath, p.imageDir)
			}
		} else {
			// Create new disk
			vmDir := filepath.Join(p.dataDir, vmName)
			diskPath = filepath.Join(vmDir, fmt.Sprintf("disk_hotplug_%d.img", time.Now().Unix()))
			sizeBytes := int64(disk.Disk.SizeGB) * 1024 * 1024 * 1024
			if err := p.createRawDisk(ctx, diskPath, sizeBytes); err != nil {
				return fmt.Errorf("failed to create disk: %w", err)
			}
		}
	default:
		return fmt.Errorf("unsupported disk type for hot-plug: %s", disk.Disk.Type)
	}

	// Load VM configuration
	vmDir := filepath.Join(p.dataDir, vmName)
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM configuration: %w", err)
	}

	// Use default disk driver (virtio-blk)
	driver := "virtio-blk"

	// Add disk to configuration
	config.DiskPaths = append(config.DiskPaths, diskPath)
	config.DiskDrivers = append(config.DiskDrivers, driver)

	// Save updated configuration
	if err := p.saveVMConfig(vmDir, config); err != nil {
		return fmt.Errorf("failed to save VM configuration: %w", err)
	}

	return nil
}

// DetachDisk detaches a disk from a VM (requires VM restart)
//
// The disk is removed from configuration and will not be attached on next boot.
// Requires VM restart for change to take effect.
func (p *BhyveProvider) DetachDisk(ctx context.Context, handle provider.InstanceHandle, diskID string) error {
	vmName := handle.ID

	// Load VM configuration
	vmDir := filepath.Join(p.dataDir, vmName)
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM configuration: %w", err)
	}

	// diskID is the disk path
	diskPath := diskID

	// Find and remove disk
	found := false
	newDiskPaths := []string{}
	newDiskDrivers := []string{}

	for i, path := range config.DiskPaths {
		if path == diskPath {
			found = true
			// Skip this disk
		} else {
			newDiskPaths = append(newDiskPaths, path)
			if i < len(config.DiskDrivers) {
				newDiskDrivers = append(newDiskDrivers, config.DiskDrivers[i])
			}
		}
	}

	if !found {
		return fmt.Errorf("disk not found in VM configuration: %s", diskPath)
	}

	config.DiskPaths = newDiskPaths
	config.DiskDrivers = newDiskDrivers

	// Save updated configuration
	if err := p.saveVMConfig(vmDir, config); err != nil {
		return fmt.Errorf("failed to save VM configuration: %w", err)
	}

	return nil
}

// generateCloudInitISO builds a NoCloud cloud-init ISO (meta-data, user-data and
// optional network-config) in the VM directory and returns its path. It returns
// an empty path when config is nil.
func (p *BhyveProvider) generateCloudInitISO(ctx context.Context, vmName string, config *provider.CloudInitConfig) (string, error) {
	if config == nil {
		return "", nil
	}

	// Create cloud-init directory in VM directory
	vmDir := filepath.Join(p.dataDir, vmName)
	ciDir := filepath.Join(vmDir, "cloud-init")
	// 0700: cloud-init user-data typically carries passwords and SSH keys, so the
	// directory holding it must not be world- or group-readable.
	if err := os.MkdirAll(ciDir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create cloud-init directory: %w", err)
	}

	// SECURITY: Enforce size limits to prevent DoS via huge cloud-init configs
	const maxMetaDataSize = 64 * 1024      // 64KB
	const maxUserDataSize = 1024 * 1024    // 1MB
	const maxNetworkConfigSize = 64 * 1024 // 64KB

	// Write meta-data
	// If not provided, generate minimal metadata
	metaData := config.MetaData
	if metaData == "" {
		metaData = fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n", vmName, vmName)
	}
	if len(metaData) > maxMetaDataSize {
		return "", fmt.Errorf("meta-data exceeds maximum size of %d bytes", maxMetaDataSize)
	}
	if err := os.WriteFile(filepath.Join(ciDir, "meta-data"), []byte(metaData), 0o600); err != nil {
		return "", fmt.Errorf("failed to write meta-data: %w", err)
	}

	// Write user-data
	// Must start with #cloud-config for cloud-config YAML
	userData := config.UserData
	if userData != "" {
		if len(userData) > maxUserDataSize {
			return "", fmt.Errorf("user-data exceeds maximum size of %d bytes", maxUserDataSize)
		}
		if !strings.HasPrefix(userData, "#cloud-config") && !strings.HasPrefix(userData, "#!/") {
			userData = "#cloud-config\n" + userData
		}
		if err := os.WriteFile(filepath.Join(ciDir, "user-data"), []byte(userData), 0o600); err != nil {
			return "", fmt.Errorf("failed to write user-data: %w", err)
		}
	} else {
		// Empty user-data file is required
		if err := os.WriteFile(filepath.Join(ciDir, "user-data"), []byte(""), 0o600); err != nil {
			return "", fmt.Errorf("failed to write user-data: %w", err)
		}
	}

	// Write network-config (optional)
	if config.Network != "" {
		if len(config.Network) > maxNetworkConfigSize {
			return "", fmt.Errorf("network-config exceeds maximum size of %d bytes", maxNetworkConfigSize)
		}
		if err := os.WriteFile(filepath.Join(ciDir, "network-config"), []byte(config.Network), 0o600); err != nil {
			return "", fmt.Errorf("failed to write network-config: %w", err)
		}
	}

	// Create ISO image with volume label "cidata" for NoCloud datasource.
	// makefs(8) is part of the FreeBSD base system and is the preferred tool.
	// Fall back to mkisofs/genisoimage on non-FreeBSD hosts.
	isoPath := filepath.Join(vmDir, "cloud-init.iso")

	var cmd *exec.Cmd
	switch {
	case p.checkCommand("makefs"):
		// FreeBSD base system tool: makefs -t cd9660 -o label=cidata -o rockridge <output> <input>
		cmd = exec.CommandContext(ctx, "makefs",
			"-t", "cd9660",
			"-o", "label=cidata",
			"-o", "rockridge",
			isoPath,
			ciDir,
		)
	case p.checkCommand("mkisofs"):
		cmd = exec.CommandContext(ctx, "mkisofs",
			"-output", isoPath,
			"-volid", "cidata",
			"-joliet",
			"-rock",
			ciDir,
		)
	case p.checkCommand("genisoimage"):
		cmd = exec.CommandContext(ctx, "genisoimage",
			"-output", isoPath,
			"-volid", "cidata",
			"-joliet",
			"-rock",
			ciDir,
		)
	default:
		return "", fmt.Errorf("no ISO creation tool found: install makefs (FreeBSD base), sysutils/cdrtools (mkisofs), or sysutils/cdrkit (genisoimage)")
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to create cloud-init ISO: %w (output: %s)", err, string(output))
	}

	return isoPath, nil
}

// Disk Type Support (virtio-blk, ahci-hd, nvme)

// DiskDriver represents the disk driver type for bhyve
func (p *BhyveProvider) AttachISO(ctx context.Context, handle provider.InstanceHandle, isoPath string) error {
	vmName := handle.ID

	// SECURITY: Validate ISO path
	if isoPath == "" {
		return fmt.Errorf("ISO path cannot be empty")
	}

	// SECURITY: Prevent path traversal and ensure ISO is under allowed directories.
	// ISOs should be in the image directory or the VM's data directory.
	absPath, err := filepath.Abs(isoPath)
	if err != nil {
		return fmt.Errorf("failed to resolve ISO path: %w", err)
	}
	if strings.Contains(absPath, "..") {
		return fmt.Errorf("ISO path contains path traversal")
	}
	// Allow ISOs from the image dir or the VM's own directory
	vmDir := filepath.Join(p.dataDir, vmName)
	if !strings.HasPrefix(absPath, p.imageDir+string(filepath.Separator)) &&
		absPath != p.imageDir &&
		!strings.HasPrefix(absPath, vmDir+string(filepath.Separator)) &&
		absPath != vmDir {
		return fmt.Errorf("ISO must be under image directory (%s) or VM directory (%s)", p.imageDir, vmDir)
	}

	// Check if ISO file exists
	if _, err := os.Stat(absPath); err != nil {
		return fmt.Errorf("ISO file not found: %s (%w)", absPath, err)
	}

	// Load VM configuration
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM configuration: %w", err)
	}

	// Add ISO to disk paths using the validated absolute path (not the raw,
	// possibly-relative isoPath, which would be resolved against the daemon's
	// working directory instead of the location we just validated).
	config.DiskPaths = append(config.DiskPaths, absPath)
	config.DiskDrivers = append(config.DiskDrivers, "ahci-cd")

	// Save updated configuration
	if err := p.saveVMConfig(vmDir, config); err != nil {
		return fmt.Errorf("failed to save VM configuration: %w", err)
	}

	return nil
}

// DetachISO removes a CD/ISO image from a bhyve VM.
func (p *BhyveProvider) DetachISO(ctx context.Context, handle provider.InstanceHandle, isoPath string) error {
	vmName := handle.ID

	// Load VM configuration
	vmDir := filepath.Join(p.dataDir, vmName)
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM configuration: %w", err)
	}

	// Remove ISO from disk paths and corresponding driver
	newDiskPaths := make([]string, 0, len(config.DiskPaths))
	newDiskDrivers := make([]string, 0, len(config.DiskDrivers))
	for i, diskPath := range config.DiskPaths {
		if diskPath != isoPath {
			newDiskPaths = append(newDiskPaths, diskPath)
			if i < len(config.DiskDrivers) {
				newDiskDrivers = append(newDiskDrivers, config.DiskDrivers[i])
			}
		}
	}
	config.DiskPaths = newDiskPaths
	config.DiskDrivers = newDiskDrivers

	// Save updated configuration
	if err := p.saveVMConfig(vmDir, config); err != nil {
		return fmt.Errorf("failed to save VM configuration: %w", err)
	}

	return nil
}

// Auto-start Support is implemented in autostart.go
// See: SetAutoStart, GetAutoStart, ListAutoStartInstances, StartAutoStartInstances

// Export/Import Support

// ExportInstance exports a bhyve VM to a tarball for migration.
//
//   - Stops the VM if requested
//   - Archives the VM directory (vm.conf, vm.state, and any file-backed
//     disk images stored there); ZVOL-backed disks are NOT included
//   - Optionally compresses with gzip
func (p *BhyveProvider) ExportInstance(ctx context.Context, handle provider.InstanceHandle, exportPath string, opts provider.ExportOptions) error {
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Check if VM exists
	if _, err := os.Stat(vmDir); err != nil {
		return fmt.Errorf("VM directory not found: %w", err)
	}

	// Stop the instance if requested or required
	if opts.StopInstance {
		state, err := p.GetInstanceState(ctx, handle)
		if err != nil {
			return fmt.Errorf("failed to get instance state: %w", err)
		}
		if state == provider.StateRunning {
			if err := p.StopInstance(ctx, handle, provider.StopOptions{Timeout: 30 * time.Second}); err != nil {
				return fmt.Errorf("failed to stop instance before export: %w", err)
			}
		}
	}

	// Validate export path is within data directory
	absExportPath, err := filepath.Abs(filepath.Clean(exportPath))
	if err != nil {
		return fmt.Errorf("invalid export path: %w", err)
	}
	absDataDir, err := filepath.Abs(p.dataDir)
	if err != nil {
		return fmt.Errorf("invalid data dir: %w", err)
	}
	if !strings.HasPrefix(absExportPath, absDataDir+string(os.PathSeparator)) && absExportPath != absDataDir {
		return fmt.Errorf("export path must be within data directory %q", p.dataDir)
	}

	// Create tarball
	// Use tar with optional gzip compression
	var cmd *exec.Cmd
	if opts.Compress {
		cmd = exec.CommandContext(ctx, "tar", "-czf", exportPath, "-C", p.dataDir, vmName)
	} else {
		cmd = exec.CommandContext(ctx, "tar", "-cf", exportPath, "-C", p.dataDir, vmName)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create tarball: %w (output: %s)", err, string(output))
	}

	return nil
}

// ImportInstance imports a bhyve VM from a tarball.
//
// This implements CBSD's bimport functionality:
//   - Extracts tarball to data directory
//   - Optionally renames the VM
//   - Registers the VM with HOSPITUS
//   - Optionally starts the VM after import
//
// The tarball should contain a directory with vm.conf, vm.state, and disk images.
func (p *BhyveProvider) ImportInstance(ctx context.Context, importPath string, opts provider.ImportOptions) (provider.InstanceHandle, error) {
	// SECURITY: Validate import path exists
	if _, err := os.Stat(importPath); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("import file not found: %w", err)
	}

	// Create temporary directory for extraction
	tempDir, err := os.MkdirTemp("", "hospitus-import-*")
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Extract tarball to temp directory.
	// bsdtar (FreeBSD tar) does not preserve owner/permissions by default
	// unless -p is explicitly passed. We omit -p for safety.
	// -m omits modification time restoration.
	cmd := exec.CommandContext(ctx, "tar", "-xmf", importPath, "-C", tempDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to extract tarball: %w (output: %s)", err, string(output))
	}

	// SECURITY: Prevent symlink escape attacks — verify all extracted files stay
	// within the intended temp directory. A malicious archive could use symlinks
	// to write outside the extraction target.
	if err := validateExtractionPath(tempDir); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("path traversal detected in tarball: %w", err)
	}

	// Find the VM directory in the extracted content
	// The tarball should contain one directory with the VM data
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to read temp directory: %w", err)
	}

	var vmDirName string
	for _, entry := range entries {
		if entry.IsDir() {
			vmDirName = entry.Name()
			break
		}
	}

	if vmDirName == "" {
		return provider.InstanceHandle{}, fmt.Errorf("no VM directory found in tarball")
	}
	// SECURITY: the directory name comes from the archive and becomes the
	// instance name, a directory under the data dir, and part of every tap and
	// zvol name derived from it.
	if err := validation.ValidateInstanceName(vmDirName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("archive contains an unusable VM name %q: %w", vmDirName, err)
	}

	// Determine target VM name (original or renamed)
	targetVMName := vmDirName
	if opts.NewName != "" {
		if err := validation.ValidateInstanceName(opts.NewName); err != nil {
			return provider.InstanceHandle{}, fmt.Errorf("invalid new name: %w", err)
		}
		targetVMName = opts.NewName
	}

	// Check if target VM already exists
	targetVMDir := filepath.Join(p.dataDir, targetVMName)
	if _, err := os.Stat(targetVMDir); err == nil {
		return provider.InstanceHandle{}, fmt.Errorf("VM %s already exists", targetVMName)
	}

	// Move VM directory from temp to data directory
	extractedVMDir := filepath.Join(tempDir, vmDirName)
	if err := os.Rename(extractedVMDir, targetVMDir); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to move VM directory: %w", err)
	}

	// Load VM configuration to update name if renamed
	config, err := p.loadVMConfig(targetVMDir)
	if err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to load VM config: %w", err)
	}

	// SECURITY: the configuration was written by whoever made the archive. Put
	// it through the guards CreateInstance applies before it can name a host
	// disk as a VM disk, or an interface of the host's as a tap this import is
	// about to destroy. A refused archive leaves nothing behind.
	if err := p.validateRuntimeConfig(config); err != nil {
		if rmErr := os.RemoveAll(targetVMDir); rmErr != nil {
			logging.WithComponent("bhyve").Warn("could not remove a refused import",
				"path", targetVMDir, logging.FieldError, rmErr)
		}
		return provider.InstanceHandle{}, fmt.Errorf("refusing to import %s: %w", vmDirName, err)
	}

	// Update VM name in config if renamed
	if opts.NewName != "" {
		config.Name = targetVMName
		if err := p.saveVMConfig(targetVMDir, config); err != nil {
			return provider.InstanceHandle{}, fmt.Errorf("failed to update VM config: %w", err)
		}
	}

	// Reset MAC addresses if requested
	if opts.ResetMAC {
		// Destroy old tap devices and create new ones
		for i := range config.TapDevs {
			// Destroy old tap device (if it exists). Best-effort: the tap may not
			// exist on this host (import from another machine), so a failure is
			// logged but not fatal.
			oldTap := config.TapDevs[i]
			if out, derr := exec.CommandContext(ctx, "ifconfig", oldTap, "destroy").CombinedOutput(); derr != nil {
				logging.WithComponent("bhyve").Debug("could not destroy old tap during import",
					"tap", oldTap, logging.FieldError, derr, "output", strings.TrimSpace(string(out)))
			}

			// Create new tap device with new name
			newTap, err := p.createTapDevice(ctx, targetVMName, i)
			if err != nil {
				return provider.InstanceHandle{}, fmt.Errorf("failed to create new tap device: %w", err)
			}
			config.TapDevs[i] = newTap
		}

		if err := p.saveVMConfig(targetVMDir, config); err != nil {
			return provider.InstanceHandle{}, fmt.Errorf("failed to save updated config: %w", err)
		}
	}

	// Create instance handle
	handle := provider.InstanceHandle{
		ID:       targetVMName,
		Provider: "bhyve",
		Metadata: map[string]interface{}{
			"name":     targetVMName,
			"path":     targetVMDir,
			"imported": true,
		},
	}

	// Start the instance if requested
	if opts.StartAfterImport {
		if err := p.StartInstance(ctx, handle); err != nil {
			return handle, fmt.Errorf("instance imported but failed to start: %w", err)
		}
	}

	return handle, nil
}

// deployCloudImage copies a cloud image to a destination disk using qemu-img.
// It handles XZ-compressed images (e.g. .qcow2.xz or mislabelled .qcow2 files
// that are actually XZ-compressed) by decompressing to a temp file first.
func (p *BhyveProvider) deployCloudImage(ctx context.Context, imagePath, destPath string) error {
	// Check if qemu-img is available
	if !p.checkCommand("qemu-img") {
		return fmt.Errorf("qemu-img not found (install emulators/qemu-tools)")
	}

	logger := logging.WithComponent("bhyve")

	// Detect XZ-compressed images regardless of file extension.
	// Some cloud images (e.g. FreeBSD ZFS) are XZ-compressed QCOW2 files
	// but distributed with a .qcow2 extension. qemu-img cannot handle XZ
	// directly — it will misidentify the format and write compressed bytes
	// verbatim to the disk, producing an unbootable result.
	xz, err := isXZCompressed(imagePath)
	if err != nil {
		return fmt.Errorf("failed to probe image format: %w", err)
	}

	srcPath := imagePath
	if xz {
		logger.Info("Decompressing XZ-compressed cloud image", "image", imagePath)

		// Decompress to a temp file alongside the source image so we stay
		// on the same filesystem and avoid large cross-device copies.
		tmpFile, err := os.CreateTemp(filepath.Dir(imagePath), ".hospitus-decomp-*.tmp")
		if err != nil {
			return fmt.Errorf("failed to create temp file for decompression: %w", err)
		}
		tmpPath := tmpFile.Name()
		defer os.Remove(tmpPath)

		xzcatCmd := exec.CommandContext(ctx, "xzcat", imagePath)
		xzcatCmd.Stdout = tmpFile
		// xzcat names the reason on stderr — a truncated download, a corrupt
		// archive, a full filesystem. Dropping it leaves the caller with
		// "exit status 1" and nothing to act on.
		var xzStderr bytes.Buffer
		xzcatCmd.Stderr = &xzStderr
		if runErr := xzcatCmd.Run(); runErr != nil {
			tmpFile.Close()
			if detail := strings.TrimSpace(xzStderr.String()); detail != "" {
				return fmt.Errorf("failed to decompress XZ image: %w: %s", runErr, detail)
			}
			return fmt.Errorf("failed to decompress XZ image: %w", runErr)
		}
		if closeErr := tmpFile.Close(); closeErr != nil {
			return fmt.Errorf("failed to flush decompressed image: %w", closeErr)
		}
		srcPath = tmpPath
		logger.Info("Decompression complete", "tmp", tmpPath)
	}

	// Use qemu-img convert to copy image and potentially convert format.
	// -O raw is used because bhyve works best with raw files or ZVOLs (block devices).
	cmd := exec.CommandContext(ctx, "qemu-img", "convert", "-O", "raw", srcPath, destPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to deploy cloud image: %w (output: %s)", err, string(output))
	}

	return nil
}

// injectFreeBSDGuestConfig injects required guest configuration into a FreeBSD
// ZFS cloud image stored in a ZVOL before first boot.
//
// FreeBSD ZFS cloud images ship with nuageinit_enable="NO" and route their
// console exclusively to COM1 (serial). Both problems are fixed here:
//   - nuageinit_enable="YES" is appended to /etc/rc.conf so cloud-init runs
//   - console="vidconsole,comconsole" is appended to /boot/loader.conf so
//     output appears on the VNC framebuffer in addition to the serial line
//
// Approach: ZVOLs are character devices that GEOM cannot probe for partition
// tables. We therefore:
//  1. Read the GPT header (sector 1) and partition table to find the ZFS
//     partition start LBA.
//  2. Extract the ZFS vdev data from the ZVOL into a temporary file using dd.
//  3. Attach the file as an md(4) device so ZFS can import it.
//  4. Import the pool with an alternate root and rename it to avoid colliding
//     with the host's "zroot" pool.
//  5. Explicitly mount ROOT/default (canmount=noauto in cloud images).
//  6. Inject the config files.
//  7. Export the pool and write the modified vdev data back to the ZVOL.
//  8. Detach the md(4) device and clean up.
//
// If the ZVOL does not contain a GPT with a ZFS partition the function returns
// nil — no injection is needed for non-ZFS guests.
func (p *BhyveProvider) injectFreeBSDGuestConfig(ctx context.Context, zvolPath string) error {
	logger := logging.WithComponent("bhyve")

	// ── Step 1: parse GPT to locate the ZFS partition ──────────────────────

	startLBA, vdevSize, err := findZFSPartitionInZVOL(zvolPath)
	if err != nil {
		// Not a GPT disk or no ZFS partition — skip silently (Linux images etc.)
		logger.Debug("No ZFS partition found in ZVOL, skipping FreeBSD config injection",
			"zvol", zvolPath, "reason", err.Error())
		return nil
	}
	logger.Debug("Found ZFS partition in ZVOL", "zvol", zvolPath,
		"startLBA", startLBA, "vdevBytes", vdevSize)

	// ── Step 2: extract vdev data to a temporary file ─────────────────────

	// Create the temp vdev file next to the VM data (dataDir), not in TMPDIR,
	// which is frequently a memory-backed tmpfs: the whole ZFS partition is
	// copied here and could exhaust RAM for a large disk.
	tmpFile, err := os.CreateTemp(p.dataDir, ".hospitus-zfs-vdev-*.raw")
	if err != nil {
		return fmt.Errorf("create temp vdev file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	sectorCount := vdevSize / 512
	ddArgs := []string{
		"if=" + zvolPath,
		"of=" + tmpPath,
		"bs=512",
		fmt.Sprintf("skip=%d", startLBA),
		fmt.Sprintf("count=%d", sectorCount),
	}
	if out, err := exec.CommandContext(ctx, "dd", ddArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("dd extract ZFS vdev: %w: %s", err, out)
	}
	logger.Debug("Extracted ZFS vdev to temp file", "path", tmpPath,
		"sectors", sectorCount)

	// ── Step 3: attach temp file as md(4) device ──────────────────────────

	// Use Output() so mdOut is stdout only (the "mdN" device name); mixing in
	// stderr via CombinedOutput would corrupt the device name we parse below.
	mdOut, err := exec.CommandContext(ctx, "mdconfig", "-a", "-t", "vnode", "-f", tmpPath).Output()
	if err != nil {
		stderr := ""
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr = strings.TrimSpace(string(exitErr.Stderr))
		}
		return fmt.Errorf("mdconfig attach: %w: %s", err, stderr)
	}
	mdName := strings.TrimSpace(string(mdOut))
	mdDev := "/dev/" + mdName
	defer func() {
		// Extract unit number for -u flag
		unit := strings.TrimPrefix(mdName, "md")
		exec.Command("mdconfig", "-d", "-u", unit).Run() //nolint:errcheck
	}()
	logger.Debug("Attached md device", "dev", mdDev)

	// ── Step 4: import the ZFS pool via the md device ─────────────────────

	importName := fmt.Sprintf("hospitus-import-%d", time.Now().UnixNano())
	altRoot := filepath.Join(os.TempDir(), importName)
	if err := os.MkdirAll(altRoot, 0o755); err != nil {
		return fmt.Errorf("create alt root: %w", err)
	}
	defer os.RemoveAll(altRoot)

	importOut, err := exec.CommandContext(ctx, "zpool", "import",
		"-d", mdDev,
		"-R", altRoot,
		"-f",
		"zroot",    // pool name inside the image
		importName, // rename to avoid host conflict
	).CombinedOutput()
	if err != nil {
		logger.Debug("ZFS pool import via md failed, skipping injection",
			"zvol", zvolPath, "output", strings.TrimSpace(string(importOut)))
		return nil
	}
	logger.Info("ZFS pool imported for FreeBSD guest config injection",
		"pool", importName, "md", mdDev)

	poolExported := false
	defer func() {
		if !poolExported {
			if err := exec.Command("zpool", "export", importName).Run(); err != nil {
				logger.Warn("Failed to export ZFS pool after config injection",
					"pool", importName, logging.FieldError, err)
			}
		}
	}()

	// ── Step 5: mount ROOT/default (canmount=noauto in cloud images) ───────

	rootDS := importName + "/ROOT/default"
	mountOut, err := exec.CommandContext(ctx, "zfs", "mount", rootDS).CombinedOutput()
	if err != nil {
		logger.Warn("Could not mount ROOT/default dataset, injecting into pool root",
			"dataset", rootDS, "output", strings.TrimSpace(string(mountOut)))
		// altRoot still points to pool root — try anyway
	}

	// ── Step 6: inject config files ───────────────────────────────────────

	rcconfPath := filepath.Join(altRoot, "etc", "rc.conf")
	if data, readErr := os.ReadFile(rcconfPath); readErr == nil {
		if !strings.Contains(string(data), "nuageinit_enable") {
			f, openErr := os.OpenFile(rcconfPath, os.O_APPEND|os.O_WRONLY, 0o644)
			if openErr == nil {
				_, _ = fmt.Fprintln(f, `nuageinit_enable="YES"`)
				_ = f.Close()
				logger.Info("Injected nuageinit_enable=YES into guest /etc/rc.conf")
			} else {
				logger.Warn("Could not write guest /etc/rc.conf", logging.FieldError, openErr)
			}
		}
	}

	loaderPath := filepath.Join(altRoot, "boot", "loader.conf")
	loaderData, _ := os.ReadFile(loaderPath)
	if !strings.Contains(string(loaderData), "console=") {
		f, openErr := os.OpenFile(loaderPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if openErr == nil {
			_, _ = fmt.Fprintln(f, `console="vidconsole,comconsole"`)
			_ = f.Close()
			logger.Info("Injected vidconsole into guest /boot/loader.conf")
		} else {
			logger.Warn("Could not write guest /boot/loader.conf", logging.FieldError, openErr)
		}
	}

	// ── Step 7: export pool, write vdev back to ZVOL, detach md ───────────

	if err := exec.CommandContext(ctx, "zpool", "export", importName).Run(); err != nil {
		return fmt.Errorf("export pool %s: %w", importName, err)
	}
	poolExported = true
	logger.Debug("ZFS pool exported after config injection", "pool", importName)

	writeArgs := []string{
		"if=" + tmpPath,
		"of=" + zvolPath,
		"bs=512",
		fmt.Sprintf("seek=%d", startLBA),
		fmt.Sprintf("count=%d", sectorCount),
	}
	if out, err := exec.CommandContext(ctx, "dd", writeArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("dd write ZFS vdev back to ZVOL: %w: %s", err, out)
	}
	logger.Info("FreeBSD guest config injection complete", "zvol", zvolPath)

	return nil
}

// findZFSPartitionInZVOL reads the ZVOL's GPT partition table and returns the
// start LBA and byte size of the first FreeBSD-ZFS partition found.
//
// FreeBSD ZFS type GUID: 516E7CBA-6ECF-11D6-8FF8-00022D09712B
// The GPT header is at LBA 1 (bytes 512–1023), the partition entries follow
// at LBA 2 (bytes 1024+). Each entry is 128 bytes.
func findZFSPartitionInZVOL(zvolPath string) (startLBA, vdevBytes int64, err error) {
	f, err := os.Open(zvolPath)
	if err != nil {
		return 0, 0, fmt.Errorf("open ZVOL: %w", err)
	}
	defer f.Close()

	// Read GPT header at LBA 1
	gptHeader := make([]byte, 512)
	if _, err := f.ReadAt(gptHeader, 512); err != nil {
		return 0, 0, fmt.Errorf("read GPT header: %w", err)
	}
	// Verify GPT signature "EFI PART"
	if string(gptHeader[0:8]) != "EFI PART" {
		return 0, 0, fmt.Errorf("no GPT signature")
	}

	// Parse partition entry start LBA (offset 72, 8 bytes LE) and count (offset 80, 4 bytes)
	// and entry size (offset 84, 4 bytes)
	partEntryLBA := int64(gptHeader[72]) | int64(gptHeader[73])<<8 |
		int64(gptHeader[74])<<16 | int64(gptHeader[75])<<24 |
		int64(gptHeader[76])<<32 | int64(gptHeader[77])<<40 |
		int64(gptHeader[78])<<48 | int64(gptHeader[79])<<56
	partCount := int(gptHeader[80]) | int(gptHeader[81])<<8 |
		int(gptHeader[82])<<16 | int(gptHeader[83])<<24
	entrySize := int(gptHeader[84]) | int(gptHeader[85])<<8 |
		int(gptHeader[86])<<16 | int(gptHeader[87])<<24

	// Bound both dimensions before allocating: entrySize comes from an untrusted
	// image and, without an upper limit, partCount*entrySize could request a
	// multi-gigabyte allocation. A GPT entry is 128 bytes and never exceeds a few
	// KiB, so cap it at 4096 (worst case 128*4096 = 512 KiB).
	if partCount <= 0 || partCount > 128 || entrySize < 128 || entrySize > 4096 {
		return 0, 0, fmt.Errorf("invalid GPT partition count/size: count=%d size=%d",
			partCount, entrySize)
	}

	// FreeBSD ZFS partition type GUID: 516E7CBA-6ECF-11D6-8FF8-00022D09712B
	// GPT stores the first 3 GUID components in little-endian byte order:
	//   516E7CBA → BA 7C 6E 51  (32-bit LE)
	//   6ECF     → CF 6E        (16-bit LE)
	//   11D6     → D6 11        (16-bit LE)
	//   8FF8 00022D09712B → 8F F8 00 02 2D 09 71 2B  (big-endian)
	freebsdZFSTypeGUID := []byte{
		0xBA, 0x7C, 0x6E, 0x51,
		0xCF, 0x6E,
		0xD6, 0x11,
		0x8F, 0xF8,
		0x00, 0x02, 0x2D, 0x09, 0x71, 0x2B,
	}

	tableData := make([]byte, partCount*entrySize)
	if _, err := f.ReadAt(tableData, partEntryLBA*512); err != nil {
		return 0, 0, fmt.Errorf("read GPT table: %w", err)
	}

	for i := 0; i < partCount; i++ {
		entry := tableData[i*entrySize : (i+1)*entrySize]
		typeGUID := entry[0:16]
		if !bytes.Equal(typeGUID, freebsdZFSTypeGUID) {
			continue
		}
		// Found a ZFS partition
		pStart := int64(entry[32]) | int64(entry[33])<<8 | int64(entry[34])<<16 | int64(entry[35])<<24 |
			int64(entry[36])<<32 | int64(entry[37])<<40 | int64(entry[38])<<48 | int64(entry[39])<<56
		pEnd := int64(entry[40]) | int64(entry[41])<<8 | int64(entry[42])<<16 | int64(entry[43])<<24 |
			int64(entry[44])<<32 | int64(entry[45])<<40 | int64(entry[46])<<48 | int64(entry[47])<<56
		sectors := pEnd - pStart + 1
		return pStart, sectors * 512, nil
	}

	return 0, 0, fmt.Errorf("no FreeBSD-ZFS partition found in GPT")
}

// isXZCompressed reports whether the file at path starts with the XZ magic bytes.
// XZ magic: \xfd 7 z X Z \x00
func isXZCompressed(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 6)
	if _, err := io.ReadFull(f, buf); err != nil {
		return false, nil // file too small to be XZ
	}
	xzMagic := []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}
	return bytes.Equal(buf, xzMagic), nil
}

// validateExtractionPath walks a directory tree and verifies that all entries
// resolve within the given root directory. This prevents symlink escape attacks
// where a malicious tarball could create symlinks pointing outside the intended
// extraction target.
func validateExtractionPath(root string) error {
	// Resolve the root to an absolute path to prevent TOCTOU races
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("failed to resolve root path: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	return filepath.WalkDir(absRoot, func(walkPath string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// Both sides are symlink-resolved: an entry cannot escape the root through
		// a link, and a root reached through a link still matches.
		within, err := validation.PathWithin(walkPath, absRoot)
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", walkPath, err)
		}
		if !within {
			return fmt.Errorf("entry %s resolves outside extraction root", walkPath)
		}
		return nil
	})
}
