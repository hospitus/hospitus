package secret

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/pkg/manifest"
)

// printScopes writes each scope's secrets, and says so when there are none.
//
// A scope directory can outlive the secrets it held, so having scopes is not
// the same as having something to list: "hospitus secret list" printed nothing at
// all and exited 0, which reads as a command that failed.
func printScopes(out io.Writer, store *manifest.FileSecretStore, scopes []string) {
	listed := false
	for _, scope := range scopes {
		secrets, err := store.List(scope)
		if err != nil {
			fmt.Fprintf(out, "Error listing scope %s: %v\n", scope, err)
			continue
		}
		if len(secrets) > 0 {
			listed = true
			fmt.Fprintf(out, "Scope: %s\n", scope)
			for _, s := range secrets {
				fmt.Fprintf(out, "  - %s\n", s)
			}
		}
	}
	if !listed {
		fmt.Fprintln(out, "No secrets found.")
	}
}

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [scope]",
		Short: "List all secrets",
		Long: `List all persistent secrets. If a scope is provided, only secrets in that scope are listed.
Common scopes are workload or stack names.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store := manifest.NewFileSecretStore("")

			var scopes []string
			if len(args) > 0 {
				scopes = append(scopes, args[0])
			} else {
				// Ask the store where it resolved. DefaultSecretsDir is the
				// daemon's directory, and an unprivileged caller keeps its
				// secrets elsewhere — reading the constant reported "No
				// secrets found" for secrets that "list <scope>" then printed.
				entries, err := os.ReadDir(store.BaseDir())
				if err != nil {
					if os.IsNotExist(err) {
						fmt.Fprintln(out, "No secrets found.")
						return nil
					}
					return err
				}
				for _, e := range entries {
					if e.IsDir() {
						scopes = append(scopes, e.Name())
					}
				}
			}

			if len(scopes) == 0 {
				fmt.Fprintln(out, "No secrets found.")
				return nil
			}

			printScopes(out, store, scopes)
			return nil
		},
	}

	return cmd
}
