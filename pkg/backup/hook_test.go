package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRunHookConfinement verifies backup hooks are confined to an allowed
// hooks directory and reject shell commands / arbitrary executables, matching
// the jail provider's validateHookPath (audit HIGH backup.go runHook).
func TestRunHookConfinement(t *testing.T) {
	dataDir := t.TempDir()
	hooksDir := filepath.Join(dataDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A legitimate, executable hook inside the allowed directory.
	good := filepath.Join(hooksDir, "post-backup.sh")
	if err := os.WriteFile(good, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	bm := NewBackupManager(dataDir, datasetUnder("zroot/hospitus/backups"), nil)
	ctx := context.Background()

	tests := []struct {
		name    string
		hook    string
		wantErr bool
	}{
		{"allowed executable in hooks dir", good, false},
		{"arbitrary system binary", "/bin/sh", true},
		{"outside allowed dir", "/usr/bin/id", true},
		{"relative path", "post-backup.sh", true},
		{"shell metacharacters", "/bin/sh; rm -rf /", true},
		{"traversal out of hooks dir", filepath.Join(hooksDir, "../../etc/evil.sh"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := bm.runHook(ctx, tt.hook, "web")
			if tt.wantErr && err == nil {
				t.Errorf("runHook(%q) = nil, want error", tt.hook)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("runHook(%q) = %v, want nil", tt.hook, err)
			}
		})
	}
}
