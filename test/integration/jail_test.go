package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFreeBSDJailNative tests creating and managing native FreeBSD jails
func TestFreeBSDJailNative(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ensureDaemonRunning(t)
	ensureImageExists(t, "freebsd-14.3-RELEASE-amd64")

	jailName := "test-freebsd-native"
	defer cleanupJail(t, jailName)

	t.Run("Create", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
			"--image", "freebsd-14.3-RELEASE-amd64",
			"--vnet",
			"--description", "Test FreeBSD native jail")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to create jail: %v\nOutput: %s", err, output)
		}
	})

	t.Run("Start", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "start", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to start jail: %v\nOutput: %s", err, output)
		}
	})

	t.Run("List", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "list")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to list jails: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), jailName) {
			t.Error("Jail not found in list")
		}
	})

	t.Run("Info", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "info", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to get jail info: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "running") {
			t.Error("Jail should be running")
		}
	})

	t.Run("Exec", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName, "uname", "-a")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to exec in jail: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "FreeBSD") {
			t.Error("Expected FreeBSD in uname output")
		}
	})

	t.Run("ExecWithEnv", func(t *testing.T) {
		// Test exec with environment variables
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"sh", "-c", "echo $HOME")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to exec with env: %v\nOutput: %s", err, output)
		}
		// Should output something (root's home)
		if len(strings.TrimSpace(string(output))) == 0 {
			t.Error("Expected non-empty output for HOME env")
		}
	})

	t.Run("Stats", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "stats", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to get jail stats: %v\nOutput: %s", err, output)
		}
		// Check for expected fields
		if !strings.Contains(string(output), "CPU Usage") {
			t.Error("Expected CPU Usage in stats output")
		}
		if !strings.Contains(string(output), "Memory") {
			t.Error("Expected Memory in stats output")
		}
	})

	t.Run("StatsJSON", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "stats", jailName, "--output", "json")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to get jail stats JSON: %v\nOutput: %s", err, output)
		}
		// Verify it's valid JSON with expected fields
		if !strings.Contains(string(output), "cpu_usage_percent") {
			t.Error("Expected cpu_usage_percent in JSON output")
		}
	})

	t.Run("Health", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "health", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to get jail health: %v\nOutput: %s", err, output)
		}
		// Check for expected status
		if !strings.Contains(string(output), "healthy") && !strings.Contains(string(output), "[OK]") {
			t.Errorf("Expected healthy status in health output: %s", output)
		}
	})

	t.Run("HealthJSON", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "health", jailName, "--output", "json")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to get jail health JSON: %v\nOutput: %s", err, output)
		}
		// Verify it's valid JSON with expected fields
		if !strings.Contains(string(output), "\"status\"") {
			t.Error("Expected status in JSON output")
		}
		if !strings.Contains(string(output), "\"checks\"") {
			t.Error("Expected checks in JSON output")
		}
	})

	t.Run("NetworkConnectivity", func(t *testing.T) {
		// Test that the jail can reach the internet
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName,
			"ping", "-c", "1", "-t", "5", "8.8.8.8")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("Warning: Network connectivity test failed: %v\nOutput: %s", err, output)
			// Don't fail - network may not be available in all test environments
		}
	})

	t.Run("Stop", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "stop", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to stop jail: %v\nOutput: %s", err, output)
		}
	})

	t.Run("Restart", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "start", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to restart jail: %v\nOutput: %s", err, output)
		}

		cmd = exec.Command(hospitusBinary(), "jail", "restart", jailName)
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to restart jail: %v\nOutput: %s", err, output)
		}
	})

	t.Run("Destroy", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "stop", jailName)
		cmd.Run() // Ignore error if already stopped

		cmd = exec.Command(hospitusBinary(), "jail", "destroy", jailName, "-y")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to destroy jail: %v\nOutput: %s", err, output)
		}
	})
}

