package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestExampleManifests deploys every example manifest and then asks each
// instance the question the manifest itself declares.
//
// The shell scripts this replaces stopped at `hospitus podman list | grep nginx`,
// which proves a record exists in the database and nothing about the service:
// a container whose server never started passes it. Every example now carries a
// health_check describing what working means, so the check the manifest
// declares is the check that runs, inside the instance. Nothing is invented
// here, and nothing is asserted twice.
//
// Cases are discovered by walking examples/manifests, so a new example is
// tested the day it is added, without a list to keep in step.
func TestExampleManifests(t *testing.T) {
	ensureDaemonRunning(t)

	for _, dir := range exampleManifestDirs(t) {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			runManifestCase(t, dir, name)
		})
	}
}

// runManifestCase deploys one manifest, verifies it, and always tears it down.
func runManifestCase(t *testing.T, dir, shortName string) {
	t.Helper()

	// A name short enough for a jail and unique enough to survive a rerun that
	// left something behind.
	instanceBase := fmt.Sprintf("ex-%s-%d", truncate(shortName, 12), time.Now().UnixNano()%100000)

	targets, err := manifestTargets(filepath.Join(dir, "template.toml"), instanceBase)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(targets) == 0 {
		t.Fatal("manifest declares no instances")
	}

	for _, target := range targets {
		if reason := providerUnavailable(t, target.provider); reason != "" {
			t.Skipf("%s: %s", target.provider, reason)
		}
		// The manifest hands these straight to the guest. A host without them
		// cannot run the example, and pretending otherwise would either fail
		// every run or, worse, write to the wrong disk.
		for _, device := range target.needsDevice {
			if _, err := os.Stat(device); err != nil {
				t.Skipf("%s needs %s, which this host does not have", target.name, device)
			}
		}
		if arch := target.needsEmulation; arch != "" && !emulationIsRegistered(arch) {
			t.Skipf("%s runs %s binaries, and no binmiscctl(8) interpreter is "+
				"registered for them on this host", target.name, arch)
		}
		for _, function := range target.needsPCI {
			if !pciFunctionIsBound(function) {
				t.Skipf("%s needs PCI function %s bound to ppt(4); add it to pptdevs in "+
					"/boot/loader.conf and reboot", target.name, function)
			}
		}
	}

	t.Cleanup(func() {
		for _, target := range targets {
			removeInstance(target.provider, target.name)
		}
	})

	// --pull because nothing here can answer a prompt: without it, a manifest
	// whose image is not on the host fails at the question instead of running.
	apply := exec.Command(hospitusBinary(), "apply", "--start", "--pull",
		"--var", "name="+instanceBase, filepath.Join(dir, "template.toml"))
	if out, err := apply.CombinedOutput(); err != nil {
		// Handing a real disk to a guest takes more than the device existing:
		// the daemon also demands it be named in bhyve.allowed_physical_disks.
		// Refusing a disk nobody allow-listed is the guard working, so the
		// example is skipped rather than failed — a test may not decide that
		// someone's disk is expendable.
		if strings.Contains(string(out), "is not allow-listed for passthrough") {
			t.Skipf("a disk this manifest passes through is not in "+
				"bhyve.allowed_physical_disks on this host:\n%s", out)
		}
		t.Fatalf("apply failed: %v\n%s", err, out)
	}

	for _, target := range targets {
		waitForState(t, target.provider, target.name, "running", 5*time.Minute)

		if len(target.check) == 0 {
			// A provider with no exec cannot be questioned from the outside;
			// the health-check lint test exempts the same set.
			t.Logf("%s (%s): running; no in-instance check for this provider",
				target.name, target.provider)
			continue
		}
		verifyInside(t, target)
	}

	seedAndReadBack(t, dir, targets)
}

