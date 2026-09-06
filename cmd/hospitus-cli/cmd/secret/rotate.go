package secret

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/pkg/manifest"
)

func newRotateCommand() *cobra.Command {
	var show bool

	cmd := &cobra.Command{
		Use:   "rotate <scope> <name>",
		Short: "Rotate a secret (generate a new value)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope := args[0]
			name := args[1]

			store := manifest.NewFileSecretStore("")
			newVal, err := store.Rotate(scope, name)
			if err != nil {
				return err
			}

			if show {
				fmt.Printf("Secret %s/%s rotated. New value: %s\n", scope, name, newVal)
			} else {
				fmt.Printf("Secret %s/%s rotated.\n", scope, name)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&show, "show", false, "Print the new secret value in cleartext")

	return cmd
}
