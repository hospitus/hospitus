package bhyve

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/pkg/provider"
)

// autounattendXML is the Windows Setup autounattend.xml that injects
// LabConfig registry keys to skip TPM 2.0, Secure Boot, and RAM checks.
//
// Why is this needed? bhyve provides TPM 2.0 via swtpm, but the CRB
// cancellation register is not implemented. The Windows 11 installer probes
// this register and reports "This PC doesn't meet Windows 11 requirements"
// even though swtpm is running and the TPM is functional post-install.
//
// This XML is picked up automatically by Windows Setup from any attached
// CD/DVD media. It only bypasses the installer's hardware check — the
// running OS still has full access to the TPM.
const autounattendXML = `<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend">
  <settings pass="windowsPE">
    <component name="Microsoft-Windows-Setup"
               processorArchitecture="amd64"
               publicKeyToken="31bf3856ad364e35"
               language="neutral"
               versionScope="nonSxS"
               xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State"
               xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
      <RunSynchronous>
        <RunSynchronousCommand wcm:action="add">
          <Order>1</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassTPMCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add">
          <Order>2</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassSecureBootCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
        <RunSynchronousCommand wcm:action="add">
          <Order>3</Order>
          <Path>cmd /c reg add HKLM\SYSTEM\Setup\LabConfig /v BypassRAMCheck /t REG_DWORD /d 1 /f</Path>
        </RunSynchronousCommand>
      </RunSynchronous>
    </component>
  </settings>
</unattend>
`

