package podman

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/pkg/provider"
)

func newCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new Podman container",
		Long: `Create a new Podman container from an OCI image.

The container is created but not started. Use 'hospitus podman start <name>'
to start it after creation, or use --start to start immediately.

Examples:
  hospitus podman create nginx-web --image nginx:latest
  hospitus podman create app --image myapp:1.0 --port 8080:80 --env APP_ENV=prod
  hospitus podman create db --image postgres:15 --env POSTGRES_PASSWORD=secret --start`,
		Args: cobra.ExactArgs(1),
		RunE: runPodmanCreate,
	}

	cmd.Flags().String(cmdutil.FlagImage, "", "Container image (required)")
	cmd.Flags().String(cmdutil.FlagDescription, "", "Container description")
	cmd.Flags().StringArrayP("port", "p", nil, "Port mapping host:container (can be repeated)")
	cmd.Flags().StringArrayP("env", "e", nil, "Environment variable KEY=VALUE (can be repeated)")
	cmd.Flags().StringArrayP("volume", "v", nil, "Volume mount host:container[:options] (can be repeated)")
	cmd.Flags().StringArray("label", nil, "Label KEY=VALUE (can be repeated)")
	cmd.Flags().StringArray("cmd", nil, "Override container command (repeatable args)")
	cmd.Flags().Bool("start", false, "Start container immediately after creation")
	// The podman provider implements auto-start, but the only way to reach it
	// was the API: the flags existed for jails and VMs, not for containers.
	cmdutil.AddBootAutoStartFlags(cmd)

	_ = cmd.MarkFlagRequired(cmdutil.FlagImage)

	return cmd
}

func runPodmanCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	image, _ := cmd.Flags().GetString(cmdutil.FlagImage)
	description, _ := cmd.Flags().GetString(cmdutil.FlagDescription)
	ports, _ := cmd.Flags().GetStringArray("port")
	envVars, _ := cmd.Flags().GetStringArray("env")
	volumes, _ := cmd.Flags().GetStringArray("volume")
	labels, _ := cmd.Flags().GetStringArray("label")
	cmdArgs, _ := cmd.Flags().GetStringArray("cmd")
	startAfter, _ := cmd.Flags().GetBool("start")
	bootAutoStart, _ := cmd.Flags().GetBool(cmdutil.FlagBootAutoStart)
	bootPriority, _ := cmd.Flags().GetInt(cmdutil.FlagBootAutoStartPriority)
	bootDelay, _ := cmd.Flags().GetInt(cmdutil.FlagBootAutoStartDelay)

	// Build port_forwards from --port flags
	var portForwards []map[string]interface{}
	for _, p := range ports {
		// Parse "hostPort:containerPort[/proto]"
		parts := strings.SplitN(p, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid port mapping %q: expected host:container", p)
		}
		protocol := "tcp"
		containerParts := strings.SplitN(parts[1], "/", 2)
		if len(containerParts) == 2 {
			protocol = containerParts[1]
		}
		hostPort, err := strconv.Atoi(parts[0])
		if err != nil || hostPort < 1 || hostPort > 65535 {
			return fmt.Errorf("invalid port mapping %q: host port must be an integer in 1-65535", p)
		}
		containerPort, err := strconv.Atoi(containerParts[0])
		if err != nil || containerPort < 1 || containerPort > 65535 {
			return fmt.Errorf("invalid port mapping %q: container port must be an integer in 1-65535", p)
		}
		portForwards = append(portForwards, map[string]interface{}{
			"host":      hostPort,
			"container": containerPort,
			"protocol":  protocol,
		})
	}

	// Build environment map
	envMap := make(map[string]interface{})
	for _, e := range envVars {
		kv := strings.SplitN(e, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("invalid env var %q: expected KEY=VALUE", e)
		}
		envMap[kv[0]] = kv[1]
	}

	// Build label map
	labelMap := make(map[string]string)
	for _, l := range labels {
		kv := strings.SplitN(l, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("invalid label %q: expected KEY=VALUE", l)
		}
		labelMap[kv[0]] = kv[1]
	}

	// Build provider config
	providerConfig := map[string]interface{}{}
	if len(portForwards) > 0 {
		providerConfig["port_forwards"] = portForwards
	}
	if len(volumes) > 0 {
		volIfaces := make([]interface{}, len(volumes))
		for i, v := range volumes {
			volIfaces[i] = v
		}
		providerConfig["volumes"] = volIfaces
	}
	if len(envMap) > 0 {
		providerConfig["environment"] = envMap
	}
	if len(cmdArgs) > 0 {
		cmdIfaces := make([]interface{}, len(cmdArgs))
		for i, a := range cmdArgs {
			cmdIfaces[i] = a
		}
		providerConfig["command"] = cmdIfaces
	}

	if bootAutoStart {
		providerConfig["autostart"] = true
		providerConfig["autostart_priority"] = bootPriority
		providerConfig["autostart_delay"] = bootDelay
	}

	spec := provider.InstanceSpec{
		Name:           name,
		Description:    description,
		Image:          image,
		Labels:         labelMap,
		Annotations:    make(map[string]string),
		ProviderConfig: providerConfig,
	}

	req := client.CreateInstanceRequest{
		Provider: "podman",
		Spec:     spec,
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Creating Podman container %s (image: %s)...\n", name, image)
	instance, err := cmdutil.APIClient.CreateInstance(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Container created: %s (ID: %s)\n", instance.Name, instance.ID)

	if startAfter {
		fmt.Fprintf(cmd.OutOrStdout(), "Starting container %s...\n", name)
		if err := cmdutil.APIClient.StartInstance(ctx, instance.ID, cmd.OutOrStdout()); err != nil {
			return fmt.Errorf("container created but failed to start: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Container started: %s\n", name)
	}

	return nil
}
