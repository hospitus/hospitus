package bhyve

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newStartCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start <name>",
		Short: "Start a bhyve VM",
		Long: `Start a stopped bhyve virtual machine.

Examples:
  hospitus bhyve start myvm
  bstart myvm`,
		Args: cobra.ExactArgs(1),
		RunE: runStart,
	}

	return cmd
}

func runStart(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "bhyve"); err != nil {
		return err
	}

	if err := cmdutil.APIClient.StartInstance(ctx, name, cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "VM started: %s\n", name)
	return nil
}
