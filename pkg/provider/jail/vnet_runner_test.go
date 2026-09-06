package jail

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestEnableVNETDisabledNoop(t *testing.T) {
	p := &JailProvider{runner: &execx.Fake{}}
	if err := p.EnableVNET(context.Background(), provider.InstanceHandle{ID: "web"}, VNETConfig{Enabled: false}); err != nil {
		t.Fatalf("EnableVNET disabled should be no-op, got %v", err)
	}
}

func TestEnableVNET(t *testing.T) {
	p, fake := runningProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		if cmd == "ifconfig" && len(args) >= 2 && args[0] == "epair" && args[1] == "create" {
			return []byte("epair2a\n"), nil
		}
		return nil, nil
	})
	cfg := VNETConfig{Enabled: true, Bridge: "hospitus0", IPv4Address: "10.0.0.5"}
	if err := p.EnableVNET(context.Background(), provider.InstanceHandle{ID: "web"}, cfg); err != nil {
		t.Fatalf("EnableVNET err = %v", err)
	}
	if want := "ifconfig epair create"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
	if want := "ifconfig hospitus0 addm epair2a"; !hasCmd(fake, want) {
		t.Errorf("missing bridge add %q", want)
	}
	if want := "ifconfig epair2b vnet web"; !hasCmd(fake, want) {
		t.Errorf("missing vnet assign %q", want)
	}
	if want := "jexec web ifconfig epair2b inet 10.0.0.5"; !hasCmd(fake, want) {
		t.Errorf("missing IPv4 config %q", want)
	}
}

func TestEnableVNETInvalidBridge(t *testing.T) {
	p := &JailProvider{runner: &execx.Fake{}}
	cfg := VNETConfig{Enabled: true, Bridge: "bad;bridge"}
	if err := p.EnableVNET(context.Background(), provider.InstanceHandle{ID: "web"}, cfg); err == nil {
		t.Fatal("expected error for invalid bridge name")
	}
}

func TestDisableVNET(t *testing.T) {
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "jexec" && len(args) >= 3 && args[1] == "ifconfig" && args[2] == "-l" {
			return []byte("epair0b lo0\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	if err := p.DisableVNET(context.Background(), provider.InstanceHandle{ID: "web"}); err != nil {
		t.Fatalf("DisableVNET err = %v", err)
	}
	if want := "ifconfig epair0b -vnet web"; !hasCmd(fake, want) {
		t.Errorf("missing move-out %q", want)
	}
	if want := "ifconfig epair0a destroy"; !hasCmd(fake, want) {
		t.Errorf("missing destroy %q", want)
	}
}

func TestGetVNETStatus(t *testing.T) {
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "jls" {
			return []byte("1\n"), nil
		}
		if cmd == "jexec" && len(args) >= 3 && args[1] == "ifconfig" && args[2] == "-l" {
			return []byte("epair0b lo0\n"), nil
		}
		if cmd == "jexec" {
			return []byte("epair0b: flags\n\tinet 10.0.0.5 netmask 0xffffff00\n"), nil
		}
		return nil, nil
	}}
	p := &JailProvider{runner: fake}
	status, err := p.GetVNETStatus(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("GetVNETStatus err = %v", err)
	}
	if !status.Enabled {
		t.Error("expected VNET enabled")
	}
	if status.IPv4Address != "10.0.0.5" {
		t.Errorf("IPv4 = %q, want 10.0.0.5", status.IPv4Address)
	}
}
