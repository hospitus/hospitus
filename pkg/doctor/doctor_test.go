package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSystem is a scriptable System for tests.
type fakeSystem struct {
	goos     string
	commands map[string]bool
	sysctls  map[string]string
	modules  map[string]bool
	services map[string]bool
	paths    map[string]bool
	writable map[string]bool
	pools    map[string]bool
	env      map[string]string
	// pfAnchors records which PF anchors the loaded ruleset declares.
	pfAnchors map[string]bool
	// pfRules is the loaded filter ruleset, one rule per entry.
	pfRules []string
	// binmisc names the architectures imgact_binmisc has interpreters for.
	binmisc []string
	// isRoot is whether the checks run privileged. Several read PF, which
	// answers nothing without it.
	isRoot bool
	// ran records fix commands; runErr forces Run to fail for a command name.
	ran    []string
	runErr map[string]error
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		goos: "freebsd", commands: map[string]bool{}, sysctls: map[string]string{},
		modules: map[string]bool{}, services: map[string]bool{}, paths: map[string]bool{},
		writable: map[string]bool{}, pools: map[string]bool{}, env: map[string]string{},
		pfAnchors: map[string]bool{},
		runErr:    map[string]error{},
		// Privileged by default: most checks read what only root can, and a
		// test that forgets it would be measuring the wrong thing.
		isRoot: true,
	}
}

func (f *fakeSystem) GOOS() string             { return f.goos }
func (f *fakeSystem) HasCommand(n string) bool { return f.commands[n] }
func (f *fakeSystem) Sysctl(_ context.Context, n string) (string, error) {
	v, ok := f.sysctls[n]
	if !ok {
		return "", errors.New("unknown oid")
	}
	return v, nil
}
func (f *fakeSystem) KernelModuleLoaded(_ context.Context, n string) bool { return f.modules[n] }
func (f *fakeSystem) ServiceEnabled(_ context.Context, n string) bool     { return f.services[n] }
func (f *fakeSystem) PathExists(p string) bool                            { return f.paths[p] }
func (f *fakeSystem) DirWritable(p string) bool                           { return f.writable[p] }
func (f *fakeSystem) ZFSPoolExists(_ context.Context, p string) bool      { return f.pools[p] }
func (f *fakeSystem) PFAnchorDeclared(_ context.Context, a string) bool   { return f.pfAnchors[a] }
func (f *fakeSystem) PFFilterRules(context.Context) []string              { return f.pfRules }
func (f *fakeSystem) BinmiscActivators(context.Context) []string          { return f.binmisc }
func (f *fakeSystem) IsRoot() bool                                        { return f.isRoot }
func (f *fakeSystem) Getenv(n string) string                              { return f.env[n] }
func (f *fakeSystem) Run(_ context.Context, name string, args ...string) error {
	f.ran = append(f.ran, name)
	if err := f.runErr[name]; err != nil {
		return err
	}
	// Simulate the effect for the checks used in tests.
	if name == "mkdir" && len(args) >= 2 {
		f.paths[args[len(args)-1]] = true
		f.writable[args[len(args)-1]] = true
	}
	if name == "kldload" && len(args) == 1 {
		f.modules[args[0]] = true
	}
	return nil
}

func okCheck(id string, req bool, providers ...string) Check {
	return Check{
		ID: id, Name: id, Required: req, Providers: providers,
		Run: func(context.Context, System) Result { return Result{Status: StatusOK} },
	}
}

func TestAppliesTo(t *testing.T) {
	core := okCheck("c", true, ProviderCore)
	bhyve := okCheck("b", true, ProviderBhyve)
	if !core.appliesTo(map[string]bool{ProviderJail: true}) {
		t.Error("core check must always apply")
	}
	if bhyve.appliesTo(map[string]bool{ProviderJail: true}) {
		t.Error("bhyve check must not apply when only jail selected")
	}
	if !bhyve.appliesTo(nil) {
		t.Error("nil selection means all providers")
	}
}

func TestDiagnoseRequiredFailure(t *testing.T) {
	failing := Check{
		ID: "x", Required: true, Providers: []string{ProviderCore},
		Run: func(context.Context, System) Result { return Result{Status: StatusFail} },
	}
	r := NewRunner(newFakeSystem(), []Check{okCheck("ok", true, ProviderCore), failing})
	rep := r.Diagnose(context.Background(), nil)
	if len(rep.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(rep.Results))
	}
	if !rep.HasRequiredFailures() {
		t.Error("expected a required failure")
	}
}

