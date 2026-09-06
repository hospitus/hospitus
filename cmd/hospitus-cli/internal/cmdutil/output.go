package cmdutil

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/pkg/provider"
)

// OutputFormat represents the output format type
type OutputFormat string

const (
	OutputFormatTable OutputFormat = "table"
	OutputFormatJSON  OutputFormat = "json"
)

// PrintInstanceList prints a list of instances in the specified format
func PrintInstanceList(writer io.Writer, instances []datastore.Instance, format OutputFormat) error {
	if format == OutputFormatJSON {
		return printJSON(writer, instances)
	}

	// Table format
	if len(instances) == 0 {
		fmt.Fprintln(writer, "No instances found")
		return nil
	}

	w := tabwriter.NewWriter(writer, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tIP ADDRESS\tCPUs\tMEMORY\tPROVIDER\tCREATED")

	for i := range instances {
		inst := &instances[i]
		cpus := 1
		if inst.Spec.CPUs > 0 {
			cpus = inst.Spec.CPUs
		}
		memory := fmt.Sprintf("%dMB", inst.Spec.MemoryMB)
		if inst.Spec.MemoryMB == 0 {
			memory = "512MB" // default
		}
		created := inst.CreatedAt.Format("2006-01-02 15:04")

		// Get IP address from networks
		ipAddr := "-"
		if len(inst.Spec.Networks) > 0 && inst.Spec.Networks[0].IPv4 != "" {
			ipAddr = inst.Spec.Networks[0].IPv4
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			inst.Name,
			inst.State,
			ipAddr,
			cpus,
			memory,
			inst.Provider,
			created,
		)
	}

	return w.Flush()
}

// PrintInstance prints a single instance in the specified format
func PrintInstance(writer io.Writer, inst *datastore.Instance, format OutputFormat) error {
	if format == OutputFormatJSON {
		return printJSON(writer, inst)
	}

	// Human-readable format with sections
	fmt.Fprintf(writer, "Name:         %s\n", inst.Name)
	fmt.Fprintf(writer, "State:        %s\n", inst.State)
	fmt.Fprintf(writer, "Provider:     %s\n", inst.Provider)
	if inst.Spec.Description != "" {
		fmt.Fprintf(writer, "Description:  %s\n", inst.Spec.Description)
	}

	// Resources section
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Resources:")
	cpus := inst.Spec.CPUs
	if cpus == 0 {
		cpus = 1
	}
	fmt.Fprintf(writer, "  CPUs:       %d\n", cpus)
	memory := inst.Spec.MemoryMB
	if memory == 0 {
		memory = 512
	}
	fmt.Fprintf(writer, "  Memory:     %d MB\n", memory)

	// Network section
	if len(inst.Spec.Networks) > 0 {
		net := inst.Spec.Networks[0]
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, "Network:")
		if net.Type != "" {
			fmt.Fprintf(writer, "  Type:       %s\n", net.Type)
		}
		if net.Bridge != "" {
			fmt.Fprintf(writer, "  Bridge:     %s\n", net.Bridge)
		}
		if net.IPv4 != "" {
			fmt.Fprintf(writer, "  IPv4:       %s\n", net.IPv4)
		}
		if net.IPv6 != "" {
			fmt.Fprintf(writer, "  IPv6:       %s\n", net.IPv6)
		}
		if net.MAC != "" {
			fmt.Fprintf(writer, "  MAC:        %s\n", net.MAC)
		}
	}

	// Image/OS section
	if inst.Spec.Image != "" {
		fmt.Fprintln(writer)
		fmt.Fprintf(writer, "Image:        %s\n", inst.Spec.Image)
	}
	if inst.Spec.OSType != "" {
		if inst.Spec.Image == "" {
			fmt.Fprintln(writer)
		}
		// The version is optional on the spec — it only arrives when the caller
		// passed --os-version — but the jail provider derives it from the image
		// and reports it on the handle. Fall back to that, and print the type
		// alone rather than leave a dangling space when neither is known.
		osVersion := inst.Spec.OSVersion
		if osVersion == "" && inst.Handle.Metadata != nil {
			if release, ok := inst.Handle.Metadata["os_release"].(string); ok {
				osVersion = release
			}
		}
		if osVersion == "" {
			fmt.Fprintf(writer, "OS:           %s\n", inst.Spec.OSType)
		} else {
			fmt.Fprintf(writer, "OS:           %s %s\n", inst.Spec.OSType, osVersion)
		}
	}

	// ZFS dataset (from handle metadata)
	if inst.Handle.Metadata != nil {
		if zfsDataset, ok := inst.Handle.Metadata["zfs_dataset"].(string); ok {
			fmt.Fprintf(writer, "ZFS Dataset:  %s\n", zfsDataset)
		}
	}

	// Timestamps
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "Created:      %s\n", inst.CreatedAt.Format(time.RFC3339))

	// Labels
	if len(inst.Labels) > 0 {
		fmt.Fprintln(writer)
		fmt.Fprintln(writer, "Labels:")
		keys := make([]string, 0, len(inst.Labels))
		for k := range inst.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(writer, "  %s: %s\n", k, inst.Labels[k])
		}
	}

	return nil
}

// FormatBytes renders a byte count for human reading.
//
// Every provider's stats command needs this, and each had grown its own copy —
// three identical, one rendering the same number differently. One product
// should print one gigabyte the same way whichever workload it came from.
func FormatBytes(bytes int64) string {
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

// FormatState formats an instance state for display.
func FormatState(state provider.InstanceState) string {
	return string(state)
}

// FormatDuration formats a duration in human-readable form
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// printJSON prints data as JSON
func printJSON(writer io.Writer, data interface{}) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}
