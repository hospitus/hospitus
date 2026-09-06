package bhyve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hospitus/hospitus/pkg/validation"
)

// resolveCloudImagePath resolves a cloud image reference to a file path.
// It looks in the configured image directory for the image.
func (p *BhyveProvider) resolveCloudImagePath(imageRef, arch string) (string, error) {
	// Check if it's already an absolute path
	if filepath.IsAbs(imageRef) {
		if _, err := os.Stat(imageRef); err == nil {
			return p.confinedImage(imageRef)
		}
		return "", fmt.Errorf("image file not found: %s", imageRef)
	}

	// Cloud image directory
	cloudImageDir := filepath.Join(p.imageDir, "cloud")

	// Try different naming conventions
	candidates := []string{
		// <image>-<arch>.img (e.g., ubuntu-24.04-amd64.img)
		filepath.Join(cloudImageDir, fmt.Sprintf("%s-%s.img", imageRef, arch)),
		filepath.Join(cloudImageDir, fmt.Sprintf("%s-%s.qcow2", imageRef, arch)),
		filepath.Join(cloudImageDir, fmt.Sprintf("%s.img", imageRef)),
		filepath.Join(cloudImageDir, fmt.Sprintf("%s.qcow2", imageRef)),
		// Without cloud prefix
		filepath.Join(p.imageDir, fmt.Sprintf("%s.img", imageRef)),
		filepath.Join(p.imageDir, fmt.Sprintf("%s.qcow2", imageRef)),
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return p.confinedImage(candidate)
		}
	}

	// List available images for helpful error message
	var available []string
	if entries, err := os.ReadDir(cloudImageDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				available = append(available, e.Name())
			}
		}
	}

	if len(available) > 0 {
		return "", fmt.Errorf("image '%s' not found. Available images in %s: %s. Use 'hospitus image fetch' to download",
			imageRef, cloudImageDir, strings.Join(available, ", "))
	}
	return "", fmt.Errorf("image '%s' not found and no images in %s. Use 'hospitus image fetch' to download cloud images",
		imageRef, cloudImageDir)
}

// confinedImage returns candidate when it exists and lies inside the image
// directory, and refuses it otherwise.
//
// Both resolvers feed CreateInstance, which turns the result into a VM disk or
// a cloud image to deploy. An absolute reference was returned on sight, and a
// relative one containing ".." escaped the join — either way the guest got a
// host file.
func (p *BhyveProvider) confinedImage(candidate string) (string, error) {
	within, err := validation.PathWithinAny(candidate, p.imageDir)
	if err != nil {
		return "", fmt.Errorf("resolving image path %s: %w", candidate, err)
	}
	if !within {
		return "", fmt.Errorf("image %s is outside the image directory %s", candidate, p.imageDir)
	}
	return candidate, nil
}

// resolveISOPath resolves an ISO image reference to a file path.
func (p *BhyveProvider) resolveISOPath(isoRef string) (string, error) {
	// Check if it's already an absolute path
	if filepath.IsAbs(isoRef) {
		if _, err := os.Stat(isoRef); err == nil {
			return p.confinedImage(isoRef)
		}
		return "", fmt.Errorf("ISO file not found: %s", isoRef)
	}

	// ISO image directory
	isoDir := filepath.Join(p.imageDir, "iso")

	candidates := []string{
		filepath.Join(isoDir, isoRef),
		filepath.Join(isoDir, isoRef+".iso"),
		filepath.Join(p.imageDir, isoRef),
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return p.confinedImage(candidate)
		}
	}

	return "", fmt.Errorf("ISO '%s' not found in %s", isoRef, isoDir)
}

// parseImageSource parses an image source string and returns type and reference.
func (p *BhyveProvider) parseImageSource(source string) (imageType, reference string, err error) {
	if source == "" {
		return "", "", fmt.Errorf("empty image source")
	}

	parts := strings.SplitN(source, ":", 2)
	if len(parts) < 2 {
		// Default to cloud if no type prefix
		return "cloud", source, nil
	}

	return strings.ToLower(parts[0]), parts[1], nil
}
