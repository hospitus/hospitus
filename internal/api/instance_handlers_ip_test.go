package api

import (
	"context"
	"log/slog"
	"net"
	"testing"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// ipProvider reports an address the way a DHCP-backed provider does: worked out
// from the host, never present in the stored spec.
type ipProvider struct {
	*mockProvider
	ip    string
	state provider.InstanceState
}

func (p *ipProvider) GetInstanceInfo(context.Context, provider.InstanceHandle) (provider.InstanceInfo, error) {
	info := provider.InstanceInfo{State: p.state}
	if p.ip != "" {
		info.IPAddresses = []net.IP{net.ParseIP(p.ip)}
	}
	return info, nil
}

// TestRefreshFromProviderFillsADHCPAddress covers an address the stored spec
// cannot hold: four providers work one out and set InstanceInfo.IPAddresses,
// and this is where it reaches the instance an operator asks about.
func TestRefreshFromProviderFillsADHCPAddress(t *testing.T) {
	srv := &Server{registry: provider.NewRegistry(), logger: slog.Default()}
	if err := srv.registry.Register(&ipProvider{
		mockProvider: newMockProvider(),
		ip:           "10.10.0.173",
		state:        provider.StateRunning,
	}); err != nil {
		t.Fatal(err)
	}

	instance := &datastore.Instance{Provider: "mock"}
	instance.Spec.Networks = []provider.NetworkSpec{{ID: "tap0"}}

	srv.refreshFromProvider(context.Background(), instance)

	if got := instance.Spec.Networks[0].IPv4; got != "10.10.0.173" {
		t.Errorf("IPv4 = %q, want the address the provider reported", got)
	}
	if instance.State != provider.StateRunning {
		t.Errorf("State = %q, want running", instance.State)
	}
}

// TestRefreshFromProviderKeepsADeclaredAddress leaves a static address alone.
func TestRefreshFromProviderKeepsADeclaredAddress(t *testing.T) {
	srv := &Server{registry: provider.NewRegistry(), logger: slog.Default()}
	if err := srv.registry.Register(&ipProvider{
		mockProvider: newMockProvider(),
		ip:           "10.10.0.173",
		state:        provider.StateRunning,
	}); err != nil {
		t.Fatal(err)
	}

	instance := &datastore.Instance{Provider: "mock"}
	instance.Spec.Networks = []provider.NetworkSpec{{ID: "tap0", IPv4: "192.168.1.50"}}

	srv.refreshFromProvider(context.Background(), instance)

	if got := instance.Spec.Networks[0].IPv4; got != "192.168.1.50" {
		t.Errorf("IPv4 = %q, want the declared address untouched", got)
	}
}

// TestRefreshFromProviderKeepsStateWhenInfoOmitsIt guards the stored state
// against a provider that returns info without one.
func TestRefreshFromProviderKeepsStateWhenInfoOmitsIt(t *testing.T) {
	srv := &Server{registry: provider.NewRegistry(), logger: slog.Default()}
	if err := srv.registry.Register(&ipProvider{mockProvider: newMockProvider()}); err != nil {
		t.Fatal(err)
	}

	instance := &datastore.Instance{Provider: "mock", State: provider.StateStopped}
	srv.refreshFromProvider(context.Background(), instance)

	// The mock's GetInstanceState says running, which is the fallback. The
	// exact state, not merely a non-empty one: any wrong value passed before.
	if instance.State != provider.StateRunning {
		t.Errorf("state = %q, want %q from the GetInstanceState fallback",
			instance.State, provider.StateRunning)
	}
}

// addressOnlyProvider implements the optional address interface and nothing
// else beyond the mock, which is what the instance list asks for.
type addressOnlyProvider struct {
	*mockProvider
	ip       string
	asked    int
	failWith error
}

func (p *addressOnlyProvider) InstanceAddresses(context.Context, provider.InstanceHandle) ([]net.IP, error) {
	p.asked++
	if p.failWith != nil {
		return nil, p.failWith
	}
	return []net.IP{net.ParseIP(p.ip)}, nil
}

// TestListAsksOnlyForTheAddress covers the instance list, where a full
// GetInstanceInfo per row would read a zfs list per disk for every bhyve VM.
func TestListAsksOnlyForTheAddress(t *testing.T) {
	srv := &Server{registry: provider.NewRegistry(), logger: slog.Default()}
	prov := &addressOnlyProvider{mockProvider: newMockProvider(), ip: "10.10.0.173"}

	instance := &datastore.Instance{Provider: "mock", State: provider.StateRunning}
	instance.Spec.Networks = []provider.NetworkSpec{{ID: "tap0"}}

	srv.askProviderForAddress(context.Background(), prov, instance)

	if got := instance.Spec.Networks[0].IPv4; got != "10.10.0.173" {
		t.Errorf("IPv4 = %q, want the address the provider reported", got)
	}
	if prov.asked != 1 {
		t.Errorf("the provider was asked %d times, want once", prov.asked)
	}
}

// TestListLeavesAStoppedInstanceAlone keeps a dead instance from wearing the
// address it used to hold: a DHCP lease and an ARP entry both outlive it.
func TestListLeavesAStoppedInstanceAlone(t *testing.T) {
	srv := &Server{registry: provider.NewRegistry(), logger: slog.Default()}
	prov := &addressOnlyProvider{mockProvider: newMockProvider(), ip: "10.10.0.173"}

	instance := &datastore.Instance{Provider: "mock", State: provider.StateStopped}
	instance.Spec.Networks = []provider.NetworkSpec{{ID: "tap0"}}

	srv.askProviderForAddress(context.Background(), prov, instance)

	if instance.Spec.Networks[0].IPv4 != "" {
		t.Errorf("a stopped instance was given the address %q", instance.Spec.Networks[0].IPv4)
	}
	if prov.asked != 0 {
		t.Error("a stopped instance's provider was asked for an address")
	}
}

// TestListKeepsADeclaredAddress leaves a static address as it was written.
func TestListKeepsADeclaredAddress(t *testing.T) {
	srv := &Server{registry: provider.NewRegistry(), logger: slog.Default()}
	prov := &addressOnlyProvider{mockProvider: newMockProvider(), ip: "10.10.0.173"}

	instance := &datastore.Instance{Provider: "mock", State: provider.StateRunning}
	instance.Spec.Networks = []provider.NetworkSpec{{ID: "tap0", IPv4: "192.168.1.50"}}

	srv.askProviderForAddress(context.Background(), prov, instance)

	if got := instance.Spec.Networks[0].IPv4; got != "192.168.1.50" {
		t.Errorf("IPv4 = %q, want the declared address untouched", got)
	}
}

// TestListSurvivesAProviderWithoutTheInterface covers a provider that does not
// implement the optional interface: the list shows what was declared.
func TestListSurvivesAProviderWithoutTheInterface(t *testing.T) {
	srv := &Server{registry: provider.NewRegistry(), logger: slog.Default()}

	instance := &datastore.Instance{Provider: "mock", State: provider.StateRunning}
	instance.Spec.Networks = []provider.NetworkSpec{{ID: "tap0"}}

	srv.askProviderForAddress(context.Background(), newMockProvider(), instance)

	if instance.Spec.Networks[0].IPv4 != "" {
		t.Errorf("an address appeared from a provider that reports none: %q", instance.Spec.Networks[0].IPv4)
	}
}

// The assertion askProviderForAddress makes, made here too: addressOnlyProvider
// must keep implementing the interface, or the test that exercises that branch
// quietly stops exercising it. ipProvider deliberately does not — it reports
// addresses through GetInstanceInfo, which is the other branch.
var _ provider.InstanceAddressProvider = (*addressOnlyProvider)(nil)

func TestMockProviderReportsNoAddress(t *testing.T) {
	if _, ok := any(newMockProvider()).(provider.InstanceAddressProvider); ok {
		t.Error("mockProvider implements InstanceAddressProvider, so the no-address test proves nothing")
	}
}

// TestPrivilegedProviderConfigIgnoresKeyCase covers the gate's blind spot:
// ParseJailParametersFromMap lowercases every key, so a caller spelling one in
// capitals reached jail(8) as the real parameter while the gate saw a key it
// did not know.
func TestPrivilegedProviderConfigIgnoresKeyCase(t *testing.T) {
	for _, key := range []string{"exec.start", "EXEC.START", "Exec.Start", "PATH", "Allow.Mount.ZFS"} {
		if got := privilegedProviderConfig(map[string]interface{}{key: "x"}); got == "" {
			t.Errorf("%q passed the privilege gate", key)
		}
	}

	// And a key that really is harmless still passes, whatever its case.
	for _, key := range []string{"exec.clean", "EXEC.CLEAN"} {
		if got := privilegedProviderConfig(map[string]interface{}{key: "1"}); got != "" {
			t.Errorf("%q was refused as privileged", key)
		}
	}

	// The mounts list is read case-insensitively too.
	config := map[string]interface{}{
		"MOUNTS": []interface{}{
			map[string]interface{}{"host_path": "/etc"},
		},
	}
	if got := privilegedProviderConfig(config); got == "" {
		t.Error("a mount declared under \"MOUNTS\" passed the privilege gate")
	}
}
