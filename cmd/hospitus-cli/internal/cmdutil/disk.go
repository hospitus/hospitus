package cmdutil

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider"
)

// ParseDiskSpecs parses CLI --disk values into provider.DiskSpec entries.
//
// Supported forms per value:
//
//	<size>            a disk of <size> GB (e.g. "20", "20G", "20GB")
//	<size>:<name>     a disk of <size> GB with the given device name/label
//	physical:/dev/xxx pass through an existing physical device (external disk)
//	/dev/xxx          shorthand for physical:/dev/xxx
//
// Size suffixes G and GB are accepted and ignored; sizes are always GB.
func ParseDiskSpecs(specs []string) ([]provider.DiskSpec, error) {
	var disks []provider.DiskSpec
	for _, raw := range specs {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}

		// Physical device passthrough (external disk).
		if strings.HasPrefix(s, "physical:") || strings.HasPrefix(s, "/dev/") {
			path := strings.TrimPrefix(s, "physical:")
			// Reject path traversal (e.g. "/dev/../etc/passwd"): the cleaned
			// path must still resolve under /dev/.
			cleaned := filepath.Clean(path)
			if !strings.HasPrefix(cleaned, "/dev/") {
				return nil, fmt.Errorf("physical disk path must be under /dev/ (got %q)", path)
			}
			disks = append(disks, provider.DiskSpec{
				Type: provider.DiskTypePhysical,
				Path: cleaned,
			})
			continue
		}

		// <size>[:<name>]
		parts := strings.SplitN(s, ":", 2)
		size, err := parseDiskSizeGB(parts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid disk spec %q: %w", raw, err)
		}
		disk := provider.DiskSpec{SizeGB: size}
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			disk.DeviceName = strings.TrimSpace(parts[1])
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

// parseDiskSizeGB parses a disk size in GB, accepting optional G/GB suffixes.
func parseDiskSizeGB(s string) (int, error) {
	v := strings.TrimSpace(s)
	v = strings.TrimSuffix(strings.TrimSuffix(strings.ToUpper(v), "GB"), "G")
	size, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("size must be an integer number of GB")
	}
	if size <= 0 {
		return 0, fmt.Errorf("size must be positive")
	}
	return size, nil
}
