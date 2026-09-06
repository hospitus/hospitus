package doctor

import (
	"context"
	"strings"
	"testing"
)

// fakeSystem answers the three questions dirWritableCheck asks.
type dirCheckSystem struct {
	System
	root     bool
	writable bool
	exists   bool
}

func (s dirCheckSystem) IsRoot() bool              { return s.root }
func (s dirCheckSystem) DirWritable(_ string) bool { return s.writable }
func (s dirCheckSystem) PathExists(_ string) bool  { return s.exists }

func TestDirWritableCheckSkipsWhenNotRoot(t *testing.T) {
	check := dirWritableCheck("data-dir", "Data directory", "/var/lib/hospitus", true, "")

	// An unprivileged --check cannot write to the daemon's directory, and
	// reported it as a missing prerequisite on a host where it was present.
	got := check.Run(context.Background(), dirCheckSystem{root: false, writable: false, exists: true})
	if got.Status != StatusSkip {
		t.Errorf("status = %v, want skip: %s", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "doas") {
		t.Errorf("detail does not say how to answer it: %q", got.Detail)
	}

	// As root, an unwritable directory is a real failure.
	got = check.Run(context.Background(), dirCheckSystem{root: true, writable: false})
	if got.Status != StatusFail {
		t.Errorf("status = %v, want fail: %s", got.Status, got.Detail)
	}

	// Writable is fine either way.
	got = check.Run(context.Background(), dirCheckSystem{root: false, writable: true})
	if got.Status != StatusOK {
		t.Errorf("status = %v, want ok: %s", got.Status, got.Detail)
	}
}

// TestDirWritableCheckWarnsWhenTheDirectoryIsMissing covers a fresh host with
// no data directory: existence needs no privileges, and an unprivileged run
// reported only "needs root" — hiding a prerequisite the daemon fails to start
// without.
func TestDirWritableCheckWarnsWhenTheDirectoryIsMissing(t *testing.T) {
	check := dirWritableCheck("data-dir", "Data directory", "/var/lib/hospitus", true, "")

	got := check.Run(context.Background(), dirCheckSystem{root: false, writable: false, exists: false})
	if got.Status != StatusWarn {
		t.Errorf("status = %v, want warn: %s", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "does not exist") {
		t.Errorf("detail does not say the directory is missing: %q", got.Detail)
	}
	if !strings.Contains(got.Remediation, "mkdir -p") {
		t.Errorf("remediation does not say how to create it: %q", got.Remediation)
	}
}

// TestDirWritableCheckNamesANonDirectory covers a regular file at the data-dir
// path: it passed as writable for root, and the daemon's own MkdirAll then
// failed with ENOTDIR.
func TestDirWritableCheckNamesANonDirectory(t *testing.T) {
	check := dirWritableCheck("data-dir", "Data directory", "/var/lib/hospitus", true, "")

	got := check.Run(context.Background(), dirCheckSystem{root: true, writable: false, exists: true})
	if got.Status != StatusFail {
		t.Errorf("status = %v, want fail: %s", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "exists but is not a directory") {
		t.Errorf("detail does not say what is wrong: %q", got.Detail)
	}
}

// TestDefaultChecksHonorsAConfiguredDataDir covers a deployment run with
// --data-dir: the check reported on the default path, a directory that host
// does not use.
func TestDefaultChecksHonorsAConfiguredDataDir(t *testing.T) {
	c := findCheck(t, DefaultChecks("/tank/hospitus"), "data-dir")
	sys := newFakeSystem()
	sys.writable["/tank/hospitus"] = true

	got := c.Run(context.Background(), sys)
	if got.Status != StatusOK {
		t.Errorf("status = %v, want ok: %s", got.Status, got.Detail)
	}
	if !strings.Contains(got.Detail, "/tank/hospitus") {
		t.Errorf("detail names the wrong directory: %q", got.Detail)
	}

	// Omitted, the default is what it always was.
	def := findCheck(t, DefaultChecks(), "data-dir")
	if got := def.Run(context.Background(), newFakeSystem()); !strings.Contains(got.Detail, DefaultDataDir) {
		t.Errorf("default check names %q, want %q", got.Detail, DefaultDataDir)
	}
}

// The bhyve firmware check must skip off FreeBSD like every other bhyve check:
// macos.md promises "0 failed" on a healthy Mac, and this check failing there
// made `hospitus init --check` exit non-zero and advise `pkg install edk2-bhyve`.
func TestUEFIFirmwareCheckSkipsOffFreeBSD(t *testing.T) {
	check := uefiFirmwareCheck([]string{"bhyve"})

	for _, goos := range []string{"darwin", "linux"} {
		got := check.Run(context.Background(), &fakeSystem{goos: goos})
		if got.Status != StatusSkip {
			t.Errorf("on %s: status = %v, want %v (detail %q)", goos, got.Status, StatusSkip, got.Detail)
		}
	}

	// On FreeBSD it still reports the missing firmware.
	got := check.Run(context.Background(), &fakeSystem{goos: "freebsd"})
	if got.Status != StatusFail {
		t.Errorf("on freebsd with no firmware: status = %v, want %v", got.Status, StatusFail)
	}
}
