package bhyve

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newCloneVMCommand() *cobra.Command {
	var (
		snapshot string
		linked   bool
		resetMAC bool
	)

	cmd := &cobra.Command{
		Use:   "clone <source-vm> <new-vm-name>",
		Short: "Clone a bhyve VM",
		Long: `Create a clone of an existing bhyve VM using ZFS copy-on-write.

The clone initially shares all data with the source and only consumes
space for differences.

Options:
  --snapshot  Clone from a specific snapshot instead of the current state
  --linked    Create a linked clone (shares ZFS origin with source)
  --reset-mac Generate new MAC addresses for the cloned NIC(s)
              (enabled by default; use --reset-mac=false to keep source MACs)

Examples:
  # Clone current state
  hospitus bhyve clone prod-vm test-vm

  # Clone from a named snapshot
  hospitus bhyve clone prod-vm test-vm --snapshot before-upgrade

  # Space-efficient linked clone
  hospitus bhyve clone base-template web-server-1 --linked`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			sourceVM, err := cmdutil.ResolveInstanceName(ctx, cmdutil.GetClient(), args[0], "bhyve")
			if err != nil {
				return fmt.Errorf("VM not found: %w", err)
			}
			newName := args[1]

			opts := client.CloneOptions{
				Name:     newName,
				Linked:   linked,
				ResetMAC: resetMAC,
			}

			var result *client.CloneResult
			if snapshot != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Cloning VM %s from snapshot %s to %s...\n",
					sourceVM, snapshot, newName)
				result, err = cmdutil.GetClient().CloneFromSnapshot(ctx, sourceVM, snapshot, opts)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Cloning VM %s to %s...\n", sourceVM, newName)
				result, err = cmdutil.GetClient().CloneInstance(ctx, sourceVM, opts)
			}

			if err != nil {
				return fmt.Errorf("failed to clone VM: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Clone created: %s\n", result.CloneInstance)
			if result.Message != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", result.Message)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&snapshot, "snapshot", "s", "", "Clone from a specific snapshot")
	cmd.Flags().BoolVar(&linked, "linked", false, "Create a linked clone (shares ZFS origin)")
	cmd.Flags().BoolVar(&resetMAC, "reset-mac", true, "Generate new MAC addresses for cloned NICs (default: enabled; use --reset-mac=false to keep source MACs)")

	return cmd
}
