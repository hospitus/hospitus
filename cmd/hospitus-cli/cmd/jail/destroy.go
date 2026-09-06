package jail

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
		Use:     "destroy [<jail-name>...]",
		Aliases: []string{"delete", "rm", "remove"},
		Short:   "Destroy one or more jails",
		Long: `Destroy jails and remove all their data.

WARNING: This operation is irreversible. All data will be lost.

Examples:
  # Destroy a single jail (with confirmation prompt)
  hospitus jail destroy myjail

  # Destroy multiple jails
  hospitus jail destroy jail1 jail2 jail3

  # Skip confirmation prompt
  hospitus jail destroy myjail -y
  hospitus jail destroy myjail --yes

  # Destroy jails matching a pattern (glob)
  hospitus jail destroy -x "test-*"
  hospitus jail destroy --pattern "dev-*"

  # Destroy all jails matching pattern without confirmation
  hospitus jail destroy -x "test-*" -y

  # Force destroy even if ZFS dataset is busy
  hospitus jail destroy myjail --force
  hospitus jail destroy myjail -f

  # Combine both: skip confirmation AND force destroy
  hospitus jail destroy myjail -y -f
  hospitus jail destroy myjail --yes --force

  # CBSD compatibility
  jdestroy myjail
  jdestroy myjail -f`,
		Args: cobra.ArbitraryArgs,
		RunE: runDestroy,
	}

	cmdutil.AddForceFlag(cmd)
	cmdutil.AddYesFlag(cmd)
	cmd.Flags().StringP("pattern", "x", "", "Glob pattern to match jail names (e.g., 'test-*', 'dev-*')")

	return cmd
}

func runDestroy(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// --force/-f: Force destroy even if ZFS is busy
	force, _ := cmd.Flags().GetBool(cmdutil.FlagForce)

	// --yes/-y: Skip confirmation prompt
	yes, _ := cmd.Flags().GetBool("yes")

	// --pattern/-x: Glob pattern for matching jail names
	pattern, _ := cmd.Flags().GetString("pattern")

	// Collect jails to destroy
	var jailsToDestroy []string

	switch {
	case pattern != "":
		// List all jails and filter by pattern
		instances, err := cmdutil.APIClient.ListInstances(ctx, client.ListInstancesFilter{
			Provider: "jail",
		})
		if err != nil {
			return fmt.Errorf("failed to list jails: %w", err)
		}

		for _, inst := range instances {
			matched, err := filepath.Match(pattern, inst.ID)
			if err != nil {
				return fmt.Errorf("invalid pattern '%s': %w", pattern, err)
			}
			if matched {
				jailsToDestroy = append(jailsToDestroy, inst.ID)
			}
		}

		if len(jailsToDestroy) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "No jails matching pattern '%s'\n", pattern)
			return nil
		}
	case len(args) > 0:
		// Resolve each name by exact match only: destroy must never act on a
		// jail whose name merely resembles the one asked for.
		for _, arg := range args {
			resolved, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, arg, "jail")
			if err != nil {
				return fmt.Errorf("cannot resolve jail %q: %w", arg, err)
			}
			jailsToDestroy = append(jailsToDestroy, resolved)
		}
	default:
		return fmt.Errorf("either jail name(s) or --pattern/-x is required")
	}

	// Show what will be destroyed
	if len(jailsToDestroy) == 1 {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: This will permanently destroy jail '%s' and all its data.\n", jailsToDestroy[0])
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: This will permanently destroy %d jails and all their data:\n", len(jailsToDestroy))
		for _, name := range jailsToDestroy {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", name)
		}
	}

	// Ask for confirmation unless --yes/-y is specified
	if !yes {
		fmt.Fprint(cmd.OutOrStdout(), "Are you sure? (yes/no): ")
		reader := bufio.NewReader(cmd.InOrStdin())
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

	// Destroy each jail
	var errors []string
	destroyed := 0

	for _, name := range jailsToDestroy {
		if err := cmdutil.APIClient.DeleteInstance(ctx, name, force); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", name, err))
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Jail destroyed: %s\n", name)
			destroyed++
		}
	}

	// Report summary
	if len(jailsToDestroy) > 1 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nSummary: %d/%d jails destroyed\n", destroyed, len(jailsToDestroy))
	}

	// Report any errors
	if len(errors) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "\nErrors:\n")
		for _, e := range errors {
			fmt.Fprintf(cmd.ErrOrStderr(), "  - %s\n", e)
		}
		return fmt.Errorf("%d jail(s) failed to destroy", len(errors))
	}

	return nil
}
