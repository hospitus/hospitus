package jail

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// RCTL allows fine-grained resource control for jails including:
//   - CPU limits (cputime, pcpu)
//   - Memory limits (memoryuse, vmemoryuse, memorylocked)
//   - Process limits (maxproc, nthr)
//   - I/O limits (readbps, writebps, readiops, writeiops)
//
// Note: RCTL requires kern.racct.enable=1 in /boot/loader.conf

// ResourceLimit represents a FreeBSD rctl resource limit rule.
type ResourceLimit struct {
	Resource string `json:"resource"` // e.g., "memoryuse", "cputime", "maxproc"
	Action   string `json:"action"`   // e.g., "deny", "log", "devctl"
	Amount   string `json:"amount"`   // e.g., "2G", "3600", "100"
}

// applyRCTLLimits applies resource limits to a jail using RCTL.
// This is called during jail startup to apply CPU and memory limits.
func (p *JailProvider) applyRCTLLimits(ctx context.Context, name string, resources provider.ResourceSpec) error {
	// The name is interpolated into an rctl rule, which is a colon-separated
	// grammar: a name carrying ":" or "=" would define a different rule than
	// the one intended.
	if err := validation.ValidateInstanceName(name); err != nil {
		return fmt.Errorf("invalid jail name %q: %w", name, err)
	}
	// MemoryMB * 1024 * 1024 overflows int64 above ~8.8e6 TB and would hand rctl
	// a negative byte count; CPUs * 100 has the same shape.
	const maxMemoryMB = 1 << 40 // 1 PiB expressed in MB, far past any real host
	if resources.MemoryMB < 0 || resources.MemoryMB > maxMemoryMB {
		return fmt.Errorf("memory limit %d MB is out of range (0..%d)", resources.MemoryMB, maxMemoryMB)
	}
	if resources.CPUs < 0 || resources.CPUs > 1024 {
		return fmt.Errorf("cpu limit %d is out of range (0..1024)", resources.CPUs)
	}

	// Check if RACCT/RCTL is enabled
	output, err := p.cmd().Output(ctx, "sysctl", "-n", "kern.racct.enable")
	if err != nil || strings.TrimSpace(string(output)) != "1" {
		return fmt.Errorf("RACCT/RCTL is not enabled; resource limits cannot be applied to jail %q — add kern.racct.enable=1 to /boot/loader.conf and reboot", name)
	}

	// rctl keeps every matching rule, so adding a looser one leaves the earlier,
	// stricter rule in force. ApplyResourceLimits already clears first; this
	// path — the one start and SetInstanceResources take — did not.
	if err := p.RemoveResourceLimits(ctx, provider.InstanceHandle{ID: name}); err != nil {
		p.logWarn(ctx, "failed to remove existing RCTL rules before applying new ones", "jail", name, logging.FieldError, err)
	}

	// CPU limit (percentage)
	if resources.CPUs > 0 {
		limit := fmt.Sprintf("jail:%s:pcpu:deny=%d", name, resources.CPUs*100)
		if err := p.cmd().Run(ctx, "rctl", "-a", limit); err != nil {
			return fmt.Errorf("failed to set CPU limit: %w", err)
		}
	}

	// Memory limit
	if resources.MemoryMB > 0 {
		bytes := resources.MemoryMB * 1024 * 1024
		limit := fmt.Sprintf("jail:%s:memoryuse:deny=%d", name, bytes)
		if err := p.cmd().Run(ctx, "rctl", "-a", limit); err != nil {
			return fmt.Errorf("failed to set memory limit: %w", err)
		}
	}

	// I/O limits — hard failures (user-configured limits must be applied)
	if resources.ReadBPS > 0 {
		limit := fmt.Sprintf("jail:%s:readbps:throttle=%d", name, resources.ReadBPS)
		if err := p.cmd().Run(ctx, "rctl", "-a", limit); err != nil {
			return fmt.Errorf("failed to set read BPS limit: %w", err)
		}
	}
	if resources.WriteBPS > 0 {
		limit := fmt.Sprintf("jail:%s:writebps:throttle=%d", name, resources.WriteBPS)
		if err := p.cmd().Run(ctx, "rctl", "-a", limit); err != nil {
			return fmt.Errorf("failed to set write BPS limit: %w", err)
		}
	}
	if resources.ReadIOPS > 0 {
		limit := fmt.Sprintf("jail:%s:readiops:throttle=%d", name, resources.ReadIOPS)
		if err := p.cmd().Run(ctx, "rctl", "-a", limit); err != nil {
			return fmt.Errorf("failed to set read IOPS limit: %w", err)
		}
	}
	if resources.WriteIOPS > 0 {
		limit := fmt.Sprintf("jail:%s:writeiops:throttle=%d", name, resources.WriteIOPS)
		if err := p.cmd().Run(ctx, "rctl", "-a", limit); err != nil {
			return fmt.Errorf("failed to set write IOPS limit: %w", err)
		}
	}

	// Process limit — hard failure
	processLimit := "1000"
	if resources.MaxProc > 0 {
		processLimit = strconv.Itoa(resources.MaxProc)
	}
	limit := fmt.Sprintf("jail:%s:maxproc:deny=%s", name, processLimit)
	if err := p.cmd().Run(ctx, "rctl", "-a", limit); err != nil {
		return fmt.Errorf("failed to set maxproc limit: %w", err)
	}

	return nil
}

