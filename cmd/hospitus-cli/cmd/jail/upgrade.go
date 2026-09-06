package jail

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newUpgradeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade <jail-name> --release <version>",
		Short: "Upgrade a jail's FreeBSD base system",
		Long: `Upgrade the FreeBSD base system inside a jail to a new release.

The jail must be stopped. This command runs freebsd-update(8) in the
jail root to fetch and install the target release. If pkg(8) is
available inside the jail, installed packages are upgraded too.

This is a long-running operation. The command returns a job ID that
can be polled with:

  hospitus job info <job-id>

Examples:
  # Upgrade jail "web" to FreeBSD 14.3-RELEASE
  hospitus jail upgrade web --release 14.3-RELEASE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			jailName := args[0]
			release, _ := cmd.Flags().GetString("release")
			if release == "" {
				return fmt.Errorf("--release is required (e.g. --release 14.3-RELEASE)")
			}

			resolvedID, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.GetClient(), jailName, "jail")
			if err != nil {
				return fmt.Errorf("jail not found: %w", err)
			}

			result, err := cmdutil.GetClient().UpgradeInstance(ctx, resolvedID, release)
			if err != nil {
				return fmt.Errorf("failed to submit upgrade: %w", err)
			}

			fmt.Printf("Upgrade job submitted.\n")
			fmt.Printf("  Jail:    %s\n", result["instance"])
			fmt.Printf("  Release: %s\n", result["target_release"])
			fmt.Printf("  Job ID:  %s\n", result["job_id"])
			fmt.Printf("\nPoll status with: hospitus job info %s\n", result["job_id"])
			return nil
		},
	}

	cmd.Flags().StringP("release", "r", "", "Target FreeBSD release (e.g. 14.3-RELEASE)")
	_ = cmd.MarkFlagRequired("release")

	return cmd
}
