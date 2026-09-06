// Package image provides image management for Hospitus
// It handles different image categories: sets (txz), iso, and cloud images
package image

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
)

//go:embed catalog.json
var defaultCatalogJSON []byte

// catalogHTTPClient is used for all catalog network access. Unlike
// http.DefaultClient it enforces a timeout so a hung server cannot block the
// daemon indefinitely.
var catalogHTTPClient = &http.Client{Timeout: 30 * time.Second}

// maxCatalogBytes caps the size of a remote catalog response so a hostile or
// broken server cannot exhaust memory.
const maxCatalogBytes = 10 << 20 // 10 MiB

// ImageCategory represents the type of image
type ImageCategory string

const (
	// CategorySet represents FreeBSD/Linux base system archives (txz)
	CategorySet ImageCategory = "set"
	// CategoryISO represents ISO images for VM installation
	CategoryISO ImageCategory = "iso"
	// CategoryCloud represents cloud images (raw, qcow2, img, vmdk)
	CategoryCloud ImageCategory = "cloud"
)

// ImageFormat represents the image file format
type ImageFormat string

const (
	FormatTXZ   ImageFormat = "txz"
	FormatTarXZ ImageFormat = "tar.xz"
	FormatTarGZ ImageFormat = "tar.gz"
	FormatISO   ImageFormat = "iso"
	FormatRaw   ImageFormat = "raw"
	FormatQCOW2 ImageFormat = "qcow2"
	FormatVMDK  ImageFormat = "vmdk"
	FormatIMG   ImageFormat = "img"
)

// ProviderType indicates which provider can use this image
type ProviderType string

const (
	ProviderJail  ProviderType = "jail"
	ProviderBhyve ProviderType = "bhyve"
	ProviderQemu  ProviderType = "qemu"
)

// ImageProfile represents metadata about an available image
type ImageProfile struct {
	// Name is the unique identifier for the image (e.g., "freebsd-14.3-RELEASE-amd64")
	Name string `json:"name"`

	// Display is a human-readable name
	Display string `json:"display"`

	// Description provides details about the image
	Description string `json:"description,omitempty"`

	// Category is set, iso, or cloud
	Category ImageCategory `json:"category"`

	// Format is the file format (txz, iso, qcow2, raw, etc.)
	Format ImageFormat `json:"format"`

	// OSType is freebsd, linux, windows, etc.
	OSType string `json:"os_type"`

	// OSVersion is the OS version (e.g., "14.3-RELEASE", "22.04")
	OSVersion string `json:"os_version"`

	// Arch is the architecture (amd64, arm64, i386, riscv64)
	Arch string `json:"arch"`

	// Providers lists which providers can use this image
	Providers []ProviderType `json:"providers"`

	// URL is the download URL
	URL string `json:"url"`

	// Size is the file size in bytes (0 if unknown)
	Size int64 `json:"size,omitempty"`

	// SHA256 is the checksum for verification
	SHA256 string `json:"sha256,omitempty"`

	// Mirrors is a list of alternative download URLs
	Mirrors []string `json:"mirrors,omitempty"`

	// CloudInit indicates if the image supports cloud-init
	CloudInit bool `json:"cloud_init,omitempty"`

	// MinDiskGB is the minimum disk size required
	MinDiskGB int `json:"min_disk_gb,omitempty"`

	// DefaultUser is the default login username for cloud images
	DefaultUser string `json:"default_user,omitempty"`
}

// Catalog manages the available and downloaded images
type Catalog struct {
	mu sync.RWMutex

	// baseDir is the directory where images are stored
	baseDir string

	// Available images from the built-in catalog
	builtin []ImageProfile

	// Custom profiles loaded from config
	custom []ImageProfile

	logger *slog.Logger
}

// NewCatalog creates a new image catalog
func NewCatalog(baseDir string) *Catalog {
	c := &Catalog{
		baseDir: baseDir,
		logger:  logging.WithComponent("image"),
	}

	// Load the trusted embedded profiles first.
	if err := json.Unmarshal(defaultCatalogJSON, &c.builtin); err != nil {
		// Should never happen with valid embedded JSON
		c.logger.Error("Failed to unmarshal built-in catalog", logging.FieldError, err)
		c.builtin = []ImageProfile{}
	}

	// Merge any cached catalog on top of (not replacing) the embedded one. A
	// stale or tampered cache must not be able to silently drop trusted
	// embedded entries.
	cachePath := filepath.Join(filepath.Dir(baseDir), "config", "catalog.json")
	if data, err := os.ReadFile(cachePath); err == nil {
		var cached []ImageProfile
		if err := json.Unmarshal(data, &cached); err == nil {
			c.builtin = mergeProfiles(c.builtin, cached)
			c.logger.Info("Merged image catalog from cache", "path", cachePath)
		}
	}
	return c
}

