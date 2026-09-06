package jail

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
)

// fakeJexec puts a jexec on PATH that runs the given shell script body, so the
// exec paths that call jexec directly — they do not go through execx — can be
// exercised off FreeBSD.
func fakeJexec(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "jexec"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake jexec: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestATimeoutIsNotReportedAsAnExitCode covers both exec paths at once.
//
// A deadline kills the process, so Run returns an *exec.ExitError exactly as a
// command that exited on its own would. Reading the code off it tells the
// caller the command ran and failed — so a backup script cut off halfway is
// indistinguishable from one that declined to run.
func TestATimeoutIsNotReportedAsAnExitCode(t *testing.T) {
	fakeJexec(t, "sleep 30")
	p, _ := runningProvider(t, "web", nil)
	handle := provider.InstanceHandle{ID: "web", Provider: "jail"}
	opts := provider.ExecOptions{Command: "sleep", Args: []string{"30"}, Timeout: 1}

	t.Run("ExecCommand", func(t *testing.T) {
		// The command sleeps 30s; returning anywhere near that means the
		// deadline killed jexec but the call still waited on the pipes its
		// children inherited.
		start := time.Now()
		defer func() {
			if elapsed := time.Since(start); elapsed > 15*time.Second {
				t.Errorf("returned after %s: the deadline did not bound the wait", elapsed)
			}
		}()
		_, err := p.ExecCommand(context.Background(), handle, opts)
		if err == nil {
			t.Fatal("the timeout was reported as a normal exit")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("error does not name the timeout: %v", err)
		}
	})

	t.Run("ExecCommandStreaming", func(t *testing.T) {
		code, err := p.ExecCommandStream(context.Background(), handle, opts, os.Stdout, os.Stderr)
		if err == nil {
			t.Fatal("the timeout was reported as a normal exit")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("error does not name the timeout: %v", err)
		}
		if code != -1 {
			t.Errorf("exit code = %d, want -1 for a command that never finished", code)
		}
	})
}
