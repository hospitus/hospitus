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
	format, err := cmdutil.OutputFormatFrom(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()

	filter := client.ListInstancesFilter{Provider: "jail"}
	instances, err := cmdutil.APIClient.ListInstances(ctx, filter)
	if err != nil {
		return fmt.Errorf("failed to list jails: %w", err)
	}

	// A null element in the daemon's JSON array decodes to a nil pointer, and
	// dereferencing it panicked the whole command. Reported rather than
	// skipped: a list quietly missing an entry is the worse of the two.
	if err := cmdutil.CheckInstanceList(instances); err != nil {
		return err
	}

	var jails []datastore.Instance
	for _, inst := range instances {
		jails = append(jails, *inst)
	}

	return cmdutil.PrintInstanceList(cmd.OutOrStdout(), jails, format)
}
