package bhyve

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newCheckpointCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "checkpoint",
		Short: "Manage bhyve VM checkpoints",
		Long: `Create, restore, list, and delete bhyve checkpoints.

A checkpoint freezes a running VM's memory state to disk so it can be
restored later. Unlike a snapshot, a checkpoint captures the CPU and
device state and is used for live migration or suspend/resume.

Examples:
  hospitus bhyve checkpoint list myvm
  hospitus bhyve checkpoint create myvm before-upgrade
  hospitus bhyve checkpoint restore myvm before-upgrade
  hospitus bhyve checkpoint delete myvm before-upgrade`,
	}

	cmd.AddCommand(newCheckpointListCommand())
	cmd.AddCommand(newCheckpointCreateCommand())
	cmd.AddCommand(newCheckpointRestoreCommand())
	cmd.AddCommand(newCheckpointDeleteCommand())
	return cmd
}

func newCheckpointListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list <vm-name>",
		Short: "List checkpoints",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			vmName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), args[0], "bhyve")
			if err != nil {
				return fmt.Errorf("VM not found: %w", err)
			}

			checkpoints, err := cmdutil.GetClient().ListCheckpoints(ctx, vmName)
			if err != nil {
				return fmt.Errorf("failed to list checkpoints: %w", err)
			}

			if len(checkpoints) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No checkpoints found.")
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%-30s  %s\n", "NAME", "CREATED AT")
			for _, cp := range checkpoints {
				fmt.Fprintf(cmd.OutOrStdout(), "%-30s  %s\n", cp.Name, cp.CreatedAt.Format("2006-01-02 15:04:05"))
			}
			return nil
		},
	}
}

func newCheckpointCreateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "create <vm-name> <checkpoint-name>",
		Short: "Create a checkpoint",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			vmName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), args[0], "bhyve")
			if err != nil {
				return fmt.Errorf("VM not found: %w", err)
			}

			if err := cmdutil.GetClient().CreateCheckpoint(ctx, vmName, args[1]); err != nil {
				return fmt.Errorf("failed to create checkpoint: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Checkpoint %q created for VM %s\n", args[1], vmName)
			return nil
		},
	}
}

func newCheckpointRestoreCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <vm-name> <checkpoint-name>",
		Short: "Restore from a checkpoint",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			vmName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), args[0], "bhyve")
			if err != nil {
				return fmt.Errorf("VM not found: %w", err)
			}

			if err := cmdutil.GetClient().RestoreCheckpoint(ctx, vmName, args[1]); err != nil {
				return fmt.Errorf("failed to restore checkpoint: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "VM %s restored from checkpoint %q\n", vmName, args[1])
			return nil
		},
	}
}

func newCheckpointDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <vm-name> <checkpoint-name>",
		Aliases: []string{"rm"},
		Short:   "Delete a checkpoint",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			vmName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), args[0], "bhyve")
			if err != nil {
				return fmt.Errorf("VM not found: %w", err)
			}

			if err := cmdutil.GetClient().DeleteCheckpoint(ctx, vmName, args[1]); err != nil {
				return fmt.Errorf("failed to delete checkpoint: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Checkpoint %q deleted from VM %s\n", args[1], vmName)
			return nil
		},
	}
}