// SetResourceLimits sets resource limits on a jail using FreeBSD rctl.
//
// FreeBSD rctl allows fine-grained resource control for jails.
// Common resources:
//   - memoryuse: Physical memory usage (e.g., "2G", "512M")
//   - vmemoryuse: Virtual memory usage
//   - cputime: CPU time in seconds
//   - maxproc: Maximum number of processes
//   - openfiles: Maximum number of open files
//   - coredumpsize: Maximum core dump size
//
// Actions:
//   - deny: Deny resource allocation when limit is reached
//   - log: Log when limit is reached
//   - devctl: Send devctl notification
//
// Example:
//
//	limits := []ResourceLimit{
//	    {Resource: "memoryuse", Action: "deny", Amount: "2G"},
//	    {Resource: "cputime", Action: "deny", Amount: "3600"},
//	}
//	err := p.SetResourceLimits(ctx, handle, limits)
func (p *JailProvider) SetResourceLimits(ctx context.Context, handle provider.InstanceHandle, limits []ResourceLimit) error {
	// SECURITY: Validate handle
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance handle: %w", err)
	}

	// Check if jail exists
	_, err := p.GetInstanceState(ctx, handle)
	if err != nil {
		return fmt.Errorf("jail not found: %w", err)
	}

	// Remove existing rules first
	if err := p.RemoveResourceLimits(ctx, handle); err != nil {
		// Log but don't fail if no rules exist
		p.logWarn(ctx, "failed to remove existing RCTL rules before applying new ones", "jail", handle.ID, "err", err)
	}

	// Add each limit rule
	for _, limit := range limits {
		// SECURITY: Validate resource name
		if !isValidRctlResource(limit.Resource) {
			return fmt.Errorf("invalid resource: %s", limit.Resource)
		}
		if !isValidRctlAction(limit.Action) {
			return fmt.Errorf("invalid action: %s", limit.Action)
		}

		// SECURITY: Validate amount to prevent rule injection.
		// RCTL amounts are numeric with optional suffix (G, M, K, %).
		// Reject newlines, null bytes, or shell metacharacters.
		if !utf8.ValidString(limit.Amount) {
			return fmt.Errorf("invalid amount: must be valid UTF-8")
		}
		if strings.ContainsAny(limit.Amount, "\x00\r\n;&|$`(){}[]<>\\\"'") {
			return fmt.Errorf("invalid amount: contains invalid characters")
		}
		if len(limit.Amount) > 20 {
			return fmt.Errorf("invalid amount: too long (max 20 characters)")
		}

		// Build rctl rule: jail:jailname:resource:action=amount
		rule := fmt.Sprintf("jail:%s:%s:%s=%s", handle.ID, limit.Resource, limit.Action, limit.Amount)

		// Add rule using rctl -a
		output, err := p.cmd().CombinedOutput(ctx, "rctl", "-a", rule)
		if err != nil {
			return fmt.Errorf("failed to add rctl rule %s: %w (output: %s)", rule, err, string(output))
		}
	}

	return nil
}

