package bhyve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPCISlotToBhyve(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"0000:01:00.0", "1/0/0", false},
		{"0000:02:00.1", "2/0/1", false},
		{"01:00.0", "1/0/0", false},
		{"0000:0a:1f.7", "a/1f/7", false},
		{"0000:00:00.0", "0/0/0", false},
		{"1/0/0", "1/0/0", false}, // already converted
		{"bogus", "", true},
		{"0000:01:0000", "", true}, // missing .function
	}
	for _, c := range cases {
		got, err := pciSlotToBhyve(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("pciSlotToBhyve(%q) expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("pciSlotToBhyve(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("pciSlotToBhyve(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMergeRuntimePassthrough(t *testing.T) {
	p := &BhyveProvider{}

	t.Run("no passthrough file is a no-op", func(t *testing.T) {
		cfg := &vmConfig{}
		if err := p.mergeRuntimePassthrough(t.TempDir(), cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Passthrough) != 0 {
			t.Fatalf("expected empty passthrough, got %v", cfg.Passthrough)
		}
	})

	t.Run("merges and de-duplicates devices", func(t *testing.T) {
		dir := t.TempDir()
		devices := []PassthroughDevice{
			{Type: "gpu", PCISlot: "0000:01:00.0"},
			{Type: "gpu", PCISlot: "0000:01:00.1"},
		}
		data, _ := json.MarshalIndent(devices, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "passthrough.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}

		// Pre-existing spec-level passthrough that overlaps with one device.
		cfg := &vmConfig{Passthrough: []string{"1/0/0"}}
		if err := p.mergeRuntimePassthrough(dir, cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		want := []string{"1/0/0", "1/0/1"}
		if len(cfg.Passthrough) != len(want) {
			t.Fatalf("Passthrough = %v, want %v", cfg.Passthrough, want)
		}
		for i := range want {
			if cfg.Passthrough[i] != want[i] {
				t.Errorf("Passthrough[%d] = %q, want %q", i, cfg.Passthrough[i], want[i])
			}
		}
	})
}
