package jail

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
)

// Tmux sessions allow:
// - Persistent shell sessions that survive disconnects
// - Detach/reattach capability for long-running tasks
// - Multiple windows/panes within a session

// validTmuxSessionName matches valid tmux session names.
// Alphanumeric, dash, underscore, dot; 1-128 chars.
var validTmuxSessionName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// validateTmuxSessionName validates a tmux session name to prevent
// command injection and shell metacharacter attacks.
func validateTmuxSessionName(sessionName string) error {
	if sessionName == "" {
		return fmt.Errorf("tmux session name cannot be empty")
	}
	if !validTmuxSessionName.MatchString(sessionName) {
		return fmt.Errorf("invalid tmux session name: must be alphanumeric with dash, underscore, or dot")
	}
	return nil
}

// TmuxSession represents a tmux session inside a jail.
type TmuxSession struct {
	Name      string `json:"name"`
	Windows   int    `json:"windows"`
	Created   string `json:"created"`
	Attached  bool   `json:"attached"`
	SessionID string `json:"session_id"`
}

// TmuxProvider is an optional interface for providers that support tmux sessions.
type TmuxProvider interface {
	// TmuxHasSupport checks if tmux is installed in the instance.
	TmuxHasSupport(ctx context.Context, handle provider.InstanceHandle) (bool, error)

	// TmuxInstall installs tmux in the instance.
	TmuxInstall(ctx context.Context, handle provider.InstanceHandle) error

	// TmuxListSessions lists all tmux sessions in the instance.
	TmuxListSessions(ctx context.Context, handle provider.InstanceHandle) ([]TmuxSession, error)

	// TmuxNewSession creates a new tmux session.
	TmuxNewSession(ctx context.Context, handle provider.InstanceHandle, sessionName string) error

	// TmuxAttachSession attaches to an existing tmux session interactively.
	TmuxAttachSession(ctx context.Context, handle provider.InstanceHandle, sessionName string) error

	// TmuxKillSession kills a tmux session.
	TmuxKillSession(ctx context.Context, handle provider.InstanceHandle, sessionName string) error

	// TmuxSendKeys sends keys/commands to a tmux session.
	TmuxSendKeys(ctx context.Context, handle provider.InstanceHandle, sessionName, keys string) error
}

// Ensure JailProvider implements TmuxProvider
var _ TmuxProvider = (*JailProvider)(nil)

// TmuxHasSupport checks if tmux is installed in the jail.
func (p *JailProvider) TmuxHasSupport(ctx context.Context, handle provider.InstanceHandle) (bool, error) {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return false, fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return false, fmt.Errorf("jail %s is not running", jailName)
	}

	// Check if tmux is installed
	result, err := p.ExecCommand(ctx, handle, provider.ExecOptions{
		Command: "which",
		Args:    []string{"tmux"},
		Timeout: 10,
	})
	if err != nil {
		return false, nil // Command failed, tmux not found
	}

	return result.ExitCode == 0, nil
}

// TmuxInstall installs tmux in the jail using pkg.
func (p *JailProvider) TmuxInstall(ctx context.Context, handle provider.InstanceHandle) error {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return fmt.Errorf("jail %s is not running", jailName)
	}

	// Install tmux
	result, err := p.ExecCommand(ctx, handle, provider.ExecOptions{
		Command: "/bin/sh",
		Args:    []string{"-c", "env ASSUME_ALWAYS_YES=yes pkg install -y tmux"},
		Timeout: 300, // 5 minutes for package install
	})
	if err != nil {
		return fmt.Errorf("failed to install tmux: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("pkg install tmux failed: %s", result.Stderr)
	}

	return nil
}

// TmuxListSessions lists all tmux sessions in the jail.
func (p *JailProvider) TmuxListSessions(ctx context.Context, handle provider.InstanceHandle) ([]TmuxSession, error) {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return nil, fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return nil, fmt.Errorf("jail %s is not running", jailName)
	}

	// List tmux sessions with format: session_name:windows:created:attached:session_id
	result, err := p.ExecCommand(ctx, handle, provider.ExecOptions{
		Command: "tmux",
		Args:    []string{"list-sessions", "-F", "#{session_name}:#{session_windows}:#{session_created_string}:#{session_attached}:#{session_id}"},
		Timeout: 10,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list tmux sessions: %w", err)
	}

	// tmux returns exit code 1 if no sessions exist
	if result.ExitCode != 0 {
		if strings.Contains(result.Stderr, "no server running") || strings.Contains(result.Stderr, "no sessions") {
			return []TmuxSession{}, nil
		}
		return nil, fmt.Errorf("tmux list-sessions failed: %s", result.Stderr)
	}

	// Parse output
	var sessions []TmuxSession
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 5)
		if len(parts) < 5 {
			continue
		}

		windows := 0
		_, _ = fmt.Sscanf(parts[1], "%d", &windows)

		sessions = append(sessions, TmuxSession{
			Name:      parts[0],
			Windows:   windows,
			Created:   parts[2],
			Attached:  parts[3] == "1",
			SessionID: parts[4],
		})
	}

	return sessions, nil
}

