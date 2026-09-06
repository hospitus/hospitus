package jail

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/config"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// vnetProvider builds a provider whose jail is running and has VNET enabled.
func vnetProvider(t *testing.T, name string, fn func(cmd string, args []string) ([]byte, error)) (*JailProvider, *execx.Fake) {
	t.Helper()
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	if err := p.saveJailConfig(&jailConfig{Name: name}, filepath.Join(stateDir, name+".json")); err != nil {
		t.Fatalf("saveJailConfig: %v", err)
	}
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "jls" {
			for _, a := range args {
				if a == "vnet" {
					return []byte("vnet=1\n"), nil
				}
			}
			return []byte("1\n"), nil
		}
		if fn != nil {
			return fn(cmd, args)
		}
		return nil, nil
	}}
	p.runner = fake
	return p, fake
}

func TestJailHasVNET(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "enabled", output: "vnet=1\n", want: true},
		{name: "new", output: "vnet=new\n", want: true},
		{name: "disabled", output: "vnet=0\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(tt.output), nil }}
			p := &JailProvider{runner: fake}
			got, err := p.jailHasVNET(context.Background(), "web")
			if err != nil {
				t.Fatalf("jailHasVNET err = %v", err)
			}
			if got != tt.want {
				t.Errorf("jailHasVNET = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAddNetworkInterface(t *testing.T) {
	p, fake := vnetProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		if cmd == "ifconfig" && len(args) >= 2 && args[0] == "epair" && args[1] == "create" {
			return []byte("epair7a\n"), nil
		}
		return nil, nil
	})

	iface := NetworkInterface{IPv4Address: "10.0.0.9/24", MTU: 1500}
	got, err := p.AddNetworkInterface(context.Background(), provider.InstanceHandle{ID: "web"}, iface)
	if err != nil {
		t.Fatalf("AddNetworkInterface err = %v", err)
	}
	if got.HostInterface != "epair7a" || got.JailInterface != "epair7b" {
		t.Errorf("interfaces = %s/%s, want epair7a/epair7b", got.HostInterface, got.JailInterface)
	}
	if want := "ifconfig epair7b vnet web"; !hasCmd(fake, want) {
		t.Errorf("missing vnet assignment %q", want)
	}
	if want := "jexec web ifconfig epair7b inet 10.0.0.9/24"; !hasCmd(fake, want) {
		t.Errorf("missing IPv4 config %q", want)
	}
}

// TestAddNetworkInterfacePersists covers what happens to an added interface
// when the jail stops.
//
// Adding one is runtime work — an epair created, moved into the jail and
// addressed — and none of it survives a restart on its own. StartInstance
// builds one interface per entry in Networks, so the entry is what brings it
// back, and the jail-side name rides in the spec's ID because nothing else
// carries it.
func TestAddNetworkInterfacePersists(t *testing.T) {
	p, _ := vnetProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		if cmd == "ifconfig" && len(args) >= 2 && args[0] == "epair" && args[1] == "create" {
			return []byte("epair7a\n"), nil
		}
		return nil, nil
	})

	// Attaching to a bridge goes through the network manager, which the
	// helper above does not build.
	p.networkManager = NewNetworkManager(&config.Config{FirewallType: "none"}, nil)
	p.networkManager.runner = p.runner

	iface := NetworkInterface{
		Name:        "eth1",
		Bridge:      "internal0",
		IPv4Address: "172.16.0.10/24",
	}
	if _, err := p.AddNetworkInterface(context.Background(), provider.InstanceHandle{ID: "web"}, iface); err != nil {
		t.Fatalf("AddNetworkInterface err = %v", err)
	}

	cfg, err := p.loadJailConfig(filepath.Join(p.stateDir, "web.json"))
	if err != nil {
		t.Fatalf("loadJailConfig: %v", err)
	}

	var found *provider.NetworkSpec
	for i := range cfg.Networks {
		if cfg.Networks[i].IPv4 == "172.16.0.10/24" {
			found = &cfg.Networks[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("added interface not recorded; networks = %+v", cfg.Networks)
	}
	if found.Bridge != "internal0" {
		t.Errorf("bridge = %q, want internal0", found.Bridge)
	}
	if found.ID != "eth1" {
		t.Errorf("jail-side name = %q, want eth1; it comes back as epairNb without it", found.ID)
	}
}

func TestAddNetworkInterfaceNoVNET(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	_ = p.saveJailConfig(&jailConfig{Name: "web"}, filepath.Join(stateDir, "web.json"))
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "jls" {
			for _, a := range args {
				if a == "vnet" {
					return []byte("vnet=0\n"), nil
				}
			}
			return []byte("1\n"), nil
		}
		return nil, nil
	}}
	p.runner = fake
	if _, err := p.AddNetworkInterface(context.Background(), provider.InstanceHandle{ID: "web"}, NetworkInterface{}); err == nil {
		t.Fatal("expected error when VNET not enabled")
	}
}

