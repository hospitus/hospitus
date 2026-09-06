// Package job provides CLI commands for managing async jobs.
package job

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

// NewJobCommand returns the root job command.
func NewJobCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Manage async jobs",
		Long: `List, inspect, cancel, and delete background jobs.

Long-running operations (instance create, image fetch, etc.) run as async
jobs. Use these commands to monitor progress or cancel running work.

Examples:
  hospitus job list
  hospitus job list --status running
  hospitus job info <job-id>
  hospitus job cancel <job-id>
  hospitus job delete <job-id>
  hospitus job stats`,
	}

	cmd.AddCommand(newJobListCommand())
	cmd.AddCommand(newJobInfoCommand())
	cmd.AddCommand(newJobCancelCommand())
	cmd.AddCommand(newJobDeleteCommand())
	cmd.AddCommand(newJobStatsCommand())

	return cmd
}

// ── list ──────────────────────────────────────────────────────────────────────

func newJobListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List jobs",
		Long: `List background jobs. Without --status, all jobs are returned.

Examples:
  hospitus job list
  hospitus job list --status running
  hospitus job list --status pending
  hospitus job list --output json`,
		RunE: runJobList,
	}

	cmd.Flags().String("status", "", "Filter by status (pending|running|completed|failed|canceled)")
	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runJobList(cmd *cobra.Command, _ []string) error {
	status, _ := cmd.Flags().GetString("status")
	output, _ := cmd.Flags().GetString("output")

	ctx := cmd.Context()

	resp, err := cmdutil.GetClient().ListJobs(ctx, status)
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}

	if output == "json" {
		return json.NewEncoder(os.Stdout).Encode(resp)
	}

	if len(resp.Jobs) == 0 {
		fmt.Println("No jobs found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tTYPE\tSTATUS\tPROGRESS\tDESCRIPTION\tAGE\n")
	for _, j := range resp.Jobs {
		progress := fmt.Sprintf("%.0f%%", j.Progress*100)
		age := formatAge(j.CreatedAt)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			j.ID, j.Type, j.Status, progress, truncate(j.Description, 40), age)
	}
	return w.Flush()
}

// ── info ──────────────────────────────────────────────────────────────────────

func newJobInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "info <job-id>",
		Aliases: []string{"get", "show"},
		Short:   "Show job details",
		Long: `Display full details for a specific job, including progress and any error.

Examples:
  hospitus job info <job-id>
  hospitus job info <job-id> --output json`,
		Args: cobra.ExactArgs(1),
		RunE: runJobInfo,
	}

	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runJobInfo(cmd *cobra.Command, args []string) error {
	jobID := args[0]
	output, _ := cmd.Flags().GetString("output")

	ctx := cmd.Context()

	j, err := cmdutil.GetClient().GetJob(ctx, jobID)
	if err != nil {
		return fmt.Errorf("get job: %w", err)
	}

	if output == "json" {
		return json.NewEncoder(os.Stdout).Encode(j)
	}

	fmt.Printf("ID:          %s\n", j.ID)
	fmt.Printf("Type:        %s\n", j.Type)
	fmt.Printf("Description: %s\n", j.Description)
	fmt.Printf("Status:      %s\n", j.Status)
	fmt.Printf("Progress:    %.0f%%\n", j.Progress*100)
	if j.Message != "" {
		fmt.Printf("Message:     %s\n", j.Message)
	}
	if j.Error != "" {
		fmt.Printf("Error:       %s\n", j.Error)
	}
	fmt.Printf("Created:     %s\n", j.CreatedAt.Format(time.RFC3339))
	if j.StartedAt != nil {
		fmt.Printf("Started:     %s\n", j.StartedAt.Format(time.RFC3339))
	}
	if j.CompletedAt != nil {
		fmt.Printf("Completed:   %s\n", j.CompletedAt.Format(time.RFC3339))
		// Duration is the actual run time (from start to completion), not the
		// wall-clock age since submission. Fall back to CreatedAt only when the
		// job never recorded a start time.
		start := j.CreatedAt
		if j.StartedAt != nil {
			start = *j.StartedAt
		}
		fmt.Printf("Duration:    %s\n", j.CompletedAt.Sub(start).Truncate(time.Millisecond))
	}
	if len(j.Metadata) > 0 {
		fmt.Println("Metadata:")
		for k, v := range j.Metadata {
			fmt.Printf("  %s: %s\n", k, v)
		}
	}
	if j.Result != nil {
		fmt.Println("Result:")
		out, _ := json.MarshalIndent(j.Result, "  ", "  ")
		fmt.Printf("  %s\n", out)
	}

	return nil
}

// ── cancel ────────────────────────────────────────────────────────────────────

func newJobCancelCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <job-id>",
		Short: "Cancel a running or pending job",
		Long: `Cancel a job that is currently pending or running.
Already completed, failed, or canceled jobs cannot be canceled.

Examples:
  hospitus job cancel <job-id>`,
		Args: cobra.ExactArgs(1),
		RunE: runJobCancel,
	}
}

func runJobCancel(cmd *cobra.Command, args []string) error {
	jobID := args[0]

	ctx := cmd.Context()

	if err := cmdutil.GetClient().CancelJob(ctx, jobID); err != nil {
		return fmt.Errorf("cancel job: %w", err)
	}

	fmt.Printf("Job %s canceled.\n", jobID)
	return nil
}

// ── delete ────────────────────────────────────────────────────────────────────

func newJobDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <job-id>",
		Aliases: []string{"rm", "remove"},
		Short:   "Delete a finished job",
		Long: `Remove a completed, failed, or canceled job from the history.
Running or pending jobs must be canceled first.

Examples:
  hospitus job delete <job-id>`,
		Args: cobra.ExactArgs(1),
		RunE: runJobDelete,
	}
}

func runJobDelete(cmd *cobra.Command, args []string) error {
	jobID := args[0]

	ctx := cmd.Context()

	if err := cmdutil.GetClient().DeleteJob(ctx, jobID); err != nil {
		return fmt.Errorf("delete job: %w", err)
	}

	fmt.Printf("Job %s deleted.\n", jobID)
	return nil
}

// ── stats ─────────────────────────────────────────────────────────────────────

func newJobStatsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show job manager statistics",
		Long: `Display counts of jobs by status.

Examples:
  hospitus job stats
  hospitus job stats --output json`,
		RunE: runJobStats,
	}

	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runJobStats(cmd *cobra.Command, _ []string) error {
	output, _ := cmd.Flags().GetString("output")

	ctx := cmd.Context()

	stats, err := cmdutil.GetClient().JobStats(ctx)
	if err != nil {
		return fmt.Errorf("job stats: %w", err)
	}

	if output == "json" {
		return json.NewEncoder(os.Stdout).Encode(stats)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "STATUS\tCOUNT\n")
	order := []string{"pending", "running", "completed", "failed", "canceled"}
	for _, s := range order {
		if count, ok := stats[s]; ok {
			fmt.Fprintf(w, "%s\t%d\n", s, count)
		}
	}
	return w.Flush()
}

// ── helpers ───────────────────────────────────────────────────────────────────

func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n-1] + "…"
}