// mergeProfiles overlays src onto base by name: entries with a matching Name
// are replaced, new ones are appended, and base-only entries are preserved.
func mergeProfiles(base, src []ImageProfile) []ImageProfile {
	for i := range src {
		sp := src[i]
		found := false
		for j := range base {
			if base[j].Name == sp.Name {
				base[j] = sp
				found = true
				break
			}
		}
		if !found {
			base = append(base, sp)
		}
	}
	return base
}

// Refresh updates the catalog from remote sources
func (c *Catalog) Refresh(ctx context.Context) error {
	c.logger.Info("Refreshing image catalog from remote sources")

	// 1. Fetch updated catalog from project repository (GitHub)
	// Using a raw URL from the main branch
	projectURL := "https://raw.githubusercontent.com/hospitus/hospitus/main/pkg/image/catalog.json"
	if err := c.fetchRemoteCatalog(ctx, projectURL); err != nil {
		c.logger.Warn("Failed to fetch project catalog, will try scanning sources", logging.FieldError, err)
	}

	// 2. Probe for releases the catalog does not list yet
	if err := c.probeKnownFreeBSDReleases(ctx); err != nil {
		c.logger.Error("Failed to probe FreeBSD releases", logging.FieldError, err)
	}

	// 3. Save updated catalog to cache
	cachePath := filepath.Join(filepath.Dir(c.baseDir), "config", "catalog.json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
		if err := c.SaveBuiltinCatalog(cachePath); err != nil {
			c.logger.Warn("Failed to save catalog to cache", logging.FieldError, err)
		}
	}

	return nil
}

// fetchRemoteCatalog downloads a catalog.json from a URL and merges it
func (c *Catalog) fetchRemoteCatalog(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}

	resp, err := catalogHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch remote catalog: status %d", resp.StatusCode)
	}

	var remoteProfiles []ImageProfile
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCatalogBytes)).Decode(&remoteProfiles); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Merge remote profiles into builtin (replacing existing ones with same name)
	c.builtin = mergeProfiles(c.builtin, remoteProfiles)

	return nil
}

// freeBSDReleaseCandidates are the releases probeKnownFreeBSDReleases looks for.
//
// This is a list, not a scan: nothing here reads the project's release index.
// A new release has to be added by hand, alongside the entries in
// catalog.json.
var freeBSDReleaseCandidates = []string{"14.3-RELEASE", "15.0-RELEASE"}

// probeKnownFreeBSDReleases asks the download servers which of the candidates
// above are published, and adds the ones that are
func (c *Catalog) probeKnownFreeBSDReleases(ctx context.Context) error {
	c.logger.Info("Probing for published FreeBSD releases", "candidates", freeBSDReleaseCandidates)

	architectures := []string{"amd64", "arm64", "riscv64", "i386"}
	potentialVersions := freeBSDReleaseCandidates

	for _, ver := range potentialVersions {
		for _, arch := range architectures {
			// Check if we already have this version as RELEASE
			name := fmt.Sprintf("freebsd-%s-%s", ver, arch)
			exists := false
			c.mu.RLock()
			for i := range c.builtin {
				p := c.builtin[i]
				if p.Name == name && !strings.Contains(p.OSVersion, "CURRENT") {
					exists = true
					break
				}
			}
			c.mu.RUnlock()

			if exists {
				continue
			}

			// Probe FreeBSD server. The download layout uses MACHINE/MACHINE_ARCH
			// path components, which differ from the short arch name for arm64
			// (arm64/aarch64) and riscv64 (riscv/riscv64).
			url := freeBSDBaseURL(arch, ver)

			if c.probeURL(ctx, url) {
				c.logger.Info("Found new FreeBSD release!", "version", ver, "arch", arch)
				c.AddBuiltinProfile(ImageProfile{
					Name:      name,
					Display:   fmt.Sprintf("FreeBSD %s (%s)", ver, arch),
					Category:  CategorySet,
					Format:    FormatTXZ,
					OSType:    "freebsd",
					OSVersion: ver,
					Arch:      arch,
					Providers: []ProviderType{ProviderJail},
					URL:       url,
				})
			}
		}
	}

	return nil
}

// freeBSDBaseURL returns the download URL for a FreeBSD base.txz set for the
// given short arch name and release version. It maps the short arch to the
// MACHINE/MACHINE_ARCH path layout used by the FreeBSD mirrors.
func freeBSDBaseURL(arch, version string) string {
	const base = "https://download.freebsd.org/releases"
	switch arch {
	case "arm64", "aarch64":
		return fmt.Sprintf("%s/arm64/aarch64/%s/base.txz", base, version)
	case "riscv64", "riscv":
		return fmt.Sprintf("%s/riscv/riscv64/%s/base.txz", base, version)
	default:
		return fmt.Sprintf("%s/%s/%s/base.txz", base, arch, version)
	}
}

