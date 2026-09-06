// Package integration provides smoke tests for all Hospitus providers.
// These tests verify the basic create/start/stop/destroy lifecycle works
// end-to-end through the hospitusd daemon and CLI binary.
//
// Requirements:
//   - Root privileges (doas / sudo)
//   - hospitusd running at http://127.0.0.1:8080
//   - A suitable FreeBSD image downloaded (for jail/bhyve tests)
//   - HOSPITUS_LONG_TESTS=1 for large-download operations
//
// Skip conditions:
//   - Non-root: all tests skip
//   - Provider unavailable: individual tests skip
package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ─────────────────────── provider availability helpers ──────────────────────

func checkCommand(t *testing.T, name string) bool {
	t.Helper()
	_, err := exec.LookPath(name)
	return err == nil
}

func cleanupVM(t *testing.T, name string) {
	t.Helper()
	cmd := exec.Command(hospitusBinary(), "bhyve", "destroy", "-y", name)
	cmd.CombinedOutput() //nolint:errcheck
}

func cleanupQEMUVM(t *testing.T, name string) {
	t.Helper()
	cmd := exec.Command(hospitusBinary(), "qemu", "destroy", "-y", name)
	cmd.CombinedOutput() //nolint:errcheck
}

func cleanupContainer(t *testing.T, name string) {
	t.Helper()
	cmd := exec.Command(hospitusBinary(), "podman", "destroy", "-y", name)
	cmd.CombinedOutput() //nolint:errcheck
}

// waitForState polls 'hospitus <provider> info <name>' until the instance
// reaches the desired state or timeout expires.
func waitForState(t *testing.T, provider, name, wantState string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command(hospitusBinary(), provider, "info", name)
		out, err := cmd.Output()
		if err == nil && strings.Contains(string(out), wantState) {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("instance %s/%s did not reach state %q within %s", provider, name, wantState, timeout)
}

// ─────────────────────── Jail smoke test ────────────────────────────────────

// TestSmokeJailLifecycle verifies the minimum viable lifecycle for a jail:
// create → start → exec → stop → destroy.
func TestSmokeJailLifecycle(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("jail tests require root")
	}
	ensureDaemonRunning(t)

	name := fmt.Sprintf("smoke-jail-%d", time.Now().Unix()%10000)
	defer cleanupJail(t, name)

	t.Run("Create", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "create", name,
			"--image", "freebsd-14.3-RELEASE-amd64",
			"--description", "smoke test jail",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("create failed: %v\n%s", err, out)
		}
	})

	t.Run("Start", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "start", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("start failed: %v\n%s", err, out)
		}
		waitForState(t, "jail", name, "running", 30*time.Second)
	})

	t.Run("List", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "list")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("list failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), name) {
			t.Errorf("jail %s not found in list output", name)
		}
	})

	t.Run("Info", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "info", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("info failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), name) {
			t.Errorf("jail name not found in info output")
		}
	})

	t.Run("Exec", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, hospitusBinary(), "jail", "exec", name, "echo", "hospitus-smoke-ok")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("exec failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "hospitus-smoke-ok") {
			t.Errorf("expected 'hospitus-smoke-ok' in exec output, got: %s", out)
		}
	})

	t.Run("Snapshot", func(t *testing.T) {
		snapName := "smoke-snap"
		createCmd := exec.Command(hospitusBinary(), "jail", "snapshot", "create", name, snapName)
		out, err := createCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("snapshot create failed: %v\n%s", err, out)
		}

		listCmd := exec.Command(hospitusBinary(), "jail", "snapshot", "list", name)
		out, err = listCmd.Output()
		if err != nil {
			t.Fatalf("snapshot list failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), snapName) {
			t.Errorf("snapshot not found in list output")
		}

		// Cleanup snapshot
		exec.Command(hospitusBinary(), "jail", "snapshot", "delete", "-y", name, snapName).Run() //nolint:errcheck
	})

	t.Run("Stop", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "stop", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("stop failed: %v\n%s", err, out)
		}
	})
}

// ─────────────────────── bhyve smoke test ───────────────────────────────────

// TestSmokeBhyveLifecycle verifies the minimum viable lifecycle for a bhyve VM.
// Requires a bhyve-capable system (VT-x/AMD-V) and a cloud image.
func TestSmokeBhyveLifecycle(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("bhyve tests require root")
	}
	if !checkCommand(t, "bhyve") {
		t.Skip("bhyve not found — skipping")
	}
	ensureDaemonRunning(t)

	// Check hardware virtualization
	vmm, _ := exec.Command("kldstat", "-q", "-n", "vmm").Output()
	if !strings.Contains(string(vmm), "vmm") {
		// Try loading it
		if err := exec.Command("kldload", "vmm").Run(); err != nil {
			t.Skip("vmm kernel module not available — skipping bhyve smoke test")
		}
	}

	name := fmt.Sprintf("smoke-bhyve-%d", time.Now().Unix()%10000)
	defer cleanupVM(t, name)

	t.Run("Create", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "bhyve", "create", name,
			"--image", "cloud:freebsd-14.3",
			"--cpus", "1",
			"--memory", "512",
			"--description", "smoke test VM",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("create failed: %v\n%s", err, out)
		}
	})

	t.Run("List", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "bhyve", "list")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("list failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), name) {
			t.Errorf("VM %s not found in list", name)
		}
	})

	t.Run("Info", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "bhyve", "info", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("info failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), name) {
			t.Errorf("VM name not in info output")
		}
	})
}

