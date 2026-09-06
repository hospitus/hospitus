package secret

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/pkg/manifest"
)

func newGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <scope> <name>",
		Short: "Get a secret value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope := args[0]
			name := args[1]

			store := manifest.NewFileSecretStore("")

			// Lookup, not Get: Get generates on a miss. Listing the scope
			// first and calling Get only for a name just seen was not enough —
			// the file can go between the two, and Get then minted a fresh
			// secret and saved it, answering with a credential that matched
			// nothing else using that name.
			val, err := store.Lookup(scope, name)
			if err != nil {
				if errors.Is(err, manifest.ErrSecretNotFound) {
					return fmt.Errorf("secret %s/%s not found", scope, name)
				}
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), val)
			return nil
		},
	}

	return cmd
}
