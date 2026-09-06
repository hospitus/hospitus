package jail

import (
	"os"
	"path/filepath"
	"testing"
)

// TestValidateMirrorScheme verifies only HTTPS debootstrap mirrors are accepted
// (audit HIGH linux.go:390: base packages were fetched over plaintext HTTP with
// --no-check-gpg).
func TestValidateMirrorScheme(t *testing.T) {
	if err := validateMirrorScheme("https://deb.debian.org/debian"); err != nil {
		t.Errorf("https mirror rejected: %v", err)
	}
	for _, m := range []string{"http://deb.debian.org/debian", "ftp://x/debian", "deb.debian.org"} {
		if err := validateMirrorScheme(m); err == nil {
			t.Errorf("insecure mirror %q accepted", m)
		}
	}
}

// TestFirstExistingFile verifies keyring selection picks an existing file and
// returns "" when none exist (which makes debootstrapBase fail closed rather
// than fall back to --no-check-gpg).
func TestFirstExistingFile(t *testing.T) {
	if got := firstExistingFile([]string{"/nonexistent/a", "/nonexistent/b"}); got != "" {
		t.Errorf("expected empty for missing keyrings, got %q", got)
	}
	kr := filepath.Join(t.TempDir(), "keyring.gpg")
	if err := os.WriteFile(kr, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := firstExistingFile([]string{"/nonexistent/a", kr}); got != kr {
		t.Errorf("expected %q, got %q", kr, got)
	}
}
