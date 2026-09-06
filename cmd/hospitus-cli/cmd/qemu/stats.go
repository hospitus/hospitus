package qemu

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newStatsCommand() *cobra.Command {
	var watch bool
	var interval int

	cmd := &cobra.Command{
		Use:     "stats <name>",
		Aliases: []string{"metrics", "top"},
		Short:   "Show resource usage statistics for a QEMU VM",
		Long: `Show resource usage statistics for a QEMU VM.

Displays CPU usage, memory, disk I/O, and network I/O statistics
from the hospitus daemon.

Examples:
  hospitus qemu stats myvm
  hospitus qemu stats myvm --watch
  hospitus qemu stats myvm --watch --interval 5`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQEMUStats(cmd, args, watch, interval)
		},
	}

	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Continuously display stats")
	cmd.Flags().IntVarP(&interval, "interval", "i", 2, "Refresh interval in seconds (with --watch)")

	return cmd
}

func runQEMUStats(cmd *cobra.Command, args []string, watch bool, interval int) error {
	vmName := args[0]
	if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, vmName, "qemu"); err != nil {
		return err
	}

	if !watch {
		return displayQEMUStats(cmd, vmName)
	}

	if interval < 1 {
		return fmt.Errorf("--interval must be >= 1")
	}

	ctx := cmd.Context()
	for {
		if err := displayQEMUStats(cmd, vmName); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Duration(interval) * time.Second):
		}
	}
}

func displayQEMUStats(cmd *cobra.Command, vmName string) error {
	ctx := cmd.Context()

	metrics, err := cmdutil.APIClient.GetInstanceMetrics(ctx, vmName)
	if err != nil {
		return fmt.Errorf("failed to get metrics for %s: %w", vmName, err)
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)

	fmt.Fprintf(w, "VM:\t%s\n", vmName)
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "RESOURCES\n")
	fmt.Fprintf(w, "  CPU Usage:\t%.1f%%\n", metrics.CPUUsagePercent)
	fmt.Fprintf(w, "  Memory Used:\t%s\n", cmdutil.FormatBytes(metrics.MemoryUsedMB*1024*1024))
	fmt.Fprintf(w, "  Memory Total:\t%s\n", cmdutil.FormatBytes(metrics.MemoryTotalMB*1024*1024))
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "DISK I/O\n")
	fmt.Fprintf(w, "  Read:\t%s\n", cmdutil.FormatBytes(metrics.DiskReadBytes))
	fmt.Fprintf(w, "  Write:\t%s\n", cmdutil.FormatBytes(metrics.DiskWriteBytes))
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "NETWORK\n")
	fmt.Fprintf(w, "  RX:\t%s\n", cmdutil.FormatBytes(metrics.NetRxBytes))
	fmt.Fprintf(w, "  TX:\t%s\n", cmdutil.FormatBytes(metrics.NetTxBytes))

	if metrics.Timestamp != "" {
		fmt.Fprintf(w, "\n")
		fmt.Fprintf(w, "Timestamp:\t%s\n", metrics.Timestamp)
	}

	w.Flush()
	return nil
}
