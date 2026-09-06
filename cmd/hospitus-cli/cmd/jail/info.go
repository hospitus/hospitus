package jail

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "info <jail-name>",
		Aliases: []string{"get", "show"},
		Short:   "Show detailed information about a jail",
		Long: `Display detailed information about a specific jail.

Examples:
  hospitus jail info myjail
  hospitus jail info myjail --output json
  jget myjail`,
		Args: cobra.ExactArgs(1),
		RunE: runInfo,
	}

	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runInfo(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	instance, err := cmdutil.APIClient.GetInstance(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get jail info: %w", err)
	}

	// Verify it's a jail
	if instance.Provider != "jail" {
		return fmt.Errorf("%s is not a jail (provider: %s)", name, instance.Provider)
	}

	var format cmdutil.OutputFormat
	if outputFmt == "json" {
		format = cmdutil.OutputFormatJSON
	} else {
		format = cmdutil.OutputFormatTable
	}

	return cmdutil.PrintInstance(cmd.OutOrStdout(), instance, format)
}
