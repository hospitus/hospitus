package provider

import (
	"os"
	"strings"
	"testing"
)

// TestMinimalEnv_NoSecretLeak verifies that MinimalEnv does not inherit the
// daemon's process environment, which would hand every hook script and every
// in-guest command the daemon's own secrets.
func TestMinimalEnv_NoSecretLeak(t *testing.T) {
	t.Setenv("HOSPITUS_API_KEY", "super-secret-key")
	t.Setenv("DB_PASSWORD", "hunter2")

	env := MinimalEnv("HOSPITUS_JAIL_NAME=web", "PAGER=cat")

	for _, kv := range env {
		if strings.Contains(kv, "super-secret-key") || strings.Contains(kv, "hunter2") {
			t.Fatalf("MinimalEnv leaked a daemon secret: %q", kv)
		}
		if strings.HasPrefix(kv, "HOSPITUS_API_KEY=") || strings.HasPrefix(kv, "DB_PASSWORD=") {
			t.Fatalf("MinimalEnv inherited a daemon variable: %q", kv)
		}
	}

	var hasPath, hasJail, hasPager bool
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "PATH="):
			hasPath = true
		case kv == "HOSPITUS_JAIL_NAME=web":
			hasJail = true
		case kv == "PAGER=cat":
			hasPager = true
		}
	}
	if !hasPath || !hasJail || !hasPager {
		t.Fatalf("MinimalEnv missing expected entries: PATH=%v JAIL=%v PAGER=%v", hasPath, hasJail, hasPager)
	}

	// Sanity: the secret really is in the process environment.
	if os.Getenv("HOSPITUS_API_KEY") != "super-secret-key" {
		t.Fatal("test setup failed: HOSPITUS_API_KEY not set in process env")
	}
}
