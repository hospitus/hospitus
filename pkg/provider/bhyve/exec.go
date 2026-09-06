package bhyve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Compile-time assertion: BhyveProvider implements ExecProvider
var _ provider.ExecProvider = (*BhyveProvider)(nil)

// ExecCommand executes a command inside a running bhyve VM via SSH.
//
// Requires:
//   - VM must be running with a reachable IP address
//   - SSH must be running inside the VM
//   - An SSH key must be available (typically injected via cloud-init)
//
// The SSH key is searched in:
//  1. VM directory: <dataDir>/<vmName>/ssh_key
//  2. Global hospitus key: <stateDir>/ssh/hospitus_key
func (p *BhyveProvider) ExecCommand(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions) (*provider.ExecResult, error) {
	vmName := handle.ID

	if err := validation.ValidateExecOptions(opts); err != nil {
		return nil, fmt.Errorf("invalid exec options: %w", err)
	}

	// Honor the per-call timeout (seconds).
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(opts.Timeout)*time.Second)
		defer cancel()
	}

	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM state: %w", err)
	}
	if state != provider.StateRunning {
		return nil, fmt.Errorf("VM %s is not running", vmName)
	}

	// Get VM IP address
	ip, err := p.getVMIP(handle)
	if err != nil {
		return nil, fmt.Errorf("failed to determine VM IP: %w", err)
	}

	sshKey, err := p.findSSHKey(vmName)
	if err != nil {
		return nil, fmt.Errorf("no SSH key found for VM %s: %w", vmName, err)
	}

	// Build SSH command
	user := opts.User
	if user == "" {
		user = "root"
	}

	remoteCmd, err := buildRemoteCommand(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to build remote command: %w", err)
	}

	sshArgs := []string{
		"-i", sshKey,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		fmt.Sprintf("%s@%s", user, ip),
		remoteCmd,
	}

	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	result := &provider.ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("SSH exec failed: %w", err)
		}
	}

	return result, nil
}

// ExecInteractive executes an interactive SSH session to the VM.
func (p *BhyveProvider) ExecInteractive(ctx context.Context, handle provider.InstanceHandle, opts provider.ExecOptions) error {
	vmName := handle.ID

	state, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("failed to get VM state: %w", err)
	}
	if state != provider.StateRunning {
		return fmt.Errorf("VM %s is not running", vmName)
	}

	ip, err := p.getVMIP(handle)
	if err != nil {
		return fmt.Errorf("failed to determine VM IP: %w", err)
	}

	sshKey, err := p.findSSHKey(vmName)
	if err != nil {
		return fmt.Errorf("no SSH key found for VM %s: %w", vmName, err)
	}

	user := opts.User
	if user == "" {
		user = "root"
	}

	sshArgs := []string{
		"-i", sshKey,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		"-t", // Force pseudo-terminal for interactive use
		fmt.Sprintf("%s@%s", user, ip),
	}

	if opts.Command != "" {
		if err := validation.ValidateExecOptions(opts); err != nil {
			return fmt.Errorf("invalid exec options: %w", err)
		}
		remoteCmd, err := buildRemoteCommand(opts)
		if err != nil {
			return fmt.Errorf("failed to build remote command: %w", err)
		}
		sshArgs = append(sshArgs, remoteCmd)
	}

	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// buildRemoteCommand assembles the command string executed by the remote shell
// over SSH. Every component (env values, working directory, command and each
// argument) is POSIX-quoted so that shell metacharacters are treated as literal
// data rather than interpreted by the guest shell.
func buildRemoteCommand(opts provider.ExecOptions) (string, error) {
	var b strings.Builder

	if opts.WorkingDir != "" {
		wd, err := shellQuote(opts.WorkingDir)
		if err != nil {
			return "", fmt.Errorf("working dir: %w", err)
		}
		b.WriteString("cd ")
		b.WriteString(wd)
		b.WriteString(" && ")
	}

	// Environment assignments, sorted for a deterministic command string.
	if len(opts.Env) > 0 {
		keys := make([]string, 0, len(opts.Env))
		for k := range opts.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val, err := shellQuote(opts.Env[k])
			if err != nil {
				return "", fmt.Errorf("env %s: %w", k, err)
			}
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(val)
			b.WriteString(" ")
		}
	}

	cmd, err := shellQuote(opts.Command)
	if err != nil {
		return "", fmt.Errorf("command: %w", err)
	}
	b.WriteString(cmd)
	for _, arg := range opts.Args {
		quoted, err := shellQuote(arg)
		if err != nil {
			return "", fmt.Errorf("argument: %w", err)
		}
		b.WriteString(" ")
		b.WriteString(quoted)
	}

	return b.String(), nil
}

