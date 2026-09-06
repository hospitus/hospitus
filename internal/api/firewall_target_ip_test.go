package api

import (
	"context"
	"net"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// TestResolveInstanceIP covers the address a port forward is aimed at when the
// caller does not name one. A DHCP guest keeps the literal "dhcp" in its spec
// because nothing reports the lease back, and forwarding to that string would
// produce a rule pointing nowhere — it has to read as unknown instead.
func TestResolveInstanceIP(t *testing.T) {
	tests := []struct {
		name     string
		networks []provider.NetworkSpec
		want     string
	}{
		{
			name:     "no networks",
			networks: nil,
			want:     "",
		},
		{
			name:     "dhcp is not an address",
			networks: []provider.NetworkSpec{{IPv4: "dhcp"}},
			want:     "",
		},
		{
			name:     "plain address",
			networks: []provider.NetworkSpec{{IPv4: "10.0.0.12"}},
			want:     "10.0.0.12",
		},
		{
			name:     "CIDR is trimmed to the address",
			networks: []provider.NetworkSpec{{IPv4: "10.30.0.10/24"}},
			want:     "10.30.0.10",
		},
		{
			name: "first addressed network wins over an earlier dhcp one",
			networks: []provider.NetworkSpec{
				{IPv4: "dhcp"},
				{IPv4: "10.40.0.3/24"},
			},
			want: "10.40.0.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, ds := setupTestServer(t)
			defer ds.Close()

			inst := &datastore.Instance{
				ID:       "vm1",
				Name:     "vm1",
				Provider: "bhyve",
				State:    provider.StateRunning,
				Spec:     provider.InstanceSpec{Name: "vm1", Networks: tt.networks},
				Handle:   provider.InstanceHandle{ID: "vm1", Provider: "bhyve"},
			}
			if err := ds.CreateInstance(context.Background(), inst); err != nil {
				t.Fatalf("CreateInstance: %v", err)
			}

			if got := srv.resolveInstanceIP(context.Background(), "vm1"); got != tt.want {
				t.Errorf("resolveInstanceIP = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResolveInstanceIPUnknownInstance keeps a missing instance from becoming a
// forwarding rule aimed at nothing.
func TestResolveInstanceIPUnknownInstance(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	if got := srv.resolveInstanceIP(context.Background(), "absent"); got != "" {
		t.Errorf("resolveInstanceIP = %q, want empty", got)
	}
}

// addressProvider answers InstanceAddresses, which is how a DHCP guest's
// address is found: nothing writes the lease back into the spec.
type addressProvider struct {
	*mockProvider
	addrs []net.IP
}

func (p addressProvider) InstanceAddresses(context.Context, provider.InstanceHandle) ([]net.IP, error) {
	return p.addrs, nil
}

// The fallback the table above cannot reach: with no address in the spec,
// resolveInstanceIP asks the provider. Nothing covered that branch, so a
// change to it would have gone unnoticed.
func TestResolveInstanceIPFallsBackToTheProvider(t *testing.T) {
	want := "10.7.0.42"
	srv, ds := setupTestServerWithProvider(t, addressProvider{
		mockProvider: newMockProvider(),
		addrs:        []net.IP{net.ParseIP(want)},
	})
	defer ds.Close()

	ctx := t.Context()
	inst := &datastore.Instance{
		ID:       "dhcp-guest",
		Name:     "dhcp-guest",
		Provider: "mock",
		Handle:   provider.InstanceHandle{ID: "dhcp-guest", Provider: "mock"},
		Spec: provider.InstanceSpec{
			Name:     "dhcp-guest",
			Networks: []provider.NetworkSpec{{IPv4: "dhcp"}},
		},
	}
	if err := ds.CreateInstance(ctx, inst); err != nil {
		t.Fatal(err)
	}

	if got := srv.resolveInstanceIP(ctx, "dhcp-guest"); got != want {
		t.Errorf("resolveInstanceIP = %q, want %q", got, want)
	}
}
