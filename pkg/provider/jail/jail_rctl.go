package jail

import (
	"context"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

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

// ResourceLimit is an alias: the type moved to pkg/provider so the API can
// reach rctl through the RctlProvider interface instead of the concrete
// *JailProvider.
type ResourceLimit = provider.ResourceLimit

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

	// The same lock SetResourceLimits takes, held across the removal and every
	// addition below: this path — the one start and SetInstanceResources take —
	// is the other rctl mutation for a jail, and the two interleaved freely.
	lock := rctlLock(name)
	lock.Lock()
	defer lock.Unlock()

	handle := provider.InstanceHandle{ID: name}

	// Read before replacing, and refuse without a snapshot: the same rule
	// SetResourceLimits follows. Without it a failure halfway through leaves
	// the jail holding part of the new set and none of the old, with no way
	// back.
	previous, err := p.getResourceLimitsLocked(ctx, handle)
	if err != nil {
		return fmt.Errorf("refusing to apply the RCTL limits: the existing rules could not be read, so a failure could not be undone: %w", err)
	}

	// rctl keeps every matching rule, so adding a looser one leaves the earlier,
	// stricter rule in force. ApplyResourceLimits already clears first; this
	// path — the one start and SetInstanceResources take — did not.
	//
	// A failed removal is fatal: RemoveResourceLimits answers nil when there is
	// nothing to remove, so an error means the old rules may still be in force,
	// and adding the new ones on top of them reports a success nobody asked for.
	if err := p.removeResourceLimitsLocked(ctx, handle); err != nil {
		return fmt.Errorf("failed to remove the existing RCTL rules: %w", err)
	}

	// Every addition goes through this, so a failure part-way puts the jail
	// back the way it was rather than leaving it with a fragment of both sets.
	add := func(rule, what string) error {
		if err := p.cmd().Run(ctx, "rctl", "-a", rule); err != nil {
			// A canceled request is one way the command above failed, and the
			// rollback runs rctl too.
			rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancelRollback()
			p.restoreResourceLimits(rollbackCtx, handle, previous)
			return fmt.Errorf("failed to set %s: %w", what, err)
		}
		return nil
	}

	// CPU limit (percentage)
	if resources.CPUs > 0 {
		limit := fmt.Sprintf("jail:%s:pcpu:deny=%d", name, resources.CPUs*100)
		if err := add(limit, "CPU limit"); err != nil {
			return err
		}
	}

	// Memory limit
	if resources.MemoryMB > 0 {
		bytes := resources.MemoryMB * 1024 * 1024
		limit := fmt.Sprintf("jail:%s:memoryuse:deny=%d", name, bytes)
		if err := add(limit, "memory limit"); err != nil {
			return err
		}
	}

	// I/O limits — hard failures (user-configured limits must be applied)
	if resources.ReadBPS > 0 {
		limit := fmt.Sprintf("jail:%s:readbps:throttle=%d", name, resources.ReadBPS)
		if err := add(limit, "read BPS limit"); err != nil {
			return err
		}
	}
	if resources.WriteBPS > 0 {
		limit := fmt.Sprintf("jail:%s:writebps:throttle=%d", name, resources.WriteBPS)
		if err := add(limit, "write BPS limit"); err != nil {
			return err
		}
	}
	if resources.ReadIOPS > 0 {
		limit := fmt.Sprintf("jail:%s:readiops:throttle=%d", name, resources.ReadIOPS)
		if err := add(limit, "read IOPS limit"); err != nil {
			return err
		}
	}
	if resources.WriteIOPS > 0 {
		limit := fmt.Sprintf("jail:%s:writeiops:throttle=%d", name, resources.WriteIOPS)
		if err := add(limit, "write IOPS limit"); err != nil {
			return err
		}
	}

	// Process limit — hard failure
	processLimit := "1000"
	if resources.MaxProc > 0 {
		processLimit = strconv.Itoa(resources.MaxProc)
	}
	limit := fmt.Sprintf("jail:%s:maxproc:deny=%s", name, processLimit)
	if err := add(limit, "maxproc limit"); err != nil {
		return err
	}

	return nil
}

