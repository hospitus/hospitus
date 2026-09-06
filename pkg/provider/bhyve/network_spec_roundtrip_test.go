package bhyve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestTheNetworkSpecSurvivesARoundTrip covers what the clone paths read back.
//
// CloneInstance and CloneFromSnapshot rebuild a VM from the spec this returns,
// and CreateInstance switches on Type. Reporting every NIC as a bridge with an
// empty Bridge sent a NAT VM into the bridge branch, where it got neither NAT
// nor a bridge. NATEnabled alone could not fix it either: it is VM-wide, so a
// VM with one bridged NIC and one NAT NIC has no way to say which is which.
func TestTheNetworkSpecSurvivesARoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := &BhyveProvider{dataDir: dir, zfsParent: testZFSParent}

	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &vmConfig{
		Name:       "web",
		TapDevs:    []string{"tap_web_0", "tap_web_1"},
		Bridges:    []string{"bridge0", ""},
		NetTypes:   []string{string(provider.NetworkTypeBridge), string(provider.NetworkTypeNAT)},
		NICMACs:    []string{"58:9c:fc:00:00:01", "58:9c:fc:00:00:02"},
		VLANIDs:    []int{0, 42},
		NATEnabled: true,
	}
	if err := p.saveVMConfig(vmDir, cfg); err != nil {
		t.Fatalf("saveVMConfig: %v", err)
	}
	reloaded, err := p.loadVMConfig(vmDir)
	if err != nil {
		t.Fatalf("loadVMConfig: %v", err)
	}

	if got := reloaded.NetTypes; len(got) != 2 || got[0] != "bridge" || got[1] != "nat" {
		t.Fatalf("net types did not survive the round trip: %v", got)
	}
	if got := reloaded.Bridges; len(got) != 2 || got[0] != "bridge0" {
		t.Errorf("bridges = %v", got)
	}
	if got := reloaded.VLANIDs; len(got) != 2 || got[1] != 42 {
		t.Errorf("vlan ids = %v", got)
	}
}
