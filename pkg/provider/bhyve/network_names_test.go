package bhyve

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/validation"
)

// TestNATBridgeNameIsUsable pins the NAT bridge name against the kernel limit.
//
// FreeBSD caps an interface name at 15 characters, so a name derived from the
// product name is one rename away from being rejected by ifconfig at runtime.
func TestNATBridgeNameIsUsable(t *testing.T) {
	if err := validation.ValidateInterfaceName(natBridge); err != nil {
		t.Errorf("NAT bridge name %q is not a usable interface name: %v", natBridge, err)
	}
}
