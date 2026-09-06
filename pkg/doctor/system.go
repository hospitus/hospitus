package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// System abstracts the host-environment probes and mutations the checks need.
// The real implementation shells out to FreeBSD tools; tests supply a fake.
type System interface {
	// GOOS returns the target operating system (runtime.GOOS).
	GOOS() string
	// HasCommand reports whether an executable is on PATH.
	HasCommand(name string) bool
	// Sysctl returns the value of a sysctl OID, or an error if unavailable.
	Sysctl(name string) (string, error)
	// KernelModuleLoaded reports whether a kernel module is loaded (kldstat).
	KernelModuleLoaded(name string) bool
	// ServiceEnabled reports whether an rc.d service is enabled (rcvar=YES).
	ServiceEnabled(rcvar string) bool
	// PathExists reports whether a filesystem path exists.
	PathExists(path string) bool
	// DirWritable reports whether dir exists and is writable (or can be created).
	DirWritable(path string) bool
	// ZFSPoolExists reports whether a ZFS pool of the given name exists.
	ZFSPoolExists(pool string) bool
	// PFAnchorDeclared reports whether the loaded PF ruleset declares an anchor
	// of the given name. Reading the live ruleset never touches pf.conf.
	PFAnchorDeclared(anchor string) bool
	// PFFilterRules returns the filter rules PF currently has loaded, one per
	// line. Read-only, like PFAnchorDeclared.
	PFFilterRules() []string
	// BinmiscActivators returns the names imgact_binmisc(4) has interpreters
	// registered under, which is what lets a foreign-architecture binary run.
	BinmiscActivators() []string
	// IsRoot reports whether the check is running with the privileges some
	// inspections need. pfctl reads nothing useful without them, and a check
	// that cannot look must say so rather than report what it did not see.
	IsRoot() bool
	// Getenv reads an environment variable.
	Getenv(name string) string
	// Run executes a command (used by fixes). Output is discarded; only the
	// error matters.
	Run(ctx context.Context, name string, args ...string) error
}

// realSystem is the production System backed by the host.
type realSystem struct{}

// NewSystem returns a System backed by the real host.
func NewSystem() System { return realSystem{} }

func (realSystem) GOOS() string { return runtime.GOOS }

func (realSystem) HasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func (realSystem) Sysctl(name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sysctl", "-n", name).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (realSystem) KernelModuleLoaded(name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The checks name what kldload takes and what loader.conf persists: a kld
	// file. kldstat -n looks one up by that name; -m looks up a module inside a
	// file, and the two often differ — linux64.ko provides no module called
	// linux64, so -m reported it unloaded while it was running.
	if exec.CommandContext(ctx, "kldstat", "-q", "-n", name).Run() == nil {
		return true
	}

	// A module compiled into the kernel has no file to find, so fall back to
	// the module lookup rather than calling it missing.
	return exec.CommandContext(ctx, "kldstat", "-q", "-m", name).Run() == nil
}

func (realSystem) ServiceEnabled(rcvar string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sysrc", "-n", rcvar).Output()
	if err != nil {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(string(out)))
	return v == "yes" || v == "true" || v == "on" || v == "1"
}

func (realSystem) PathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (realSystem) DirWritable(path string) bool {
	info, err := os.Stat(path)
	if err == nil {
		// Something is there. Only a directory can hold the daemon's data: a
		// regular file at the path passed as writable for root, and the
		// daemon's own MkdirAll then failed with ENOTDIR.
		if !info.IsDir() {
			return false
		}
		// Probe writability by creating a temp file.
		f, err := os.CreateTemp(path, ".hospitus-doctor-*")
		if err != nil {
			return false
		}
		name := f.Name()
		_ = f.Close()
		_ = os.Remove(name)
		return true
	}
	// Does not exist yet: writable if the nearest existing parent is writable.
	parent := filepath.Dir(path)
	for parent != "/" && parent != "." {
		if info, err := os.Stat(parent); err == nil {
			return info.IsDir() && unixWritable(info)
		}
		parent = filepath.Dir(parent)
	}
	return unixDirWritable("/")
}

func (realSystem) ZFSPoolExists(pool string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "zpool", "list", "-H", "-o", "name").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == pool {
			return true
		}
	}
	return false
}

// IsRoot reports whether the process has an effective uid of 0.
func (realSystem) IsRoot() bool {
	return os.Geteuid() == 0
}

// BinmiscActivators asks binmiscctl(8) what is registered. An unprepared host
// answers nothing, which is not an error: registering an interpreter is a
// deliberate act.
func (realSystem) BinmiscActivators() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "binmiscctl", "list").Output()
	if err != nil {
		return nil
	}

	// One "name: <activator>" line per entry, and the conventional name is the
	// architecture it interprets.
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if rest, found := strings.CutPrefix(line, "name:"); found {
			if name := strings.TrimSpace(rest); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// PFFilterRules reads the loaded filter ruleset. Like PFAnchorDeclared it only
// asks pf what it is enforcing; pf.conf is never opened.
func (realSystem) PFFilterRules() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "pfctl", "-s", "rules").Output()
	if err != nil {
		return nil
	}

	var rules []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			rules = append(rules, line)
		}
	}
	return rules
}

// PFAnchorDeclared inspects the ruleset PF currently has loaded. It is
// read-only: pf.conf itself is never opened, let alone written, which keeps the
// invariant that Hospitus manages only its own anchor.
func (realSystem) PFAnchorDeclared(anchor string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The nat anchor carries NAT and redirections, the filter anchor the pass
	// rules; a pf.conf declaring only one leaves half of Hospitus's rules
	// unevaluated. `pfctl -s Anchors` is deliberately not consulted: it lists
	// the anchor hospitusd loads itself, whether or not the main ruleset ever
	// references it, so it reported OK where jails had no NAT at all.
	return pfRulesetDeclares(ctx, "nat", "nat-anchor", anchor) &&
		pfRulesetDeclares(ctx, "rules", "anchor", anchor)
}

// pfRulesetDeclares reports whether `pfctl -s <table>` prints a line declaring
// the anchor, in either the `anchor "hospitus"` or the `anchor "hospitus/*"` form.
func pfRulesetDeclares(ctx context.Context, table, keyword, anchor string) bool {
	out, err := exec.CommandContext(ctx, "pfctl", "-s", table).Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == keyword &&
			(fields[1] == `"`+anchor+`"` || fields[1] == `"`+anchor+`/*"`) {
			return true
		}
	}
	return false
}

func (realSystem) Getenv(name string) string { return os.Getenv(name) }

func (realSystem) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func unixDirWritable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return unixWritable(info)
}

// unixWritable is a coarse writability check for the current euid. Root (euid 0)
// is treated as always able to write, matching how `hospitus init` runs.
func unixWritable(info os.FileInfo) bool {
	if os.Geteuid() == 0 {
		return true
	}
	return info.Mode().Perm()&0o200 != 0
}

// poolOf returns the pool name from a "pool/dataset/..." parent string.
func poolOf(parent string) string {
	if i := strings.IndexByte(parent, '/'); i >= 0 {
		return parent[:i]
	}
	return parent
}

// atoiDefault parses s as an int, returning def on failure.
func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
