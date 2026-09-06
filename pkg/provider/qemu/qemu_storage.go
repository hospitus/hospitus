package qemu

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hospitus/hospitus/pkg/cloudinit"
	"github.com/hospitus/hospitus/pkg/provider"
)

// validatePhysicalDisk verifies that a physical-passthrough disk path is a
// device node under /dev/ that exists on the host. It never opens or modifies
// the device — the VM uses it as-is.
func validatePhysicalDisk(devPath string) error {
	if devPath == "" {
		return fmt.Errorf("physical disk requires a 'path' (e.g. /dev/ada2)")
	}
	if !strings.HasPrefix(devPath, "/dev/") {
		return fmt.Errorf("physical disk path must be under /dev/ (got: %s)", devPath)
	}
	info, err := os.Stat(devPath)
	if err != nil {
		return fmt.Errorf("physical disk device not found: %s (%w)", devPath, err)
	}
	if info.Mode()&os.ModeDevice == 0 {
		return fmt.Errorf("path is not a device: %s", devPath)
	}
	return nil
}

// AttachDisk attaches a disk to a running VM via QMP hotplug.
func (p *QEMUProvider) AttachDisk(ctx context.Context, handle provider.InstanceHandle, disk provider.DiskAttachment) error {
	qmpSocket := p.qmpSocketFor(handle.ID)
	if qmpSocket == "" {
		return fmt.Errorf("QMP socket not found for VM %s", handle.ID)
	}

	// Validate disk path is within allowed directories
	if !p.isPathAllowed(disk.Disk.Path) {
		return fmt.Errorf("disk path not allowed: %s must be under %s or %s", disk.Disk.Path, p.dataDir, p.imageDir)
	}

	qmp, err := NewQMPClient(qmpSocket)
	if err != nil {
		return fmt.Errorf("failed to create QMP client: %w", err)
	}
	if err := qmp.Connect(); err != nil {
		return fmt.Errorf("failed to connect to QMP: %w", err)
	}
	defer qmp.Close()

	// Generate unique IDs for the disk
	nodeID := fmt.Sprintf("disk-%s", disk.Disk.ID)
	deviceID := fmt.Sprintf("virtio-disk-%s", disk.Disk.ID)

	// Determine driver from disk type
	driver := "raw"
	if disk.Disk.Type == provider.DiskTypeQCOW2 {
		driver = "qcow2"
	}

	if err := qmp.AddBlockDevice(nodeID, driver, disk.Disk.Path); err != nil {
		return fmt.Errorf("failed to add block device: %w", err)
	}

	// Attach as virtio-blk device
	props := map[string]interface{}{
		"id":    deviceID,
		"drive": nodeID,
	}
	if err := qmp.DeviceAdd("virtio-blk-pci", props); err != nil {
		// Cleanup: remove block device
		_ = qmp.RemoveBlockDevice(nodeID)
		return fmt.Errorf("failed to attach disk device: %w", err)
	}

	return nil
}

// DetachDisk detaches a disk from a running VM via QMP.
func (p *QEMUProvider) DetachDisk(ctx context.Context, handle provider.InstanceHandle, diskID string) error {
	qmpSocket := p.qmpSocketFor(handle.ID)
	if qmpSocket == "" {
		return fmt.Errorf("QMP socket not found for VM %s", handle.ID)
	}

	qmp, err := NewQMPClient(qmpSocket)
	if err != nil {
		return fmt.Errorf("failed to create QMP client: %w", err)
	}
	if err := qmp.Connect(); err != nil {
		return fmt.Errorf("failed to connect to QMP: %w", err)
	}
	defer qmp.Close()

	deviceID := fmt.Sprintf("virtio-disk-%s", diskID)
	nodeID := fmt.Sprintf("disk-%s", diskID)

	// Remove the device first
	if err := qmp.DeviceDel(deviceID); err != nil {
		return fmt.Errorf("failed to remove disk device: %w", err)
	}

	if err := qmp.RemoveBlockDevice(nodeID); err != nil {
		return fmt.Errorf("failed to remove block device: %w", err)
	}

	return nil
}