func (c *Catalog) probeURL(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, http.NoBody)
	if err != nil {
		return false
	}

	resp, err := catalogHTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// AddBuiltinProfile adds or updates a built-in profile
func (c *Catalog) AddBuiltinProfile(profile ImageProfile) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.builtin {
		if c.builtin[i].Name == profile.Name {
			c.builtin[i] = profile
			return
		}
	}
	c.builtin = append(c.builtin, profile)
}

// SaveBuiltinCatalog saves the builtin profiles to a JSON file
func (c *Catalog) SaveBuiltinCatalog(path string) error {
	c.mu.RLock()
	profiles := append([]ImageProfile(nil), c.builtin...)
	c.mu.RUnlock()

	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o600)
}

// NewCatalogWithFile creates a catalog loading profiles from an external file
// Falls back to embedded default if the file doesn't exist
func NewCatalogWithFile(baseDir, catalogPath string) *Catalog {
	c := &Catalog{
		baseDir: baseDir,
		logger:  logging.WithComponent("image"),
	}

	// Try to load from external file first
	if data, err := os.ReadFile(catalogPath); err == nil {
		if err := json.Unmarshal(data, &c.builtin); err == nil {
			return c
		}
		c.logger.Warn("Failed to unmarshal external catalog, falling back to embedded", "path", catalogPath, logging.FieldError, err)
	}

	// Fall back to embedded default
	if err := json.Unmarshal(defaultCatalogJSON, &c.builtin); err != nil {
		c.logger.Error("Failed to unmarshal built-in catalog", logging.FieldError, err)
		c.builtin = []ImageProfile{}
	}
	return c
}

// BaseDir returns the base directory for images
func (c *Catalog) BaseDir() string {
	return c.baseDir
}

// GetSubDir returns the subdirectory for a category
func (c *Catalog) GetSubDir(category ImageCategory) string {
	switch category {
	case CategorySet:
		return filepath.Join(c.baseDir, "sets")
	case CategoryISO:
		return filepath.Join(c.baseDir, "iso")
	case CategoryCloud:
		return filepath.Join(c.baseDir, "cloud")
	default:
		return c.baseDir
	}
}

// Available returns all available images (built-in + custom)
func (c *Catalog) Available() []ImageProfile {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]ImageProfile, 0, len(c.builtin)+len(c.custom))
	for i := range c.builtin {
		result = append(result, cloneProfile(c.builtin[i]))
	}
	for i := range c.custom {
		result = append(result, cloneProfile(c.custom[i]))
	}
	return result
}

// AvailableByCategory returns available images filtered by category
func (c *Catalog) AvailableByCategory(category ImageCategory) []ImageProfile {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var result []ImageProfile
	for i := range c.builtin {
		p := c.builtin[i]
		if p.Category == category {
			result = append(result, p)
		}
	}
	for i := range c.custom {
		p := c.custom[i]
		if p.Category == category {
			result = append(result, p)
		}
	}
	return result
}

// AvailableByProvider returns images usable by a specific provider
func (c *Catalog) AvailableByProvider(provider ProviderType) []ImageProfile {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var result []ImageProfile
	for i := range c.builtin {
		p := c.builtin[i]
		for _, prov := range p.Providers {
			if prov == provider {
				result = append(result, p)
				break
			}
		}
	}
	for i := range c.custom {
		p := c.custom[i]
		for _, prov := range p.Providers {
			if prov == provider {
				result = append(result, p)
				break
			}
		}
	}
	return result
}

// FindProfile finds a profile by name
func (c *Catalog) FindProfile(name string) *ImageProfile {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Return a copy: a pointer into the internal slice would let callers read
	// (and the catalog mutate) the same element after the RLock is released.
	for i := range c.builtin {
		if c.builtin[i].Name == name {
			p := c.builtin[i]
			return &p
		}
	}
	for i := range c.custom {
		if c.custom[i].Name == name {
			p := c.custom[i]
			return &p
		}
	}
	return nil
}

// DownloadedImage describes a locally downloaded image file.
type DownloadedImage struct {
	Profile  *ImageProfile `json:"profile,omitempty"`
	Filename string        `json:"filename"`
	Path     string        `json:"path"`
	Size     int64         `json:"size"`
	Category ImageCategory `json:"category"`
}

