package bhyve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestDeleteSnapshotRunsZFSDestroy(t *testing.T) {
	fake := &execx.Fake{}
	p := &BhyveProvider{runner: fake, zfsParent: testZFSParent}

	snap := provider.SnapshotHandle{
		ID:       "web_snap1",
		Instance: "web",
		Metadata: map[string]interface{}{"zfs_name": testZFSParent + "/web/disk0@snap1"},
	}
	if err := p.DeleteSnapshot(context.Background(), snap); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 command, got %d", len(fake.Calls))
	}
	got := strings.Join(fake.Calls[0].Args, " ")
	if got != "destroy "+testZFSParent+"/web/disk0@snap1" {
		t.Errorf("zfs args = %q, want destroy of the snapshot", got)
	}
}

func TestDeleteSnapshotMissingMetadata(t *testing.T) {
	p := &BhyveProvider{runner: &execx.Fake{}}
	if err := p.DeleteSnapshot(context.Background(), provider.SnapshotHandle{}); err == nil {
		t.Error("expected error when zfs_name metadata is missing")
	}
}

func TestDeleteSnapshotPropagatesError(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) {
		return []byte("dataset is busy"), errors.New("exit 1")
	}}
	// The handle has to pass the ownership guard, or the refusal it gets there
	// is the error this test sees — and zfs never runs at all, which is not
	// what "propagates the error from zfs" means.
	p := &BhyveProvider{runner: fake, zfsParent: testZFSParent}
	snap := provider.SnapshotHandle{
		Instance: "web",
		Metadata: map[string]interface{}{"zfs_name": testZFSParent + "/web/disk0@s"},
	}
	err := p.DeleteSnapshot(context.Background(), snap)
	if err == nil {
		t.Fatal("expected error from failing zfs destroy")
	}
	if !strings.Contains(err.Error(), "dataset is busy") {
		t.Errorf("got %v, want the zfs failure propagated", err)
	}
	if len(fake.Calls) == 0 {
		t.Error("zfs was never run: the error came from somewhere before it")
	}
}

// cmd() must fall back to the real runner when none is injected.
func TestCmdAccessorDefaults(t *testing.T) {
	p := &BhyveProvider{}
	if p.cmd() == nil {
		t.Fatal("cmd() returned nil for a runner-less provider")
	}
}
