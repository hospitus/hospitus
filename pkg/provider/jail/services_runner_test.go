package jail

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestEnableService(t *testing.T) {
	p, fake := runningProvider(t, "web", nil)
	if err := p.EnableService(context.Background(), provider.InstanceHandle{ID: "web"}, "nginx"); err != nil {
		t.Fatalf("EnableService err = %v", err)
	}
	if want := "jexec web sysrc nginx_enable=YES"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestDisableService(t *testing.T) {
	p, fake := runningProvider(t, "web", nil)
	if err := p.DisableService(context.Background(), provider.InstanceHandle{ID: "web"}, "nginx"); err != nil {
		t.Fatalf("DisableService err = %v", err)
	}
	if want := "jexec web sysrc nginx_enable=NO"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestServiceActions(t *testing.T) {
	tests := []struct {
		name string
		call func(p *JailProvider, h provider.InstanceHandle) error
		want string
	}{
		{"start", func(p *JailProvider, h provider.InstanceHandle) error {
			return p.StartService(context.Background(), h, "nginx")
		}, "jexec web service nginx start"},
		{"stop", func(p *JailProvider, h provider.InstanceHandle) error {
			return p.StopService(context.Background(), h, "nginx")
		}, "jexec web service nginx stop"},
		{"restart", func(p *JailProvider, h provider.InstanceHandle) error {
			return p.RestartService(context.Background(), h, "nginx")
		}, "jexec web service nginx restart"},
		{"reload", func(p *JailProvider, h provider.InstanceHandle) error {
			return p.ReloadService(context.Background(), h, "nginx")
		}, "jexec web service nginx reload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, fake := runningProvider(t, "web", nil)
			if err := tt.call(p, provider.InstanceHandle{ID: "web"}); err != nil {
				t.Fatalf("err = %v", err)
			}
			if !hasCmd(fake, tt.want) {
				t.Errorf("missing %q; got %q", tt.want, lastCmd(fake))
			}
		})
	}
}

func TestServiceActionJailNotRunning(t *testing.T) {
	// No config file -> GetInstanceState returns not found -> ensureJailRunning fails.
	p := &JailProvider{stateDir: t.TempDir(), runner: &execx.Fake{}}
	if err := p.StartService(context.Background(), provider.InstanceHandle{ID: "ghost"}, "nginx"); err == nil {
		t.Fatal("expected error when jail not running")
	}
}

func TestGetServiceStatus(t *testing.T) {
	p, _ := runningProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		// sysrc -n nginx_enable -> YES
		if cmd == "jexec" && len(args) >= 3 && args[1] == "sysrc" {
			return []byte("YES\n"), nil
		}
		// service onestatus succeeds (running)
		return nil, nil
	})
	info, err := p.GetServiceStatus(context.Background(), provider.InstanceHandle{ID: "web"}, "nginx")
	if err != nil {
		t.Fatalf("GetServiceStatus err = %v", err)
	}
	if !info.Enabled {
		t.Error("expected service to be enabled")
	}
	if !info.Running {
		t.Error("expected service to be running")
	}
}

func TestListServices(t *testing.T) {
	p, _ := runningProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		if cmd == "jexec" && len(args) >= 2 && args[1] == "ls" {
			return []byte("nginx\nsshd\n.hidden\nfoo.sample\n"), nil
		}
		return nil, nil
	})
	services, err := p.ListServices(context.Background(), provider.InstanceHandle{ID: "web"})
	if err != nil {
		t.Fatalf("ListServices err = %v", err)
	}
	// nginx + sshd from each of two dirs = 4 (hidden and .sample skipped).
	if len(services) != 4 {
		t.Fatalf("got %d services, want 4: %+v", len(services), services)
	}
}

func TestSetAndGetServiceConfig(t *testing.T) {
	p, fake := runningProvider(t, "web", func(cmd string, args []string) ([]byte, error) {
		if cmd == "jexec" && len(args) >= 4 && args[1] == "sysrc" && args[2] == "-n" {
			return []byte("-c /custom.conf\n"), nil
		}
		return nil, nil
	})
	if err := p.SetServiceConfig(context.Background(), provider.InstanceHandle{ID: "web"}, "nginx", "flags", "-c /custom.conf"); err != nil {
		t.Fatalf("SetServiceConfig err = %v", err)
	}
	if want := "jexec web sysrc nginx_flags=-c /custom.conf"; !hasCmd(fake, want) {
		t.Errorf("missing %q; got %q", want, lastCmd(fake))
	}

	val, err := p.GetServiceConfig(context.Background(), provider.InstanceHandle{ID: "web"}, "nginx", "flags")
	if err != nil {
		t.Fatalf("GetServiceConfig err = %v", err)
	}
	if val != "-c /custom.conf" {
		t.Errorf("value = %q", val)
	}
}
