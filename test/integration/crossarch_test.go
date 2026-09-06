// Cross-architecture jail tests
//
// These tests require:
// - QEMU user-mode static binaries: pkg install qemu-user-static
// - binmiscctl configuration: doas tools/setup-binmiscctl.sh
// - Cross-architecture base images: hospitus image fetch freebsd-14.3-RELEASE-arm64
//
// Run with: HOSPITUS_CROSSARCH_TESTS=1 doas go test -v ./test/integration/... -run TestCrossArch

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// checkBinmiscctl verifies that binmiscctl is configured for an architecture
func checkBinmiscctl(arch string) bool {
	cmd := exec.Command("binmiscctl", "lookup", arch)
	output, _ := cmd.CombinedOutput()
	// Check if the output contains the arch name (successful lookup)
	return strings.Contains(string(output), arch)
}

// checkQemuStatic verifies that QEMU static binary exists
func checkQemuStatic(arch string) bool {
	var path string
	switch arch {
	case "aarch64":
		path = "/usr/local/bin/qemu-aarch64-static"
	case "riscv64":
		path = "/usr/local/bin/qemu-riscv64-static"
	case "armv7":
		path = "/usr/local/bin/qemu-arm-static"
	default:
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// checkCrossArchImage verifies that a cross-arch image is available
// imagesDir is where a fetched image lands: the daemon's data directory, which
// the integration suite runs with its default of /var/lib/hospitus. Base sets sit
// at the root of it, rootfs tarballs under sets/, cloud disks under cloud/.
const imagesDir = "/var/lib/hospitus/images"

// checkCloudImage reports whether a cloud disk for imageName has been fetched.
//
// The QEMU provider accepts several spellings — with or without the
// architecture, .img or .qcow2 — so rather than repeat that list here, this
// matches any file under images/cloud whose name starts with the image.
func checkCloudImage(imageName string) bool {
	matches, err := filepath.Glob(filepath.Join(imagesDir, "cloud", imageName+"*"))
	return err == nil && len(matches) > 0
}

func checkCrossArchImage(imageName string) bool {
	// Checking /var/hospitus meant this never found anything, so both
	// cross-architecture tests skipped on every host without saying why.
	paths := []string{
		imagesDir + "/" + imageName + ".txz",
		imagesDir + "/" + imageName + ".tar.xz",
		imagesDir + "/sets/" + imageName + ".txz",
		imagesDir + "/sets/" + imageName + ".tar.xz",
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// randomTestSuffix generates a unique suffix for test jails
func randomTestSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano()%100000)
}

func TestCrossArchARM64(t *testing.T) {
	// Skip unless explicitly enabled (these tests are slow and require special setup)
	if os.Getenv("HOSPITUS_CROSSARCH_TESTS") != "1" {
		t.Skip("Cross-architecture tests disabled. Set HOSPITUS_CROSSARCH_TESTS=1 to enable.")
	}

	// Check prerequisites
	if !checkQemuStatic("aarch64") {
		t.Skip("QEMU aarch64 static binary not found. Install with: pkg install qemu-user-static")
	}

	if !checkBinmiscctl("aarch64") {
		t.Skip("binmiscctl not configured for aarch64. Run: doas tools/setup-binmiscctl.sh")
	}

	// Check for ARM64 image
	imageName := "14.3-RELEASE-arm64"
	if !checkCrossArchImage(imageName) {
		t.Skipf("ARM64 image not found. Fetch with: hospitus image fetch freebsd-%s", imageName)
	}

	jailName := "test-arm64-" + randomTestSuffix()
	defer cleanupJail(t, jailName)

	// Create ARM64 jail
	t.Run("Create", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
			"--image", imageName,
			"--cpus", "1",
			"--memory", "512",
			"--vnet")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to create ARM64 jail: %v\nOutput: %s", err, output)
		}
		t.Logf("Create output: %s", output)
	})

	// Start jail
	t.Run("Start", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "start", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to start ARM64 jail: %v\nOutput: %s", err, output)
		}
	})

	// Verify architecture via uname -m
	t.Run("VerifyArchitecture", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName, "uname", "-m")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to exec uname -m: %v\nOutput: %s", err, output)
		}

		arch := strings.TrimSpace(string(output))
		if arch != "aarch64" {
			t.Errorf("Expected architecture 'aarch64', got '%s'", arch)
		}
		t.Logf("Architecture: %s", arch)
	})

	// Stop jail
	t.Run("Stop", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "stop", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to stop ARM64 jail: %v\nOutput: %s", err, output)
		}
	})

	// Destroy jail
	t.Run("Destroy", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "destroy", jailName, "-y")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to destroy ARM64 jail: %v\nOutput: %s", err, output)
		}
	})
}

