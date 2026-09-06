package bhyve

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// Default VNC viewers to try (in order of preference)
var vncViewers = []string{
	"vncviewer",      // TigerVNC, RealVNC, etc.
	"xvncviewer",     // X11 VNC viewer
	"vinagre",        // GNOME VNC viewer
	"remote-viewer",  // Spice/VNC viewer
	"krdc",           // KDE VNC client
	"remmina",        // Remmina remote desktop
	"gtk-vnc-viewer", // GTK VNC viewer
}

func newVNCCommand() *cobra.Command {
	var (
		showInfo   bool
		viewerPath string
		enable     bool
		disable    bool
		port       int
		width      int
		height     int
		host       string
		wait       bool
	)

	cmd := &cobra.Command{
		Use:     "vnc <name>",
		Aliases: []string{"graphics", "display"},
		Short:   "Access VNC console of a bhyve VM",
		Long: `Access the VNC graphical console of a bhyve VM.

This command launches a VNC viewer to connect to the VM's framebuffer.
VNC must be enabled for the VM (enabled during creation or with --enable).

bhyve VMs with VNC support expose a framebuffer device that can be accessed
via any VNC client. The VM must be running for the connection to succeed.

Supported VNC clients (auto-detected in order):
  - vncviewer (TigerVNC, RealVNC)
  - xvncviewer, vinagre, remote-viewer, krdc, remmina

Examples:
  # Connect to VM's VNC console
  hospitus bhyve vnc myvm

  # Show VNC connection info without connecting
  hospitus bhyve vnc myvm --info

  # Use a specific VNC viewer
  hospitus bhyve vnc myvm --viewer /usr/local/bin/vncviewer

  # Enable VNC for an existing VM (requires restart)
  hospitus bhyve vnc myvm --enable --port 5901

  # Disable VNC for a VM (requires restart)
  hospitus bhyve vnc myvm --disable`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVNC(cmd, args, showInfo, viewerPath, enable, disable, port, width, height, host, wait)
		},
	}

	cmd.Flags().BoolVar(&showInfo, "info", false, "Show VNC connection info instead of connecting")
	cmd.Flags().StringVar(&viewerPath, "viewer", "", "Path to VNC viewer executable")
	cmd.Flags().BoolVar(&enable, "enable", false, "Enable VNC for this VM (requires restart)")
	cmd.Flags().BoolVar(&disable, "disable", false, "Disable VNC for this VM (requires restart)")
	// Both at once is a contradiction cobra can refuse for us; without this
	// --enable simply won, silently.
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	cmd.Flags().IntVar(&port, "port", 5900, "VNC port (default: 5900)")
	cmd.Flags().IntVar(&width, "width", 1024, "Framebuffer width")
	cmd.Flags().IntVar(&height, "height", 768, "Framebuffer height")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "VNC bind address")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for VNC connection before booting")

	return cmd
}