// GetResourceLimits retrieves current resource limits for a jail.
func (p *JailProvider) GetResourceLimits(ctx context.Context, handle provider.InstanceHandle) ([]ResourceLimit, error) {
	// SECURITY: Validate handle
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return nil, fmt.Errorf("invalid instance handle: %w", err)
	}

	// List rules for this jail using rctl -l
	output, err := p.cmd().CombinedOutput(ctx, "rctl", "-l", fmt.Sprintf("jail:%s", handle.ID))
	if err != nil {
		// If no rules exist, rctl returns an error
		if strings.Contains(string(output), "No rules") || strings.Contains(string(output), "not found") {
			return []ResourceLimit{}, nil
		}
		return nil, fmt.Errorf("failed to list rctl rules: %w (output: %s)", err, string(output))
	}

	// Parse output
	// Format: jail:jailname:resource:action=amount
	var limits []ResourceLimit
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse rule: jail:jailname:resource:action=amount
		parts := strings.Split(line, ":")
		if len(parts) < 4 {
			continue
		}

		// parts[0] = "jail"
		// parts[1] = jailname
		// parts[2] = resource
		// parts[3] = action=amount

		resource := parts[2]
		actionAmount := strings.SplitN(parts[3], "=", 2)
		if len(actionAmount) != 2 {
			continue
		}

		limits = append(limits, ResourceLimit{
			Resource: resource,
			Action:   actionAmount[0],
			Amount:   actionAmount[1],
		})
	}

	return limits, nil
}

// RemoveResourceLimits removes all resource limits from a jail.
func (p *JailProvider) RemoveResourceLimits(ctx context.Context, handle provider.InstanceHandle) error {
	// SECURITY: Validate handle
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance handle: %w", err)
	}

	// Remove all rules for this jail using rctl -r
	output, err := p.cmd().CombinedOutput(ctx, "rctl", "-r", fmt.Sprintf("jail:%s", handle.ID))
	if err != nil {
		// If no rules exist, this is not an error
		if strings.Contains(string(output), "No rules") || strings.Contains(string(output), "not found") {
			return nil
		}
		return fmt.Errorf("failed to remove rctl rules: %w (output: %s)", err, string(output))
	}

	return nil
}

// isValidRctlResource checks if a resource name is valid.
func isValidRctlResource(resource string) bool {
	validResources := map[string]bool{
		"cputime":         true, // CPU time in seconds
		"datasize":        true, // Data segment size
		"stacksize":       true, // Stack size
		"coredumpsize":    true, // Core dump size
		"memoryuse":       true, // Physical memory usage
		"memorylocked":    true, // Locked memory
		"maxproc":         true, // Maximum number of processes
		"openfiles":       true, // Maximum number of open files
		"vmemoryuse":      true, // Virtual memory usage
		"pseudoterminals": true, // Number of PTYs
		"swapuse":         true, // Swap usage
		"nthr":            true, // Number of threads
		"msgqqueued":      true, // Message queue bytes
		"msgqsize":        true, // Message queue size
		"nmsgq":           true, // Number of message queues
		"nsem":            true, // Number of semaphores
		"nsemop":          true, // Number of semaphore operations
		"nshm":            true, // Number of shared memory segments
		"shmsize":         true, // Shared memory size
		"wallclock":       true, // Wallclock time
		"pcpu":            true, // CPU usage percentage
		"readbps":         true, // Read bytes per second
		"writebps":        true, // Write bytes per second
		"readiops":        true, // Read operations per second
		"writeiops":       true, // Write operations per second
	}
	return validResources[resource]
}

// isValidRctlAction checks if an action is valid.
func isValidRctlAction(action string) bool {
	switch action {
	case "deny", // Deny resource allocation
		"log",      // Log when limit is reached
		"devctl",   // Send devctl notification
		"throttle": // Slow the process down — applyRCTLLimits creates these
		return true
	}
	// rctl(8) needs a signal name after "sig": bare "sig" is not a rule it
	// accepts, while "sigterm", "sigkill" and the rest are.
	return strings.HasPrefix(action, "sig") && len(action) > len("sig")
}

