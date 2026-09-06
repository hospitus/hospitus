package podman

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newExecCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <name> <command> [args...]",
		Short: "Execute a command in a Podman container",
		Long: `Execute a command in a Podman container and print its output.

This runs a single non-interactive command. For an interactive shell,
use 'hospitus podman console <name>'.`,
		Args: cobra.MinimumNArgs(2),
		RunE: runExec,
	}

	// Everything after the container name belongs to the command being run.
	// Without this, `exec web wget -q URL` hands -q to hospitus, which rejects it
	// as an unknown shorthand flag — and the usage line above promises
	// otherwise. `jail exec` has always done this.
	cmd.Flags().SetInterspersed(false)

	return cmd
}

func runExec(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "podman"); err != nil {
		return err
	}

	// "hospitus podman exec web -- nginx -t" separates the container's command
	// from hospitus's own flags, and SetInterspersed(false) keeps the "--" in
	// args. Taken literally it becomes the command to run.
	rest := args[1:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return fmt.Errorf("no command given to run in %s", name)
	}

	command := rest[0]
	cmdArgs := rest[1:]

	req := client.ExecRequest{
		Command: command,
		Args:    cmdArgs,
	}

	result, err := cmdutil.APIClient.ExecCommand(ctx, name, req)
	if err != nil {
		return fmt.Errorf("failed to execute command: %w", err)
	}

	fmt.Fprint(os.Stdout, result.Stdout)
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	// Propagate the remote command's exact exit status so scripts can react to
	// it, mirroring 'hospitus jail exec'.
	if result.ExitCode != 0 {
		os.Exit(result.ExitCode)
	}
	return nil
}
