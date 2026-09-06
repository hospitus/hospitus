package jail

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/dataset"

	"github.com/hospitus/hospitus/pkg/provider/execx"
)

// cmdLine renders a recorded call as a single "name arg1 arg2 ..." string so
// tests can assert on the exact command line that was issued.
func cmdLine(c execx.Call) string {
	if len(c.Args) == 0 {
		return c.Name
	}
	return c.Name + " " + strings.Join(c.Args, " ")
}

// lastCmd returns the command line of the most recent recorded call.
func lastCmd(f *execx.Fake) string {
	if len(f.Calls) == 0 {
		return ""
	}
	return cmdLine(f.Calls[len(f.Calls)-1])
}

// hasCmd reports whether any recorded call matches the given command line exactly.
func hasCmd(f *execx.Fake, want string) bool {
	for _, c := range f.Calls {
		if cmdLine(c) == want {
			return true
		}
	}
	return false
}

// runningProvider builds a JailProvider whose named jail is persisted (so
// GetInstanceState finds it) and whose jls calls report the jail as running.
// Any non-jls command is delegated to fn (nil => success with empty output).
func runningProvider(t *testing.T, name string, fn func(cmd string, args []string) ([]byte, error)) (*JailProvider, *execx.Fake) {
	t.Helper()
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: dataset.Child("jails")}
	if err := p.saveJailConfig(&jailConfig{Name: name}, filepath.Join(stateDir, name+".json")); err != nil {
		t.Fatalf("saveJailConfig: %v", err)
	}
	fake := &execx.Fake{Func: func(cmd string, args []string) ([]byte, error) {
		if cmd == "jls" {
			// Only the jail this helper is documented to report as running.
			// Answering for every name let provider code query the wrong jail
			// and still see a success.
			for _, a := range args {
				if a == name {
					// isJailRunning uses Run (err==nil => running); getJailID parses jid.
					return []byte("1\n"), nil
				}
			}
			return nil, fmt.Errorf("jail not found")
		}
		if fn != nil {
			return fn(cmd, args)
		}
		return nil, nil
	}}
	p.runner = fake
	return p, fake
}

// runningProviderStopped builds a JailProvider whose named jail is persisted
// but reported as stopped (jls fails). Any command is delegated to a fake that
// fails jls and succeeds otherwise.
func runningProviderStopped(t *testing.T, name string) (*JailProvider, *execx.Fake) {
	t.Helper()
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: dataset.Child("jails")}
	if err := p.saveJailConfig(&jailConfig{Name: name}, filepath.Join(stateDir, name+".json")); err != nil {
		t.Fatalf("saveJailConfig: %v", err)
	}
	fake := &execx.Fake{Func: func(cmd string, _ []string) ([]byte, error) {
		if cmd == "jls" {
			return nil, errAssertStopped
		}
		return nil, nil
	}}
	p.runner = fake
	return p, fake
}

var errAssertStopped = &stoppedError{}

type stoppedError struct{}

func (*stoppedError) Error() string { return "jail not running" }

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
		// Reported, not discarded: a silent failure leaves the test dataset on
		// the pool and the next run inherits it. A dataset that was never
		// created is not a failure — this helper only exports the parent, and
		// the dataset appears later, and only if the test calls Initialize.
		out, err := exec.Command("zfs", "destroy", "-r", parent).CombinedOutput()
		if err != nil && !strings.Contains(strings.ToLower(string(out)), "does not exist") {
			t.Errorf("could not remove the test dataset %s: %v (%s)", parent, err, strings.TrimSpace(string(out)))
		}
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
		// Truncation alone collides: two long test names sharing their first 48
		// sanitized characters would get the same ZFS parent, and concurrent
		// tests would create and destroy each other's datasets.
		sum := sha256.Sum256([]byte(safe))
		safe = safe[:maxComponent-9] + "-" + hex.EncodeToString(sum[:4])
	}
	return safe
}
