package jail

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/internal/datastore"
)

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all jails",
		Long: `List all jails managed by Hospitus.

Examples:
  hospitus jail list
  hospitus jail list --output json
  jls  # Alias`,
		RunE: runList,
	}

	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	filter := client.ListInstancesFilter{Provider: "jail"}
	instances, err := cmdutil.APIClient.ListInstances(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to list jails: %w", err)
	}

	// Convert to slice
	var jails []datastore.Instance
	for _, inst := range instances {
		jails = append(jails, *inst)
	}

	var format cmdutil.OutputFormat
	if outputFmt == "json" {
		format = cmdutil.OutputFormatJSON
	} else {
		format = cmdutil.OutputFormatTable
	}

	return cmdutil.PrintInstanceList(cmd.OutOrStdout(), jails, format)
}
