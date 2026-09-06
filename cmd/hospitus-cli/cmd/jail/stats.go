package jail

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newStatsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "stats <jail-name>",
		Aliases: []string{"metrics", "top"},
		Short:   "Show resource usage statistics for a jail",
		Long: `Display real-time resource usage metrics for a specific jail.

Shows CPU usage, memory consumption, disk I/O, and network traffic.
Requires RACCT to be enabled (kern.racct.enable=1 in /boot/loader.conf).

Examples:
  hospitus jail stats myjail
  hospitus jail stats myjail --output json`,
		Args: cobra.ExactArgs(1),
		RunE: runStats,
	}

	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runStats(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "jail"); err != nil {
		return err
	}
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	// Get metrics from API
	metrics, err := cmdutil.APIClient.GetInstanceMetrics(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get jail stats: %w", err)
	}

	if outputFmt == "json" {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(metrics)
	}

	// Human-readable output
	fmt.Fprintf(cmd.OutOrStdout(), "Resource Statistics for %s\n", name)
	fmt.Fprintf(cmd.OutOrStdout(), "================================\n\n")

	// CPU
	fmt.Fprintf(cmd.OutOrStdout(), "CPU Usage:      %.2f%%\n", metrics.CPUUsagePercent)

	// Memory
	if metrics.MemoryTotalMB > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Memory:         %d MB / %d MB\n", metrics.MemoryUsedMB, metrics.MemoryTotalMB)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Memory Used:    %d MB\n", metrics.MemoryUsedMB)
	}

	// Disk I/O
	fmt.Fprintf(cmd.OutOrStdout(), "Disk Read:      %s\n", cmdutil.FormatBytes(metrics.DiskReadBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "Disk Write:     %s\n", cmdutil.FormatBytes(metrics.DiskWriteBytes))

	// Network
	fmt.Fprintf(cmd.OutOrStdout(), "Network RX:     %s\n", cmdutil.FormatBytes(metrics.NetRxBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "Network TX:     %s\n", cmdutil.FormatBytes(metrics.NetTxBytes))

	fmt.Fprintf(cmd.OutOrStdout(), "\nTimestamp: %s\n", metrics.Timestamp)

	return nil
}

// formatBytes converts bytes to human-readable format
