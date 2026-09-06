package podman

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a Podman container",
		Args:  cobra.ExactArgs(1),
		RunE:  runStop,
	}

	cmd.Flags().BoolP("force", "f", false, "Force stop (kill)")
	return cmd
}

func runStop(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "podman"); err != nil {
		return err
	}
	force, _ := cmd.Flags().GetBool("force")

	if err := cmdutil.APIClient.StopInstance(ctx, name, force); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	fmt.Printf("Container stopped: %s\n", name)
	return nil
}
