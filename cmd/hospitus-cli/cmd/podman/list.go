package podman

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
		Short:   "List all Podman containers",
		RunE:    runList,
	}

	cmdutil.AddOutputFlag(cmd)
	return cmd
}

func runList(cmd *cobra.Command, args []string) error {
	format, err := cmdutil.OutputFormatFrom(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()

	filter := client.ListInstancesFilter{Provider: providerName}
	instances, err := cmdutil.APIClient.ListInstances(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	// Convert to slice
	// A null element decodes to a nil pointer, and *inst panics the command.
	// Reported rather than skipped, as jail list and every destroy do: a list
	// quietly missing an entry is the worse of the two.
	if err := cmdutil.CheckInstanceList(instances); err != nil {
		return err
	}

	var containers []datastore.Instance
	for _, inst := range instances {
		containers = append(containers, *inst)
	}

	return cmdutil.PrintInstanceList(cmd.OutOrStdout(), containers, format)
}