func newWindowsPrepCommand() *cobra.Command {
	var (
		windowsISO string
		outputISO  string
		skipAttach bool
	)

	cmd := &cobra.Command{
		Use:   "windows-prep <vm-name>",
		Short: "Prepare a bhyve VM for Windows 11 installation",
		Long: `Prepare a bhyve VM for a Windows 11 installation by automatically
generating a bypass ISO and attaching both ISOs to the VM.

bhyve provides TPM 2.0 via swtpm, but its CRB cancellation register is
not implemented. The Windows 11 installer detects this and shows:

  "This PC doesn't meet Windows 11 requirements"

This command creates a small companion ISO containing an autounattend.xml
that adds the LabConfig registry keys before the hardware check runs.
Windows Setup scans all attached media automatically — no remastering of
the Windows ISO is needed.

On AMD hosts, the VM should also be created from a manifest carrying
ignore_msr = true under [provider_overrides.bhyve], to prevent a BSOD
(PHASE0_EXCEPTION 0x78) caused by Windows probing unimplemented AMD MSRs
during phase 0 initialization.

Requirements:
  - sysutils/cdrtools (mkisofs) or sysutils/xorriso must be installed
  - The VM must be stopped before running this command

Examples:
  # Basic usage (VM must be stopped first)
  hospitus bhyve windows-prep win11 --windows-iso ~/Win11_25H2_French_x64_v2.iso

  # Generate bypass ISO only, attach manually later
  hospitus bhyve windows-prep win11 --windows-iso ~/Win11.iso --skip-attach

  # Custom bypass ISO output path
  hospitus bhyve windows-prep win11 --windows-iso ~/Win11.iso --output /tmp/bypass.iso`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]
			if err := cmdutil.RequireInstanceOf(cmd.Context(), cmdutil.APIClient, vmName, "bhyve"); err != nil {
				return err
			}

			if windowsISO == "" {
				return fmt.Errorf("--windows-iso is required")
			}

			// Resolve absolute path early so the user gets a clear error.
			absWindowsISO, err := filepath.Abs(windowsISO)
			if err != nil {
				return fmt.Errorf("invalid --windows-iso path: %w", err)
			}
			if _, err := os.Stat(absWindowsISO); err != nil {
				return fmt.Errorf("no Windows ISO at %s", absWindowsISO)
			}

			// Locate the ISO tool (prefer mkisofs, fall back to xorriso).
			isoTool, isoArgs, err := findISOTool()
			if err != nil {
				return err
			}

			// Default bypass ISO output path. Use a private (0700) temp
			// directory so the generated ISO cannot be pre-created or
			// tampered with via a predictable path in the shared temp dir.
			if outputISO == "" {
				tmpDir, err := os.MkdirTemp("", "hospitus-win11-*")
				if err != nil {
					return fmt.Errorf("create temp dir for bypass ISO: %w", err)
				}
				outputISO = filepath.Join(tmpDir, fmt.Sprintf("hospitus-%s-bypass.iso", vmName))
			}
			absOutputISO, err := filepath.Abs(outputISO)
			if err != nil {
				return fmt.Errorf("invalid --output path: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Generating Windows 11 bypass ISO...\n")

			if err := generateBypassISO(absOutputISO, isoTool, isoArgs); err != nil {
				return fmt.Errorf("failed to generate bypass ISO: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Bypass ISO created: %s\n", absOutputISO)

			if skipAttach {
				fmt.Fprintln(cmd.OutOrStdout())
				printManualAttachInstructions(cmd, vmName, absWindowsISO, absOutputISO)
				return nil
			}

			ctx := cmd.Context()
			resolvedName, err := cmdutil.ResolveInstanceNameStrict(ctx, cmdutil.APIClient, vmName, "bhyve")
			if err != nil {
				return fmt.Errorf("VM %q not found: %w", vmName, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Attaching Windows ISO as bootable cdrom0...\n")
			if err := cmdutil.APIClient.InsertMedia(ctx, resolvedName, provider.MediaSpec{
				Type:     provider.MediaTypeCDROM,
				Path:     absWindowsISO,
				ReadOnly: true,
				Bootable: true,
				DeviceID: "cdrom0",
			}); err != nil {
				return fmt.Errorf("failed to attach Windows ISO: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Attaching bypass ISO as cdrom1...\n")
			if err := cmdutil.APIClient.InsertMedia(ctx, resolvedName, provider.MediaSpec{
				Type:     provider.MediaTypeCDROM,
				Path:     absOutputISO,
				ReadOnly: true,
				Bootable: false,
				DeviceID: "cdrom1",
			}); err != nil {
				return fmt.Errorf("failed to attach bypass ISO: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "✓ Both ISOs attached. Next steps:")
			fmt.Fprintf(cmd.OutOrStdout(), "  doas hospitus bhyve start %s\n", vmName)
			fmt.Fprintf(cmd.OutOrStdout(), "  hospitus bhyve vnc %s\n", vmName)
			fmt.Fprintln(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), "After installation, eject both ISOs before rebooting:")
			fmt.Fprintf(cmd.OutOrStdout(), "  hospitus bhyve media detach %s --device cdrom0\n", vmName)
			fmt.Fprintf(cmd.OutOrStdout(), "  hospitus bhyve media detach %s --device cdrom1\n", vmName)

			return nil
		},
	}

	cmd.Flags().StringVar(&windowsISO, "windows-iso", "", "Path to the Windows 11 ISO image (required)")
	cmd.Flags().StringVar(&outputISO, "output", "", "Output path for the generated bypass ISO (default: a private temporary directory)")
	cmd.Flags().BoolVar(&skipAttach, "skip-attach", false, "Generate the bypass ISO but do not attach it to the VM")
	_ = cmd.MarkFlagRequired("windows-iso")

	return cmd
}

// findISOTool returns the path and base args for mkisofs or xorriso.
// mkisofs (sysutils/cdrtools) is preferred; xorriso (sysutils/xorriso)
// is accepted as a drop-in replacement via its mkisofs-compatible mode.
func findISOTool() (tool string, args []string, err error) {
	if path, err := exec.LookPath("mkisofs"); err == nil {
		return path, []string{"-J", "-r"}, nil
	}
	if path, err := exec.LookPath("xorriso"); err == nil {
		// xorriso -as mkisofs provides mkisofs-compatible behavior.
		return path, []string{"-as", "mkisofs", "-J", "-r"}, nil
	}
	return "", nil, fmt.Errorf(
		"neither mkisofs nor xorriso found in PATH\n" +
			"Install one of:\n" +
			"  pkg install cdrtools    # provides mkisofs\n" +
			"  pkg install xorriso     # alternative ISO tool",
	)
}

// generateBypassISO writes autounattend.xml to a temp directory and
// invokes the ISO tool to produce a single-file ISO at outputPath.
func generateBypassISO(outputPath, isoTool string, baseArgs []string) error {
	tmpDir, err := os.MkdirTemp("", "hospitus-win11-bypass-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	xmlPath := filepath.Join(tmpDir, "autounattend.xml")
	if err := os.WriteFile(xmlPath, []byte(autounattendXML), 0o600); err != nil {
		return fmt.Errorf("write autounattend.xml: %w", err)
	}

	// Build: mkisofs -J -r -o <output> <tmpDir>
	args := append(baseArgs, "-o", outputPath, tmpDir) //nolint:gocritic
	out, err := exec.Command(isoTool, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w\n%s", filepath.Base(isoTool), err, string(out))
	}
	return nil
}

func printManualAttachInstructions(cmd *cobra.Command, vmName, windowsISO, bypassISO string) {
	fmt.Fprintln(cmd.OutOrStdout(), "Attach the ISOs manually, then start the VM:")
	fmt.Fprintf(cmd.OutOrStdout(), "  hospitus bhyve media attach %s --iso %s --bootable\n", vmName, windowsISO)
	fmt.Fprintf(cmd.OutOrStdout(), "  hospitus bhyve media attach %s --iso %s\n", vmName, bypassISO)
	fmt.Fprintf(cmd.OutOrStdout(), "  doas hospitus bhyve start %s\n", vmName)
	fmt.Fprintf(cmd.OutOrStdout(), "  hospitus bhyve vnc %s\n", vmName)
}