func TestCrossArchRISCV64(t *testing.T) {
	// Skip unless explicitly enabled
	if os.Getenv("HOSPITUS_CROSSARCH_TESTS") != "1" {
		t.Skip("Cross-architecture tests disabled. Set HOSPITUS_CROSSARCH_TESTS=1 to enable.")
	}

	// Check prerequisites
	if !checkQemuStatic("riscv64") {
		t.Skip("QEMU riscv64 static binary not found. Install with: pkg install qemu-user-static")
	}

	if !checkBinmiscctl("riscv64") {
		t.Skip("binmiscctl not configured for riscv64. Run: doas tools/setup-binmiscctl.sh")
	}

	// Check for RISC-V image
	imageName := "14.3-RELEASE-riscv64"
	if !checkCrossArchImage(imageName) {
		t.Skipf("RISC-V image not found. Fetch with: hospitus image fetch freebsd-%s", imageName)
	}

	jailName := "test-riscv64-" + randomTestSuffix()
	defer cleanupJail(t, jailName)

	// Create RISC-V jail
	t.Run("Create", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "create", jailName,
			"--image", imageName,
			"--cpus", "1",
			"--memory", "512",
			"--vnet")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to create RISC-V jail: %v\nOutput: %s", err, output)
		}
		t.Logf("Create output: %s", output)
	})

	// Start jail
	t.Run("Start", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "start", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to start RISC-V jail: %v\nOutput: %s", err, output)
		}
	})

	// Verify architecture via uname -m
	t.Run("VerifyArchitecture", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "exec", jailName, "uname", "-m")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to exec uname -m: %v\nOutput: %s", err, output)
		}

		arch := strings.TrimSpace(string(output))
		if arch != "riscv64" {
			t.Errorf("Expected architecture 'riscv64', got '%s'", arch)
		}
		t.Logf("Architecture: %s", arch)
	})

	// Stop jail
	t.Run("Stop", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "stop", jailName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to stop RISC-V jail: %v\nOutput: %s", err, output)
		}
	})

	// Destroy jail
	t.Run("Destroy", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "jail", "destroy", jailName, "-y")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to destroy RISC-V jail: %v\nOutput: %s", err, output)
		}
	})
}

// TestBinmiscctlSetup verifies that binmiscctl can be configured
func TestBinmiscctlSetup(t *testing.T) {
	// This test doesn't require HOSPITUS_CROSSARCH_TESTS since it just checks the setup script

	// Check if the setup script exists
	scriptPath := "../../tools/setup-binmiscctl.sh"
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		t.Fatalf("Setup script not found at %s", scriptPath)
	}

	// Verify the script is executable
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("Failed to stat setup script: %v", err)
	}

	if info.Mode().Perm()&0o111 == 0 {
		t.Log("Setup script is not executable. Run: chmod +x tools/setup-binmiscctl.sh")
	}

	// Check if binmiscctl command exists
	if _, err := exec.LookPath("binmiscctl"); err != nil {
		t.Skip("binmiscctl not found - not running on FreeBSD?")
	}

	// List current binmiscctl configuration
	cmd := exec.Command("binmiscctl", "list")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("binmiscctl list failed (may need root or kernel module): %v", err)
	} else {
		t.Logf("Current binmiscctl configuration:\n%s", output)
	}

	// Check for QEMU static binaries
	qemuBinaries := map[string]string{
		"aarch64": "/usr/local/bin/qemu-aarch64-static",
		"riscv64": "/usr/local/bin/qemu-riscv64-static",
		"armv7":   "/usr/local/bin/qemu-arm-static",
	}

	for arch, path := range qemuBinaries {
		if _, err := os.Stat(path); err == nil {
			t.Logf("QEMU %s: found at %s", arch, path)
		} else {
			t.Logf("QEMU %s: not found (install with: pkg install qemu-user-static)", arch)
		}
	}
}
