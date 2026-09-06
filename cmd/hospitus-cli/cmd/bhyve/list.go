package bhyve

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
		Short:   "List all bhyve VMs",
		RunE:    runList,
	}

	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	outputFmt, _ := cmd.Flags().GetString(cmdutil.FlagOutput)

	filter := client.ListInstancesFilter{Provider: "bhyve"}
	instances, err := cmdutil.APIClient.ListInstances(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to list VMs: %w", err)
	}

	// Convert to slice
	var vms []datastore.Instance
	for _, inst := range instances {
		vms = append(vms, *inst)
	}

	var format cmdutil.OutputFormat
	if outputFmt == "json" {
		format = cmdutil.OutputFormatJSON
	} else {
		format = cmdutil.OutputFormatTable
	}

	return cmdutil.PrintInstanceList(cmd.OutOrStdout(), vms, format)
}
