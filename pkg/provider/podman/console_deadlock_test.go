package podman

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestConsoleCloseDoesNotDeadlockWithBlockedRead verifies Close returns while a
// Read is blocked waiting for data, i.e. Read does not hold the mutex during
// the blocking read (audit HIGH podman/console.go:90).
func TestConsoleCloseDoesNotDeadlockWithBlockedRead(t *testing.T) {
	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	defer stdinR.Close()
	defer stdoutW.Close()

	cmd := exec.Command("sleep", "1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}

	c := &PodmanConsoleConnection{
		containerID: "test",
		cmd:         cmd,
		stdin:       stdinW,
		stdout:      stdoutR,
	}

	// A Read blocks: nothing is ever written to the stdout pipe.
	readReturned := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		_, _ = c.Read(buf)
		close(readReturned)
	}()
	time.Sleep(50 * time.Millisecond) // let the Read block

	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()

	select {
	case <-closed:
		// Close returned; closing stdout should also unblock the Read.
		select {
		case <-readReturned:
		case <-time.After(2 * time.Second):
			t.Error("blocked Read did not unblock after Close")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Close deadlocked against a blocked Read")
	}
}
