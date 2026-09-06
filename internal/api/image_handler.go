package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hospitus/hospitus/pkg/image"
	"github.com/hospitus/hospitus/pkg/provider/jail"
	"github.com/hospitus/hospitus/pkg/validation"
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
		s.writeMethodNotAllowed(w, http.MethodPost)
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
		// The catalog is a JSON file the daemon reads and a remote refresh can
		// replace, and this name is joined into a download path below. The
		// request's own name was checked above; the one the catalog answers
		// with has to be too.
		if err := validation.ValidateSnapshotName(profile.Name); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError,
				"The catalog entry for this image has an unusable name", err)
			return
		}
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

		// Type assert to jail provider. A registered provider that is not one
		// is a capability this build does not have, not a server fault: 501.
		jailProvider, ok := jailProviderInterface.(*jail.JailProvider)
		if !ok {
			s.writeError(w, http.StatusNotImplemented, "Base system downloads require the jail provider")
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

	// One download path for both. The catalog carries the digest in the
	// profile, a FreeBSD release in its MANIFEST; that is the only thing that
	// ever differed between the two copies of this code, and they had already
	// drifted apart on progress formatting.
	expectedSHA256 := ""
	switch {
	case profile != nil:
		expectedSHA256 = profile.SHA256
	case release != nil:
		expectedSHA256 = release.SHA256
	}

	if err := downloadImageWithProgress(r.Context(), downloadURL, destination, pw, expectedSHA256); err != nil {
		fmt.Fprintf(w, "ERROR: Failed to download image: %v\n", err)
		flusher.Flush()
		return
	}

	fmt.Fprintf(w, "SUCCESS: Image downloaded successfully: %s\n", destination)
	flusher.Flush()
}

// handleRefreshCatalog triggers a refresh of the image catalog from remote sources
func (s *Server) handleRefreshCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, http.MethodPost)
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
		s.writeMethodNotAllowed(w, http.MethodDelete)
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
			// A regular file, not merely something that exists. Both lists
			// carry an empty entry, so a bare name resolves straight under
			// imageDir — and "sets", "iso" or "cloud" named the category
			// directory itself, which os.Remove then emptied out from under
			// every image in it.
			if info, err := os.Stat(testPath); err == nil && info.Mode().IsRegular() {
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

// imageDownloadLocks is a fixed set of mutexes destinations hash into.
//
// A map keyed by destination would have been simpler to read, but the legacy
// branch builds a destination out of req.Version, so an authenticated caller
// could mint one permanent entry per distinct version and grow the map without
// bound. A fixed array cannot grow, and needs no deletion — which is the other
// way keyed-lock maps go wrong, by removing a mutex someone is blocked on.
//
// Two different destinations can share a mutex. That costs one needless wait
// on a collision and is otherwise harmless: the lock only serializes writers
// to the same file.
// One-slot channels rather than mutexes, so the wait can be abandoned: a
// sync.Mutex cannot be acquired with a deadline, and the download it waits on
// has no total timeout. A second request for the same image held its goroutine
// and its connection for the length of a multi-gigabyte transfer, and hanging
// up did not release it — the request context was only consulted once the lock
// had been taken.
var imageDownloadLocks = func() [64]chan struct{} {
	var locks [64]chan struct{}
	for i := range locks {
		locks[i] = make(chan struct{}, 1)
	}
	return locks
}()

// lockImageDownload waits for this destination to be free and returns the
// release, or reports why the wait ended.
func lockImageDownload(ctx context.Context, destination string) (func(), error) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(destination))
	slot := imageDownloadLocks[h.Sum32()%uint32(len(imageDownloadLocks))]

	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("gave up waiting for another download of %s to finish: %w", destination, ctx.Err())
	}
}

// contentRangeStart reads the first byte position out of a Content-Range
// header of the form "bytes <start>-<end>/<total>".
//
// It reports false for anything it cannot read, which the caller treats the
// same as a wrong offset: a resume it cannot confirm is one it will not make.
func contentRangeStart(header string) (int64, bool) {
	const prefix = "bytes "
	value := strings.TrimSpace(header)
	if !strings.HasPrefix(value, prefix) {
		return 0, false
	}
	value = strings.TrimSpace(strings.TrimPrefix(value, prefix))

	dash := strings.Index(value, "-")
	if dash <= 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(value[:dash], 10, 64)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

// checkDownloadURL refuses a URL whose bytes would arrive unprotected.
//
// The catalog is a file on disk and a remote refresh away from being someone
// else's; plain HTTP over the network hands whoever is in the path the
// contents of every jail built from the image. Loopback stays open because a
// local mirror — and the test server — has no network to be in the middle of.
func checkDownloadURL(rawURL string) error {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid download URL: %w", err)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("refusing to download %s over plain HTTP: use https", parsed.Host)
	default:
		return fmt.Errorf("unsupported download scheme %q: use https", parsed.Scheme)
	}
}

