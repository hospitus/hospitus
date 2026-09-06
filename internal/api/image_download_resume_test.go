package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
			// Refused rather than served whole: answering 200 with the entire
			// body let an implementation that restarts from zero pass this
			// test without ever taking the 206 append path it covers.
			http.Error(w, "expected a Range header", http.StatusBadRequest)
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

// TestDownloadImageRejectsMismatchedContentRange covers a 206 whose
// Content-Range does not answer the Range that was asked for.
//
// A server that resumes from the wrong offset produces a file that is the
// right length and wrong in the middle — it caches happily and only fails much
// later, as an image that will not decompress.
func TestDownloadImageRejectsMismatchedContentRange(t *testing.T) {
	full := bytes.Repeat([]byte("hospitus"), 4096)
	const have = 1000

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(full)
			return
		}
		// Asked to resume at 1000, answers from 2000 and says so.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 2000-%d/%d", len(full)-1, len(full)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(full[2000:])
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "image.qcow2")
	if err := os.WriteFile(dest+".partial", full[:have], 0o644); err != nil {
		t.Fatalf("seed partial: %v", err)
	}

	err := downloadImageWithProgress(context.Background(), srv.URL, dest, io.Discard, "")
	if err == nil {
		t.Fatal("a 206 resuming from the wrong offset was accepted")
	}
	if !strings.Contains(err.Error(), "resumed at byte") {
		t.Errorf("error does not name the cause: %v", err)
	}

	// And nothing was left behind: not at the destination for a later fetch to
	// trust, and not as a partial file either — those bytes are what made the
	// next attempt ask for the same range and get the same bad answer.
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("a rejected resume still produced a file at the destination")
	}
	if _, statErr := os.Stat(dest + ".partial"); statErr == nil {
		t.Error("a rejected resume left its partial file behind")
	}
}

// TestCheckRedirectURLRefusesLoopbackHTTP covers where a redirect may send the
// daemon.
//
// checkDownloadURL lets a loopback host answer over plain HTTP, which is how a
// mirror on the machine itself is served — an operator asks for that by giving
// the address. Applying the same exemption to a redirect let a remote catalog
// point the daemon at any http service on its own host (CWE-918).
func TestCheckRedirectURLRefusesLoopbackHTTP(t *testing.T) {
	for _, tt := range []struct {
		url     string
		wantErr bool
	}{
		{"https://mirror.example.com/base.txz", false},
		{"https://127.0.0.1:8443/base.txz", false},

		{"http://127.0.0.1:8080/admin", true},
		{"http://localhost/admin", true},
		{"http://[::1]:9000/metrics", true},
		{"http://mirror.example.com/base.txz", true},
		{"file:///etc/passwd", true},
		{"://nonsense", true},
	} {
		err := checkRedirectURL(tt.url)
		if (err != nil) != tt.wantErr {
			t.Errorf("checkRedirectURL(%q) = %v, wantErr %v", tt.url, err, tt.wantErr)
		}
	}

	// The initial check still allows a loopback mirror, which is the point of
	// having two rules.
	if err := checkDownloadURL("http://127.0.0.1:8080/base.txz"); err != nil {
		t.Errorf("checkDownloadURL should still allow a loopback mirror: %v", err)
	}
}

// TestLockImageDownloadIsCancellable covers a second fetch of the same image.
//
// The lock was a sync.Mutex, which cannot be acquired with a deadline: the
// waiting request held its goroutine and its connection for the length of the
// transfer ahead of it, and a client hanging up did not release it.
func TestLockImageDownloadIsCancellable(t *testing.T) {
	const dest = "/tmp/hospitus-test-image.txz"

	held, err := lockImageDownload(context.Background(), dest)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer held()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	release, err := lockImageDownload(ctx, dest)
	if err == nil {
		release()
		t.Fatal("the second acquire succeeded while the first still held the slot")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want it to wrap context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the wait took %s: it is not observing the context", elapsed)
	}
}