func runVNC(cmd *cobra.Command, args []string, showInfo bool, viewerPath string, enable, disable bool, port, width, height int, host string, wait bool) error {
	vmName := args[0]

	// VNC config is read straight off the local data dir — this command does
	// not go through the API — so it only ever describes a VM on this host.
	// HOSPITUS_DATA_DIR follows the daemon's own override; the provider keeps its
	// VMs in the bhyve/ subdirectory.
	dataDir := "/var/lib/hospitus"
	if dir := os.Getenv("HOSPITUS_DATA_DIR"); dir != "" {
		dataDir = dir
	}
	vmDir := filepath.Join(dataDir, "bhyve", vmName)
	configPath := filepath.Join(vmDir, "vm.conf")

	// Check if VM exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return fmt.Errorf("VM %s not found on this host (config file %s does not exist)", vmName, configPath)
	}

	// The port/width/height/host/wait flags only take effect when enabling
	// VNC. Reject them otherwise so they are not silently ignored.
	if !enable {
		for _, name := range []string{"port", "width", "height", "host", "wait"} {
			if cmd.Flags().Changed(name) {
				return fmt.Errorf("--%s only applies with --enable", name)
			}
		}
	}

	// Handle enable/disable
	if enable {
		return enableVNC(cmd.OutOrStdout(), vmDir, port, width, height, host, wait)
	}
	if disable {
		return disableVNC(cmd.OutOrStdout(), vmDir)
	}

	// Read VNC config
	vncInfo, err := readVNCConfig(vmDir)
	if err != nil {
		return err
	}

	if !vncInfo.enabled {
		return fmt.Errorf("VNC is not enabled for VM %s\n\nEnable VNC with: hospitus bhyve vnc %s --enable", vmName, vmName)
	}

	// Show info mode
	if showInfo {
		printVNCConnectInfo(cmd, vmName, vncInfo)
		return nil
	}

	// Check for an X display before trying to launch a GUI viewer.
	// doas/sudo typically strips DISPLAY/XAUTHORITY, so detect this early
	// and fall back to printing connection info rather than giving a
	// cryptic "Can't open display" error.
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		printVNCConnectInfo(cmd, vmName, vncInfo)
		fmt.Fprintln(cmd.OutOrStdout(), "Note: no graphical display available (running under doas/sudo without X forwarding).")
		fmt.Fprintln(cmd.OutOrStdout(), "Connect manually using one of the options above.")
		return nil
	}

	// Find VNC viewer
	viewer := viewerPath
	if viewer == "" {
		viewer = findVNCViewer()
		if viewer == "" {
			printVNCConnectInfo(cmd, vmName, vncInfo)
			return fmt.Errorf("no VNC viewer found — install one with: pkg install tigervnc-viewer")
		}
	}

	// Verify viewer exists
	if _, err := exec.LookPath(viewer); err != nil {
		return fmt.Errorf("VNC viewer %q not found: %w", viewer, err)
	}

	// Build connection string
	connectionStr := fmt.Sprintf("%s::%d", vncInfo.host, vncInfo.port)

	fmt.Fprintf(cmd.OutOrStdout(), "Connecting to %s VNC console at %s:%d...\n", vmName, vncInfo.host, vncInfo.port)
	fmt.Fprintf(cmd.OutOrStdout(), "Using viewer: %s\n", viewer)
	fmt.Fprintln(cmd.OutOrStdout())

	// Launch VNC viewer, propagating the caller's X environment explicitly.
	vncCmd := exec.Command(viewer, connectionStr)
	vncCmd.Stdin = os.Stdin
	vncCmd.Stdout = os.Stdout
	vncCmd.Stderr = os.Stderr
	vncCmd.Env = os.Environ()

	if err := vncCmd.Run(); err != nil {
		// Viewer launched but failed (e.g. auth error) — print helpful fallback.
		fmt.Fprintln(cmd.OutOrStdout())
		printVNCConnectInfo(cmd, vmName, vncInfo)
		return fmt.Errorf("VNC viewer exited with error: %w", err)
	}
	return nil
}

type vncConfig struct {
	enabled bool
	host    string
	port    int
	width   int
	height  int
	wait    bool
}

func readVNCConfig(vmDir string) (*vncConfig, error) {
	configPath := filepath.Join(vmDir, "vm.conf")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	config := &vncConfig{
		host:   "127.0.0.1",
		port:   5900,
		width:  1024,
		height: 768,
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]
		value := parts[1]

		switch key {
		case "vnc_enabled":
			config.enabled = value == "true"
		case "vnc_port":
			if port, err := strconv.Atoi(value); err == nil {
				config.port = port
			}
		case "vnc_width":
			if width, err := strconv.Atoi(value); err == nil {
				config.width = width
			}
		case "vnc_height":
			if height, err := strconv.Atoi(value); err == nil {
				config.height = height
			}
		case "vnc_wait":
			config.wait = value == "true"
		case "vnc_host":
			if value != "" {
				config.host = value
			}
		}
	}

	return config, nil
}

