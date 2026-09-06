package jail

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// OSRelease represents a FreeBSD or Linux release
type OSRelease struct {
	Type    string // freebsd, linux
	Version string // 13.2-RELEASE, 14.0-CURRENT, ubuntu-22.04, debian-12
	Arch    string // amd64, arm64, riscv64, i386
	BaseURL string // URL to base system archive
	SHA256  string // expected SHA256 hex digest of the archive (optional)
	// GPGKey is the URL of the distribution's package-signing key, for the
	// RHEL-family path: dnf needs it to verify what it installs, and without
	// one the whole base system arrives from the network unchecked.
	GPGKey string
}

// digestFromManifest reads the SHA-256 of the archive named by archiveURL from
// the MANIFEST that sits beside it.
func (p *JailProvider) digestFromManifest(ctx context.Context, tmpDir, archiveURL string) (string, error) {
	idx := strings.LastIndex(archiveURL, "/")
	if idx < 0 {
		return "", fmt.Errorf("cannot locate a MANIFEST for %s", archiveURL)
	}
	manifestURL := archiveURL[:idx+1] + "MANIFEST"
	archiveName := archiveURL[idx+1:]

	manifestPath := filepath.Join(tmpDir, "MANIFEST")
	if out, err := p.cmd().CombinedOutput(ctx, "fetch", "-o", manifestPath, manifestURL); err != nil {
		return "", fmt.Errorf("cannot fetch %s to verify the base system: %w (output: %s)",
			manifestURL, err, strings.TrimSpace(string(out)))
	}

	data, err := os.ReadFile(manifestPath) //nolint:gosec // G304: path built from our own temp dir
	if err != nil {
		return "", fmt.Errorf("cannot read the fetched MANIFEST: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) >= 2 && fields[0] == archiveName {
			return strings.TrimSpace(fields[1]), nil
		}
	}
	return "", fmt.Errorf("%s lists no digest for %s", manifestURL, archiveName)
}

// verifyArchiveDigest compares a downloaded archive against its expected
// SHA-256. An empty expectation is refused rather than skipped: the caller has
// no way to tell "verified" from "never checked" otherwise.
func verifyArchiveDigest(path, expected string) error {
	if expected == "" {
		return fmt.Errorf("no SHA-256 is recorded for this base system; refusing to extract %s unverified", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot read the downloaded archive: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("cannot read the downloaded archive: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, expected) {
		return fmt.Errorf("base system digest mismatch: got %s, expected %s", got, expected)
	}
	return nil
}

// freeBSDArchPath maps an architecture to its FreeBSD mirror path segment(s).
// amd64/i386 are single-segment; arm64 and riscv64 require the machine segment
// (aarch64 / riscv64) that the mirrors use under the CPU family directory.
func freeBSDArchPath(arch string) string {
	switch arch {
	case "arm64", "aarch64":
		return "arm64/aarch64"
	case "riscv64", "riscv":
		return "riscv/riscv64"
	case "386":
		return "i386"
	default: // amd64, i386, ...
		return arch
	}
}

// GetOSRelease returns OS release information, constructing the download URL
// for any FreeBSD version.
func (p *JailProvider) GetOSRelease(osType, osVersion, arch string) (*OSRelease, error) {
	// Default to host architecture if not specified
	if arch == "" {
		arch = runtime.GOARCH
	}

	// Dynamic FreeBSD resolution
	if osType == "freebsd" || osType == "" {
		// Construct the most likely URL for FreeBSD mirrors. Non-x86
		// architectures need an extra "machine" path segment
		// (e.g. arm64/aarch64, riscv/riscv64); amd64/i386 use a single segment.
		archPath := freeBSDArchPath(arch)

		baseURL := fmt.Sprintf("https://download.freebsd.org/releases/%s/%s/base.txz", archPath, osVersion)

		// Handle snapshots (CURRENT/STABLE)
		if strings.Contains(osVersion, "CURRENT") || strings.Contains(osVersion, "STABLE") {
			baseURL = fmt.Sprintf("https://download.freebsd.org/snapshots/%s/%s/base.txz", archPath, osVersion)
		}

		return &OSRelease{
			Type:    "freebsd",
			Version: osVersion,
			Arch:    arch,
			BaseURL: baseURL,
		}, nil
	}

	// For other types (linux, etc.), return basic info
	// URL construction for Linux is too varied to be dynamic easily without a catalog
	return &OSRelease{
		Type:    osType,
		Version: osVersion,
		Arch:    arch,
	}, nil
}

// FetchManifestSHA256 downloads the FreeBSD MANIFEST file and returns the SHA256
// digest for base.txz. Returns an empty string (no error) if the MANIFEST is
// unavailable so that callers can proceed without verification when offline.
func FetchManifestSHA256(arch, osVersion string) (string, error) {
	archPath := freeBSDArchPath(arch)
	manifestURL := fmt.Sprintf("https://download.freebsd.org/releases/%s/%s/MANIFEST", archPath, osVersion)
	if strings.Contains(osVersion, "CURRENT") || strings.Contains(osVersion, "STABLE") {
		manifestURL = fmt.Sprintf("https://download.freebsd.org/snapshots/%s/%s/MANIFEST", archPath, osVersion)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(manifestURL)
	if err != nil {
		return "", nil // Network unavailable — skip verification
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil // MANIFEST not published for this release — skip
	}

	// MANIFEST format: <filename>\t<SHA256_hex>\t<size_bytes>\t...\n
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "base.txz") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return fields[1], nil
			}
		}
	}
	return "", nil
}

