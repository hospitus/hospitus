package jail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Jails support console access via jexec, which executes commands directly
// inside the jail's environment. This is different from VMs which use
// serial consoles or VNC.

// Ensure JailProvider implements ConsoleProvider, ExecProvider and ExecStreamingProvider
var (
	_ provider.ConsoleProvider       = (*JailProvider)(nil)
	_ provider.ExecProvider          = (*JailProvider)(nil)
	_ provider.ExecStreamingProvider = (*JailProvider)(nil)
)

// JailConsoleConnection represents an interactive console connection to a jail.
//
// Unlike VM consoles, jail consoles work via jexec which spawns a shell
// inside the jail. This implementation provides a wrapper for programmatic
// access to the jail shell.
type JailConsoleConnection struct {
	jailName string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   io.ReadCloser
	mu       sync.Mutex
	closed   bool
}

// GetConsole returns a connection to the jail's console (shell).
//
// This creates an interactive shell session inside the jail using jexec.
// For most use cases, ExecInteractive or the CLI console command are
// more appropriate.
//
// The returned connection provides Read/Write access to the shell.
func (p *JailProvider) GetConsole(ctx context.Context, handle provider.InstanceHandle) (provider.ConsoleConnection, error) {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return nil, fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return nil, fmt.Errorf("jail %s is not running", jailName)
	}

	// Get jexec path
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return nil, fmt.Errorf("jexec command not found: %w", err)
	}

	// Create the jexec command
	cmd := exec.CommandContext(ctx, jexecPath, jailName, "/bin/sh")

	// Set up pipes for stdin/stdout/stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return nil, fmt.Errorf("failed to start jexec: %w", err)
	}

	return &JailConsoleConnection{
		jailName: jailName,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderr,
	}, nil
}

// Read reads data from the console (stdout).
func (c *JailConsoleConnection) Read(p []byte) (n int, err error) {
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
func (c *JailConsoleConnection) Write(p []byte) (n int, err error) {
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
func (c *JailConsoleConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true

	// Close stdin to signal EOF to the shell
	c.stdin.Close()
	c.stdout.Close()
	c.stderr.Close()

	// Wait for the process to exit (with timeout)
	done := make(chan error, 1)
	go func() {
		done <- c.cmd.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-time.After(5 * time.Second):
		// Force kill if it doesn't exit gracefully
		_ = c.cmd.Process.Kill()
		return nil
	}
}

// ExecCommand executes a command inside a jail and returns the result.
//
// This is the non-interactive version that captures stdout/stderr.
// For interactive commands, use ExecInteractive instead.
func (p *JailProvider) ExecCommand(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions) (*provider.ExecResult, error) {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return nil, fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return nil, fmt.Errorf("jail %s is not running", jailName)
	}

	if err := validation.ValidateExecOptions(opts); err != nil {
		return nil, fmt.Errorf("invalid exec options: %w", err)
	}

	// Get jexec path
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return nil, fmt.Errorf("jexec command not found: %w", err)
	}

	// Build jexec command arguments using shared helper
	args, err := buildJexecArgs(jailName, opts, false)
	if err != nil {
		return nil, err
	}

	// Create context with timeout if specified
	execCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(opts.Timeout)*time.Second)
		defer cancel()
	}

	// Create the command
	cmd := exec.CommandContext(execCtx, jexecPath, args...)

	// Apply environment variables
	if err := applyExecEnv(cmd, opts); err != nil {
		return nil, err
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run the command
	err = cmd.Run()

	result := &provider.ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	// Get exit code
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			result.ExitCode = exitError.ExitCode()
		} else {
			// Command failed to run (not found, permission denied, etc.)
			return result, fmt.Errorf("command execution failed: %w", err)
		}
	} else {
		result.ExitCode = 0
	}

	return result, nil
}

// ExecInteractive executes an interactive command inside a jail.
//
// This attaches stdin/stdout/stderr for interactive use and is suitable
// for running shells or interactive programs.
func (p *JailProvider) ExecInteractive(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions) error {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return fmt.Errorf("jail %s is not running", jailName)
	}

	if err := validation.ValidateExecOptions(opts); err != nil {
		return fmt.Errorf("invalid exec options: %w", err)
	}

	// Get jexec path
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return fmt.Errorf("jexec command not found: %w", err)
	}

	// Build jexec command arguments using shared helper (interactive mode)
	args, err := buildJexecArgs(jailName, opts, true)
	if err != nil {
		return err
	}

	// Create the command
	cmd := exec.CommandContext(ctx, jexecPath, args...)

	// Apply environment variables
	if err := applyExecEnv(cmd, opts); err != nil {
		return err
	}

	// Attach stdin/stdout/stderr for interactive use
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Run the command
	return cmd.Run()
}

