package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// TestResolveInstanceIP covers the address a port forward is aimed at when the
// caller does not name one. A DHCP guest keeps the literal "dhcp" in its spec
// because nothing reports the lease back, and forwarding to that string would
// produce a rule pointing nowhere — it has to read as unknown instead.
func TestResolveInstanceIP(t *testing.T) {
	// The instances use "mock", the provider setupTestServer registers. They
	// used to say "bhyve", which it does not: since resolveInstanceIP began
	// reporting a failed registry lookup as an error, the two cases with no
	// address in the spec fell straight into that error path and passed
	// because the assertion threw the error away — exercising nothing.
	tests := []struct {
		name            string
		networks        []provider.NetworkSpec
		want            string
		wantUnsupported bool
	}{
		{
			// No address in the spec, so the provider is asked — and
			// mockProvider implements no InstanceAddressProvider.
			name:            "no networks",
			networks:        nil,
			want:            "",
			wantUnsupported: true,
		},
		{
			name:            "dhcp is not an address",
			networks:        []provider.NetworkSpec{{IPv4: "dhcp"}},
			want:            "",
			wantUnsupported: true,
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
				Provider: "mock",
				State:    provider.StateRunning,
				Spec:     provider.InstanceSpec{Name: "vm1", Networks: tt.networks},
				Handle:   provider.InstanceHandle{ID: "vm1", Provider: "mock"},
			}
			if err := ds.CreateInstance(context.Background(), inst); err != nil {
				t.Fatalf("CreateInstance: %v", err)
			}

			got, unsupported, err := srv.resolveInstanceIP(context.Background(), "vm1")
			if err != nil {
				t.Fatalf("resolveInstanceIP: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveInstanceIP = %q, want %q", got, tt.want)
			}
			if unsupported != tt.wantUnsupported {
				t.Errorf("unsupported = %v, want %v", unsupported, tt.wantUnsupported)
			}
		})
	}
}

// TestResolveInstanceIPUnknownInstance keeps a missing instance from becoming a
// forwarding rule aimed at nothing.
func TestResolveInstanceIPUnknownInstance(t *testing.T) {
	srv, ds := setupTestServer(t)
	defer ds.Close()

	// All three: an empty address on its own also comes back from a datastore
	// that failed, and discarding the other two let that satisfy this test.
	got, unsupported, err := srv.resolveInstanceIP(context.Background(), "absent")
	if err != nil {
		t.Fatalf("a missing instance is not an error: %v", err)
	}
	if got != "" {
		t.Errorf("resolveInstanceIP = %q, want empty", got)
	}
	if unsupported {
		t.Error("unsupported = true: a missing instance says nothing about the provider's capabilities")
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

	if got, _, _ := srv.resolveInstanceIP(ctx, "dhcp-guest"); got != want {
		t.Errorf("resolveInstanceIP = %q, want %q", got, want)
	}
}

// TestFirewallExposeRouteShape covers which paths each method serves.
//
// The dispatch accepted four or five segments for every method, but only the
// GET listing reads the fifth: handleExposePort names the instance from the
// request body and ignored it, so a POST to .../expose/web answered 201 for a
// rule created against whatever the body said.
func TestFirewallExposeRouteShape(t *testing.T) {
	s, _ := setupTestServer(t)

	for _, tt := range []struct {
		method string
		path   string
		want   int
		why    string
	}{
		// The instance segment belongs to the listing alone.
		{http.MethodPost, "/api/v1/firewall/expose/web", http.StatusNotFound, "POST names the instance in the body"},
		{http.MethodDelete, "/api/v1/firewall/expose/web", http.StatusNotFound, "DELETE names the instance in the body"},
		{http.MethodGet, "/api/v1/firewall/expose/web/extra", http.StatusNotFound, "a surplus segment is no route"},

		// A GET without one cannot list anything.
		{http.MethodGet, "/api/v1/firewall/expose", http.StatusBadRequest, "the listing needs an instance"},

		// These reach the handler, which reports the firewall manager absent
		// in this test server. What matters is that they are routed at all.
		{http.MethodPost, "/api/v1/firewall/expose", http.StatusServiceUnavailable, "routed"},
		{http.MethodGet, "/api/v1/firewall/expose/web", http.StatusServiceUnavailable, "routed"},
	} {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)

		if w.Code != tt.want {
			t.Errorf("%s %s = %d, want %d (%s); body=%s", tt.method, tt.path, w.Code, tt.want, tt.why, w.Body.String())
		}
	}
}
