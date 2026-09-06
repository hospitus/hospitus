package jail

import (
	"testing"
)

func TestNetworkInterfaceAdvanced(t *testing.T) {
	iface := NetworkInterface{
		Name:        "lan0",
		Bridge:      "hospitus0",
		BridgeFlags: []string{"private"},
		VLAN:        100,
	}

	if len(iface.BridgeFlags) != 1 || iface.BridgeFlags[0] != "private" {
		t.Error("NetworkInterface BridgeFlags mismatch")
	}
	if iface.VLAN != 100 {
		t.Error("NetworkInterface VLAN mismatch")
	}

	// The assertions above only read back the literals this test just wrote, so
	// they exercise no package logic. Put the value through something that does:
	// validateNetworkInterface is what every caller-supplied interface passes.
	if err := validateNetworkInterface("web", iface); err != nil {
		t.Errorf("a struct the tests treat as valid is rejected by validation: %v", err)
	}
}
