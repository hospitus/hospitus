package bhyve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImageResolversStayInsideTheImageDirectory covers both resolvers.
//
// CreateInstance turns what they return into a VM disk or a cloud image to
// deploy, so a path outside the image directory hands the guest a host file. An
// absolute reference was returned on sight, and a relative one carrying ".."
// escaped the join it was built with.
func TestImageResolversStayInsideTheImageDirectory(t *testing.T) {
	root := t.TempDir()
	imageDir := filepath.Join(root, "images")
	if err := os.MkdirAll(filepath.Join(imageDir, "iso"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A real file outside the image directory, of the kind the resolvers would
	// otherwise happily return.
	outside := filepath.Join(root, "secret.img")
	if err := os.WriteFile(outside, []byte("host file"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The cloud resolver joins "<ref>-<arch>.img" and friends onto the catalog
	// directory, so a traversal resolves to these names. Without files here it
	// returns "not found" before confinement is ever reached, and the test
	// would pass without exercising the guard at all.
	for _, name := range []string{
		"secret.img-amd64.img", "secret.img-amd64.qcow2", "secret.img.img", "secret.img.qcow2",
		"secret-amd64.img", "secret.img",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("host file"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// And a legitimate one inside it.
	inside := filepath.Join(imageDir, "iso", "freebsd.iso")
	if err := os.WriteFile(inside, []byte("iso"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &BhyveProvider{imageDir: imageDir, dataDir: filepath.Join(root, "data")}

	for _, ref := range []string{
		outside,                // an absolute path outside
		"../secret.img",        // a traversal out of the iso directory
		"iso/../../secret.img", // and a longer one
	} {
		if got, err := p.resolveISOPath(ref); err == nil {
			t.Errorf("resolveISOPath(%q) returned %q", ref, got)
		}
		if got, err := p.resolveCloudImagePath(ref, "amd64"); err == nil {
			t.Errorf("resolveCloudImagePath(%q) returned %q", ref, got)
		}
	}

	// The ordinary case still resolves.
	got, err := p.resolveISOPath("freebsd.iso")
	if err != nil {
		t.Fatalf("resolveISOPath refused a legitimate image: %v", err)
	}
	if !strings.HasPrefix(got, imageDir) {
		t.Errorf("resolveISOPath = %q, want a path under %q", got, imageDir)
	}
}
