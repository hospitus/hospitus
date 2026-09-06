package bhyve

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// TestValidatePCIDeviceMatchesRealPciconfOutput covers a check that could never
// succeed.
//
// pciconf(8) writes "name@pci<domain>:<bus>:<dev>:<func>:" while a passthrough
// slot is bhyve's "bus/dev/func", so the substring search across those two
// spellings never matched and every device was reported as absent. The fixture
// is real output from a FreeBSD host.
func TestValidatePCIDeviceMatchesRealPciconfOutput(t *testing.T) {
	const pciconf = "hostb0@pci0:0:0:0:\tclass=0x060000 rev=0x00 hdr=0x00 vendor=0x1022\n" +
		"amdviiommu0@pci0:0:0:2:\tclass=0x080600 rev=0x00 hdr=0x00 vendor=0x1022\n" +
		"vgapci0@pci0:3:0:0:\tclass=0x030000 rev=0xc1 hdr=0x00 vendor=0x10de\n"

	p := &BhyveProvider{runner: &execx.Fake{
		Func: func(string, []string) ([]byte, error) { return []byte(pciconf), nil },
	}}

	// The three spellings pciSelectorRE and the config accept, all naming the
	// GPU on the third bus.
	for _, slot := range []string{"3/0/0", "3.0.0", "0:3:0.0"} {
		if err := p.validatePCIDevice(context.Background(), slot); err != nil {
			t.Errorf("validatePCIDevice(%q) = %v, want the device found", slot, err)
		}
	}

	// And a device the host does not have is still refused — a check that
	// accepts everything is no better than one that accepts nothing.
	for _, slot := range []string{"9/9/9", "0/0/1"} {
		if err := p.validatePCIDevice(context.Background(), slot); err == nil {
			t.Errorf("validatePCIDevice(%q) accepted a device the host does not have", slot)
		}
	}
}
