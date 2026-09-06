package api

import (
	"fmt"
	"net/http"

	"github.com/hospitus/hospitus/internal/security"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// handleSnapshots handles snapshot operations on an instance.
//
// Endpoints:
//
//	GET    /api/v1/instances/{id}/snapshots       - List all snapshots for an instance
//	POST   /api/v1/instances/{id}/snapshots       - Create a new snapshot
//	DELETE /api/v1/instances/{id}/snapshots/{name} - Delete a snapshot
//	POST   /api/v1/instances/{id}/snapshots/{name}/restore - Restore to a snapshot
//
// SECURITY: All operations require authentication and validate snapshot names.
// parts is the path suffix after /instances/{id}/, split on "/":
// ["snapshots"], ["snapshots", "{name}"] or ["snapshots", "{name}", "restore"].
func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request, instanceID string, parts []string) {
	// SECURITY: Validate instance ID (already validated by caller, but double-check)
	if err := validation.ValidateInstanceName(instanceID); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid instance ID", err)
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

	// Check if provider supports snapshots
	snapProvider, ok := prov.(provider.SnapshotProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support snapshots", instance.Provider))
		return
	}

	// Handle different HTTP methods and paths
	// parts: ["snapshots"] for /api/v1/instances/{id}/snapshots
	// parts: ["snapshots", "{name}"] for /api/v1/instances/{id}/snapshots/{name}
	// parts: ["snapshots", "{name}", "restore"] for /api/v1/instances/{id}/snapshots/{name}/restore
	// parts: ["snapshots", "{name}", "clone"] for /api/v1/instances/{id}/snapshots/{name}/clone
	switch {
	case len(parts) == 1:
		// /api/v1/instances/{id}/snapshots
		switch r.Method {
		case http.MethodGet:
			s.handleListSnapshots(w, r, instanceID, snapProvider)
		case http.MethodPost:
			s.handleCreateSnapshot(w, r, instanceID, snapProvider)
		default:
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	case len(parts) == 2:
		// /api/v1/instances/{id}/snapshots/{name}
		snapshotName := parts[1]

		if r.Method == http.MethodDelete {
			s.handleDeleteSnapshot(w, r, instanceID, snapshotName, snapProvider)
		} else {
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
	case len(parts) == 3 && parts[2] == "restore":
		// Restore endpoint
		if r.Method != http.MethodPost {
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		s.handleRestoreSnapshot(w, r, instanceID, parts[1], snapProvider)
	case len(parts) == 3 && parts[2] == "clone":
		// Clone from snapshot endpoint - route to clone handler
		s.handleClone(w, r, instanceID, parts)
	default:
		s.writeError(w, http.StatusNotFound, "Not found")
	}
}

// handleListSnapshots lists all snapshots for an instance.
func (s *Server) handleListSnapshots(w http.ResponseWriter, r *http.Request, instanceID string, snapProvider provider.SnapshotProvider) {
	// Get instance handle from datastore (contains ZFS dataset metadata)
	instance, err := s.datastore.GetInstance(r.Context(), instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	// List snapshots using the instance's handle (with metadata)
	snapshots, err := snapProvider.ListSnapshots(r.Context(), instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list snapshots", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(clientIP, "", "LIST", fmt.Sprintf("/api/v1/instances/%s/snapshots", instanceID))

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instance":  instanceID,
		"snapshots": snapshots,
		"count":     len(snapshots),
	})
}

// handleCreateSnapshot creates a new snapshot.
func (s *Server) handleCreateSnapshot(w http.ResponseWriter, r *http.Request, instanceID string, snapProvider provider.SnapshotProvider) {
	// Parse request body
	var req struct {
		Name string `json:"name"`
	}

	if err := s.decodeJSONBody(r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// SECURITY: Validate snapshot name
	if err := validation.ValidateSnapshotName(req.Name); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid snapshot name", err)
		return
	}

	// Get instance handle from datastore (contains ZFS dataset metadata)
	instance, err := s.datastore.GetInstance(r.Context(), instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	// Create snapshot using the instance's handle (with metadata)
	snapshot, err := snapProvider.CreateSnapshot(r.Context(), instance.Handle, req.Name)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to create snapshot", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(
		clientIP,
		"",
		"CREATE",
		fmt.Sprintf("/api/v1/instances/%s/snapshots/%s", instanceID, req.Name),
	)

	s.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"instance": instanceID,
		"snapshot": snapshot,
		"message":  fmt.Sprintf("Snapshot %s created successfully", req.Name),
	})
}

// handleDeleteSnapshot deletes a snapshot.
func (s *Server) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request, instanceID, snapshotName string, snapProvider provider.SnapshotProvider) {
	// SECURITY: Validate snapshot name
	if err := validation.ValidateSnapshotName(snapshotName); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid snapshot name", err)
		return
	}

	// Get instance handle from datastore (contains ZFS dataset metadata)
	instance, err := s.datastore.GetInstance(r.Context(), instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	// List snapshots to find the one to delete
	snapshots, err := snapProvider.ListSnapshots(r.Context(), instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list snapshots", err)
		return
	}

	// Find snapshot by name
	var targetSnapshot *provider.SnapshotInfo
	for i := range snapshots {
		if snapshots[i].Name == snapshotName {
			targetSnapshot = &snapshots[i]
			break
		}
	}

	if targetSnapshot == nil {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("Snapshot %s not found", snapshotName))
		return
	}

	if err := snapProvider.DeleteSnapshot(r.Context(), targetSnapshot.Handle); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to delete snapshot", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(
		clientIP,
		"",
		"DELETE",
		fmt.Sprintf("/api/v1/instances/%s/snapshots/%s", instanceID, snapshotName),
	)

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instance": instanceID,
		"snapshot": snapshotName,
		"message":  fmt.Sprintf("Snapshot %s deleted successfully", snapshotName),
	})
}

// handleRestoreSnapshot restores an instance to a snapshot.
func (s *Server) handleRestoreSnapshot(w http.ResponseWriter, r *http.Request, instanceID, snapshotName string, snapProvider provider.SnapshotProvider) {
	// SECURITY: Validate snapshot name
	if err := validation.ValidateSnapshotName(snapshotName); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid snapshot name", err)
		return
	}

	// Get instance handle from datastore (contains ZFS dataset metadata)
	instance, err := s.datastore.GetInstance(r.Context(), instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	// List snapshots to find the one to restore
	snapshots, err := snapProvider.ListSnapshots(r.Context(), instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list snapshots", err)
		return
	}

	// Find snapshot by name
	var targetSnapshot *provider.SnapshotInfo
	for i := range snapshots {
		if snapshots[i].Name == snapshotName {
			targetSnapshot = &snapshots[i]
			break
		}
	}

	if targetSnapshot == nil {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("Snapshot %s not found", snapshotName))
		return
	}

	// Restore snapshot using the instance's handle (with metadata)
	if err := snapProvider.RestoreSnapshot(r.Context(), instance.Handle, targetSnapshot.Handle); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to restore snapshot", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(
		clientIP,
		"",
		"RESTORE",
		fmt.Sprintf("/api/v1/instances/%s/snapshots/%s", instanceID, snapshotName),
	)

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instance": instanceID,
		"snapshot": snapshotName,
		"message":  fmt.Sprintf("Instance %s restored to snapshot %s successfully", instanceID, snapshotName),
	})
}
