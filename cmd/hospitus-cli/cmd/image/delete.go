package image

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newDeleteCommand() *cobra.Command {
	return &cobra.Command{
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
}

func runDelete(cmd *cobra.Command, args []string) error {
	imageName := args[0]
	ctx := cmd.Context()

	// Call API to delete image (requires daemon privileges)
	if err := cmdutil.APIClient.DeleteImage(ctx, imageName); err != nil {
		return fmt.Errorf("failed to delete image: %w", err)
	}

	fmt.Printf("✓ Image deleted: %s\n", imageName)
	return nil
}
