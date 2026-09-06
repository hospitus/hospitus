package podman

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newLogsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Show logs for a Podman container",
		Args:  cobra.ExactArgs(1),
		RunE:  runLogs,
	}

	cmd.Flags().BoolP("follow", "f", false, "Follow log output")
	cmd.Flags().IntP("tail", "n", 0, "Number of lines to show from the end (0 for all)")
	return cmd
}

func runLogs(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, providerName); err != nil {
		return err
	}
	follow, _ := cmd.Flags().GetBool("follow")
	tail, _ := cmd.Flags().GetInt("tail")

	// Check root privileges (skip on macOS where podman machine handles this)
	// FreeBSD only. Podman is rootless by design on Linux, and macOS goes
	// through podman machine — demanding root on either refused a command
	// that would have worked.
	if runtime.GOOS == "freebsd" {
		if err := cmdutil.RequireRootStrict(); err != nil {
			return err
		}
	}

	// Get the instance to verify it exists
	instance, err := cmdutil.APIClient.GetInstance(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get container: %w", err)
	}

	// Use the instance name as container name
	containerName := instance.Name

	// Zero means all logs; a negative number means nothing podman understands,
	// and was silently dropped into the same "all" as zero.
	if tail < 0 {
		return fmt.Errorf("--tail must be zero (all logs) or positive, got %d", tail)
	}

	// Build podman logs command
	logsArgs := []string{"logs"}
	if follow {
		logsArgs = append(logsArgs, "-f")
	}
	if tail > 0 {
		logsArgs = append(logsArgs, "--tail", fmt.Sprintf("%d", tail))
	}
	logsArgs = append(logsArgs, containerName)

	// With the context, and through the command's streams: without the first
	// a Ctrl-C left "podman logs -f" running, and without the second the
	// output ignored any redirection the caller set.
	podmanCmd := exec.CommandContext(cmd.Context(), "podman", logsArgs...)
	podmanCmd.Stdout = cmd.OutOrStdout()
	podmanCmd.Stderr = cmd.ErrOrStderr()

	return podmanCmd.Run()
}
