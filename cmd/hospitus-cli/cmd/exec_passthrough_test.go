package cmd

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/jail"
	"github.com/hospitus/hospitus/cmd/hospitus-cli/cmd/podman"
)

// TestExecPassesFlagsToTheCommandItRuns guards the promise every exec command's
// usage line makes: "exec <name> <command> [args...]".
//
// Cobra keeps looking for its own flags among the positional arguments unless
// told to stop, so `exec web wget -q URL` failed with "unknown shorthand flag:
// 'q'" — the flag belonged to wget, not to hospitus. jail exec had been told;
// podman exec had not, so the two commands answered the same input differently.
func TestExecPassesFlagsToTheCommandItRuns(t *testing.T) {
	// A command with a shorthand flag hospitus does not define, and one it does:
	// -q is unknown to hospitus, -i is its own interactive flag on jail exec.
	// Both belong to the program being run, and neither may be intercepted.
	argv := []string{"exec", "web", "wget", "-q", "-i", "-O", "/dev/null", "http://127.0.0.1"}
	want := []string{"web", "wget", "-q", "-i", "-O", "/dev/null", "http://127.0.0.1"}

	for _, tc := range []struct {
		provider string
		root     *cobra.Command
	}{
		{"jail", jail.NewJailCommand()},
		{"podman", podman.NewPodmanCommand()},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			exec := findSubcommand(t, tc.root, "exec")

			var got []string
			exec.RunE = func(_ *cobra.Command, args []string) error {
				got = args
				return nil
			}

			tc.root.SetArgs(argv)
			tc.root.SetOut(discard{})
			tc.root.SetErr(discard{})
			if err := tc.root.Execute(); err != nil {
				t.Fatalf("exec rejected the command it was asked to run: %v", err)
			}

			if len(got) != len(want) {
				t.Fatalf("exec received %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("exec received %v, want %v", got, want)
				}
			}
		})
	}
}

// TestExecAliasesPassFlagsToTheCommandTheyRun holds jexec/pexec to the same
// promise. The aliases are rebuilt commands: AddFlagSet copies the flag
// definitions but not interspersed parsing, so `jexec web wget -q URL` failed
// on wget's -q while `hospitus jail exec` with the same argv passed.
func TestExecAliasesPassFlagsToTheCommandTheyRun(t *testing.T) {
	argv := []string{"web", "wget", "-q", "-i", "-O", "/dev/null", "http://127.0.0.1"}
	want := argv

	for _, aliasName := range []string{"jexec", "pexec"} {
		t.Run(aliasName, func(t *testing.T) {
			root := &cobra.Command{Use: "hospitus"}
			RegisterAliases(root)

			alias := findSubcommand(t, root, aliasName)

			var got []string
			alias.RunE = func(_ *cobra.Command, args []string) error {
				got = args
				return nil
			}

			root.SetArgs(append([]string{aliasName}, argv...))
			root.SetOut(discard{})
			root.SetErr(discard{})
			if err := root.Execute(); err != nil {
				t.Fatalf("%s rejected the command it was asked to run: %v", aliasName, err)
			}

			if len(got) != len(want) {
				t.Fatalf("%s received %v, want %v", aliasName, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s received %v, want %v", aliasName, got, want)
				}
			}
		})
	}
}

func findSubcommand(t *testing.T, parent *cobra.Command, name string) *cobra.Command {
	t.Helper()

	for _, sub := range parent.Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	t.Fatalf("%s has no %q subcommand", parent.Name(), name)
	return nil
}

// discard swallows the usage text cobra prints, which is not what is under test.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
