package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
)

// This enables web-based access to jail consoles without requiring SSH.
// The WebSocket console uses xterm.js-compatible protocol for terminal emulation.

// consoleUpgrader builds the WebSocket upgrader for this server.
//
// Per-server rather than package-level, so CheckOrigin can consult the
// configured cross-origin policy. It used to compare the Origin against the
// request Host alone, which refused a browser UI served from an origin the
// rest of the API accepts — and accepted one the policy does not.
func (s *Server) consoleUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		// Validate Origin to prevent CSRF. A request with no Origin header is
		// not a browser (CLI, curl) and is allowed.
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			// Same origin as the request itself is always fine, whatever the
			// configured policy says: that is not a cross-origin request.
			if origin == "http://"+r.Host || origin == "https://"+r.Host {
				return true
			}
			allowed, _ := s.originAllowed(origin)
			return allowed
		},
	}
}

// ConsoleMessage represents a message in the WebSocket console protocol
type ConsoleMessage struct {
	Type string `json:"type"` // "input", "output", "resize", "ping", "pong"
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// WebSocketConsole manages a WebSocket connection to a jail console
type WebSocketConsole struct {
	conn     *websocket.Conn
	jailName string
	cmd      *exec.Cmd
	pty      *os.File
	mu       sync.Mutex
	closed   bool
	// done is closed alongside the closed flag, so a goroutine waiting on a
	// long ticker learns about the close immediately instead of on its next
	// tick.
	done   chan struct{}
	logger *slog.Logger
}

// handleWebSocketConsole handles WebSocket console connections.
//
// Route: GET /api/v1/instances/{id}/console/ws
//
// Terminal sizing is done with resize protocol messages, not query parameters.
//
// Protocol:
//   - Input: {"type": "input", "data": "command\n"}
//   - Output: {"type": "output", "data": "response"}
//   - Resize: {"type": "resize", "cols": 120, "rows": 40}
//   - Ping: {"type": "ping"} -> {"type": "pong"}
func (s *Server) handleWebSocketConsole(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

	// Get instance
	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	// WebSocket console only works with jail provider
	if instance.Provider != "jail" {
		s.writeError(w, http.StatusBadRequest, "WebSocket console is only supported for jails")
		return
	}

	// Get jail provider
	prov, err := s.registry.Get("jail")
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Jail provider not available")
		return
	}

	// No assertion to *jail.JailProvider: the only method used here is
	// GetInstanceState, which every Provider has. Demanding the concrete type
	// answered 501 to a valid wrapper registered as "jail" before the upgrade.
	//
	// Check if jail is running
	state, err := prov.GetInstanceState(ctx, instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to get jail state", err)
		return
	}
	if state != provider.StateRunning {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Jail is not running (state: %s)", state))
		return
	}

	// Upgrade to WebSocket
	upgrader := s.consoleUpgrader()
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Warn("WebSocket upgrade failed", logging.FieldError, err)
		return
	}
	// Limit incoming message size to 64 KiB (PTY input/resize messages are tiny)
	conn.SetReadLimit(64 * 1024)

	// The same fallback handleInstanceVolumes applies: an empty Handle.ID ran
	// jexec with no jail argument, and the session died straight after a
	// successful upgrade.
	jailName := instance.Handle.ID
	if jailName == "" {
		jailName = instance.Name
	}

	// Create console session
	console := &WebSocketConsole{
		conn:     conn,
		jailName: jailName,
		done:     make(chan struct{}),
		logger:   s.logger.With(logging.FieldInstance, instance.Name),
	}

	// Track this console session
	sessionBytes := make([]byte, 8)
	rand.Read(sessionBytes) //nolint:errcheck // crypto/rand.Read never returns an error on supported platforms
	sessionID := hex.EncodeToString(sessionBytes)
	session := &ConsoleSession{
		ID:        sessionID,
		JailName:  jailName,
		StartedAt: time.Now(),
		RemoteIP:  r.RemoteAddr,
	}
	// Capped: each accepted connection starts a jexec shell and holds a PTY,
	// and nothing here limited how many an authenticated caller could open. A
	// script that reconnects on every error filled the host with shells.
	//
	// The slot is claimed with one atomic add, not counted and then taken: two
	// upgrades arriving together both read a count below the limit and both
	// stored, so the cap bounded sequential reconnects and nothing else.
	if open := s.consoleSessions.Add(1); open > maxConsoleSessions {
		s.consoleSessions.Add(-1)
		console.logger.Warn("Refusing console session: too many are open",
			"open", open-1, "limit", maxConsoleSessions)
		// net/http clears the connection deadline at the upgrade, so a client
		// that stops reading blocks this write and holds the goroutine.
		_ = conn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout))
		_ = conn.WriteJSON(ConsoleMessage{
			Type: "error",
			Data: fmt.Sprintf("too many console sessions are open (limit %d)", maxConsoleSessions),
		})
		conn.Close()
		return
	}
	defer s.consoleSessions.Add(-1)

	s.activeConsoleSessions.Store(sessionID, session)
	defer s.activeConsoleSessions.Delete(sessionID)

	if err := console.start(ctx); err != nil {
		console.logger.Warn("Failed to start WebSocket console", logging.FieldError, err)
		_ = conn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout))
		_ = conn.WriteJSON(ConsoleMessage{
			Type: "error",
			Data: fmt.Sprintf("Failed to start console: %v", err),
		})
		conn.Close()
		return
	}

	// Handle console I/O
	console.run()
}