// needsCloudInit determines if cloud-init should be generated for this spec
func (p *QEMUProvider) needsCloudInit(spec provider.InstanceSpec) bool {
	// Cloud-init needed for cloud images (linux VMs)
	if spec.OSType == "linux" || spec.OSType == "ubuntu" || spec.OSType == "debian" || spec.OSType == "centos" || spec.OSType == "fedora" {
		return true
	}

	// Check if cloud-init config is explicitly provided
	if spec.CloudInit != nil && (spec.CloudInit.UserData != "" || spec.CloudInit.MetaData != "") {
		return true
	}

	// Check provider config for cloud-init flag
	if useCloudInit, ok := spec.ProviderConfig["cloud_init"].(bool); ok && useCloudInit {
		return true
	}

	return false
}

// generateCloudInitISO creates a cloud-init ISO for the VM
func (p *QEMUProvider) generateCloudInitISO(ctx context.Context, spec provider.InstanceSpec, outputPath string) error {
	generator, err := cloudinit.NewGenerator()
	if err != nil {
		return fmt.Errorf("failed to create cloud-init generator: %w", err)
	}

	// Build network configs from spec
	var networks []cloudinit.NetworkConfig
	for i := range spec.Networks {
		net := &spec.Networks[i]
		// Default interface name based on index
		ifName := fmt.Sprintf("eth%d", i)
		if i == 0 {
			// First interface is often named differently in modern systems
			ifName = "ens3"
		}

		// Get gateway and DNS from provider config
		var gateway string
		var dns []string
		if net.IPv4 != "" && net.IPv4 != "dhcp" {
			// Try to get gateway from provider config
			if gw, ok := spec.ProviderConfig["gateway"].(string); ok {
				gateway = gw
			}
			switch dnsServers := spec.ProviderConfig["dns"].(type) {
			case []string:
				dns = dnsServers
			case []interface{}:
				for _, d := range dnsServers {
					if ds, ok := d.(string); ok {
						dns = append(dns, ds)
					}
				}
			}
		}

		networks = append(networks, cloudinit.NetworkFromProviderSpec(
			ifName,
			net.IPv4,
			gateway,
			dns,
			net.MAC,
		))
	}

	// Get SSH keys from provider config
	var sshKeys []string
	switch keys := spec.ProviderConfig["ssh_authorized_keys"].(type) {
	case []string:
		sshKeys = keys
	case []interface{}:
		for _, k := range keys {
			if ks, ok := k.(string); ok {
				sshKeys = append(sshKeys, ks)
			}
		}
	}

	// Create cloud-init config
	config := cloudinit.ConfigFromSpec(
		spec.Name,
		spec.Name,
		networks,
		sshKeys,
	)

	// Add custom user-data if provided
	if spec.CloudInit != nil && spec.CloudInit.UserData != "" {
		config.CustomUserData = spec.CloudInit.UserData
	}

	return generator.GenerateISO(ctx, config, outputPath)
}

// createDiskImage creates a new disk image, optionally with a backing file for cloud images
func (p *QEMUProvider) createDiskImage(ctx context.Context, path string, sizeGB int, diskType provider.DiskType, backingFile string) error {
	format := "qcow2"
	switch diskType {
	case provider.DiskTypeRaw:
		format = "raw"
	case provider.DiskTypeQCOW2:
		format = "qcow2"
	case provider.DiskTypeVHD:
		format = "vpc"
	case provider.DiskTypeVMDK:
		format = "vmdk"
	}

	args := []string{"create", "-f", format}

	// If backing file is specified, create a copy-on-write disk
	if backingFile != "" {
		// Verify backing file exists
		if _, err := os.Stat(backingFile); os.IsNotExist(err) {
			return fmt.Errorf("backing file not found: %s", backingFile)
		}

		backingFormat := p.detectImageFormat(ctx, backingFile)

		args = append(args, "-b", backingFile)
		if backingFormat != "" {
			args = append(args, "-F", backingFormat)
		}
	}

	args = append(args, path)
	if sizeGB > 0 {
		args = append(args, fmt.Sprintf("%dG", sizeGB))
	}

	output, err := p.cmd().CombinedOutput(ctx, "qemu-img", args...)
	if err != nil {
		return fmt.Errorf("qemu-img failed: %s: %w", string(output), err)
	}

	return nil
}

