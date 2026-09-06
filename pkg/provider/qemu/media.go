package qemu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// This file implements the MediaProvider interface for managing CD-ROM/ISO media
// and boot order, enabling ISO-based OS installation workflows.

// Ensure QEMUProvider implements MediaProvider
var _ provider.MediaProvider = (*QEMUProvider)(nil)

// InsertMedia inserts media (ISO, etc.) into a drive slot.
// For running VMs, this uses QEMU QMP blockdev-change-medium.
// For stopped VMs, this updates the configuration.
func (p *QEMUProvider) InsertMedia(ctx context.Context, handle provider.InstanceHandle, media provider.MediaSpec) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	// Verify media file exists
	if _, err := os.Stat(media.Path); os.IsNotExist(err) {
		return fmt.Errorf("media file not found: %s", media.Path)
	}

	// Validate media path is within allowed directories
	if !p.isPathAllowed(media.Path) {
		return fmt.Errorf("media path not allowed: %s must be under %s or %s", media.Path, p.dataDir, p.imageDir)
	}

	// Check if VM is running
	running, err := p.isVMRunning(ctx, vmName)
	if err != nil {
		return err
	}

	if running {
		// Use QMP to change media on running VM
		return p.qmpInsertMedia(ctx, vmName, media)
	}

	// For stopped VMs, update the configuration
	return p.updateConfigMedia(ctx, vmName, media, true)
}

// EjectMedia ejects media from a drive slot.
func (p *QEMUProvider) EjectMedia(ctx context.Context, handle provider.InstanceHandle, deviceID string) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	// Check if VM is running
	running, err := p.isVMRunning(ctx, vmName)
	if err != nil {
		return err
	}

	if running {
		// Use QMP to eject media on running VM
		return p.qmpEjectMedia(ctx, vmName, deviceID)
	}

	// For stopped VMs, update the configuration to remove media
	return p.updateConfigMedia(ctx, vmName, provider.MediaSpec{DeviceID: deviceID}, false)
}

// ListMedia returns all currently attached media devices.
func (p *QEMUProvider) ListMedia(ctx context.Context, handle provider.InstanceHandle) ([]provider.MediaInfo, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	// Check if VM is running
	running, err := p.isVMRunning(ctx, vmName)
	if err != nil {
		return nil, err
	}

	if running {
		// Query QMP for block devices
		return p.qmpListMedia(ctx, vmName)
	}

	// For stopped VMs, read from configuration
	return p.getConfigMedia(ctx, vmName)
}

// SetBootOrder changes the boot device order.
func (p *QEMUProvider) SetBootOrder(ctx context.Context, handle provider.InstanceHandle, order provider.BootOrder) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	// Check if VM is running
	running, err := p.isVMRunning(ctx, vmName)
	if err != nil {
		return err
	}

	if running {
		// Use QMP to set boot order on running VM
		return p.qmpSetBootOrder(ctx, vmName, order)
	}

	// For stopped VMs, update the configuration
	return p.updateConfigBootOrder(ctx, vmName, order)
}

// GetBootOrder returns the current boot device order.
func (p *QEMUProvider) GetBootOrder(ctx context.Context, handle provider.InstanceHandle) (*provider.BootOrder, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	// Read from configuration
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	config, err := p.loadVMConfig(configPath)
	if err != nil {
		return nil, err
	}

	// Extract boot order from config
	order := &provider.BootOrder{
		Devices: []provider.BootDevice{provider.BootDeviceHardDisk},
	}

	// Check for install_iso which indicates CD-ROM boot
	if _, ok := config.Spec.ProviderConfig["install_iso"].(string); ok {
		order.Devices = []provider.BootDevice{provider.BootDeviceCDROM, provider.BootDeviceHardDisk}
	}

	// Check for explicit boot order in config
	if bootOrder, ok := config.Spec.ProviderConfig["boot_order"].([]interface{}); ok {
		order.Devices = []provider.BootDevice{}
		for _, d := range bootOrder {
			if ds, ok := d.(string); ok {
				order.Devices = append(order.Devices, provider.BootDevice(ds))
			}
		}
	}

	return order, nil
}

