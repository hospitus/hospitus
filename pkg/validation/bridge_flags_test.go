package validation

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestValidateNetworkSpecBridgeFlags verifies bridge port flags are restricted
// to an allow-list, so an API/manifest caller cannot inject arbitrary tokens
// into the root `ifconfig <bridge> <flag> <if>` command (audit HIGH
// network_manager.go:820).
func TestValidateNetworkSpecBridgeFlags(t *testing.T) {
	good := provider.NetworkSpec{BridgeFlags: []string{"private", "-learn", "sticky"}}
	if err := ValidateNetworkSpec(good); err != nil {
		t.Errorf("allowed bridge flags rejected: %v", err)
	}

	bad := []string{"deletem", "descr", "addm", "private; rm -rf /", "-tunnel"}
	for _, f := range bad {
		spec := provider.NetworkSpec{BridgeFlags: []string{f}}
		if err := ValidateNetworkSpec(spec); err == nil {
			t.Errorf("bridge flag %q should be rejected", f)
		}
	}
}
