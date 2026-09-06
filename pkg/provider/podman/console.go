package podman

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Console access uses "podman exec -i" to provide an interactive shell inside
// the container, similar to the jail console pattern which uses jexec. No -t:
// the caller (the websocket console) is not a terminal, and the shell's stderr
// is routed into the same pipe as its stdout so a single Read serves both.

// Ensure PodmanProvider implements ConsoleProvider
var _ provider.ConsoleProvider = (*PodmanProvider)(nil)

// PodmanConsoleConnection represents an interactive console connection to a container.
type PodmanConsoleConnection struct {
	containerID string
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser // carries the shell's stderr too
	cancel      context.CancelFunc
	mu          sync.Mutex
	closed      bool
}

// GetConsole returns a connection to the container's console.
//
// This creates an interactive shell session inside the container using
// podman exec -it. The returned connection provides Read/Write access
// to the shell.
func (p *PodmanProvider) GetConsole(ctx context.Context, handle provider.InstanceHandle) (provider.ConsoleConnection, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	containerID := handle.ID

	// Check if container is running
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("failed to check container state: %w", err)
	}
	if state != provider.StateRunning {
		return nil, fmt.Errorf("container %s is not running", containerID)
	}

	// The session outlives this call, so it gets a context of its own: ctx is
	// request-scoped, and its cancellation once GetConsole returned killed the
	// shell out from under a console the caller was still using. Close cancels
	// it.
	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := exec.CommandContext(sessionCtx, p.podmanBin, "exec", "-i", containerID, "/bin/sh")

	// Set up pipes for stdin/stdout/stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		stdin.Close()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// The shell's stderr goes to the same pipe as its stdout. It used to have a
	// pipe of its own that nothing ever read: the session froze once the shell
	// had written a pipe buffer of stderr (~64KB), and until then the user saw
	// no error output at all.
	cmd.Stderr = cmd.Stdout

	// Start the process
	if err := cmd.Start(); err != nil {
		cancel()
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("failed to start podman exec: %w", err)
	}

	return &PodmanConsoleConnection{
		containerID: containerID,
		cmd:         cmd,
		stdin:       stdin,
		stdout:      stdout,
		cancel:      cancel,
	}, nil
}

// Read reads data from the console (the shell's stdout and stderr).
func (c *PodmanConsoleConnection) Read(p []byte) (n int, err error) {
	// Only the closed check and field access are guarded; the blocking read
	// itself must NOT hold the mutex, otherwise Close (which needs the mutex)
	// deadlocks against a Read that is waiting for data that never arrives.
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, io.EOF
	}
	stdout := c.stdout
	c.mu.Unlock()

	return stdout.Read(p)
}

// Write writes data to the console (stdin).
func (c *PodmanConsoleConnection) Write(p []byte) (n int, err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, fmt.Errorf("console connection closed")
	}
	stdin := c.stdin
	c.mu.Unlock()

	return stdin.Write(p)
}

// Close closes the console connection and terminates the shell.
func (c *PodmanConsoleConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true

	// The session context goes with the connection: without this the detached
	// context above would leave a shell running after Close, which is the leak
	// the request-scoped context used to prevent by accident.
	defer c.cancel()

	// Close stdin to signal EOF to the shell
	c.stdin.Close()
	c.stdout.Close()

	// Wait for the process to exit (with timeout)
	done := make(chan error, 1)
	go func() {
		done <- c.cmd.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-time.After(consoleCloseTimeout):
		// Force kill if it doesn't exit gracefully, then wait for the reaping
		// goroutine to observe the exit so we don't leave a zombie / leak the FDs.
		if err := c.cmd.Process.Kill(); err != nil {
			logging.WithProvider("podman").Debug("failed to kill console process (may already be gone)",
				logging.FieldError, err)
		}
		<-done
		return nil
	}
}

// consoleCloseTimeout bounds how long Close waits for the session process to
// exit gracefully before force-killing it.
const consoleCloseTimeout = 5 * time.Second

// ResizeConsole updates the terminal dimensions for a container session.
//
// It does nothing: podman has no resize command, and this session runs the
// shell without a TTY ("podman exec -i"), so there is no terminal to resize.
// The method exists to satisfy ConsoleProvider; the dimensions are validated so
// a caller sending nonsense hears about it.
func (p *PodmanProvider) ResizeConsole(ctx context.Context, handle provider.InstanceHandle, width, height int) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid terminal dimensions: %dx%d", width, height)
	}
	return nil
}
