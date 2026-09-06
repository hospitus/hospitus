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
	p := &BhyveProvider{runner: fake}

	snap := provider.SnapshotHandle{
		ID:       "web_snap1",
		Instance: "web",
		Metadata: map[string]interface{}{"zfs_name": "zroot/hospitus/bhyve/web@snap1"},
	}
	if err := p.DeleteSnapshot(context.Background(), snap); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 command, got %d", len(fake.Calls))
	}
	got := strings.Join(fake.Calls[0].Args, " ")
	if got != "destroy zroot/hospitus/bhyve/web@snap1" {
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
	p := &BhyveProvider{runner: fake}
	snap := provider.SnapshotHandle{Metadata: map[string]interface{}{"zfs_name": "pool/vm@s"}}
	if err := p.DeleteSnapshot(context.Background(), snap); err == nil {
		t.Error("expected error from failing zfs destroy")
	}
}

// cmd() must fall back to the real runner when none is injected.
func TestCmdAccessorDefaults(t *testing.T) {
	p := &BhyveProvider{}
	if p.cmd() == nil {
		t.Fatal("cmd() returned nil for a runner-less provider")
	}
}
