package jail

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newCloneCommand() *cobra.Command {
	var (
		snapshot string
		linked   bool
		resetMAC bool
	)

	cmd := &cobra.Command{
		Use:   "clone <source-jail> <new-jail-name>",
		Short: "Clone a jail",
		Long: `Create a clone of an existing jail.

Cloning uses ZFS's copy-on-write capabilities to efficiently create
a new jail based on an existing one. The clone initially shares all
data with the source and only consumes space for differences.

Options:
  --snapshot  Clone from a specific snapshot instead of current state
  --linked    Create a linked clone (shares storage with source)
  --reset-mac Generate new MAC addresses for the clone

Examples:
  # Clone current state of a jail
  hospitus jail clone prod-db test-db

  # Clone from a specific snapshot
  hospitus jail clone prod-db test-db --snapshot before-upgrade

  # Create a linked clone (more space efficient, but depends on source)
  hospitus jail clone template-web web-server-1 --linked`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClone(cmd, args, snapshot, linked, resetMAC)
		},
	}

	cmd.Flags().StringVarP(&snapshot, "snapshot", "s", "", "Clone from specific snapshot")
	cmd.Flags().BoolVar(&linked, "linked", false, "Create a linked clone (shares storage)")
	cmd.Flags().BoolVar(&resetMAC, "reset-mac", true, "Generate new MAC addresses")

	return cmd
}

func runClone(cmd *cobra.Command, args []string, snapshot string, linked, resetMAC bool) error {
	ctx := cmd.Context()
	sourceJail := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, sourceJail, "jail"); err != nil {
		return err
	}
	newJailName := args[1]

	opts := client.CloneOptions{
		Name:     newJailName,
		Linked:   linked,
		ResetMAC: resetMAC,
	}

	var result *client.CloneResult
	var err error

	if snapshot != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Cloning jail %s from snapshot %s to %s...\n",
			sourceJail, snapshot, newJailName)
		result, err = cmdutil.APIClient.CloneFromSnapshot(ctx, sourceJail, snapshot, opts)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Cloning jail %s to %s...\n", sourceJail, newJailName)
		result, err = cmdutil.APIClient.CloneInstance(ctx, sourceJail, opts)
	}

	if err != nil {
		return fmt.Errorf("failed to clone jail: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Clone created: %s\n", result.CloneInstance)
	if result.Message != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", result.Message)
	}

	warnAboutInheritedAddress(ctx, cmd, result.CloneInstance)

	return nil
}

// warnAboutInheritedAddress reports that a clone carries the static address of
// the jail it came from.
//
// A clone copies the whole spec, the address included, so starting it while
// the source runs puts two jails on one address. Neither complains: they both
// come up, traffic goes to whichever answered the last ARP request, and the
// only trace is in the kernel log —
//
//	arp: 58:9c:fc:10:0a:e0 is using my IP address 10.50.0.20 on epair3b!
//
// The address is left alone rather than reassigned. A clone destined for
// another host wants to keep it, and which address it should take here is not
// something the CLI can guess.
func warnAboutInheritedAddress(ctx context.Context, cmd *cobra.Command, cloneName string) {
	inst, err := cmdutil.APIClient.GetInstance(ctx, cloneName)
	if err != nil {
		// The clone was made. Not being able to describe it is not a failure
		// of the clone, and saying so would only obscure the success above.
		return
	}

	for i := range inst.Spec.Networks {
		n := &inst.Spec.Networks[i]
		if n.IPv4 == "" || strings.EqualFold(n.IPv4, "dhcp") {
			continue
		}
		fmt.Fprintf(cmd.ErrOrStderr(),
			"Warning: %s inherits the address %s from its source.\n"+
				"  Running both at once conflicts. To give the clone another address:\n"+
				"    hospitus jail network remove %s <interface>\n"+
				"    hospitus jail network add %s --bridge <bridge> --ipv4 <address>\n",
			cloneName, n.IPv4, cloneName, cloneName)
		return
	}
}
