package qemu

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

func newConsoleCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "console <name>",
		Aliases: []string{"login", "attach"},
		Short:   "Attach to a QEMU VM console",
		Long: `Attach to the console of a QEMU VM.

QEMU VMs use VNC for graphical console access. This command prints the VNC
display allocated to the VM; connect to it with any VNC client.

Examples:
  hospitus qemu console myvm`,
		Args: cobra.ExactArgs(1),
		RunE: runConsole,
	}

	return cmd
}

func runConsole(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	vmName := args[0]

	// Get VM info to determine console settings
	instance, err := cmdutil.APIClient.GetInstance(ctx, vmName)
	if err != nil {
		return fmt.Errorf("failed to get VM info: %w", err)
	}

	if instance.Provider != "qemu" {
		return fmt.Errorf("%s is not a QEMU VM (provider: %s)", vmName, instance.Provider)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Console information for VM %s:\n\n", vmName)

	port, ok := vncPort(instance.Spec.ProviderConfig)
	if !ok {
		fmt.Fprintln(out, "No VNC display is recorded for this VM.")
		fmt.Fprintln(out, "The port is assigned when the VM is created; start it and run")
		fmt.Fprintf(out, "  hospitus qemu info %s\n", vmName)
		fmt.Fprintln(out, "to see its current configuration.")
		return nil
	}

	display := port - vncBasePort
	fmt.Fprintln(out, "VNC access:")
	fmt.Fprintf(out, "  Address: localhost:%d (display :%d)\n", port, display)
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Example clients:")
	fmt.Fprintf(out, "  vncviewer localhost:%d\n", display)
	fmt.Fprintf(out, "  remote-viewer vnc://localhost:%d\n", port)
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "The VM must be running for the display to accept connections.")
	fmt.Fprintln(out, "When hospitusd runs on another host, tunnel the port over SSH:")
	fmt.Fprintf(out, "  ssh -L %d:127.0.0.1:%d <hospitusd-host>\n", port, port)

	return nil
}

// vncBasePort is QEMU's first VNC display port, matching the provider's
// allocation scheme (display :N listens on vncBasePort+N).
const vncBasePort = 5900

// vncPort reads the VNC port the provider recorded for the VM. Provider config
// crosses the API as JSON, so a port stored as an int arrives as a float64;
// both forms are accepted. The boolean reports whether a usable port was found,
// so the caller can say so instead of inventing a default that is wrong for
// every VM but the first.
func vncPort(providerConfig map[string]interface{}) (int, bool) {
	raw, ok := providerConfig["vnc_port"]
	if !ok {
		return 0, false
	}

	var port int
	switch v := raw.(type) {
	case int:
		port = v
	case int64:
		port = int(v)
	case float64:
		port = int(v)
	default:
		return 0, false
	}

	if port < vncBasePort || port > 65535 {
		return 0, false
	}
	return port, true
}
