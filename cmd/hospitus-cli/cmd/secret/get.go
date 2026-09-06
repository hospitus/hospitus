package secret

import (
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

			// Get generates the secret if it is missing; for a read command we
			// only want to return an existing value, so check existence first.
			names, err := store.List(scope)
			if err != nil {
				return err
			}
			exists := false
			for _, n := range names {
				if n == name {
					exists = true
					break
				}
			}
			if !exists {
				return fmt.Errorf("secret %s/%s not found", scope, name)
			}

			val, err := store.Get(scope, name)
			if err != nil {
				return err
			}

			fmt.Println(val)
			return nil
		},
	}

	return cmd
}