// seedAndReadBack runs an example's own seed script, if it has one.
//
// A health check answers "is the service up". It does not answer "does it keep
// what it is given", and the difference is where most of the value is: a
// Nextcloud that serves its login page and loses every upload passes one and
// fails the other. So an example may ship a seed.sh that writes something,
// reads it back, and checks where it landed.
//
// The script runs on the host rather than inside an instance, because seeding a
// stack usually spans tiers — create the account through the application, fetch
// the file through the web front. It addresses instances by the role the
// manifest gives them, since the created names carry a per-run prefix:
//
//	HOSPITUS_BIN         path to the hospitus CLI
//	HOSPITUS_ROLE_<ROLE> created name of the instance the manifest calls <role>
//	HOSPITUS_ROLES       every role, space-separated
//
// An example with no seed.sh is not seeded.
func seedAndReadBack(t *testing.T, dir string, targets []manifestTarget) {
	t.Helper()

	script := filepath.Join(dir, "seed.sh")
	if _, err := os.Stat(script); err != nil {
		return
	}

	env := append(os.Environ(), "HOSPITUS_BIN="+hospitusBinary())
	roles := make([]string, 0, len(targets))
	for _, target := range targets {
		roles = append(roles, target.role)
		env = append(env, fmt.Sprintf("HOSPITUS_ROLE_%s=%s",
			strings.ToUpper(strings.ReplaceAll(target.role, "-", "_")), target.name))
	}
	env = append(env, "HOSPITUS_ROLES="+strings.Join(roles, " "))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("the example stored nothing it could read back: %v\n%s", err, out)
		return
	}
	t.Logf("seeded and read back:\n%s", out)
}

// verifyInside runs the manifest's own health check inside the instance, giving
// the service the start_period the manifest asked for before believing a
// failure.
func verifyInside(t *testing.T, target manifestTarget) {
	t.Helper()

	deadline := time.Now().Add(target.startPeriod + 2*time.Minute)
	var lastOut []byte
	var lastErr error

	for time.Now().Before(deadline) {
		// Bound each attempt by the timeout the manifest declares. wget and
		// fetch wait indefinitely for a server that accepts nothing, so an
		// unbounded attempt hangs rather than fails, and one silent service
		// stalls every case behind it.
		attemptCtx, cancel := context.WithTimeout(context.Background(), target.timeout)
		args := append([]string{target.provider, "exec", target.name}, target.check...)
		lastOut, lastErr = exec.CommandContext(attemptCtx, hospitusBinary(), args...).CombinedOutput()
		cancel()

		if lastErr == nil {
			t.Logf("%s: %s answered", target.name, strings.Join(target.check, " "))
			return
		}
		time.Sleep(5 * time.Second)
	}

	t.Errorf("%s: the manifest's own health check never succeeded: %s\nlast error: %v\n%s",
		target.name, strings.Join(target.check, " "), lastErr, lastOut)
}

// providerUnavailable explains why this host cannot run the provider, or "".
func providerUnavailable(t *testing.T, provider string) string {
	t.Helper()

	switch provider {
	case "jail", "bhyve":
		if runtime.GOOS != "freebsd" {
			return "FreeBSD only"
		}
		if os.Geteuid() != 0 {
			return "requires root"
		}
	case "podman", "qemu":
		binary := provider
		if provider == "qemu" {
			binary = "qemu-system-x86_64"
		}
		if !checkCommand(t, binary) {
			return binary + " not installed"
		}
	default:
		return "unknown provider " + provider
	}
	return ""
}

// removeInstance tears one instance down; teardown is best effort, because a
// case that failed early may have created nothing at all.
func removeInstance(provider, name string) {
	cmd := exec.Command(hospitusBinary(), provider, "destroy", "-y", "-f", name)
	cmd.CombinedOutput() //nolint:errcheck
}

// exampleManifestDirs returns every directory holding a template.toml.
func exampleManifestDirs(t *testing.T) []string {
	t.Helper()

	root := filepath.Join("..", "..", "examples", "manifests")
	var dirs []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Name() == "template.toml" {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return dirs
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
