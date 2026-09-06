package vfkit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func testProvider(t *testing.T) *VFKitProvider {
	t.Helper()

	dir := t.TempDir()
	return &VFKitProvider{
		dataDir:  filepath.Join(dir, "data"),
		stateDir: filepath.Join(dir, "state"),
		imageDir: filepath.Join(dir, "images"),
		vfkitBin: "/opt/homebrew/bin/vfkit",
		runner:   &execx.Fake{},
	}
}

// TestBuildArgs pins the command line, which is the whole contract with vfkit.
func TestBuildArgs(t *testing.T) {
	p := testProvider(t)
	config := &vmConfig{
		Name:       "vm1",
		CPUs:       2,
		MemoryMB:   2048,
		DiskPath:   "/vms/vm1/disk.img",
		EFIStore:   "/vms/vm1/efistore.nvram",
		MACAddress: "2e:cf:f3:c4:48:66",
	}

	args := strings.Join(p.buildArgs(config), " ")

	for _, want := range []string{
		"--cpus 2",
		"--memory 2048",
		// create makes the store on first boot and reuses it after, so guest boot
		// entries survive a restart.
		"--bootloader efi,variable-store=/vms/vm1/efistore.nvram,create",
		"--device virtio-blk,path=/vms/vm1/disk.img",
		"--device virtio-net,nat,mac=2e:cf:f3:c4:48:66",
		"--restful-uri unix://",
		"--pidfile ",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("command line is missing %q:\n%s", want, args)
		}
	}
}

// TestNormalizeVMState covers the difference between what vfkit documents and
// what it sends: the documented "Running" never appears on the wire.
func TestNormalizeVMState(t *testing.T) {
	cases := map[string]string{
		"VirtualMachineStateRunning": "Running",
		"VirtualMachineStateStopped": "Stopped",
		"VirtualMachineStatePaused":  "Paused",
		"Running":                    "Running",
		"":                           "",
	}
	for in, want := range cases {
		if got := normalizeVMState(in); got != want {
			t.Errorf("normalizeVMState(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRandomMACIsLocallyAdministered checks the two bits that decide whether an
// address may be used on a local network without colliding with real hardware.
func TestRandomMACIsLocallyAdministered(t *testing.T) {
	mac, err := randomMAC()
	if err != nil {
		t.Fatalf("randomMAC: %v", err)
	}

	var first int
	if _, err := fmtSscanf(mac, &first); err != nil {
		t.Fatalf("unparseable MAC %q: %v", mac, err)
	}
	if first&0x02 == 0 {
		t.Errorf("MAC %q is not locally administered", mac)
	}
	if first&0x01 != 0 {
		t.Errorf("MAC %q is a multicast address", mac)
	}

	other, err := randomMAC()
	if err != nil {
		t.Fatalf("randomMAC: %v", err)
	}
	if mac == other {
		t.Error("two VMs would share a MAC address")
	}
}

func TestResolveImage(t *testing.T) {
	p := testProvider(t)
	cloudDir := filepath.Join(p.imageDir, "cloud")
	if err := os.MkdirAll(cloudDir, 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	imagePath := filepath.Join(cloudDir, "alpine-3.20-arm64.qcow2")
	if err := os.WriteFile(imagePath, []byte("image"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := p.resolveImage("alpine-3.20-arm64")
	if err != nil {
		t.Fatalf("resolveImage: %v", err)
	}
	if got != imagePath {
		t.Errorf("resolveImage = %q, want %q", got, imagePath)
	}

	// An image that is not there must say how to get it.
	_, err = p.resolveImage("not-downloaded")
	if err == nil {
		t.Fatal("resolveImage on a missing image returned nil")
	}
	if !strings.Contains(err.Error(), "hospitus image fetch") {
		t.Errorf("error should point at the fetch command, got: %v", err)
	}
}

// The framework runs guest instructions on the host CPU, so claiming
// cross-architecture support would let the API accept VMs that cannot boot.
func TestCapabilitiesRejectCrossArch(t *testing.T) {
	caps := testProvider(t).Capabilities()

	if caps.SupportsCrossArch {
		t.Error("vfkit cannot emulate a foreign architecture")
	}
	if len(caps.SupportedArchitectures) != 1 {
		t.Errorf("expected exactly the host architecture, got %v", caps.SupportedArchitectures)
	}
	if caps.SupportsSnapshots {
		t.Error("Virtualization.framework has no snapshots")
	}
}

// Operations the framework cannot perform must fail as unsupported rather than
// report success for something that did not happen.
func TestUnsupportedOperationsAreReported(t *testing.T) {
	p := testProvider(t)
	handle := provider.InstanceHandle{ID: "vm1", Provider: "vfkit"}
	ctx := context.Background()

	tests := map[string]error{
		"AttachDisk":           p.AttachDisk(ctx, handle, provider.DiskAttachment{}),
		"DetachDisk":           p.DetachDisk(ctx, handle, "disk0"),
		"AttachNetwork":        p.AttachNetwork(ctx, handle, provider.NetworkAttachment{}),
		"DetachNetwork":        p.DetachNetwork(ctx, handle, "net0"),
		"SetInstanceResources": p.SetInstanceResources(ctx, handle, provider.ResourceSpec{}),
	}

	for name, err := range tests {
		if err == nil {
			t.Errorf("%s returned nil for an operation vfkit cannot perform", name)
			continue
		}
		if !strings.Contains(err.Error(), "not supported") {
			t.Errorf("%s should report the operation as unsupported, got: %v", name, err)
		}
	}
}

// fmtSscanf reads the first hex octet of a MAC address.
func fmtSscanf(mac string, out *int) (int, error) {
	var value int
	for _, c := range mac[:2] {
		value *= 16
		switch {
		case c >= '0' && c <= '9':
			value += int(c - '0')
		case c >= 'a' && c <= 'f':
			value += int(c-'a') + 10
		default:
			return 0, os.ErrInvalid
		}
	}
	*out = value
	return 1, nil
}
