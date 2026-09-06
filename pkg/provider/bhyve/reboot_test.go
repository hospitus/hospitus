package bhyve

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
)

// exitErrorWithCode produces the *exec.ExitError a finished process yields, so
// the reboot decision can be tested without running bhyve.
func exitErrorWithCode(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil && code != 0 {
		t.Fatalf("expected a non-zero exit for code %d", code)
	}
	return err
}

// runningVMProvider builds a provider whose VM is recorded as running.
func runningVMProvider(t *testing.T) (*BhyveProvider, string) {
	t.Helper()
	dir := t.TempDir()
	p := &BhyveProvider{dataDir: dir, stateDir: dir}
	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := p.saveVMState(vmDir, &vmState{Name: "web", State: provider.StateRunning}); err != nil {
		t.Fatal(err)
	}
	return p, vmDir
}

// TestIsBhyveRebootExit pins the exit status that means "start me again".
//
// bhyve does not restart a guest itself: it exits 0 and leaves that to its
// supervisor. Powering off is 1, halting 2, a triple fault 3 — none of those
// should bring the VM back.
func TestIsBhyveRebootExit(t *testing.T) {
	if !isBhyveRebootExit(nil) {
		t.Error("exit status 0 is the guest asking for a reboot")
	}
	for _, code := range []int{1, 2, 3, 4} {
		if isBhyveRebootExit(exitErrorWithCode(t, code)) {
			t.Errorf("exit status %d must not restart the VM", code)
		}
	}
	if isBhyveRebootExit(errors.New("signal: terminated")) {
		t.Error("a VM killed by a signal must not restart itself")
	}
}

// TestRebootRelaunchRespectsTheRecordedState covers the race with an operator:
// stop kills the process, and the supervisor sees the exit a moment later.
func TestRebootRelaunchRespectsTheRecordedState(t *testing.T) {
	p, vmDir := runningVMProvider(t)

	var reboots []time.Time
	if !p.shouldRelaunchAfterExit("web", vmDir, nil, &reboots) {
		t.Error("a running VM that rebooted should come back")
	}

	if err := p.saveVMState(vmDir, &vmState{Name: "web", State: provider.StateStopped}); err != nil {
		t.Fatal(err)
	}
	reboots = nil
	if p.shouldRelaunchAfterExit("web", vmDir, nil, &reboots) {
		t.Error("a VM recorded as stopped was restarted anyway")
	}
}

// TestRebootBudgetStopsALoop covers a guest that resets as fast as it starts:
// honoring every reset would spin.
func TestRebootBudgetStopsALoop(t *testing.T) {
	p, vmDir := runningVMProvider(t)

	var reboots []time.Time
	for i := 0; i < bhyveRebootBudget; i++ {
		if !p.shouldRelaunchAfterExit("web", vmDir, nil, &reboots) {
			t.Fatalf("reboot %d was inside the budget and should have been honored", i+1)
		}
	}
	if p.shouldRelaunchAfterExit("web", vmDir, nil, &reboots) {
		t.Error("a guest resetting in a loop was restarted past the budget")
	}

	// A reset long after the others starts a fresh window.
	reboots = []time.Time{time.Now().Add(-2 * bhyveRebootWindow)}
	if !p.shouldRelaunchAfterExit("web", vmDir, nil, &reboots) {
		t.Error("an old reboot should not count against the current window")
	}
}