// qmpInsertMedia inserts media via QMP on a running VM
func (p *QEMUProvider) qmpInsertMedia(ctx context.Context, vmName string, media provider.MediaSpec) error {
	qmpSocket := filepath.Join(p.dataDir, vmName, "qmp.sock")

	client, err := NewQMPClient(qmpSocket)
	if err != nil {
		return fmt.Errorf("failed to create QMP client: %w", err)
	}
	if err := client.Connect(); err != nil {
		return fmt.Errorf("failed to connect to QMP socket: %w", err)
	}
	defer client.Close()

	deviceID := media.DeviceID
	if deviceID == "" {
		deviceID = "ide0-cd0"
	}

	if err := client.ChangeMedia(deviceID, media.Path); err != nil {
		return fmt.Errorf("failed to insert media: %w", err)
	}

	return nil
}

// qmpEjectMedia ejects media via QMP on a running VM
func (p *QEMUProvider) qmpEjectMedia(ctx context.Context, vmName, deviceID string) error {
	qmpSocket := filepath.Join(p.dataDir, vmName, "qmp.sock")

	client, err := NewQMPClient(qmpSocket)
	if err != nil {
		return fmt.Errorf("failed to create QMP client: %w", err)
	}
	if err := client.Connect(); err != nil {
		return fmt.Errorf("failed to connect to QMP socket: %w", err)
	}
	defer client.Close()

	if deviceID == "" {
		deviceID = "ide0-cd0"
	}

	if err := client.EjectMedia(deviceID); err != nil {
		return fmt.Errorf("failed to eject media: %w", err)
	}

	return nil
}

// qmpListMedia queries block devices via QMP
func (p *QEMUProvider) qmpListMedia(ctx context.Context, vmName string) ([]provider.MediaInfo, error) {
	qmpSocket := filepath.Join(p.dataDir, vmName, "qmp.sock")

	client, err := NewQMPClient(qmpSocket)
	if err != nil {
		return nil, fmt.Errorf("failed to create QMP client: %w", err)
	}
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to QMP socket: %w", err)
	}
	defer client.Close()

	blocks, err := client.QueryBlock()
	if err != nil {
		return nil, fmt.Errorf("failed to query block devices: %w", err)
	}

	var mediaList []provider.MediaInfo
	for _, block := range blocks {
		if !block.Removable {
			continue
		}

		info := provider.MediaInfo{
			DeviceID: block.Device,
			Type:     provider.MediaTypeCDROM,
			Inserted: block.Inserted != nil,
			Locked:   block.Locked,
		}

		if block.Inserted != nil {
			info.Path = block.Inserted.File
		}

		mediaList = append(mediaList, info)
	}

	return mediaList, nil
}

// qmpSetBootOrder sets boot order via QMP on a running VM
func (p *QEMUProvider) qmpSetBootOrder(ctx context.Context, vmName string, order provider.BootOrder) error {
	qmpSocket := filepath.Join(p.dataDir, vmName, "qmp.sock")

	// Convert boot order to QEMU format: d=cdrom, c=disk, n=network
	bootStr := ""
	for _, device := range order.Devices {
		switch device {
		case provider.BootDeviceCDROM:
			bootStr += "d"
		case provider.BootDeviceHardDisk:
			bootStr += "c"
		case provider.BootDeviceNetwork:
			bootStr += "n"
		case provider.BootDeviceFloppy:
			bootStr += "a"
		default:
			return fmt.Errorf("qemu has no boot letter for %s", device)
		}
	}
	if bootStr == "" {
		return fmt.Errorf("boot order names no device qemu can boot from")
	}

	client, err := NewQMPClient(qmpSocket)
	if err != nil {
		return fmt.Errorf("failed to create QMP client: %w", err)
	}
	if err := client.Connect(); err != nil {
		return fmt.Errorf("failed to connect to QMP socket: %w", err)
	}
	defer client.Close()

	if err := client.SetBootOrder(bootStr); err != nil {
		return fmt.Errorf("failed to set boot order: %w", err)
	}

	// Also update config for next boot
	return p.updateConfigBootOrder(ctx, vmName, order)
}

