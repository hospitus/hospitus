package jail

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// =============================================================================
// ExportManifest Tests — JSON round-trip and schema stability
// =============================================================================

func TestExportManifestJSONRoundTrip(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

	manifest := ExportManifest{
		Version:      "1.0",
		ExportDate:   now,
		JailName:     "test-jail",
		OSType:       "freebsd",
		OSVersion:    "14.3-RELEASE",
		Arch:         "amd64",
		ZFSDataset:   "zroot/hospitus/jails/test-jail",
		UseZFSSend:   true,
		Compressed:   true,
		HasSnapshots: true,
		Snapshots:    []string{"zroot/hospitus/jails/test-jail@backup1", "zroot/hospitus/jails/test-jail@backup2"},
		Networks: []provider.NetworkSpec{
			{Type: provider.NetworkTypeBridge, Bridge: "hospitus0", IPv4: "10.0.0.10/24"},
		},
		Checksums: map[string]string{
			"zfs.stream.gz": "sha256:abcdef1234567890",
		},
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}

	var decoded ExportManifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal manifest: %v", err)
	}

	// Verify all fields round-trip correctly
	if decoded.Version != manifest.Version {
		t.Errorf("Version: got %q, want %q", decoded.Version, manifest.Version)
	}
	if decoded.JailName != manifest.JailName {
		t.Errorf("JailName: got %q, want %q", decoded.JailName, manifest.JailName)
	}
	if decoded.OSType != manifest.OSType {
		t.Errorf("OSType: got %q, want %q", decoded.OSType, manifest.OSType)
	}
	if decoded.OSVersion != manifest.OSVersion {
		t.Errorf("OSVersion: got %q, want %q", decoded.OSVersion, manifest.OSVersion)
	}
	if decoded.Arch != manifest.Arch {
		t.Errorf("Arch: got %q, want %q", decoded.Arch, manifest.Arch)
	}
	if decoded.ZFSDataset != manifest.ZFSDataset {
		t.Errorf("ZFSDataset: got %q, want %q", decoded.ZFSDataset, manifest.ZFSDataset)
	}
	if decoded.UseZFSSend != manifest.UseZFSSend {
		t.Errorf("UseZFSSend: got %v, want %v", decoded.UseZFSSend, manifest.UseZFSSend)
	}
	if decoded.Compressed != manifest.Compressed {
		t.Errorf("Compressed: got %v, want %v", decoded.Compressed, manifest.Compressed)
	}
	if decoded.HasSnapshots != manifest.HasSnapshots {
		t.Errorf("HasSnapshots: got %v, want %v", decoded.HasSnapshots, manifest.HasSnapshots)
	}
	if len(decoded.Snapshots) != len(manifest.Snapshots) {
		t.Errorf("Snapshots length: got %d, want %d", len(decoded.Snapshots), len(manifest.Snapshots))
	}
	if len(decoded.Networks) != len(manifest.Networks) {
		t.Errorf("Networks length: got %d, want %d", len(decoded.Networks), len(manifest.Networks))
	}
	if decoded.Checksums["zfs.stream.gz"] != manifest.Checksums["zfs.stream.gz"] {
		t.Errorf("Checksums: got %v, want %v", decoded.Checksums, manifest.Checksums)
	}

	// ExportDate round-trips through RFC 3339 (nanosecond precision loss is acceptable)
	if !decoded.ExportDate.Equal(now) {
		t.Errorf("ExportDate: got %v, want %v", decoded.ExportDate, now)
	}
}