// ConsoleInfo contains information about a jail's console.
type ConsoleInfo struct {
	JailName  string `json:"jail_name"`
	JailID    int    `json:"jail_id"`
	Available bool   `json:"available"`
	Command   string `json:"command"` // Suggested command to connect
}

// GetConsoleInfo returns information about the jail's console configuration.
func (p *JailProvider) GetConsoleInfo(ctx context.Context, handle provider.InstanceHandle) (*ConsoleInfo, error) {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return nil, fmt.Errorf("failed to check jail state: %w", err)
	}

	// Get jail ID
	jailID := 0
	if running {
		jailID, _ = p.getJailID(ctx, jailName)
	}

	return &ConsoleInfo{
		JailName:  jailName,
		JailID:    jailID,
		Available: running,
		Command:   fmt.Sprintf("jexec %s /bin/sh", jailName),
	}, nil
}

// ExecConsole launches an interactive console session.
// This is a convenience method that's equivalent to:
//
//	ExecInteractive(ctx, handle, ExecOptions{Command: "/bin/sh"})
func (p *JailProvider) ExecConsole(ctx context.Context, handle provider.InstanceHandle, shell string) error {
	if shell == "" {
		shell = "/bin/sh"
	}

	return p.ExecInteractive(ctx, handle, provider.ExecOptions{
		Command: shell,
	})
}

// ExecCommandStream executes a command inside a jail and streams output to the provided writers.
// This allows real-time output display for long-running commands like pkg install.
func (p *JailProvider) ExecCommandStream(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions, stdout, stderr io.Writer) (int, error) {
	jailName := handle.ID

	// Check if jail is running
	running, err := p.isJailRunning(ctx, jailName)
	if err != nil {
		return -1, fmt.Errorf("failed to check jail state: %w", err)
	}
	if !running {
		return -1, fmt.Errorf("jail %s is not running", jailName)
	}

	if err := validation.ValidateExecOptions(opts); err != nil {
		return -1, fmt.Errorf("invalid exec options: %w", err)
	}

	// Get jexec path
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return -1, fmt.Errorf("jexec command not found: %w", err)
	}

	// Build jexec command arguments using shared helper
	args, err := buildJexecArgs(jailName, opts, false)
	if err != nil {
		return -1, err
	}

	// Create context with timeout if specified
	execCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(opts.Timeout)*time.Second)
		defer cancel()
	}

	// Create the command
	cmd := exec.CommandContext(execCtx, jexecPath, args...)

	// Apply environment variables
	if err := applyExecEnv(cmd, opts); err != nil {
		return -1, err
	}

	// Stream output directly to provided writers
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Run the command
	err = cmd.Run()

	// Get exit code
	exitCode := 0
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		} else {
			// Command failed to run (not found, permission denied, etc.)
			return -1, fmt.Errorf("command execution failed: %w", err)
		}
	}

	return exitCode, nil
}

// shellQuote quotes a string for safe use in shell commands.
//
// Returns an error for strings containing null bytes or non-tab control
// characters, which can truncate or manipulate shell arguments.
//
// Every string is quoted, including ones that look harmless. Deciding this
// from a list of special characters means the list has to be complete, and the
// one here was not: it named ' " \ $ ` ! * ? and whitespace, but not ; & | < >
// ( ). An argument of "foo;id" came back as "foo;id", and the working-directory
// branch of buildJexecArgs puts it in a string that /bin/sh -c then runs.
func shellQuote(s string) (string, error) {
	// SECURITY: Reject null bytes completely - they can truncate shell arguments
	if strings.IndexByte(s, 0) >= 0 {
		return "", fmt.Errorf("string contains null byte")
	}

	// SECURITY: Reject control characters (except tab) that could affect shell behavior
	for _, c := range s {
		if c < 32 && c != '\t' {
			return "", fmt.Errorf("string contains control character (code: %d)", c)
		}
	}

	// Single quotes suspend every shell meaning; the only character they
	// cannot hold is a single quote, which ends the run and is re-opened.
	// This is correct for any input, the empty string included.
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'", nil
}

