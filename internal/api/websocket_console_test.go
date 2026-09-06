package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestWebSocketConsoleDeadlock verifies the deadlock fix in close():
// the mutex is released during cmd.Wait() to avoid blocking concurrent
// WebSocket I/O (readLoop, writeLoop) which also acquires wc.mu.
//
// Without the fix, close() holds mu across cmd.Wait(), so if a
// concurrent goroutine tries to acquire mu (e.g. to write a WebSocket
// message), it deadlocks until the process terminates.
func TestWebSocketConsoleDeadlock(t *testing.T) {
	// Create a minimal WebSocket server/client pair
	serverReady := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		serverReady <- conn
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	defer clientConn.Close()
	sc := <-serverReady

	// Start a long-running process (sleep) so cmd.Wait() blocks.
	cmd := exec.Command("sleep", "30")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep: %v", err)
	}
	defer func() {
		if cmd.Process == nil {
			return
		}
		// Errors ignored on purpose, and both calls kept: the test's own path
		// waits on this process, so by here it has usually gone already and
		// Kill answers "process already finished". When it has not, Wait is
		// what reaps it.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	wc := &WebSocketConsole{
		conn:     sc,
		jailName: "testjail",
		cmd:      cmd,
		pty:      nil,
		mu:       sync.Mutex{},
		closed:   false,
		logger:   slog.Default(),
	}

	// Simulate a concurrent goroutine that needs the lock to check closed
	// state and write a WebSocket message — this is what readLoop does.
	var writerDone sync.WaitGroup
	writerDone.Add(1)
	go func() {
		defer writerDone.Done()
		// Simulate readLoop: acquire mu, check closed, write, release mu
		for i := 0; i < 10; i++ {
			wc.mu.Lock()
			closed := wc.closed
			if !closed {
				// Write a message while holding mu (same as readLoop)
				_ = wc.conn.WriteJSON(ConsoleMessage{Type: "output", Data: "ok"})
			}
			wc.mu.Unlock()
			if closed {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	// Let the writer get going
	time.Sleep(20 * time.Millisecond)

	// Call close() — this must NOT deadlock.
	// With the fix, close() releases mu before cmd.Wait(), so the
	// writer goroutine above can still acquire mu and see closed=true.
	closeDone := make(chan struct{})
	go func() {
		wc.close()
		close(closeDone)
	}()

	// If close() deadlocks, this timeout will fire
	select {
	case <-closeDone:
		// close() completed, writer should see closed=true and exit
		writerDone.Wait()
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock detected: close() blocked for 5+ seconds")
	}
}

// TestWebSocketConsoleDoubleClose is a secondary regression test that
// verifies close() is safe to call multiple times (idempotent close).
func TestWebSocketConsoleDoubleClose(t *testing.T) {
	serverReady := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		serverReady <- conn
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	defer clientConn.Close()
	sc := <-serverReady

	wc := &WebSocketConsole{
		conn:     sc,
		jailName: "test",
		mu:       sync.Mutex{},
		logger:   slog.Default(),
	}

	// Call close from two goroutines, neither should panic
	var wg sync.WaitGroup
	wg.Add(2)
	done := make(chan struct{})

	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			wc.close()
		}()
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// all good
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: concurrent close() calls blocked")
	}
}