func TestExportManifestJSONRoundTripMinimal(t *testing.T) {
	// Minimal manifest for tar-based (non-ZFS) export without snapshots
	manifest := ExportManifest{
		Version:      "1.0",
		ExportDate:   time.Now(),
		JailName:     "minimal-jail",
		OSType:       "freebsd",
		OSVersion:    "14.3-RELEASE",
		Arch:         "arm64",
		ZFSDataset:   "",
		UseZFSSend:   false,
		Compressed:   false,
		HasSnapshots: false,
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}

	var decoded ExportManifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal manifest: %v", err)
	}

	if decoded.UseZFSSend {
		t.Error("UseZFSSend should be false for minimal manifest")
	}
	if decoded.Compressed {
		t.Error("Compressed should be false for minimal manifest")
	}
	if decoded.HasSnapshots {
		t.Error("HasSnapshots should be false for minimal manifest")
	}
	if len(decoded.Snapshots) != 0 {
		t.Error("Snapshots should be nil/empty for minimal manifest")
	}
	if len(decoded.Networks) != 0 {
		t.Error("Networks should be nil/empty for minimal manifest")
	}
	if len(decoded.Checksums) != 0 {
		t.Error("Checksums should be nil/empty for minimal manifest")
	}
}

func TestExportManifestBackwardCompatibility(t *testing.T) {
	// Simulate a manifest exported by an older version (no snapshots field populated)
	oldJSON := `{
		"version": "1.0",
		"export_date": "2025-01-15T12:00:00Z",
		"jail_name": "old-jail",
		"os_type": "freebsd",
		"os_version": "13.2-RELEASE",
		"arch": "amd64",
		"zfs_dataset": "zroot/hospitus/jails/old-jail",
		"use_zfs_send": true,
		"compressed": true,
		"has_snapshots": false
	}`

	var manifest ExportManifest
	if err := json.Unmarshal([]byte(oldJSON), &manifest); err != nil {
		t.Fatalf("failed to unmarshal old manifest: %v", err)
	}

	if manifest.JailName != "old-jail" {
		t.Errorf("JailName: got %q, want %q", manifest.JailName, "old-jail")
	}
	if manifest.OSVersion != "13.2-RELEASE" {
		t.Errorf("OSVersion: got %q, want %q", manifest.OSVersion, "13.2-RELEASE")
	}
	if manifest.Snapshots != nil {
		t.Error("Snapshots should be nil when omitted from JSON")
	}
	if manifest.Networks != nil {
		t.Error("Networks should be nil when omitted from JSON")
	}
}

func TestExportManifestVersionPresent(t *testing.T) {
	manifest := ExportManifest{
		JailName: "test",
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if _, ok := decoded["version"]; !ok {
		t.Error("manifest JSON must include 'version' field even when empty")
	}
	if _, ok := decoded["jail_name"]; !ok {
		t.Error("manifest JSON must include 'jail_name' field even when empty")
	}
	if _, ok := decoded["use_zfs_send"]; !ok {
		// use_zfs_send is always present (bool zero-value is serialized)
		t.Error("manifest JSON should include 'use_zfs_send' field")
	}
}

// =============================================================================
// ImportOptions / ExportOptions — Default behavior
// =============================================================================

func TestExportOptionsDefaults(t *testing.T) {
	opts := provider.ExportOptions{}

	if opts.Compress {
		t.Error("Compress should default to false (uncompressed for local export)")
	}
	if opts.StopInstance {
		t.Error("StopInstance should default to false (don't stop running jail without explicit request)")
	}
	if opts.IncludeSnapshots {
		t.Error("IncludeSnapshots should default to false (snapshots are optional)")
	}
}

func TestImportOptionsDefaults(t *testing.T) {
	opts := provider.ImportOptions{}

	if opts.NewName != "" {
		t.Error("NewName should default to empty (use original name)")
	}
	if opts.ResetMAC {
		t.Error("ResetMAC should default to false (preserve original MAC)")
	}
	if opts.NewIP != "" {
		t.Error("NewIP should default to empty (preserve original IP)")
	}
	if opts.StartAfterImport {
		t.Error("StartAfterImport should default to false (don't auto-start)")
	}
}

// =============================================================================
// ImportOptions.NewName validation — name is validated via ValidateInstanceName
// =============================================================================

func TestImportOptionsNewNameValidation(t *testing.T) {
	// ImportInstance calls validation.ValidateInstanceName(targetJailName)
	// Test that ValidateInstanceName rejects dangerous names that could be
	// passed through ImportOptions.NewName.

	tests := []struct {
		name    string
		newName string
		wantErr bool
	}{
		{"valid name", "imported-jail", false},
		{"valid with dash", "my-jail-1", false},
		{"valid with underscore", "my_jail_backup", false},
		{"empty (optional field)", "", false},
		{"path traversal with dots", "../../etc/passwd", true},
		{"path traversal with separators", "/root/escape", true},
		{"command injection semicolon", "test; rm -rf /", true},
		{"command injection pipe", "test|cat /etc/passwd", true},
		{"command injection backtick", "test`id`", true},
		{"command injection dollar", "$(whoami)", true},
		{"shell metacharacters", "test&", true},
		{"special chars", "test<>jail", true},
		{"too long (64 chars)", "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijkl", true},
		{"max length (63 chars)", "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijk", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.newName == "" {
				// Empty NewName means "use original name" — valid case
				return
			}
			err := validation.ValidateInstanceName(tt.newName)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateInstanceName(%q) error = %v, wantErr = %v", tt.newName, err, tt.wantErr)
			}
		})
	}
}

