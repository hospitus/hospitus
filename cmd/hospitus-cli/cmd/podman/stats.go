package podman

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newStatsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "stats <name>",
		Aliases: []string{"metrics", "top"},
		Short:   "Show resource usage statistics for a Podman container",
		Long: `Display resource usage metrics for a Podman container.

Shows CPU usage, memory consumption, network I/O, and block I/O.
Requires the container to be running.

Examples:
  hospitus podman stats mycontainer
  hospitus podman stats mycontainer --output json`,
		Args: cobra.ExactArgs(1),
		RunE: runPodmanStats,
	}

	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runPodmanStats(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "podman"); err != nil {
		return err
	}
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	metrics, err := cmdutil.APIClient.GetInstanceMetrics(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get container stats: %w", err)
	}

	if outputFmt == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(metrics)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Resource Statistics for %s\n", name)
	fmt.Fprintf(cmd.OutOrStdout(), "================================\n\n")

	fmt.Fprintf(cmd.OutOrStdout(), "CPU Usage:      %.2f%%\n", metrics.CPUUsagePercent)

	if metrics.MemoryTotalMB > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Memory:         %d MB / %d MB\n", metrics.MemoryUsedMB, metrics.MemoryTotalMB)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Memory Used:    %d MB\n", metrics.MemoryUsedMB)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Network RX:     %s\n", cmdutil.FormatBytes(metrics.NetRxBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "Network TX:     %s\n", cmdutil.FormatBytes(metrics.NetTxBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "Block Read:     %s\n", cmdutil.FormatBytes(metrics.DiskReadBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "Block Write:    %s\n", cmdutil.FormatBytes(metrics.DiskWriteBytes))

	fmt.Fprintf(cmd.OutOrStdout(), "\nTimestamp: %s\n", metrics.Timestamp)
	return nil
}
