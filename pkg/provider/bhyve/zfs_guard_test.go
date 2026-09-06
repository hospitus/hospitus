package bhyve

import (
	"context"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// TestSnapshotHandlesAreBoundToTheirInstance covers the destructive family at
// its resolution point.
//
// The handle is supplied by the caller, not by CreateSnapshot, so "constructed
// during creation from validated inputs" was never a property of the value that
// reaches zfs. A handle declaring one VM while naming another's snapshot ran
// "zfs rollback -r" on the second — which destroys every snapshot taken after
// it — and "zfs destroy" likewise.
func TestSnapshotHandlesAreBoundToTheirInstance(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, nil }}
	p := &BhyveProvider{runner: fake, zfsParent: testZFSParent}

	for _, tc := range []struct {
		name, zfsName, dataset string
	}{
		{"another instance", testZFSParent + "/db/disk0@backup1", testZFSParent + "/db/disk0"},
		{"outside the parent", "tank/elsewhere/disk0@backup1", "tank/elsewhere/disk0"},
		{"a bare dataset", testZFSParent + "/win11/disk0", testZFSParent + "/win11/disk0"},
		{"a traversal", testZFSParent + "/../../other@s", testZFSParent + "/../../other"},
		// zfs(8) reads "%" as a range separator with optional bounds, so these
		// name several snapshots — or every one the volume has.
		{"a snapshot range", testZFSParent + "/win11/disk0@a%b", testZFSParent + "/win11/disk0"},
		{"every snapshot", testZFSParent + "/win11/disk0@%", testZFSParent + "/win11/disk0"},
		// The instance root is not a disk. CloneFromSnapshot derives its target
		// two directories up, so accepting it puts the clone at
		// "zroot/hospitus/copy/disk0" — outside the parent entirely.
		{"the instance root", testZFSParent + "/win11@backup1", testZFSParent + "/win11"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := provider.SnapshotHandle{
				Instance: "win11",
				Metadata: map[string]interface{}{"zfs_name": tc.zfsName, "dataset": tc.dataset},
			}
			if err := p.DeleteSnapshot(context.Background(), h); err == nil {
				t.Error("DeleteSnapshot accepted it")
			}
			if _, err := p.CloneFromSnapshot(context.Background(), h, "copy",
				provider.CloneOptions{}); err == nil {
				t.Error("CloneFromSnapshot accepted it")
			}
		})
	}

	// No destructive zfs command may have run on any of them.
	for _, c := range fake.Snapshot() {
		if c.Name != "zfs" || len(c.Args) == 0 {
			continue
		}
		switch c.Args[0] {
		case "destroy", "rollback", "clone", "send", "receive":
			t.Errorf("a zfs %s ran on a rejected handle: %v", c.Args[0], c.Args)
		}
	}

	// Each name is bound to win11 here, so DeleteSnapshot — which reads only
	// zfs_name — is right to accept it. Only the clone uses both, and cloning
	// disk0's snapshot into the place computed for disk9 is what must not
	// happen.
	disagreeing := provider.SnapshotHandle{
		Instance: "win11",
		Metadata: map[string]interface{}{
			"zfs_name": testZFSParent + "/win11/disk0@backup1",
			"dataset":  testZFSParent + "/win11/disk9",
		},
	}
	if _, err := p.datasetFor(disagreeing); err == nil ||
		!strings.Contains(err.Error(), "inconsistent") {
		t.Errorf("datasetFor: got %v, want the inconsistency refused", err)
	}

	// And the shape a real producer emits is still accepted.
	good := provider.SnapshotHandle{
		Instance: "win11",
		Metadata: map[string]interface{}{
			"zfs_name": testZFSParent + "/win11/disk0@backup1",
			"dataset":  testZFSParent + "/win11/disk0",
		},
	}
	if _, err := p.snapshotFor(good); err != nil {
		t.Errorf("snapshotFor refused a well-formed handle: %v", err)
	}
	if _, err := p.datasetFor(good); err != nil {
		t.Errorf("datasetFor refused a well-formed handle: %v", err)
	}
	if err := p.DeleteSnapshot(context.Background(), good); err != nil &&
		strings.Contains(err.Error(), "belongs to") {
		t.Errorf("DeleteSnapshot refused a well-formed handle: %v", err)
	}
}
