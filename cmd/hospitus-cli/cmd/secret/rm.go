package secret

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/pkg/manifest"
)

func newRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <scope> <name>",
		Short: "Remove a secret",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope := args[0]
			name := args[1]

			store := manifest.NewFileSecretStore("")
			if err := store.Remove(scope, name); err != nil {
				return err
			}

			fmt.Printf("Secret %s/%s removed.\n", scope, name)
			return nil
		},
	}

	return cmd
}
