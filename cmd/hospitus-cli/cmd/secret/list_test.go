package secret

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/manifest"
)

// TestPrintScopesSaysWhenAScopeIsEmpty covers a scope directory that outlived
// the secrets it held: the command printed nothing at all and exited 0.
func TestPrintScopesSaysWhenAScopeIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := manifest.NewFileSecretStore(dir)

	var out bytes.Buffer
	printScopes(&out, store, []string{"web"})

	if !strings.Contains(out.String(), "No secrets found.") {
		t.Errorf("an empty scope printed %q", out.String())
	}
}

func TestPrintScopesListsWhatIsThere(t *testing.T) {
	dir := t.TempDir()
	store := manifest.NewFileSecretStore(dir)
	// Get generates and saves a secret that is not there yet.
	if _, err := store.Get("web", "db-password"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	printScopes(&out, store, []string{"web"})

	if !strings.Contains(out.String(), "Scope: web") || !strings.Contains(out.String(), "db-password") {
		t.Errorf("listing missed the secret: %q", out.String())
	}
	if strings.Contains(out.String(), "No secrets found.") {
		t.Errorf("a populated scope was called empty: %q", out.String())
	}
}
