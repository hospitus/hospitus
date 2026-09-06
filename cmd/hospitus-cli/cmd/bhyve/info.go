package bhyve

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newInfoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "info <name>",
		Aliases: []string{"get", "show"},
		Short:   "Show detailed information about a bhyve VM",
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
		return fmt.Errorf("failed to get VM info: %w", err)
	}

	if instance == nil {
		return fmt.Errorf("VM not found: %s", name)
	}
	if instance.Provider != "bhyve" {
		return fmt.Errorf("%s is not a bhyve VM (provider: %s)", name, instance.Provider)
	}

	// An unknown value is refused rather than quietly treated as "table": a
	// caller asking for --output yaml got a table and no indication that the
	// format it wanted does not exist.
	var format cmdutil.OutputFormat
	switch outputFmt {
	case "json":
		format = cmdutil.OutputFormatJSON
	case "", "table":
		format = cmdutil.OutputFormatTable
	default:
		return fmt.Errorf("unknown --%s value %q: expected \"table\" or \"json\"", cmdutil.FlagOutput, outputFmt)
	}

	return cmdutil.PrintInstance(cmd.OutOrStdout(), instance, format)
}
