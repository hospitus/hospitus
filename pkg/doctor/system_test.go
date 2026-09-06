package doctor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHelpers(t *testing.T) {
	if poolOf("zroot/hospitus/jails") != "zroot" {
		t.Error("poolOf should return the first path component")
	}
	if poolOf("tank") != "tank" {
		t.Error("poolOf of a bare pool is itself")
	}
	if atoiDefault("7", 0) != 7 {
		t.Error("atoiDefault should parse")
	}
	if atoiDefault("nope", 3) != 3 {
		t.Error("atoiDefault should fall back")
	}
}

func TestRealSystemBasics(t *testing.T) {
	s := NewSystem()

	if s.GOOS() != runtime.GOOS {
		t.Errorf("GOOS() = %q, want %q", s.GOOS(), runtime.GOOS)
	}

	// A shell is present on any Unix CI host; a random name is not.
	if !s.HasCommand("sh") {
		t.Error("expected sh to be found")
	}
	if s.HasCommand("definitely-not-a-real-binary-xyz") {
		t.Error("did not expect a bogus binary to be found")
	}

	t.Setenv("HOSPITUS_DOCTOR_TEST", "value")
	if s.Getenv("HOSPITUS_DOCTOR_TEST") != "value" {
		t.Error("Getenv should read the environment")
	}

	dir := t.TempDir()
	if !s.PathExists(dir) {
		t.Error("PathExists should see the temp dir")
	}
	if s.PathExists(filepath.Join(dir, "nope")) {
		t.Error("PathExists should not see a missing path")
	}
	if !s.DirWritable(dir) {
		t.Error("temp dir should be writable")
	}
	// A not-yet-existing subdir under a writable parent is creatable.
	if !s.DirWritable(filepath.Join(dir, "sub", "child")) {
		t.Error("creatable subdir should be reported writable")
	}

	// A regular file at the path is not a directory to write into: it passed
	// as writable for root, and the daemon's MkdirAll then failed with
	// ENOTDIR.
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if s.DirWritable(file) {
		t.Error("a regular file was reported as a writable directory")
	}
}

func TestRealSystemRun(t *testing.T) {
	s := NewSystem()
	ctx := context.Background()
	if err := s.Run(ctx, "true"); err != nil {
		t.Errorf("run true: %v", err)
	}
	if err := s.Run(ctx, "false"); err == nil {
		t.Error("run false should error")
	}
}

func TestRealSystemSysctl(t *testing.T) {
	if runtime.GOOS != "freebsd" && runtime.GOOS != "darwin" {
		t.Skip("sysctl kern.ostype is BSD-specific")
	}
	s := NewSystem()
	ctx := context.Background()
	v, err := s.Sysctl(ctx, "kern.ostype")
	if err != nil {
		t.Skipf("sysctl unavailable: %v", err)
	}
	if v == "" {
		t.Error("kern.ostype should be non-empty")
	}
	if _, err := s.Sysctl(ctx, "hospitus.doctor.no.such.oid"); err == nil {
		t.Error("unknown sysctl should error")
	}
}

func TestRealSystemFreeBSDProbes(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("kldstat/sysrc/zpool are FreeBSD-specific")
	}
	s := NewSystem()
	ctx := context.Background()
	// These just exercise the code paths; results depend on the host, so we
	// only assert they don't panic and return a bool.
	_ = s.KernelModuleLoaded(ctx, "nonexistent_module_xyz")
	_ = s.ServiceEnabled(ctx, "nonexistent_rcvar_xyz")
	_ = s.ZFSPoolExists(ctx, "nonexistent_pool_xyz")
}

func TestUnixWritableRoot(t *testing.T) {
	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	// As root (CI often runs as root) this is always true; as a normal user the
	// temp dir we own is writable too.
	if !unixWritable(info) {
		t.Error("own temp dir should be writable")
	}
}

// TestProbesHonourTheCallerContext covers what threading ctx through System
// buys: a canceled diagnose stops instead of running every remaining probe to
// its own timeout.
func TestProbesHonourTheCallerContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probes shell out to Unix tools")
	}
	s := NewSystem()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.Sysctl(ctx, "kern.ostype"); err == nil {
		t.Error("Sysctl ran against a canceled context")
	}
	if err := s.Run(ctx, "true"); err == nil {
		t.Error("Run ran against a canceled context")
	}
}
