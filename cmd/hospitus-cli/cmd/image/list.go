package image

import (
	"encoding/json"
	"fmt"
	"os"
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

	if len(images) == 0 {
		fmt.Println("No images found.")
		fmt.Println("\nTo see available images to download:")
		fmt.Println("  hospitus image available")
		fmt.Println("\nTo download an image:")
		fmt.Println("  hospitus image fetch <name>")
		return nil
	}

	// Check if JSON output was requested
	outputFormat, _ := cmd.Flags().GetString(cmdutil.FlagOutput)
	if outputFormat == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(images)
	}

	// Display as table
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
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

	fmt.Printf("\nTotal: %d image%s downloaded\n", len(images), cmdutil.Plural(len(images)))

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
