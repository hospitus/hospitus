package image

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "delete <image>",
		Aliases: []string{"rm", "remove"},
		Short:   "Delete a downloaded image",
		Long: `Delete a downloaded base system image.

Examples:
  hospitus image delete 14.1-RELEASE-amd64.txz
  hospitus image rm 15.0-STABLE-amd64.txz`,
		Args: cobra.ExactArgs(1),
		RunE: runDelete,
	}

	cmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func runDelete(cmd *cobra.Command, args []string) error {
	imageName := args[0]
	ctx := cmd.Context()

	// Asked for, like a backup deletion: an image is a multi-gigabyte download
	// and this took it away on a typo, with no way back but fetching it again.
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes {
		fmt.Fprintf(cmd.OutOrStdout(), "Delete image %s? [y/N] ", imageName)
		var confirm string
		fmt.Fscanln(cmd.InOrStdin(), &confirm) //nolint:errcheck
		if confirm != "y" && confirm != "Y" {
			fmt.Fprintln(cmd.OutOrStdout(), "Canceled.")
			return nil
		}
	}

	// Call API to delete image (requires daemon privileges)
	if err := cmdutil.APIClient.DeleteImage(ctx, imageName); err != nil {
		return fmt.Errorf("failed to delete image: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "✓ Image deleted: %s\n", imageName)
	return nil
}
