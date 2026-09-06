package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/gorilla/websocket"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/jail"
)

// This enables web-based access to jail consoles without requiring SSH.
// The WebSocket console uses xterm.js-compatible protocol for terminal emulation.

// WebSocket upgrader with reasonable buffer sizes
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Validate Origin against Host to prevent CSRF attacks.
	// CLI/curl requests without an Origin header are allowed.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// No Origin header: non-browser client (CLI, curl) — allow
			return true
		}
		// Strip scheme from origin for comparison
		host := r.Host
		return origin == "http://"+host || origin == "https://"+host
	},
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
	logger   *slog.Logger
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

	jailProv, ok := prov.(*jail.JailProvider)
	if !ok {
		// The provider does not offer this, which is not a server fault: 500
		// told the caller to retry something that will never work.
		s.writeError(w, http.StatusNotImplemented, "Invalid jail provider")
		return
	}

	// Check if jail is running
	state, err := jailProv.GetInstanceState(ctx, instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to get jail state", err)
		return
	}
	if state != provider.StateRunning {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Jail is not running (state: %s)", state))
		return
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Warn("WebSocket upgrade failed", logging.FieldError, err)
		return
	}
	// Limit incoming message size to 64 KiB (PTY input/resize messages are tiny)
	conn.SetReadLimit(64 * 1024)

	// Create console session
	console := &WebSocketConsole{
		conn:     conn,
		jailName: instance.Handle.ID,
		logger:   s.logger.With(logging.FieldInstance, instance.Name),
	}

	// Track this console session
	sessionBytes := make([]byte, 8)
	rand.Read(sessionBytes) //nolint:errcheck // crypto/rand.Read never returns an error on supported platforms
	sessionID := hex.EncodeToString(sessionBytes)
	session := &ConsoleSession{
		ID:        sessionID,
		JailName:  instance.Handle.ID,
		StartedAt: time.Now(),
		RemoteIP:  r.RemoteAddr,
	}
	s.activeConsoleSessions.Store(sessionID, session)
	defer s.activeConsoleSessions.Delete(sessionID)

	if err := console.start(ctx); err != nil {
		console.logger.Warn("Failed to start WebSocket console", logging.FieldError, err)
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

	// Read from WebSocket and write to PTY
	wc.writeLoop()
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

			var err error
			wc.mu.Lock()
			closed := wc.closed
			if !closed {
				err = wc.conn.WriteJSON(msg)
			}
			wc.mu.Unlock()

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
			// Respond with pong
			wc.mu.Lock()
			if !wc.closed {
				_ = wc.conn.WriteJSON(ConsoleMessage{Type: "pong"})
			}
			wc.mu.Unlock()
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

	// Close WebSocket (safe without lock — closed flag is already set)
	_ = wc.conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
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

// clampToUint16 converts a signed dimension into the uint16 range expected by
// TIOCSWINSZ, guarding against integer overflow.
func clampToUint16(v int) uint16 {
	if v <= 0 {
		return 0
	}
	if v >= math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(v)
}

// setPtySize sets the window size of a PTY
func setPtySize(f *os.File, cols, rows int) error {
	ws := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{
		Row: clampToUint16(rows),
		Col: clampToUint16(cols),
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return errno
	}
	return nil
}

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
