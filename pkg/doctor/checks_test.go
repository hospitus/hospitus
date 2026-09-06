package doctor

import (
	"context"
	"strings"
	"testing"
)

func findCheck(t *testing.T, checks []Check, id string) Check {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %q not found", id)
	return Check{}
}

func TestDefaultChecksWellFormed(t *testing.T) {
	checks := DefaultChecks()
	if len(checks) < 15 {
		t.Fatalf("expected a substantial check set, got %d", len(checks))
	}
	seen := map[string]bool{}
	for _, c := range checks {
		if c.ID == "" || c.Run == nil {
			t.Errorf("check %q missing ID or Run", c.Name)
		}
		if seen[c.ID] {
			t.Errorf("duplicate check ID %q", c.ID)
		}
		seen[c.ID] = true
		if len(c.Providers) == 0 {
			t.Errorf("check %q has no providers", c.ID)
		}
	}
}

func TestOSCheck(t *testing.T) {
	c := findCheck(t, DefaultChecks(), "os")
	sys := newFakeSystem()
	if got := c.Run(context.Background(), sys).Status; got != StatusOK {
		t.Errorf("freebsd: got %v, want OK", got)
	}
	sys.goos = "darwin"
	if got := c.Run(context.Background(), sys).Status; got != StatusWarn {
		t.Errorf("darwin: got %v, want WARN", got)
	}
}

func TestZFSPoolCheck(t *testing.T) {
	c := findCheck(t, DefaultChecks(), "zfs-pool")
	sys := newFakeSystem()
	// default parent zroot/hospitus → pool zroot
	if got := c.Run(context.Background(), sys).Status; got != StatusFail {
		t.Errorf("no pool: got %v, want FAIL", got)
	}
	sys.pools["zroot"] = true
	if got := c.Run(context.Background(), sys).Status; got != StatusOK {
		t.Errorf("pool present: got %v, want OK", got)
	}
	// HOSPITUS_ZFS_PARENT override to a different pool
	sys.env["HOSPITUS_ZFS_PARENT"] = "tank/hospitus"
	if got := c.Run(context.Background(), sys).Status; got != StatusFail {
		t.Errorf("override to missing pool: got %v, want FAIL", got)
	}
	sys.pools["tank"] = true
	if got := c.Run(context.Background(), sys).Status; got != StatusOK {
		t.Errorf("override pool present: got %v, want OK", got)
	}
	// non-freebsd → skip
	sys.goos = "darwin"
	if got := c.Run(context.Background(), sys).Status; got != StatusSkip {
		t.Errorf("darwin: got %v, want SKIP", got)
	}
}

func TestDataDirCheckAndFix(t *testing.T) {
	c := findCheck(t, DefaultChecks(), "data-dir")
	sys := newFakeSystem()
	if !c.Fixable() {
		t.Fatal("data-dir must be fixable")
	}
	if got := c.Run(context.Background(), sys).Status; got != StatusFail {
		t.Errorf("missing dir: got %v, want FAIL", got)
	}
	if err := c.Fix(context.Background(), sys); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if got := c.Run(context.Background(), sys).Status; got != StatusOK {
		t.Errorf("after mkdir: got %v, want OK", got)
	}
}

func TestCommandCheckSeverityAndSkip(t *testing.T) {
	checks := DefaultChecks()
	reqJail := findCheck(t, checks, "cmd-jail")   // required, jail (freebsd-only)
	optSwtpm := findCheck(t, checks, "cmd-swtpm") // optional, bhyve

	sys := newFakeSystem()
	if got := reqJail.Run(context.Background(), sys).Status; got != StatusFail {
		t.Errorf("missing required jail binary: got %v, want FAIL", got)
	}
	if got := optSwtpm.Run(context.Background(), sys).Status; got != StatusWarn {
		t.Errorf("missing optional binary: got %v, want WARN", got)
	}
	sys.commands["jail"] = true
	if got := reqJail.Run(context.Background(), sys).Status; got != StatusOK {
		t.Errorf("present binary: got %v, want OK", got)
	}
	// FreeBSD-only command check skips on darwin.
	sys.goos = "darwin"
	if got := reqJail.Run(context.Background(), sys).Status; got != StatusSkip {
		t.Errorf("jail binary on darwin: got %v, want SKIP", got)
	}
}

func TestModuleCheckAndFix(t *testing.T) {
	c := findCheck(t, DefaultChecks(), "kmod-vmm")
	sys := newFakeSystem()
	if !c.Fixable() {
		t.Fatal("kmod-vmm must be fixable")
	}
	if got := c.Run(context.Background(), sys).Status; got != StatusFail {
		t.Errorf("module not loaded: got %v, want FAIL", got)
	}
	if err := c.Fix(context.Background(), sys); err != nil {
		t.Fatal(err)
	}
	if got := c.Run(context.Background(), sys).Status; got != StatusOK {
		t.Errorf("after kldload: got %v, want OK", got)
	}
}

func TestSysctlChecks(t *testing.T) {
	checks := DefaultChecks()
	fwd := findCheck(t, checks, "ip-forward") // required, fixable
	racct := findCheck(t, checks, "racct")    // optional, NOT fixable

	if !fwd.Fixable() {
		t.Error("ip-forward should be fixable")
	}
	if racct.Fixable() {
		t.Error("racct must not be auto-fixable (needs reboot)")
	}

	sys := newFakeSystem()
	sys.sysctls["net.inet.ip.forwarding"] = "0"
	if got := fwd.Run(context.Background(), sys).Status; got != StatusFail {
		t.Errorf("forwarding off: got %v, want FAIL", got)
	}
	sys.sysctls["net.inet.ip.forwarding"] = "1"
	if got := fwd.Run(context.Background(), sys).Status; got != StatusOK {
		t.Errorf("forwarding on: got %v, want OK", got)
	}
}