// DownloadBaseSystem downloads a FreeBSD base system
func (p *JailProvider) DownloadBaseSystem(ctx context.Context, release *OSRelease, destination string) error {
	if release.BaseURL == "" {
		return fmt.Errorf("no base URL for %s %s %s", release.Type, release.Version, release.Arch)
	}

	// Create temp directory for download
	tmpDir, err := os.MkdirTemp("", "hospitus-base-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, "base.txz")

	// Download base system
	// Using fetch(1) on FreeBSD
	p.logInfo(ctx, "downloading base system", "os_type", release.Type, "version", release.Version, "arch", release.Arch, "url", release.BaseURL, "archive_path", archivePath)
	output, err := p.cmd().CombinedOutput(ctx, "fetch", "-o", archivePath, release.BaseURL)
	if err != nil {
		return fmt.Errorf("failed to download base system: %w (output: %s)", err, string(output))
	}

	// The archive is unpacked into the jail root as root. A mirror that was
	// tampered with, or a truncated response, would be extracted just the same.
	expected := release.SHA256
	if expected == "" {
		// FreeBSD publishes a MANIFEST beside the archive: tab-separated, the
		// file name then its SHA-256. Fetching it from the same directory is
		// how the digest is known without pinning one per release here.
		expected, err = p.digestFromManifest(ctx, tmpDir, release.BaseURL)
		if err != nil {
			return err
		}
	}
	if err := verifyArchiveDigest(archivePath, expected); err != nil {
		return err
	}

	// Extract to destination
	p.logInfo(ctx, "extracting downloaded base system", "archive_path", archivePath, "destination", destination)
	output, err = p.cmd().CombinedOutput(ctx, "tar", "-xf", archivePath, "-C", destination)
	if err != nil {
		return fmt.Errorf("failed to extract base system: %w (output: %s)", err, string(output))
	}

	p.logInfo(ctx, "base system extracted successfully", "destination", destination, "os_type", release.Type, "version", release.Version, "arch", release.Arch)
	return nil
}

// extractBaseSystem extracts a FreeBSD or Linux base system
func (p *JailProvider) extractBaseSystem(ctx context.Context, image, destination, arch string) error {
	if image == "" {
		return nil
	}

	// Check if image is a local file
	if _, err := os.Stat(image); err == nil {
		p.logInfo(ctx, "extracting base system from local archive", "image", image, "destination", destination)
		output, err := p.cmd().CombinedOutput(ctx, "tar", "-xf", image, "-C", destination)
		if err != nil {
			return fmt.Errorf("failed to extract base system: %w (output: %s)", err, string(output))
		}
		return nil
	}

	// Beyond the explicit local-file case above, image is treated as a catalog
	// name that is joined onto the image directory. Reject path separators and
	// ".." so a crafted name cannot escape imageDir (CWE-22).
	if strings.Contains(image, "..") || strings.ContainsAny(image, `/\`) {
		return fmt.Errorf("invalid image name %q: must not contain path separators or '..'", image)
	}

	// Standard image directory — default to DataDir/images with HOSPITUS_IMAGE_DIR override
	imageDir := filepath.Join(p.config.DataDir, "images")
	if envDir := os.Getenv("HOSPITUS_IMAGE_DIR"); envDir != "" {
		imageDir = envDir
	}

	extensions := []string{".txz", ".tar.gz", ".tar.xz", ".tar.bz2", ".tgz"}
	searchDirs := []string{
		filepath.Join(imageDir, "sets"),
		imageDir,
	}

	// A requested architecture is the only one tried. Falling back to the host's
	// would hand back an amd64 base for an arm64 request and say nothing: the
	// jail then ran host binaries under a foreign-architecture emulator that
	// nothing needed, and /bin/sh inside an "arm64" jail was x86-64.
	archSuffixes := []string{"-" + runtime.GOARCH, "-amd64", "-arm64", "-i386", "-riscv64", ""}
	if requested := normalizeArch(arch); arch != "" && arch != "native" {
		archSuffixes = []string{"-" + requested}
		// A manifest may name the architecture twice: once in the image, as
		// "14.3-RELEASE-arm64", and once in the arch field beside it. Appending
		// the suffix again would look for 14.3-RELEASE-arm64-arm64.
		if strings.HasSuffix(image, "-"+requested) {
			archSuffixes = append(archSuffixes, "")
		}
	}
	prefixes := []string{"freebsd-", ""}

	findAndExtract := func() (string, bool) {
		for _, dir := range searchDirs {
			fullPath := filepath.Join(dir, image)
			if _, err := os.Stat(fullPath); err == nil {
				return fullPath, true
			}

			for _, prefix := range prefixes {
				for _, archSuffix := range archSuffixes {
					imageName := prefix + image + archSuffix
					for _, ext := range extensions {
						if strings.HasSuffix(imageName, ext) {
							imagePath := filepath.Join(dir, imageName)
							if _, err := os.Stat(imagePath); err == nil {
								return imagePath, true
							}
							continue
						}
						imagePath := filepath.Join(dir, imageName+ext)
						if _, err := os.Stat(imagePath); err == nil {
							return imagePath, true
						}
					}
				}
			}
		}
		return "", false
	}

	if imagePath, found := findAndExtract(); found {
		p.logInfo(ctx, "extracting base system from catalog image", "image", imagePath, "destination", destination)
		output, err := p.cmd().CombinedOutput(ctx, "tar", "-xf", imagePath, "-C", destination)
		if err != nil {
			return fmt.Errorf("failed to extract base system: %w (output: %s)", err, string(output))
		}
		return nil
	}

	if arch != "" && arch != "native" {
		named := image + "-" + normalizeArch(arch)
		return fmt.Errorf("image not found: %s for %s\n\nThe image must be downloaded first. To download it, run:\n  hospitus image fetch %s", image, normalizeArch(arch), named)
	}
	return fmt.Errorf("image not found: %s\n\nThe image must be downloaded first. To download it, run:\n  hospitus image fetch %s", image, image)
}
