package bhyve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
)

// GPU and USB Passthrough

// PassthroughDevice represents a PCI device to pass through to the VM.
type PassthroughDevice struct {
	Type     string `json:"type"`      // gpu, usb, network, storage
	PCISlot  string `json:"pci_slot"`  // PCI address (e.g., "0000:01:00.0")
	DeviceID string `json:"device_id"` // Device identifier
}

// AttachPassthroughDevice attaches a PCI device to a VM for passthrough.
//
// This implements CBSD's PCI passthrough functionality:
//   - GPU passthrough for gaming/graphics workloads
//   - USB controller passthrough for device access
//   - Network adapter passthrough for performance
//   - Storage controller passthrough
//
// Requirements:
//   - VT-d/AMD-Vi must be enabled in BIOS
//   - Device must support IOMMU
//   - Device must not be in use by host
//
// Note: Requires VM restart to take effect.
func (p *BhyveProvider) AttachPassthroughDevice(ctx context.Context, handle provider.InstanceHandle, device PassthroughDevice) error {
	vmName := handle.ID

	// SECURITY: Validate PCI slot format
	if device.PCISlot == "" {
		return fmt.Errorf("PCI slot is required")
	}

	// Validate PCI slot format (e.g., "0000:01:00.0")
	if !strings.Contains(device.PCISlot, ":") {
		return fmt.Errorf("invalid PCI slot format: %s (expected format: 0000:01:00.0)", device.PCISlot)
	}

	// Check if device exists
	if err := p.validatePCIDevice(ctx, device.PCISlot); err != nil {
		return fmt.Errorf("invalid PCI device: %w", err)
	}

	// Store the passthrough device in a separate metadata file. VMConfig.Passthrough
	// only holds bare PCI-slot strings for argument building; the full
	// PassthroughDevice records (type, PCI slot, device id) are persisted here.
	vmDir := filepath.Join(p.dataDir, vmName)
	passthroughPath := filepath.Join(vmDir, "passthrough.json")

	// Load existing passthrough config. A corrupt file must not be silently
	// discarded: doing so would erase every previously attached device.
	var devices []PassthroughDevice
	if data, err := os.ReadFile(passthroughPath); err == nil {
		if err := json.Unmarshal(data, &devices); err != nil {
			return fmt.Errorf("passthrough config %s is corrupt: %w", passthroughPath, err)
		}
	}

	// Add new device
	devices = append(devices, device)

	// Save passthrough config
	data, err := json.MarshalIndent(devices, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal passthrough config: %w", err)
	}

	if err := os.WriteFile(passthroughPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to save passthrough config: %w", err)
	}

	return nil
}

// DetachPassthroughDevice removes a PCI passthrough device from a VM.
func (p *BhyveProvider) DetachPassthroughDevice(ctx context.Context, handle provider.InstanceHandle, pciSlot string) error {
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Load passthrough config
	passthroughPath := filepath.Join(vmDir, "passthrough.json")
	var devices []PassthroughDevice

	data, err := os.ReadFile(passthroughPath)
	if err != nil {
		return fmt.Errorf("no passthrough devices configured")
	}

	if err := json.Unmarshal(data, &devices); err != nil {
		return fmt.Errorf("failed to parse passthrough config: %w", err)
	}

	// Remove device
	found := false
	newDevices := []PassthroughDevice{}
	for _, dev := range devices {
		if dev.PCISlot != pciSlot {
			newDevices = append(newDevices, dev)
		} else {
			found = true
		}
	}

	if !found {
		return fmt.Errorf("passthrough device not found: %s", pciSlot)
	}

	// Save updated config
	newData, err := json.MarshalIndent(newDevices, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal passthrough config: %w", err)
	}

	if err := os.WriteFile(passthroughPath, newData, 0o600); err != nil {
		return fmt.Errorf("failed to save passthrough config: %w", err)
	}

	return nil
}

// ListPassthroughDevices returns a list of PCI devices configured for passthrough.
func (p *BhyveProvider) ListPassthroughDevices(ctx context.Context, handle provider.InstanceHandle) ([]PassthroughDevice, error) {
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Load passthrough config
	passthroughPath := filepath.Join(vmDir, "passthrough.json")
	var devices []PassthroughDevice

	data, err := os.ReadFile(passthroughPath)
	if err != nil {
		// No passthrough devices configured
		return []PassthroughDevice{}, nil
	}

	if err := json.Unmarshal(data, &devices); err != nil {
		return nil, fmt.Errorf("failed to parse passthrough config: %w", err)
	}

	return devices, nil
}

