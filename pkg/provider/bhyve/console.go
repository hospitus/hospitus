package bhyve

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/hospitus/hospitus/pkg/provider"
)

// bhyve supports serial console access via null modem (nmdm) devices.
// Each VM gets a pair of nmdm devices: /dev/nmdmXA (VM side) and /dev/nmdmXB (host side).
// Users can connect to the host side using 'cu' or other terminal programs.

// Ensure BhyveProvider implements ConsoleProvider
var _ provider.ConsoleProvider = (*BhyveProvider)(nil)

// BhyveConsoleConnection represents a connection to a bhyve VM console.
//
// This wraps the nmdm device for read/write operations.
// For interactive use, clients should use the cu command directly.
type BhyveConsoleConnection struct {
	vmName     string
	nmdmDevice string
	file       *os.File
	mu         sync.Mutex
	closed     bool
}

// GetConsole returns a connection to the VM's serial console.
//
// bhyve uses null modem (nmdm) devices for serial console access.
// The VM is configured to use /dev/nmdmXA, and the host connects to /dev/nmdmXB.
//
// For interactive terminal access, it's recommended to use:
//
//	cu -l /dev/nmdm-<vmname>B
//
// This method provides programmatic access for automation.
func (p *BhyveProvider) GetConsole(ctx context.Context, handle provider.InstanceHandle) (provider.ConsoleConnection, error) {
	vmName := handle.ID

	// Check if VM exists
	vmDir := filepath.Join(p.dataDir, vmName)
	if _, err := os.Stat(vmDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("VM %s does not exist", vmName)
	}

	// Check if VM is running
	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM state: %w", err)
	}
	if state != provider.StateRunning {
		return nil, fmt.Errorf("VM %s is not running (state: %s)", vmName, state)
	}

	// Determine nmdm device path
	// Convention: /dev/nmdm-<vmname>B for host side
	nmdmDevice := fmt.Sprintf("/dev/nmdm-%sB", vmName)

	// Check if nmdm device exists
	if _, err := os.Stat(nmdmDevice); os.IsNotExist(err) {
		// Try alternative naming: /dev/nmdm<N>B where N is based on VM
		// For now, return error with hint
		return nil, fmt.Errorf("console device %s not found - VM may not have been started with console support", nmdmDevice)
	}

	// Open the device for read/write
	file, err := os.OpenFile(nmdmDevice, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open console device: %w", err)
	}

	return &BhyveConsoleConnection{
		vmName:     vmName,
		nmdmDevice: nmdmDevice,
		file:       file,
	}, nil
}

// ResizeConsole updates the terminal dimensions for a bhyve session.
func (p *BhyveProvider) ResizeConsole(ctx context.Context, handle provider.InstanceHandle, width, height int) error {
	// Serial consoles in bhyve (nmdm) don't have a concept of "resizing" the TTY
	// from the host side in a way that notifies the guest, but we satisfy the interface.
	return nil
}

// Read reads data from the console.
//
// The lock guards the closed flag, not the I/O. A read on an nmdm device blocks
// until the guest writes, so holding the mutex across it froze Write and Close
// meanwhile. *os.File is safe for concurrent use.
func (c *BhyveConsoleConnection) Read(p []byte) (n int, err error) {
	if c.isClosed() {
		return 0, io.EOF
	}
	return c.file.Read(p)
}

// Write writes data to the console.
func (c *BhyveConsoleConnection) Write(p []byte) (n int, err error) {
	if c.isClosed() {
		return 0, fmt.Errorf("console connection closed")
	}
	return c.file.Write(p)
}

// isClosed reports whether Close has run.
func (c *BhyveConsoleConnection) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Close closes the console connection.
func (c *BhyveConsoleConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	return c.file.Close()
}

// GetConsoleDevice returns the path to the console device for a VM.
// This can be used by CLI tools to launch 'cu' or other terminal programs.
func (p *BhyveProvider) GetConsoleDevice(ctx context.Context, handle provider.InstanceHandle) (string, error) {
	vmName := handle.ID

	// Check if VM exists
	vmDir := filepath.Join(p.dataDir, vmName)
	if _, err := os.Stat(vmDir); os.IsNotExist(err) {
		return "", fmt.Errorf("VM %s does not exist", vmName)
	}

	// Load VM config to get console device (loadVMConfig takes the VM directory).
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return "", fmt.Errorf("failed to load VM config: %w", err)
	}

	if config.Console == "" {
		return "", fmt.Errorf("VM %s has no console configured", vmName)
	}

	// Return the host-side device (B suffix)
	return fmt.Sprintf("/dev/nmdm-%sB", vmName), nil
}

// ExecConsole launches an interactive console session using 'cu'.
// This is a convenience method for CLI tools.
func (p *BhyveProvider) ExecConsole(ctx context.Context, handle provider.InstanceHandle) error {
	device, err := p.GetConsoleDevice(ctx, handle)
	if err != nil {
		return err
	}

	// Check if cu command exists
	cuPath, err := exec.LookPath("cu")
	if err != nil {
		return fmt.Errorf("'cu' command not found in PATH (cu(1) ships with the FreeBSD base system): %w", err)
	}

	// Launch cu with the console device
	// cu -l /dev/nmdm-<vmname>B
	cmd := exec.CommandContext(ctx, cuPath, "-l", device)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// ConsoleInfo contains information about a VM's console.
type ConsoleInfo struct {
	Device    string `json:"device"`
	Type      string `json:"type"` // "nmdm" or "socket"
	Available bool   `json:"available"`
	Command   string `json:"command"` // Suggested command to connect
}

// GetConsoleInfo returns information about the VM's console configuration.
func (p *BhyveProvider) GetConsoleInfo(ctx context.Context, handle provider.InstanceHandle) (*ConsoleInfo, error) {
	vmName := handle.ID

	device := fmt.Sprintf("/dev/nmdm-%sB", vmName)
	available := false

	if _, err := os.Stat(device); err == nil {
		available = true
	}

	return &ConsoleInfo{
		Device:    device,
		Type:      "nmdm",
		Available: available,
		Command:   fmt.Sprintf("cu -l %s", device),
	}, nil
}
