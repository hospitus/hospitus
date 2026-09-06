package podman

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newHealthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "health <container-name>",
		Aliases: []string{"check"},
		Short:   "Check health status of a container",
		Long: `Perform a health check on a Podman container.

Checks performed:
- Container exists and is running
- Container's built-in healthcheck status (if configured)

Examples:
  hospitus podman health mycontainer
  hospitus podman health mycontainer --output json`,
		Args: cobra.ExactArgs(1),
		RunE: runPodmanHealth,
	}

	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runPodmanHealth(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "podman"); err != nil {
		return err
	}
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	health, err := cmdutil.APIClient.GetInstanceHealth(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get container health: %w", err)
	}

	// The unhealthy verdict applies to both output formats: it used to sit
	// after this branch, so "--output json" always exited zero — and JSON is
	// exactly what a script parses.
	unhealthy := strings.EqualFold(health.Status, "unhealthy")

	if outputFmt == "json" {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(health); err != nil {
			return err
		}
		if unhealthy {
			return fmt.Errorf("container %s is unhealthy", name)
		}
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Health Check for %s\n", name)
	fmt.Fprintf(cmd.OutOrStdout(), "================================\n\n")

	statusIcon := getStatusIcon(health.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s %s\n", statusIcon, health.Status)
	if health.Message != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Message: %s\n", health.Message)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\n")

	if len(health.Checks) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Checks:\n")
		for _, check := range health.Checks {
			icon := getStatusIcon(check.Status)
			fmt.Fprintf(cmd.OutOrStdout(), "  %s %-20s %s\n", icon, check.Name, check.Message)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\n")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Timestamp: %s\n", health.Timestamp)

	// A failing health check is reported in the exit status too: a script
	// running "hospitus podman health web" got zero whatever the container's
	// state, and had to parse the text to find out.
	if unhealthy {
		return fmt.Errorf("container %s is unhealthy", name)
	}

	return nil
}

func getStatusIcon(status string) string {
	// Lower-cased first, as the unhealthy verdict above is: the daemon's
	// wording is not guaranteed, and "Unhealthy" drew the unknown icon beside
	// an error that said the container was unhealthy.
	switch strings.ToLower(status) {
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