// mergeRuntimePassthrough reads passthrough.json (populated by
// AttachPassthroughDevice) and appends the devices to config.Passthrough so the
// bhyve command line includes them. PCI slots are converted from the
// "domain:bus:device.function" form returned by pciconf into the
// "bus/device/function" form bhyve(8) expects for passthru. Devices already
// present in config.Passthrough are not duplicated.
func (p *BhyveProvider) mergeRuntimePassthrough(vmDir string, config *vmConfig) error {
	passthroughPath := filepath.Join(vmDir, "passthrough.json")
	data, err := os.ReadFile(passthroughPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read passthrough config: %w", err)
	}

	var devices []PassthroughDevice
	if err := json.Unmarshal(data, &devices); err != nil {
		return fmt.Errorf("failed to parse passthrough config: %w", err)
	}

	existing := make(map[string]struct{}, len(config.Passthrough))
	for _, slot := range config.Passthrough {
		existing[slot] = struct{}{}
	}

	for _, dev := range devices {
		slot, err := pciSlotToBhyve(dev.PCISlot)
		if err != nil {
			return fmt.Errorf("device %s: %w", dev.PCISlot, err)
		}
		if _, ok := existing[slot]; ok {
			continue
		}
		existing[slot] = struct{}{}
		config.Passthrough = append(config.Passthrough, slot)
	}

	return nil
}

// pciSlotToBhyve converts a PCI slot from "domain:bus:device.function"
// (e.g. "0000:01:00.0") into the "bus/device/function" form bhyve(8) requires
// for passthru (e.g. "1/0/0"). It also accepts an already-converted value.
func pciSlotToBhyve(slot string) (string, error) {
	// Already in bhyve form (bus/device/function).
	if strings.Contains(slot, "/") {
		return slot, nil
	}

	parts := strings.Split(slot, ":")
	// Accept either "domain:bus:device.function" or "bus:device.function".
	var bus, devFunc string
	switch len(parts) {
	case 3:
		bus, devFunc = parts[1], parts[2]
	case 2:
		bus, devFunc = parts[0], parts[1]
	default:
		return "", fmt.Errorf("invalid PCI slot format: %q (expected domain:bus:device.function)", slot)
	}

	df := strings.Split(devFunc, ".")
	if len(df) != 2 {
		return "", fmt.Errorf("invalid PCI device.function in slot: %q", slot)
	}

	bus = strings.TrimLeft(bus, "0")
	if bus == "" {
		bus = "0"
	}
	dev := strings.TrimLeft(df[0], "0")
	if dev == "" {
		dev = "0"
	}
	fn := strings.TrimLeft(df[1], "0")
	if fn == "" {
		fn = "0"
	}

	return fmt.Sprintf("%s/%s/%s", bus, dev, fn), nil
}

// validatePCIDevice checks if a PCI device exists on the system.
func (p *BhyveProvider) validatePCIDevice(ctx context.Context, pciSlot string) error {
	// Use pciconf to check if device exists
	cmd := exec.CommandContext(ctx, "pciconf", "-l")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to list PCI devices: %w", err)
	}

	// Search for PCI slot in output
	if !strings.Contains(string(output), pciSlot) {
		return fmt.Errorf("PCI device not found: %s", pciSlot)
	}

	return nil
}

// ListAvailablePCIDevices returns a list of PCI devices available for passthrough.
func (p *BhyveProvider) ListAvailablePCIDevices(ctx context.Context) ([]PCIDeviceInfo, error) {
	// Use pciconf to list all PCI devices
	cmd := exec.CommandContext(ctx, "pciconf", "-l", "-v")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list PCI devices: %w", err)
	}

	devices := []PCIDeviceInfo{}
	lines := strings.Split(string(output), "\n")

	var currentDevice *PCIDeviceInfo
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Device line format: "vendor@pci0:0:0:0:   class=0x060000 ..."
		if strings.Contains(line, "@pci") {
			if currentDevice != nil {
				devices = append(devices, *currentDevice)
			}
			currentDevice = &PCIDeviceInfo{}

			// Parse PCI slot
			parts := strings.Split(line, "@")
			if len(parts) >= 2 {
				pciPart := strings.Split(parts[1], ":")
				if len(pciPart) >= 4 {
					// pciPart[3] is like "0:\tclass=..."; take its first
					// whitespace-delimited field, guarding against an empty field.
					if funcFields := strings.Fields(pciPart[3]); len(funcFields) > 0 {
						currentDevice.PCISlot = fmt.Sprintf("0000:%s:%s.%s", pciPart[1], pciPart[2], funcFields[0])
					}
				}
			}
		} else if currentDevice != nil {
			// Parse device information. Guard the split so a line without "="
			// cannot panic on an out-of-range index.
			kv := strings.SplitN(line, "=", 2)
			if len(kv) != 2 {
				continue
			}
			value := strings.TrimSpace(kv[1])
			switch {
			case strings.Contains(line, "vendor"):
				currentDevice.Vendor = value
			case strings.Contains(line, "device"):
				currentDevice.Device = value
			case strings.Contains(line, "class"):
				currentDevice.Class = value
			}
		}
	}

	if currentDevice != nil {
		devices = append(devices, *currentDevice)
	}

	return devices, nil
}

// PCIDeviceInfo represents information about a PCI device.
type PCIDeviceInfo struct {
	PCISlot string `json:"pci_slot"`
	Vendor  string `json:"vendor"`
	Device  string `json:"device"`
	Class   string `json:"class"`
}
