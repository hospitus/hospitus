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

// TestHookConfinementResolvesParentSymlinks covers the gap a final-component
// check leaves: a symlinked directory *inside* the allowed hooks directory.
// The script itself is an ordinary file, so the symlink branch never fires, and
// a textual prefix test on the unresolved path passes on a script that lives
// outside every allowed directory.
//
// A hooks directory the operator symlinked wholesale is a different matter and
// stays allowed: that is where they chose to keep their hooks, and ownership
// and permission checks still apply to the script itself.
func TestHookConfinementResolvesParentSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "post-backup.sh"),
		[]byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	dataDir := filepath.Join(root, "data")
	hooks := filepath.Join(dataDir, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	// A subdirectory of the allowed directory points out of the tree.
	if err := os.Symlink(outside, filepath.Join(hooks, "sub")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	bm := &BackupManager{dataDir: dataDir}
	if _, err := bm.validateHookPath(filepath.Join(hooks, "sub", "post-backup.sh")); err == nil {
		t.Error("a hook reached through a symlinked subdirectory was accepted")
	}
}

// TestParseScheduleRejectsNonPositive covers what reaches time.NewTicker: it
// panics on a non-positive interval, from a goroutine, which ends the daemon.
func TestParseScheduleRejectsNonPositive(t *testing.T) {
	for _, in := range []string{"-1h", "-30m", "0s", "-0.5h"} {
		if got := parseSchedule(in); got != 0 {
			t.Errorf("parseSchedule(%q) = %v, want 0", in, got)
		}
	}
	if got := parseSchedule("2h"); got == 0 {
		t.Error("parseSchedule(\"2h\") was rejected")
	}
}

// TestCreateBackupRejectsTraversingIDs covers the id reaching filepath.Join:
// a "/" or ".." would place the stream outside the configured destination.
func TestCreateBackupRejectsTraversingIDs(t *testing.T) {
	bm := &BackupManager{configs: map[string]*BackupConfig{}, backups: map[string][]*BackupInfo{}}
	for _, id := range []string{"../../etc/cron.d/x", "a/b", "..", "/absolute"} {
		if _, err := bm.CreateBackup(context.Background(), id, BackupTypeFull); err == nil {
			t.Errorf("instance id %q was accepted", id)
		}
	}
}