// TestFreeBSDJailWithResources tests resource limits on jails
func TestFreeBSDJailWithResources(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ensureDaemonRunning(t)
	ensureImageExists(t, "freebsd-14.3-RELEASE-amd64")

	jailName := "test-freebsd-rctl"
	defer cleanupJail(t, jailName)

	t.Run("CreateWithResources", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
			"--image", "freebsd-14.3-RELEASE-amd64",
			"--vnet",
			"--cpus", "2",
			"--memory", "512")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to create jail with resources: %v\nOutput: %s", err, output)
		}
	})

	t.Run("StartAndCheckResources", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "start", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to start jail: %v\nOutput: %s", err, output)
		}

		// Check that RCTL rules are applied (test runs as root)
		cmd = exec.Command("rctl", "-h", fmt.Sprintf("jail:%s", jailName))
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Logf("RCTL may not be enabled: %v", err)
		} else if len(output) == 0 {
			t.Log("No RCTL rules found (RCTL may need to be enabled)")
		}
	})
}

// TestFreeBSDJailCrossArch tests creating jails for different architectures
func TestFreeBSDJailCrossArch(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	longTests := os.Getenv("HOSPITUS_LONG_TESTS") == "1"
	if !longTests {
		t.Skip("Cross-architecture tests require HOSPITUS_LONG_TESTS=1")
	}

	ensureDaemonRunning(t)

	testCases := []struct {
		name  string
		image string
		arch  string
	}{
		{
			name:  "arm64",
			image: "freebsd-14.3-RELEASE-arm64",
			arch:  "arm64",
		},
		{
			name:  "riscv64",
			image: "freebsd-14.3-RELEASE-riscv64",
			arch:  "riscv64",
		},
		{
			name:  "i386",
			image: "freebsd-14.3-RELEASE-i386",
			arch:  "i386",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Check if QEMU user-mode emulation is available for this arch
			if !isQemuUserAvailable(tc.arch) {
				t.Skipf("QEMU user-mode emulation not available for %s", tc.arch)
			}

			jailName := fmt.Sprintf("test-freebsd-%s", tc.arch)
			defer cleanupJail(t, jailName)

			ensureImageExists(t, tc.image)

			cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
				"--image", tc.image,
				"--vnet")
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Failed to create %s jail: %v\nOutput: %s", tc.arch, err, output)
			}

			cmd = exec.Command(hospitusBinary(), "jail", "start", jailName)
			output, err = cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Failed to start %s jail: %v\nOutput: %s", tc.arch, err, output)
			}

			// Test execution
			cmd = exec.Command(hospitusBinary(), "jail", "exec", jailName, "uname", "-m")
			_, err = cmd.CombinedOutput()
			if err != nil {
				t.Logf("Exec in %s jail failed (expected with QEMU): %v", tc.arch, err)
			}
		})
	}
}

// TestLinuxJailAlpine tests creating Alpine Linux jails
func TestLinuxJailAlpine(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	if !isLinuxCompatEnabled() {
		t.Skip("Linux compatibility layer not enabled")
	}

	ensureDaemonRunning(t)
	ensureImageExists(t, "alpine-3.20-rootfs-amd64")

	jailName := "test-alpine-linux"
	defer cleanupJail(t, jailName)

	t.Run("Create", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
			"--image", "alpine-3.20-rootfs-amd64",
			"--os-type", "linux",
			"--os-version", "alpine-3.20",
			"--vnet",
			"--description", "Alpine Linux test jail")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to create Alpine jail: %v\nOutput: %s", err, output)
		}
	})

	t.Run("Start", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "start", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to start Alpine jail: %v\nOutput: %s", err, output)
		}
	})

	t.Run("ExecLinuxCommand", func(t *testing.T) {
		// Test executing a Linux command
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName, "cat", "/etc/alpine-release")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to exec in Alpine jail: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "3.20") {
			t.Errorf("Expected Alpine 3.20, got: %s", output)
		}
	})

	t.Run("PackageManager", func(t *testing.T) {
		// Test apk package manager
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName, "apk", "--version")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("apk not available: %v", err)
		} else if !strings.Contains(string(output), "apk-tools") {
			t.Log("apk-tools not found in output")
		}
	})
}

