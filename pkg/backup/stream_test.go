package backup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestStreamToFileWritesTheStream covers a local backup that reported success
// and restored nothing.
//
// The send ran through CombinedOutput: the whole dataset was read into memory
// and dropped, no file was written, and restore later fed zfs receive an empty
// stdin.
func TestStreamToFileWritesTheStream(t *testing.T) {
	bm := NewBackupManager(t.TempDir(), datasetUnder("zroot"), nil)
	dir := filepath.Join(t.TempDir(), "backups")

	// echo stands in for zfs send: what matters is that its output lands in the
	// file rather than in a buffer nobody reads.
	send := exec.Command("printf", "%s", "stream-contents")

	path, err := bm.streamToFile(t.Context(), send, dir, "web-20260101-000000", "")
	if err != nil {
		t.Fatalf("streamToFile: %v", err)
	}

	want := filepath.Join(dir, "web-20260101-000000.zfs")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the stream back: %v", err)
	}
	if string(content) != "stream-contents" {
		t.Errorf("file holds %q, want the stream", content)
	}
}

// TestStreamToFileLeavesNothingBehindOnFailure keeps a partial stream from
// staying: it restores nothing, and the next incremental would chain onto it.
func TestStreamToFileLeavesNothingBehindOnFailure(t *testing.T) {
	bm := NewBackupManager(t.TempDir(), datasetUnder("zroot"), nil)
	dir := filepath.Join(t.TempDir(), "backups")

	send := exec.Command("false")

	if _, err := bm.streamToFile(t.Context(), send, dir, "web-failed", ""); err == nil {
		t.Fatal("a failed send was reported as a backup")
	}
	if _, err := os.Stat(filepath.Join(dir, "web-failed.zfs")); !os.IsNotExist(err) {
		t.Error("a partial stream was left behind")
	}
}

// TestStreamToFileRefusesARelativeDestination keeps the daemon from writing
// backups next to its working directory.
func TestStreamToFileRefusesARelativeDestination(t *testing.T) {
	bm := NewBackupManager(t.TempDir(), datasetUnder("zroot"), nil)

	for _, dir := range []string{"backups", "./backups", "../backups"} {
		if _, err := bm.streamToFile(t.Context(), exec.Command("true"), dir, "web", ""); err == nil {
			t.Errorf("destination %q was accepted", dir)
		}
	}
}

// TestFullBackupNeedsADestination covers the empty destination that used to
// produce a completed backup holding nothing.
func TestFullBackupNeedsADestination(t *testing.T) {
	bm := NewBackupManager(t.TempDir(), datasetUnder("zroot"), nil)

	_, _, err := bm.runPipeline(t.Context(), exec.Command("true"), &BackupConfig{}, "web-1")
	if err == nil {
		t.Fatal("a backup with no destination was accepted")
	}
	if !strings.Contains(err.Error(), "destination") {
		t.Errorf("error = %v, want it to name the missing destination", err)
	}
}

// TestCompressionRoundTrips covers a configured algorithm that was accepted and
// then dropped: the transfer recorded Compressed: false whatever was asked for,
// and the stream went out as it came.
func TestCompressionRoundTrips(t *testing.T) {
	for algorithm, c := range streamCompressors {
		t.Run(algorithm, func(t *testing.T) {
			if _, err := exec.LookPath(c.compress[0]); err != nil {
				t.Skipf("%s is not installed", c.compress[0])
			}

			bm := NewBackupManager(t.TempDir(), datasetUnder("zroot"), nil)
			dir := filepath.Join(t.TempDir(), "backups")

			send := exec.Command("printf", "%s", "stream-contents")
			path, err := bm.streamToFile(t.Context(), send, dir, "web-1", algorithm)
			if err != nil {
				t.Fatalf("streamToFile: %v", err)
			}
			if !strings.HasSuffix(path, c.extension) {
				t.Errorf("file %q does not carry the %s extension", path, algorithm)
			}

			// The bytes on disk are not the stream, and the recorded
			// decompressor turns them back into it.
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) == "stream-contents" {
				t.Errorf("%s wrote the stream uncompressed", algorithm)
			}

			expand := exec.Command(c.decompress[0], c.decompress[1:]...)
			expand.Stdin = bytes.NewReader(raw)
			back, err := expand.Output()
			if err != nil {
				t.Fatalf("%s: %v", c.decompress[0], err)
			}
			if string(back) != "stream-contents" {
				t.Errorf("expanded to %q, want the stream back", back)
			}
		})
	}
}

// TestValidateCompression covers what a configuration may ask for.
func TestValidateCompression(t *testing.T) {
	tests := []struct {
		algorithm   string
		destination string
		wantErr     bool
	}{
		{"", "/backups", false},
		{"none", "/backups", false},
		{"gzip", "/backups", false},
		{"zstd", "/backups", false},
		{"lz4", "/backups", false},
		{"bzip2", "/backups", true},
		{"yes", "/backups", true},
		// Over ssh the far side runs zfs receive with no shell, so nothing
		// there can expand a compressed stream.
		{"gzip", "backup@host:tank/backups", true},
		{"none", "backup@host:tank/backups", false},
	}

	for _, tt := range tests {
		err := ValidateCompression(tt.algorithm, tt.destination)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateCompression(%q, %q) = %v, want error: %v",
				tt.algorithm, tt.destination, err, tt.wantErr)
		}
	}
}
