package bhyve

import (
	"path/filepath"
	"testing"
)

// TestRepointIntoNewVMDir covers what made a renamed VM unbootable: rename
// rewrote the zvol paths, the taps and the console, but not the UEFI variable
// store, so bhyve got a bootrom argument naming a directory that was gone.
func TestRepointIntoNewVMDir(t *testing.T) {
	oldDir := filepath.Join("/var/lib/hospitus/bhyve", "windows")
	newDir := filepath.Join("/var/lib/hospitus/bhyve", "win11")

	config := &vmConfig{
		Name:        "windows",
		UEFIVars:    filepath.Join(oldDir, "uefi_vars.fd"),
		TPMSockPath: filepath.Join(oldDir, "tpm", "swtpm.sock"),
		DiskPaths: []string{
			"/dev/zvol/zroot/hospitus/bhyve/win11/disk0", // already renamed with the dataset
			filepath.Join(oldDir, "seed.iso"),            // lives in the VM directory
			"/var/lib/hospitus/images/install.iso",       // outside: must not move
			"/dev/nda0",                                  // a passthrough device: must not move
		},
	}

	repointIntoNewVMDir(config, oldDir, newDir)

	if want := filepath.Join(newDir, "uefi_vars.fd"); config.UEFIVars != want {
		t.Errorf("UEFIVars = %q, want %q", config.UEFIVars, want)
	}
	if want := filepath.Join(newDir, "tpm", "swtpm.sock"); config.TPMSockPath != want {
		t.Errorf("TPMSockPath = %q, want %q", config.TPMSockPath, want)
	}
	if want := filepath.Join(newDir, "seed.iso"); config.DiskPaths[1] != want {
		t.Errorf("disk in the VM directory = %q, want %q", config.DiskPaths[1], want)
	}
	if want := "/var/lib/hospitus/images/install.iso"; config.DiskPaths[2] != want {
		t.Errorf("an image outside the VM directory was moved: %q", config.DiskPaths[2])
	}
	if want := "/dev/nda0"; config.DiskPaths[3] != want {
		t.Errorf("a passthrough device was rewritten: %q", config.DiskPaths[3])
	}
}

// TestRepointIntoNewVMDirLeavesSiblingsAlone checks the prefix is compared with
// its separator: /var/lib/hospitus/bhyve/web must not match web2.
func TestRepointIntoNewVMDirLeavesSiblingsAlone(t *testing.T) {
	config := &vmConfig{UEFIVars: "/var/lib/hospitus/bhyve/web2/uefi_vars.fd"}
	repointIntoNewVMDir(config, "/var/lib/hospitus/bhyve/web", "/var/lib/hospitus/bhyve/api")
	if config.UEFIVars != "/var/lib/hospitus/bhyve/web2/uefi_vars.fd" {
		t.Errorf("a sibling VM's path was rewritten: %q", config.UEFIVars)
	}
}

// TestTapDeviceNamesAreDistinct covers the collision that made two VMs share
// one interface: FreeBSD caps a name at 15 characters, and truncating to fit
// gave two VMs with a common prefix the same tap.
func TestTapDeviceNamesAreDistinct(t *testing.T) {
	a := tapDeviceName("webserver-prod-a", 0)
	b := tapDeviceName("webserver-prod-b", 0)
	if a == b {
		t.Errorf("two VMs share the tap %q", a)
	}
	for _, name := range []string{a, b} {
		if len(name) > 15 {
			t.Errorf("tap name %q is %d characters; FreeBSD allows 15", name, len(name))
		}
	}

	// A short name still reads as the VM it belongs to, which is what makes
	// ifconfig output legible.
	if got, want := tapDeviceName("web", 0), "tap_web_0"; got != want {
		t.Errorf("tapDeviceName(web, 0) = %q, want %q", got, want)
	}

	// The same VM and index always produce the same name: a start after a
	// reboot has to find the interface the config recorded.
	if tapDeviceName("webserver-prod-a", 1) == tapDeviceName("webserver-prod-a", 0) {
		t.Error("two interfaces of one VM share a name")
	}
	if tapDeviceName("webserver-prod-a", 0) != a {
		t.Error("the name is not stable for the same VM")
	}
}
