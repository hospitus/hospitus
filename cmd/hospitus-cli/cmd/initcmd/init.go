// Package initcmd implements `hospitus init` — the host prerequisite doctor and
// setup wizard backed by pkg/doctor.
package initcmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/pkg/doctor"
)

// NewInitCommand builds the `hospitus init` command.
func NewInitCommand() *cobra.Command {
	var (
		checkOnly bool
		auto      bool
		providers []string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Check and prepare the host for Hospitus (prerequisite doctor)",
		Long: `Inspect the host for everything Hospitus needs — ZFS, kernel modules, tools,
sysctls, PF — and optionally fix what it safely can.

Modes:
  hospitus init --check        Report prerequisite status and exit (read-only doctor).
  hospitus init                Interactive wizard: prompt to fix each fixable item.
  hospitus init --auto         Non-interactively apply all safe fixes (idempotent).

On FreeBSD all providers are checked; on macOS only the QEMU and Podman
prerequisites apply. Hospitus never edits your /etc/pf.conf — it only reports what
anchor lines to add and manages its own anchors.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd.Context(), cmd.OutOrStdout(), cmd.InOrStdin(), checkOnly, auto, providers)
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "read-only: report prerequisite status and exit with non-zero on required failures")
	cmd.Flags().BoolVar(&auto, "auto", false, "non-interactively apply all safe fixes (idempotent)")
	cmd.Flags().StringSliceVar(&providers, "provider", nil, "limit checks to these providers: jail,bhyve,qemu,podman,vfkit,container (default: all applicable)")
	return cmd
}

func runInit(ctx context.Context, out io.Writer, in io.Reader, checkOnly, auto bool, providers []string) error {
	runner := doctor.NewRunner(doctor.NewSystem(), nil)
	return runInitWithRunner(ctx, out, in, runner, checkOnly, auto, providers, cmdutil.RequireRootStrict)
}

// runInitWithRunner is the testable core: the runner and the root-requirement
// are injected.
func runInitWithRunner(ctx context.Context, out io.Writer, in io.Reader, runner *doctor.Runner, checkOnly, auto bool, providers []string, requireRoot func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	selected := selectedProviders(providers)

	report := runner.Diagnose(ctx, selected)
	fmt.Fprintln(out, "Hospitus prerequisite check")
	fmt.Fprintln(out, strings.Repeat("─", 40))
	printReport(out, report)

	if checkOnly {
		printSummary(out, report)
		if report.HasRequiredFailures() {
			return fmt.Errorf("required prerequisites are missing (see FAIL items above)")
		}
		return nil
	}

	// Fix modes need root (kldload, sysctl, sysrc, mkdir under /var).
	if err := requireRoot(); err != nil {
		return err
	}

	fixable := fixableFailures(report)
	if len(fixable) == 0 {
		fmt.Fprintln(out, "\nNothing to fix — all fixable prerequisites are satisfied.")
		printSummary(out, report)
		if report.HasRequiredFailures() {
			return fmt.Errorf("required prerequisites remain that Hospitus cannot fix automatically (see remediation above)")
		}
		return nil
	}

	var confirm func(doctor.Check) bool
	if !auto {
		confirm = interactiveConfirm(out, bufio.NewReader(in))
	}

	fmt.Fprintf(out, "\nApplying fixes (%d candidate%s)...\n", len(fixable), cmdutil.Plural(len(fixable)))
	report = runner.ApplyFixes(ctx, report, confirm)

	fmt.Fprintln(out, "\nResult:")
	fmt.Fprintln(out, strings.Repeat("─", 40))
	printReport(out, report)
	printSummary(out, report)

	if report.HasRequiredFailures() {
		return fmt.Errorf("required prerequisites remain (see remediation above)")
	}
	return nil
}

// selectedProviders turns the --provider flag into a set. Empty means all.
func selectedProviders(providers []string) map[string]bool {
	if len(providers) == 0 {
		return nil
	}
	set := make(map[string]bool, len(providers))
	for _, p := range providers {
		set[strings.ToLower(strings.TrimSpace(p))] = true
	}
	return set
}

func fixableFailures(report doctor.Report) []doctor.Check {
	var out []doctor.Check
	for i := range report.Results {
		cr := report.Results[i]
		if cr.Check.Fixable() &&
			(cr.Result.Status == doctor.StatusFail || cr.Result.Status == doctor.StatusWarn) {
			out = append(out, cr.Check)
		}
	}
	return out
}

// interactiveConfirm returns a confirm callback that prompts on out/in.
func interactiveConfirm(out io.Writer, r *bufio.Reader) func(doctor.Check) bool {
	return func(c doctor.Check) bool {
		fmt.Fprintf(out, "  Fix %q (%s)? [y/N] ", c.Name, c.Description)
		line, _ := r.ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		return line == "y" || line == "yes"
	}
}

// printReport writes one line per check, with remediation for problems.
func printReport(out io.Writer, report doctor.Report) {
	for i := range report.Results {
		cr := report.Results[i]
		req := ""
		if cr.Check.Required {
			req = " (required)"
		}
		fmt.Fprintf(out, "%s %-28s %s%s\n", symbol(cr.Result.Status), cr.Check.Name, cr.Result.Detail, req)
		if cr.Result.Remediation != "" &&
			(cr.Result.Status == doctor.StatusFail || cr.Result.Status == doctor.StatusWarn) {
			for _, l := range strings.Split(cr.Result.Remediation, "\n") {
				fmt.Fprintf(out, "        → %s\n", l)
			}
		}
	}
}

func printSummary(out io.Writer, report doctor.Report) {
	c := report.Counts()
	fmt.Fprintln(out, strings.Repeat("─", 40))
	fmt.Fprintf(out, "Summary: %d OK, %d fixed, %d warning%s, %d failed, %d skipped\n",
		c[doctor.StatusOK], c[doctor.StatusFixed], c[doctor.StatusWarn],
		cmdutil.Plural(c[doctor.StatusWarn]), c[doctor.StatusFail], c[doctor.StatusSkip])
}

func symbol(s doctor.Status) string {
	switch s {
	case doctor.StatusOK:
		return "[ OK ]"
	case doctor.StatusFixed:
		return "[FIXD]"
	case doctor.StatusWarn:
		return "[WARN]"
	case doctor.StatusFail:
		return "[FAIL]"
	case doctor.StatusSkip:
		return "[skip]"
	default:
		return "[????]"
	}
}
