package jail

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newExecCommand() *cobra.Command {
	var (
		user        string
		workingDir  string
		interactive bool
	)

	cmd := &cobra.Command{
		Use:   "exec <jail-name> <command> [args...]",
		Short: "Execute a command in a jail",
		Long: `Execute a command inside a running jail.

By default, commands are executed through the hospitusd API, allowing remote execution.
Use -i for interactive commands that require a TTY (shells, editors, etc).

NOTE: Interactive mode (-i) runs locally via jexec and requires:
  - Running on the FreeBSD host where the jail is running
  - The jexec(8) command to be available

Non-interactive mode works remotely through the API.

NOTE: Interactive programs like vi, vim, nano, top, htop require the -i flag.
Without -i, they will fail with "stdin/stdout must be a terminal".

Examples:
  # Execute a simple command
  hospitus jail exec myjail ps aux

  # Execute a command with arguments
  hospitus jail exec myjail ls -la /var/log

  # Run an interactive shell
  hospitus jail exec -i myjail /bin/sh

  # Edit a file interactively (requires -i flag)
  hospitus jail exec -i myjail vi /etc/rc.conf

  # Run as a specific user
  hospitus jail exec -u www myjail whoami

  # Run in a specific directory
  hospitus jail exec -w /var/www myjail pwd

  # Non-interactive config editing (use sed instead of vi)
  hospitus jail exec myjail sed -i '' 's/old/new/' /etc/config.conf

  # CBSD compatibility
  jexec myjail ps aux`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExec(cmd, args, user, workingDir, interactive)
		},
	}

	cmd.Flags().StringVarP(&user, "user", "u", "", "User to run the command as")
	cmd.Flags().StringVarP(&workingDir, "workdir", "w", "", "Working directory inside the jail")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Run in interactive mode (attach tty)")

	// Don't parse flags after positional args - this allows passing flags like -y to the command
	cmd.Flags().SetInterspersed(false)

	return cmd
}

func runExec(cmd *cobra.Command, args []string, user, workingDir string, interactive bool) error {
	jailName := args[0]
	if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}

	// "hospitus jail exec web -- uname -r" separates the jail's command from
	// hospitus's own flags. SetInterspersed(false) keeps everything after the
	// name, "--" included, so without this the command run was "--" and jexec
	// answered "execvp: --: No such file or directory".
	rest := args[1:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return fmt.Errorf("no command given to run in %s", jailName)
	}

	command := rest[0]
	commandArgs := rest[1:]

	if interactive {
		// Interactive mode requires direct jexec for TTY attachment
		// This only works when running locally on the host
		fmt.Fprintln(os.Stderr, "Note: Interactive mode runs locally via jexec (not through the API)")

		// Check if jexec command exists
		jexecPath, err := exec.LookPath("jexec")
		if err != nil {
			return fmt.Errorf("'jexec' command not found - interactive mode requires running on the FreeBSD host")
		}

		// Build jexec arguments
		jexecArgs := []string{}

		// Add user flag if specified (-U looks up user in jail's passwd, -u uses host passwd)
		if user != "" {
			jexecArgs = append(jexecArgs, "-U", user)
		}

		// Add jail name and command
		jexecArgs = append(jexecArgs, jailName, command)

		// Add command arguments
		jexecArgs = append(jexecArgs, commandArgs...)

		// Create the command
		jexecCmd := exec.Command(jexecPath, jexecArgs...)

		// Set working directory if specified
		if workingDir != "" {
			jexecArgs = []string{}
			if user != "" {
				jexecArgs = append(jexecArgs, "-U", user)
			}
			// Safely single-quote every element so paths/arguments with
			// spaces or shell metacharacters cannot be misinterpreted.
			quoted := make([]string, 0, len(commandArgs)+1)
			quoted = append(quoted, shellQuote(command))
			for _, arg := range commandArgs {
				quoted = append(quoted, shellQuote(arg))
			}
			shellCmd := fmt.Sprintf("cd %s && %s", shellQuote(workingDir), strings.Join(quoted, " "))
			jexecArgs = append(jexecArgs, jailName, "/bin/sh", "-c", shellCmd)
			jexecCmd = exec.Command(jexecPath, jexecArgs...)
		}

		// Interactive mode: attach stdin/stdout/stderr
		jexecCmd.Stdin = os.Stdin
		jexecCmd.Stdout = os.Stdout
		jexecCmd.Stderr = os.Stderr
		return jexecCmd.Run()
	}

	// Non-interactive mode: use the API with streaming for real-time output
	ctx := cmd.Context()

	req := client.ExecRequest{
		Command:    command,
		Args:       commandArgs,
		User:       user,
		WorkingDir: workingDir,
	}

	// Use streaming to display output in real-time
	exitCode, err := cmdutil.APIClient.ExecCommandStream(ctx, jailName, req, os.Stdout, os.Stderr)
	if err != nil {
		return fmt.Errorf("exec failed: %w", err)
	}

	// Exit with the command's exit code
	if exitCode != 0 {
		os.Exit(exitCode)
	}

	return nil
}

// shellQuote wraps a string in single quotes for safe use in a /bin/sh -c
// command, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
