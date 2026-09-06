package bhyve

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hospitus/hospitus/pkg/validation"
)

// Guards for the values that become bhyve(8) arguments.
//
// bhyve takes its devices as comma-separated slot specifications —
// `-s 5:0,passthru,1/0/0`, `-s 3:0,ahci-hd,/dev/nda0` — so a value carrying a
// comma adds an option rather than a value, and a device path names something
// the guest then reads and writes directly. Everything below is applied where
// the value enters: at create, at import, and again at start for a config that
// was written by some other means.

// pciSelectorRE matches the bus/device/function selector bhyve expects for a
// passthrough device, in either the slash or the dot spelling. Anything else —
// a comma, an option, a path — is refused.
// One separator style for the whole selector: "1/0.0" and "1.0/0" matched the
// mixed form, passed validation, and then failed at start with an opaque bhyve
// error instead of being refused where the value entered.
var pciSelectorRE = regexp.MustCompile(`^\d{1,4}(?:/\d{1,3}/\d{1,3}|\.\d{1,3}\.\d{1,3})$`)

// tapNameRE matches the tap devices this provider creates and destroys.
// Requiring the prefix keeps `ifconfig <name> destroy`, which import and delete
// both run, off an interface Hospitus never made: an imported archive naming
// lagg0 or the host's uplink would otherwise tear it down.
var tapNameRE = regexp.MustCompile(`^tap[a-zA-Z0-9_.-]*$`)

// bhyveDiskDrivers is the set of emulations this provider knows how to attach.
// The value is written into a slot specification verbatim.
// The spellings are the ones getBhyveDiskDriver maps; anything else was never
// honored, it only reached the config file and was silently replaced by the
// default at start.
var bhyveDiskDrivers = map[string]bool{
	"virtio-blk": true,
	"virtio":     true,
	"ahci-hd":    true,
	"ahci":       true,
	"ahci-cd":    true,
	"nvme":       true,
}

// validateDiskDriver refuses a disk emulation bhyve would not understand, or
// one carrying an option of its own.
func validateDiskDriver(driver string) error {
	if !bhyveDiskDrivers[driver] {
		return fmt.Errorf("unsupported disk driver %q (use virtio-blk, ahci-hd, ahci-cd or nvme)", driver)
	}
	return nil
}

// validatePassthroughSelector refuses anything that is not a bare PCI selector.
//
// A value like "1/0/0,rom=/etc/master.passwd" would otherwise reach bhyve as a
// passthru device plus an option naming a host file to load as an option ROM.
func validatePassthroughSelector(kind, selector string) error {
	if !pciSelectorRE.MatchString(selector) {
		return fmt.Errorf("invalid %s device %q: expected a PCI selector such as 1/0/0", kind, selector)
	}
	return nil
}

// validateTapName refuses a tap device name this provider would not have made.
func validateTapName(tap string) error {
	if tap == "" {
		return fmt.Errorf("empty tap device name")
	}
	if !tapNameRE.MatchString(tap) {
		return fmt.Errorf("invalid tap device %q: Hospitus only manages interfaces named tap*", tap)
	}
	return nil
}

// validatePhysicalDisk reports whether a device may be handed to a guest.
//
// Passthrough is default-deny: the operator names each device in
// allowed_physical_disks, because the alternative is an API caller attaching
// the host's own system disk to a VM they control. The check resolves symlinks
// so /dev/diskid/... and its alias are the same decision.
func (p *BhyveProvider) validatePhysicalDisk(path string) error {
	if path == "" {
		return fmt.Errorf("physical disk requires 'path' to be specified (e.g., /dev/ada2)")
	}
	if !strings.HasPrefix(path, "/dev/") {
		return fmt.Errorf("physical disk path must be in /dev/ (got: %s)", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("physical disk device not found: %s (%w)", path, err)
	}
	if info.Mode()&os.ModeDevice == 0 {
		return fmt.Errorf("path is not a device: %s", path)
	}

	resolved := path
	if r, rerr := filepath.EvalSymlinks(path); rerr == nil {
		resolved = r
	}
	if !physicalDiskAllowed(p.allowedPhysicalDisks(), path, resolved) {
		return fmt.Errorf("physical disk %q is not allow-listed for passthrough (name it in allowed_physical_disks in hospitusd.conf, then restart hospitusd)", path)
	}
	return nil
}

// validateRuntimeConfig re-checks the parts of a stored VM configuration that
// decide what the guest can reach, at the two moments the configuration did not
// come from CreateInstance: an archive being imported, and a start reading a
// vm.conf written earlier or by hand.
//
// It deliberately does not run on load: a VM whose device left the allow-list
// must still be listable and deletable, it just must not run.
func (p *BhyveProvider) validateRuntimeConfig(config *vmConfig) error {
	// bhyve's device arguments are comma-separated key=value lists, so a comma
	// in any of these adds an option the caller never asked for — a second
	// backing file, another slot. The disk paths below are checked separately.
	for name, value := range map[string]string{
		"disk_driver":   config.DiskDriver,
		"uefi_vars":     config.UEFIVars,
		"tpm_sock_path": config.TPMSockPath,
		"console":       config.Console,
		"vnc_host":      config.VNCHost,
	} {
		if strings.ContainsAny(value, ",\r\n") {
			return fmt.Errorf("%s %q contains a comma or a line break, which bhyve would read as another option", name, value)
		}
	}
	if config.DiskDriver != "" {
		if err := validateDiskDriver(config.DiskDriver); err != nil {
			return err
		}
	}
	// The bind policy again, from what vm.conf records rather than from the
	// spec that created the VM: a hand-edited config could otherwise set
	// vnc_host=0.0.0.0 and buildBhyveArgs would emit an unauthenticated
	// listener at the next start.
	if config.VNCEnabled {
		if err := validateVNCHost(config.VNCHost, config.VNCInsecure); err != nil {
			return fmt.Errorf("VNC security error: %w", err)
		}
	}

	for i, disk := range config.DiskPaths {
		disk = strings.TrimSpace(disk)
		if disk == "" {
			continue
		}
		if i < len(config.DiskDrivers) && config.DiskDrivers[i] != "" {
			if err := validateDiskDriver(config.DiskDrivers[i]); err != nil {
				return err
			}
		}
		// A zvol is one of ours; a plain file was confined when it was
		// attached. Only a raw device needs the operator's consent.
		if strings.HasPrefix(disk, "/dev/") && !strings.HasPrefix(disk, "/dev/zvol/") {
			if err := p.validatePhysicalDisk(disk); err != nil {
				return err
			}
		}
	}

	for _, tap := range config.TapDevs {
		if tap == "" {
			continue
		}
		if err := validateTapName(tap); err != nil {
			return err
		}
	}

	for _, bridge := range config.Bridges {
		if bridge == "" {
			continue
		}
		if err := validation.ValidateBridgeName(bridge); err != nil {
			return err
		}
	}

	for _, pt := range config.Passthrough {
		if err := validatePassthroughSelector("passthrough", pt); err != nil {
			return err
		}
	}
	for _, usb := range config.USBDevices {
		if err := validatePassthroughSelector("usb", usb); err != nil {
			return err
		}
	}

	return nil
}
