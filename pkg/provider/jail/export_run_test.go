package jail

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunCommandPresetStdoutFailure verifies runCommand runs a command whose
// Stdout is already redirected (as with `zfs send` streaming to a file) and
// surfaces stderr on failure — the case where (*Cmd).CombinedOutput would
// instead error immediately with "Stdout already set" (audit HIGH export.go
// 181/371: ZFS export/import always failed).
func TestRunCommandPresetStdoutFailure(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "stream"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	cmd := exec.Command("sh", "-c", "echo boom >&2; exit 3")
	cmd.Stdout = f // preset — CombinedOutput() would reject this

	err = runCommand(cmd)
	if err == nil {
		t.Fatal("expected error from failing command")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("stderr not captured in error: %v", err)
	}
}

// TestRunCommandPresetStdoutSuccess verifies stdout still reaches the preset
// writer on success.
func TestRunCommandPresetStdoutSuccess(t *testing.T) {
	var out bytes.Buffer
	cmd := exec.Command("sh", "-c", "echo hello")
	cmd.Stdout = &out
	if err := runCommand(cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out.String()) != "hello" {
		t.Errorf("stdout not written to preset writer: %q", out.String())
	}
}
