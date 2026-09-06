package jail

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newStartCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start <jail-name>",
		Short: "Start a jail",
		Long: `Start a stopped jail.

Examples:
  hospitus jail start myjail
  jstart myjail`,
		Args: cobra.ExactArgs(1),
		RunE: runStart,
	}

	return cmd
}

func runStart(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "jail"); err != nil {
		return err
	}

	if err := cmdutil.APIClient.StartInstance(ctx, name, cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("failed to start jail: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Jail started: %s\n", name)
	return nil
}
