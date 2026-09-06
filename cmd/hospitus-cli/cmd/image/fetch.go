package image

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
	"github.com/hospitus/hospitus/internal/client"
	"github.com/hospitus/hospitus/pkg/image"
)

func newFetchCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "fetch <version>",
		Short: "Download a FreeBSD base system image",
		Long: `Download a FreeBSD base system image.

Examples:
  hospitus image fetch 14.1-RELEASE-amd64
  hospitus image fetch 15.0-STABLE-amd64
  hospitus image fetch 13.3-RELEASE-amd64`,
		Args: cobra.ExactArgs(1),
		RunE: runFetch,
	}
}

func runFetch(cmd *cobra.Command, args []string) error {
	version := args[0]
	// The root command installs a signal-canceled context; using it is what
	// makes Ctrl-C during a multi-minute download end the request.
	ctx := cmd.Context()

	fmt.Fprintf(cmd.OutOrStdout(), "Fetching image: %s\n", version)
	fmt.Fprintln(cmd.OutOrStdout(), "This may take a few minutes...")

	// Call API to download image
	err := cmdutil.APIClient.FetchImage(ctx, version, cmd.OutOrStdout())
	if errors.Is(err, client.ErrDownloadInterrupted) {
		// The partial file stays on the daemon, but this run produced no
		// usable image and must not exit 0.
		return fmt.Errorf("image %s is incomplete: %w (run the same command again to resume)", version, err)
	}
	if err != nil {
		return fmt.Errorf("failed to fetch image: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "✓ Image %s downloaded successfully\n", version)
	fmt.Fprintf(cmd.OutOrStdout(), "\nYou can now create instances with:\n")
	for _, providerName := range imageProviders(version) {
		fmt.Fprintf(cmd.OutOrStdout(), "  hospitus %s create <name> --image %s\n", providerName, version)
	}

	return nil
}

// imageProviders returns the providers that can use the given catalog image, so
// the hint after a download names one that exists. Suggesting "jail" for a
// QEMU-only cloud image sent the reader down a dead end, and jail does not exist
// at all on macOS. An image missing from the catalog falls back to the full set.
func imageProviders(name string) []string {
	imageDir := "/var/lib/hospitus/images"
	if dir := os.Getenv("HOSPITUS_IMAGE_DIR"); dir != "" {
		imageDir = dir
	}

	// ResolveProfile, not a comparison against Name: the catalog is keyed on
	// "freebsd-14.3-RELEASE-amd64" while the caller types "14.3-RELEASE-amd64",
	// so an exact match never succeeded and every image fell to the list below —
	// a base set for jails was advertised for bhyve, QEMU and Podman as well.
	profile := image.NewCatalog(imageDir).ResolveProfile(name)
	if profile != nil && len(profile.Providers) > 0 {
		names := make([]string, 0, len(profile.Providers))
		for _, p := range profile.Providers {
			names = append(names, string(p))
		}
		return names
	}

	return []string{"jail", "bhyve", "qemu", "podman"}
}
