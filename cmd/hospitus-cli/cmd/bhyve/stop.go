package bhyve

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a bhyve VM",
		Args:  cobra.ExactArgs(1),
		RunE:  runStop,
	}

	cmdutil.AddForceFlag(cmd)
	return cmd
}

func runStop(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "bhyve"); err != nil {
		return err
	}
	force, _ := cmd.Flags().GetBool(cmdutil.FlagForce)

	if err := cmdutil.APIClient.StopInstance(ctx, name, force); err != nil {
		return fmt.Errorf("failed to stop VM: %w", err)
	}

	fmt.Printf("VM stopped: %s\n", name)
	return nil
}
