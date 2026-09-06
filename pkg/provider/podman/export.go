package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
	if err := validation.ValidateInstanceName(handle.ID); err != nil {
		return fmt.Errorf("invalid instance id %q: %w", handle.ID, err)
	}
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
			// The effective values, not the ones a clone strips: "podman
			// import" builds an image with no Config at all, so an imported
			// container has no image defaults to inherit and would come back
			// without the environment and command the original ran with.
			"spec": info.Spec,
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
	// Clean is lexical only: a symlink under dataDir redirects the privileged
	// write that follows. The destination does not exist yet, so its parent is
	// what gets resolved — and the final component is refused outright when it
	// is itself a link.
	cleaned := filepath.Clean(exportPath)
	within, err := validation.PathWithinAny(filepath.Dir(cleaned), p.dataDir)
	if err != nil {
		return fmt.Errorf("resolving export path: %w", err)
	}
	if !within {
		return fmt.Errorf("export path must be under the data directory %s: %s", p.dataDir, exportPath)
	}
	if info, lerr := os.Lstat(cleaned); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("export path %s is a symbolic link", cleaned)
	}
	return nil
}

// ImportInstance imports a container from an exported tarball.
//
// Uses `podman import` to create an image from the tarball, then creates a
// new container from that image.
func (p *PodmanProvider) ImportInstance(ctx context.Context, importPath string, opts provider.ImportOptions) (provider.InstanceHandle, error) {
	// Determine the container name
	// The export writes the container's whole spec beside the tarball; reading
	// only the name back created a bare container from the image's defaults and
	// silently dropped the labels and limits it recorded — which
	// StartAfterImport then started.
	// The same containment as the export: importPath and its ".meta.json"
	// sibling are read here and handed to podman, so a caller could otherwise
	// name any file on the host.
	if err := p.validateExportPath(importPath); err != nil {
		return provider.InstanceHandle{}, fmt.Errorf("invalid import path: %w", err)
	}

	var meta struct {
		Name string                `json:"name"`
		Spec provider.InstanceSpec `json:"spec"`
	}
	if data, err := os.ReadFile(importPath + ".meta.json"); err == nil {
		if err := json.Unmarshal(data, &meta); err != nil {
			p.logger.Warn("export metadata could not be read; importing with defaults",
				"path", importPath+".meta.json", logging.FieldError, err)
		}
	}

	name := opts.NewName
	if name == "" {
		name = meta.Name
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

	// Created through CreateInstance rather than through a create command built
	// here: it is what turns a spec into podman arguments — networks, volumes,
	// ports, environment, command — and a second copy of that would drift.
	// pullImageIfNeeded returns immediately for an image that is already local,
	// which the one just imported is.
	spec := meta.Spec
	spec.Name = name
	spec.Image = imageName

	handle, err := p.CreateInstance(ctx, spec)
	if err != nil {
		// Clean up the imported image
		_ = p.cmd().Run(ctx, p.podmanBin, "rmi", imageName)
		return provider.InstanceHandle{}, fmt.Errorf("failed to create container from import: %w", err)
	}

	if handle.Metadata == nil {
		handle.Metadata = map[string]interface{}{}
	}
	handle.Metadata["imported_from"] = importPath
	handle.Metadata["image"] = imageName

	// Optionally start the container
	if opts.StartAfterImport {
		if err := p.StartInstance(ctx, handle); err != nil {
			return handle, fmt.Errorf("container imported but failed to start: %w", err)
		}
	}

	return handle, nil
}
