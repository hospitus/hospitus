package bhyve

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVMConfigRefusesValuesTheFormatCannotHold covers injection into vm.conf.
//
// The file is flat "key=value" lines with comma-separated lists. A newline in a
// value writes a second key that parseVMConfig reads back as genuine — which is
// how a disk path could add a passthrough device — and a comma inside a list
// element splits it into two entries.
func TestVMConfigRefusesValuesTheFormatCannotHold(t *testing.T) {
	dir := t.TempDir()
	vmDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &BhyveProvider{dataDir: dir}

	for _, tc := range []struct {
		name string
		cfg  *vmConfig
	}{
		{"a newline in a scalar", &vmConfig{Name: "web", Console: "nmdm0\npassthrough=1/0/0"}},
		{"a newline in a list element", &vmConfig{
			Name: "web", DiskPaths: []string{"/dev/zvol/p/web/disk0\ntpm_enabled=true"},
		}},
		{"a comma in a list element", &vmConfig{
			Name: "web", DiskPaths: []string{"/dev/zvol/p/web/disk0,/dev/ada0"},
		}},
		{"a carriage return", &vmConfig{Name: "web", UEFIVars: "vars.fd\rvnc_port=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := p.saveVMConfig(vmDir, tc.cfg); err == nil {
				t.Error("saveVMConfig accepted a value the format cannot represent")
			}
		})
	}

	// And an ordinary configuration still round-trips.
	good := &vmConfig{
		Name:        "web",
		CPUs:        2,
		DiskPaths:   []string{"/dev/zvol/p/web/disk0", "/dev/zvol/p/web/disk1"},
		DiskDrivers: []string{"virtio-blk", "virtio-blk"},
	}
	if err := p.saveVMConfig(vmDir, good); err != nil {
		t.Fatalf("saveVMConfig refused an ordinary config: %v", err)
	}
	back, err := p.loadVMConfig(vmDir)
	if err != nil {
		t.Fatalf("loadVMConfig: %v", err)
	}
	if len(back.DiskPaths) != 2 {
		t.Errorf("disk paths = %v, want the two that were written", back.DiskPaths)
	}
}
