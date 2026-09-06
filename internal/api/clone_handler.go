package api

import (
	"fmt"
	"net/http"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/internal/security"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// handleClone handles cloning operations.
//
// Endpoints:
//
//	POST /api/v1/instances/{id}/clone                        - Clone from instance
//	POST /api/v1/instances/{id}/snapshots/{name}/clone       - Clone from snapshot
//
// SECURITY: All operations require authentication and validate names.
// parts is the path suffix after /instances/{id}/, split on "/":
// ["clone"] or ["snapshots", "{name}", "clone"].
func (s *Server) handleClone(w http.ResponseWriter, r *http.Request, instanceID string, parts []string) {
	// Only POST method is allowed for cloning
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// SECURITY: Validate instance ID
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

	// Check if provider supports cloning
	cloneProvider, ok := prov.(provider.CloneProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support cloning", instance.Provider))
		return
	}

	// Route based on path
	// parts: ["clone"] for /api/v1/instances/{id}/clone
	// parts: ["snapshots", "{name}", "clone"] for /api/v1/instances/{id}/snapshots/{name}/clone
	switch {
	case len(parts) == 1 && parts[0] == "clone":
		// /api/v1/instances/{id}/clone - Clone from instance
		s.handleCloneFromInstance(w, r, instanceID, instance, cloneProvider)
	case len(parts) == 3 && parts[0] == "snapshots" && parts[2] == "clone":
		// /api/v1/instances/{id}/snapshots/{name}/clone - Clone from snapshot
		snapshotName := parts[1]
		s.handleCloneFromSnapshot(w, r, instanceID, snapshotName, instance, cloneProvider)
	default:
		s.writeError(w, http.StatusNotFound, "Not found")
	}
}

// handleCloneFromInstance creates a clone from an existing instance.
func (s *Server) handleCloneFromInstance(w http.ResponseWriter, r *http.Request, instanceID string, instance *datastore.Instance, cloneProvider provider.CloneProvider) {
	// Parse request body
	var req struct {
		Name        string            `json:"name"`
		Linked      bool              `json:"linked"`
		CPUs        int               `json:"cpus,omitempty"`
		MemoryMB    int64             `json:"memory_mb,omitempty"`
		ResetMAC    bool              `json:"reset_mac"`
		Labels      map[string]string `json:"labels,omitempty"`
		Annotations map[string]string `json:"annotations,omitempty"`
	}

	if err := s.decodeJSONBody(r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// SECURITY: Validate clone name
	if err := validation.ValidateInstanceName(req.Name); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid clone name", err)
		return
	}

	// Check if clone name already exists (instances are keyed by ID; the name
	// lives in its own column, so the lookup must be by name)
	existing, _ := s.datastore.GetInstanceByName(r.Context(), req.Name)
	if existing != nil {
		s.writeError(w, http.StatusConflict, fmt.Sprintf("Instance with name %s already exists", req.Name))
		return
	}

	// Build clone options
	opts := provider.CloneOptions{
		LinkedClone: req.Linked,
		CPUs:        req.CPUs,
		MemoryMB:    req.MemoryMB,
		ResetMAC:    req.ResetMAC,
		Labels:      req.Labels,
		Annotations: req.Annotations,
	}

	// Create clone via provider
	cloneHandle, err := cloneProvider.CloneInstance(r.Context(), instance.Handle, req.Name, opts)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to clone instance", err)
		return
	}

	// Store clone in datastore
	cloneSpec := instance.Spec
	cloneSpec.Name = req.Name

	// Apply resource customizations
	if opts.CPUs > 0 {
		cloneSpec.CPUs = opts.CPUs
	}
	if opts.MemoryMB > 0 {
		cloneSpec.MemoryMB = opts.MemoryMB
	}

	// Merge labels and annotations into fresh maps: cloneSpec was copied by
	// value from the source spec, so its map fields still alias the source's
	// in-memory maps and writing into them would mutate the source.
	labels := make(map[string]string, len(instance.Spec.Labels)+len(opts.Labels))
	for k, v := range instance.Spec.Labels {
		labels[k] = v
	}
	for k, v := range opts.Labels {
		labels[k] = v
	}
	cloneSpec.Labels = labels

	annotations := make(map[string]string, len(instance.Spec.Annotations)+len(opts.Annotations))
	for k, v := range instance.Spec.Annotations {
		annotations[k] = v
	}
	for k, v := range opts.Annotations {
		annotations[k] = v
	}
	cloneSpec.Annotations = annotations

	// Add clone metadata
	cloneSpec.Annotations["cloned_from"] = instanceID
	cloneSpec.Annotations["clone_type"] = "instance"
	if opts.LinkedClone {
		cloneSpec.Annotations["clone_method"] = "linked"
	} else {
		cloneSpec.Annotations["clone_method"] = "full"
	}

	cloneInstance := &datastore.Instance{
		ID:          cloneHandle.ID,
		Name:        req.Name,
		Provider:    instance.Provider,
		State:       provider.StateStopped,
		Spec:        cloneSpec,
		Handle:      cloneHandle,
		Labels:      cloneSpec.Labels,
		Annotations: cloneSpec.Annotations,
	}

	if err := s.datastore.CreateInstance(r.Context(), cloneInstance); err != nil {
		// Best-effort cleanup: destroy the ZFS clone since we can't track it
		if dp, ok := cloneProvider.(provider.Provider); ok {
			_ = dp.DeleteInstance(r.Context(), cloneHandle, true)
		}
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to store clone", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(
		clientIP,
		"",
		"CLONE",
		fmt.Sprintf("/api/v1/instances/%s/clone", instanceID),
	)

	s.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"source_instance": instanceID,
		"clone_instance":  req.Name,
		"clone":           cloneInstance,
		"linked":          opts.LinkedClone,
		"message":         fmt.Sprintf("Instance %s cloned successfully as %s", instanceID, req.Name),
	})
}