func TestApplyFixesAppliesAndConfirms(t *testing.T) {
	sys := newFakeSystem()
	// A fixable check: fails until "kldload vmm" runs.
	c := Check{
		ID: "kmod", Required: true, Providers: []string{ProviderCore},
		Run: func(ctx context.Context, s System) Result {
			if s.KernelModuleLoaded(ctx, "vmm") {
				return Result{Status: StatusOK}
			}
			return Result{Status: StatusFail}
		},
		Fix: func(ctx context.Context, s System) error { return s.Run(ctx, "kldload", "vmm") },
	}
	r := NewRunner(sys, []Check{c})

	rep := r.Diagnose(context.Background(), nil)
	if rep.Results[0].Result.Status != StatusFail {
		t.Fatalf("expected initial FAIL, got %v", rep.Results[0].Result.Status)
	}

	// confirm=false: not applied.
	skipped := r.ApplyFixes(context.Background(), rep, func(Check) bool { return false })
	if skipped.Results[0].Result.Status != StatusFail {
		t.Errorf("confirm=false must not apply the fix")
	}
	if len(sys.ran) != 0 {
		t.Errorf("no fix should have run, got %v", sys.ran)
	}

	// confirm=nil (auto): applied and re-checked → FIXED.
	fixed := r.ApplyFixes(context.Background(), rep, nil)
	if fixed.Results[0].Result.Status != StatusFixed {
		t.Errorf("expected FIXED after auto-apply, got %v", fixed.Results[0].Result.Status)
	}
}

func TestApplyFixesReportsFixFailure(t *testing.T) {
	sys := newFakeSystem()
	sys.runErr["kldload"] = errors.New("boom")
	c := Check{
		ID: "kmod", Required: true, Providers: []string{ProviderCore},
		Run: func(context.Context, System) Result { return Result{Status: StatusFail} },
		Fix: func(ctx context.Context, s System) error { return s.Run(ctx, "kldload", "vmm") },
	}
	r := NewRunner(sys, []Check{c})
	rep := r.Diagnose(context.Background(), nil)
	out := r.ApplyFixes(context.Background(), rep, nil)
	if out.Results[0].Result.Status != StatusFail {
		t.Errorf("failed fix must remain FAIL")
	}
}

// TestAutoIdempotent verifies a second --auto run is a no-op (everything OK).
func TestAutoIdempotent(t *testing.T) {
	sys := newFakeSystem()
	sys.modules["vmm"] = true // already satisfied
	c := Check{
		ID: "kmod", Required: true, Providers: []string{ProviderCore},
		Run: func(ctx context.Context, s System) Result {
			if s.KernelModuleLoaded(ctx, "vmm") {
				return Result{Status: StatusOK}
			}
			return Result{Status: StatusFail}
		},
		Fix: func(ctx context.Context, s System) error { return s.Run(ctx, "kldload", "vmm") },
	}
	r := NewRunner(sys, []Check{c})
	rep := r.Diagnose(context.Background(), nil)
	out := r.ApplyFixes(context.Background(), rep, nil)
	if out.Results[0].Result.Status != StatusOK {
		t.Errorf("already-satisfied check must stay OK, got %v", out.Results[0].Result.Status)
	}
	if len(sys.ran) != 0 {
		t.Errorf("no fix should run when already satisfied, got %v", sys.ran)
	}
}

// TestPodmanPFCheck covers the failure it exists to name: a container that
// starts, listens, and serves nothing because PF drops its traffic.
func TestPodmanPFCheck(t *testing.T) {
	const denyAll = "block return all"
	const passPodman = "pass quick inet from 10.88.0.0/16 to any flags S/SA keep state"
	const passJails = "pass in inet from 10.0.0.0/24 to any flags S/SA keep state"

	tests := []struct {
		name  string
		rules []string
		want  Status
	}{
		{
			name:  "default deny with no podman rule",
			rules: []string{passJails, denyAll},
			want:  StatusWarn,
		},
		{
			name:  "default deny with a podman rule",
			rules: []string{passJails, passPodman, denyAll},
			want:  StatusOK,
		},
		{
			name:  "no default deny",
			rules: []string{passJails},
			want:  StatusOK,
		},
		{
			name:  "no ruleset loaded",
			rules: nil,
			want:  StatusSkip,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sys := &fakeSystem{
				goos:     "freebsd",
				commands: map[string]bool{"pfctl": true},
				services: map[string]bool{"pf_enable": true},
				pfRules:  tt.rules,
				isRoot:   true,
			}

			got := podmanPFCheck([]string{ProviderPodman}).Run(context.Background(), sys)
			if got.Status != tt.want {
				t.Errorf("status = %v, want %v (detail: %s)", got.Status, tt.want, got.Detail)
			}
			if tt.want == StatusWarn && got.Remediation == "" {
				t.Error("a warning with no remediation leaves the operator where they started")
			}
		})
	}
}

