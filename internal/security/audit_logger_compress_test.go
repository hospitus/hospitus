package security

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCompressLogFile(t *testing.T) {
	tempDir := t.TempDir()
	logFile := filepath.Join(tempDir, "test.log")

	// 1. Create a dummy log file
	originalData := []byte("this is some test log data\nwith multiple lines\n")
	if err := os.WriteFile(logFile, originalData, 0o644); err != nil {
		t.Fatalf("failed to create dummy log file: %v", err)
	}

	// 2. Compress it
	if err := compressLogFile(logFile); err != nil {
		t.Fatalf("compressLogFile failed: %v", err)
	}

	// 3. Verify original file is gone
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Errorf("expected original log file to be removed, but it exists (or another error: %v)", err)
	}

	// 4. Verify compressed file exists
	gzFile := logFile + ".gz"
	if _, err := os.Stat(gzFile); err != nil {
		t.Fatalf("expected compressed log file %s to exist, error: %v", gzFile, err)
	}

	// 5. Verify compressed contents match original data
	f, err := os.Open(gzFile)
	if err != nil {
		t.Fatalf("failed to open compressed file: %v", err)
	}
	defer f.Close()

	gzReader, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer gzReader.Close()

	uncompressedData, err := io.ReadAll(gzReader)
	if err != nil {
		t.Fatalf("failed to read from gzip reader: %v", err)
	}

	if !bytes.Equal(originalData, uncompressedData) {
		t.Errorf("uncompressed data does not match original data.\nGot: %q\nExpected: %q", uncompressedData, originalData)
	}
}