// TestLinuxJailUbuntu tests creating Ubuntu Linux jails
func TestLinuxJailUbuntu(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	if !isLinuxCompatEnabled() {
		t.Skip("Linux compatibility layer not enabled")
	}

	longTests := os.Getenv("HOSPITUS_LONG_TESTS") == "1"
	if !longTests {
		t.Skip("Ubuntu test requires HOSPITUS_LONG_TESTS=1 (large download)")
	}

	ensureDaemonRunning(t)
	ensureImageExists(t, "ubuntu-24.04-rootfs-amd64")

	jailName := "test-ubuntu-linux"
	defer cleanupJail(t, jailName)

	t.Run("Create", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
			"--image", "ubuntu-24.04-rootfs-amd64",
			"--os-type", "linux",
			"--os-version", "ubuntu-24.04",
			"--vnet",
			"--description", "Ubuntu Linux test jail")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to create Ubuntu jail: %v\nOutput: %s", err, output)
		}
	})

	t.Run("Start", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "start", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to start Ubuntu jail: %v\nOutput: %s", err, output)
		}
	})

	t.Run("ExecLinuxCommand", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName, "cat", "/etc/lsb-release")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to exec in Ubuntu jail: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "Ubuntu") {
			t.Errorf("Expected Ubuntu, got: %s", output)
		}
	})

	t.Run("AptPackageManager", func(t *testing.T) {
		// Test apt package manager
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName, "apt", "--version")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("apt not available: %v", err)
		} else if !strings.Contains(string(output), "apt") {
			t.Log("apt not found in output")
		}
	})
}

// TestLinuxJailFedora tests creating Fedora Linux jails
func TestLinuxJailFedora(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	if !isLinuxCompatEnabled() {
		t.Skip("Linux compatibility layer not enabled")
	}

	longTests := os.Getenv("HOSPITUS_LONG_TESTS") == "1"
	if !longTests {
		t.Skip("Fedora test requires HOSPITUS_LONG_TESTS=1 (large download)")
	}

	ensureDaemonRunning(t)
	ensureImageExists(t, "fedora-41-rootfs-amd64")

	jailName := "test-fedora-linux"
	defer cleanupJail(t, jailName)

	t.Run("Create", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
			"--image", "fedora-41-rootfs-amd64",
			"--vnet",
			"--description", "Fedora Linux test jail")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to create Fedora jail: %v\nOutput: %s", err, output)
		}
	})

	t.Run("Start", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "start", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to start Fedora jail: %v\nOutput: %s", err, output)
		}
	})

	t.Run("ExecLinuxCommand", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName, "cat", "/etc/fedora-release")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to exec in Fedora jail: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), "Fedora") {
			t.Errorf("Expected Fedora, got: %s", output)
		}
	})

	t.Run("DnfPackageManager", func(t *testing.T) {
		// Test dnf package manager
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName, "dnf", "--version")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("dnf not available: %v", err)
		}
		t.Logf("dnf output: %s", output)
	})
}

// TestJailSnapshot tests jail snapshot functionality
func TestJailSnapshot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ensureDaemonRunning(t)
	ensureImageExists(t, "freebsd-14.3-RELEASE-amd64")

	jailName := "test-snapshot"
	snapshotName := "test-snap1"
	defer cleanupJail(t, jailName)

	// Create and start jail
	cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
		"--image", "freebsd-14.3-RELEASE-amd64")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create jail: %v\nOutput: %s", err, output)
	}

	t.Run("CreateSnapshot", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "snapshot", "create", jailName, snapshotName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to create snapshot: %v\nOutput: %s", err, output)
		}
	})

	t.Run("ListSnapshots", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "snapshot", "list", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to list snapshots: %v\nOutput: %s", err, output)
		}
		if !strings.Contains(string(output), snapshotName) {
			t.Error("Snapshot not found in list")
		}
	})

	t.Run("RestoreSnapshot", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "snapshot", "restore", jailName, snapshotName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to restore snapshot: %v\nOutput: %s", err, output)
		}
	})

	t.Run("DeleteSnapshot", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "snapshot", "delete", jailName, snapshotName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to delete snapshot: %v\nOutput: %s", err, output)
		}
	})
}

