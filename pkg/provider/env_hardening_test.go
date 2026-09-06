package provider

import (
	"slices"
	"strings"
	"testing"
)

// TestMinimalEnvDropsLoaderVariables covers what a manifest author could
// otherwise choose: PATH picks the binary, the loader variables pick the code
// it runs.
func TestMinimalEnvDropsLoaderVariables(t *testing.T) {
	env := MinimalEnv(
		"PATH=/tmp/evil",
		"LD_PRELOAD=/tmp/evil.so",
		"LD_LIBRARY_PATH=/tmp",
		"LD_32_PRELOAD=/tmp/evil.so",
		"DYLD_INSERT_LIBRARIES=/tmp/evil.dylib",
		"HOME=/home/user",
	)

	for _, e := range env {
		if strings.HasPrefix(e, "LD_") || strings.HasPrefix(e, "DYLD_") {
			t.Errorf("loader variable survived: %q", e)
		}
	}
	if !slices.Contains(env, "HOME=/home/user") {
		t.Errorf("an ordinary variable was dropped: %v", env)
	}
	if got := env[0]; !strings.HasPrefix(got, "PATH=/usr/local/sbin") {
		t.Errorf("PATH = %q, want the system default", got)
	}
}