// rctlAmountPattern is the whole grammar an rctl amount may use: a whole
// number with an optional unit.
//
// rctl(8) hands byte amounts to expand_number(3), which documents "a decimal
// number ... optionally followed ... by a suffix indicating a power-of-two
// multiplier" — K, M, G, T, P, E, in either case, and no fraction. pcpu is
// "in percents of a single CPU core", a bare number rather than a percent
// sign. So "1.5G" and "50%" were accepted here and refused by rctl, turning a
// caller's mistake into a provider error where it is a 400.
var rctlAmountPattern = regexp.MustCompile(`^\d+[KMGTPEkmgtpe]?$`)

// rctlLocks serializes a whole rctl replacement per jail.
//
// SetResourceLimits reads the current rules, removes them, then adds the new
// ones. Two calls for the same jail interleaved freely: one could remove the
// rules the other had just added, or roll back over them, and both reported
// success. There is no existing per-instance lock in this provider — createMu
// and dhcpMu guard other things — so the replacement gets its own.
//
// A fixed array rather than a map keyed by jail name: the names come from the
// API, and a map would grow with them.
var rctlLocks [32]sync.Mutex

func rctlLock(jailName string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(jailName))
	return &rctlLocks[h.Sum32()%uint32(len(rctlLocks))]
}

// validateRCTLAmount reports whether an amount may be interpolated into an
// rctl rule. Named rather than inline so the tests can exercise the rule that
// ships: the test file carried its own copy, which checked the length and some
// shell metacharacters but never the grammar.
func validateRCTLAmount(amount string) error {
	if len(amount) > 20 {
		return fmt.Errorf("invalid amount: too long (max 20 characters)")
	}
	if !rctlAmountPattern.MatchString(amount) {
		return fmt.Errorf("invalid amount %q: expected a whole number with an optional unit suffix, e.g. 2G, 512M or 3600", amount)
	}
	// The suffix is a power-of-two multiplier, and expand_number(3) answers
	// ERANGE when the product does not fit a uint64: "16E" is well-formed and
	// refused by rctl, which made a caller's mistake a 500.
	if _, err := expandRCTLAmount(amount); err != nil {
		return err
	}
	return nil
}

// expandRCTLAmount applies the unit suffix the way expand_number(3) does, and
// reports the overflow it reports.
func expandRCTLAmount(amount string) (uint64, error) {
	shift := uint(0)
	digits := amount
	switch last := amount[len(amount)-1]; last {
	case 'K', 'k':
		shift = 10
	case 'M', 'm':
		shift = 20
	case 'G', 'g':
		shift = 30
	case 'T', 't':
		shift = 40
	case 'P', 'p':
		shift = 50
	case 'E', 'e':
		shift = 60
	}
	if shift > 0 {
		digits = amount[:len(amount)-1]
	}

	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("amount %q does not fit a 64-bit count", amount)
	}
	if shift > 0 && n > (^uint64(0))>>shift {
		return 0, fmt.Errorf("amount %q overflows once its unit suffix is applied", amount)
	}
	return n << shift, nil
}