// detectImageFormat detects the format of an image file
func (p *QEMUProvider) detectImageFormat(ctx context.Context, imagePath string) string {
	output, err := p.cmd().Output(ctx, "qemu-img", "info", "--output=json", imagePath)
	if err != nil {
		// Default to raw if detection fails
		return "raw"
	}

	var info struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(output, &info); err != nil {
		return "raw"
	}

	return info.Format
}

// resolveCloudImagePath resolves a cloud image reference to a file path
// It looks in the configured image directory for the image
func (p *QEMUProvider) resolveCloudImagePath(imageRef, arch string) (string, error) {
	// A manifest's "cloud:ubuntu-24.04" reaches the provider already stripped,
	// but the same reference typed as --image does not. Accept both so one
	// image reference resolves the same way from the CLI and from a manifest.
	imageRef = strings.TrimPrefix(imageRef, "cloud:")

	// Check if it's already an absolute path
	if filepath.IsAbs(imageRef) {
		if _, err := os.Stat(imageRef); err == nil {
			if !p.isPathAllowed(imageRef) {
				return "", fmt.Errorf("image path not allowed: %s must be under %s or %s", imageRef, p.dataDir, p.imageDir)
			}
			return imageRef, nil
		}
		return "", fmt.Errorf("image file not found: %s", imageRef)
	}

	// Cloud image directory
	cloudImageDir := filepath.Join(p.imageDir, "cloud")

	// Try different naming conventions
	candidates := []string{
		// <image>-<arch>.img (e.g., ubuntu-24.04-amd64.img)
		filepath.Join(cloudImageDir, fmt.Sprintf("%s-%s.img", imageRef, arch)),
		filepath.Join(cloudImageDir, fmt.Sprintf("%s-%s.qcow2", imageRef, arch)),
		filepath.Join(cloudImageDir, fmt.Sprintf("%s.img", imageRef)),
		filepath.Join(cloudImageDir, fmt.Sprintf("%s.qcow2", imageRef)),
		// Without cloud prefix
		filepath.Join(p.imageDir, fmt.Sprintf("%s.img", imageRef)),
		filepath.Join(p.imageDir, fmt.Sprintf("%s.qcow2", imageRef)),
	}

	for _, candidate := range candidates {
		// Guard against a relative imageRef containing ".." that escapes imageDir.
		if !p.isPathAllowed(candidate) {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	// List available images for helpful error message
	var available []string
	if entries, err := os.ReadDir(cloudImageDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				available = append(available, e.Name())
			}
		}
	}

	if len(available) > 0 {
		return "", fmt.Errorf("image '%s' not found. Available images in %s: %s. Use 'hospitus image fetch' to download",
			imageRef, cloudImageDir, strings.Join(available, ", "))
	}
	return "", fmt.Errorf("image '%s' not found and no images in %s. Use 'hospitus image fetch' to download cloud images",
		imageRef, cloudImageDir)
}

// resolveISOPath resolves an ISO image reference to a file path
func (p *QEMUProvider) resolveISOPath(isoRef string) (string, error) {
	// Check if it's already an absolute path
	if filepath.IsAbs(isoRef) {
		if _, err := os.Stat(isoRef); err == nil {
			if !p.isPathAllowed(isoRef) {
				return "", fmt.Errorf("ISO path not allowed: %s must be under %s or %s", isoRef, p.dataDir, p.imageDir)
			}
			return isoRef, nil
		}
		return "", fmt.Errorf("ISO file not found: %s", isoRef)
	}

	// ISO image directory
	isoDir := filepath.Join(p.imageDir, "iso")

	candidates := []string{
		filepath.Join(isoDir, isoRef),
		filepath.Join(isoDir, isoRef+".iso"),
		filepath.Join(p.imageDir, isoRef),
	}

	for _, candidate := range candidates {
		// Guard against a relative isoRef containing ".." that escapes imageDir.
		if !p.isPathAllowed(candidate) {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("ISO '%s' not found in %s", isoRef, isoDir)
}