// TmuxNewSession creates a new tmux session in the jail.
// If sessionName is empty, it uses the jail name as the session name.
func (p *JailProvider) TmuxNewSession(ctx context.Context, handle provider.InstanceHandle, sessionName string) error {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return fmt.Errorf("jail %s is not running", jailName)
	}

	// Use jail name as default session name
	if sessionName == "" {
		sessionName = jailName
	}

	if err := validateTmuxSessionName(sessionName); err != nil {
		return fmt.Errorf("invalid tmux session name: %w", err)
	}

	// Create detached tmux session
	result, err := p.ExecCommand(ctx, handle, provider.ExecOptions{
		Command: "tmux",
		Args:    []string{"new-session", "-d", "-s", sessionName},
		Timeout: 30,
	})
	if err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}
	if result.ExitCode != 0 {
		// Check if session already exists
		if strings.Contains(result.Stderr, "duplicate session") {
			return fmt.Errorf("tmux session '%s' already exists", sessionName)
		}
		return fmt.Errorf("tmux new-session failed: %s", result.Stderr)
	}

	return nil
}

// TmuxAttachSession attaches to an existing tmux session interactively.
// This uses jexec directly to provide proper TTY handling.
func (p *JailProvider) TmuxAttachSession(ctx context.Context, handle provider.InstanceHandle, sessionName string) error {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return fmt.Errorf("jail %s is not running", jailName)
	}

	// Use jail name as default session name
	if sessionName == "" {
		sessionName = jailName
	}

	if err := validateTmuxSessionName(sessionName); err != nil {
		return fmt.Errorf("invalid tmux session name: %w", err)
	}

	// Check if session exists, create if not
	sessions, err := p.TmuxListSessions(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to list sessions: %w", err)
	}

	sessionExists := false
	for _, s := range sessions {
		if s.Name == sessionName {
			sessionExists = true
			break
		}
	}

	if !sessionExists {
		// Create the session first
		if err := p.TmuxNewSession(ctx, handle, sessionName); err != nil {
			return fmt.Errorf("failed to create session: %w", err)
		}
	}

	// Get jexec path
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return fmt.Errorf("jexec command not found: %w", err)
	}

	// Run jexec with tmux attach interactively
	cmd := exec.CommandContext(ctx, jexecPath, jailName, "tmux", "attach", "-t", sessionName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// TmuxKillSession kills a tmux session in the jail.
func (p *JailProvider) TmuxKillSession(ctx context.Context, handle provider.InstanceHandle, sessionName string) error {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return fmt.Errorf("jail %s is not running", jailName)
	}

	if err := validateTmuxSessionName(sessionName); err != nil {
		return fmt.Errorf("invalid tmux session name: %w", err)
	}

	result, err := p.ExecCommand(ctx, handle, provider.ExecOptions{
		Command: "tmux",
		Args:    []string{"kill-session", "-t", sessionName},
		Timeout: 10,
	})
	if err != nil {
		return fmt.Errorf("failed to kill tmux session: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("tmux kill-session failed: %s", result.Stderr)
	}

	return nil
}

// TmuxSendKeys sends keys/commands to a tmux session.
// Use "Enter" at the end to execute the command.
func (p *JailProvider) TmuxSendKeys(ctx context.Context, handle provider.InstanceHandle, sessionName, keys string) error {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return fmt.Errorf("jail %s is not running", jailName)
	}

	if err := validateTmuxSessionName(sessionName); err != nil {
		return fmt.Errorf("invalid tmux session name: %w", err)
	}

	result, err := p.ExecCommand(ctx, handle, provider.ExecOptions{
		Command: "tmux",
		Args:    []string{"send-keys", "-t", sessionName, keys, "Enter"},
		Timeout: 10,
	})
	if err != nil {
		return fmt.Errorf("failed to send keys to tmux session: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("tmux send-keys failed: %s", result.Stderr)
	}

	return nil
}

// TmuxCapturePane captures the current content of a tmux pane.
// Useful for getting output from commands sent via TmuxSendKeys.
func (p *JailProvider) TmuxCapturePane(ctx context.Context, handle provider.InstanceHandle, sessionName string, lines int) (string, error) {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return "", fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return "", fmt.Errorf("jail %s is not running", jailName)
	}

	if err := validateTmuxSessionName(sessionName); err != nil {
		return "", fmt.Errorf("invalid tmux session name: %w", err)
	}

	// Default to 100 lines
	if lines <= 0 {
		lines = 100
	}

	// Capture pane content
	result, err := p.ExecCommand(ctx, handle, provider.ExecOptions{
		Command: "tmux",
		Args:    []string{"capture-pane", "-t", sessionName, "-p", "-S", fmt.Sprintf("-%d", lines)},
		Timeout: 10,
	})
	if err != nil {
		return "", fmt.Errorf("failed to capture tmux pane: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("tmux capture-pane failed: %s", result.Stderr)
	}

	return result.Stdout, nil
}

// TmuxGetOrCreateSession ensures a session exists and returns its name.
// If the session doesn't exist, it creates one.
func (p *JailProvider) TmuxGetOrCreateSession(ctx context.Context, handle provider.InstanceHandle, sessionName string) (string, error) {
	jailName := handle.ID

	// Use jail name as default session name
	if sessionName == "" {
		sessionName = jailName
	}

	if err := validateTmuxSessionName(sessionName); err != nil {
		return "", fmt.Errorf("invalid tmux session name: %w", err)
	}

	// Check if tmux is installed
	hasTmux, err := p.TmuxHasSupport(ctx, handle)
	if err != nil {
		return "", fmt.Errorf("failed to check tmux support: %w", err)
	}
	if !hasTmux {
		return "", fmt.Errorf("tmux is not installed in jail %s (install with: hospitus jail exec %s 'pkg install -y tmux')", jailName, jailName)
	}

	// List existing sessions
	sessions, err := p.TmuxListSessions(ctx, handle)
	if err != nil {
		return "", fmt.Errorf("failed to list tmux sessions: %w", err)
	}

	// Check if session already exists
	for _, s := range sessions {
		if s.Name == sessionName {
			return sessionName, nil
		}
	}

	// Create new session
	if err := p.TmuxNewSession(ctx, handle, sessionName); err != nil {
		return "", fmt.Errorf("failed to create tmux session: %w", err)
	}

	return sessionName, nil
}

// TmuxExecInSession executes a command in a tmux session and waits for output.
// This is useful for long-running commands that need to persist.
func (p *JailProvider) TmuxExecInSession(ctx context.Context, handle provider.InstanceHandle, sessionName, command string) error {
	// Ensure session exists
	sessionName, err := p.TmuxGetOrCreateSession(ctx, handle, sessionName)
	if err != nil {
		return err
	}

	return p.TmuxSendKeys(ctx, handle, sessionName, command)
}

// TmuxInfo returns information about tmux sessions for an instance.
type TmuxInfo struct {
	Available    bool          `json:"available"`     // Whether tmux is installed
	Sessions     []TmuxSession `json:"sessions"`      // List of active sessions
	SuggestedCmd string        `json:"suggested_cmd"` // Command to attach
}

// GetTmuxInfo returns tmux session information for the jail.
func (p *JailProvider) GetTmuxInfo(ctx context.Context, handle provider.InstanceHandle) (*TmuxInfo, error) {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return nil, fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return &TmuxInfo{
			Available:    false,
			SuggestedCmd: fmt.Sprintf("# Jail %s is not running", jailName),
		}, nil
	}

	// Check if tmux is installed
	hasTmux, err := p.TmuxHasSupport(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("failed to check tmux support: %w", err)
	}

	if !hasTmux {
		return &TmuxInfo{
			Available:    false,
			SuggestedCmd: fmt.Sprintf("hospitus jail exec %s 'pkg install -y tmux'", jailName),
		}, nil
	}

	// Get sessions
	sessions, err := p.TmuxListSessions(ctx, handle)
	if err != nil {
		// Return empty sessions if tmux command fails
		sessions = []TmuxSession{}
	}

	return &TmuxInfo{
		Available:    true,
		Sessions:     sessions,
		SuggestedCmd: fmt.Sprintf("hospitus jail tmux %s", jailName),
	}, nil
}

// TmuxExecCommandStream executes a command via tmux and streams output.
// This is the preferred method for long-running provisioning commands.
// The command runs in a tmux session, allowing reconnection if connection drops.
func (p *JailProvider) TmuxExecCommandStream(ctx context.Context, handle provider.InstanceHandle, sessionName, command string, output *bytes.Buffer) error {
	// Ensure session exists
	sessionName, err := p.TmuxGetOrCreateSession(ctx, handle, sessionName)
	if err != nil {
		return err
	}

	if err := p.TmuxSendKeys(ctx, handle, sessionName, command); err != nil {
		return err
	}

	// Optionally capture initial output
	if output != nil {
		content, err := p.TmuxCapturePane(ctx, handle, sessionName, 50)
		if err == nil {
			output.WriteString(content)
		}
	}

	return nil
}