// buildJexecArgs constructs the argument list for a jexec command.
//
// Parameters:
//   - jailName: the name of the target jail
//   - opts: execution options (user, command, args, working directory)
//   - interactive: if true, defaults to /bin/sh when no command is specified
//
// Returns the argument slice ready to be passed to exec.CommandContext.
func buildJexecArgs(jailName string, opts provider.ExecOptions, interactive bool) ([]string, error) {
	args := []string{}

	// Add user flag if specified (-U looks up user in jail's passwd)
	if opts.User != "" {
		if err := validation.ValidateUsername(opts.User); err != nil {
			return nil, fmt.Errorf("invalid user: %w", err)
		}
		args = append(args, "-U", opts.User)
	}

	// Add jail name
	args = append(args, jailName)

	// Add command and its arguments
	switch {
	case opts.WorkingDir != "":
		// Validate working directory. It is absolute inside the jail, whose
		// root confines it, and it is shell-quoted into the "cd" below.
		if err := validation.ValidateFilePath(opts.WorkingDir, true); err != nil {
			return nil, fmt.Errorf("invalid working directory: %w", err)
		}

		// Build command string with working directory wrapper
		var cmdStr string
		if opts.Command == "" && interactive {
			cmdStr = "/bin/sh"
		} else {
			// Apply the same command validation as the non-WorkingDir branch,
			// otherwise the "cd <dir> && <cmd>" wrapper would run an unvalidated
			// command string.
			if err := validation.ValidateExecCommand(opts.Command); err != nil {
				return nil, fmt.Errorf("invalid command: %w", err)
			}
			cmdStr = opts.Command
			for _, arg := range opts.Args {
				if err := validation.ValidateExecArgument(arg); err != nil {
					return nil, fmt.Errorf("invalid command argument: %w", err)
				}
				quoted, err := shellQuote(arg)
				if err != nil {
					return nil, fmt.Errorf("cannot quote argument: %w", err)
				}
				cmdStr += " " + quoted
			}
		}
		wdQuoted, err := shellQuote(opts.WorkingDir)
		if err != nil {
			return nil, fmt.Errorf("cannot quote working directory: %w", err)
		}
		args = append(args, "/bin/sh", "-c", fmt.Sprintf("cd %s && %s", wdQuoted, cmdStr))
	case opts.Command == "":
		// Default to /bin/sh if no command specified (only for interactive mode)
		if interactive {
			args = append(args, "/bin/sh")
		} else {
			return nil, fmt.Errorf("command is required for non-interactive execution")
		}
	default:
		if err := validation.ValidateExecCommand(opts.Command); err != nil {
			return nil, fmt.Errorf("invalid command: %w", err)
		}

		// Detect shell-mode: /bin/sh -c <shellcode>
		// The shell-code argument (args[1] when args[0]=="-c") is intentionally
		// allowed to contain shell metacharacters; only reject null bytes / non-UTF-8.
		shellMode := validation.IsShellInterpreter(opts.Command) &&
			len(opts.Args) >= 1 && opts.Args[0] == "-c"

		args = append(args, opts.Command)
		for i, arg := range opts.Args {
			if shellMode && i == 1 {
				if !utf8.ValidString(arg) {
					return nil, fmt.Errorf("invalid command argument: shell command must be valid UTF-8")
				}
				if strings.ContainsAny(arg, "\x00\r") {
					return nil, fmt.Errorf("invalid command argument: shell command contains invalid control character")
				}
				args = append(args, arg)
				continue
			}
			if err := validation.ValidateExecArgument(arg); err != nil {
				return nil, fmt.Errorf("invalid command argument: %w", err)
			}
			args = append(args, arg)
		}
	}

	return args, nil
}

// applyExecEnv validates and applies environment variables to an exec.Cmd.
func applyExecEnv(cmd *exec.Cmd, opts provider.ExecOptions) error {
	if len(opts.Env) == 0 {
		return nil
	}

	// Start from a minimal environment rather than the daemon's, so secrets
	// in hospitusd's process environment (API keys, credentials) are never
	// exposed to processes executed inside the jail.
	env := provider.MinimalEnv()
	for k, v := range opts.Env {
		if err := validation.ValidateEnvVar(k, v); err != nil {
			return fmt.Errorf("invalid environment variable: %w", err)
		}
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env
	return nil
}