// shellQuote quotes a string for safe use in a POSIX shell command. It rejects
// null bytes and non-tab control characters, which can truncate or manipulate
// shell arguments.
func shellQuote(s string) (string, error) {
	if strings.IndexByte(s, 0) >= 0 {
		return "", fmt.Errorf("string contains null byte")
	}
	for _, c := range s {
		if c < 32 && c != '\t' {
			return "", fmt.Errorf("string contains control character (code: %d)", c)
		}
	}
	// Always single-quote and escape embedded single quotes: this is safe for
	// every input, including the empty string ('').
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'", nil
}

// getVMIP extracts the VM IP from handle metadata or by querying the ARP table.
func (p *BhyveProvider) getVMIP(handle provider.InstanceHandle) (string, error) {
	// Check handle metadata first
	if ip, ok := handle.Metadata["ip"].(string); ok && ip != "" {
		return ip, nil
	}

	// Find the IP via the ARP table, correlated to the guest NIC's MAC. The tap
	// host MAC is NOT the guest MAC, so the guest MAC is read from the running
	// bhyve command line. Without a strict MAC match we return no IP rather than
	// an arbitrary ARP entry that could belong to another VM.
	guestMAC := p.guestMACForVM(handle.ID)
	if guestMAC != "" {
		arpOut, err := exec.Command("arp", "-an").Output()
		if err == nil {
			if ip := ipForMACInARP(string(arpOut), guestMAC); ip != "" {
				return ip, nil
			}
		}
	}

	return "", fmt.Errorf("no IP address found — VM may need DHCP or manual IP configuration")
}

// guestMACForVM returns the guest NIC MAC of the VM's first tap, read from the
// running bhyve process command line. Returns "" if the VM is not running or
// the NIC has no explicit MAC.
func (p *BhyveProvider) guestMACForVM(vmName string) string {
	config, err := p.loadVMConfig(filepath.Join(p.dataDir, vmName))
	if err != nil || len(config.TapDevs) == 0 {
		return ""
	}
	// What hospitus recorded, when it recorded one. Reading it back from the
	// process works only for a VM started before bhyve rewrote its own title,
	// which is to say almost never.
	if len(config.NICMACs) > 0 && config.NICMACs[0] != "" {
		return config.NICMACs[0]
	}
	pids := bhyvePIDsForVM(context.Background(), vmName)
	if len(pids) == 0 {
		return ""
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pids[0]), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return guestMACForTap(string(out), config.TapDevs[0])
}

// guestMACForTap extracts the mac= value from the bhyve virtio-net/e1000 device
// spec that includes tapDev.
func guestMACForTap(command, tapDev string) string {
	for _, tok := range strings.Fields(command) {
		if !strings.Contains(tok, "virtio-net") && !strings.Contains(tok, "e1000") {
			continue
		}
		parts := strings.Split(tok, ",")
		hasTap := false
		mac := ""
		for _, part := range parts {
			if part == tapDev {
				hasTap = true
			}
			if strings.HasPrefix(part, "mac=") {
				mac = strings.TrimPrefix(part, "mac=")
			}
		}
		if hasTap {
			return mac
		}
	}
	return ""
}

// ipForMACInARP returns the IPv4 address bound to mac in `arp -an` output, or ""
// if the MAC is absent.
func ipForMACInARP(arpOutput, mac string) string {
	mac = strings.ToLower(mac)
	for _, line := range strings.Split(arpOutput, "\n") {
		if strings.Contains(line, "incomplete") {
			continue
		}
		// Format: "? (10.10.0.2) at aa:bb:cc:dd:ee:ff on bridge0 ..."
		fields := strings.Fields(line)
		if len(fields) >= 4 && strings.EqualFold(fields[3], mac) {
			ip := strings.Trim(fields[1], "()")
			if ip != "" && !strings.HasPrefix(ip, "127.") {
				return ip
			}
		}
	}
	return ""
}

// findSSHKey searches for an SSH private key for the VM.
//
// Only Hospitus-managed keys are considered: the per-VM key and the global hospitus
// key. The daemon user's personal keys (~/.ssh/id_*) are deliberately NOT used —
// falling back to them would let any VM exec borrow the operator's identity and
// reach hosts far outside the VM's blast radius.
func (p *BhyveProvider) findSSHKey(vmName string) (string, error) {
	candidates := []string{
		filepath.Join(p.dataDir, vmName, "ssh_key"),
		filepath.Join(p.stateDir, "ssh", "hospitus_key"),
	}

	for _, key := range candidates {
		if key == "" {
			continue
		}
		if info, err := os.Stat(key); err == nil && !info.IsDir() {
			return key, nil
		}
	}

	return "", fmt.Errorf("no SSH key found; tried: %s", strings.Join(candidates, ", "))
}
