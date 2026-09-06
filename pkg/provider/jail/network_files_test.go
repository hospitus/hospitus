package jail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestConfigureGatewayWritesRcConf(t *testing.T) {
	jailPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(jailPath, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	// test -f fails (rc.conf missing) -> append path taken.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("missing") }}
	nm := newNMWithFake(fake, nil)

	if err := nm.ConfigureGateway(context.Background(), jailPath, "10.0.0.1"); err != nil {
		t.Fatalf("ConfigureGateway err = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(jailPath, "etc", "rc.conf"))
	if err != nil {
		t.Fatalf("reading rc.conf: %v", err)
	}
	if !strings.Contains(string(data), `defaultrouter="10.0.0.1"`) {
		t.Errorf("rc.conf missing defaultrouter: %q", string(data))
	}
}

func TestConfigureGatewayAlreadyConfigured(t *testing.T) {
	jailPath := t.TempDir()
	// test -f succeeds AND grep finds defaultrouter -> skip (no write).
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	nm := newNMWithFake(fake, nil)
	if err := nm.ConfigureGateway(context.Background(), jailPath, "10.0.0.1"); err != nil {
		t.Fatalf("ConfigureGateway err = %v", err)
	}
	// rc.conf should not have been created.
	if _, err := os.Stat(filepath.Join(jailPath, "etc", "rc.conf")); !os.IsNotExist(err) {
		t.Error("rc.conf should not be written when already configured")
	}
}

func TestConfigureDNS(t *testing.T) {
	jailPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(jailPath, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	nm := newNMWithFake(&execx.Fake{}, nil)

	if err := nm.ConfigureDNS(jailPath, "10.0.0.1"); err != nil {
		t.Fatalf("ConfigureDNS err = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(jailPath, "etc", "resolv.conf"))
	if err != nil {
		t.Fatalf("reading resolv.conf: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected resolv.conf to have content")
	}
}
