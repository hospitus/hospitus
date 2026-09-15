package secret

import (
	"github.com/spf13/cobra"
)

func NewSecretCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage persistent secrets for manifests",
		Long: `Manage persistent secrets used in manifest templates via the secret "name" function.
Secrets are stored securely on the host and persist across manifest applies.`,
	}

	cmd.AddCommand(newListCommand())
	cmd.AddCommand(newGetCommand())
	cmd.AddCommand(newRemoveCommand())
	cmd.AddCommand(newRotateCommand())

	return cmd
}
