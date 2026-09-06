package podman

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
)

func newCloneCommand() *cobra.Command {
	var (
		snapshot string
		linked   bool
	)

	cmd := &cobra.Command{
		Use:   "clone <source-container> <new-name>",
		Short: "Clone a container",
		Long: `Create a clone of an existing Podman container.

Cloning commits the source container to a temporary image, then creates
a new container from that image. The source container can be running or stopped.

Examples:
  hospitus podman clone myapp myapp-staging
  hospitus podman clone myapp myapp-test --snapshot before-upgrade
  hospitus podman clone myapp myapp-dev --linked`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPodmanClone(cmd, args, snapshot, linked)
		},
	}

	cmd.Flags().StringVarP(&snapshot, "snapshot", "s", "", "Clone from specific snapshot")
	cmd.Flags().BoolVar(&linked, "linked", false, "Create a linked clone")

	return cmd
}

func runPodmanClone(cmd *cobra.Command, args []string, snapshot string, linked bool) error {
	ctx := cmd.Context()
	sourceName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, sourceName, "podman"); err != nil {
		return err
	}
	cloneName := args[1]

	opts := client.CloneOptions{
		Name:   cloneName,
		Linked: linked,
	}

	var result *client.CloneResult
	var err error

	if snapshot != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Cloning container %s from snapshot %s to %s...\n",
			sourceName, snapshot, cloneName)
		result, err = cmdutil.APIClient.CloneFromSnapshot(ctx, sourceName, snapshot, opts)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Cloning container %s to %s...\n", sourceName, cloneName)
		result, err = cmdutil.APIClient.CloneInstance(ctx, sourceName, opts)
	}

	if err != nil {
		return fmt.Errorf("failed to clone container: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Clone created: %s\n", result.CloneInstance)
	if result.Message != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", result.Message)
	}

	return nil
}
