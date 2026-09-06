package api

import (
	"context"
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