// Export and import are exercised end to end by test/integration, which needs a
// FreeBSD host with a ZFS pool and root. What is left here is what can be
// checked without one: the pieces that build the commands and validate names.

// =============================================================================
// ZFS Export Pipe Construction Tests
// =============================================================================

// TestZFSExportPipeConstruction pins the pipe ExportInstance builds.
//
// The stream goes through cmd.StdoutPipe() rather than a shell pipeline: a
// dataset or path reaching "zfs send | gzip -c" as a string would be a shell
// injection.
//
// This test exercises the pipe pattern with a trivial command pair to
// confirm the plumbing works correctly. The actual ZFS send/gzip pipeline
// cannot be tested without a ZFS pool.
func TestZFSExportPipeConstruction(t *testing.T) {
	ctx := t.Context()

	producer := exec.CommandContext(ctx, "echo", "hello from producer")
	// Consumer: cat (reads stdin, writes stdout)
	consumer := exec.CommandContext(ctx, "cat")

	// Set up pipe: consumer stdin <- producer stdout
	pipeReader, err := producer.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe failed: %v", err)
	}
	consumer.Stdin = pipeReader

	// Collect consumer output
	var consumerOut strings.Builder
	consumer.Stdout = &consumerOut
	consumer.Stderr = &consumerOut

	if err := producer.Start(); err != nil {
		t.Fatalf("producer start failed: %v", err)
	}

	// Run consumer (blocks until pipe is closed)
	if err := consumer.Run(); err != nil {
		t.Fatalf("consumer failed: %v", err)
	}

	// Wait for producer to finish
	if err := producer.Wait(); err != nil {
		t.Fatalf("producer wait failed: %v", err)
	}

	// Verify output
	if consumerOut.String() != "hello from producer\n" {
		t.Errorf("unexpected output: %q", consumerOut.String())
	}
}

// TestZFSExportPipeErrorHandling tests what happens when the producer
// fails in a StdoutPipe chain. The consumer should see the partial output
// before the pipe breaks.
func TestZFSExportPipeErrorHandling(t *testing.T) {
	ctx := t.Context()

	// Producer: a command that prints then fails (exit 1)
	producer := exec.CommandContext(ctx, "sh", "-c", "echo start; exit 1")
	consumer := exec.CommandContext(ctx, "cat")

	pipeReader, err := producer.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe failed: %v", err)
	}
	consumer.Stdin = pipeReader
	consumer.Stderr = os.Stderr

	if err := producer.Start(); err != nil {
		t.Fatalf("producer start failed: %v", err)
	}

	// Consumer should still read what producer wrote before failing
	consumerOutput := &strings.Builder{}
	consumer.Stdout = consumerOutput
	consumer.Stderr = consumerOutput
	_ = consumer.Run()

	if consumerOutput.String() != "start\n" {
		t.Errorf("expected 'start\\n' from consumer, got: %q", consumerOutput.String())
	}

	err = producer.Wait()
	if err == nil {
		t.Error("producer should have failed with exit code 1")
	}
}