// updateConfigMedia updates the VM configuration for media
func (p *QEMUProvider) updateConfigMedia(ctx context.Context, vmName string, media provider.MediaSpec, insert bool) error {
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	config, err := p.loadVMConfig(configPath)
	if err != nil {
		return err
	}

	if insert {
		// A spec created without provider settings carries a nil map, and
		// writing to one panics.
		if config.Spec.ProviderConfig == nil {
			config.Spec.ProviderConfig = make(map[string]interface{})
		}
		// Add or update install_iso in config
		config.Spec.ProviderConfig["install_iso"] = media.Path
	} else {
		// Remove install_iso from config
		delete(config.Spec.ProviderConfig, "install_iso")
	}

	// Rebuild args to reflect the change
	// This is a simplified approach - ideally we'd parse and modify the args more carefully
	config.Args = p.rebuildArgsWithMedia(config.Args, media.Path, insert)

	// And the recorded boot order with them. GetBootOrder reads boot_order in
	// preference to the install_iso inference, so ejecting a CD left it saying
	// the VM still boots from one while the rewritten -boot argument no longer
	// named it.
	if order, ok := config.Spec.ProviderConfig["boot_order"].([]interface{}); ok {
		kept := make([]interface{}, 0, len(order)+1)
		for _, d := range order {
			if ds, _ := d.(string); provider.BootDevice(ds) == provider.BootDeviceCDROM {
				continue
			}
			kept = append(kept, d)
		}
		if insert {
			kept = append([]interface{}{string(provider.BootDeviceCDROM)}, kept...)
		}
		if len(kept) == 0 {
			delete(config.Spec.ProviderConfig, "boot_order")
		} else {
			config.Spec.ProviderConfig["boot_order"] = kept
		}
	}

	return p.saveVMConfig(config, configPath)
}

// rebuildArgsWithMedia updates QEMU args to add/remove CD-ROM media
func (p *QEMUProvider) rebuildArgsWithMedia(args []string, isoPath string, insert bool) []string {
	var newArgs []string
	skipNext := false
	foundCDROM := false
	foundBoot := false

	for i, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}

		// Look for an existing CD-ROM drive — but never touch the cloud-init
		// seed ISO, which is also a media=cdrom drive. Inserting/ejecting user
		// media must leave the cloud-init CD-ROM intact.
		if arg == "-drive" && i+1 < len(args) &&
			strings.Contains(args[i+1], "media=cdrom") &&
			!strings.Contains(args[i+1], "cloud-init.iso") {
			foundCDROM = true
			if insert {
				// Replace the existing CD-ROM with the new ISO
				newArgs = append(newArgs, "-drive", fmt.Sprintf("file=%s,format=raw,media=cdrom,readonly=on", isoPath))
				skipNext = true
				continue
			} else {
				// Remove the CD-ROM drive (skip both -drive and its argument)
				skipNext = true
				continue
			}
		}

		// The existing -boot is rewritten in place, either way. Leaving it
		// untouched on the insert path and appending another one below produced
		// two -boot options whose values contradicted each other.
		if arg == "-boot" && i+1 < len(args) {
			value := withoutCDROM(args[i+1])
			if insert {
				value = withCDROMFirst(args[i+1])
			}
			if value != "" {
				newArgs = append(newArgs, "-boot", value)
			}
			foundBoot = true
			skipNext = true
			continue
		}

		newArgs = append(newArgs, arg)
	}

	// If inserting and no CD-ROM was found, add one
	if insert && !foundCDROM {
		// order= rather than the legacy letter: rebuildArgsWithBootOrder writes
		// that form, and the two together would leave two -boot options.
		newArgs = append(newArgs, "-drive",
			fmt.Sprintf("file=%s,format=raw,media=cdrom,readonly=on", isoPath))
		if !foundBoot {
			newArgs = append(newArgs, "-boot", "order=dc")
		}
	}

	return newArgs
}

// updateConfigBootOrder updates the VM configuration boot order
func (p *QEMUProvider) updateConfigBootOrder(ctx context.Context, vmName string, order provider.BootOrder) error {
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	config, err := p.loadVMConfig(configPath)
	if err != nil {
		return err
	}

	// loadVMConfig decodes this map from JSON, so it is nil whenever the stored
	// config omits provider_config or holds null — and assigning into a nil map
	// panics.
	if config.Spec.ProviderConfig == nil {
		config.Spec.ProviderConfig = make(map[string]interface{})
	}

	// Store boot order in config
	bootDevices := make([]string, len(order.Devices))
	for i, d := range order.Devices {
		bootDevices[i] = string(d)
	}
	config.Spec.ProviderConfig["boot_order"] = bootDevices

	// Convert to QEMU format for args
	bootStr := ""
	for _, device := range order.Devices {
		switch device {
		case provider.BootDeviceCDROM:
			bootStr += "d"
		case provider.BootDeviceHardDisk:
			bootStr += "c"
		case provider.BootDeviceNetwork:
			bootStr += "n"
		case provider.BootDeviceFloppy:
			bootStr += "a"
		default:
			return fmt.Errorf("qemu has no boot letter for %s", device)
		}
	}
	if bootStr == "" {
		return fmt.Errorf("boot order names no device qemu can boot from")
	}

	// Update -boot argument in args
	config.Args = p.rebuildArgsWithBootOrder(config.Args, bootStr)

	return p.saveVMConfig(config, configPath)
}

