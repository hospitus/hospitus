package jail

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func newConsoleCommand() *cobra.Command {
	var shell string

	cmd := &cobra.Command{
		Use:     "console <jail-name>",
		Aliases: []string{"login", "attach"},
		Short:   "Attach to a jail console",
		Long: `Attach to the console of a running jail.

This opens an interactive shell session inside the jail using jexec(8).

NOTE: This command runs locally and requires:
  - Running on the FreeBSD host where the jail is running
  - The jexec(8) command to be available

This command does NOT go through the hospitusd API because interactive
console access requires direct TTY attachment.

Examples:
  hospitus jail console myjail
  hospitus jail console myjail --shell /bin/csh
  jlogin myjail`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsole(cmd, args, shell)
		},
	}

	cmd.Flags().StringVar(&shell, "shell", "/bin/sh", "Shell to use inside the jail")

	return cmd
}

func runConsole(cmd *cobra.Command, args []string, shell string) error {
	jailName := args[0]

	// Check if jexec command exists
	jexecPath, err := exec.LookPath("jexec")
	if err != nil {
		return fmt.Errorf("'jexec' command not found - console requires running on the FreeBSD host")
	}

	fmt.Fprintln(os.Stderr, "Note: Console runs locally via jexec (not through the API)")
	fmt.Fprintf(cmd.OutOrStdout(), "Attaching to jail %s with %s\n", jailName, shell)
	fmt.Fprintln(cmd.OutOrStdout(), "Type 'exit' to detach from the console.")
	fmt.Fprintln(cmd.OutOrStdout(), "")

	// Launch jexec interactively
	jexecCmd := exec.Command(jexecPath, jailName, shell)
	jexecCmd.Stdin = os.Stdin
	jexecCmd.Stdout = os.Stdout
	jexecCmd.Stderr = os.Stderr

	return jexecCmd.Run()
}
