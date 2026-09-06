package qemu

import (
	"net"
	"testing"
	"time"
)

// TestConsoleCloseDoesNotDeadlockWithBlockedRead verifies Close returns while a
// Read is blocked waiting for data, i.e. Read does not hold the mutex during
// the blocking socket read. A serial console on a quiet guest sends nothing for
// minutes, so a Read holding the lock made Close wait for a byte that may never
// arrive. Mirrors the podman regression test of the same name.
func TestConsoleCloseDoesNotDeadlockWithBlockedRead(t *testing.T) {
	// A socket pair stands in for the QEMU serial socket: nothing is ever
	// written to it, so the Read below blocks.
	local, remote := net.Pipe()
	defer remote.Close()

	c := &QEMUConsoleConnection{vmName: "web", conn: local}

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
		// Close returned; closing the connection also unblocks the Read.
		select {
		case <-readReturned:
		case <-time.After(2 * time.Second):
			t.Error("blocked Read did not unblock after Close")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Close deadlocked against a blocked Read")
	}
}
