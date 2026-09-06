package jail

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newRestartCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart <jail-name>",
		Short: "Restart a jail",
		Long: `Restart a running jail (stop then start).

Examples:
  hospitus jail restart myjail
  jrestart myjail`,
		Args: cobra.ExactArgs(1),
		RunE: runRestart,
	}

	return cmd
}

func runRestart(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "jail"); err != nil {
		return err
	}

	if err := cmdutil.APIClient.RestartInstance(ctx, name); err != nil {
		return fmt.Errorf("failed to restart jail: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Jail restarted: %s\n", name)
	return nil
}
