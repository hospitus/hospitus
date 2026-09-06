package podman

import (
	"bufio"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newDestroyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "destroy [<name>...]",
		Aliases: []string{"rm", "delete"},
		Short:   "Destroy one or more Podman containers",
		Args:    cobra.ArbitraryArgs,
		RunE:    runDestroy,
	}

	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation")
	cmd.Flags().BoolP("force", "f", false, "Force destroy (stop if running)")
	cmd.Flags().StringP("pattern", "x", "", "Glob pattern to match container names (e.g., 'test-*', 'dev-*')")

	return cmd
}

func runDestroy(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	force, _ := cmd.Flags().GetBool("force")
	yes, _ := cmd.Flags().GetBool("yes")
	pattern, _ := cmd.Flags().GetString("pattern")

	var containersToDestroy []string

	switch {
	case pattern != "":
		instances, err := cmdutil.APIClient.ListInstances(ctx, client.ListInstancesFilter{
			Provider: "podman",
		})
		if err != nil {
			return fmt.Errorf("failed to list containers: %w", err)
		}
		for _, inst := range instances {
			matched, err := filepath.Match(pattern, inst.Name)
			if err != nil {
				return fmt.Errorf("invalid pattern '%s': %w", pattern, err)
			}
			if !matched {
				matched, err = filepath.Match(pattern, inst.ID)
				if err != nil {
					return fmt.Errorf("invalid pattern '%s': %w", pattern, err)
				}
			}
			if matched {
				containersToDestroy = append(containersToDestroy, inst.Name)
			}
		}
		if len(containersToDestroy) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "No containers matching pattern '%s'\n", pattern)
			return nil
		}
	case len(args) > 0:
		for _, arg := range args {
			resolved, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, arg, "podman")
			if err != nil {
				return fmt.Errorf("cannot resolve container %q: %w", arg, err)
			}
			containersToDestroy = append(containersToDestroy, resolved)
		}
	default:
		return fmt.Errorf("either container name(s) or --pattern/-x is required")
	}

	if len(containersToDestroy) == 1 {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: This will permanently destroy container '%s' and all its data.\n", containersToDestroy[0])
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: This will permanently destroy %d containers and all their data:\n", len(containersToDestroy))
		for _, name := range containersToDestroy {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", name)
		}
	}

	if !yes {
		// Through the command's own streams, and reading a whole line:
		// fmt.Scanln stops at the first space, so "yes please" answered "yes"
		// and an empty line was an unchecked error.
		fmt.Fprint(cmd.OutOrStdout(), "Are you sure? (yes/no): ")
		reader := bufio.NewReader(cmd.InOrStdin())
		response, err := reader.ReadString('\n')
		if err != nil && response == "" {
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
	for _, name := range containersToDestroy {
		if err := cmdutil.APIClient.DeleteInstance(ctx, name, force); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", name, err))
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Container destroyed: %s\n", name)
			destroyed++
		}
	}

	if len(containersToDestroy) > 1 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nSummary: %d/%d containers destroyed\n", destroyed, len(containersToDestroy))
	}

	if len(errors) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "\nErrors:\n")
		for _, e := range errors {
			fmt.Fprintf(cmd.ErrOrStderr(), "  - %s\n", e)
		}
		return fmt.Errorf("%d container(s) failed to destroy", len(errors))
	}

	return nil
}
