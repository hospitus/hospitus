package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSecretsAreGeneratedNotPlaceheld pins that a secret coming out of the
// file store is a generated value.
//
// Applying a manifest used the placeholder store, so every {{ secret }}
// rendered to PLACEHOLDER-<scope>-<name>. The WordPress example created its
// database user with that as the password — two strings anyone can read in the
// repository, joined by dashes — and nothing anywhere reported a problem,
// because a placeholder works exactly as well as a password until someone
// guesses it.
func TestSecretsAreGeneratedNotPlaceheld(t *testing.T) {
	store := NewFileSecretStore(t.TempDir())

	value, err := store.Get("wordpress", "wp_db_pass")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if strings.Contains(value, "PLACEHOLDER") {
		t.Errorf("secret is a placeholder: %q", value)
	}
	if len(value) < 16 {
		t.Errorf("secret %q is too short to be a generated value", value)
	}

	// The same secret must come back, or every render would rewrite the
	// credentials the instance already holds.
	again, err := store.Get("wordpress", "wp_db_pass")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if again != value {
		t.Errorf("secret changed between reads: %q then %q", value, again)
	}
}

// TestResolveSecretsDirFallsBackToTheUser covers where an unprivileged caller
// keeps its secrets.
//
// The system directory belongs to root, so an unprivileged "hospitus apply" falls
// back to a directory of the user's own — never to a shared temp path, where
// another user could pre-create a symlink and read what lands there.
func TestResolveSecretsDirFallsBackToTheUser(t *testing.T) {
	dir := resolveSecretsDir()

	if dir == "" {
		t.Fatal("resolveSecretsDir returned nothing")
	}
	if strings.HasPrefix(dir, os.TempDir()) {
		t.Errorf("secrets resolved into the temp directory: %q", dir)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory to check the fallback against")
	}
	if dir != DefaultSecretsDir && !strings.HasPrefix(dir, home) {
		t.Errorf("secrets resolved to %q, which is neither %q nor under %q",
			dir, DefaultSecretsDir, home)
	}
}

// TestWritableDirAnswersByWriting covers the check behind that choice.
//
// MkdirAll succeeds on a directory that already exists and refuses this
// process, so asking it alone would have picked the root-owned path and failed
// later, at the point where a secret is needed.
func TestWritableDirAnswersByWriting(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes everywhere; the refusal cannot be staged")
	}

	base := t.TempDir()
	readOnly := filepath.Join(base, "read-only")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if writableDir(filepath.Join(readOnly, "secrets")) {
		t.Error("a directory that cannot be created was reported writable")
	}
	if !writableDir(filepath.Join(base, "fresh")) {
		t.Error("a directory that can be created was reported unwritable")
	}
}
