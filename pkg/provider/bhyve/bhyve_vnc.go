package bhyve

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hospitus/hospitus/pkg/provider"
)

// VNCInfo contains VNC connection information for a bhyve VM.
type VNCInfo struct {
	Enabled    bool   `json:"enabled"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Wait       bool   `json:"wait"`
	URI        string `json:"uri"`     // e.g., "vnc://127.0.0.1:5900"
	Running    bool   `json:"running"` // Whether VM is currently running
	InstanceID string `json:"instance_id"`
}

// GetVNCInfo returns VNC connection information for a bhyve VM.
// Returns an error if VNC is not enabled for this VM.
func (p *BhyveProvider) GetVNCInfo(ctx context.Context, handle provider.InstanceHandle) (*VNCInfo, error) {
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Load VM configuration
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load VM config: %w", err)
	}

	if !config.VNCEnabled {
		return nil, fmt.Errorf("VNC is not enabled for VM %s", vmName)
	}

	// Get defaults
	host := config.VNCHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := config.VNCPort
	if port == 0 {
		port = 5900
	}
	width := config.VNCWidth
	if width == 0 {
		width = 1024
	}
	height := config.VNCHeight
	if height == 0 {
		height = 768
	}

	// Check if VM is running
	state, err := p.GetInstanceState(ctx, handle)
	running := err == nil && state == provider.StateRunning

	return &VNCInfo{
		Enabled:    true,
		Host:       host,
		Port:       port,
		Width:      width,
		Height:     height,
		Wait:       config.VNCWait,
		URI:        fmt.Sprintf("vnc://%s:%d", host, port),
		Running:    running,
		InstanceID: vmName,
	}, nil
}

// EnableVNC enables VNC for a bhyve VM (requires VM restart).
func (p *BhyveProvider) EnableVNC(ctx context.Context, handle provider.InstanceHandle, port, width, height int, host string, wait bool) error {
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Load current config
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	// Update VNC settings
	config.VNCEnabled = true
	if port > 0 {
		config.VNCPort = port
	}
	if width > 0 {
		config.VNCWidth = width
	}
	if height > 0 {
		config.VNCHeight = height
	}
	if host != "" {
		// Validate the bind address: bhyve's fbuf has no VNC authentication, so a
		// non-loopback bind must be an explicit opt-in. EnableVNC offers no
		// insecure toggle, so enforce the strict (loopback-only) policy here.
		if err := validateVNCHost(host, false); err != nil {
			return fmt.Errorf("VNC security error: %w", err)
		}
		config.VNCHost = host
	}
	config.VNCWait = wait

	// Save updated config
	if err := p.saveVMConfig(vmDir, config); err != nil {
		return fmt.Errorf("failed to save VM config: %w", err)
	}

	return nil
}

// DisableVNC disables VNC for a bhyve VM (requires VM restart).
func (p *BhyveProvider) DisableVNC(ctx context.Context, handle provider.InstanceHandle) error {
	vmName := handle.ID
	vmDir := filepath.Join(p.dataDir, vmName)

	// Load current config
	config, err := p.loadVMConfig(vmDir)
	if err != nil {
		return fmt.Errorf("failed to load VM config: %w", err)
	}

	// Disable VNC
	config.VNCEnabled = false

	// Save updated config
	if err := p.saveVMConfig(vmDir, config); err != nil {
		return fmt.Errorf("failed to save VM config: %w", err)
	}

	return nil
}
