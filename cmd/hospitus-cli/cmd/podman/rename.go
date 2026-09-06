package podman

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newRenameCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rename <container-name> <new-name>",
		Short: "Rename a stopped container",
		Long: `Rename a Podman container. The container must be stopped.

Examples:
  hospitus podman rename old-web new-web`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			oldName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), args[0], "podman")
			if err != nil {
				return fmt.Errorf("container not found: %w", err)
			}
			newName := args[1]

			result, err := cmdutil.GetClient().RenameInstance(ctx, oldName, newName)
			if err != nil {
				return fmt.Errorf("failed to rename container: %w", err)
			}

			from, to := oldName, newName
			if v, ok := result["old_name"]; ok && v != "" {
				from = v
			}
			if v, ok := result["new_name"]; ok && v != "" {
				to = v
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Container renamed: %s -> %s\n", from, to)
			return nil
		},
	}
	return cmd
}
