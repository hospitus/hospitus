package jail

import (
	"context"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/config"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// bridgeFake builds a NetworkManager whose commands are recorded. existingAddrs
// is the ifconfig output returned for the bridge, letting a test present a
// bridge that already exists with or without its gateway address.
func bridgeFake(t *testing.T, bridgeExists bool, existingAddrs string) (*NetworkManager, *execx.Fake) {
	t.Helper()

	nm := NewNetworkManager(&config.Config{AutoCreateBridges: true}, nil)
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		// `ifconfig <bridge>` with no further arguments is the existence probe and
		// the address query; anything else is a mutation.
		// The existence probe is "ifconfig <name>". "ifconfig -a" is also a
		// single argument, and CheckIPConflicts uses it, so matching on the
		// count alone made the conflict scan answer with the bridge's own
		// addresses.
		if cmd == "ifconfig" && len(args) == 1 && args[0] != "-a" {
			if !bridgeExists {
				return nil, errNoSuchInterface
			}
			return []byte(existingAddrs), nil
		}
		return nil, nil
	}}
	nm.runner = fake
	return nm, fake
}

// errNoSuchInterface stands in for ifconfig's failure on a missing interface.
var errNoSuchInterface = &ifconfigError{}

type ifconfigError struct{}

func (*ifconfigError) Error() string { return "interface does not exist" }

func ranCommand(f *execx.Fake, want string) bool {
	for _, c := range f.Calls {
		if c.Name+" "+strings.Join(c.Args, " ") == want {
			return true
		}
	}
	return false
}

func TestEnsureBridgeCreatesAndAddressesBridge(t *testing.T) {
	nm, fake := bridgeFake(t, false, "")

	if err := nm.EnsureBridge(context.Background(), "hospitus0", "10.0.0.0/24"); err != nil {
		t.Fatalf("EnsureBridge: %v", err)
	}

	if !ranCommand(fake, "ifconfig bridge create name hospitus0") {
		t.Error("bridge was not created")
	}
	if !ranCommand(fake, "ifconfig hospitus0 up") {
		t.Error("bridge was not brought up")
	}
	if !ranCommand(fake, "ifconfig hospitus0 inet 10.0.0.1/24 alias") {
		t.Errorf("gateway address was not configured; calls: %v", fake.Calls)
	}
}

// A bridge left over from an earlier run must still receive its gateway address:
// without it every jail attached to the bridge starts with a default route to
// nothing.
func TestEnsureBridgeAddressesPreexistingBridge(t *testing.T) {
	nm, fake := bridgeFake(t, true, "hospitus0: flags=8843<UP,BROADCAST,RUNNING>\n\tether 58:9c:fc:10:d0:b3\n")

	if err := nm.EnsureBridge(context.Background(), "hospitus0", "10.0.0.0/24"); err != nil {
		t.Fatalf("EnsureBridge: %v", err)
	}

	if ranCommand(fake, "ifconfig bridge create name hospitus0") {
		t.Error("an existing bridge must not be recreated")
	}
	if !ranCommand(fake, "ifconfig hospitus0 inet 10.0.0.1/24 alias") {
		t.Errorf("gateway address was not added to the existing bridge; calls: %v", fake.Calls)
	}
}

// Re-running start on a healthy host must not touch the bridge.
func TestEnsureBridgeIsIdempotent(t *testing.T) {
	nm, fake := bridgeFake(t, true, "hospitus0: flags=8843<UP,BROADCAST,RUNNING>\n\tinet 10.0.0.1 netmask 0xffffff00\n")

	if err := nm.EnsureBridge(context.Background(), "hospitus0", "10.0.0.0/24"); err != nil {
		t.Fatalf("EnsureBridge: %v", err)
	}

	for _, c := range fake.Calls {
		if len(c.Args) > 1 {
			t.Errorf("unexpected mutation on an already-configured bridge: %v", c)
		}
	}
}

func TestEnsureBridgeWithoutPoolLeavesBridgeUnaddressed(t *testing.T) {
	nm, fake := bridgeFake(t, false, "")

	if err := nm.EnsureBridge(context.Background(), "hospitus0", ""); err != nil {
		t.Fatalf("EnsureBridge: %v", err)
	}

	if !ranCommand(fake, "ifconfig bridge create name hospitus0") {
		t.Error("bridge should still be created without a pool")
	}
	for _, c := range fake.Calls {
		if len(c.Args) >= 2 && c.Args[1] == "inet" {
			t.Errorf("no address must be configured without a pool, got %v", c)
		}
	}
}

// TestEnsureBridgeWithoutPoolIgnoresGlobalDefault is the regression that the
// test above cannot catch: with no global pool configured, falling back to it
// looks the same as not falling back. Configure one, and an empty ipPool must
// still leave the bridge unaddressed — the default pool's first address belongs
// to the default bridge, and putting it on a named bridge is what made stacks
// like guacamole fail to start with an IP conflict.
func TestEnsureBridgeWithoutPoolIgnoresGlobalDefault(t *testing.T) {
	nm := NewNetworkManager(&config.Config{AutoCreateBridges: true, IPPool: "10.0.0.0/24"}, nil)
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "ifconfig" && len(args) == 1 {
			return nil, errNoSuchInterface
		}
		return nil, nil
	}}
	nm.runner = fake

	if err := nm.EnsureBridge(context.Background(), "guacbr0", ""); err != nil {
		t.Fatalf("EnsureBridge: %v", err)
	}

	for _, c := range fake.Calls {
		if len(c.Args) >= 2 && c.Args[1] == "inet" {
			t.Errorf("named bridge took the default pool's gateway: %v", c)
		}
	}
}
