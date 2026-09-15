package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestLookupNeverGenerates is the difference between the two readers.
//
// Get materializes what a manifest declares, which is right for a render and
// wrong for "hospitus secret get": a read that mints a fresh credential hands
// back a value nothing else using that name will match.
func TestLookupNeverGenerates(t *testing.T) {
	dir := t.TempDir()
	store := NewFileSecretStore(dir)

	if _, err := store.Lookup("demo", "apikey"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Lookup of a missing secret = %v, want ErrSecretNotFound", err)
	}
	if n := countFiles(t, dir); n != 0 {
		t.Errorf("Lookup created %d file(s); it must not write", n)
	}

	// Get, by contrast, is expected to generate — that is what a manifest
	// render relies on.
	generated, err := store.Get("demo", "apikey")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if generated == "" {
		t.Fatal("Get returned an empty secret")
	}
	if n := countFiles(t, dir); n != 1 {
		t.Errorf("Get left %d file(s), want 1", n)
	}

	// And now Lookup answers with what Get wrote, unchanged.
	got, err := store.Lookup("demo", "apikey")
	if err != nil {
		t.Fatalf("Lookup after Get: %v", err)
	}
	if got != generated {
		t.Errorf("Lookup = %q, want the stored %q", got, generated)
	}
}

// An empty file is a crash between create and write. Lookup reports it rather
// than repairing it: repairing is a write.
func TestLookupReportsAnEmptySecret(t *testing.T) {
	dir := t.TempDir()
	store := NewFileSecretStore(dir)

	if _, err := store.Get("demo", "apikey"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	path := filepath.Join(dir, "demo", "apikey")
	if _, err := os.Stat(path); err != nil {
		// The layout is the store's business; find the one file it wrote.
		path = onlyFile(t, dir)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Lookup("demo", "apikey"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("Lookup of an empty secret = %v, want ErrSecretNotFound", err)
	}
	if n := countFiles(t, dir); n != 1 {
		t.Errorf("Lookup changed the store: %d file(s), want the empty one left alone", n)
	}
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func onlyFile(t *testing.T, dir string) string {
	t.Helper()
	var found string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatal("no file in the store")
	}
	return found
}
