package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestDownloadImageIgnoredRangeRestarts covers a server that ignores the Range
// header and answers 200 with the whole body. Appending that to the partial file
// already on disk yields something larger than the original and corrupt in the
// middle — it caches without complaint and only fails later, when the image
// turns out not to decompress.
func TestDownloadImageIgnoredRangeRestarts(t *testing.T) {
	full := bytes.Repeat([]byte("hospitus"), 4096)

	// Guarded: the handler runs on the server's goroutine and the assertion
	// below reads this from the test's, which is a data race under -race.
	var mu sync.Mutex
	var sawRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately ignore any Range header, as a CDN or mirror may.
		mu.Lock()
		sawRange = r.Header.Get("Range")
		mu.Unlock()
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
	// Without this the content check also passes for a run that never tried to
	// resume at all, which is the other way to end up with the right bytes and
	// proves nothing about the branch under test.
	mu.Lock()
	seen := sawRange
	mu.Unlock()
	if seen != "bytes=1000-" {
		t.Errorf("Range header = %q, want \"bytes=1000-\" — no resume was attempted", seen)
	}
}

// TestDownloadImageHonouredRangeResumes is the other half: a server that does
// answer 206 must have its bytes appended, or every interrupted download starts
// from zero again.
func TestDownloadImageHonouredRangeResumes(t *testing.T) {
	full := bytes.Repeat([]byte("hospitus"), 4096)
	const have = 1000

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(full)
			return
		}

		// The offset the client actually asked for, and a Content-Range that
		// matches it. Answering 206 from a fixed offset with no Content-Range
		// made this test pass for a client that resumed from anywhere at all.
		var from int
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-", &from); err != nil || from < 0 || from > len(full) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(full)-1, len(full)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(full[from:])
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
