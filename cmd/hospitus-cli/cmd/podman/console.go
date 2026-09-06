package podman

import (
	"fmt"
	"os"
	"os/exec"

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
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "podman"); err != nil {
		return err
	}

	// Check root privileges (skip on macOS where podman machine handles this)
	if err := cmdutil.RequireRoot(true); err != nil {
		return err
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
	podmanCmd := exec.Command("podman", "exec", "-it", containerName, "/bin/sh")
	podmanCmd.Stdin = os.Stdin
	podmanCmd.Stdout = os.Stdout
	podmanCmd.Stderr = os.Stderr

	return podmanCmd.Run()
}
