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
}
