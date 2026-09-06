package vfkit

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/pkg/provider"
)

func newCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new VM",
		Args:  cobra.ExactArgs(1),
		RunE:  runCreate,
	}

	// Declare only what this provider honors. The shared create-flag helpers
	// offer the full jail/bhyve vocabulary — disks, VNET, bridges, IOPS limits,
	// a bootloader — none of which reaches the framework, and a flag that is
	// accepted and silently dropped is worse than one that is absent.
	cmd.Flags().IntP(cmdutil.FlagCPUs, "c", 1, "Number of CPUs")
	cmd.Flags().Int64P(cmdutil.FlagMemory, "m", 512, "Memory in MB")
	cmd.Flags().String(cmdutil.FlagImage, "", "Base image")
	cmd.Flags().String(cmdutil.FlagArch, "native", "Architecture (native, amd64, arm64)")
	cmd.Flags().String(cmdutil.FlagOSType, "", "OS type (freebsd, linux, windows)")
	cmd.Flags().String(cmdutil.FlagOSVersion, "", "OS version")
	cmd.Flags().String(cmdutil.FlagDescription, "", "Instance description")
	cmd.Flags().Bool(cmdutil.FlagStart, false, "Start the VM once it is created")

	return cmd
}

func runCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	name := args[0]

	cpus, _ := cmd.Flags().GetInt(cmdutil.FlagCPUs)
	memory, _ := cmd.Flags().GetInt64(cmdutil.FlagMemory)
	if err := cmdutil.ValidateCPUs(cpus); err != nil {
		return err
	}
	if err := cmdutil.ValidateMemory(memory); err != nil {
		return err
	}

	image, _ := cmd.Flags().GetString(cmdutil.FlagImage)
	osType, _ := cmd.Flags().GetString(cmdutil.FlagOSType)
	osVersion, _ := cmd.Flags().GetString(cmdutil.FlagOSVersion)
	description, _ := cmd.Flags().GetString(cmdutil.FlagDescription)
	autoStart, _ := cmd.Flags().GetBool(cmdutil.FlagStart)

	// The framework runs guest instructions on the host CPU and has no emulation
	// fallback, so a foreign architecture cannot boot here at all.
	arch, _ := cmd.Flags().GetString(cmdutil.FlagArch)
	// Refused here, where the message can say why: the comment above explains
	// that a foreign architecture cannot boot, and the flag accepted one
	// anyway — the VM was created and then failed to start.
	if arch != "" && arch != "native" && arch != runtime.GOARCH {
		return fmt.Errorf("vfkit runs guest instructions on the host CPU: %s cannot boot on %s", arch, runtime.GOARCH)
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
		Labels:      make(map[string]string),
		Annotations: make(map[string]string),
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Creating VM %s...\n", name)
	instance, err := cmdutil.APIClient.CreateInstance(ctx, client.CreateInstanceRequest{
		Provider: providerName,
		Spec:     spec,
	})
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "VM created: %s (ID: %s)\n", instance.Name, instance.ID)

	if autoStart {
		if err := cmdutil.APIClient.StartInstance(ctx, instance.ID, cmd.OutOrStdout()); err != nil {
			return fmt.Errorf("VM created but failed to start: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "VM started: %s\n", name)
	}

	return nil
}
