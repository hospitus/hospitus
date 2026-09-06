package integration

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestImageCatalog tests the image catalog functionality
func TestImageCatalog(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ensureDaemonRunning(t)

	t.Run("ListAvailable", func(t *testing.T) {
		cmd := exec.Command(hospitusBinary(), "image", "available")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to list available images: %v\nOutput: %s", err, output)
		}

		// Check for expected images in the catalog
		outputStr := string(output)
		expectedImages := []string{
			"freebsd-14.3-RELEASE-amd64",
			"alpine-3.20-rootfs-amd64",
			"ubuntu-24.04-rootfs-amd64",
		}

		for _, img := range expectedImages {
			if !strings.Contains(outputStr, img) {
				t.Errorf("Expected image %s not found in catalog", img)
			}
		}
	})

	t.Run("ListByCategory", func(t *testing.T) {
		categories := []string{"set", "iso", "cloud"}
		for _, cat := range categories {
			cmd := exec.Command(hospitusBinary(), "image", "available", "--category", cat)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("Failed to list images for category %s: %v\nOutput: %s", cat, err, output)
			}
		}
	})

	t.Run("ListByProvider", func(t *testing.T) {
		providers := []string{"jail", "bhyve", "qemu"}
		for _, prov := range providers {
			cmd := exec.Command(hospitusBinary(), "image", "available", "--provider", prov)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("Failed to list images for provider %s: %v\nOutput: %s", prov, err, output)
			}
		}
	})

	t.Run("ListByOS", func(t *testing.T) {
		osTypes := []string{"freebsd", "linux"}
		for _, osType := range osTypes {
			cmd := exec.Command(hospitusBinary(), "image", "available", "--os", osType)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("Failed to list images for OS %s: %v\nOutput: %s", osType, err, output)
			}
		}
	})
}

// TestImageDownload tests downloading images from the catalog
func TestImageDownload(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ensureDaemonRunning(t)

	// Test images to download (small ones first)
	testCases := []struct {
		name     string
		image    string
		category string
		skip     string // Skip reason if not empty
	}{
		{
			name:     "AlpineLinux",
			image:    "alpine-3.20-rootfs-amd64",
			category: "set",
		},
		{
			name:     "FreeBSD14.3",
			image:    "freebsd-14.3-RELEASE-amd64",
			category: "set",
			skip:     "Large download, run with -long flag",
		},
		{
			name:     "UbuntuRootfs",
			image:    "ubuntu-24.04-rootfs-amd64",
			category: "set",
			skip:     "Large download, run with -long flag",
		},
		{
			name:     "FedoraRootfs",
			image:    "fedora-41-rootfs-amd64",
			category: "set",
			skip:     "Large download, run with -long flag",
		},
	}

	longTests := os.Getenv("HOSPITUS_LONG_TESTS") == "1"

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skip != "" && !longTests {
				t.Skip(tc.skip)
			}

			// Download the image
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			cmd := exec.CommandContext(ctx, hospitusBinary(), "image", "fetch", tc.image)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Failed to fetch image %s: %v\nOutput: %s", tc.image, err, output)
			}

			// Verify the image is in the list
			cmd = exec.Command(hospitusBinary(), "image", "list", "--category", tc.category)
			output, err = cmd.CombinedOutput()
			if err != nil {
				t.Errorf("Failed to list images: %v", err)
			}

			if !strings.Contains(string(output), tc.image) {
				t.Errorf("Downloaded image %s not found in list", tc.image)
			}
		})
	}
}

// TestImageAlreadyExists tests that fetching an existing image doesn't fail
func TestImageAlreadyExists(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ensureDaemonRunning(t)

	// First fetch Alpine (small image)
	image := "alpine-3.20-rootfs-amd64"

	cmd := exec.Command(hospitusBinary(), "image", "fetch", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to fetch image: %v\nOutput: %s", err, output)
	}

	// Fetch again - should succeed with "already exists" message
	cmd = exec.Command(hospitusBinary(), "image", "fetch", image)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Fetching existing image failed: %v\nOutput: %s", err, output)
	}

	if !strings.Contains(string(output), "already exists") {
		t.Error("Expected 'already exists' message when fetching existing image")
	}
}

