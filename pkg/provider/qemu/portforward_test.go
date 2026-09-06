package qemu

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// persistEmptyVM writes a minimal stopped VM config with no disk requirement.
func persistEmptyVM(t *testing.T, p *QEMUProvider, name string) {
	t.Helper()
	cfg := &vmConfig{Name: name, Spec: provider.InstanceSpec{Name: name}}
	if err := p.saveVMConfig(cfg, filepath.Join(p.stateDir, name+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestAddAndListPortForward(t *testing.T) {
	// Arrange
	p := fakeProvider(t, &execx.Fake{})
	persistEmptyVM(t, p, "web")
	h := provider.InstanceHandle{ID: "web"}

	// Act
	err := p.AddPortForward(context.Background(), h, provider.PortForward{Protocol: "tcp", HostPort: 8080, GuestPort: 80})
	if err != nil {
		t.Fatalf("AddPortForward: %v", err)
	}

	// Assert: the rule round-trips through the persisted config.
	rules, err := p.ListPortForwards(context.Background(), h)
	if err != nil {
		t.Fatalf("ListPortForwards: %v", err)
	}
	if len(rules) != 1 || rules[0].HostPort != 8080 || rules[0].GuestPort != 80 || rules[0].Protocol != "tcp" {
		t.Errorf("rules = %+v, want one tcp 8080->80", rules)
	}
}

func TestAddPortForwardRejectsBadInput(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	persistEmptyVM(t, p, "web")
	h := provider.InstanceHandle{ID: "web"}

	cases := []provider.PortForward{
		{Protocol: "icmp", HostPort: 80, GuestPort: 80}, // bad protocol
		{Protocol: "tcp", HostPort: 0, GuestPort: 80},   // bad host port
		{Protocol: "tcp", HostPort: 80, GuestPort: 0},   // bad guest port
	}
	for _, pf := range cases {
		if err := p.AddPortForward(context.Background(), h, pf); err == nil {
			t.Errorf("expected error for invalid rule %+v", pf)
		}
	}
}

func TestAddPortForwardRejectsDuplicate(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	persistEmptyVM(t, p, "web")
	h := provider.InstanceHandle{ID: "web"}
	pf := provider.PortForward{Protocol: "tcp", HostPort: 8080, GuestPort: 80}
	if err := p.AddPortForward(context.Background(), h, pf); err != nil {
		t.Fatal(err)
	}
	if err := p.AddPortForward(context.Background(), h, pf); err == nil {
		t.Error("expected error when the same host port is forwarded twice")
	}
}

func TestRemovePortForward(t *testing.T) {
	// Arrange
	p := fakeProvider(t, &execx.Fake{})
	persistEmptyVM(t, p, "web")
	h := provider.InstanceHandle{ID: "web"}
	if err := p.AddPortForward(context.Background(), h, provider.PortForward{Protocol: "tcp", HostPort: 8080, GuestPort: 80}); err != nil {
		t.Fatal(err)
	}

	// Act
	if err := p.RemovePortForward(context.Background(), h, "tcp", 8080); err != nil {
		t.Fatalf("RemovePortForward: %v", err)
	}

	// Assert
	rules, err := p.ListPortForwards(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 0 {
		t.Errorf("expected no rules after removal, got %+v", rules)
	}
}

func TestRemovePortForwardNotFound(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	persistEmptyVM(t, p, "web")
	if err := p.RemovePortForward(context.Background(), provider.InstanceHandle{ID: "web"}, "tcp", 9999); err == nil {
		t.Error("expected error when removing a non-existent rule")
	}
}

func TestRemovePortForwardRejectsBadProtocol(t *testing.T) {
	p := fakeProvider(t, &execx.Fake{})
	if err := p.RemovePortForward(context.Background(), provider.InstanceHandle{ID: "web"}, "icmp", 80); err == nil {
		t.Error("expected error for an unsupported protocol")
	}
}

func TestPortForwardsFromConfigAndRaw(t *testing.T) {
	// Arrange: round-trip through the raw map form (JSON decodes numbers to float64).
	pfs := []provider.PortForward{
		{Protocol: "tcp", HostPort: 8080, GuestPort: 80},
		{Protocol: "udp", HostPort: 5353, GuestPort: 53},
	}
	raw := portForwardsToRaw(pfs)

	// Simulate the float64 typing a JSON round-trip produces.
	rawJSON := make([]interface{}, len(raw))
	for i, item := range raw {
		m := item.(map[string]interface{})
		rawJSON[i] = map[string]interface{}{
			"protocol": m["protocol"],
			"host":     float64(m["host"].(int)),
			"guest":    float64(m["guest"].(int)),
		}
	}

	// Act
	got := portForwardsFromConfig(map[string]interface{}{"port_forwards": rawJSON})

	// Assert
	if len(got) != 2 || got[0].HostPort != 8080 || got[1].Protocol != "udp" || got[1].GuestPort != 53 {
		t.Errorf("round-trip = %+v, want tcp 8080->80 and udp 5353->53", got)
	}
}

func TestPortForwardsFromConfigNil(t *testing.T) {
	if got := portForwardsFromConfig(nil); got != nil {
		t.Errorf("portForwardsFromConfig(nil) = %v, want nil", got)
	}
	if got := portForwardsFromConfig(map[string]interface{}{}); got != nil {
		t.Errorf("portForwardsFromConfig(no key) = %v, want nil", got)
	}
}
