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
	// An rc.conf that already names a defaultrouter: the check reads the file
	// itself now, so the state has to be on disk rather than faked through
	// test(1) and grep(1).
	if err := os.MkdirAll(filepath.Join(jailPath, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := "hostname=\"web\"\ndefaultrouter=\"10.0.0.254\"\n"
	if err := os.WriteFile(filepath.Join(jailPath, "etc", "rc.conf"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	nm := newNMWithFake(fake, nil)
	if err := nm.ConfigureGateway(context.Background(), jailPath, "10.0.0.1"); err != nil {
		t.Fatalf("ConfigureGateway err = %v", err)
	}
	// The existing file must be left exactly as it was.
	data, err := os.ReadFile(filepath.Join(jailPath, "etc", "rc.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != existing {
		t.Errorf("rc.conf was rewritten although a defaultrouter was already set:\n%s", data)
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
