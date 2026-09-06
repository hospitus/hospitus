package bhyve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// InsertMedia inserts an ISO or other media into a bhyve VM's CD-ROM drive.
//
// Important: bhyve does not support hot-plug of media. The VM must be stopped
// before inserting media. This function modifies the VM configuration; the
// media will be available when the VM is next started.
//
// If DeviceID is not specified, a new ahci-cd device is added to the VM.
// If DeviceID matches an existing ahci-cd device, its path is updated.
func (p *BhyveProvider) InsertMedia(ctx context.Context, handle provider.InstanceHandle, spec provider.MediaSpec) error {
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Verify the media file exists
	if _, err := os.Stat(spec.Path); os.IsNotExist(err) {
		return fmt.Errorf("media file not found: %s", spec.Path)
	}
	// SECURITY: the path becomes an ahci-cd slot the guest reads, so confine it
	// the way AttachISO does. Otherwise a host device, another VM's zvol or any
	// readable file could be handed to the guest.
	within, err := validation.PathWithinAny(spec.Path, p.imageDir, vmDir)
	if err != nil {
		return fmt.Errorf("resolving media path: %w", err)
	}
	if !within {
		return fmt.Errorf("media must be under the image directory (%s) or the VM directory (%s)", p.imageDir, vmDir)
	}

	// Load VM configuration
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	// Check if VM is running - media changes require restart
	state, err := p.GetInstanceState(ctx, handle)
	if err == nil && state == provider.StateRunning {
		return fmt.Errorf("VM is running; bhyve does not support hot-plug of media. Stop the VM first")
	}

	// Find or create the CD-ROM device
	deviceFound := false
	deviceIndex := -1

	if spec.DeviceID != "" {
		// Look for existing device by ID
		for i, driver := range config.DiskDrivers {
			if driver == "ahci-cd" {
				// Device IDs are formatted as "ahci-cd-N" where N is the index
				expectedID := fmt.Sprintf("ahci-cd-%d", i)
				if spec.DeviceID == expectedID {
					deviceFound = true
					deviceIndex = i
					break
				}
			}
		}
	}

	if deviceFound {
		// Update existing CD-ROM device path.
		config.DiskPaths[deviceIndex] = spec.Path
	} else {
		// Add new CD-ROM device.
		config.DiskPaths = append(config.DiskPaths, spec.Path)
		config.DiskDrivers = append(config.DiskDrivers, "ahci-cd")
	}

	// Save updated configuration
	if err := p.saveVMConfig(vmDir, config); err != nil {
		return fmt.Errorf("failed to save VM config: %w", err)
	}

	return nil
}

// EjectMedia ejects media from a bhyve VM's CD-ROM drive.
//
// Important: bhyve does not support hot-eject. The VM must be stopped before
// ejecting media. This function removes the CD-ROM from the VM configuration;
// the change takes effect when the VM is next started.
func (p *BhyveProvider) EjectMedia(ctx context.Context, handle provider.InstanceHandle, deviceID string) error {
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Load VM configuration
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	// Check if VM is running - media changes require restart
	state, err := p.GetInstanceState(ctx, handle)
	if err == nil && state == provider.StateRunning {
		return fmt.Errorf("VM is running; bhyve does not support hot-eject. Stop the VM first")
	}

	// Find the CD-ROM device to eject. Iterate over DiskPaths, the authoritative
	// axis: DiskDrivers may be shorter, and iterating over it would silently drop
	// any trailing disks that have no matching driver entry.
	deviceFound := false
	newDiskPaths := make([]string, 0, len(config.DiskPaths))
	newDiskDrivers := make([]string, 0, len(config.DiskDrivers))

	for i, diskPath := range config.DiskPaths {
		driver := ""
		if i < len(config.DiskDrivers) {
			driver = config.DiskDrivers[i]
		}
		expectedID := fmt.Sprintf("ahci-cd-%d", i)
		if driver == "ahci-cd" && deviceID == expectedID {
			deviceFound = true
			// Skip this device (effectively ejecting it)
			continue
		}
		newDiskPaths = append(newDiskPaths, diskPath)
		if i < len(config.DiskDrivers) {
			newDiskDrivers = append(newDiskDrivers, driver)
		}
	}

	if !deviceFound {
		return fmt.Errorf("CD-ROM device not found: %s", deviceID)
	}

	config.DiskPaths = newDiskPaths
	config.DiskDrivers = newDiskDrivers

	// Save updated configuration
	if err := p.saveVMConfig(vmDir, config); err != nil {
		return fmt.Errorf("failed to save VM config: %w", err)
	}

	return nil
}

