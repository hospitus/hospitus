package qemu

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/pkg/provider"
)

func newCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new QEMU VM",
		Long: `Create a new QEMU virtual machine.

Examples:
  # Create a VM with an image and a 20 GB root disk
  hospitus qemu create myvm --image ubuntu-24.04 --cpus 2 --memory 2048 --disk 20

  # Boot an existing physical/external disk directly
  hospitus qemu create myvm --disk physical:/dev/ada2`,
		Args: cobra.ExactArgs(1),
		RunE: runCreate,
	}

	// qemu reads only this subset of the shared create flags. Registering
	// exactly the subset means an unwired flag (--ip, --bridge, --pull, the
	// rctl limits, …) is refused at parse time instead of being accepted and
	// silently discarded.
	cmdutil.AddCoreResourceFlags(cmd)
	cmdutil.AddOSFlags(cmd)
	cmdutil.AddBootAutoStartFlags(cmd)
	cmdutil.AddBaseCreateFlags(cmd)
	return cmd
}

func runCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	// Same as bhyve: the flag comes from the shared create flag set, nothing
	// reads it here, and the API request has no cloud_init field to carry it.
	// The QEMU provider builds a seed ISO when the spec asks for one, which a
	// manifest does and this never did.
	if ci, _ := cmd.Flags().GetString(cmdutil.FlagCloudInit); ci != "" {
		return fmt.Errorf("--cloud-init is not wired up for the command line\n" +
			"  → put the same configuration in a manifest's [cloud_init] section\n" +
			"    and apply it with 'hospitus apply'")
	}

	cpus, _ := cmd.Flags().GetInt(cmdutil.FlagCPUs)
	memory, _ := cmd.Flags().GetInt64(cmdutil.FlagMemory)

	// Validate resource flags
	if err := cmdutil.ValidateCPUs(cpus); err != nil {
		return err
	}
	if err := cmdutil.ValidateMemory(memory); err != nil {
		return err
	}

	image, _ := cmd.Flags().GetString(cmdutil.FlagImage)
	osType, _ := cmd.Flags().GetString(cmdutil.FlagOSType)
	osVersion, _ := cmd.Flags().GetString(cmdutil.FlagOSVersion)
	arch, _ := cmd.Flags().GetString(cmdutil.FlagArch)
	description, _ := cmd.Flags().GetString(cmdutil.FlagDescription)
	autoStart, _ := cmd.Flags().GetBool(cmdutil.FlagStart)

	// Disk specification. The QEMU provider is single-disk: either a sized
	// qcow2 root image (`<size>`) or a physical device (`physical:/dev/xxx`).
	diskSpecs, _ := cmd.Flags().GetStringSlice(cmdutil.FlagDisk)
	disks, err := cmdutil.ParseDiskSpecs(diskSpecs)
	if err != nil {
		return err
	}
	if len(disks) > 1 {
		return fmt.Errorf("the qemu provider supports a single disk; got %d", len(disks))
	}

	spec := provider.InstanceSpec{
		Name:        name,
		Description: description,
		CPUs:        cpus,
		MemoryMB:    memory,
		Image:       image,
		OSType:      osType,
		OSVersion:   osVersion,
		Arch:        arch,
		Disks:       disks,
		Labels:      make(map[string]string),
		Annotations: make(map[string]string),
	}

	req := client.CreateInstanceRequest{
		Provider: "qemu",
		Spec:     spec,
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Creating QEMU VM %s...\n", name)
	instance, err := cmdutil.APIClient.CreateInstance(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "VM created: %s (ID: %s)\n", instance.Name, instance.ID)

	// Record boot auto-start, as bhyve creation does. The flags are declared on
	// this command, and without this nothing reads them: a VM created with
	// --auto-start would neither appear in "hospitus qemu autostart list" nor come
	// up with the host.
	if boot, _ := cmd.Flags().GetBool(cmdutil.FlagBootAutoStart); boot {
		priority, _ := cmd.Flags().GetInt(cmdutil.FlagBootAutoStartPriority)
		delay, _ := cmd.Flags().GetInt(cmdutil.FlagBootAutoStartDelay)
		if _, err := cmdutil.APIClient.SetAutoStart(ctx, "qemu", instance.ID, provider.AutoStartConfig{
			Enabled:  true,
			Priority: priority,
			DelayMS:  delay,
		}); err != nil {
			return fmt.Errorf("VM created but auto-start could not be configured: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  auto-start: priority=%d, delay=%dms\n", priority, delay)
	}

	if autoStart {
		fmt.Fprintf(cmd.OutOrStdout(), "Starting VM %s...\n", name)
		if err := cmdutil.APIClient.StartInstance(ctx, instance.ID, cmd.OutOrStdout()); err != nil {
			return fmt.Errorf("VM created but failed to start: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "VM started: %s\n", name)
	}

	return nil
}
