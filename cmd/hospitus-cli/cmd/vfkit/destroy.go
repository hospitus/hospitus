package vfkit

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newDestroyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "destroy <name>...",
		Aliases: []string{"delete", "rm", "remove"},
		Short:   "Destroy one or more VMs",
		Args:    cobra.MinimumNArgs(1),
		RunE:    runDestroy,
	}

	cmdutil.AddForceFlag(cmd)
	cmdutil.AddYesFlag(cmd)
	return cmd
}

func runDestroy(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	force, _ := cmd.Flags().GetBool(cmdutil.FlagForce)
	yes, _ := cmd.Flags().GetBool("yes")

	// Resolve every name before destroying any of them: a typo should stop the
	// command, not take out the instances that happened to be listed first.
	names := make([]string, 0, len(args))
	for _, arg := range args {
		resolved, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, arg, providerName)
		if err != nil {
			return fmt.Errorf("cannot resolve VM %q: %w", arg, err)
		}
		names = append(names, resolved)
	}

	if len(names) == 1 {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: This will permanently destroy VM '%s' and all its data.\n", names[0])
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: This will permanently destroy %d VMs and all their data:\n", len(names))
		for _, name := range names {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", name)
		}
	}

	if !yes && !cmdutil.AskYesNo("Are you sure?") {
		fmt.Fprintln(cmd.OutOrStdout(), "Operation canceled")
		return nil
	}

	var failed []string
	for _, name := range names {
		if err := cmdutil.APIClient.DeleteInstance(ctx, name, force); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "VM destroyed: %s\n", name)
	}

	if len(failed) > 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "\nErrors:")
		for _, e := range failed {
			fmt.Fprintf(cmd.ErrOrStderr(), "  - %s\n", e)
		}
		return fmt.Errorf("%d VM(s) failed to destroy", len(failed))
	}

	return nil
}