// ListDownloaded returns all downloaded images
func (c *Catalog) ListDownloaded() ([]DownloadedImage, error) {
	var result []DownloadedImage

	// Scan each category directory
	for _, category := range []ImageCategory{CategorySet, CategoryISO, CategoryCloud} {
		dir := c.GetSubDir(category)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("failed to read directory %s: %w", dir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			// Skip partial downloads
			if strings.HasSuffix(entry.Name(), ".partial") {
				continue
			}

			info, err := entry.Info()
			if err != nil {
				continue
			}

			img := DownloadedImage{
				Filename: entry.Name(),
				Path:     filepath.Join(dir, entry.Name()),
				Size:     info.Size(),
				Category: category,
			}

			// Try to match with a profile
			img.Profile = c.findProfileByFilename(entry.Name())

			result = append(result, img)
		}
	}

	// Also check legacy location (flat /var/lib/hospitus/images)
	if entries, err := os.ReadDir(c.baseDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			// Skip partial downloads
			if strings.HasSuffix(entry.Name(), ".partial") {
				continue
			}

			// Only include known extensions
			category := getCategoryByExtension(strings.ToLower(entry.Name()))
			if category == "" {
				continue
			}

			info, err := entry.Info()
			if err != nil {
				continue
			}

			img := DownloadedImage{
				Filename: entry.Name(),
				Path:     filepath.Join(c.baseDir, entry.Name()),
				Size:     info.Size(),
				Category: category,
			}

			// Try to match with a profile
			img.Profile = c.findProfileByFilename(entry.Name())

			result = append(result, img)
		}
	}

	return result, nil
}

// ResolveProfile finds a profile by the name a user is likely to type.
//
// The catalog spells FreeBSD base sets "freebsd-14.3-RELEASE-amd64", while the
// guides teach the short "14.3-RELEASE-amd64" and `jail create` accepts it, so
// both have to reach the same entry. Treating them as two images fetches the
// same release twice, under two names, into two directories.
func (c *Catalog) ResolveProfile(name string) *ImageProfile {
	if p := c.FindProfile(name); p != nil {
		return p
	}
	return c.FindProfile("freebsd-" + name)
}

// findProfileByFilename tries to match a filename to a profile
func (c *Catalog) findProfileByFilename(filename string) *ImageProfile {
	// Remove extension and try to match
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	return c.ResolveProfile(base)
}

// getCategoryByExtension returns the category based on a (lower-cased) file
// name. It matches on the full name so multi-part suffixes like ".tar.xz" and
// ".tar.gz" are recognized (filepath.Ext would only yield ".xz"/".gz").
func getCategoryByExtension(filename string) ImageCategory {
	switch {
	case strings.HasSuffix(filename, ".txz"),
		strings.HasSuffix(filename, ".tar.xz"),
		strings.HasSuffix(filename, ".tar.gz"),
		strings.HasSuffix(filename, ".tgz"):
		return CategorySet
	case strings.HasSuffix(filename, ".iso"):
		return CategoryISO
	case strings.HasSuffix(filename, ".raw"),
		strings.HasSuffix(filename, ".qcow2"),
		strings.HasSuffix(filename, ".vmdk"),
		strings.HasSuffix(filename, ".img"):
		return CategoryCloud
	}
	return ""
}

// LoadCustomProfiles loads custom image profiles from a JSON file
func (c *Catalog) LoadCustomProfiles(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read custom profiles: %w", err)
	}

	var profiles []ImageProfile
	if err := json.Unmarshal(data, &profiles); err != nil {
		return fmt.Errorf("failed to parse custom profiles: %w", err)
	}

	c.mu.Lock()
	c.custom = profiles
	c.mu.Unlock()

	return nil
}

// SaveCustomProfiles saves custom profiles to a JSON file
func (c *Catalog) SaveCustomProfiles(path string) error {
	c.mu.RLock()
	profiles := append([]ImageProfile(nil), c.custom...)
	c.mu.RUnlock()

	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal custom profiles: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("failed to write custom profiles: %w", err)
	}

	return nil
}

// AddCustomProfile adds a custom image profile
func (c *Catalog) AddCustomProfile(profile ImageProfile) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Remove if exists
	for i := range c.custom {
		if c.custom[i].Name == profile.Name {
			c.custom = append(c.custom[:i], c.custom[i+1:]...)
			break
		}
	}

	c.custom = append(c.custom, profile)
}

// cloneProfile copies a profile and the slices inside it.
//
// Assigning the struct shares Providers and Mirrors with the catalog's own
// storage, so a caller sorting or rewriting either one changes what every later
// caller is handed.
func cloneProfile(p ImageProfile) ImageProfile {
	if p.Providers != nil {
		p.Providers = append([]ProviderType(nil), p.Providers...)
	}
	if p.Mirrors != nil {
		p.Mirrors = append([]string(nil), p.Mirrors...)
	}
	return p
}
