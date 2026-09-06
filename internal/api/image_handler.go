package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hospitus/hospitus/pkg/image"
	"github.com/hospitus/hospitus/pkg/provider/jail"
)

// FetchImageRequest represents a request to fetch a base system image
type FetchImageRequest struct {
	Version string `json:"version"`
}

// progressWriter wraps an http.ResponseWriter for streaming progress updates
type progressWriter struct {
	writer  io.Writer
	flusher http.Flusher
}

func (pw *progressWriter) Write(p []byte) (n int, err error) {
	n, err = pw.writer.Write(p)
	if pw.flusher != nil {
		pw.flusher.Flush()
	}
	return
}

// timeoutReader wraps an io.Reader and returns an error if no data is received within the timeout
type timeoutReader struct {
	reader  io.Reader
	timeout time.Duration

	mu           sync.Mutex
	lastActivity time.Time
	timedOut     bool

	stop chan struct{}
	once sync.Once
}

// verifySHA256 computes the SHA256 of a file and compares it to the expected hex digest.
// Returns an error if the file cannot be read or the digest does not match.
func verifySHA256(path, expectedHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("sha256: failed to open file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("sha256: failed to hash file: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expectedHex) {
		return fmt.Errorf("sha256 mismatch: expected %s, got %s", expectedHex, got)
	}
	return nil
}

// errDownloadStalled marks a transfer the watchdog cut off. A caller that
// wants to tell a stall from a network error — to decide whether the partial
// file is worth keeping, say — matches on this rather than on the message.
var errDownloadStalled = errors.New("download timeout")

// newTimeoutReader wraps a download body so a stalled transfer ends.
//
// The reader is closed to unblock a Read that has already started: checking
// the clock at the top of Read only catches a stall between two reads, and a
// server that accepts the connection and then sends nothing left the read
// blocked for as long as the connection stayed open.
func newTimeoutReader(r io.ReadCloser, timeout time.Duration) *timeoutReader {
	tr := &timeoutReader{
		reader:       r,
		timeout:      timeout,
		lastActivity: time.Now(),
		stop:         make(chan struct{}),
	}

	// A non-positive timeout means "no watchdog": time.NewTicker panics on one,
	// and a caller asking for no inactivity limit should get exactly that
	// rather than a crash.
	if timeout <= 0 {
		return tr
	}

	go func() {
		ticker := time.NewTicker(timeout / 4)
		defer ticker.Stop()
		for {
			select {
			case <-tr.stop:
				return
			case <-ticker.C:
				tr.mu.Lock()
				idle := time.Since(tr.lastActivity)
				tr.mu.Unlock()
				if idle > timeout {
					// Recorded before the close, so the Read this unblocks
					// reports the timeout rather than the
					// "use of closed network connection" that closing it
					// produces.
					tr.mu.Lock()
					tr.timedOut = true
					tr.mu.Unlock()
					_ = r.Close()
					return
				}
			}
		}
	}()

	return tr
}

// Close stops the watchdog. The body itself belongs to the caller.
func (tr *timeoutReader) Close() {
	tr.once.Do(func() { close(tr.stop) })
}

func (tr *timeoutReader) Read(p []byte) (n int, err error) {
	tr.mu.Lock()
	idle := time.Since(tr.lastActivity)
	tr.mu.Unlock()
	if tr.timeout > 0 && idle > tr.timeout {
		return 0, fmt.Errorf("%w: no data received for %v", errDownloadStalled, tr.timeout)
	}

	n, err = tr.reader.Read(p)
	tr.mu.Lock()
	if n > 0 {
		tr.lastActivity = time.Now()
	}
	timedOut := tr.timedOut
	tr.mu.Unlock()

	if err != nil && timedOut {
		return n, fmt.Errorf("%w: no data received for %v", errDownloadStalled, tr.timeout)
	}
	return n, err
}

// handleListImages returns all downloaded images
func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	imageDir := s.resolveImageDir()

	catalog := image.NewCatalog(imageDir)
	images, err := catalog.ListDownloaded()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to list images: %v", err))
		return
	}

	s.writeJSON(w, http.StatusOK, images)
}

