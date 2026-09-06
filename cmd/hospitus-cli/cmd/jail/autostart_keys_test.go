package jail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAutostartKeysMatchTheProvider guards a pair of names that can drift
// apart in silence: the CLI sends a provider-config key that the jail provider
// reads by name, and a key it does not recognize is dropped without an error.
//
// The two live in different packages and neither imports the other, so the
// check is on the source: whatever set.go sends, config.go must have a case
// for.
func TestAutostartKeysMatchTheProvider(t *testing.T) {
	// Both paths, not just set.go: create.go builds the same provider config
	// on the way in, and a key that drifts there is just as silently dropped.
	sent := append(providerConfigKeys(t, "set.go"), providerConfigKeys(t, "create.go")...)
	if len(sent) == 0 {
		t.Fatal("no provider config keys found in set.go or create.go; have the files moved?")
	}

	// The whole provider package, not one file: some keys are read where the
	// jail is configured, others where it is started, and a key read anywhere
	// is a key that arrives.
	providerSource := readPackage(t, filepath.Join("..", "..", "..", "..",
		"pkg", "provider", "jail"))

	for _, key := range sent {
		if !strings.Contains(providerSource, `"`+key+`"`) {
			t.Errorf("set.go sends %q, which the jail provider never reads; "+
				"the setting is dropped and the command reports success anyway", key)
		}
	}
}

// readPackage concatenates a package's non-test sources.
func readPackage(t *testing.T, dir string) string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b.WriteString(readSource(t, filepath.Join(dir, name)))
	}
	return b.String()
}

// providerConfigKeys returns the keys a file puts into req.ProviderConfig.
func providerConfigKeys(t *testing.T, name string) []string {
	t.Helper()

	// Both spellings: set.go writes through a request, create.go through the
	// spec it builds. Matching only one of them let the other drift unwatched.
	markers := []string{`req.ProviderConfig["`, `spec.ProviderConfig["`}

	var keys []string
	for _, line := range strings.Split(readSource(t, name), "\n") {
		for _, marker := range markers {
			i := strings.Index(line, marker)
			if i < 0 {
				continue
			}
			rest := line[i+len(marker):]
			if end := strings.Index(rest, `"`); end > 0 {
				keys = append(keys, rest[:end])
			}
		}
	}
	return keys
}

func readSource(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