// TestPFIncludeFileCheck covers the ordering that takes a firewall down: pf
// refuses a ruleset whose include names a file that is not there, so the file
// must exist before pf.conf refers to it.
func TestPFIncludeFileCheck(t *testing.T) {
	check := pfIncludeFileCheck([]string{ProviderPodman})

	t.Run("file missing", func(t *testing.T) {
		sys := &fakeSystem{
			goos:     "freebsd",
			commands: map[string]bool{"pfctl": true},
			paths:    map[string]bool{},
		}

		got := check.Run(context.Background(), sys)
		if got.Status != StatusWarn {
			t.Fatalf("status = %v, want %v", got.Status, StatusWarn)
		}
		if !strings.Contains(got.Remediation, "install") {
			t.Error("the remediation must say how to create the file, not only that it is absent")
		}

		if err := check.Fix(context.Background(), sys); err != nil {
			t.Fatalf("fix: %v", err)
		}
		if len(sys.ran) == 0 || sys.ran[0] != "install" {
			t.Errorf("fix ran %v, want it to create the file with install", sys.ran)
		}
	})

	t.Run("file present", func(t *testing.T) {
		sys := &fakeSystem{
			goos:     "freebsd",
			commands: map[string]bool{"pfctl": true},
			paths:    map[string]bool{pfIncludeFile: true},
		}

		if got := check.Run(context.Background(), sys); got.Status != StatusOK {
			t.Errorf("status = %v, want %v", got.Status, StatusOK)
		}
	})
}

// TestCrossArchJailCheck covers the state that costs the most time to work out
// by hand: the emulator installed, and nothing telling the kernel to use it.
func TestCrossArchJailCheck(t *testing.T) {
	check := crossArchJailCheck([]string{ProviderJail})

	withEmulators := map[string]bool{
		"qemu-aarch64-static": true,
		"qemu-riscv64-static": true,
	}

	tests := []struct {
		name     string
		commands map[string]bool
		modules  map[string]bool
		binmisc  []string
		want     Status
	}{
		{
			name:     "no emulator installed",
			commands: map[string]bool{},
			want:     StatusSkip,
		},
		{
			name:     "emulator installed, module not loaded",
			commands: withEmulators,
			modules:  map[string]bool{},
			want:     StatusWarn,
		},
		{
			name:     "module loaded, nothing registered",
			commands: withEmulators,
			modules:  map[string]bool{"imgact_binmisc": true},
			binmisc:  nil,
			want:     StatusWarn,
		},
		{
			name:     "one architecture registered, the other not",
			commands: withEmulators,
			modules:  map[string]bool{"imgact_binmisc": true},
			binmisc:  []string{"aarch64"},
			want:     StatusWarn,
		},
		{
			name:     "both registered",
			commands: withEmulators,
			modules:  map[string]bool{"imgact_binmisc": true},
			binmisc:  []string{"aarch64", "riscv64"},
			want:     StatusOK,
		},
		{
			// binmiscctl takes any name; arm64 is the other spelling of the
			// same machine and must count.
			name:     "aarch64 registered under its other name",
			commands: withEmulators,
			modules:  map[string]bool{"imgact_binmisc": true},
			binmisc:  []string{"arm64", "riscv64"},
			want:     StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sys := &fakeSystem{
				goos:     "freebsd",
				commands: tt.commands,
				modules:  tt.modules,
				binmisc:  tt.binmisc,
			}

			got := check.Run(context.Background(), sys)
			if got.Status != tt.want {
				t.Fatalf("status = %v, want %v (detail: %s)", got.Status, tt.want, got.Detail)
			}
			if tt.want == StatusWarn && !strings.Contains(got.Remediation, "binmiscctl") &&
				!strings.Contains(got.Remediation, "kldload") {
				t.Error("a warning must say which command fixes it")
			}
		})
	}
}

// TestPFChecksSaySoWhenTheyCannotLook covers a report that misled: run without
// root, pfctl answers nothing, and the checks concluded the anchor was missing
// and the ruleset empty. An operator following that advice would add anchor
// lines to pf.conf that were already there.
func TestPFChecksSaySoWhenTheyCannotLook(t *testing.T) {
	// A host that is fully configured — the checks cannot see it.
	sys := &fakeSystem{
		goos:      "freebsd",
		commands:  map[string]bool{"pfctl": true},
		services:  map[string]bool{"pf_enable": true},
		pfAnchors: map[string]bool{"hospitus": true},
		pfRules:   []string{"pass in inet from 10.0.0.0/24", "block return all"},
		isRoot:    false,
	}

	for _, check := range []Check{
		podmanPFCheck([]string{ProviderPodman}),
		findCheck(t, DefaultChecks(), "pf-anchor"),
	} {
		t.Run(check.ID, func(t *testing.T) {
			got := check.Run(context.Background(), sys)
			if got.Status != StatusSkip {
				t.Errorf("status = %v, want %v — a check that cannot read must not "+
					"report what it did not see", got.Status, StatusSkip)
			}
			if !strings.Contains(got.Detail, "root") {
				t.Errorf("detail %q does not say privileges are what is missing", got.Detail)
			}
		})
	}
}
