package cmdutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsLoopbackTarget guards the condition that keeps a locally-read key from
// being sent to someone else's daemon.
func TestIsLoopbackTarget(t *testing.T) {
	tests := map[string]bool{
		"localhost:8080":                true,
		"127.0.0.1:8080":                true,
		"http://127.0.0.1:8080":         true,
		"https://localhost:8443":        true,
		"http://[::1]:8080":             true,
		"127.0.0.2:8080":                true,
		"":                              true,
		"192.168.1.16:8080":             false,
		"https://hospitus.example:8443": false,
		"http://10.0.0.5/api":           false,
	}

	for target, want := range tests {
		if got := isLoopbackTarget(target); got != want {
			t.Errorf("isLoopbackTarget(%q) = %v, want %v", target, got, want)
		}
	}
}

// A key readable on this machine must not travel to a remote daemon.
func TestLocalDaemonKeyRefusesRemoteTargets(t *testing.T) {
	if key := localDaemonKey("https://hospitus.example:8443"); key != "" {
		t.Errorf("localDaemonKey returned a key for a remote target: %q", key)
	}
}

// And it must actually find one for a local daemon: the refusal test above
// passes just as well for a function that never returns a key at all.
func TestLocalDaemonKeyReadsTheServiceKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apikey")
	// Padded the way the service scripts leave it, with a trailing newline.
	if err := os.WriteFile(path, []byte("  nx-service-generated-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	original := localAPIKeyFiles
	localAPIKeyFiles = []string{path}
	t.Cleanup(func() { localAPIKeyFiles = original })

	if key := localDaemonKey("http://127.0.0.1:8443"); key != "nx-service-generated-key" {
		t.Errorf("localDaemonKey = %q, want the trimmed key from the file", key)
	}
}
