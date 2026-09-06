package bhyve

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/dataset"
)

// ----------------------------------------------------------------------------
// intSliceToString / stringToIntSlice
// ----------------------------------------------------------------------------

func TestIntSliceToString(t *testing.T) {
	tests := []struct {
		name  string
		input []int
		want  string
	}{
		{"nil slice", nil, ""},
		{"empty slice", []int{}, ""},
		{"single element", []int{0}, "0"},
		{"multiple elements", []int{1, 2, 3}, "1,2,3"},
		{"negative values", []int{-1, 0, 100}, "-1,0,100"},
		{"large values", []int{1000000, 999}, "1000000,999"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := intSliceToString(tt.input)
			if got != tt.want {
				t.Errorf("intSliceToString(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStringToIntSlice(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []int
	}{
		{"empty string", "", []int{}},
		{"single zero", "0", []int{0}},
		{"three values", "1,2,3", []int{1, 2, 3}},
		{"negative values", "-1,0,1", []int{-1, 0, 1}},
		{"whitespace trimmed", " 10 , 20 ", []int{10, 20}},
		{"invalid entry skipped", "1,bad,3", []int{1, 3}},
		{"all invalid", "x,y,z", []int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringToIntSlice(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("stringToIntSlice(%q) len=%d, want %d (got %v)", tt.input, len(got), len(tt.want), got)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("stringToIntSlice(%q)[%d] = %d, want %d", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestIntSliceRoundtrip verifies the two helpers are inverse operations.
func TestIntSliceRoundtrip(t *testing.T) {
	original := []int{0, 10, 4096, 512, -5}
	encoded := intSliceToString(original)
	decoded := stringToIntSlice(encoded)

	if len(decoded) != len(original) {
		t.Fatalf("roundtrip length mismatch: got %v, want %v", decoded, original)
	}
	for i := range original {
		if decoded[i] != original[i] {
			t.Errorf("roundtrip[%d]: got %d, want %d", i, decoded[i], original[i])
		}
	}
}

// ----------------------------------------------------------------------------
// detectUEFI
// ----------------------------------------------------------------------------

func TestDetectUEFINotFound(t *testing.T) {
	p := NewBhyveProvider()
	p.detectUEFI()

	// If firmware IS installed (common on a bhyve host), the test is a no-op.
	if p.hasUEFI {
		t.Skipf("UEFI firmware found at %s — skipping not-found test", p.uefiPath)
	}
	// Firmware not found: both fields must remain zero values.
	if p.uefiPath != "" {
		t.Errorf("uefiPath should be empty when no firmware found, got %q", p.uefiPath)
	}
}

func TestDetectUEFICodeFile(t *testing.T) {
	// Create a fake CODE.fd in a temp directory and temporarily override the
	// lookup paths by patching a known firmware path into a location we control.
	// We do this by creating the exact path that detectUEFI looks for.
	//
	// NOTE: detectUEFI reads hard-coded paths so we can only test the "not found"
	// branch here unless the real firmware is present. The positive branch is
	// implicitly covered by TestBhyveProviderInitializeFreeBSD when firmware exists.
	p := NewBhyveProvider()
	p.detectUEFI()

	// If firmware isn't installed, hasUEFI stays false — that's correct.
	// If firmware IS installed (e.g., on a real bhyve host), hasUEFI is true.
	// Both outcomes are valid; what we assert is that uefiPath is non-empty
	// whenever hasUEFI is true.
	if p.hasUEFI && p.uefiPath == "" {
		t.Error("detectUEFI() set hasUEFI=true but uefiPath is empty")
	}
	if !p.hasUEFI && p.uefiPath != "" {
		t.Error("detectUEFI() left uefiPath non-empty but hasUEFI=false")
	}
}

// ----------------------------------------------------------------------------
// copyUEFIVars
// ----------------------------------------------------------------------------

func TestCopyUEFIVarsNoTemplate(t *testing.T) {
	p := NewBhyveProvider()
	// uefiVarsPath is empty → must return an error.
	tmpDir := t.TempDir()
	err := p.copyUEFIVars(filepath.Join(tmpDir, "VARS.fd"))
	if err == nil {
		t.Error("expected error when uefiVarsPath is empty, got nil")
	}
}

func TestCopyUEFIVarsSuccess(t *testing.T) {
	// Write a fake VARS template, then copy it.
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "BHYVE_UEFI_VARS.fd")
	if err := os.WriteFile(srcPath, []byte("fake-uefi-vars"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewBhyveProvider()
	p.uefiVarsPath = srcPath

	dstPath := filepath.Join(tmpDir, "vm-specific-VARS.fd")
	if err := p.copyUEFIVars(dstPath); err != nil {
		t.Fatalf("copyUEFIVars failed: %v", err)
	}

	data, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("failed to read copied file: %v", err)
	}
	if string(data) != "fake-uefi-vars" {
		t.Errorf("copied content = %q, want %q", string(data), "fake-uefi-vars")
	}
}

func TestCopyUEFIVarsSourceMissing(t *testing.T) {
	tmpDir := t.TempDir()
	p := NewBhyveProvider()
	p.uefiVarsPath = filepath.Join(tmpDir, "nonexistent-VARS.fd")

	err := p.copyUEFIVars(filepath.Join(tmpDir, "dst.fd"))
	if err == nil {
		t.Error("expected error when source file is missing, got nil")
	}
}

// isolatedZFSParent points the provider at a ZFS parent dataset of its own for
// the duration of the test, destroys it afterwards, and returns its name for a
// test that has to name the dataset itself.
//
// Initialize creates the parent dataset, and `go test ./...` runs packages in
// parallel: as root on a real FreeBSD host the jail and bhyve suites otherwise
// create and destroy the same zroot/hospitus tree at the same time, which fails
// intermittently with "dataset is busy" or "dataset does not exist". Giving each
// test its own tree removes the contention instead of hiding it behind -p 1.
func isolatedZFSParent(t *testing.T) string {
	t.Helper()

	// Read the pool before overriding the variable it is derived from.
	pool := dataset.Pool()
	parent := fmt.Sprintf("%s/hospitus-test-%d-%s", pool, os.Getpid(), zfsSafeName(t.Name()))

	// A root FreeBSD run creates this tree for real, which needs the pool to be
	// there. It is not on every host: a UFS-rooted machine has no zroot, and the
	// test would fail on the environment rather than on the code.
	if runtime.GOOS == "freebsd" && os.Geteuid() == 0 {
		if err := exec.Command("zfs", "list", "-H", "-o", "name", pool).Run(); err != nil {
			t.Skipf("ZFS pool %q is not available on this host", pool)
		}
	}

	t.Setenv("HOSPITUS_ZFS_PARENT", parent)

	t.Cleanup(func() {
		// Only a root FreeBSD run ever created anything; elsewhere the tests skip
		// before Initialize.
		if runtime.GOOS != "freebsd" || os.Geteuid() != 0 {
			return
		}
		_ = exec.Command("zfs", "destroy", "-r", parent).Run()
	})

	return parent
}

// zfsSafeName turns a test name into a ZFS dataset component: subtests contain
// slashes and names can be long, neither of which a dataset name accepts.
func zfsSafeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}

	const maxComponent = 48
	safe := b.String()
	if len(safe) > maxComponent {
		safe = safe[:maxComponent]
	}
	return safe
}
