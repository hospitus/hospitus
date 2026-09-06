package jail

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestBridgePoolFor covers which bridge may take its gateway from the default
// pool. Lending that pool to a named bridge puts the default gateway on two
// bridges at once, which the conflict check rejects: a stack declaring its
// subnet on only one of the services sharing a bridge then fails to start.
func TestBridgePoolFor(t *testing.T) {
	t.Setenv("HOSPITUS_IP_POOL", "10.0.0.0/24")

	p := &JailProvider{
		config: provider.ProviderConfig{Settings: make(map[string]interface{})},
	}

	tests := []struct {
		name         string
		bridge       string
		declaredPool string
		want         string
	}{
		{
			name:         "declared pool wins on a named bridge",
			bridge:       "guacbr0",
			declaredPool: "10.40.0.0/24",
			want:         "10.40.0.0/24",
		},
		{
			name:         "declared pool wins on the default bridge",
			bridge:       "hospitus0",
			declaredPool: "10.40.0.0/24",
			want:         "10.40.0.0/24",
		},
		{
			name:   "default bridge falls back to the default pool",
			bridge: "hospitus0",
			want:   "10.0.0.0/24",
		},
		{
			name:   "named bridge without a pool gets none",
			bridge: "guacbr0",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.bridgePoolFor(tt.bridge, tt.declaredPool, "hospitus0")
			if got != tt.want {
				t.Errorf("bridgePoolFor(%q, %q) = %q, want %q",
					tt.bridge, tt.declaredPool, got, tt.want)
			}
		})
	}
}

// TestDefaultRouteGoesToTheBridgeWithAWayOut covers a jail on two networks.
//
// The default route was decided twice — once for the rc.conf entry, once for the
// route installed after start — and each network got one. The kernel used
// whichever arrived first. A jail whose manifest gives it an internal segment
// with no gateway beyond it, plus a second interface for the way out, sent
// everything to the internal one: every name lookup failed, and pkg reported
// "Address family for host not supported".
func TestDefaultRouteGoesToTheBridgeWithAWayOut(t *testing.T) {
	internal := provider.NetworkSpec{Bridge: "wpint", IPv4: "10.31.0.2/24"}
	public := provider.NetworkSpec{Bridge: "hospitus0", IPv4: "10.0.0.7/24"}

	tests := []struct {
		name     string
		networks []provider.NetworkSpec
		want     int
	}{
		{"the NAT bridge wins over an internal segment", []provider.NetworkSpec{internal, public}, 1},
		{"whatever its order", []provider.NetworkSpec{public, internal}, 0},
		{
			"the first addressed one when none is on it",
			[]provider.NetworkSpec{internal, {Bridge: "other", IPv4: "10.40.0.2/24"}},
			0,
		},
		{
			"a network with no address does not take it",
			[]provider.NetworkSpec{{Bridge: "wpint"}, public},
			1,
		},
		{
			"no route at all rather than one at random",
			[]provider.NetworkSpec{{Bridge: "wpint"}, {Bridge: "hospitus0"}},
			-1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultRouteNetwork(tt.networks, "hospitus0"); got != tt.want {
				t.Errorf("defaultRouteNetwork = %d, want %d", got, tt.want)
			}
		})
	}
}
