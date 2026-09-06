package qemu

import (
	"bufio"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// TestQMPExecuteHonorsDeadline verifies Execute returns an error within the
// command timeout when QEMU never replies, instead of blocking forever while
// holding the client mutex (audit HIGH qmp.go:166).
func TestQMPExecuteHonorsDeadline(t *testing.T) {
	orig := qmpCommandTimeout
	qmpCommandTimeout = 150 * time.Millisecond
	defer func() { qmpCommandTimeout = orig }()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	// The peer consumes our command but never sends a response.
	go io.Copy(io.Discard, server)

	qc := &QMPClient{conn: client, reader: bufio.NewReader(client)}

	done := make(chan error, 1)
	go func() {
		_, err := qc.Execute("query-status", nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error from Execute")
		}
		// From the deadline, not from anything else Execute might refuse: the
		// point of the test is the bound on the exchange.
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Errorf("Execute failed with %v, want a deadline error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute blocked past the deadline")
	}
}
