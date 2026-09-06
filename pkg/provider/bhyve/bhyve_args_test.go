package bhyve

import (
	"strings"
	"testing"
)

// argsContains returns true if the args slice contains the sequence [flag, value].
func argsContains(args []string, flag, value string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}

// argsContainsSingle returns true when a flag (with no separate value token) is present.
func argsContainsSingle(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// argsContainsSlot returns true when "-s <slot_spec_prefix,...>" is present.
func argsContainsSlotPrefix(args []string, prefix string) bool {
	for i, a := range args {
		if a == "-s" && i+1 < len(args) && strings.HasPrefix(args[i+1], prefix) {
			return true
		}
	}
	return false
}

// joinArgs joins args for diagnostic messages.
func joinArgs(args []string) string { return strings.Join(args, " ") }

func newTestProvider(hasUEFI bool, uefiPath string) *BhyveProvider {
	p := NewBhyveProvider()
	p.hasUEFI = hasUEFI
	p.uefiPath = uefiPath
	return p
}

// ----------------------------------------------------------------------------
// buildBhyveArgs – table-driven
// ----------------------------------------------------------------------------

func TestBuildBhyveArgsTable(t *testing.T) {
	const uefiPath = "/usr/local/share/uefi-firmware/BHYVE_UEFI.fd"

	tests := []struct {
		name   string
		p      func() *BhyveProvider
		config *vmConfig
		checks []func(t *testing.T, args []string)
	}{
		{
			name: "minimal config produces required args",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:     "minimal",
				CPUs:     1,
				MemoryMB: 512,
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if !argsContains(args, "-m", "512M") {
						t.Errorf("expected -m 512M in %s", joinArgs(args))
					}
				},
				func(t *testing.T, args []string) {
					if !argsContains(args, "-c", "cpus=1,sockets=1,cores=1,threads=1") {
						t.Errorf("expected cpu topology arg, got %s", joinArgs(args))
					}
				},
				func(t *testing.T, args []string) {
					if !argsContainsSingle(args, "-H") {
						t.Errorf("expected -H flag in %s", joinArgs(args))
					}
				},
				func(t *testing.T, args []string) {
					if !argsContains(args, "-s", "0:0,hostbridge") {
						t.Errorf("expected hostbridge in %s", joinArgs(args))
					}
				},
				func(t *testing.T, args []string) {
					if args[len(args)-1] != "minimal" {
						t.Errorf("expected VM name as last arg, got %s", args[len(args)-1])
					}
				},
			},
		},
		{
			name: "UEFI boot adds -Y and bootrom",
			p:    func() *BhyveProvider { return newTestProvider(true, uefiPath) },
			config: &vmConfig{
				Name:     "uefi-vm",
				CPUs:     2,
				MemoryMB: 1024,
				UEFIBoot: true,
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if !argsContainsSingle(args, "-Y") {
						t.Errorf("expected -Y for UEFI boot in %s", joinArgs(args))
					}
				},
				func(t *testing.T, args []string) {
					if !argsContains(args, "-l", "bootrom,"+uefiPath) {
						t.Errorf("expected bootrom arg in %s", joinArgs(args))
					}
				},
				func(t *testing.T, args []string) {
					if !argsContains(args, "-s", "31,lpc") {
						t.Errorf("expected LPC device in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "UEFI with separate vars file",
			p:    func() *BhyveProvider { return newTestProvider(true, uefiPath) },
			config: &vmConfig{
				Name:     "uefi-vars",
				CPUs:     2,
				MemoryMB: 1024,
				UEFIBoot: true,
				UEFIVars: "/var/lib/hospitus/bhyve/uefi-vars/VARS.fd",
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					want := "bootrom," + uefiPath + ",/var/lib/hospitus/bhyve/uefi-vars/VARS.fd"
					if !argsContains(args, "-l", want) {
						t.Errorf("expected bootrom+vars arg %q in %s", want, joinArgs(args))
					}
				},
			},
		},
		{
			name: "non-UEFI boot omits -Y and bootrom",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:     "bios-vm",
				CPUs:     1,
				MemoryMB: 512,
				UEFIBoot: false,
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if argsContainsSingle(args, "-Y") {
						t.Errorf("unexpected -Y flag for BIOS boot in %s", joinArgs(args))
					}
				},
				func(t *testing.T, args []string) {
					if argsContainsSingle(args, "-s") {
						for i, a := range args {
							if a == "-s" && i+1 < len(args) && args[i+1] == "31,lpc" {
								t.Errorf("unexpected LPC device for BIOS boot in %s", joinArgs(args))
							}
						}
					}
				},
			},
		},
		{
			name: "MSRIgnoreUnimplemented adds -w",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:                   "msr-vm",
				CPUs:                   2,
				MemoryMB:               1024,
				MSRIgnoreUnimplemented: true,
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if !argsContainsSingle(args, "-w") {
						t.Errorf("expected -w flag in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "passthrough wires -S and passthru slot",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:        "pt-vm",
				CPUs:        1,
				MemoryMB:    512,
				Passthrough: []string{"0/0/0"},
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if !argsContainsSingle(args, "-S") {
						t.Errorf("expected -S for passthrough in %s", joinArgs(args))
					}
				},
				func(t *testing.T, args []string) {
					if !argsContainsSlotPrefix(args, "4:0,passthru,0/0/0") {
						t.Errorf("expected passthru slot in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "VirtioRNG adds virtio-rnd slot",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:      "rng-vm",
				CPUs:      1,
				MemoryMB:  512,
				VirtioRNG: true,
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if !argsContainsSlotPrefix(args, "4:0,virtio-rnd") {
						t.Errorf("expected virtio-rnd in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "USBTablet without VNC adds xhci tablet",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:       "tablet-vm",
				CPUs:       1,
				MemoryMB:   512,
				USBTablet:  true,
				VNCEnabled: false,
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if !argsContainsSlotPrefix(args, "4:0,xhci,tablet") {
						t.Errorf("expected xhci tablet in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "USBTablet with VNC defers tablet to VNC block",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:       "vnc-tablet",
				CPUs:       1,
				MemoryMB:   512,
				USBTablet:  true,
				VNCEnabled: true,
				VNCPort:    5900,
				VNCWidth:   1024,
				VNCHeight:  768,
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					// Tablet must appear (added by VNC block, not the standalone path)
					if !argsContainsSlotPrefix(args, "5:0,xhci,tablet") {
						// slot depends on no disks/taps; fbuf is at slot 4, tablet at 5
						found := false
						for i, a := range args {
							if a == "-s" && i+1 < len(args) && strings.Contains(args[i+1], "xhci,tablet") {
								found = true
								break
							}
						}
						if !found {
							t.Errorf("expected xhci tablet with VNC in %s", joinArgs(args))
						}
					}
				},
			},
		},
		{
			name: "VNC enabled adds fbuf and tablet",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:       "vnc-vm",
				CPUs:       2,
				MemoryMB:   2048,
				VNCEnabled: true,
				VNCPort:    5901,
				VNCWidth:   1920,
				VNCHeight:  1080,
				VNCHost:    "0.0.0.0",
				VNCWait:    false,
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					found := false
					for i, a := range args {
						if a == "-s" && i+1 < len(args) &&
							strings.Contains(args[i+1], "fbuf,tcp=0.0.0.0:5901,w=1920,h=1080") {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected fbuf with custom VNC settings in %s", joinArgs(args))
					}
				},
				func(t *testing.T, args []string) {
					found := false
					for i, a := range args {
						if a == "-s" && i+1 < len(args) && strings.Contains(args[i+1], "xhci,tablet") {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected xhci tablet alongside VNC in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "VNC wait adds wait suffix",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:       "vnc-wait",
				CPUs:       1,
				MemoryMB:   512,
				VNCEnabled: true,
				VNCWait:    true,
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					found := false
					for i, a := range args {
						if a == "-s" && i+1 < len(args) && strings.Contains(args[i+1], ",wait") {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected ,wait in fbuf options in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "VNC defaults to 127.0.0.1 and port 5900",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:       "vnc-defaults",
				CPUs:       1,
				MemoryMB:   512,
				VNCEnabled: true,
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					found := false
					for i, a := range args {
						if a == "-s" && i+1 < len(args) &&
							strings.Contains(args[i+1], "127.0.0.1:5900") &&
							strings.Contains(args[i+1], "w=1024,h=768") {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected default VNC 127.0.0.1:5900 1024×768 in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "TPM enabled adds -l tpm arg",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:        "tpm-vm",
				CPUs:        2,
				MemoryMB:    2048,
				TPMEnabled:  true,
				TPMSockPath: "/var/run/swtpm/tpm.sock",
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					want := "tpm,swtpm,/var/run/swtpm/tpm.sock"
					if !argsContains(args, "-l", want) {
						t.Errorf("expected tpm arg %q in %s", want, joinArgs(args))
					}
				},
			},
		},
		{
			name: "TPM without sockpath is omitted",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:       "notpm-vm",
				CPUs:       1,
				MemoryMB:   512,
				TPMEnabled: true,
				// TPMSockPath intentionally empty
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					for i, a := range args {
						if a == "-l" && i+1 < len(args) && strings.HasPrefix(args[i+1], "tpm,") {
							t.Errorf("unexpected tpm arg when sockpath empty in %s", joinArgs(args))
						}
					}
				},
			},
		},
		{
			name: "console emitted when hasNMDM=true",
			p: func() *BhyveProvider {
				p := newTestProvider(false, "")
				p.hasNMDM = true
				return p
			},
			config: &vmConfig{
				Name:     "console-vm",
				CPUs:     1,
				MemoryMB: 512,
				Console:  "/dev/nmdm0A",
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if !argsContains(args, "-l", "com1,/dev/nmdm0A") {
						t.Errorf("expected console arg in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "console suppressed when hasNMDM=false",
			p:    func() *BhyveProvider { return newTestProvider(false, "") }, // hasNMDM defaults false
			config: &vmConfig{
				Name:     "no-console",
				CPUs:     1,
				MemoryMB: 512,
				Console:  "/dev/nmdm0A",
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					for i, a := range args {
						if a == "-l" && i+1 < len(args) && strings.HasPrefix(args[i+1], "com1,") {
							t.Errorf("unexpected console arg when hasNMDM=false in %s", joinArgs(args))
						}
					}
				},
			},
		},
		{
			name: "custom disk driver overrides default",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:        "nvme-vm",
				CPUs:        1,
				MemoryMB:    512,
				DiskPaths:   []string{"/dev/zvol/zroot/disk0"},
				DiskDrivers: []string{"nvme"},
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if !argsContains(args, "-s", "4:0,nvme,/dev/zvol/zroot/disk0") {
						t.Errorf("expected nvme driver in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "global DiskDriver field overrides default for all disks",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:       "ahci-vm",
				CPUs:       1,
				MemoryMB:   512,
				DiskPaths:  []string{"/var/bhyve/disk0.img"},
				DiskDriver: "ahci-hd",
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if !argsContainsSlotPrefix(args, "4:0,ahci-hd,") {
						t.Errorf("expected ahci-hd driver in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "per-disk driver takes precedence over global DiskDriver",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:        "mixed-drivers",
				CPUs:        1,
				MemoryMB:    512,
				DiskPaths:   []string{"/var/d0.img", "/var/d1.img"},
				DiskDrivers: []string{"nvme", ""},
				DiskDriver:  "ahci-hd",
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					// disk0 should use nvme (per-disk override)
					if !argsContains(args, "-s", "4:0,nvme,/var/d0.img") {
						t.Errorf("expected nvme for disk0 in %s", joinArgs(args))
					}
				},
				func(t *testing.T, args []string) {
					// disk1 has empty per-disk driver → falls back to global ahci-hd
					if !argsContains(args, "-s", "5:0,ahci-hd,/var/d1.img") {
						t.Errorf("expected ahci-hd for disk1 in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "custom NIC driver e1000",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:       "e1000-vm",
				CPUs:       1,
				MemoryMB:   512,
				TapDevs:    []string{"tap0"},
				NICDrivers: []string{"e1000"},
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if !argsContains(args, "-s", "4:0,e1000,tap0") {
						t.Errorf("expected e1000 NIC driver in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "multiple NICs get sequential slots after disks",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:      "multi-nic",
				CPUs:      1,
				MemoryMB:  512,
				DiskPaths: []string{"/var/d0.img"},
				TapDevs:   []string{"tap0", "tap1"},
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					// 1 disk → slot 4; nic0 → slot 5, nic1 → slot 6
					if !argsContains(args, "-s", "5:0,virtio-net,tap0") {
						t.Errorf("expected nic0 at slot 5 in %s", joinArgs(args))
					}
					if !argsContains(args, "-s", "6:0,virtio-net,tap1") {
						t.Errorf("expected nic1 at slot 6 in %s", joinArgs(args))
					}
				},
			},
		},
		{
			name: "USB devices passthrough wires -S",
			p:    func() *BhyveProvider { return newTestProvider(false, "") },
			config: &vmConfig{
				Name:       "usb-pt",
				CPUs:       1,
				MemoryMB:   512,
				USBDevices: []string{"1.2.0"},
			},
			checks: []func(t *testing.T, args []string){
				func(t *testing.T, args []string) {
					if !argsContainsSingle(args, "-S") {
						t.Errorf("expected -S for USB passthrough in %s", joinArgs(args))
					}
				},
				func(t *testing.T, args []string) {
					found := false
					for i, a := range args {
						if a == "-s" && i+1 < len(args) && strings.Contains(args[i+1], "passthru,1.2.0") {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected USB passthru in %s", joinArgs(args))
					}
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, _ := tt.p().buildBhyveArgs(tt.config)
			for _, check := range tt.checks {
				check(t, args)
			}
		})
	}
}

// TestBuildBhyveArgsPCISlotAssignment verifies the PCISlots map is populated correctly.
func TestBuildBhyveArgsPCISlotAssignment(t *testing.T) {
	p := newTestProvider(false, "")
	config := &vmConfig{
		Name:      "slot-test",
		CPUs:      1,
		MemoryMB:  512,
		DiskPaths: []string{"/d0.img", "/d1.img"},
		TapDevs:   []string{"tap0"},
		PCISlots:  make(map[string]string),
	}
	p.buildBhyveArgs(config)

	cases := map[string]string{
		"disk0": "4:0",
		"disk1": "5:0",
		"nic0":  "6:0",
	}
	for dev, want := range cases {
		if got := config.PCISlots[dev]; got != want {
			t.Errorf("PCISlots[%s] = %q, want %q", dev, got, want)
		}
	}
}

// TestBuildBhyveArgsNilPCISlots verifies buildBhyveArgs doesn't panic when PCISlots is nil.
func TestBuildBhyveArgsNilPCISlots(t *testing.T) {
	p := newTestProvider(false, "")
	config := &vmConfig{
		Name:      "nil-slots",
		CPUs:      1,
		MemoryMB:  512,
		DiskPaths: []string{"/d0.img"},
		TapDevs:   []string{"tap0"},
		PCISlots:  nil,
	}
	// Must not panic
	_, _ = p.buildBhyveArgs(config)
}
