package bhyve

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func newConsoleCommand() *cobra.Command {
	var showDevice bool

	cmd := &cobra.Command{
		Use:     "console <name>",
		Aliases: []string{"login", "attach"},
		Short:   "Attach to a bhyve VM console",
		Long: `Attach to the serial console of a bhyve VM.

bhyve uses null modem (nmdm) devices for serial console access.
This command launches 'cu' to connect to the VM's console.

To disconnect from the console, type: ~.

Examples:
  hospitus bhyve console myvm
  hospitus bhyve console myvm --device  # Show device path only`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsole(cmd, args, showDevice)
		},
	}

	cmd.Flags().BoolVar(&showDevice, "device", false, "Show console device path instead of attaching")

	return cmd
}

func runConsole(cmd *cobra.Command, args []string, showDevice bool) error {
	vmName := args[0]

	// Console device convention: /dev/nmdm-<vmname>B for host side
	device := fmt.Sprintf("/dev/nmdm-%sB", vmName)

	if showDevice {
		fmt.Fprintln(cmd.OutOrStdout(), device)
		return nil
	}

	// Check if device exists
	if _, err := os.Stat(device); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("console device %s not found - VM may not be running or was not started with console support", device)
		}
		return fmt.Errorf("failed to stat console device %s: %w", device, err)
	}

	// Check if cu command exists
	cuPath, err := exec.LookPath("cu")
	if err != nil {
		return fmt.Errorf("'cu' command not found - install it with: pkg install cu")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Connecting to %s console at %s\n", vmName, device)
	fmt.Fprintln(cmd.OutOrStdout(), "To disconnect, type: ~.")
	fmt.Fprintln(cmd.OutOrStdout(), "")

	// Launch cu interactively
	cuCmd := exec.Command(cuPath, "-l", device)
	cuCmd.Stdin = os.Stdin
	cuCmd.Stdout = os.Stdout
	cuCmd.Stderr = os.Stderr

	return cuCmd.Run()
}
