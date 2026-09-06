package jail

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newRenameCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rename <jail-name> <new-name>",
		Short: "Rename a stopped jail",
		Long: `Rename a jail. The jail must be stopped.

The command renames the ZFS dataset, updates the jail.conf file, and
updates the internal state so that subsequent commands refer to the
jail by its new name.

Examples:
  hospitus jail rename old-web new-web`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			oldName := args[0]
			newName := args[1]

			resolvedID, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), oldName, "jail")
			if err != nil {
				return fmt.Errorf("jail not found: %w", err)
			}

			result, err := cmdutil.GetClient().RenameInstance(ctx, resolvedID, newName)
			if err != nil {
				return fmt.Errorf("failed to rename jail: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Jail renamed: %s → %s\n", result["old_name"], result["new_name"])
			return nil
		},
	}
	return cmd
}