// resolveImageDir returns the directory where images are stored, honoring the
// HOSPITUS_IMAGE_DIR override and otherwise deriving it from the server data dir.
func (s *Server) resolveImageDir() string {
	if dir := os.Getenv("HOSPITUS_IMAGE_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(s.config.DataDir, "images")
}

// handleFetchImage handles downloading a base system image from catalog or FreeBSD
func (s *Server) handleFetchImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req FetchImageRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if req.Version == "" {
		s.writeError(w, http.StatusBadRequest, "Version is required")
		return
	}

	// Get image directory — derive from server config
	imageDir := s.resolveImageDir()

	// Create catalog to look up profile
	catalog := image.NewCatalog(imageDir)

	// Strip optional type prefix (e.g. "cloud:ubuntu-24.04" → "ubuntu-24.04").
	// The catalog stores names without a prefix; the prefix is a user-facing
	// namespace used in manifest source fields and the CLI.
	imageName := req.Version
	if idx := strings.Index(imageName, ":"); idx >= 0 {
		imageName = imageName[idx+1:]
	}

	// SECURITY: Validate image name to prevent path traversal. Checked on the full
	// request value: the legacy branch below builds the destination from
	// req.Version, not from the stripped imageName.
	if strings.Contains(req.Version, "..") || strings.Contains(req.Version, "/") || strings.Contains(req.Version, "\\") {
		s.writeError(w, http.StatusBadRequest, "Invalid image name: path traversal characters not allowed")
		return
	}
	if !utf8.ValidString(imageName) {
		s.writeError(w, http.StatusBadRequest, "Invalid image name: must be valid UTF-8")
		return
	}

	// Look up the image by the name the caller typed, long or short, and store
	// it under the catalog's own name. Both spellings must reach the same
	// entry: a short name that misses the catalog falls through to the legacy
	// branch below, which fetches a second copy of the same release into the
	// images root while the catalog name puts it under sets/.
	profile := catalog.ResolveProfile(imageName)
	if profile != nil {
		imageName = profile.Name
	}

	var downloadURL string
	var destination string
	var fileExt string
	var release *jail.OSRelease

	if profile != nil {
		// Found in catalog - use the profile's URL
		downloadURL = profile.URL
		fileExt = "." + string(profile.Format)
		switch fileExt {
		case ".txz", ".tar.xz", ".tar.gz", ".tgz":
			// Store in sets subdirectory
			subDir := catalog.GetSubDir(image.CategorySet)
			if err := os.MkdirAll(subDir, 0o755); err != nil {
				s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to create image directory: %v", err))
				return
			}
			destination = filepath.Join(subDir, imageName+fileExt)
		case ".iso":
			subDir := catalog.GetSubDir(image.CategoryISO)
			if err := os.MkdirAll(subDir, 0o755); err != nil {
				s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to create image directory: %v", err))
				return
			}
			destination = filepath.Join(subDir, imageName+fileExt)
		default:
			subDir := catalog.GetSubDir(image.CategoryCloud)
			if err := os.MkdirAll(subDir, 0o755); err != nil {
				s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to create image directory: %v", err))
				return
			}
			destination = filepath.Join(subDir, imageName+fileExt)
		}
	} else {
		// Not in catalog - try legacy FreeBSD format
		// Get jail provider from registry
		jailProviderInterface, err := s.registry.Get("jail")
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Jail provider not available: %v", err))
			return
		}

		// Type assert to jail provider
		jailProvider, ok := jailProviderInterface.(*jail.JailProvider)
		if !ok {
			s.writeError(w, http.StatusInternalServerError, "Failed to get jail provider")
			return
		}

		// Parse version string (e.g., "15.0-STABLE-amd64" or "14.1-RELEASE-amd64")
		parts := strings.Split(req.Version, "-")
		if len(parts) < 2 {
			s.writeError(w, http.StatusBadRequest, "Image not found in catalog. For FreeBSD images, use format: VERSION-RELEASE-ARCH (e.g., 14.1-RELEASE-amd64)")
			return
		}

		// Determine OS type and architecture
		arch := parts[len(parts)-1]
		fullVersion := strings.Join(parts[:len(parts)-1], "-")

		// The last segment has to name an architecture. Without this, any name
		// with a dash in it was read as a FreeBSD release and turned into a
		// download URL: "cloud:ubuntu-24.04" became version "cloud:ubuntu" on
		// architecture "24.04", and the reader got
		//
		//	Failed to download base system: download failed with status: 404
		//
		// for an image that was not in the catalog.
		if !isFreeBSDArch(arch) {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"Image %q not found. Use 'hospitus image available' to see available images, "+
					"or name a FreeBSD release as VERSION-RELEASE-ARCH (e.g. 14.3-RELEASE-amd64).",
				req.Version))
			return
		}

		// Get OS release info
		var err2 error
		release, err2 = jailProvider.GetOSRelease("freebsd", fullVersion, arch)
		if err2 != nil {
			s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Image not found. Use 'hospitus image available' to see available images. Error: %v", err2))
			return
		}

		// Best-effort: fetch SHA256 from FreeBSD MANIFEST for integrity verification
		if sha, _ := jail.FetchManifestSHA256(arch, fullVersion); sha != "" {
			release.SHA256 = sha
		}

		// Build URL from release info
		if release.BaseURL != "" {
			downloadURL = release.BaseURL
		} else {
			// Extreme fallback if provider gave nothing
			downloadURL = fmt.Sprintf("https://download.freebsd.org/releases/%s/%s/base.txz",
				arch, fullVersion)
		}

		// Create directory if it doesn't exist
		if err := os.MkdirAll(imageDir, 0o755); err != nil {
			s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to create image directory: %v", err))
			return
		}

		destination = filepath.Join(imageDir, fmt.Sprintf("%s.txz", req.Version))
	}

	// Check if already exists
	if _, err := os.Stat(destination); err == nil {
		// Clean up any partial file
		os.Remove(destination + ".partial")
		s.writeJSON(w, http.StatusOK, map[string]string{
			"status":  "already_exists",
			"message": fmt.Sprintf("Image already exists: %s", destination),
			"path":    destination,
		})
		return
	}

	// Get flusher for real-time streaming (check before writing headers)
	flusher, ok := w.(http.Flusher)
	if !ok {
		// This should not happen with our loggingResponseWriter
		s.writeError(w, http.StatusInternalServerError, "Streaming not supported")
		return
	}

	// Set headers for streaming response
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	// Create progress writer
	pw := &progressWriter{
		writer:  w,
		flusher: flusher,
	}

	if profile != nil {
		// Download with progress streaming (catalog image)
		if err := downloadImageWithProgress(r.Context(), downloadURL, destination, pw, profile.SHA256); err != nil {
			fmt.Fprintf(w, "ERROR: Failed to download image: %v\n", err)
			flusher.Flush()
			return
		}
	} else {
		// 'release' comes from earlier in the same function scope

		// Download with progress streaming (base system)
		if err := downloadBaseSystemWithProgress(r.Context(), release, downloadURL, destination, pw); err != nil {
			fmt.Fprintf(w, "ERROR: Failed to download base system: %v\n", err)
			flusher.Flush()
			return
		}
	}

	fmt.Fprintf(w, "SUCCESS: Image downloaded successfully: %s\n", destination)
	flusher.Flush()
}