// checkRedirectURL is checkDownloadURL for a hop the daemon was sent to rather
// than one an operator named.
//
// HTTPS only: the loopback exemption exists so a mirror on the machine itself
// can be fetched, and an operator asks for that by giving the address. A remote
// server redirecting there is asking the daemon to read a service it can reach
// and the caller cannot.
func checkRedirectURL(rawURL string) error {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid redirect URL: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("refusing to follow a redirect to %q: only https redirects are followed", parsed.Scheme)
	}
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
	if err := checkDownloadURL(url); err != nil {
		return err
	}

	// One download at a time per destination. Two POSTs for the same version
	// both cleared the exists check, then both opened the same .partial file —
	// the second with O_APPEND, on a size the first was still growing — and
	// interleaved their byte streams into one unusable file.
	unlock, err := lockImageDownload(ctx, destination)
	if err != nil {
		return err
	}
	defer unlock()

	// The other request may have finished while this one waited.
	if _, err := os.Stat(destination); err == nil {
		fmt.Fprintf(progressWriter, "COMPLETE: Image already downloaded: %s\n", destination)
		return nil
	}

	fmt.Fprintf(progressWriter, "Downloading from: %s\n", url)
	fmt.Fprintf(progressWriter, "To: %s\n", destination)

	if expectedSHA256 == "" {
		// The catalog does not pin every image yet, and a FreeBSD MANIFEST
		// fetch is best-effort. Say so rather than letting an unverified image
		// land silently.
		fmt.Fprintf(progressWriter, "WARNING: no SHA-256 published for this image; contents cannot be verified\n")
	}

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
		// Every hop, not only the first: checkDownloadURL above passes an
		// https catalog URL, and the default policy would follow a redirect
		// from it to http:// and pull the image in cleartext.
		//
		// Stricter than the initial check, though: that one lets a loopback
		// host answer over plain HTTP, which is how a local mirror is served.
		// Honoring the same exemption on a redirect let a remote catalog
		// point the daemon at any http service on the host it runs on.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return checkRedirectURL(req.URL.String())
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

	// A partial larger than the object it resumes can never satisfy its own
	// range: every retry sends the same Range and is refused the same way, and
	// nothing else removes the file. Discard it so the next attempt starts
	// from zero.
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && resumeFrom > 0 {
		_ = os.Remove(partialFile)
		return fmt.Errorf("the partial download could not satisfy its resume range and has been discarded; retry the fetch")
	}

	// Check status code
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	// A 206 nobody asked for is refused. With resumeFrom at 0 no Range header
	// was sent, the checks below are skipped, and the file opens with O_TRUNC —
	// so a partial body was written out as the whole image and, with no digest
	// to catch it, cached under the final name.
	if resumeFrom == 0 && resp.StatusCode == http.StatusPartialContent {
		return fmt.Errorf("server answered 206 to a request that asked for no range")
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

	// And a 206 has to resume from where we asked. Refused rather than
	// restarted: the body in hand is already a partial one from the wrong
	// place, so there is nothing here to salvage — appending it produces a
	// file of plausible length that is wrong in the middle, and truncating
	// first produces one that is simply short.
	//
	// The partial file is removed here, explicitly. The deferred cleanup that
	// would otherwise do it is registered further down, after the file is
	// opened, so returning from this point skipped it — and the bytes left
	// behind made the next attempt ask for the same range and get the same
	// bad answer.
	if resumeFrom > 0 {
		start, ok := contentRangeStart(resp.Header.Get("Content-Range"))
		if !ok {
			_ = os.Remove(partialFile)
			return fmt.Errorf("server answered 206 without a usable Content-Range (%q)",
				resp.Header.Get("Content-Range"))
		}
		if start != resumeFrom {
			_ = os.Remove(partialFile)
			return fmt.Errorf("server resumed at byte %d, not the %d that was asked for", start, resumeFrom)
		}
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

	// Before the rename, not after. Hashing the file at its final name left
	// unverified content sitting at destination for the length of the hash: a
	// concurrent fetch answered "already_exists" for it, a create used it, and
	// a daemon that exited mid-hash left it there to be trusted forever.
	if expectedSHA256 != "" {
		fmt.Fprintf(progressWriter, "Verifying SHA256 checksum...\n")
		if err := verifySHA256(partialFile, expectedSHA256); err != nil {
			_ = os.Remove(partialFile)
			return fmt.Errorf("integrity check failed: %w", err)
		}
		fmt.Fprintf(progressWriter, "SHA256 verified OK\n")
	}

	// Move partial file to final destination
	if err := os.Rename(partialFile, destination); err != nil {
		return fmt.Errorf("failed to finalize download: %w", err)
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
