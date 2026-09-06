package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestDownloadImageIgnoredRangeRestarts covers a server that ignores the Range
// header and answers 200 with the whole body. Appending that to the partial file
// already on disk yields something larger than the original and corrupt in the
// middle — it caches without complaint and only fails later, when the image
// turns out not to decompress.
func TestDownloadImageIgnoredRangeRestarts(t *testing.T) {
	full := bytes.Repeat([]byte("hospitus"), 4096)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Deliberately ignore any Range header, as a CDN or mirror may.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(full)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "image.qcow2")

	// Leave a partial download behind, so a resume is attempted.
	if err := os.WriteFile(dest+".partial", full[:1000], 0o644); err != nil {
		t.Fatalf("seed partial: %v", err)
	}

	if err := downloadImageWithProgress(context.Background(), srv.URL, dest, io.Discard, ""); err != nil {
		t.Fatalf("downloadImageWithProgress: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !bytes.Equal(got, full) {
		t.Errorf("downloaded %d bytes, want %d — the ignored range was appended rather than restarted",
			len(got), len(full))
	}
}

// TestDownloadImageHonouredRangeResumes is the other half: a server that does
// answer 206 must have its bytes appended, or every interrupted download starts
// from zero again.
func TestDownloadImageHonouredRangeResumes(t *testing.T) {
	full := bytes.Repeat([]byte("hospitus"), 4096)
	const have = 1000

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(full)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(full[have:])
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "image.qcow2")
	if err := os.WriteFile(dest+".partial", full[:have], 0o644); err != nil {
		t.Fatalf("seed partial: %v", err)
	}

	if err := downloadImageWithProgress(context.Background(), srv.URL, dest, io.Discard, ""); err != nil {
		t.Fatalf("downloadImageWithProgress: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !bytes.Equal(got, full) {
		t.Errorf("resumed download produced %d bytes, want %d", len(got), len(full))
	}
}
