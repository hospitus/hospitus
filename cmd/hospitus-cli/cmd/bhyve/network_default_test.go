package bhyve

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestCreateGivesAVMANetwork covers a VM created with no network flags.
//
// It got no interface at all: vm.conf recorded taps= empty and
// nat_enabled=false, so the VM booted unreachable and "hospitus bhyve info" never
// showed an address — while the quick start says NAT and DHCP work out of the
// box. There is no flag for NAT either, so a default is the only way to ask for
// it from the command line.
func TestCreateGivesAVMANetwork(t *testing.T) {
	tests := []struct {
		name        string
		bridge      string
		ip          string
		bridgeFlags []string
		wantType    provider.NetworkType
		wantBridge  string
	}{
		{"no flags at all", "", "", nil, provider.NetworkTypeNAT, ""},
		{"a named bridge", "hospitus0", "", nil, provider.NetworkTypeBridge, "hospitus0"},
		{"an address", "", "10.0.0.5/24", nil, provider.NetworkTypeBridge, ""},
		{"bridge flags", "", "", []string{"sticky"}, provider.NetworkTypeBridge, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := networksFor(tt.bridge, tt.ip, tt.bridgeFlags)
			if len(got) != 1 {
				t.Fatalf("networks = %v, want exactly one", got)
			}
			if got[0].Type != tt.wantType {
				t.Errorf("type = %q, want %q", got[0].Type, tt.wantType)
			}
			if got[0].Bridge != tt.wantBridge {
				t.Errorf("bridge = %q, want %q", got[0].Bridge, tt.wantBridge)
			}
		})
	}
}