// restoreResourceLimits puts back the rules a failed replacement removed.
// The caller holds the jail's lock; this uses the unlocked helpers.
//
// The partial replacement is cleared first. Re-adding the old rules on top of
// it left the jail holding both sets — the rules it had and the ones that had
// been applied before the failure — which is neither state the caller asked
// for. SetResourceLimits refuses to start without a snapshot, so there is
// always something to put back here.
//
// A restore that itself fails is logged rather than swallowed: the jail is
// then running with no limits, which the operator needs to know.
func (p *JailProvider) restoreResourceLimits(ctx context.Context, handle provider.InstanceHandle, previous []ResourceLimit) {
	if err := p.removeResourceLimitsLocked(ctx, handle); err != nil {
		// Adding the old rules on top of a partial set that could not be
		// cleared leaves the jail with neither the set it had nor the one that
		// was asked for. Stopping leaves the partial set, which is at least
		// one coherent thing, and says so.
		p.logWarn(ctx, "the partial RCTL replacement could not be cleared, so the old rules were not restored: the jail holds neither set",
			"jail", handle.ID, "err", err)
		return
	}
	for _, limit := range previous {
		rule := fmt.Sprintf("jail:%s:%s:%s=%s", handle.ID, limit.Resource, limit.Action, limit.Amount)
		if output, err := p.cmd().CombinedOutput(ctx, "rctl", "-a", rule); err != nil {
			p.logWarn(ctx, "could not restore an RCTL rule after a failed replacement",
				"jail", handle.ID, "rule", rule, "err", err, "output", string(output))
		}
	}
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

	// Everything is validated before anything is removed. Validation used to
	// sit inside the add loop, after the removal: a bad resource, action or
	// amount in the second entry left the jail with the first new rule and
	// none of its old ones — or, if it was the first entry, with no limits at
	// all.
	//
	// SECURITY: the amount is interpolated into a colon-separated,
	// equals-terminated rule grammar, so what it may contain is spelled out
	// rather than what it may not. The old blacklist named shell
	// metacharacters — which rctl never sees, there being no shell — and let
	// ":" and "=" through: "2G:pcpu:deny=100" defined a second rule nobody
	// asked for.
	for _, limit := range limits {
		if !isValidRctlResource(limit.Resource) {
			return fmt.Errorf("%w: unknown resource %q", provider.ErrInvalidResourceLimit, limit.Resource)
		}
		if !isValidRctlAction(limit.Action) {
			return fmt.Errorf("%w: unknown action %q", provider.ErrInvalidResourceLimit, limit.Action)
		}
		if err := validateRCTLAmount(limit.Amount); err != nil {
			return fmt.Errorf("%w: %w", provider.ErrInvalidResourceLimit, err)
		}
		if err := rctlCombinationError(limit.Resource, limit.Action); err != nil {
			return err
		}
	}

	// Read the rules back before replacing them, so a failed add can put them
	// there again. A failure halfway through used to return with the jail
	// holding neither the old set nor the new one.
	// Held across the snapshot, the removal, every addition and the rollback:
	// anything less lets a second call for this jail interleave with this one.
	lock := rctlLock(handle.ID)
	lock.Lock()
	defer lock.Unlock()

	// Refused rather than logged: removing the rules without a snapshot means
	// a failure halfway through leaves the jail unlimited with no way back.
	// Not replacing them at all is the safer answer.
	previous, err := p.getResourceLimitsLocked(ctx, handle)
	if err != nil {
		return fmt.Errorf("refusing to replace the RCTL rules: the existing ones could not be read, so a failure could not be undone: %w", err)
	}

	// Removal failing is fatal to the replacement: RemoveResourceLimits already
	// answers nil when there is nothing to remove, so any error left means the
	// old rules may still be in force — and adding the new ones on top of them
	// and reporting success is the one outcome nobody asked for.
	if err := p.removeResourceLimitsLocked(ctx, handle); err != nil {
		return fmt.Errorf("failed to remove the existing RCTL rules: %w", err)
	}

	// Add each limit rule
	for _, limit := range limits {
		// Build rctl rule: jail:jailname:resource:action=amount
		rule := fmt.Sprintf("jail:%s:%s:%s=%s", handle.ID, limit.Resource, limit.Action, limit.Amount)

		// Add rule using rctl -a
		output, err := p.cmd().CombinedOutput(ctx, "rctl", "-a", rule)
		if err != nil {
			// A canceled request is one way the command above failed, and the
			// rollback runs rctl too: on the same context it would fail at
			// once, leaving the partial set in place.
			rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			p.restoreResourceLimits(rollbackCtx, handle, previous)
			cancelRollback()
			return fmt.Errorf("failed to add rctl rule %s: %w (output: %s)", rule, err, string(output))
		}
	}

	return nil
}