// start initializes the jail console with a PTY
func (wc *WebSocketConsole) start(ctx context.Context) error {
	// Create a pseudo-terminal
	pty, tty, err := openPty()
	if err != nil {
		return fmt.Errorf("failed to create PTY: %w", err)
	}

	// Get jexec path
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		pty.Close()
		tty.Close()
		return fmt.Errorf("jexec not found: %w", err)
	}

	// Create jexec command with the TTY
	wc.cmd = exec.CommandContext(ctx, jexecPath, wc.jailName, "/bin/sh", "-l")
	wc.cmd.Stdin = tty
	wc.cmd.Stdout = tty
	wc.cmd.Stderr = tty
	wc.cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	}

	// Start the command
	if err := wc.cmd.Start(); err != nil {
		pty.Close()
		tty.Close()
		return fmt.Errorf("failed to start jexec: %w", err)
	}

	// Close TTY - we use the PTY master
	tty.Close()
	wc.pty = pty

	return nil
}

// run handles the WebSocket console I/O loop
func (wc *WebSocketConsole) run() {
	defer wc.close()

	// Read from PTY and send to WebSocket
	go wc.readLoop()

	// Keep a watching client alive. The protocol's own "ping" message refreshes
	// the read deadline, but only a client that sends one — someone watching a
	// build scroll past types nothing for minutes. A protocol-level ping is
	// answered by the WebSocket library on the other end, browser or not, and
	// its pong refreshes the deadline through the handler below.
	wc.conn.SetPongHandler(func(string) error {
		return wc.conn.SetReadDeadline(time.Now().Add(consoleIdleTimeout))
	})
	go wc.pingLoop()

	// Read from WebSocket and write to PTY
	wc.writeLoop()
}

// pingLoop sends a WebSocket ping until the console closes.
func (wc *WebSocketConsole) pingLoop() {
	ticker := time.NewTicker(consoleIdleTimeout / 3)
	defer ticker.Stop()

	for {
		select {
		case <-wc.done:
			// Woken by close() rather than by the next tick, which is minutes
			// away: this goroutine held its console that long after the
			// session ended.
			return
		case <-ticker.C:
		}

		wc.mu.Lock()
		closed := wc.closed
		var err error
		if !closed {
			err = wc.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(consoleWriteTimeout))
		}
		wc.mu.Unlock()

		if closed || err != nil {
			return
		}
	}
}

// writeJSON sends one message under the write lock, with a deadline.
//
// Every write goes through here. gorilla/websocket blocks in WriteJSON until
// the peer reads and panics if two goroutines write at once, so a client that
// stays connected and stops reading used to strand whichever goroutine was
// writing — with wc.mu held, which blocked close() on its first Lock.
//
// It reports whether the console was already closed, and the write error.
func (wc *WebSocketConsole) writeJSON(msg ConsoleMessage) (closed bool, err error) {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	if wc.closed {
		return true, nil
	}
	if err := wc.conn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout)); err != nil {
		return false, err
	}
	return false, wc.conn.WriteJSON(msg)
}

// readLoop reads from the PTY and sends to WebSocket
func (wc *WebSocketConsole) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := wc.pty.Read(buf)
		if err != nil {
			if err != io.EOF {
				wc.logger.Warn("PTY read error", logging.FieldError, err)
			}
			wc.close()
			return
		}

		if n > 0 {
			msg := ConsoleMessage{
				Type: "output",
				Data: string(buf[:n]),
			}

			closed, err := wc.writeJSON(msg)
			if closed {
				return
			}
			if err != nil {
				wc.logger.Warn("WebSocket write error", logging.FieldError, err)
				wc.close()
				return
			}
		}
	}
}

