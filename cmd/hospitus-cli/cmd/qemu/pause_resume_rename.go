package qemu

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newPauseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "pause <vm-name>",
		Short: "Pause a running QEMU VM",
		Long: `Suspend a running QEMU VM via QMP.

The VM's state is preserved in memory; use 'hospitus qemu resume' to continue.

Examples:
  hospitus qemu pause myvm`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			vmName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), args[0], "qemu")
			if err != nil {
				return fmt.Errorf("VM not found: %w", err)
			}

			if err := cmdutil.GetClient().PauseInstance(ctx, vmName); err != nil {
				return fmt.Errorf("failed to pause VM: %w", err)
			}

			fmt.Printf("VM %s paused\n", vmName)
			return nil
		},
	}
}

func newResumeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <vm-name>",
		Short: "Resume a paused QEMU VM",
		Long: `Continue a paused QEMU VM via QMP.

Examples:
  hospitus qemu resume myvm`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			vmName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), args[0], "qemu")
			if err != nil {
				return fmt.Errorf("VM not found: %w", err)
			}

			if err := cmdutil.GetClient().ResumeInstance(ctx, vmName); err != nil {
				return fmt.Errorf("failed to resume VM: %w", err)
			}

			fmt.Printf("VM %s resumed\n", vmName)
			return nil
		},
	}
}

func newRenameCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <vm-name> <new-name>",
		Short: "Rename a stopped QEMU VM",
		Long: `Rename a QEMU VM. The VM must be stopped.

Examples:
  hospitus qemu rename old-vm new-vm`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			oldName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), args[0], "qemu")
			if err != nil {
				return fmt.Errorf("VM not found: %w", err)
			}

			result, err := cmdutil.GetClient().RenameInstance(ctx, oldName, args[1])
			if err != nil {
				return fmt.Errorf("failed to rename VM: %w", err)
			}

			fmt.Printf("VM renamed: %s → %s\n", result["old_name"], result["new_name"])
			return nil
		},
	}
}
