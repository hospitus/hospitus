package podman

import (
	"fmt"
	"os/exec"

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
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, name, "podman"); err != nil {
		return err
	}
	follow, _ := cmd.Flags().GetBool("follow")
	tail, _ := cmd.Flags().GetInt("tail")

	// Check root privileges (skip on macOS where podman machine handles this)
	if err := cmdutil.RequireRoot(true); err != nil {
		return err
	}

	// Get the instance to verify it exists
	instance, err := cmdutil.APIClient.GetInstance(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get container: %w", err)
	}

	// Use the instance name as container name
	containerName := instance.Name

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