// handleCloneFromSnapshot creates a clone from a snapshot.
func (s *Server) handleCloneFromSnapshot(w http.ResponseWriter, r *http.Request, instanceID, snapshotName string, instance *datastore.Instance, cloneProvider provider.CloneProvider) {
	// SECURITY: Validate snapshot name
	if err := validation.ValidateSnapshotName(snapshotName); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid snapshot name", err)
		return
	}

	// Parse request body
	var req struct {
		Name        string            `json:"name"`
		Linked      bool              `json:"linked"`
		CPUs        int               `json:"cpus,omitempty"`
		MemoryMB    int64             `json:"memory_mb,omitempty"`
		ResetMAC    bool              `json:"reset_mac"`
		Labels      map[string]string `json:"labels,omitempty"`
		Annotations map[string]string `json:"annotations,omitempty"`
	}

	if err := s.decodeJSONBody(r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// SECURITY: Validate clone name
	if err := validation.ValidateInstanceName(req.Name); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid clone name", err)
		return
	}

	// Check if clone name already exists (instances are keyed by ID; the name
	// lives in its own column, so the lookup must be by name)
	existing, _ := s.datastore.GetInstanceByName(r.Context(), req.Name)
	if existing != nil {
		s.writeError(w, http.StatusConflict, fmt.Sprintf("Instance with name %s already exists", req.Name))
		return
	}

	// Check if provider supports snapshots
	snapProvider, ok := cloneProvider.(provider.SnapshotProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, "Provider does not support snapshots")
		return
	}

	// List snapshots using the instance's handle (with ZFS dataset metadata)
	snapshots, err := snapProvider.ListSnapshots(r.Context(), instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list snapshots", err)
		return
	}

	// Find the target snapshot
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

	// Build clone options
	opts := provider.CloneOptions{
		LinkedClone: req.Linked,
		CPUs:        req.CPUs,
		MemoryMB:    req.MemoryMB,
		ResetMAC:    req.ResetMAC,
		Labels:      req.Labels,
		Annotations: req.Annotations,
	}

	// Create clone from snapshot via provider
	cloneHandle, err := cloneProvider.CloneFromSnapshot(r.Context(), targetSnapshot.Handle, req.Name, opts)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to clone from snapshot", err)
		return
	}

	// Store clone in datastore
	cloneSpec := instance.Spec
	cloneSpec.Name = req.Name

	// Apply resource customizations
	if opts.CPUs > 0 {
		cloneSpec.CPUs = opts.CPUs
	}
	if opts.MemoryMB > 0 {
		cloneSpec.MemoryMB = opts.MemoryMB
	}

	// Merge labels and annotations into fresh maps: cloneSpec was copied by
	// value from the source spec, so its map fields still alias the source's
	// in-memory maps and writing into them would mutate the source.
	labels := make(map[string]string, len(instance.Spec.Labels)+len(opts.Labels))
	for k, v := range instance.Spec.Labels {
		labels[k] = v
	}
	for k, v := range opts.Labels {
		labels[k] = v
	}
	cloneSpec.Labels = labels

	annotations := make(map[string]string, len(instance.Spec.Annotations)+len(opts.Annotations))
	for k, v := range instance.Spec.Annotations {
		annotations[k] = v
	}
	for k, v := range opts.Annotations {
		annotations[k] = v
	}
	cloneSpec.Annotations = annotations

	// Add clone metadata
	cloneSpec.Annotations["cloned_from"] = instanceID
	cloneSpec.Annotations["cloned_from_snapshot"] = snapshotName
	cloneSpec.Annotations["clone_type"] = "snapshot"
	if opts.LinkedClone {
		cloneSpec.Annotations["clone_method"] = "linked"
	} else {
		cloneSpec.Annotations["clone_method"] = "full"
	}

	cloneInstance := &datastore.Instance{
		ID:          cloneHandle.ID,
		Name:        req.Name,
		Provider:    instance.Provider,
		State:       provider.StateStopped,
		Spec:        cloneSpec,
		Handle:      cloneHandle,
		Labels:      cloneSpec.Labels,
		Annotations: cloneSpec.Annotations,
	}

	if err := s.datastore.CreateInstance(r.Context(), cloneInstance); err != nil {
		// Best-effort cleanup: destroy the ZFS clone since we can't track it
		if dp, ok := cloneProvider.(provider.Provider); ok {
			_ = dp.DeleteInstance(r.Context(), cloneHandle, true)
		}
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to store clone", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(
		clientIP,
		"",
		"CLONE",
		fmt.Sprintf("/api/v1/instances/%s/snapshots/%s/clone", instanceID, snapshotName),
	)

	s.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"source_instance": instanceID,
		"source_snapshot": snapshotName,
		"clone_instance":  req.Name,
		"clone":           cloneInstance,
		"linked":          opts.LinkedClone,
		"message":         fmt.Sprintf("Snapshot %s cloned successfully as %s", snapshotName, req.Name),
	})
}