// ListMedia returns a list of all removable media devices attached to the VM.
// For bhyve, this includes all devices using the ahci-cd driver.
func (p *BhyveProvider) ListMedia(ctx context.Context, handle provider.InstanceHandle) ([]provider.MediaInfo, error) {
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Load VM configuration
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load VM config: %w", err)
	}

	var mediaList []provider.MediaInfo

	for i, driver := range config.DiskDrivers {
		if driver == "ahci-cd" {
			mediaInfo := provider.MediaInfo{
				DeviceID: fmt.Sprintf("ahci-cd-%d", i),
				Type:     provider.MediaTypeCDROM,
				Inserted: false,
				Locked:   false,
				Bootable: false,
			}

			// Check if a path is set for this device
			if i < len(config.DiskPaths) && config.DiskPaths[i] != "" {
				mediaInfo.Inserted = true
				mediaInfo.Path = config.DiskPaths[i]
			}

			// Check if this device is in boot order
			for _, bootIdx := range config.BootOrder {
				if bootIdx == i {
					mediaInfo.Bootable = true
					break
				}
			}

			mediaList = append(mediaList, mediaInfo)
		}
	}

	return mediaList, nil
}

// Note: the index-based boot-order helpers (setBootOrderByIndex, using []int
// for disk indices) live in bhyve_boot.go. The MediaProvider-facing SetBootOrder
// and GetBootOrder below translate between provider.BootOrder and those indices.

// SetBootOrder sets the boot device order for the VM.
// Translates provider.BootOrder device types to disk indices stored in VMConfig.
func (p *BhyveProvider) SetBootOrder(ctx context.Context, handle provider.InstanceHandle, order provider.BootOrder) error {
	vmDir := filepath.Join(p.dataDir, handle.ID)
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	// Translate BootDevice types to disk indices.
	// "cdrom"  → first ahci-cd entry in DiskDrivers
	// "disk"   → first non-ahci-cd entry
	// Unknown types default to index 0.
	findIndex := func(dev provider.BootDevice) int {
		for i, drv := range config.DiskDrivers {
			switch dev {
			case provider.BootDeviceCDROM:
				if drv == "ahci-cd" {
					return i
				}
			default: // disk, usb, etc.
				if drv != "ahci-cd" {
					return i
				}
			}
		}
		return 0
	}

	indices := make([]int, 0, len(order.Devices))
	seen := map[int]bool{}
	for _, dev := range order.Devices {
		idx := findIndex(dev)
		if !seen[idx] {
			indices = append(indices, idx)
			seen[idx] = true
		}
	}

	return p.setBootOrderByIndex(ctx, handle, indices)
}

// GetBootOrder returns the current boot device order for the VM.
func (p *BhyveProvider) GetBootOrder(ctx context.Context, handle provider.InstanceHandle) (*provider.BootOrder, error) {
	vmDir := filepath.Join(p.dataDir, handle.ID)
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load VM config: %w", err)
	}

	order := &provider.BootOrder{}
	for _, idx := range config.BootOrder {
		if idx >= 0 && idx < len(config.DiskDrivers) {
			if config.DiskDrivers[idx] == "ahci-cd" {
				order.Devices = append(order.Devices, provider.BootDeviceCDROM)
			} else {
				order.Devices = append(order.Devices, provider.BootDeviceHardDisk)
			}
		}
	}

	return order, nil
}

// Compile-time assertion that BhyveProvider implements MediaProvider.
var _ provider.MediaProvider = (*BhyveProvider)(nil)
