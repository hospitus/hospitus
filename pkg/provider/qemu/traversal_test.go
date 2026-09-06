package qemu

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// TestAHandleCannotEscapeTheDataDirectory covers the entry surface rather than
// the methods a review happened to name.
//
// Every path this provider builds joins the instance id onto dataDir or
// stateDir, and the QMP and serial sockets are opened from those paths. An id
// of ".." resolves to their parent, and DeleteInstance hands that to a removal.
func TestAHandleCannotEscapeTheDataDirectory(t *testing.T) {
	root := t.TempDir()
	p := &QEMUProvider{
		dataDir:  filepath.Join(root, "data"),
		stateDir: filepath.Join(root, "state"),
		imageDir: filepath.Join(root, "images"),
		runner:   &execx.Fake{},
	}
	for _, d := range []string{p.dataDir, p.stateDir, p.imageDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	witness := filepath.Join(root, "witness")
	if err := os.WriteFile(witness, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for _, id := range []string{"..", "../..", "web/../..", "/etc", "", "-drive"} {
		h := provider.InstanceHandle{ID: id, Provider: "qemu"}

		if err := p.DeleteInstance(ctx, h, true); err == nil {
			t.Errorf("DeleteInstance accepted the id %q", id)
		}
		if err := p.StartInstance(ctx, h); err == nil {
			t.Errorf("StartInstance accepted the id %q", id)
		}
		if _, err := p.GetInstanceState(ctx, h); err == nil {
			t.Errorf("GetInstanceState accepted the id %q", id)
		}
		if err := p.InsertMedia(ctx, h, provider.MediaSpec{Path: "/x.iso"}); err == nil {
			t.Errorf("InsertMedia accepted the id %q", id)
		}
		if err := p.PauseInstance(ctx, h); err == nil {
			t.Errorf("PauseInstance accepted the id %q", id)
		}
	}

	for _, path := range []string{witness, p.dataDir, p.stateDir} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was removed: %v", path, err)
		}
	}
}
