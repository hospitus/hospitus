package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Compile-time assertion: PodmanProvider implements ExportImportProvider
var _ provider.ExportImportProvider = (*PodmanProvider)(nil)

// ExportInstance exports a container's filesystem to a tarball.
//
// Uses `podman export` which exports the container's filesystem (not volumes).
// The container must be stopped for a consistent export.
func (p *PodmanProvider) ExportInstance(ctx context.Context, handle provider.InstanceHandle, exportPath string, opts provider.ExportOptions) error {
	containerName := handle.ID

	// SECURITY: the daemon runs privileged, so an API client must not be able to
	// write the export tarball to an arbitrary location. Restrict exports to the
	// server-managed data directory.
	if err := p.validateExportPath(exportPath); err != nil {
		return err
	}

	if opts.StopInstance {
		state, err := p.GetInstanceState(ctx, handle)
		if err != nil {
			return fmt.Errorf("failed to get container state: %w", err)
		}
		if state == provider.StateRunning {
			if err := p.StopInstance(ctx, handle, provider.StopOptions{}); err != nil {
				return fmt.Errorf("failed to stop container before export: %w", err)
			}
		}
	}

	// Ensure the export directory exists
	if err := os.MkdirAll(filepath.Dir(exportPath), 0o755); err != nil {
		return fmt.Errorf("failed to create export directory: %w", err)
	}

	// Export the container filesystem
	if output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "export", "-o", exportPath, containerName); err != nil {
		return fmt.Errorf("failed to export container: %w (output: %s)", err, string(output))
	}

	// Save container metadata alongside the export for import
	metaPath := exportPath + ".meta.json"
	info, err := p.GetInstanceInfo(ctx, handle)
	if err == nil {
		meta := map[string]interface{}{
			"name":     containerName,
			"provider": "podman",
			"spec":     info.Spec,
		}
		if data, err := json.MarshalIndent(meta, "", "  "); err == nil {
			if werr := os.WriteFile(metaPath, data, 0o600); werr != nil {
				p.logger.Warn("failed to write export metadata", "path", metaPath, logging.FieldError, werr)
			}
		} else {
			p.logger.Warn("failed to marshal export metadata", "container", containerName, logging.FieldError, err)
		}
	} else {
		p.logger.Warn("failed to load container info for export metadata", "container", containerName, logging.FieldError, err)
	}

	return nil
}

// validateExportPath ensures the export target is an absolute, traversal-free
// path contained within the provider's data directory. This prevents an API
// client from writing an arbitrary file on the (privileged) daemon host.
func (p *PodmanProvider) validateExportPath(exportPath string) error {
	if exportPath == "" {
		return fmt.Errorf("export path is required")
	}
	if p.dataDir == "" {
		return fmt.Errorf("provider data directory is not configured")
	}
	if !filepath.IsAbs(exportPath) {
		return fmt.Errorf("export path must be absolute: %s", exportPath)
	}
	cleaned := filepath.Clean(exportPath)
	root := filepath.Clean(p.dataDir)
	if cleaned != root && !strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
		return fmt.Errorf("export path must be under the data directory %s: %s", root, exportPath)
	}
	return nil
}

// ImportInstance imports a container from an exported tarball.
//
// Uses `podman import` to create an image from the tarball, then creates a
// new container from that image.
func (p *PodmanProvider) ImportInstance(ctx context.Context, importPath string, opts provider.ImportOptions) (provider.InstanceHandle, error) {
	// Determine the container name
	name := opts.NewName
	if name == "" {
		// Try to read the name from the metadata file
		metaPath := importPath + ".meta.json"
		if data, err := os.ReadFile(metaPath); err == nil {
			var meta map[string]interface{}
			if err := json.Unmarshal(data, &meta); err == nil {
				if n, ok := meta["name"].(string); ok {
					name = n
				}
			}
		}
	}

	if name == "" {
		return provider.InstanceHandle{}, fmt.Errorf("no name specified and no metadata found; use NewName option")
	}

	if err := validation.ValidateInstanceName(name); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid instance name: %w", err)
	}

	// Import the tarball as a new image
	imageName := fmt.Sprintf("hospitus-import/%s:latest", name)
	if output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, "import", importPath, imageName); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("failed to import image: %w (output: %s)", err, string(output))
	}

	// Create a container from the imported image
	createArgs := []string{"create", "--name", name, imageName}

	output, err := p.cmd().CombinedOutput(ctx, p.podmanBin, createArgs...)
	if err != nil {
		// Clean up the imported image
		_ = p.cmd().Run(ctx, p.podmanBin, "rmi", imageName)
		return provider.InstanceHandle{}, fmt.Errorf("failed to create container from import: %w (output: %s)", err, string(output))
	}

	handle := provider.InstanceHandle{
		ID:       name,
		Provider: providerName,
		Metadata: map[string]interface{}{
			"imported_from": importPath,
			"image":         imageName,
		},
	}

	// Optionally start the container
	if opts.StartAfterImport {
		if err := p.StartInstance(ctx, handle); err != nil {
			return handle, fmt.Errorf("container imported but failed to start: %w", err)
		}
	}

	return handle, nil
}