// GetResourceLimits retrieves current resource limits for a jail.
//
// Takes the same lock the replacement paths hold, so a read cannot observe a
// jail midway through one — between the removal and the additions it has no
// rules at all, and that state used to be reportable.
func (p *JailProvider) GetResourceLimits(ctx context.Context, handle provider.InstanceHandle) ([]ResourceLimit, error) {
	lock := rctlLock(handle.ID)
	lock.Lock()
	defer lock.Unlock()

	return p.getResourceLimitsLocked(ctx, handle)
}

// getResourceLimitsLocked is GetResourceLimits for a caller that already holds
// the jail's lock. Go mutexes are not reentrant, so the replacement paths must
// call this one.
func (p *JailProvider) getResourceLimitsLocked(ctx context.Context, handle provider.InstanceHandle) ([]ResourceLimit, error) {
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
//
// Takes the jail's lock: a DELETE arriving during a replacement used to clear
// the rules the replacement had just added, which then returned success with
// only part of its set in force.
func (p *JailProvider) RemoveResourceLimits(ctx context.Context, handle provider.InstanceHandle) error {
	lock := rctlLock(handle.ID)
	lock.Lock()
	defer lock.Unlock()

	return p.removeResourceLimitsLocked(ctx, handle)
}

// removeResourceLimitsLocked is RemoveResourceLimits for a caller that already
// holds the jail's lock.
func (p *JailProvider) removeResourceLimitsLocked(ctx context.Context, handle provider.InstanceHandle) error {
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
	// An allowlist, not a "sig" prefix: "sigbogus" satisfied the prefix and was
	// refused by rctl, turning a caller's typo into a 500. These are the
	// signals rctl(8) names.
	// Every signal signal(3) names, which is what rctl(8) points at for "sig*".
	// The first list here was written from the common signals and left out
	// sigchld, sigemt, siginfo, sigio, sigprof, sigthr and sigwinch — refusing
	// rules rctl accepts, which is the opposite mistake from the "sig" prefix
	// it replaced. Not siglibrt: it is in neither action table.
	switch action {
	case "sigabrt", "sigalrm", "sigbus", "sigchld", "sigcont", "sigemt", "sigfpe",
		"sighup", "sigill", "siginfo", "sigint", "sigio", "sigkill", "sigpipe",
		"sigprof", "sigquit", "sigsegv", "sigstop", "sigsys", "sigterm", "sigthr",
		"sigtrap", "sigtstp", "sigttin", "sigttou", "sigurg", "sigusr1", "sigusr2",
		"sigvtalrm", "sigwinch", "sigxcpu", "sigxfsz":
		return true
	}
	return false
}

// rctlCombinationError reports why rctl(8) will not take this resource with
// this action, or nil when it will.
//
// The two allowlists are independent, so a pair each half accepts can still be
// one rctl refuses: "cputime:deny" passed both and was rejected by rctl, after
// the replacement had already removed the jail's existing rules.
func rctlCombinationError(resource, action string) error {
	// rctl(8): deny is "not supported for cputime, wallclock, readbps,
	// writebps, readiops, and writeiops".
	if action == "deny" {
		switch resource {
		case "cputime", "wallclock", "readbps", "writebps", "readiops", "writeiops":
			return fmt.Errorf("%w: rctl does not deny %q; use log, devctl or a signal",
				provider.ErrInvalidResourceLimit, resource)
		}
	}
	// rctl(8): throttle is "only supported for readbps, writebps, readiops and
	// writeiops".
	if action == "throttle" {
		switch resource {
		case "readbps", "writebps", "readiops", "writeiops":
		default:
			return fmt.Errorf("%w: rctl only throttles readbps, writebps, readiops and writeiops, not %q",
				provider.ErrInvalidResourceLimit, resource)
		}
	}
	return nil
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

// The API reaches rctl and VNET through these interfaces, not through
// *JailProvider. Asserted here so a signature change breaks the build rather
// than turning a live endpoint into 501.
var (
	_ provider.RctlProvider = (*JailProvider)(nil)
	_ provider.VNETProvider = (*JailProvider)(nil)
)
