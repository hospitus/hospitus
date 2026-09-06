package jail

import (
	"context"
	"errors"
	"testing"

	"github.com/hospitus/hospitus/pkg/config"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestEnsureBridgeDisabledMissing(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("no such") }}
	nm := newNMWithFake(fake, &config.Config{AutoCreateBridges: false})
	if err := nm.EnsureBridge(context.Background(), "hospitus0", ""); err == nil {
		t.Fatal("expected error when bridge missing and auto-create disabled")
	}
}

func TestEnsureBridgeAlreadyExists(t *testing.T) {
	// ifconfig <name> succeeds -> exists -> no create.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	nm := newNMWithFake(fake, &config.Config{AutoCreateBridges: true})
	if err := nm.EnsureBridge(context.Background(), "hospitus0", ""); err != nil {
		t.Fatalf("EnsureBridge err = %v", err)
	}
	if hasCmd(fake, "ifconfig bridge create name hospitus0") {
		t.Error("should not create an existing bridge")
	}
}

func TestEnsureBridgeCreates(t *testing.T) {
	// ifconfig <name> fails (missing); create + up succeed. No pool -> no IP config.
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		// existence probe: exactly "ifconfig hospitus0"
		if name == "ifconfig" && len(args) == 1 && args[0] == "hospitus0" {
			return nil, errors.New("missing")
		}
		return nil, nil
	}}
	nm := newNMWithFake(fake, &config.Config{AutoCreateBridges: true})
	if err := nm.EnsureBridge(context.Background(), "hospitus0", ""); err != nil {
		t.Fatalf("EnsureBridge err = %v", err)
	}
	if want := "ifconfig bridge create name hospitus0"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
	if want := "ifconfig hospitus0 up"; !hasCmd(fake, want) {
		t.Errorf("missing %q", want)
	}
}

func TestHasIPOnInterface(t *testing.T) {
	out := "hospitus0: flags=8843\n\tinet 10.0.0.1 netmask 0xffffff00\n"
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(out), nil }}
	nm := newNMWithFake(fake, nil)

	if !nm.hasIPOnInterface(context.Background(), "hospitus0", "10.0.0.1") {
		t.Error("expected 10.0.0.1 to be found")
	}
	// 10.0.0.10 must not spuriously match 10.0.0.1.
	if nm.hasIPOnInterface(context.Background(), "hospitus0", "10.0.0.10") {
		t.Error("10.0.0.10 should not match")
	}
}

func TestEnsureGatewayOnBridgeAlreadyPresent(t *testing.T) {
	out := "hospitus0: flags=8843\n\tinet 10.0.0.1 netmask 0xffffff00\n"
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(out), nil }}
	nm := newNMWithFake(fake, nil)
	if err := nm.EnsureGatewayOnBridge(context.Background(), "hospitus0", "10.0.0.1", "10.0.0.5/24"); err != nil {
		t.Fatalf("err = %v", err)
	}
	// Gateway already present -> should not issue an alias add.
	if hasCmd(fake, "ifconfig hospitus0 inet 10.0.0.1 netmask 255.255.255.0 alias") {
		t.Error("should not add an already-present gateway")
	}
}

func TestEnsureGatewayOnBridgeAdds(t *testing.T) {
	// ifconfig probe returns no matching inet -> gateway added.
	fake := &execx.Fake{Func: func(name string, args []string) ([]byte, error) {
		if name == "ifconfig" && len(args) == 1 {
			return []byte("hospitus0: flags=8843\n"), nil
		}
		return nil, nil
	}}
	nm := newNMWithFake(fake, nil)
	if err := nm.EnsureGatewayOnBridge(context.Background(), "hospitus0", "10.0.0.1", "10.0.0.5/24"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if want := "ifconfig hospitus0 inet 10.0.0.1 netmask 255.255.255.0 alias"; !hasCmd(fake, want) {
		t.Errorf("missing gateway alias %q; got %+v", want, fake.Calls)
	}
}

func TestDetectAndWarnIPConflictsFound(t *testing.T) {
	// A conflicting interface is found -> returns an error.
	out := "em0: flags=8843\n\tinet 10.0.0.1 netmask 0xffffff00\n"
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(out), nil }}
	nm := newNMWithFake(fake, nil)
	if err := nm.DetectAndWarnIPConflicts(context.Background(), "hospitus0", "10.0.0.1"); err == nil {
		t.Fatal("expected error when IP conflict detected")
	}
}

func TestDetectAndWarnIPConflictsNone(t *testing.T) {
	// No matching inet -> no conflict.
	out := "em0: flags=8843\n\tinet 192.168.1.1 netmask 0xffffff00\n"
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return []byte(out), nil }}
	nm := newNMWithFake(fake, nil)
	if err := nm.DetectAndWarnIPConflicts(context.Background(), "hospitus0", "10.0.0.1"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestGetGatewayIPFromSpecificPool(t *testing.T) {
	nm := newNMWithFake(&execx.Fake{}, nil)
	gw := nm.getGatewayIPFromSpecificPool("10.20.0.0/24")
	if gw == "" {
		t.Error("expected a gateway IP for a CIDR pool")
	}
}
