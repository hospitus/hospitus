package qemu

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Compile-time assertion: QEMUProvider implements ConsoleProvider
var _ provider.ConsoleProvider = (*QEMUProvider)(nil)

// QEMUConsoleConnection wraps a Unix socket connection to the QEMU serial console.
type QEMUConsoleConnection struct {
	vmName string
	conn   net.Conn
	mu     sync.Mutex
	closed bool
}

// GetConsole returns a connection to the VM's serial console.
//
// QEMU serial console is exposed via a Unix socket at <vmDir>/serial.sock.
// The VM must be running for the socket to exist.
func (p *QEMUProvider) GetConsole(ctx context.Context, handle provider.InstanceHandle) (provider.ConsoleConnection, error) {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	vmName := handle.ID

	// Check VM is running
	running, err := p.isVMRunning(ctx, vmName)
	if err != nil {
		return nil, fmt.Errorf("failed to check VM state: %w", err)
	}
	if !running {
		return nil, fmt.Errorf("VM %s is not running", vmName)
	}

	// Connect to the serial socket
	serialSocket := filepath.Join(p.dataDir, vmName, "serial.sock")
	conn, err := net.DialTimeout("unix", serialSocket, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to serial console: %w", err)
	}

	return &QEMUConsoleConnection{
		vmName: vmName,
		conn:   conn,
	}, nil
}

// ResizeConsole updates terminal dimensions.
// QEMU serial consoles don't support resize signaling from the host side.
func (p *QEMUProvider) ResizeConsole(ctx context.Context, handle provider.InstanceHandle, width, height int) error {
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
	return nil
}

// Read reads data from the serial console.
func (c *QEMUConsoleConnection) Read(p []byte) (n int, err error) {
	// Only the closed check and field access are guarded; the blocking read
	// itself must NOT hold the mutex, otherwise Close (which needs the mutex)
	// deadlocks against a Read waiting for a byte a quiet guest never sends.
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, io.EOF
	}
	conn := c.conn
	c.mu.Unlock()

	return conn.Read(p)
}

// Write writes data to the serial console.
func (c *QEMUConsoleConnection) Write(p []byte) (n int, err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, fmt.Errorf("console connection closed")
	}
	conn := c.conn
	c.mu.Unlock()

	return conn.Write(p)
}

// Close closes the console connection.
func (c *QEMUConsoleConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	return c.conn.Close()
}