// handleRefreshCatalog triggers a refresh of the image catalog from remote sources
func (s *Server) handleRefreshCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Get image directory — derive from server config
	imageDir := s.resolveImageDir()

	catalog := image.NewCatalog(imageDir)
	if err := catalog.Refresh(r.Context()); err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to refresh catalog: %v", err))
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{
		"status": "refreshed",
	})
}

// handleDeleteImage handles deleting a base system image
func (s *Server) handleDeleteImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Get image name from URL path: /api/v1/images/{name}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/images/")
	if path == "" {
		s.writeError(w, http.StatusBadRequest, "Image name is required")
		return
	}

	imageName := path

	// SECURITY: Validate image name to prevent path traversal
	if strings.Contains(imageName, "..") || strings.Contains(imageName, "/") || strings.Contains(imageName, "\\") {
		s.writeError(w, http.StatusBadRequest, "Invalid image name")
		return
	}

	// Get image directory — derive from server config
	imageDir := s.resolveImageDir()

	// Search for the image in all subdirectories and with multiple extensions
	subdirs := []string{"sets", "iso", "cloud", ""}
	extensions := []string{"", ".txz", ".tar.gz", ".tar.xz", ".tgz", ".iso", ".qcow2", ".raw", ".img"}

	var imagePath string
	var found bool

	for _, subdir := range subdirs {
		baseDir := imageDir
		if subdir != "" {
			baseDir = filepath.Join(imageDir, subdir)
		}

		for _, ext := range extensions {
			testPath := filepath.Join(baseDir, imageName+ext)
			if _, err := os.Stat(testPath); err == nil {
				imagePath = testPath
				found = true
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("Image not found: %s", imageName))
		return
	}

	// Delete the file
	if err := os.Remove(imagePath); err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to delete image: %v", err))
		return
	}

	// Also try to delete partial file if it exists
	partialPath := imagePath + ".partial"
	os.Remove(partialPath) // Ignore error if it doesn't exist

	s.writeJSON(w, http.StatusOK, map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("Image deleted: %s", imageName),
	})
}

