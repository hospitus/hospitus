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

	// This runs the podman on *this* machine. Against a daemon elsewhere it
	// would act on a container of the same name here, or report none at all,
	// while appearing to address the remote one.
	//
	// The locality test reads the configured URL, so a loopback address that
	// is really a tunnel still passes; it catches the ordinary mistake of
	// pointing --url at another host, not a deliberate tunnel.
	if !cmdutil.DaemonIsLocal() {
		return fmt.Errorf("this command runs podman on this machine, and the daemon is not local\n" +
			"  → run it on the daemon's host, or use 'hospitus podman exec', which goes through the API")
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
