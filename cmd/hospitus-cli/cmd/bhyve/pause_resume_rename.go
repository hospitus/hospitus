package bhyve

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newPauseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <vm-name>",
		Short: "Pause a running VM (SIGSTOP)",
		Long: `Freeze a running bhyve VM by sending SIGSTOP to the bhyve process.

The VM's state is preserved in memory; use 'hospitus bhyve resume' to unfreeze.
Pausing is useful for live inspection or to temporarily reduce host CPU usage.

Examples:
  hospitus bhyve pause myvm`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			vmName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), args[0], "bhyve")
			if err != nil {
				return fmt.Errorf("VM not found: %w", err)
			}

			if err := cmdutil.GetClient().PauseInstance(ctx, vmName); err != nil {
				return fmt.Errorf("failed to pause VM: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "VM %s paused\n", vmName)
			return nil
		},
	}
}

func newResumeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <vm-name>",
		Short: "Resume a paused VM (SIGCONT)",
		Long: `Unfreeze a paused bhyve VM by sending SIGCONT to the bhyve process.

Examples:
  hospitus bhyve resume myvm`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			vmName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), args[0], "bhyve")
			if err != nil {
				return fmt.Errorf("VM not found: %w", err)
			}

			if err := cmdutil.GetClient().ResumeInstance(ctx, vmName); err != nil {
				return fmt.Errorf("failed to resume VM: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "VM %s resumed\n", vmName)
			return nil
		},
	}
}

func newRenameVMCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <vm-name> <new-name>",
		Short: "Rename a stopped VM",
		Long: `Rename a bhyve VM. The VM must be stopped.

The command renames all associated files (ZFS dataset, UEFI vars, state)
and updates the internal datastore so subsequent commands use the new name.

Examples:
  hospitus bhyve rename old-vm new-vm`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			oldName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), args[0], "bhyve")
			if err != nil {
				return fmt.Errorf("VM not found: %w", err)
			}

			result, err := cmdutil.GetClient().RenameInstance(ctx, oldName, args[1])
			if err != nil {
				return fmt.Errorf("failed to rename VM: %w", err)
			}

			// The names we asked for when the daemon does not echo them back:
			// a missing key reads as "", and the message then announced a
			// rename between two blanks.
			from, to := oldName, args[1]
			if v, ok := result["old_name"]; ok && v != "" {
				from = v
			}
			if v, ok := result["new_name"]; ok && v != "" {
				to = v
			}
			fmt.Fprintf(cmd.OutOrStdout(), "VM renamed: %s → %s\n", from, to)
			return nil
		},
	}
}
