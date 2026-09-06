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
	format, err := cmdutil.OutputFormatFrom(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()

	filter := client.ListInstancesFilter{Provider: "bhyve"}
	instances, err := cmdutil.APIClient.ListInstances(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to list VMs: %w", err)
	}

	// Convert to slice
	// A null element decodes to a nil pointer, and *inst panics the command.
	// Reported rather than skipped, as jail list and every destroy do: a list
	// quietly missing an entry is the worse of the two.
	if err := cmdutil.CheckInstanceList(instances); err != nil {
		return err
	}

	var vms []datastore.Instance
	for _, inst := range instances {
		vms = append(vms, *inst)
	}

	return cmdutil.PrintInstanceList(cmd.OutOrStdout(), vms, format)
}
