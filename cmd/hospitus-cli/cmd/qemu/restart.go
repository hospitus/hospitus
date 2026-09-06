package qemu

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newRestartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <name>",
		Short: "Restart a QEMU VM",
		Args:  cobra.ExactArgs(1),
		RunE:  runRestart,
	}
}

func runRestart(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "qemu"); err != nil {
		return err
	}

	if err := cmdutil.APIClient.RestartInstance(ctx, name); err != nil {
		return fmt.Errorf("failed to restart VM: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "VM restarted: %s\n", name)
	return nil
}
