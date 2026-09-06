package podman

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newStartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Start a Podman container",
		Args:  cobra.ExactArgs(1),
		RunE:  runStart,
	}
}

func runStart(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "podman"); err != nil {
		return err
	}

	if err := cmdutil.APIClient.StartInstance(ctx, name, cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	fmt.Printf("Container started: %s\n", name)
	return nil
}
