package jail

import (
	"context"
	"errors"
	"testing"

	"github.com/hospitus/hospitus/pkg/config"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func newNMWithFake(fake *execx.Fake, cfg *config.Config) *NetworkManager {
	if cfg == nil {
		cfg = &config.Config{}
	}
	nm := NewNetworkManager(cfg, nil)
	nm.runner = fake
	return nm
}

func TestBridgeExists(t *testing.T) {
	tests := []struct {
		name   string
		runErr error
		want   bool
	}{
		{name: "exists", runErr: nil, want: true},
		{name: "missing", runErr: errors.New("no such"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, tt.runErr }}
			nm := newNMWithFake(fake, nil)
			if got := nm.bridgeExists(context.Background(), "hospitus0"); got != tt.want {
				t.Errorf("bridgeExists = %v, want %v", got, tt.want)
			}
			if want := "ifconfig hospitus0"; !hasCmd(fake, want) {
				t.Errorf("missing %q", want)
			}
		})
	}
}

func TestEnableIPForwarding(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	nm := newNMWithFake(fake, &config.Config{EnableIPv6: true})
	if err := nm.EnableIPForwarding(context.Background()); err != nil {
		t.Fatalf("EnableIPForwarding err = %v", err)
	}
	if want := "sysctl net.inet.ip.forwarding=1"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
	if want := "sysctl net.inet6.ip6.forwarding=1"; !hasCmd(fake, want) {
		t.Errorf("missing IPv6 forwarding %q", want)
	}
}

func TestEnableIPForwardingV4Fails(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("denied") }}
	nm := newNMWithFake(fake, nil)
	if err := nm.EnableIPForwarding(context.Background()); err == nil {
		t.Fatal("expected error when IPv4 forwarding fails")
	}
}

func TestGetDefaultInterface(t *testing.T) {
	out := "   route to: default\n   interface: em0\n"
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(out), nil }}
	nm := newNMWithFake(fake, nil)
	iface, err := nm.getDefaultInterface(context.Background())
	if err != nil {
		t.Fatalf("getDefaultInterface err = %v", err)
	}
	if iface != "em0" {
		t.Errorf("iface = %q, want em0", iface)
	}
	if want := "route -n get default"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestDestroyVNetInterface(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	nm := newNMWithFake(fake, nil)
	if err := nm.DestroyVNetInterface(context.Background(), "epair0a"); err != nil {
		t.Fatalf("DestroyVNetInterface err = %v", err)
	}
	if want := "ifconfig epair0a destroy"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestCreateVNetInterface(t *testing.T) {
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "ifconfig" && len(args) >= 2 && args[0] == "epair" && args[1] == "create" {
			return []byte("epair3a\n"), nil
		}
		return nil, nil
	}}
	nm := newNMWithFake(fake, nil)

	vif, err := nm.CreateVNetInterface(context.Background(), "web", provider.NetworkSpec{Bridge: "hospitus0", MTU: 1500})
	if err != nil {
		t.Fatalf("CreateVNetInterface err = %v", err)
	}
	if vif.EpairA != "epair3a" || vif.EpairB != "epair3b" {
		t.Errorf("epairs = %s/%s, want epair3a/epair3b", vif.EpairA, vif.EpairB)
	}
	if want := "ifconfig epair create"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
	if want := "ifconfig epair3a mtu 1500"; !hasCmd(fake, want) {
		t.Errorf("missing MTU set %q", want)
	}
	if want := "ifconfig hospitus0 addm epair3a"; !hasCmd(fake, want) {
		t.Errorf("missing bridge attach %q", want)
	}
}

func TestCreateVNetInterfaceCreateFails(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("boom") }}
	nm := newNMWithFake(fake, nil)
	if _, err := nm.CreateVNetInterface(context.Background(), "web", provider.NetworkSpec{}); err == nil {
		t.Fatal("expected error when epair create fails")
	}
}

func TestAddJailRoute(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	nm := newNMWithFake(fake, nil)
	if err := nm.AddJailRoute(context.Background(), "web", "10.0.0.1"); err != nil {
		t.Fatalf("AddJailRoute err = %v", err)
	}
	if want := "jexec -r web route add default 10.0.0.1"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestCheckIPConflicts(t *testing.T) {
	out := "em0: flags=8843\n" +
		"\tinet 192.168.1.10 netmask 0xffffff00\n" +
		"hospitus0: flags=8843\n" +
		"\tinet 10.0.0.1 netmask 0xffffff00\n"
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(out), nil }}
	nm := newNMWithFake(fake, nil)

	t.Run("conflict found", func(t *testing.T) {
		iface, err := nm.CheckIPConflicts(context.Background(), "10.0.0.1/24", "")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if iface != "hospitus0" {
			t.Errorf("iface = %q, want hospitus0", iface)
		}
	})

	t.Run("excluded interface skipped", func(t *testing.T) {
		iface, err := nm.CheckIPConflicts(context.Background(), "10.0.0.1/24", "hospitus0")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if iface != "" {
			t.Errorf("iface = %q, want empty (excluded)", iface)
		}
	})

	t.Run("no conflict", func(t *testing.T) {
		iface, err := nm.CheckIPConflicts(context.Background(), "172.16.0.9/24", "")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if iface != "" {
			t.Errorf("iface = %q, want empty", iface)
		}
	})
}

// TestConfigureVNetJailNetworkHostSide covers the path a Linux or
// cross-architecture jail takes, where the host configures the interfaces
// because the jail has no FreeBSD ifconfig to run.
//
// The loopback has to be given an address, not merely brought up: a VNET jail
// starts with none, and this path has no /etc/rc coming along afterwards to
// supply one. Without it nothing in the jail reaches 127.0.0.1 — apache listens
// on *:80, sockstat shows the socket, and every request to localhost is
// refused.
func TestConfigureVNetJailNetworkHostSide(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	nm := newNMWithFake(fake, &config.Config{DefaultGateway: "none"})
	// crossarch -> host-side config path (ifconfig -j).
	if err := nm.ConfigureVNetJailNetwork(context.Background(), "web", "hospitus0", "epair0b", "10.0.0.5/24", "crossarch", true); err != nil {
		t.Fatalf("err = %v", err)
	}
	if want := "ifconfig -j web lo0 inet 127.0.0.1/8 up"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
	if want := "ifconfig -j web epair0b 10.0.0.5/24 up"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

// TestConfigureVNetJailNetworkNative covers the same for a FreeBSD jail, where
// the commands run through jexec. rc.d/netif would address lo0 moments later,
// but a jail configured before rc runs, or told not to run it, would otherwise
// have no localhost at all.
func TestConfigureVNetJailNetworkNative(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	nm := newNMWithFake(fake, &config.Config{DefaultGateway: "none"})
	// freebsd -> jexec path.
	if err := nm.ConfigureVNetJailNetwork(context.Background(), "web", "hospitus0", "epair0b", "10.0.0.5/24", "freebsd", true); err != nil {
		t.Fatalf("err = %v", err)
	}
	if want := "jexec web ifconfig lo0 inet 127.0.0.1/8 up"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
	if want := "jexec web ifconfig epair0b 10.0.0.5/24 up"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}
