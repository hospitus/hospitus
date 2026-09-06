package vfkit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestALifecycleHandleCannotEscapeTheDataDirectory covers the whole entry
// surface, not the one method the traversal was found in.
//
// Every path helper joins the instance id onto dataDir or stateDir, so an id of
// ".." resolves to their parent — and DeleteInstance hands that to
// os.RemoveAll. The id is validated at each public entry point instead of in
// the helpers, which have eighteen call sites between them.
func TestALifecycleHandleCannotEscapeTheDataDirectory(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{dataDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A file that must still be there afterwards: it sits in the directory a
	// ".." id would resolve to.
	witness := filepath.Join(root, "witness")
	if err := os.WriteFile(witness, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &VFKitProvider{dataDir: dataDir, stateDir: stateDir}
	ctx := context.Background()

	for _, id := range []string{"..", "../..", "web/../..", "/etc", ""} {
		h := provider.InstanceHandle{ID: id, Provider: "vfkit"}

		if err := p.DeleteInstance(ctx, h, true); err == nil {
			t.Errorf("DeleteInstance accepted the id %q", id)
		}
		if err := p.StartInstance(ctx, h); err == nil {
			t.Errorf("StartInstance accepted the id %q", id)
		}
		if err := p.StopInstance(ctx, h, provider.StopOptions{}); err == nil {
			t.Errorf("StopInstance accepted the id %q", id)
		}
		if _, err := p.GetInstanceState(ctx, h); err == nil {
			t.Errorf("GetInstanceState accepted the id %q", id)
		}
	}

	if _, err := os.Stat(witness); err != nil {
		t.Fatalf("the parent directory was removed: %v", err)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("the data directory was removed: %v", err)
	}
}
