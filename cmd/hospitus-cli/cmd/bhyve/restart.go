package bhyve

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newRestartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <name>",
		Short: "Restart a bhyve VM",
		Args:  cobra.ExactArgs(1),
		RunE:  runRestart,
	}
}

func runRestart(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "bhyve"); err != nil {
		return err
	}

	if err := cmdutil.APIClient.RestartInstance(ctx, name); err != nil {
		return fmt.Errorf("failed to restart VM: %w", err)
	}

	fmt.Printf("VM restarted: %s\n", name)
	return nil
}
