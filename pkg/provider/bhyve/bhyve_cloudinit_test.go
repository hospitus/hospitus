package bhyve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

func TestGenerateCloudInitISO(t *testing.T) {
	// Create a temporary data directory
	tempDir := t.TempDir()

	// Initialize the Bhyve provider with the temp dir
	p := &BhyveProvider{
		dataDir: tempDir,
	}

	ctx := context.Background()
	vmName := "test-ci-vm"

	// Define cloud-init config
	config := &provider.CloudInitConfig{
		MetaData: "instance-id: test-ci-vm\nlocal-hostname: test-ci-vm\n",
		UserData: "#cloud-config\nusers:\n  - default\n",
	}

	// Try generating the ISO
	isoPath, err := p.generateCloudInitISO(ctx, vmName, config)
	// `mkisofs` might not be available in the test environment, so we expect an error or a success depending on the host.
	// If the host has mkisofs, it should succeed. If not, it returns an error about mkisofs.
	if err != nil {
		// Just skip if mkisofs is not installed instead of failing the test.
		if _, statErr := os.Stat("/usr/sbin/mkisofs"); os.IsNotExist(statErr) {
			t.Skipf("Skipping test: mkisofs not found: %v", err)
		} else {
			t.Fatalf("Failed to generate cloud-init ISO: %v", err)
		}
	}

	if isoPath == "" {
		t.Fatalf("Expected ISO path, got empty string")
	}

	// Verify ISO file exists
	if _, err := os.Stat(isoPath); os.IsNotExist(err) {
		t.Fatalf("Expected ISO file to be created at %s, but it does not exist", isoPath)
	}

	// Verify cloud-init directory was created
	ciDir := filepath.Join(tempDir, vmName, "cloud-init")
	if _, err := os.Stat(ciDir); os.IsNotExist(err) {
		t.Fatalf("Expected cloud-init directory to be created at %s, but it does not exist", ciDir)
	}
}
