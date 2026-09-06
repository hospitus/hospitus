package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/internal/security"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// handleExport handles instance export operations.
//
// Endpoint:
//
//	POST /api/v1/instances/{id}/export - Export instance to tarball
//
// SECURITY: All operations require authentication and validate instance names.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request, instanceID string) {
	// Only POST method is allowed for export
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// SECURITY: Validate instance ID
	if err := validation.ValidateInstanceName(instanceID); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid instance ID", err)
		return
	}

	// Parse request body
	var req struct {
		ExportPath       string `json:"export_path"`
		Compress         bool   `json:"compress"`
		StopInstance     bool   `json:"stop_instance"`
		IncludeSnapshots bool   `json:"include_snapshots"`
	}

	if err := s.decodeJSONBody(r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// Validate export path
	if req.ExportPath == "" {
		s.writeError(w, http.StatusBadRequest, "Export path is required")
		return
	}

	// SECURITY: Validate export path to prevent path traversal and confine it
	// to the data directory so an export cannot overwrite arbitrary host files.
	if err := validateUserFilePath(req.ExportPath, s.fileConfinementDir()); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid export path", err)
		return
	}

	instance, err := s.datastore.GetInstance(r.Context(), instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Provider not found", err)
		return
	}

	// Check if provider supports export/import
	exportProvider, ok := prov.(provider.ExportImportProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support export/import", instance.Provider))
		return
	}

	// Build export options
	opts := provider.ExportOptions{
		Compress:         req.Compress,
		StopInstance:     req.StopInstance,
		IncludeSnapshots: req.IncludeSnapshots,
	}

	if err := exportProvider.ExportInstance(r.Context(), instance.Handle, req.ExportPath, opts); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to export instance", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(
		clientIP,
		"",
		"EXPORT",
		fmt.Sprintf("/api/v1/instances/%s/export", instanceID),
	)

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instance":    instanceID,
		"export_path": req.ExportPath,
		"compress":    req.Compress,
		"message":     fmt.Sprintf("Instance %s exported successfully to %s", instanceID, req.ExportPath),
	})
}

// handleImport handles instance import operations.
//
// Endpoint:
//
//	POST /api/v1/import - Import instance from tarball
//
// SECURITY: All operations require authentication.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	// Only POST method is allowed for import
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Parse request body
	var req struct {
		ImportPath       string `json:"import_path"`
		Provider         string `json:"provider"`
		NewName          string `json:"new_name"`
		ResetMAC         bool   `json:"reset_mac"`
		NewIP            string `json:"new_ip"`
		StartAfterImport bool   `json:"start_after_import"`
	}

	if err := s.decodeJSONBody(r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// Validate import path
	if req.ImportPath == "" {
		s.writeError(w, http.StatusBadRequest, "Import path is required")
		return
	}

	// SECURITY: Validate import path to prevent path traversal and confine it
	// to the data directory so an import cannot read arbitrary host files.
	if err := validateUserFilePath(req.ImportPath, s.fileConfinementDir()); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid import path", err)
		return
	}

	// Validate provider
	if req.Provider == "" {
		s.writeError(w, http.StatusBadRequest, "Provider is required")
		return
	}

	// SECURITY: Validate new name if provided
	if req.NewName != "" {
		if err := validation.ValidateInstanceName(req.NewName); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid new name", err)
			return
		}
	}

	prov, err := s.registry.Get(req.Provider)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Provider not found", err)
		return
	}

	// Check if provider supports export/import
	importProvider, ok := prov.(provider.ExportImportProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support export/import", req.Provider))
		return
	}

	// Build import options
	opts := provider.ImportOptions{
		NewName:          req.NewName,
		ResetMAC:         req.ResetMAC,
		NewIP:            req.NewIP,
		StartAfterImport: req.StartAfterImport,
	}

	handle, err := importProvider.ImportInstance(r.Context(), req.ImportPath, opts)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to import instance", err)
		return
	}

	// Get instance name from handle
	instanceName := handle.ID

	// Store instance in datastore
	// Get instance info from provider
	info, err := prov.GetInstanceInfo(r.Context(), handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to get instance info after import", err)
		return
	}

	instance := &datastore.Instance{
		ID:          handle.ID,
		Name:        instanceName,
		Provider:    req.Provider,
		State:       info.State,
		Spec:        info.Spec,
		Handle:      handle,
		Labels:      info.Spec.Labels,
		Annotations: info.Spec.Annotations,
	}

	if err := s.datastore.CreateInstance(r.Context(), instance); err != nil {
		err = s.discardCreatedInstance(r.Context(), prov, handle, err)
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to store imported instance", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(
		clientIP,
		"",
		"IMPORT",
		"/api/v1/import",
	)

	s.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"instance":    instance,
		"import_path": req.ImportPath,
		"message":     fmt.Sprintf("Instance %s imported successfully from %s", instanceName, filepath.Base(req.ImportPath)),
	})
}

// validateUserFilePath validates a user-supplied file path for import/export operations.
// It prevents path traversal, ensures the path is absolute, blocks control characters,
// and confines the path to baseDir so a caller cannot read from or write to arbitrary
// locations on the host as root (e.g. overwriting /etc/master.passwd via a tar export).
func validateUserFilePath(p, baseDir string) error {
	if err := validation.ValidateFilePath(p, true); err != nil {
		return err
	}
	// Reject paths that, even after cleaning, still contain traversal markers
	cleaned := filepath.Clean(p)
	if strings.Contains(cleaned, "..") {
		return fmt.Errorf("path traversal detected in: %s", p)
	}
	// Confine the path to baseDir, resolving symlinks on both sides. Comparing
	// filepath.Abs strings instead would confine the name and not the file: a
	// symlink planted inside baseDir points wherever it likes, and the daemon
	// reads and writes as root.
	//
	// This closes the escape, not the race. A symlink swapped between this check
	// and the open still wins; that needs the file to be opened relative to a
	// trusted directory descriptor with no-follow semantics.
	return validation.EnsurePathWithin(cleaned, baseDir)
}

// fileConfinementDir returns the directory that export/import paths must stay
// within. It falls back to the standard data directory when no DataDir is
// configured (e.g. in tests using a nil ServerConfig).
func (s *Server) fileConfinementDir() string {
	if s.config == nil || s.config.DataDir == "" {
		return "/var/lib/hospitus"
	}
	return s.config.DataDir
}
