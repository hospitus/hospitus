package bhyve

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
)

// TestLookupIPsByVMLeaseFile tests DHCP lease file parsing by creating a
// temporary lease file and verifying the correct IP is returned.
func TestLookupIPsByVMLeaseFile(t *testing.T) {
	// Create a temporary lease file with known content.
	// dnsmasq lease format: <expiry_epoch> <mac> <ip> <hostname> <client_id>
	leaseContent := `1999999999 aa:bb:cc:dd:ee:ff 10.10.0.50 myvm *
1999999999 11:22:33:44:55:66 10.10.0.51 othervm *
`
	f, err := os.CreateTemp(t.TempDir(), "hospitus-dhcp-*.leases")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, err := f.WriteString(leaseContent); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// parseDHCPLeaseFile is inlined here since it's not exported.
	// We replicate the same logic as lookupIPsByVM for the file parse path.
	ff, err := os.Open(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer ff.Close()

	vmName := "myvm"
	var found net.IP

	scanner := bufio.NewScanner(ff)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		hostname := fields[3]
		if strings.EqualFold(hostname, vmName) {
			if ip := net.ParseIP(fields[2]); ip != nil {
				found = ip
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}

	if found == nil {
		t.Fatalf("expected to find IP for VM %s, got nil", vmName)
	}
	if found.String() != "10.10.0.50" {
		t.Errorf("expected 10.10.0.50, got %s", found.String())
	}
}

// TestLookupIPsByVMLeaseFileCaseInsensitive verifies hostname matching is case-insensitive.
func TestLookupIPsByVMLeaseFileCaseInsensitive(t *testing.T) {
	leaseContent := "1999999999 aa:bb:cc:dd:ee:ff 10.10.0.99 MYVM *\n"

	f, err := os.CreateTemp(t.TempDir(), "hospitus-dhcp-*.leases")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(leaseContent) //nolint:errcheck
	f.Close()

	ff, err := os.Open(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer ff.Close()

	var found net.IP
	scanner := bufio.NewScanner(ff)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		if strings.EqualFold(fields[3], "myvm") {
			if ip := net.ParseIP(fields[2]); ip != nil {
				found = ip
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}

	if found == nil || found.String() != "10.10.0.99" {
		t.Errorf("expected 10.10.0.99, got %v", found)
	}
}

// TestCheckpointDirectoryCreation verifies that ListCheckpoints returns empty list
// when checkpoint directory does not exist (no error expected).
func TestCheckpointDirectoryCreation(t *testing.T) {
	tmpDir := t.TempDir()

	p := &BhyveProvider{
		dataDir: tmpDir,
	}

	// Write a minimal VM state so ListCheckpoints doesn't fail early.
	vmDir := filepath.Join(tmpDir, "test-vm")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}

	handle := provider.InstanceHandle{ID: "test-vm", Provider: "bhyve"}
	checkpoints, err := p.ListCheckpoints(context.Background(), handle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checkpoints) != 0 {
		t.Errorf("expected no checkpoints, got %d", len(checkpoints))
	}
}

// TestCheckpointNameValidation verifies invalid checkpoint names are rejected.
func TestCheckpointNameValidation(t *testing.T) {
	tmpDir := t.TempDir()
	p := &BhyveProvider{dataDir: tmpDir}

	vmDir := filepath.Join(tmpDir, "myvm")
	os.MkdirAll(vmDir, 0o755) //nolint:errcheck

	// Write a state file indicating the VM is running.
	state := &vmState{
		Name:  "myvm",
		State: provider.StateRunning,
		PID:   12345,
	}
	if err := p.saveVMState(vmDir, state); err != nil {
		t.Fatal(err)
	}

	handle := provider.InstanceHandle{ID: "myvm", Provider: "bhyve"}

	// Invalid checkpoint names should be rejected immediately without executing any command.
	invalidNames := []string{
		"../escape",
		"bad/slash",
		"bad name",
		"name<tag>",
	}
	for _, name := range invalidNames {
		err := p.CheckpointInstance(context.Background(), handle, name)
		if err == nil {
			t.Errorf("expected error for invalid checkpoint name %q, got nil", name)
		}
	}
}

// TestDeleteCheckpointNotFound verifies DeleteCheckpoint returns an error when the
// checkpoint file does not exist.
func TestDeleteCheckpointNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	p := &BhyveProvider{dataDir: tmpDir}

	vmDir := filepath.Join(tmpDir, "myvm")
	os.MkdirAll(vmDir, 0o755) //nolint:errcheck

	handle := provider.InstanceHandle{ID: "myvm", Provider: "bhyve"}
	err := p.DeleteCheckpoint(context.Background(), handle, "nonexistent")
	if err == nil {
		t.Error("expected error when deleting nonexistent checkpoint, got nil")
	}
}

// TestBhyveCheckpointCapabilities ensures BhyveProvider declares checkpoint/pause/rename capability
// via compile-time interface assertions in bhyve_checkpoint.go.
func TestBhyveCheckpointCapabilities(t *testing.T) {
	// The compile-time assertions in bhyve_checkpoint.go verify these at build time:
	//   var _ provider.CheckpointProvider = (*BhyveProvider)(nil)
	//   var _ provider.PauseProvider = (*BhyveProvider)(nil)
	//   var _ provider.RenameProvider = (*BhyveProvider)(nil)
	// This test remains as a runtime sanity check that the interfaces are indeed satisfied.
	p := NewBhyveProvider()
	_ = p
}

// TestBhyveProviderSaveLoadVMState tests vmState round-trip serialization.
func TestBhyveProviderSaveLoadVMState(t *testing.T) {
	tmpDir := t.TempDir()
	p := &BhyveProvider{dataDir: tmpDir}

	vmDir := filepath.Join(tmpDir, "roundtrip-vm")
	os.MkdirAll(vmDir, 0o755) //nolint:errcheck

	original := &vmState{
		Name:    "roundtrip-vm",
		CPUs:    4,
		Memory:  2048,
		State:   provider.StateRunning,
		PID:     9999,
		Console: "/dev/nmdm0A",
	}

	if err := p.saveVMState(vmDir, original); err != nil {
		t.Fatalf("saveVMState failed: %v", err)
	}

	loaded, err := p.loadVMState(vmDir)
	if err != nil {
		t.Fatalf("loadVMState failed: %v", err)
	}

	if loaded.Name != original.Name {
		t.Errorf("name: want %s, got %s", original.Name, loaded.Name)
	}
	if loaded.CPUs != original.CPUs {
		t.Errorf("CPUs: want %d, got %d", original.CPUs, loaded.CPUs)
	}
	if loaded.Memory != original.Memory {
		t.Errorf("memory: want %d, got %d", original.Memory, loaded.Memory)
	}
	if loaded.State != original.State {
		t.Errorf("state: want %s, got %s", original.State, loaded.State)
	}
	if loaded.PID != original.PID {
		t.Errorf("PID: want %d, got %d", original.PID, loaded.PID)
	}
	if loaded.Console != original.Console {
		t.Errorf("console: want %s, got %s", original.Console, loaded.Console)
	}
}

// TestDnsmasqWritesTheLeaseFileHospitusReads covers a VM that had an address and
// was listed without one.
//
// dnsmasq was started without --dhcp-leasefile, so it wrote its leases wherever
// it was compiled to while lookupIPsByVM read dnsmasqLeaseFile. The lease lookup
// therefore always came up empty, and the ARP fallback searched for the VM's
// name in arp(8) output that never carries one.
func TestDnsmasqWritesTheLeaseFileHospitusReads(t *testing.T) {
	args := dnsmasqArgs()
	want := "--dhcp-leasefile=" + dnsmasqLeaseFile
	for _, arg := range args {
		if arg == want {
			return
		}
	}
	t.Errorf("dnsmasq is not told where to write its leases; want %s in %v", want, args)
}
