package qemu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
)

// Compile-time assertion: QEMUProvider implements ExecProvider
var _ provider.ExecProvider = (*QEMUProvider)(nil)

// ExecCommand executes a command inside a running QEMU VM via the guest agent.
//
// Requires qemu-guest-agent to be installed and running inside the VM.
// Uses the QMP guest-exec and guest-exec-status commands.
func (p *QEMUProvider) ExecCommand(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions) (*provider.ExecResult, error) {
	vmName := handle.ID

	running, err := p.isVMRunning(ctx, vmName)
	if err != nil {
		return nil, fmt.Errorf("failed to check VM state: %w", err)
	}
	if !running {
		return nil, fmt.Errorf("VM %s is not running", vmName)
	}

	// The QEMU guest agent's guest-exec supports neither a working directory nor
	// running as a specific user. Rather than silently ignoring these options,
	// reject them so the caller is not misled about what actually ran.
	if opts.WorkingDir != "" {
		return nil, fmt.Errorf("guest-exec does not support setting a working directory")
	}
	if opts.User != "" {
		return nil, fmt.Errorf("guest-exec does not support running as a specific user")
	}

	qmpSocket := filepath.Join(p.dataDir, vmName, "qmp.sock")
	client, err := NewQMPClient(qmpSocket)
	if err != nil {
		return nil, fmt.Errorf("failed to create QMP client: %w", err)
	}
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to QMP: %w", err)
	}
	defer client.Close()

	// Build guest-exec arguments
	args := map[string]interface{}{
		"path":           opts.Command,
		"capture-output": true,
	}
	if len(opts.Args) > 0 {
		args["arg"] = opts.Args
	}
	if len(opts.Env) > 0 {
		envList := make([]string, 0, len(opts.Env))
		for k, v := range opts.Env {
			envList = append(envList, fmt.Sprintf("%s=%s", k, v))
		}
		args["env"] = envList
	}

	// Execute the command via guest agent
	result, err := client.Execute("guest-exec", args)
	if err != nil {
		return nil, fmt.Errorf("guest-exec failed (is qemu-guest-agent running?): %w", err)
	}

	var execResp struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(result, &execResp); err != nil {
		return nil, fmt.Errorf("failed to parse guest-exec response: %w", err)
	}

	// Poll for command completion
	timeout := 30 * time.Second
	if opts.Timeout > 0 {
		timeout = time.Duration(opts.Timeout) * time.Second
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		statusResult, err := client.Execute("guest-exec-status", map[string]interface{}{
			"pid": execResp.PID,
		})
		if err != nil {
			return nil, fmt.Errorf("guest-exec-status failed: %w", err)
		}

		var status struct {
			Exited   bool   `json:"exited"`
			ExitCode int    `json:"exitcode"`
			OutData  string `json:"out-data"`
			ErrData  string `json:"err-data"`
		}
		if err := json.Unmarshal(statusResult, &status); err != nil {
			return nil, fmt.Errorf("failed to parse exec status: %w", err)
		}

		if status.Exited {
			execResult := &provider.ExecResult{
				ExitCode: status.ExitCode,
			}
			// Guest agent returns base64-encoded output. Surface a decode failure
			// instead of silently dropping the command's output.
			if status.OutData != "" {
				decoded, err := base64.StdEncoding.DecodeString(status.OutData)
				if err != nil {
					return nil, fmt.Errorf("failed to decode guest-exec stdout: %w", err)
				}
				execResult.Stdout = string(decoded)
			}
			if status.ErrData != "" {
				decoded, err := base64.StdEncoding.DecodeString(status.ErrData)
				if err != nil {
					return nil, fmt.Errorf("failed to decode guest-exec stderr: %w", err)
				}
				execResult.Stderr = string(decoded)
			}
			return execResult, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}

	return nil, fmt.Errorf("command timed out after %s", timeout)
}

// ExecInteractive executes an interactive command inside a VM.
//
// For QEMU VMs, this connects to the serial console for interactive access.
// True interactive exec via guest-agent is limited — for full shell access,
// use the console command instead.
func (p *QEMUProvider) ExecInteractive(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions) error {
	vmName := handle.ID

	running, err := p.isVMRunning(ctx, vmName)
	if err != nil {
		return fmt.Errorf("failed to check VM state: %w", err)
	}
	if !running {
		return fmt.Errorf("VM %s is not running", vmName)
	}

	// ExecInteractive always attaches to the serial console; it cannot run a
	// specific command. Reject a caller-supplied command instead of silently
	// ignoring it — use ExecCommand for running a specific program via guest-agent.
	if opts.Command != "" {
		return fmt.Errorf("interactive exec attaches to the serial console and cannot run a specific command %q; use ExecCommand instead", opts.Command)
	}

	// For interactive access, connect to the serial console socket via socat
	serialSocket := filepath.Join(p.dataDir, vmName, "serial.sock")
	if _, err := os.Stat(serialSocket); os.IsNotExist(err) {
		return fmt.Errorf("serial console socket not found — VM may not support interactive exec")
	}

	socatPath, err := exec.LookPath("socat")
	if err != nil {
		return fmt.Errorf("'socat' not found — install it for interactive VM console access")
	}

	cmd := exec.CommandContext(ctx, socatPath,
		"-,raw,echo=0",
		fmt.Sprintf("UNIX-CONNECT:%s", serialSocket))
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
