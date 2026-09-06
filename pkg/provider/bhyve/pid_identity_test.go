package bhyve

import (
	"context"
	"os"
	"syscall"
	"testing"
)

// TestPidIsBhyveVMRejectsAnUnrelatedLiveProcess is the case that matters: a PID
// recorded when the VM started, still alive, but now held by something else.
//
// The test process itself stands in for that stranger. Signal 0 reports it as
// alive, so liveness alone would have a force stop SIGKILL whatever inherited
// the number. Identity has to be checked, not liveness.
func TestPidIsBhyveVMRejectsAnUnrelatedLiveProcess(t *testing.T) {
	pid := os.Getpid()

	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the test process should be alive: %v", err)
	}

	if pidIsBhyveVM(context.Background(), pid, "windows-passthrough") {
		t.Error("an unrelated live process was taken for the VM's bhyve process")
	}
}

// TestPidIsBhyveVMRejectsUnusablePIDs covers the values that can never name a
// process, so no caller has to guard them itself.
func TestPidIsBhyveVMRejectsUnusablePIDs(t *testing.T) {
	for _, pid := range []int{0, -1, -1000} {
		if pidIsBhyveVM(context.Background(), pid, "vm") {
			t.Errorf("pid %d was accepted", pid)
		}
	}
}

// TestPidIsBhyveVMRejectsADeadPID covers a PID nothing holds any more, which is
// the ordinary case once a VM has been shut down from inside the guest.
func TestPidIsBhyveVMRejectsADeadPID(t *testing.T) {
	// Find a PID that is not in use: start a process, wait for it, reuse its id.
	proc, err := os.StartProcess("/bin/echo", []string{"echo"}, &os.ProcAttr{})
	if err != nil {
		t.Skipf("cannot start a helper process: %v", err)
	}
	if _, err := proc.Wait(); err != nil {
		t.Skipf("cannot reap the helper process: %v", err)
	}

	if pidIsBhyveVM(context.Background(), proc.Pid, "vm") {
		t.Errorf("pid %d is gone but was accepted", proc.Pid)
	}
}