func TestPFChecks(t *testing.T) {
	checks := DefaultChecks()
	pfEnabled := findCheck(t, checks, "pf-enabled")
	pfAnchor := findCheck(t, checks, "pf-anchor")

	// pf-anchor is check-only — it must never carry an auto-fix that edits pf.conf.
	if pfAnchor.Fixable() {
		t.Error("pf-anchor must be check-only (never edit pf.conf)")
	}

	sys := newFakeSystem()
	if got := pfEnabled.Run(context.Background(), sys).Status; got != StatusWarn {
		t.Errorf("pf disabled: got %v, want WARN", got)
	}
	if !pfEnabled.Fixable() {
		t.Fatal("pf-enabled should be fixable via sysrc/service")
	}
	if err := pfEnabled.Fix(context.Background(), sys); err != nil {
		t.Fatal(err)
	}
	// The fix must not have run anything touching pf.conf (only sysrc/service).
	for _, cmd := range sys.ran {
		if cmd == "pfctl" {
			t.Error("pf-enabled fix must not invoke pfctl on pf.conf")
		}
	}
}

func TestPFAnchorCheck(t *testing.T) {
	c := findCheck(t, DefaultChecks(), "pf-anchor")

	tests := []struct {
		name     string
		goos     string
		declared bool
		want     Status
	}{
		{"anchor declared", "freebsd", true, StatusOK},
		{"anchor missing", "freebsd", false, StatusWarn},
		{"not FreeBSD", "darwin", false, StatusSkip},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sys := newFakeSystem()
			sys.goos = tt.goos
			sys.pfAnchors["hospitus"] = tt.declared

			if got := c.Run(context.Background(), sys).Status; got != tt.want {
				t.Errorf("pf-anchor status = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPFAnchorCheckIsNeverFixable(t *testing.T) {
	// Declaring the anchor means editing the operator's pf.conf, which Hospitus
	// must never do; the check reports and stops there.
	if findCheck(t, DefaultChecks(), "pf-anchor").Fixable() {
		t.Error("pf-anchor must not be auto-fixable: Hospitus never edits pf.conf")
	}
}

// TestDebootstrapVerifierCheck covers the prerequisite debootstrap needs beyond
// itself: without a verifier it aborts on the release signature, and the doctor
// used to report only "debootstrap found" while creating a Linux jail failed.
func TestDebootstrapVerifierCheck(t *testing.T) {
	check := debootstrapVerifierCheck([]string{ProviderJail})

	tests := []struct {
		name     string
		commands []string
		want     Status
	}{
		{
			name:     "skipped when debootstrap is absent",
			commands: nil,
			want:     StatusSkip,
		},
		{
			name:     "warns when debootstrap has no verifier",
			commands: []string{"debootstrap"},
			want:     StatusWarn,
		},
		{
			name: "gpgv alone is not enough — debootstrap calls gpgv2",
			// FreeBSD's gnupg package installs the binary under the other name.
			commands: []string{"debootstrap", "gpgv"},
			want:     StatusWarn,
		},
		{
			name:     "gpgv2 satisfies it",
			commands: []string{"debootstrap", "gpgv2"},
			want:     StatusOK,
		},
		{
			name:     "sqv satisfies it",
			commands: []string{"debootstrap", "sqv"},
			want:     StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeSystem()
			for _, c := range tt.commands {
				s.commands[c] = true
			}
			if got := check.Run(context.Background(), s).Status; got != tt.want {
				t.Errorf("status = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLinuxKeyringCheck covers the last prerequisite for a Linux jail: hospitus
// refuses to bootstrap without an archive keyring, and the two distributions
// are not equally available on FreeBSD — ubuntu-keyring is a package, Debian's
// is not in ports.
func TestLinuxKeyringCheck(t *testing.T) {
	const (
		debianKeyring = "/usr/local/share/keyrings/debian-archive-keyring.gpg"
		ubuntuKeyring = "/usr/local/share/keyrings/ubuntu-archive-keyring.gpg"
	)
	check := linuxKeyringCheck([]string{ProviderJail})

	tests := []struct {
		name       string
		debootstap bool
		paths      []string
		want       Status
		wantDetail string
	}{
		{
			name:  "skipped without debootstrap",
			paths: []string{ubuntuKeyring},
			want:  StatusSkip,
		},
		{
			name:       "warns when neither keyring is present",
			debootstap: true,
			want:       StatusWarn,
		},
		{
			name:       "ubuntu alone warns rather than passing",
			debootstap: true,
			paths:      []string{ubuntuKeyring},
			want:       StatusWarn,
			wantDetail: "Debian jails will not bootstrap",
		},
		{
			name:       "both keyrings",
			debootstap: true,
			paths:      []string{debianKeyring, ubuntuKeyring},
			want:       StatusOK,
			wantDetail: "Debian and Ubuntu keyrings found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeSystem()
			if tt.debootstap {
				s.commands["debootstrap"] = true
			}
			for _, p := range tt.paths {
				s.paths[p] = true
			}

			got := check.Run(context.Background(), s)
			if got.Status != tt.want {
				t.Errorf("status = %v, want %v", got.Status, tt.want)
			}
			if tt.wantDetail != "" && !strings.Contains(got.Detail, tt.wantDetail) {
				t.Errorf("detail = %q, want it to mention %q", got.Detail, tt.wantDetail)
			}
		})
	}
}
