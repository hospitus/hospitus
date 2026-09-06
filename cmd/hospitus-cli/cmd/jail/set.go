package jail

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/pkg/provider"
)

func newSetCommand() *cobra.Command {
	var (
		cpus                  int
		memoryMB              int64
		description           string
		allowRawSockets       bool
		allowSysvipc          bool
		allowMlock            bool
		allowReservedPorts    bool
		allowMount            bool
		persist               bool
		bootAutostart         bool
		bootPriority          int
		setAllowRawSockets    bool
		setAllowSysvipc       bool
		setAllowMlock         bool
		setAllowReservedPorts bool
		setAllowMount         bool
		setPersist            bool
		setBootAutostart      bool
	)

	cmd := &cobra.Command{
		Use:   "set <jail-name>",
		Short: "Modify jail parameters",
		Long: `Modify parameters of an existing jail.

Some parameters require the jail to be restarted to take effect.
Parameters like CPUs, memory, and description are stored but may require
a restart to apply the new values.

Examples:
  # Change CPU and memory limits
  hospitus jail set myjail --cpus 4 --memory 2048

  # Enable raw sockets (for ping/traceroute)
  hospitus jail set myjail --allow-raw-sockets

  # Enable auto-start on boot
  hospitus jail set myjail --auto-start --auto-start-priority 10

  # Update description
  hospitus jail set myjail --description "Production web server"

Note: Some changes require a jail restart to take effect.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Track which flags were set
			setAllowRawSockets = cmd.Flags().Changed("allow-raw-sockets")
			setAllowSysvipc = cmd.Flags().Changed("allow-sysvipc")
			setAllowMlock = cmd.Flags().Changed("allow-mlock")
			setAllowReservedPorts = cmd.Flags().Changed("allow-reserved-ports")
			setAllowMount = cmd.Flags().Changed("allow-mount")
			setPersist = cmd.Flags().Changed("persist")
			setBootAutostart = cmd.Flags().Changed(cmdutil.FlagBootAutoStart)

			return runSet(cmd, args, cpus, memoryMB, description,
				allowRawSockets, allowSysvipc, allowMlock, allowReservedPorts, allowMount,
				persist, bootAutostart, bootPriority,
				setAllowRawSockets, setAllowSysvipc, setAllowMlock, setAllowReservedPorts,
				setAllowMount, setPersist, setBootAutostart)
		},
	}

	// Resource flags
	cmd.Flags().IntVar(&cpus, "cpus", 0, "Number of CPUs")
	cmd.Flags().Int64Var(&memoryMB, "memory", 0, "Memory in MB")
	cmd.Flags().StringVar(&description, "description", "", "Jail description")

	// Security flags
	cmd.Flags().BoolVar(&allowRawSockets, "allow-raw-sockets", false, "Allow raw sockets (ping/traceroute)")
	cmd.Flags().BoolVar(&allowSysvipc, "allow-sysvipc", false, "Allow System V IPC")
	cmd.Flags().BoolVar(&allowMlock, "allow-mlock", false, "Allow memory locking")
	cmd.Flags().BoolVar(&allowReservedPorts, "allow-reserved-ports", false, "Allow binding to ports < 1024")
	cmd.Flags().BoolVar(&allowMount, "allow-mount", false, "Allow filesystem mounting")
	cmd.Flags().BoolVar(&persist, "persist", false, "Keep jail running even with no processes")

	cmd.Flags().BoolVar(&bootAutostart, cmdutil.FlagBootAutoStart, false, "Start this jail when the host boots")
	cmd.Flags().IntVar(&bootPriority, cmdutil.FlagBootAutoStartPriority, -1, "Boot priority (0-100, lower starts first)")

	return cmd
}

func runSet(cmd *cobra.Command, args []string,
	cpus int, memoryMB int64, description string,
	allowRawSockets, allowSysvipc, allowMlock, allowReservedPorts, allowMount bool,
	persist, bootAutostart bool, bootPriority int,
	setAllowRawSockets, setAllowSysvipc, setAllowMlock, setAllowReservedPorts bool,
	setAllowMount, setPersist, setBootAutostart bool,
) error {
	ctx := cmd.Context()
	jailName := args[0]
	if err := cmdutil.RequireInstanceOf(ctx, cmdutil.APIClient, jailName, "jail"); err != nil {
		return err
	}

	// Get current instance
	instance, err := cmdutil.APIClient.GetInstance(ctx, jailName)
	if err != nil {
		return fmt.Errorf("failed to get jail info: %w", err)
	}

	// Build update request
	req := client.UpdateInstanceRequest{
		Spec:           &provider.InstanceSpec{},
		ProviderConfig: make(map[string]interface{}),
	}

	hasChanges := false

	// Update spec fields
	if cpus > 0 {
		req.Spec.CPUs = cpus
		hasChanges = true
	} else {
		req.Spec.CPUs = instance.Spec.CPUs
	}

	if memoryMB > 0 {
		req.Spec.MemoryMB = memoryMB
		hasChanges = true
	} else {
		req.Spec.MemoryMB = instance.Spec.MemoryMB
	}

	if description != "" {
		req.Spec.Description = description
		hasChanges = true
	} else {
		req.Spec.Description = instance.Spec.Description
	}

	// Copy existing spec values
	req.Spec.Name = instance.Spec.Name
	req.Spec.Networks = instance.Spec.Networks
	req.Spec.Image = instance.Spec.Image
	req.Spec.OSType = instance.Spec.OSType
	req.Spec.OSVersion = instance.Spec.OSVersion
	req.Spec.Labels = instance.Spec.Labels
	req.Spec.Annotations = instance.Spec.Annotations

	// Update provider config for jail-specific parameters
	if setAllowRawSockets {
		req.ProviderConfig["allow.raw_sockets"] = allowRawSockets
		hasChanges = true
	}
	if setAllowSysvipc {
		req.ProviderConfig["allow.sysvipc"] = allowSysvipc
		hasChanges = true
	}
	if setAllowMlock {
		req.ProviderConfig["allow.mlock"] = allowMlock
		hasChanges = true
	}
	if setAllowReservedPorts {
		req.ProviderConfig["allow.reserved_ports"] = allowReservedPorts
		hasChanges = true
	}
	if setAllowMount {
		req.ProviderConfig["allow.mount"] = allowMount
		hasChanges = true
	}
	if setPersist {
		req.ProviderConfig["persist"] = persist
		hasChanges = true
	}
	// Auto-start does not travel in the provider config. UpdateInstance copies
	// those keys into the instance handle's metadata in the datastore, while
	// boot-time start asks the provider — ListAutoStartInstances reads the
	// provider's own state. A setting written to one and read from the other
	// never arrives, and this command printed "updated successfully" anyway.
	//
	// It goes through the endpoint `bhyve autostart` already uses, which calls
	// the provider's SetAutoStart.
	autostartRequested := setBootAutostart || bootPriority >= 0
	// The daemon accepts 0-100. Bounded on the value the caller actually
	// supplied: bootPriority defaults to -1 as the "not given" sentinel, so a
	// plain "< 0" test would reject every invocation that leaves it alone.
	if cmd.Flags().Changed(cmdutil.FlagBootAutoStartPriority) && (bootPriority < 0 || bootPriority > 100) {
		return fmt.Errorf("--%s must be between 0 and 100, got %d", cmdutil.FlagBootAutoStartPriority, bootPriority)
	}

	if autostartRequested {
		// The current configuration first, so a caller who names only
		// --boot-priority keeps the jail's Enabled state. Building the config
		// from the flag's zero value turned auto-start off for anyone who
		// only meant to reorder it.
		cfg := provider.AutoStartConfig{}
		current, err := cmdutil.APIClient.GetAutoStart(ctx, "jail", jailName)
		if err != nil {
			// Not swallowed: falling through with a zero config would set
			// Enabled=false, which is exactly the bug this block exists to
			// prevent — only now on a transport failure rather than a flag
			// default.
			return fmt.Errorf("failed to read the current auto-start configuration: %w", err)
		}
		if current != nil {
			cfg = current.AutoStart
		}
		if setBootAutostart {
			cfg.Enabled = bootAutostart
		}
		if bootPriority >= 0 {
			cfg.Priority = bootPriority
		}
		if _, err := cmdutil.APIClient.SetAutoStart(ctx, "jail", jailName, cfg); err != nil {
			return fmt.Errorf("failed to set auto-start: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Jail %s auto-start: enabled=%v priority=%d\n",
			jailName, cfg.Enabled, cfg.Priority)
	}

	if !hasChanges {
		if autostartRequested {
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), "No changes specified. Use --help to see available options.")
		return nil
	}

	// Call API to update
	_, err = cmdutil.APIClient.UpdateInstance(ctx, jailName, req)
	if err != nil {
		return fmt.Errorf("failed to update jail: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Jail %s updated successfully\n", jailName)

	// Check if restart is needed for jail-specific params
	if setAllowRawSockets || setAllowSysvipc || setAllowMlock || setAllowReservedPorts || setAllowMount || setPersist {
		fmt.Fprintln(cmd.OutOrStdout(), "Note: Some changes may require a jail restart to take effect.")
	}

	return nil
}