// TestImageDelete tests deleting downloaded images
func TestImageDelete(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	ensureDaemonRunning(t)

	// Use alpine 3.19 (different from 3.20 used in other tests)
	image := "alpine-3.19-rootfs-amd64"

	// Skip if image doesn't exist in catalog
	cmd := exec.Command(hospitusBinary(), "image", "available")
	output, _ := cmd.CombinedOutput()
	if !strings.Contains(string(output), image) {
		t.Skipf("Image %s not in catalog", image)
	}

	cmd = exec.Command(hospitusBinary(), "image", "fetch", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to fetch image: %v\nOutput: %s", err, output)
	}

	// Get filename from list
	cmd = exec.Command(hospitusBinary(), "image", "list")
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to list images: %v", err)
	}

	// Find the filename (includes extension)
	lines := strings.Split(string(output), "\n")
	var filename string
	for _, line := range lines {
		if strings.Contains(line, image) {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				filename = fields[0]
				break
			}
		}
	}

	if filename == "" {
		t.Fatal("Could not find downloaded image filename")
	}

	cmd = exec.Command(hospitusBinary(), "image", "delete", filename)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to delete image: %v\nOutput: %s", err, output)
	}

	// Verify it's gone
	cmd = exec.Command(hospitusBinary(), "image", "list")
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to list images: %v", err)
	}

	if strings.Contains(string(output), filename) {
		t.Error("Image still exists after deletion")
	}
}

// Helper functions

// findBinary locates a binary built by `make build`. It looks in the repository
// root (tests may run from any package directory) and then on PATH. The boolean
// reports whether it was found at all, so callers can skip rather than fail on a
// host where the binaries were never built — an unprepared machine is not a
// defect in the code under test.
func findBinary(name string) (string, bool) {
	for _, dir := range []string{".", "..", "../.."} {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		return abs, true
	}
	if fromPath, err := exec.LookPath(name); err == nil {
		return fromPath, true
	}
	return name, false
}

func hospitusBinary() string {
	path, _ := findBinary("hospitus")
	return path
}

// daemonHealthy reports whether a hospitusd instance answers on the loopback health
// endpoint. It uses net/http rather than shelling out to curl(1), which is not
// part of a base FreeBSD install.
func daemonHealthy(ctx context.Context) bool {
	// Both schemes: a daemon started from an rc script serves TLS, and probing
	// it over plain HTTP fails in a way that looks like no daemon at all. The
	// test would then start a second one on the same address, where it cannot
	// bind, and wait for it until the deadline.
	for _, url := range []string{"http://127.0.0.1:8080/health", "https://127.0.0.1:8080/health"} {
		if healthyAt(ctx, url) {
			return true
		}
	}
	return false
}

// healthyAt reports whether /health answers 200 at one URL.
//
// The certificate is not verified: a loopback daemon serves a self-signed one,
// which is the documented development configuration.
func healthyAt(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return false
	}
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // G402: loopback probe of a self-signed development certificate
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

func ensureDaemonRunning(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if daemonHealthy(ctx) {
		return
	}

	// Both binaries must exist before the daemon can be started and driven.
	daemonPath, daemonFound := findBinary("hospitusd")
	_, cliFound := findBinary("hospitus")
	if !daemonFound || !cliFound {
		t.Skip("hospitus/hospitusd binaries not found — run `make build` first")
	}

	// hospitusd refuses to start without root on FreeBSD, so an unprivileged run
	// could only ever time out waiting for a daemon that exited immediately.
	if os.Geteuid() != 0 {
		t.Skip("starting hospitusd requires root — run the integration tests with doas")
	}

	t.Log("Starting hospitusd daemon...")

	// The daemon refuses to start without TLS, and rejects every request when
	// auth is enabled with no key configured. Both flags are the documented
	// loopback development configuration, which is what these tests drive.
	daemon := exec.Command(daemonPath,
		"--data-dir", "/var/lib/hospitus",
		"--state-dir", "/var/lib/hospitus/state",
		"--db", "/var/lib/hospitus/hospitus.db",
		"--allow-insecure-tls",
		"--allow-no-auth")
	var daemonOutput strings.Builder
	daemon.Stdout = &daemonOutput
	daemon.Stderr = &daemonOutput
	if err := daemon.Start(); err != nil {
		t.Fatalf("Failed to start daemon: %v", err)
	}

	// Poll for readiness instead of a fixed sleep.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		pollCtx, pollCancel := context.WithTimeout(context.Background(), 2*time.Second)
		healthy := daemonHealthy(pollCtx)
		pollCancel()
		if healthy {
			return
		}
	}
	// Say why. The usual cause is another daemon already on the address, whose
	// bind failure is the only thing that explains the wait.
	t.Fatalf("Daemon did not become ready within 15 seconds. Its output was:\n%s",
		strings.TrimSpace(daemonOutput.String()))
}