// rebuildArgsWithBootOrder updates QEMU args to change boot order
// withoutCDROM returns a -boot value with the cdrom removed, or "" when
// nothing is left to say.
//
// A -boot value is a comma-separated list of sub-options: "order=dc",
// "once=d", "menu=on", "splash=logo.jpg". Treating it as one blob — as this did
// at first — deleted "-boot menu=on" on an eject because it names no order, and
// ran a blind ReplaceAll that stripped the "d" out of "splash=logo.jpg" too.
// Each part is handled on its own.
func withoutCDROM(value string) string {
	parts := strings.Split(value, ",")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		switch {
		case strings.HasPrefix(part, "order="), strings.HasPrefix(part, "once="):
			key, letters, _ := strings.Cut(part, "=")
			if letters = strings.ReplaceAll(letters, "d", ""); letters != "" {
				kept = append(kept, key+"="+letters)
			}
		case !strings.Contains(part, "="):
			// The legacy form: bare device letters.
			if letters := strings.ReplaceAll(part, "d", ""); letters != "" {
				kept = append(kept, letters)
			}
		default:
			// menu=, splash=, strict=, reboot-timeout=: nothing to do with the
			// boot order, and none of our business here.
			kept = append(kept, part)
		}
	}
	// qemu takes "-boot menu=on" with no order at all, so sub-options that
	// survive on their own are kept: only the entries naming the CD-ROM go.
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, ",")
}

// withCDROMFirst returns a -boot value that boots from the cdrom before
// whatever it already named.
func withCDROMFirst(value string) string {
	if value == "" {
		return "order=dc"
	}
	parts := strings.Split(value, ",")
	for i, part := range parts {
		if letters, found := strings.CutPrefix(part, "order="); found {
			parts[i] = "order=d" + strings.ReplaceAll(letters, "d", "")
			return strings.Join(parts, ",")
		}
		if !strings.Contains(part, "=") {
			parts[i] = "d" + strings.ReplaceAll(part, "d", "")
			return strings.Join(parts, ",")
		}
	}
	return strings.Join(append(parts, "order=dc"), ",")
}

func (p *QEMUProvider) rebuildArgsWithBootOrder(args []string, bootStr string) []string {
	var newArgs []string
	foundBoot := false

	for i, arg := range args {
		if arg == "-boot" && i+1 < len(args) {
			// Replace existing -boot argument
			newArgs = append(newArgs, "-boot", fmt.Sprintf("order=%s", bootStr))
			foundBoot = true
			continue
		}

		// Skip the old boot argument value
		if i > 0 && args[i-1] == "-boot" {
			continue
		}

		newArgs = append(newArgs, arg)
	}

	// If no -boot was found, add one
	if !foundBoot && bootStr != "" {
		newArgs = append(newArgs, "-boot", fmt.Sprintf("order=%s", bootStr))
	}

	return newArgs
}

// getConfigMedia reads media configuration from stopped VM
func (p *QEMUProvider) getConfigMedia(ctx context.Context, vmName string) ([]provider.MediaInfo, error) {
	configPath := filepath.Join(p.stateDir, fmt.Sprintf("%s.json", vmName))
	config, err := p.loadVMConfig(configPath)
	if err != nil {
		return nil, err
	}

	var mediaList []provider.MediaInfo

	// Check for install_iso
	if isoPath, ok := config.Spec.ProviderConfig["install_iso"].(string); ok && isoPath != "" {
		mediaList = append(mediaList, provider.MediaInfo{
			DeviceID: "ide0-cd0",
			Type:     provider.MediaTypeCDROM,
			Path:     isoPath,
			Inserted: true,
			Bootable: true,
		})
	}

	// Check for cloud-init ISO
	cloudInitISO := filepath.Join(p.dataDir, vmName, "cloud-init.iso")
	if _, err := os.Stat(cloudInitISO); err == nil {
		mediaList = append(mediaList, provider.MediaInfo{
			DeviceID: "ide1-cd0",
			Type:     provider.MediaTypeCDROM,
			Path:     cloudInitISO,
			Inserted: true,
			Bootable: false,
		})
	}

	return mediaList, nil
}