// writeLoop reads from WebSocket and writes to PTY
func (wc *WebSocketConsole) writeLoop() {
	defer wc.close()
	for {
		// Refreshed before every read. net/http clears the connection deadline
		// at the upgrade, so the server's ReadTimeout and IdleTimeout stop
		// applying here: without this, ReadJSON blocks until the peer says
		// something, and an idle client held a jexec shell, a PTY and one of
		// the maxConsoleSessions slots for as long as it stayed connected.
		//
		// The client's own "ping" message refreshes it like any other, so a
		// session that is idle but alive stays open.
		if err := wc.conn.SetReadDeadline(time.Now().Add(consoleIdleTimeout)); err != nil {
			wc.logger.Warn("Cannot set the console read deadline", logging.FieldError, err)
			return
		}

		var msg ConsoleMessage
		if err := wc.conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				wc.logger.Warn("WebSocket read error", logging.FieldError, err)
			}
			return
		}

		switch msg.Type {
		case "input":
			// Write input to PTY
			if _, err := wc.pty.WriteString(msg.Data); err != nil {
				wc.logger.Warn("PTY write error", logging.FieldError, err)
				return
			}

		case "resize":
			// Resize the PTY
			if msg.Cols > 0 && msg.Rows > 0 {
				if err := setPtySize(wc.pty, msg.Cols, msg.Rows); err != nil {
					wc.logger.Warn("Failed to resize PTY", logging.FieldError, err, "cols", msg.Cols, "rows", msg.Rows)
				}
			}

		case "ping":
			// Respond with pong. A failure here is the write deadline expiring
			// on a peer that pings and never reads: ignoring it kept the PTY,
			// the shell and one of the global session slots for a connection
			// the server can no longer write to.
			closed, err := wc.writeJSON(ConsoleMessage{Type: "pong"})
			if closed || err != nil {
				return
			}
		}
	}
}

// close cleans up the console session.
// The mutex is released during cmd.Wait() to avoid blocking concurrent
// WebSocket I/O (reads/writes/pings also acquire wc.mu).
func (wc *WebSocketConsole) close() {
	wc.mu.Lock()
	if wc.closed {
		wc.mu.Unlock()
		return
	}
	wc.closed = true
	// Guarded: a WebSocketConsole built as a literal — several tests do —
	// carries a nil channel, and closing one panics.
	if wc.done != nil {
		close(wc.done)
	}

	// Close PTY
	if wc.pty != nil {
		wc.pty.Close()
	}

	// Kill the process (under lock to prevent concurrent access to wc.cmd)
	if wc.cmd != nil && wc.cmd.Process != nil {
		_ = wc.cmd.Process.Kill()
	}
	savedCmd := wc.cmd
	wc.mu.Unlock()

	// Wait for the process outside the lock
	if savedCmd != nil {
		_ = savedCmd.Wait()
	}

	// Close WebSocket. Under the same lock every other write takes: the closed
	// flag stops later writers, but one already inside WriteJSON is not stopped
	// by it, and gorilla/websocket panics on concurrent writes.
	wc.mu.Lock()
	_ = wc.conn.SetWriteDeadline(time.Now().Add(consoleWriteTimeout))
	_ = wc.conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	wc.mu.Unlock()
	// Give the client a brief moment to receive the close frame before the
	// underlying TCP connection is torn down, so browsers show a clean close
	// rather than an abnormal-closure error.
	time.Sleep(100 * time.Millisecond)
	wc.conn.Close()
}

// openPty creates a new pseudo-terminal pair
func openPty() (pty, tty *os.File, err error) {
	// Open /dev/ptmx to get a master PTY
	pty, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open /dev/ptmx: %w", err)
	}

	// Get the slave device name
	slaveName, err := ptsname(pty)
	if err != nil {
		pty.Close()
		return nil, nil, fmt.Errorf("failed to get slave name: %w", err)
	}

	// Unlock the slave
	if err := unlockpt(pty); err != nil {
		pty.Close()
		return nil, nil, fmt.Errorf("failed to unlock PTY: %w", err)
	}

	tty, err = os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		pty.Close()
		return nil, nil, fmt.Errorf("failed to open slave: %w", err)
	}

	return pty, tty, nil
}

// ptsname and unlockpt are platform-specific (they use FreeBSD-only ioctls) and
// live in websocket_console_freebsd.go / websocket_console_other.go so the
// daemon still compiles on macOS and Linux, where PTY consoles are unsupported.

// ConsoleSession represents an active console session for tracking
type ConsoleSession struct {
	ID        string    `json:"id"`
	JailName  string    `json:"jail_name"`
	StartedAt time.Time `json:"started_at"`
	RemoteIP  string    `json:"remote_ip"`
}

// handleListConsoleSessions lists active console sessions
func (s *Server) handleListConsoleSessions(w http.ResponseWriter, r *http.Request) {
	sessions := []ConsoleSession{}
	s.activeConsoleSessions.Range(func(k, v any) bool {
		sessions = append(sessions, *v.(*ConsoleSession))
		return true
	})
	s.writeJSON(w, http.StatusOK, sessions)
}

// maxConsoleSessions bounds how many interactive consoles the daemon holds at
// once. Each one is a shell process and a PTY on the host.
const maxConsoleSessions = 32

// consoleWriteTimeout bounds a single write to the client. Output is produced
// by the guest and cannot be paused, so a peer that stops reading has to be
// disconnected rather than waited for.
const consoleWriteTimeout = 10 * time.Second

// consoleIdleTimeout is how long a console may go without the client sending
// anything — a keystroke, a resize, or the protocol's own "ping" — before the
// server closes it and releases its slot.
const consoleIdleTimeout = 15 * time.Minute
