package cmdutil

import (
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestParseDiskSpecs(t *testing.T) {
	t.Run("size only", func(t *testing.T) {
		disks, err := ParseDiskSpecs([]string{"20"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(disks) != 1 || disks[0].SizeGB != 20 || disks[0].Type != "" {
			t.Fatalf("got %+v", disks)
		}
	})

	t.Run("size with name", func(t *testing.T) {
		disks, err := ParseDiskSpecs([]string{"50:backup"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if disks[0].SizeGB != 50 || disks[0].DeviceName != "backup" {
			t.Fatalf("got %+v", disks[0])
		}
	})

	t.Run("size suffixes", func(t *testing.T) {
		for _, s := range []string{"10G", "10GB", "10gb"} {
			disks, err := ParseDiskSpecs([]string{s})
			if err != nil {
				t.Fatalf("%q: unexpected error: %v", s, err)
			}
			if disks[0].SizeGB != 10 {
				t.Errorf("%q: SizeGB = %d, want 10", s, disks[0].SizeGB)
			}
		}
	})

	t.Run("physical device", func(t *testing.T) {
		for _, s := range []string{"physical:/dev/ada2", "/dev/ada2"} {
			disks, err := ParseDiskSpecs([]string{s})
			if err != nil {
				t.Fatalf("%q: unexpected error: %v", s, err)
			}
			if disks[0].Type != provider.DiskTypePhysical || disks[0].Path != "/dev/ada2" {
				t.Errorf("%q: got %+v", s, disks[0])
			}
		}
	})

	t.Run("multiple disks", func(t *testing.T) {
		disks, err := ParseDiskSpecs([]string{"20:data", "physical:/dev/ada3"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(disks) != 2 {
			t.Fatalf("want 2 disks, got %d", len(disks))
		}
	})

	t.Run("empty entries skipped", func(t *testing.T) {
		disks, err := ParseDiskSpecs([]string{"", "  "})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(disks) != 0 {
			t.Fatalf("want 0 disks, got %d", len(disks))
		}
	})

	t.Run("errors", func(t *testing.T) {
		for _, s := range []string{"abc", "0", "-5", "physical:/etc/passwd", "physical:/dev/../etc/passwd", "/dev/../root/.ssh"} {
			if _, err := ParseDiskSpecs([]string{s}); err == nil {
				t.Errorf("%q: expected error, got nil", s)
			}
		}
	})
}
