package bhyve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestIsXZCompressed(t *testing.T) {
	dir := t.TempDir()

	xzPath := filepath.Join(dir, "image.xz")
	if err := os.WriteFile(xzPath, []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00, 0x01, 0x02}, 0o600); err != nil {
		t.Fatal(err)
	}
	plainPath := filepath.Join(dir, "image.raw")
	if err := os.WriteFile(plainPath, []byte("not compressed at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	tinyPath := filepath.Join(dir, "tiny")
	if err := os.WriteFile(tinyPath, []byte{0xfd, 0x37}, 0o600); err != nil {
		t.Fatal(err)
	}

	if ok, err := isXZCompressed(xzPath); err != nil || !ok {
		t.Errorf("isXZCompressed(xz) = (%v,%v), want (true,nil)", ok, err)
	}
	if ok, err := isXZCompressed(plainPath); err != nil || ok {
		t.Errorf("isXZCompressed(plain) = (%v,%v), want (false,nil)", ok, err)
	}
	if ok, err := isXZCompressed(tinyPath); err != nil || ok {
		t.Errorf("isXZCompressed(tiny) = (%v,%v), want (false,nil) for too-small file", ok, err)
	}
	if _, err := isXZCompressed(filepath.Join(dir, "missing")); err == nil {
		t.Error("isXZCompressed(missing) should return an error")
	}
}

func TestValidateExtractionPathCleanTree(t *testing.T) {
	// Arrange: a directory tree fully contained within the root.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Act / Assert
	if err := validateExtractionPath(root); err != nil {
		t.Errorf("validateExtractionPath(clean tree) = %v, want nil", err)
	}
}

func TestValidateExtractionPathSymlinkEscape(t *testing.T) {
	// Arrange: a symlink inside root that points outside it.
	outside := t.TempDir()
	root := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	// Act / Assert
	if err := validateExtractionPath(root); err == nil {
		t.Error("expected error for a symlink escaping the extraction root")
	}
}

func TestCreateFromOSProfileUnknownProfile(t *testing.T) {
	p := &BhyveProvider{dataDir: t.TempDir(), runner: &execx.Fake{}}
	_, err := p.CreateFromOSProfile(context.Background(), "no-such-profile", "vm1", nil)
	if err == nil {
		t.Error("expected error for an unknown OS profile")
	}
}
