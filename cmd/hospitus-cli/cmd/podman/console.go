package podman

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newConsoleCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "console <name>",
		Aliases: []string{"login", "attach"},
		Short:   "Attach to a Podman container console",
		Long: `Attach to a Podman container's console for interactive access.

This opens an interactive shell inside the container using 'podman exec -it'.`,
		Args: cobra.ExactArgs(1),
		RunE: runConsole,
	}
}

func runConsole(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, providerName); err != nil {
		return err
	}

	// Check root privileges (skip on macOS where podman machine handles this)
	// FreeBSD only. Podman is rootless by design on Linux, and macOS goes
	// through podman machine — demanding root on either refused a command
	// that would have worked.
	if runtime.GOOS == "freebsd" {
		if err := cmdutil.RequireRootStrict(); err != nil {
			return err
		}
	}

	// Get the instance to verify it exists and is running
	instance, err := cmdutil.APIClient.GetInstance(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get container: %w", err)
	}

	if instance.State != "running" {
		return fmt.Errorf("container is not running (state: %s)", instance.State)
	}

	// Use the instance name as container name
	containerName := instance.Name

	// Use podman exec directly for interactive shell
	// "--" before the name: podman reads a leading dash as one of its own
	// options, so a container called "-it" or "--rm" changed the command
	// rather than being its target.
	// With the command's context: on Ctrl-C the exec'd shell used to be left
	// running with the CLI gone from under it.
	podmanCmd := exec.CommandContext(cmd.Context(), "podman", "exec", "-it", "--", containerName, "/bin/sh")
	podmanCmd.Stdin = os.Stdin
	podmanCmd.Stdout = os.Stdout
	podmanCmd.Stderr = os.Stderr

	return podmanCmd.Run()
}
