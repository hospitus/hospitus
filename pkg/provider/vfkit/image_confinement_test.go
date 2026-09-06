package vfkit

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTheImageNameCannotLeaveTheCatalog covers the relative branch.
//
// The absolute branch was confined; the relative one joined the name onto the
// catalog directory and stat'ed the result, so enough "../" segments selected
// any readable host file — which prepareDisk then copies into the guest disk.
func TestTheImageNameCannotLeaveTheCatalog(t *testing.T) {
	root := t.TempDir()
	imageDir := filepath.Join(root, "images")
	if err := os.MkdirAll(filepath.Join(imageDir, "cloud"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A readable file outside the catalog, and a legitimate image inside it.
	if err := os.WriteFile(filepath.Join(root, "secret.img"), []byte("host"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, "cloud", "debian.img"), []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &VFKitProvider{imageDir: imageDir, dataDir: filepath.Join(root, "data")}

	for _, name := range []string{"../../secret", "../../secret.img", "cloud/../../secret.img"} {
		if got, err := p.resolveImage(name); err == nil {
			t.Errorf("resolveImage(%q) returned %q", name, got)
		}
	}

	got, err := p.resolveImage("debian")
	if err != nil {
		t.Fatalf("resolveImage refused a legitimate image: %v", err)
	}
	if filepath.Base(got) != "debian.img" {
		t.Errorf("resolveImage = %q, want the catalog's debian.img", got)
	}
}