func enableVNC(out io.Writer, vmDir string, port, width, height int, host string, wait bool) error {
	configPath := filepath.Join(vmDir, "vm.conf")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	newLines := []string{}

	// Update existing VNC settings or add new ones
	foundEnabled := false
	foundPort := false
	foundWidth := false
	foundHeight := false
	foundHost := false
	foundWait := false

	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := parts[0]
			switch key {
			case "vnc_enabled":
				newLines = append(newLines, "vnc_enabled=true")
				foundEnabled = true
				continue
			case "vnc_port":
				newLines = append(newLines, fmt.Sprintf("vnc_port=%d", port))
				foundPort = true
				continue
			case "vnc_width":
				newLines = append(newLines, fmt.Sprintf("vnc_width=%d", width))
				foundWidth = true
				continue
			case "vnc_height":
				newLines = append(newLines, fmt.Sprintf("vnc_height=%d", height))
				foundHeight = true
				continue
			case "vnc_host":
				newLines = append(newLines, fmt.Sprintf("vnc_host=%s", host))
				foundHost = true
				continue
			case "vnc_wait":
				newLines = append(newLines, fmt.Sprintf("vnc_wait=%t", wait))
				foundWait = true
				continue
			}
		}
		newLines = append(newLines, line)
	}

	// Add missing settings
	if !foundEnabled {
		newLines = append(newLines, "vnc_enabled=true")
	}
	if !foundPort {
		newLines = append(newLines, fmt.Sprintf("vnc_port=%d", port))
	}
	if !foundWidth {
		newLines = append(newLines, fmt.Sprintf("vnc_width=%d", width))
	}
	if !foundHeight {
		newLines = append(newLines, fmt.Sprintf("vnc_height=%d", height))
	}
	if !foundHost {
		newLines = append(newLines, fmt.Sprintf("vnc_host=%s", host))
	}
	if !foundWait {
		newLines = append(newLines, fmt.Sprintf("vnc_wait=%t", wait))
	}

	// Written through a temp file and renamed: an interrupted in-place write
	// leaves vm.conf truncated, and bhyve then starts the VM without the
	// settings the rest of this file just computed — or refuses to start it.
	if err := writeVMConfig(configPath, strings.Join(newLines, "\n")); err != nil {
		return err
	}

	fmt.Fprintf(out, "VNC enabled for VM (port %d, resolution %dx%d)\n", port, width, height)
	fmt.Fprintln(out, "Note: Restart the VM for changes to take effect")
	return nil
}

func disableVNC(out io.Writer, vmDir string) error {
	configPath := filepath.Join(vmDir, "vm.conf")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	newLines := []string{}

	for _, line := range lines {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && parts[0] == "vnc_enabled" {
			newLines = append(newLines, "vnc_enabled=false")
			continue
		}
		newLines = append(newLines, line)
	}

	// Written through a temp file and renamed: an interrupted in-place write
	// leaves vm.conf truncated, and bhyve then starts the VM without the
	// settings the rest of this file just computed — or refuses to start it.
	if err := writeVMConfig(configPath, strings.Join(newLines, "\n")); err != nil {
		return err
	}

	fmt.Fprintln(out, "VNC disabled for VM")
	fmt.Fprintln(out, "Note: Restart the VM for changes to take effect")
	return nil
}

func findVNCViewer() string {
	for _, viewer := range vncViewers {
		if path, err := exec.LookPath(viewer); err == nil {
			return path
		}
	}
	return ""
}

// printVNCConnectInfo prints structured VNC connection details and manual
// connection instructions to the command's output writer.
func printVNCConnectInfo(cmd *cobra.Command, vmName string, vncInfo *vncConfig) {
	fmt.Fprintf(cmd.OutOrStdout(), "VNC console for %s:\n", vmName)
	fmt.Fprintf(cmd.OutOrStdout(), "  Host:       %s\n", vncInfo.host)
	fmt.Fprintf(cmd.OutOrStdout(), "  Port:       %d\n", vncInfo.port)
	fmt.Fprintf(cmd.OutOrStdout(), "  Resolution: %dx%d\n", vncInfo.width, vncInfo.height)
	fmt.Fprintf(cmd.OutOrStdout(), "  URI:        vnc://%s:%d\n", vncInfo.host, vncInfo.port)
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintf(cmd.OutOrStdout(), "Connect with:\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  vncviewer %s:%d\n", vncInfo.host, vncInfo.port)
	if vncInfo.host == "127.0.0.1" || vncInfo.host == "localhost" {
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprintln(cmd.OutOrStdout(), "For remote access, forward the port via SSH:")
		fmt.Fprintf(cmd.OutOrStdout(), "  ssh -L %d:127.0.0.1:%d user@host\n", vncInfo.port, vncInfo.port)
		fmt.Fprintf(cmd.OutOrStdout(), "  vncviewer 127.0.0.1:%d\n", vncInfo.port)
	}
}

// writeVMConfig replaces a VM's configuration file atomically.
func writeVMConfig(path, contents string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vm-conf-*")
	if err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to secure config: %w", err)
	}
	if _, err := tmp.WriteString(contents); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to flush config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed to install config: %w", err)
	}
	return nil
}