// downloadBaseSystemWithProgress downloads a FreeBSD base system with progress streaming
func downloadBaseSystemWithProgress(ctx context.Context, release *jail.OSRelease, url, destination string, progressWriter io.Writer) error {
	fmt.Fprintf(progressWriter, "Downloading from: %s\n", url)
	fmt.Fprintf(progressWriter, "To: %s\n", destination)

	// Check if partial download exists
	var resumeFrom int64 = 0
	partialFile := destination + ".partial"
	if stat, err := os.Stat(partialFile); err == nil {
		resumeFrom = stat.Size()
		fmt.Fprintf(progressWriter, "Resuming download from %d bytes\n", resumeFrom)
	}

	// Create HTTP client with no total timeout, but with connection timeouts
	client := &http.Client{
		Timeout: 0, // No total timeout (download can take as long as needed)
		Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second, // 30s to get response headers
			IdleConnTimeout:       90 * time.Second, // 90s idle connection timeout
		},
	}

	// Create request with Range header for resume support
	// With the context, not without: the parameter was typed interface{} and
	// never used, so a client that disconnected left the daemon pulling a
	// multi-gigabyte image nobody was waiting for.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}

	// Execute request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	// A server is free to ignore the Range header and answer 200 with the whole
	// body. Appending that to the bytes already on disk produces a file larger
	// than the original and corrupt in the middle — it caches happily and only
	// fails much later, as an image that will not decompress. Resume only when
	// the server says it is sending a range.
	if resumeFrom > 0 && resp.StatusCode != http.StatusPartialContent {
		fmt.Fprintf(progressWriter, "Server ignored the resume request; starting over\n")
		resumeFrom = 0
	}

	// Wrap response body with inactivity timeout (60 seconds without data = timeout)
	timeoutBody := newTimeoutReader(resp.Body, 60*time.Second)
	defer timeoutBody.Close()

	// Get total size
	// A server that does not say reports -1. Adding resumeFrom to that yields
	// a total that is positive but wrong — writeProgress then divides by it
	// and prints a percentage of a number nobody sent.
	totalSize := resp.ContentLength
	if totalSize < 0 {
		totalSize = 0
	} else if resumeFrom > 0 {
		totalSize += resumeFrom
	}

	// Open file for writing (append mode if resuming)
	var flags int
	if resumeFrom > 0 {
		flags = os.O_APPEND | os.O_WRONLY
	} else {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}

	out, err := os.OpenFile(partialFile, flags, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	cleanupNeeded := true
	stalled := false
	defer func() {
		out.Close()
		// An interrupted transfer keeps its partial file: the caller hung up,
		// the daemon is shutting down, or the connection went quiet, and the
		// bytes already on disk are exactly what the Range header resumes from
		// next time. Deleting them made every such download start over — the
		// cost the resume support exists to avoid.
		//
		// What is still removed is a body that arrived and was wrong: a
		// checksum mismatch or a write failure leaves a file no resume should
		// build on.
		if cleanupNeeded && ctx.Err() == nil && !stalled {
			os.Remove(partialFile)
		}
	}()

	// Download with progress tracking
	startTime := time.Now()
	downloaded := resumeFrom
	lastPrint := time.Now()
	lastHeartbeat := time.Now()

	buffer := make([]byte, 32*1024) // 32KB buffer
	for {
		n, err := timeoutBody.Read(buffer)
		if n > 0 {
			if _, writeErr := out.Write(buffer[:n]); writeErr != nil {
				return fmt.Errorf("failed to write to file: %w", writeErr)
			}
			downloaded += int64(n)

			now := time.Now()

			// Print progress every second
			if now.Sub(lastPrint) >= time.Second {
				elapsed := now.Sub(startTime)
				speed := float64(downloaded-resumeFrom) / elapsed.Seconds() / 1024 / 1024 // MB/s

				writeProgress(progressWriter, downloaded, totalSize, speed)

				lastPrint = now
				lastHeartbeat = now
			} else if now.Sub(lastHeartbeat) >= 500*time.Millisecond {
				// Send heartbeat every 500ms to keep connection alive
				fmt.Fprintf(progressWriter, "HEARTBEAT\n")
				lastHeartbeat = now
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			stalled = errors.Is(err, errDownloadStalled)
			return fmt.Errorf("download error: %w", err)
		}
	}

	// Move partial file to final destination
	if err := os.Rename(partialFile, destination); err != nil {
		return fmt.Errorf("failed to finalize download: %w", err)
	}

	// Verify SHA256 against FreeBSD MANIFEST if available
	if release != nil && release.SHA256 != "" {
		fmt.Fprintf(progressWriter, "Verifying SHA256 checksum...\n")
		if err := verifySHA256(destination, release.SHA256); err != nil {
			_ = os.Remove(destination)
			return fmt.Errorf("integrity check failed: %w", err)
		}
		fmt.Fprintf(progressWriter, "SHA256 verified OK\n")
	}

	// Success — keep the file; defer won't clean it up
	cleanupNeeded = false

	elapsed := time.Since(startTime)
	avgSpeed := float64(downloaded-resumeFrom) / elapsed.Seconds() / 1024 / 1024
	fmt.Fprintf(progressWriter, "COMPLETE: Download completed in %s (avg %.2f MB/s)\n", elapsed.Round(time.Second), avgSpeed)

	return nil
}

// isFreeBSDArch reports whether s names an architecture FreeBSD publishes base
// systems for.
//
// It is the test that separates a FreeBSD release from a name that merely has
// a dash in it, so an image the catalog does not carry is reported as missing
// instead of being turned into a download URL that answers 404.
func isFreeBSDArch(s string) bool {
	switch strings.ToLower(s) {
	case "amd64", "i386", "arm64", "aarch64", "riscv64",
		"armv6", "armv7", "powerpc", "powerpc64", "powerpc64le", "powerpcspe":
		return true
	}
	return false
}

// downloadImageWithProgress downloads any image with progress streaming (used for catalog images)
func downloadImageWithProgress(ctx context.Context, url, destination string, progressWriter io.Writer, expectedSHA256 string) error {
	fmt.Fprintf(progressWriter, "Downloading from: %s\n", url)
	fmt.Fprintf(progressWriter, "To: %s\n", destination)

	// Check if partial download exists
	var resumeFrom int64 = 0
	partialFile := destination + ".partial"
	if stat, err := os.Stat(partialFile); err == nil {
		resumeFrom = stat.Size()
		fmt.Fprintf(progressWriter, "Resuming download from %d bytes\n", resumeFrom)
	}

	// Create HTTP client with no total timeout, but with connection timeouts
	client := &http.Client{
		Timeout: 0, // No total timeout (download can take as long as needed)
		Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second, // 30s to get response headers
			IdleConnTimeout:       90 * time.Second, // 90s idle connection timeout
		},
	}

	// Create request with Range header for resume support
	// With the context, not without: the parameter was typed interface{} and
	// never used, so a client that disconnected left the daemon pulling a
	// multi-gigabyte image nobody was waiting for.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}

	// Execute request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	// A server is free to ignore the Range header and answer 200 with the whole
	// body. Appending that to the bytes already on disk produces a file larger
	// than the original and corrupt in the middle — it caches happily and only
	// fails much later, as an image that will not decompress. Resume only when
	// the server says it is sending a range.
	if resumeFrom > 0 && resp.StatusCode != http.StatusPartialContent {
		fmt.Fprintf(progressWriter, "Server ignored the resume request; starting over\n")
		resumeFrom = 0
	}

	// Wrap response body with inactivity timeout (60 seconds without data = timeout)
	timeoutBody := newTimeoutReader(resp.Body, 60*time.Second)
	defer timeoutBody.Close()

	// Get total size
	// A server that does not say reports -1. Adding resumeFrom to that yields
	// a total that is positive but wrong — writeProgress then divides by it
	// and prints a percentage of a number nobody sent.
	totalSize := resp.ContentLength
	if totalSize < 0 {
		totalSize = 0
	} else if resumeFrom > 0 {
		totalSize += resumeFrom
	}

	// Open file for writing (append mode if resuming)
	var flags int
	if resumeFrom > 0 {
		flags = os.O_APPEND | os.O_WRONLY
	} else {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}

	out, err := os.OpenFile(partialFile, flags, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	cleanupNeeded := true
	stalled := false
	defer func() {
		out.Close()
		// An interrupted transfer keeps its partial file: the caller hung up,
		// the daemon is shutting down, or the connection went quiet, and the
		// bytes already on disk are exactly what the Range header resumes from
		// next time. Deleting them made every such download start over — the
		// cost the resume support exists to avoid.
		//
		// What is still removed is a body that arrived and was wrong: a
		// checksum mismatch or a write failure leaves a file no resume should
		// build on.
		if cleanupNeeded && ctx.Err() == nil && !stalled {
			os.Remove(partialFile)
		}
	}()

	// Download with progress tracking
	startTime := time.Now()
	downloaded := resumeFrom
	lastPrint := time.Now()
	lastHeartbeat := time.Now()

	buffer := make([]byte, 32*1024) // 32KB buffer
	for {
		n, err := timeoutBody.Read(buffer)
		if n > 0 {
			if _, writeErr := out.Write(buffer[:n]); writeErr != nil {
				return fmt.Errorf("failed to write to file: %w", writeErr)
			}
			downloaded += int64(n)

			now := time.Now()

			// Print progress every second
			if now.Sub(lastPrint) >= time.Second {
				elapsed := now.Sub(startTime)
				speed := float64(downloaded-resumeFrom) / elapsed.Seconds() / 1024 / 1024 // MB/s
				writeProgress(progressWriter, downloaded, totalSize, speed)

				lastPrint = now
				lastHeartbeat = now
			} else if now.Sub(lastHeartbeat) >= 500*time.Millisecond {
				// Send heartbeat every 500ms to keep connection alive
				fmt.Fprintf(progressWriter, "HEARTBEAT\n")
				lastHeartbeat = now
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			stalled = errors.Is(err, errDownloadStalled)
			return fmt.Errorf("download error: %w", err)
		}
	}

	// Move partial file to final destination
	if err := os.Rename(partialFile, destination); err != nil {
		return fmt.Errorf("failed to finalize download: %w", err)
	}

	// Verify SHA256 if provided in catalog profile
	if expectedSHA256 != "" {
		fmt.Fprintf(progressWriter, "Verifying SHA256 checksum...\n")
		if err := verifySHA256(destination, expectedSHA256); err != nil {
			_ = os.Remove(destination)
			return fmt.Errorf("integrity check failed: %w", err)
		}
		fmt.Fprintf(progressWriter, "SHA256 verified OK\n")
	}

	// Success — keep the file; defer won't clean it up
	cleanupNeeded = false

	elapsed := time.Since(startTime)
	avgSpeed := float64(downloaded-resumeFrom) / elapsed.Seconds() / 1024 / 1024
	fmt.Fprintf(progressWriter, "COMPLETE: Download completed in %s (avg %.2f MB/s)\n", elapsed.Round(time.Second), avgSpeed)

	return nil
}

// writeProgress renders one progress line for a download.
//
// Shared by the two download paths, which had drifted: one guarded a
// non-positive totalSize and the other divided by it, printing "-0.0%" of a
// negative total for a server that sends no Content-Length.
func writeProgress(w io.Writer, downloaded, totalSize int64, speedMBps float64) {
	if totalSize > 0 {
		fmt.Fprintf(w, "PROGRESS: %.1f%% (%d MB / %d MB) @ %.2f MB/s\n",
			float64(downloaded)/float64(totalSize)*100,
			downloaded/1024/1024,
			totalSize/1024/1024,
			speedMBps)
		return
	}
	fmt.Fprintf(w, "PROGRESS: %d MB downloaded @ %.2f MB/s\n",
		downloaded/1024/1024, speedMBps)
}
