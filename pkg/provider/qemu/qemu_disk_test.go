package qemu

import (
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestValidatePhysicalDisk(t *testing.T) {
	t.Run("rejects empty path", func(t *testing.T) {
		if err := validatePhysicalDisk(""); err == nil {
			t.Error("expected error for empty path")
		}
	})
	t.Run("rejects non-/dev path", func(t *testing.T) {
		if err := validatePhysicalDisk("/tmp/notadev"); err == nil {
			t.Error("expected error for non-/dev path")
		}
	})
	t.Run("rejects missing device", func(t *testing.T) {
		if err := validatePhysicalDisk("/dev/this-does-not-exist-xyz"); err == nil {
			t.Error("expected error for missing device")
		}
	})
	t.Run("accepts a real device node", func(t *testing.T) {
		// /dev/null is a character device present on all supported platforms.
		if err := validatePhysicalDisk("/dev/null"); err != nil {
			t.Errorf("expected /dev/null to validate, got: %v", err)
		}
	})
}

func TestBuildQEMUConfigDiskFormat(t *testing.T) {
	p := NewQEMUProvider()
	spec := provider.InstanceSpec{Name: "fmt-vm", CPUs: 1, MemoryMB: 512}

	cases := []struct {
		diskPath   string
		wantFormat string
	}{
		{"/tmp/disk.qcow2", "format=qcow2"},
		{"/tmp/disk.raw", "format=raw"},
		{"/tmp/disk.img", "format=raw"},
		{"/dev/ada2", "format=raw"}, // physical passthrough
	}
	for _, c := range cases {
		config := p.buildQEMUConfig(spec, "qemu-system-x86_64", c.diskPath, "", "/tmp/vm", "amd64")
		joined := strings.Join(config.Args, " ")
		if !strings.Contains(joined, c.diskPath) || !strings.Contains(joined, c.wantFormat) {
			t.Errorf("diskPath %q: expected %q in args, got: %s", c.diskPath, c.wantFormat, joined)
		}
	}
}
