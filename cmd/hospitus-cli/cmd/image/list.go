package image

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hospitus/hospitus/cmd/hospitus-cli/internal/cmdutil"
)

var listCategory string

func newListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List downloaded images",
		Long: `List all downloaded images on the hospitus daemon.

Shows images organized by category (set, iso, cloud).

Examples:
  # List all downloaded images
  hospitus image list

  # List only jail base sets
  hospitus image list --category set

  # List in JSON format
  hospitus image list --output json`,
		RunE: runList,
	}

	cmd.Flags().StringVarP(&listCategory, "category", "c", "", "Filter by category (set, iso, cloud)")
	cmdutil.AddOutputFlag(cmd)

	return cmd
}

func runList(cmd *cobra.Command, args []string) error {
	format, err := cmdutil.OutputFormatFrom(cmd)
	if err != nil {
		return err
	}

	ctx := cmd.Context()

	images, err := cmdutil.APIClient.ListImages(ctx)
	if err != nil {
		return fmt.Errorf("failed to list images: %w", err)
	}

	// Filter by category if specified
	if listCategory != "" {
		var filtered []map[string]interface{}
		for _, img := range images {
			if cat, ok := img["category"].(string); ok && cat == listCategory {
				filtered = append(filtered, img)
			}
		}
		images = filtered
	}

	// The format first, then the empty case: asking for JSON and getting the
	// "No images found" prose instead is unparseable, and an empty result has
	// to encode as [] — the shape the backup listing already answers with.
	if format == cmdutil.OutputFormatJSON {
		if images == nil {
			images = []map[string]interface{}{}
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(images)
	}

	if len(images) == 0 {
		out := cmd.OutOrStdout()
		fmt.Fprintln(out, "No images found.")
		fmt.Fprintln(out, "\nTo see available images to download:")
		fmt.Fprintln(out, "  hospitus image available")
		fmt.Fprintln(out, "\nTo download an image:")
		fmt.Fprintln(out, "  hospitus image fetch <name>")
		return nil
	}

	// Display as table
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FILENAME\tCATEGORY\tSIZE\tPATH")

	for _, img := range images {
		filename, _ := img["filename"].(string)
		category, _ := img["category"].(string)
		path, _ := img["path"].(string)
		var sizeStr string
		if sz, ok := img["size"].(float64); ok {
			sizeStr = formatSize(int64(sz))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", filename, category, sizeStr, path)
	}
	w.Flush()

	fmt.Fprintf(cmd.OutOrStdout(), "\nTotal: %d image%s downloaded\n", len(images), cmdutil.Plural(len(images)))

	return nil
}

// formatSize formats a size in bytes to a human-readable string
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
