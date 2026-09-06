package jail

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <jail-name>",
		Short: "Stop a jail",
		Long: `Stop a running jail.

Examples:
  # Graceful stop
  hospitus jail stop myjail

  # Force stop (kill immediately)
  hospitus jail stop myjail --force
  hospitus jail stop myjail -f

  # CBSD compatibility
  jstop myjail
  jstop myjail -f`,
		Args: cobra.ExactArgs(1),
		RunE: runStop,
	}

	cmdutil.AddForceFlag(cmd)

	return cmd
}

func runStop(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "jail"); err != nil {
		return err
	}
	force, _ := cmd.Flags().GetBool(cmdutil.FlagForce)

	if err := cmdutil.APIClient.StopInstance(ctx, name, force); err != nil {
		return fmt.Errorf("failed to stop jail: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Jail stopped: %s\n", name)
	return nil
}
