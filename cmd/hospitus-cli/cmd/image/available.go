package image

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/pkg/image"
)

var (
	availableCategory string
	availableProvider string
	availableOS       string
	availableArch     string
)

func newAvailableCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "available",
		Short: "List available images to download",
		Long: `List available images from the Hospitus catalog.

Shows all downloadable images organized by category (set, iso, cloud).
Use filters to narrow down the list.

Examples:
  # List all available images
  hospitus image available

  # List only FreeBSD jail images (sets)
  hospitus image available --category set

  # List cloud images for bhyve VMs
  hospitus image available --category cloud --provider bhyve

  # List all FreeBSD images
  hospitus image available --os freebsd

  # List in JSON format
  hospitus image available --output json`,
		RunE: runAvailable,
	}

	cmd.Flags().StringVarP(&availableCategory, "category", "c", "", "Filter by category (set, iso, cloud)")
	cmd.Flags().StringVarP(&availableProvider, "provider", "p", "", "Filter by provider (jail, bhyve, qemu)")
	cmd.Flags().StringVar(&availableOS, "os", "", "Filter by OS type (freebsd, linux, openbsd, netbsd)")
	cmd.Flags().StringVar(&availableArch, "arch", "", "Filter by architecture (amd64, arm64, riscv64, i386)")
	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runAvailable(cmd *cobra.Command, args []string) error {
	// Get image directory
	imageDir := "/var/lib/hospitus/images"
	if dir := os.Getenv("HOSPITUS_IMAGE_DIR"); dir != "" {
		imageDir = dir
	}

	// Create catalog
	catalog := image.NewCatalog(imageDir)

	// Start from the full catalog and apply each filter cumulatively so that
	// category, provider, and OS can be combined.
	profiles := catalog.Available()

	if availableCategory != "" {
		cat := image.ImageCategory(availableCategory)
		var filtered []image.ImageProfile
		for i := range profiles {
			if profiles[i].Category == cat {
				filtered = append(filtered, profiles[i])
			}
		}
		profiles = filtered
	}

	if availableProvider != "" {
		prov := image.ProviderType(availableProvider)
		var filtered []image.ImageProfile
		for i := range profiles {
			for _, p := range profiles[i].Providers {
				if p == prov {
					filtered = append(filtered, profiles[i])
					break
				}
			}
		}
		profiles = filtered
	}

	// Architecture decides whether a VM runs natively or under emulation, so it
	// is the first thing to filter on when picking an image for a given host.
	if availableArch != "" {
		var filtered []image.ImageProfile
		for i := range profiles {
			if strings.EqualFold(profiles[i].Arch, availableArch) {
				filtered = append(filtered, profiles[i])
			}
		}
		profiles = filtered
	}

	if availableOS != "" {
		var filtered []image.ImageProfile
		for i := range profiles {
			if strings.EqualFold(profiles[i].OSType, availableOS) {
				filtered = append(filtered, profiles[i])
			}
		}
		profiles = filtered
	}

	if len(profiles) == 0 {
		fmt.Println("No images found matching the criteria.")
		return nil
	}

	// Check if JSON output was requested via -o or --output flag
	outputFormat, _ := cmd.Flags().GetString(cmdutil.FlagOutput)
	if outputFormat == "json" {
		return outputAvailableJSON(profiles)
	}

	return outputAvailableTable(profiles)
}

func outputAvailableTable(profiles []image.ImageProfile) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	fmt.Fprintln(w, "NAME\tCATEGORY\tOS\tVERSION\tARCH\tPROVIDERS")

	for i := range profiles {
		p := &profiles[i]
		providers := make([]string, len(p.Providers))
		for j, prov := range p.Providers {
			providers[j] = string(prov)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			p.Name,
			p.Category,
			p.OSType,
			p.OSVersion,
			p.Arch,
			strings.Join(providers, ","),
		)
	}

	w.Flush()

	fmt.Printf("\nTotal: %d image%s available\n", len(profiles), cmdutil.Plural(len(profiles)))
	fmt.Println("\nTo download an image, use:")
	fmt.Println("  hospitus image fetch <name>")

	return nil
}

func outputAvailableJSON(profiles []image.ImageProfile) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(profiles)
}
