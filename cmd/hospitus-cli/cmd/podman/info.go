package podman

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "info <name>",
		Aliases: []string{"get", "show"},
		Short:   "Show detailed information about a Podman container",
		Args:    cobra.ExactArgs(1),
		RunE:    runInfo,
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
		return fmt.Errorf("failed to get container info: %w", err)
	}

	if instance.Provider != "podman" {
		return fmt.Errorf("%s is not a Podman container (provider: %s)", name, instance.Provider)
	}

	var format cmdutil.OutputFormat
	if outputFmt == "json" {
		format = cmdutil.OutputFormatJSON
	} else {
		format = cmdutil.OutputFormatTable
	}

	return cmdutil.PrintInstance(cmd.OutOrStdout(), instance, format)
}
