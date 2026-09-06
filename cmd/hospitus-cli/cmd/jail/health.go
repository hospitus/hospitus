package jail

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newHealthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "health <jail-name>",
		Aliases: []string{"check"},
		Short:   "Check health status of a jail",
		Long: `Perform a health check on a specific jail.

Checks performed:
- Jail is running (via jls)
- ZFS dataset exists
- Network interface is up (for VNET jails)
- Command execution inside jail works

Examples:
  hospitus jail health myjail
  hospitus jail health myjail --output json`,
		Args: cobra.ExactArgs(1),
		RunE: runHealth,
	}

	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runHealth(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "jail"); err != nil {
		return err
	}
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	// Get health from API
	health, err := cmdutil.APIClient.GetInstanceHealth(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get jail health: %w", err)
	}

	if outputFmt == "json" {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(health)
	}

	// Human-readable output
	fmt.Fprintf(cmd.OutOrStdout(), "Health Check for %s\n", name)
	fmt.Fprintf(cmd.OutOrStdout(), "================================\n\n")

	// Status with color-like formatting
	statusIcon := getStatusIcon(health.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s %s\n", statusIcon, health.Status)
	if health.Message != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Message: %s\n", health.Message)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\n")

	// Individual checks
	if len(health.Checks) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Checks:\n")
		for _, check := range health.Checks {
			icon := getStatusIcon(check.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "  %s %-15s %s\n", icon, check.Name, check.Message)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\n")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Timestamp: %s\n", health.Timestamp)

	return nil
}

func getStatusIcon(status string) string {
	switch status {
	case "healthy":
		return "[OK]"
	case "unhealthy":
		return "[FAIL]"
	case "degraded":
		return "[WARN]"
	default:
		return "[?]"
	}
}