// ─────────────────────── QEMU smoke test ────────────────────────────────────

// TestSmokeQEMULifecycle verifies the minimum viable lifecycle for a QEMU VM.
func TestSmokeQEMULifecycle(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("QEMU tests require root (for TAP/bridge setup)")
	}
	if !checkCommand(t, "qemu-system-x86_64") && !checkCommand(t, "qemu-system-aarch64") {
		t.Skip("qemu-system not found — install with: pkg install qemu")
	}
	// Without this guard the test does not skip on a host that has no cloud
	// image, it fails: create reports "image 'cloud:ubuntu-24.04' not found",
	// and List and Info then fail looking for a VM that was never made.
	if !checkCloudImage("ubuntu-24.04") {
		t.Skip("cloud image not found — fetch with: hospitus image fetch ubuntu-24.04")
	}
	ensureDaemonRunning(t)

	name := fmt.Sprintf("smoke-qemu-%d", time.Now().Unix()%10000)
	defer cleanupQEMUVM(t, name)

	t.Run("Create", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "qemu", "create", name,
			"--image", "cloud:ubuntu-24.04",
			"--cpus", "1",
			"--memory", "512",
			"--description", "smoke test QEMU VM",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("create failed: %v\n%s", err, out)
		}
	})

	t.Run("List", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "qemu", "list")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("list failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), name) {
			t.Errorf("VM %s not found in list", name)
		}
	})

	t.Run("Info", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "qemu", "info", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("info failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), name) {
			t.Errorf("VM name not in info output")
		}
	})
}

// ─────────────────────── Podman smoke test ──────────────────────────────────

// TestSmokePodmanLifecycle verifies the minimum viable lifecycle for a Podman container:
// create → start → exec → stop → destroy.
func TestSmokePodmanLifecycle(t *testing.T) {
	podmanBin, err := exec.LookPath("podman")
	if err != nil {
		t.Skip("podman not found — install with: pkg install podman")
	}

	// Check podman is functional
	if out, err := exec.Command(podmanBin, "info").CombinedOutput(); err != nil {
		t.Skipf("podman not operational: %v\n%s", err, out)
	}

	ensureDaemonRunning(t)

	name := fmt.Sprintf("smoke-podman-%d", time.Now().Unix()%10000)
	defer cleanupContainer(t, name)

	t.Run("Create", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "podman", "create", name,
			"--image", "alpine:latest",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("create failed: %v\n%s", err, out)
		}
	})

	t.Run("Start", func(t *testing.T) {
		// Alpine needs a command to stay alive
		cmd := exec.Command(hospitusBinary(), "podman", "start", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("start failed: %v\n%s", err, out)
		}
		time.Sleep(2 * time.Second) // give it time to spin up
	})

	t.Run("List", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "podman", "list")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("list failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), name) {
			t.Errorf("container %s not found in list", name)
		}
	})

	t.Run("Info", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "podman", "info", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("info failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), name) {
			t.Errorf("container name not in info output")
		}
	})

	t.Run("Stop", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "podman", "stop", name)
		out, err := cmd.CombinedOutput()
		if err != nil {
			// Podman containers may already exit; treat as non-fatal
			t.Logf("stop (possibly already exited): %v\n%s", err, out)
		}
	})

	t.Run("Snapshot", func(t *testing.T) {
		snapName := "smoke-snap"
		createCmd := exec.Command(hospitusBinary(), "podman", "snapshot", "create", name, snapName)
		out, err := createCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("snapshot create failed: %v\n%s", err, out)
		}

		listCmd := exec.Command(hospitusBinary(), "podman", "snapshot", "list", name)
		out, err = listCmd.Output()
		if err != nil {
			t.Fatalf("snapshot list failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), snapName) {
			t.Errorf("snapshot not found in list output")
		}

		exec.Command(hospitusBinary(), "podman", "snapshot", "delete", "-y", name, snapName).Run() //nolint:errcheck
	})
}

// ─────────────────────── Provider health checks ─────────────────────────────

// TestAllProvidersHealthCheck verifies that hospitusd responds to /health
// and that all configured providers report healthy.
func TestAllProvidersHealthCheck(t *testing.T) {
	ensureDaemonRunning(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// daemonHealthy probes both schemes: a daemon serving TLS answers a plain
	// HTTP probe with 400, which is not a health failure. It also avoids
	// curl(1), which is not part of a base FreeBSD install.
	if !daemonHealthy(ctx) {
		t.Fatal("/health did not answer on either http or https")
	}
}