func TestListNetworkInterfaces(t *testing.T) {
	p, _ := runningProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		if cmd == "jexec" && len(args) >= 3 && args[1] == "ifconfig" && args[2] == "-l" {
			return []byte("epair0b lo0\n"), nil
		}
		if cmd == "jexec" && len(args) >= 3 && args[1] == "ifconfig" {
			return []byte("epair0b: flags=8843 mtu 1500\n\tether 00:11:22:33:44:55\n\tinet 10.0.0.5 netmask 0xffffff00\n"), nil
		}
		return nil, nil
	})
	ifaces, err := p.ListNetworkInterfaces(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("ListNetworkInterfaces err = %v", err)
	}
	if len(ifaces) != 1 {
		t.Fatalf("got %d interfaces, want 1 (lo0 skipped)", len(ifaces))
	}
	if ifaces[0].IPv4Address != "10.0.0.5/24" {
		t.Errorf("IPv4 = %q, want 10.0.0.5/24", ifaces[0].IPv4Address)
	}
	if ifaces[0].MAC != "00:11:22:33:44:55" {
		t.Errorf("MAC = %q", ifaces[0].MAC)
	}
	if ifaces[0].MTU != 1500 {
		t.Errorf("MTU = %d, want 1500", ifaces[0].MTU)
	}
}

func TestGetDefaultRoute(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("   route to: default\n   gateway: 10.0.0.1\n"), nil
	}}
	p := &JailProvider{runner: fake}
	gw, err := p.GetDefaultRoute(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetDefaultRoute err = %v", err)
	}
	if gw != "10.0.0.1" {
		t.Errorf("gateway = %q, want 10.0.0.1", gw)
	}
}

func TestGetDefaultRouteNone(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("no route") }}
	p := &JailProvider{runner: fake}
	gw, err := p.GetDefaultRoute(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil || gw != "" {
		t.Errorf("expected empty,nil; got %q,%v", gw, err)
	}
}

func TestSetDefaultRoute(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &JailProvider{runner: fake}

	if err := p.SetDefaultRoute(context.Background(), provider.InstanceHandle{ID: "web"}, "10.0.0.1", false); err != nil {
		t.Fatalf("SetDefaultRoute err = %v", err)
	}
	if want := "jexec web route add default 10.0.0.1"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}

	if err := p.SetDefaultRoute(context.Background(), provider.InstanceHandle{ID: "web"}, "fd00::1", true); err != nil {
		t.Fatalf("SetDefaultRoute v6 err = %v", err)
	}
	if want := "jexec web route -6 add default fd00::1"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

// TestAddNetworkInterfaceCreatesTheBridge covers a bridge that is not there yet.
//
// Starting a jail on a bridge creates it; adding an interface on one did not,
// and the attach then failed with a message naming the epair rather than the
// missing bridge: "failed to add epair12a to bridge internal0: exit status 1".
func TestAddNetworkInterfaceCreatesTheBridge(t *testing.T) {
	p, fake := vnetProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		if cmd == "ifconfig" && len(args) >= 2 && args[0] == "epair" && args[1] == "create" {
			return []byte("epair7a\n"), nil
		}
		// The bridge does not exist until it is created.
		if cmd == "ifconfig" && len(args) == 1 && args[0] == "internal0" {
			return nil, errAssertStopped
		}
		return nil, nil
	})
	p.networkManager = NewNetworkManager(&config.Config{FirewallType: "none", AutoCreateBridges: true}, nil)
	p.networkManager.runner = p.runner

	_, err := p.AddNetworkInterface(context.Background(), provider.InstanceHandle{ID: "web"},
		NetworkInterface{Name: "eth1", Bridge: "internal0", IPv4Address: "172.16.0.10/24"})
	if err != nil {
		t.Fatalf("AddNetworkInterface: %v", err)
	}

	if !hasCmd(fake, "ifconfig bridge create name internal0") {
		t.Errorf("the bridge was not created; commands: %v", fake.Calls)
	}
}
