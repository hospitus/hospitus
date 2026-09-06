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

	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// SECURITY: Validate clone name
	if err := validation.ValidateInstanceName(req.Name); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid clone name", err)
		return
	}

	// Check if clone name already exists (instances are keyed by ID; the name
	// lives in its own column, so the lookup must be by name).
	//
	// The error is not discarded: a datastore that cannot answer is not the
	// same as a name that is free, and treating it as free cloned onto a name
	// already in use.
	existing, err := s.datastore.GetInstanceByName(r.Context(), req.Name)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to check clone name", err)
		return
	}
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

	cloneSpec := cloneSpecFrom(instance, req.Name, opts)
	cloneSpec.Annotations["cloned_from"] = instanceID
	cloneSpec.Annotations["clone_type"] = "instance"

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
		// Through discardCreatedInstance, which gives the cleanup a context of
		// its own: a client that gave up is one way this write fails, and that
		// same cancellation made the r.Context() delete return at once — so
		// the clone stayed on disk with no record of it anywhere.
		if dp, ok := cloneProvider.(provider.Provider); ok {
			err = s.discardCreatedInstance(r.Context(), dp, cloneHandle, err)
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

	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// SECURITY: Validate clone name
	if err := validation.ValidateInstanceName(req.Name); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid clone name", err)
		return
	}

	// Check if clone name already exists (instances are keyed by ID; the name
	// lives in its own column, so the lookup must be by name).
	//
	// The error is not discarded: a datastore that cannot answer is not the
	// same as a name that is free, and treating it as free cloned onto a name
	// already in use.
	existing, err := s.datastore.GetInstanceByName(r.Context(), req.Name)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to check clone name", err)
		return
	}
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

	targetSnapshot, ok := s.findSnapshot(w, r, instance, snapshotName, snapProvider)
	if !ok {
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

	cloneSpec := cloneSpecFrom(instance, req.Name, opts)
	cloneSpec.Annotations["cloned_from"] = instanceID
	cloneSpec.Annotations["cloned_from_snapshot"] = snapshotName
	cloneSpec.Annotations["clone_type"] = "snapshot"

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
		// Through discardCreatedInstance, which gives the cleanup a context of
		// its own: a client that gave up is one way this write fails, and that
		// same cancellation made the r.Context() delete return at once — so
		// the clone stayed on disk with no record of it anywhere.
		if dp, ok := cloneProvider.(provider.Provider); ok {
			err = s.discardCreatedInstance(r.Context(), dp, cloneHandle, err)
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

// cloneSpecFrom derives the spec a clone is recorded with: the source's, under
// a new name, with the caller's resource overrides applied and its labels and
// annotations merged in.
//
// The maps are rebuilt rather than reused. A struct copy leaves them aliasing
// the source's, so writing the clone's annotations would have written the
// source's too — which is why both clone paths carried these thirty lines, and
// why they had already begun to differ.
func cloneSpecFrom(instance *datastore.Instance, name string, opts provider.CloneOptions) provider.InstanceSpec {
	spec := instance.Spec
	spec.Name = name

	if opts.CPUs > 0 {
		spec.CPUs = opts.CPUs
	}
	if opts.MemoryMB > 0 {
		spec.MemoryMB = opts.MemoryMB
	}

	spec.Labels = mergeStrings(instance.Spec.Labels, opts.Labels)
	spec.Annotations = mergeStrings(instance.Spec.Annotations, opts.Annotations)

	if opts.LinkedClone {
		spec.Annotations["clone_method"] = "linked"
	} else {
		spec.Annotations["clone_method"] = "full"
	}

	if opts.ResetMAC {
		// Every clone provider clears the inherited MACs when ResetMAC is set,
		// and this spec is what the datastore keeps: leaving them meant an API
		// read reported the source's addresses for interfaces the guest had
		// been given new ones on. The slice is copied first — spec is a shallow
		// copy of instance.Spec, and clearing in place would blank the source's
		// own record too.
		networks := make([]provider.NetworkSpec, len(spec.Networks))
		copy(networks, spec.Networks)
		for i := range networks {
			networks[i].MAC = ""
		}
		spec.Networks = networks
	}

	return spec
}

// mergeStrings returns a new map holding base overlaid with overrides.
func mergeStrings(base, overrides map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(overrides))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return merged
}
