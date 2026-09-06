package vfkit

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newStatsCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "stats <name>",
		Aliases: []string{"metrics"},
		Short:   "Show resource usage for a VM",
		Args:    cobra.ExactArgs(1),
		RunE:    runStats,
	}
}

func runStats(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Confirmed to be a vfkit instance, as the sibling commands do: without
	// it this reported the metrics of whatever the name resolved to.
	if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, name, "vfkit"); err != nil {
		return err
	}

	metrics, err := cmdutil.APIClient.GetInstanceMetrics(cmd.Context(), name)
	if err != nil {
		return fmt.Errorf("failed to get metrics for %s: %w", name, err)
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "VM:\t%s\n\n", name)
	fmt.Fprintf(w, "RESOURCES\n")
	fmt.Fprintf(w, "  CPU Usage:\t%.1f%%\n", metrics.CPUUsagePercent)
	fmt.Fprintf(w, "  Memory Used:\t%s\n", cmdutil.FormatBytes(metrics.MemoryUsedMB*1024*1024))
	fmt.Fprintf(w, "  Memory Total:\t%s\n\n", cmdutil.FormatBytes(metrics.MemoryTotalMB*1024*1024))
	fmt.Fprintf(w, "DISK I/O\n")
	fmt.Fprintf(w, "  Read:\t%s\n", cmdutil.FormatBytes(metrics.DiskReadBytes))
	fmt.Fprintf(w, "  Write:\t%s\n\n", cmdutil.FormatBytes(metrics.DiskWriteBytes))
	fmt.Fprintf(w, "NETWORK\n")
	fmt.Fprintf(w, "  RX:\t%s\n", cmdutil.FormatBytes(metrics.NetRxBytes))
	fmt.Fprintf(w, "  TX:\t%s\n", cmdutil.FormatBytes(metrics.NetTxBytes))

	if metrics.Timestamp != "" {
		fmt.Fprintf(w, "\nTimestamp:\t%s\n", metrics.Timestamp)
	}

	return w.Flush()
}