// TestJailClone tests jail cloning functionality
func TestJailClone(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ensureDaemonRunning(t)
	ensureImageExists(t, "freebsd-14.3-RELEASE-amd64")

	sourceJail := "test-clone-source"
	cloneJail := "test-clone-target"

	// Pre-test cleanup in case previous runs left stale datasets
	cleanupJail(t, sourceJail)
	cleanupJail(t, cloneJail)

	defer cleanupJail(t, sourceJail)
	defer cleanupJail(t, cloneJail)

	cmd := exec.Command(hospitusBinary(), "jail", "create", sourceJail,
		"--image", "freebsd-14.3-RELEASE-amd64")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to create source jail: %v\nOutput: %s", err, output)
	}

	t.Run("Clone", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "clone", sourceJail, cloneJail, "--linked")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to clone jail: %v\nOutput: %s", err, output)
		}
	})

	t.Run("VerifyClone", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "list")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to list jails: %v", err)
		}
		if !strings.Contains(string(output), cloneJail) {
			t.Error("Cloned jail not found")
		}
	})
}

// Helper functions

func ensureImageExists(t *testing.T, image string) {
	t.Helper()

	// Check if image is already downloaded (without .partial extension)
	if isImageComplete(image) {
		t.Logf("Image %s already exists", image)
		return
	}

	// Check for partial download
	if isImagePartial(image) {
		t.Skipf("Image %s has partial download, run 'hospitus image fetch %s' manually to complete", image, image)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, hospitusBinary(), "image", "fetch", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to ensure image %s exists: %v\nOutput: %s", image, err, output)
	}

	// Verify the download completed (no .partial file left)
	if isImagePartial(image) {
		t.Fatalf("Image %s download incomplete (partial file exists)", image)
	}
}

func isImageComplete(image string) bool {
	imageDir := "/var/hospitus/images/sets"
	extensions := []string{".txz", ".tar.gz", ".tar.xz", ".tgz"}
	for _, ext := range extensions {
		path := filepath.Join(imageDir, image+ext)
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func isImagePartial(image string) bool {
	imageDir := "/var/hospitus/images/sets"
	extensions := []string{".txz.partial", ".tar.gz.partial", ".tar.xz.partial", ".tgz.partial"}
	for _, ext := range extensions {
		path := filepath.Join(imageDir, image+ext)
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func cleanupJail(t *testing.T, name string) {
	t.Helper()

	// Stop the jail if running
	cmd := exec.Command(hospitusBinary(), "jail", "stop", name)
	cmd.Run() // Ignore errors

	cmd = exec.Command(hospitusBinary(), "jail", "destroy", name, "-y")
	cmd.Run() // Ignore errors
}

func isLinuxCompatEnabled() bool {
	// Check if linux64elf kernel module is loaded (the actual module name inside linux64.ko)
	cmd := exec.Command("kldstat", "-q", "-m", "linux64elf")
	return cmd.Run() == nil
}

func isQemuUserAvailable(arch string) bool {
	qemuBinaries := map[string]string{
		"arm64":   "qemu-aarch64",
		"riscv64": "qemu-riscv64",
		"i386":    "qemu-i386",
	}

	binary, ok := qemuBinaries[arch]
	if !ok {
		return false
	}

	_, err := exec.LookPath(binary)
	return err == nil
}
