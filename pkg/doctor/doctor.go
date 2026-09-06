// Package doctor implements the environment prerequisite checks and fixes that
// back `hospitus init`. Checks are data-driven and depend only on the System
// abstraction, so they are unit-testable without touching the real host.
//
// It supports three modes, all built on the same check set:
//   - Diagnose (read-only) — the `hospitus init --check` doctor.
//   - ApplyFixes with a confirm callback — the interactive wizard.
//   - ApplyFixes with no callback — the non-interactive, idempotent `--auto`.
package doctor

import "context"

// Status is the outcome of a single check.
type Status int

const (
	// StatusOK — the prerequisite is satisfied.
	StatusOK Status = iota
	// StatusWarn — an optional prerequisite is missing (non-fatal).
	StatusWarn
	// StatusFail — a required prerequisite is missing.
	StatusFail
	// StatusFixed — the prerequisite was missing and a fix was applied.
	StatusFixed
	// StatusSkip — the check does not apply on this platform/provider set.
	StatusSkip
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusWarn:
		return "WARN"
	case StatusFail:
		return "FAIL"
	case StatusFixed:
		return "FIXED"
	case StatusSkip:
		return "SKIP"
	default:
		return "?"
	}
}

// Provider tags mark which providers a check is relevant to. "core" checks
// always apply.
const (
	ProviderCore   = "core"
	ProviderJail   = "jail"
	ProviderBhyve  = "bhyve"
	ProviderQEMU   = "qemu"
	ProviderPodman = "podman"
	// macOS-only providers, both backed by Virtualization.framework.
	ProviderVFKit     = "vfkit"
	ProviderContainer = "container"
)

// Result is what a check reports.
type Result struct {
	Status Status
	// Detail is a human-readable explanation of what was found.
	Detail string
	// Remediation is shown when the check fails and cannot be auto-fixed.
	Remediation string
}

// Check is a single environment prerequisite.
type Check struct {
	// ID is a stable slug (e.g. "zfs-pool", "kmod-vmm").
	ID string
	// Name is a short human label.
	Name string
	// Description explains what the prerequisite is for.
	Description string
	// Providers this check is relevant to (ProviderCore always applies).
	Providers []string
	// Required marks a hard prerequisite; a failing required check makes
	// `hospitus init --check` exit non-zero.
	Required bool
	// Run performs the (read-only) check.
	Run func(ctx context.Context, sys System) Result
	// Fix applies the prerequisite. Nil when the check is not auto-fixable.
	Fix func(ctx context.Context, sys System) error
}

// Fixable reports whether the check has an auto-fix.
func (c Check) Fixable() bool { return c.Fix != nil }

// appliesTo reports whether the check is relevant to the requested provider set.
// A nil/empty selection means "all providers". Core checks always apply.
func (c Check) appliesTo(selected map[string]bool) bool {
	for _, p := range c.Providers {
		if p == ProviderCore {
			return true
		}
	}
	if len(selected) == 0 {
		return true
	}
	for _, p := range c.Providers {
		if selected[p] {
			return true
		}
	}
	return false
}

// CheckResult pairs a check with its result.
type CheckResult struct {
	Check  Check
	Result Result
}

// Report is the outcome of a diagnose/fix run.
type Report struct {
	Results []CheckResult
}

// HasRequiredFailures reports whether any required check is failing (not fixed).
func (r Report) HasRequiredFailures() bool {
	for i := range r.Results {
		cr := &r.Results[i]
		if cr.Check.Required && cr.Result.Status == StatusFail {
			return true
		}
	}
	return false
}

// Counts returns the number of results per status.
func (r Report) Counts() map[Status]int {
	m := make(map[Status]int)
	for i := range r.Results {
		m[r.Results[i].Result.Status]++
	}
	return m
}

// Runner executes a set of checks against a System.
type Runner struct {
	sys    System
	checks []Check
}

// NewRunner builds a runner. When checks is nil it uses DefaultChecks().
func NewRunner(sys System, checks []Check) *Runner {
	if checks == nil {
		checks = DefaultChecks()
	}
	return &Runner{sys: sys, checks: checks}
}

// Diagnose runs every applicable check read-only. selected filters checks by
// provider; nil/empty means all providers.
func (r *Runner) Diagnose(ctx context.Context, selected map[string]bool) Report {
	var out Report
	for _, c := range r.checks {
		if !c.appliesTo(selected) {
			continue
		}
		res := c.Run(ctx, r.sys)
		out.Results = append(out.Results, CheckResult{Check: c, Result: res})
	}
	return out
}

// ApplyFixes attempts to fix failing, auto-fixable checks in the report and
// returns an updated report. confirm decides whether to apply a given fix; when
// confirm is nil every fix is applied (the `--auto` path). After a successful
// fix the check is re-run to confirm.
func (r *Runner) ApplyFixes(ctx context.Context, report Report, confirm func(Check) bool) Report {
	var out Report
	for i := range report.Results {
		cr := report.Results[i]
		needsFix := cr.Result.Status == StatusFail || cr.Result.Status == StatusWarn
		if !needsFix || !cr.Check.Fixable() {
			out.Results = append(out.Results, cr)
			continue
		}
		if confirm != nil && !confirm(cr.Check) {
			out.Results = append(out.Results, cr)
			continue
		}
		if err := cr.Check.Fix(ctx, r.sys); err != nil {
			// Keep what the check found: replacing it leaves the operator with
			// the failure and no description of the prerequisite behind it.
			cr.Result.Detail = cr.Result.Detail + " (fix failed: " + err.Error() + ")"
			out.Results = append(out.Results, cr)
			continue
		}
		// Re-run to confirm the fix took effect (idempotency check).
		res := cr.Check.Run(ctx, r.sys)
		if res.Status == StatusOK {
			res.Status = StatusFixed
		}
		out.Results = append(out.Results, CheckResult{Check: cr.Check, Result: res})
	}
	return out
}