// SetCPUPriority sets the CPU scheduling priority (nice value) for all processes in a jail.
//
// The priority is a Unix nice value between -20 (highest priority) and 19 (lowest priority).
// Default priority is 0.
//
// Common use cases:
//   - Production jails: -5 to -10 (higher priority)
//   - Development jails: 5 to 10 (lower priority)
//   - Background batch jobs: 15 to 19 (lowest priority)
func (p *JailProvider) SetCPUPriority(ctx context.Context, handle provider.InstanceHandle, priority int) error {
	jailName := handle.ID
	// The name reaches jexec below.
	if err := validation.ValidateInstanceName(jailName); err != nil {
		return fmt.Errorf("invalid jail name %q: %w", jailName, err)
	}

	// SECURITY: Validate priority range
	if priority < -20 || priority > 19 {
		return fmt.Errorf("priority must be between -20 and 19 (got: %d)", priority)
	}

	jid, err := p.getJailID(ctx, jailName)
	if err != nil {
		return fmt.Errorf("failed to get jail ID: %w", err)
	}

	if jid == 0 {
		return fmt.Errorf("jail %s is not running", jailName)
	}

	// Get all PIDs in the jail
	// Use jexec to run ps inside the jail to get all processes
	output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "ps", "-ax", "-o", "pid=")
	if err != nil {
		return fmt.Errorf("failed to get jail processes: %w (output: %s)", err, string(output))
	}

	// Parse PIDs
	pids := []string{}
	for _, line := range strings.Split(string(output), "\n") {
		pid := strings.TrimSpace(line)
		if pid != "" {
			pids = append(pids, pid)
		}
	}

	if len(pids) == 0 {
		return fmt.Errorf("no processes found in jail %s", jailName)
	}

	// One process at a time. A single "renice -p <pid1> <pid2> ..." exits
	// non-zero as soon as any one of them has already gone — which is ordinary
	// in a live jail — and the whole call then reported a failure although the
	// remaining processes had been renamed correctly.
	var failed []string
	for _, pid := range pids {
		out, err := p.cmd().CombinedOutput(ctx, "renice", "-n", strconv.Itoa(priority), "-p", pid)
		if err == nil {
			continue
		}
		// A process that exited between ps and renice is not a failure.
		if strings.Contains(strings.ToLower(string(out)), "no such process") {
			continue
		}
		failed = append(failed, fmt.Sprintf("%s (%s)", pid, strings.TrimSpace(string(out))))
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed to set CPU priority for %d of %d processes in jail %s: %s",
			len(failed), len(pids), jailName, strings.Join(failed, "; "))
	}

	return nil
}

// GetCPUPriority returns the current CPU scheduling priority for a jail.
//
// Returns the nice value of the jail's init process (usually the first process in the jail).
func (p *JailProvider) GetCPUPriority(ctx context.Context, handle provider.InstanceHandle) (int, error) {
	jailName := handle.ID
	// The name reaches jexec below.
	if err := validation.ValidateInstanceName(jailName); err != nil {
		return 0, fmt.Errorf("invalid jail name %q: %w", jailName, err)
	}

	jid, err := p.getJailID(ctx, jailName)
	if err != nil {
		return 0, fmt.Errorf("failed to get jail ID: %w", err)
	}

	if jid == 0 {
		return 0, fmt.Errorf("jail %s is not running", jailName)
	}

	// No shell runs here, so "|", "head" and "-1" reached ps as literal
	// arguments and it simply failed. Read the list as SetCPUPriority does and
	// pick the first entry in Go.
	output, err := p.cmd().CombinedOutput(ctx, "jexec", jailName, "ps", "-ax", "-o", "pid=")
	if err != nil {
		return 0, fmt.Errorf("failed to get jail init process: %w (output: %s)", err, string(output))
	}

	pid := ""
	for _, line := range strings.Split(string(output), "\n") {
		if v := strings.TrimSpace(line); v != "" {
			pid = v
			break
		}
	}
	if pid == "" {
		return 0, fmt.Errorf("no processes found in jail %s", jailName)
	}

	// Get nice value using ps
	psOutput, err := p.cmd().CombinedOutput(ctx, "ps", "-o", "nice=", "-p", pid)
	if err != nil {
		return 0, fmt.Errorf("failed to get process priority: %w", err)
	}

	niceStr := strings.TrimSpace(string(psOutput))
	nice, err := strconv.Atoi(niceStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse nice value: %w", err)
	}

	return nice, nil
}
