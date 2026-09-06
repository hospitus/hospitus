package qemu

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newDestroyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "destroy [<name>...]",
		Aliases: []string{"delete", "rm", "remove"},
		Short:   "Destroy one or more QEMU VMs",
		Args:    cobra.ArbitraryArgs,
		RunE:    runDestroy,
	}

	cmdutil.AddForceFlag(cmd)
	cmdutil.AddYesFlag(cmd)
	cmd.Flags().StringP("pattern", "x", "", "Glob pattern to match VM names (e.g., 'test-*', 'dev-*')")

	return cmd
}

func runDestroy(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	force, _ := cmd.Flags().GetBool(cmdutil.FlagForce)
	yes, _ := cmd.Flags().GetBool("yes")
	pattern, _ := cmd.Flags().GetString("pattern")

	var vmsToDestroy []string

	switch {
	case pattern != "":
		instances, err := cmdutil.APIClient.ListInstances(ctx, client.ListInstancesFilter{
			Provider: "qemu",
		})
		if err != nil {
			return fmt.Errorf("failed to list VMs: %w", err)
		}
		for _, inst := range instances {
			matched, err := filepath.Match(pattern, inst.ID)
			if err != nil {
				return fmt.Errorf("invalid pattern '%s': %w", pattern, err)
			}
			if matched {
				vmsToDestroy = append(vmsToDestroy, inst.ID)
			}
		}
		if len(vmsToDestroy) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "No VMs matching pattern '%s'\n", pattern)
			return nil
		}
	case len(args) > 0:
		for _, arg := range args {
			resolved, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, arg, "qemu")
			if err != nil {
				return fmt.Errorf("cannot resolve VM %q: %w", arg, err)
			}
			vmsToDestroy = append(vmsToDestroy, resolved)
		}
	default:
		return fmt.Errorf("either VM name(s) or --pattern/-x is required")
	}

	if len(vmsToDestroy) == 1 {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: This will permanently destroy VM '%s' and all its data.\n", vmsToDestroy[0])
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: This will permanently destroy %d VMs and all their data:\n", len(vmsToDestroy))
		for _, name := range vmsToDestroy {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", name)
		}
	}

	if !yes {
		fmt.Fprint(cmd.OutOrStdout(), "Are you sure? (yes/no): ")
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read confirmation: %w", err)
		}
		response = strings.ToLower(strings.TrimSpace(response))
		if response != "yes" && response != "y" {
			fmt.Fprintln(cmd.OutOrStdout(), "Operation canceled")
			return nil
		}
	}

	var errors []string
	destroyed := 0
	for _, name := range vmsToDestroy {
		if err := cmdutil.APIClient.DeleteInstance(ctx, name, force); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", name, err))
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "VM destroyed: %s\n", name)
			destroyed++
		}
	}

	if len(vmsToDestroy) > 1 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nSummary: %d/%d VMs destroyed\n", destroyed, len(vmsToDestroy))
	}

	if len(errors) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "\nErrors:\n")
		for _, e := range errors {
			fmt.Fprintf(cmd.ErrOrStderr(), "  - %s\n", e)
		}
		return fmt.Errorf("%d VM(s) failed to destroy", len(errors))
	}

	return nil
}
