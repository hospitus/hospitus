package validation

import (
	"os"
	"path/filepath"
	"testing"
)

// realTempDir returns a symlink-free temporary directory. t.TempDir() sits under
// /var/folders on macOS, which is itself reached through a symlink, so tests that
// need an unambiguous root resolve it up front.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(t.TempDir()): %v", err)
	}
	return dir
}

func TestResolvePathMissingLeafKeepsParentResolution(t *testing.T) {
	// Arrange: a real directory reachable through a symlink.
	root := realTempDir(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	// Act: resolve a file that does not exist yet, below the symlink.
	got, err := ResolvePath(filepath.Join(link, "sub", "file.txt"))
	// Assert: the existing ancestor is resolved, the missing tail re-attached.
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	want := filepath.Join(target, "sub", "file.txt")
	if got != want {
		t.Errorf("ResolvePath = %q, want %q", got, want)
	}
}

func TestResolvePathEmpty(t *testing.T) {
	if _, err := ResolvePath(""); err == nil {
		t.Error("ResolvePath(\"\") = nil error, want error")
	}
}

func TestPathWithin(t *testing.T) {
	root := realTempDir(t)

	inside := filepath.Join(root, "data")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	outside := realTempDir(t)

	// A root reached through a symlink reproduces the macOS /var -> /private/var
	// layout that made every containment check fail.
	linkedRoot := filepath.Join(realTempDir(t), "linked-root")
	if err := os.Symlink(root, linkedRoot); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	// A symlink planted inside the root must not be able to escape it.
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	tests := []struct {
		name string
		path string
		root string
		want bool
	}{
		{"file inside root", filepath.Join(inside, "disk.qcow2"), root, true},
		{"root itself", root, root, true},
		{"sibling directory", outside, root, false},
		{"parent traversal", filepath.Join(inside, "..", "..", "etc"), root, false},
		{"root reached through symlink", filepath.Join(inside, "disk.qcow2"), linkedRoot, true},
		{"path reached through symlink", filepath.Join(linkedRoot, "data", "disk.qcow2"), root, true},
		{"symlink escaping the root", filepath.Join(escape, "secret"), root, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PathWithin(tt.path, tt.root)
			if err != nil {
				t.Fatalf("PathWithin(%q, %q): %v", tt.path, tt.root, err)
			}
			if got != tt.want {
				t.Errorf("PathWithin(%q, %q) = %v, want %v", tt.path, tt.root, got, tt.want)
			}
		})
	}
}

func TestPathWithinAnySkipsEmptyRoots(t *testing.T) {
	root := realTempDir(t)
	other := realTempDir(t)

	got, err := PathWithinAny(filepath.Join(root, "file"), "", other, root)
	if err != nil {
		t.Fatalf("PathWithinAny: %v", err)
	}
	if !got {
		t.Error("PathWithinAny = false, want true for a path under the last root")
	}

	got, err = PathWithinAny(filepath.Join(root, "file"), "", other)
	if err != nil {
		t.Fatalf("PathWithinAny: %v", err)
	}
	if got {
		t.Error("PathWithinAny = true, want false when no root contains the path")
	}
}

func TestEnsurePathWithin(t *testing.T) {
	root := realTempDir(t)

	if err := EnsurePathWithin(filepath.Join(root, "ok"), root); err != nil {
		t.Errorf("EnsurePathWithin(inside) = %v, want nil", err)
	}
	if err := EnsurePathWithin(realTempDir(t), root); err == nil {
		t.Error("EnsurePathWithin(outside) = nil, want error")
	}
}

// TestPathWithinFilesystemRoot covers root "/": a prefix test would require the
// candidate to start with "//" and reject every path on the filesystem.
func TestPathWithinFilesystemRoot(t *testing.T) {
	for _, tc := range []struct {
		path, root string
		want       bool
	}{
		{"/tmp/file", "/", true},
		{"/", "/", true},
		{"/usr/local/bin", "/", true},
		{"/tmp/file", "/tmp", true},
		{"/tmpfile", "/tmp", false},
		{"/etc/passwd", "/tmp", false},
	} {
		got, err := PathWithin(tc.path, tc.root)
		if err != nil {
			t.Errorf("PathWithin(%q, %q): %v", tc.path, tc.root, err)
			continue
		}
		if got != tc.want {
			t.Errorf("PathWithin(%q, %q) = %v, want %v", tc.path, tc.root, got, tc.want)
		}
	}
}
