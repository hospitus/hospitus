package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hospitus/hospitus/internal/security"
	"github.com/hospitus/hospitus/pkg/job"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// handleCheckpoint dispatches checkpoint create/restore/delete operations.
//
// Endpoints:
//
//	POST   /api/v1/instances/{id}/checkpoint              - Create checkpoint {"name":"snap1"}
//	POST   /api/v1/instances/{id}/checkpoint/{name}/restore - Restore from checkpoint
//	DELETE /api/v1/instances/{id}/checkpoint/{name}        - Delete checkpoint
func (s *Server) handleCheckpoint(w http.ResponseWriter, r *http.Request, instanceID string, parts []string) {
	instance, err := s.lookupInstance(r.Context(), instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	cp, ok := prov.(provider.CheckpointProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support checkpoints", instance.Provider))
		return
	}

	// parts[0] == "checkpoint"
	switch {
	case len(parts) == 1 && r.Method == http.MethodPost:
		// POST /checkpoint  →  create
		var req struct {
			Name string `json:"name"`
		}
		if err := s.decodeJSONBody(r, &req); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
			return
		}
		if err := validation.ValidateSnapshotName(req.Name); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid checkpoint name", err)
			return
		}
		if err := cp.CheckpointInstance(r.Context(), instance.Handle, req.Name); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to create checkpoint", err)
			return
		}
		security.GetGlobalAuditLogger().LogResourceAccess(s.extractClientIP(r), "", "CREATE",
			fmt.Sprintf("/api/v1/instances/%s/checkpoint/%s", instanceID, req.Name))
		s.writeJSON(w, http.StatusCreated, map[string]string{
			"instance":   instanceID,
			"checkpoint": req.Name,
			"message":    fmt.Sprintf("Checkpoint %s created", req.Name),
		})

	case len(parts) == 3 && parts[2] == "restore" && r.Method == http.MethodPost:
		// POST /checkpoint/{name}/restore
		name := parts[1]
		if err := validation.ValidateSnapshotName(name); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid checkpoint name", err)
			return
		}
		if err := cp.RestoreCheckpoint(r.Context(), instance.Handle, name); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to restore checkpoint", err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]string{
			"instance":   instanceID,
			"checkpoint": name,
			"message":    fmt.Sprintf("Instance %s restored from checkpoint %s", instanceID, name),
		})

	case len(parts) == 2 && r.Method == http.MethodDelete:
		// DELETE /checkpoint/{name}
		name := parts[1]
		if err := validation.ValidateSnapshotName(name); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid checkpoint name", err)
			return
		}
		if err := cp.DeleteCheckpoint(r.Context(), instance.Handle, name); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to delete checkpoint", err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]string{
			"instance":   instanceID,
			"checkpoint": name,
			"message":    fmt.Sprintf("Checkpoint %s deleted", name),
		})

	default:
		s.writeError(w, http.StatusNotFound, "Not found")
	}
}

// handleCheckpointList lists all checkpoints for an instance.
//
//	GET /api/v1/instances/{id}/checkpoints
func (s *Server) handleCheckpointList(w http.ResponseWriter, r *http.Request, instanceID string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	instance, err := s.lookupInstance(r.Context(), instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	cp, ok := prov.(provider.CheckpointProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support checkpoints", instance.Provider))
		return
	}

	checkpoints, err := cp.ListCheckpoints(r.Context(), instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list checkpoints", err)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instance":    instanceID,
		"checkpoints": checkpoints,
		"count":       len(checkpoints),
	})
}

// handlePauseInstance freezes execution of a running instance (SIGSTOP).
//
//	POST /api/v1/instances/{id}/pause
func (s *Server) handlePauseInstance(w http.ResponseWriter, r *http.Request, instanceID string) {
	instance, err := s.lookupInstance(r.Context(), instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	pp, ok := prov.(provider.PauseProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support pause/resume", instance.Provider))
		return
	}

	if err := pp.PauseInstance(r.Context(), instance.Handle); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to pause instance", err)
		return
	}

	if err := s.datastore.UpdateInstanceState(r.Context(), instance.ID, provider.StatePaused); err != nil {
		s.logger.Warn("Failed to update instance state to paused in datastore", "instance", instanceID, "error", err)
	}

	s.writeJSON(w, http.StatusOK, map[string]string{
		"instance": instanceID,
		"state":    string(provider.StatePaused),
		"message":  fmt.Sprintf("Instance %s paused", instanceID),
	})
}

// handleResumeInstance resumes a paused instance (SIGCONT).
//
//	POST /api/v1/instances/{id}/resume
func (s *Server) handleResumeInstance(w http.ResponseWriter, r *http.Request, instanceID string) {
	instance, err := s.lookupInstance(r.Context(), instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	pp, ok := prov.(provider.PauseProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support pause/resume", instance.Provider))
		return
	}

	if err := pp.ResumeInstance(r.Context(), instance.Handle); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to resume instance", err)
		return
	}

	if err := s.datastore.UpdateInstanceState(r.Context(), instance.ID, provider.StateRunning); err != nil {
		s.logger.Warn("Failed to update instance state to running in datastore", "instance", instanceID, "error", err)
	}

	s.writeJSON(w, http.StatusOK, map[string]string{
		"instance": instanceID,
		"state":    string(provider.StateRunning),
		"message":  fmt.Sprintf("Instance %s resumed", instanceID),
	})
}

// handleRenameInstance renames a stopped instance.
//
//	POST /api/v1/instances/{id}/rename  {"name": "new-name"}
func (s *Server) handleRenameInstance(w http.ResponseWriter, r *http.Request, instanceID string) {
	var req struct {
		Name string `json:"name"`
	}
	if err := s.decodeJSONBody(r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	if err := validation.ValidateInstanceName(req.Name); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid name", err)
		return
	}

	instance, err := s.lookupInstance(r.Context(), instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	rp, ok := prov.(provider.RenameProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support rename", instance.Provider))
		return
	}

	if err := rp.RenameInstance(r.Context(), instance.Handle, req.Name); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to rename instance", err)
		return
	}

	// Update the name in the datastore.
	if err := s.datastore.RenameInstance(r.Context(), instanceID, req.Name); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Provider renamed but failed to update datastore", err)
		return
	}

	// And the handle: RenameInstance rewrites the id and name columns and
	// leaves it, a JSON blob, naming the old instance. Every operation taking
	// a handle then misses — and delete destroys the dataset the handle names,
	// which a later jail of the old name would own.
	if err := s.datastore.UpdateInstanceHandle(r.Context(), req.Name,
		renamedHandle(r.Context(), prov, instance.Handle, req.Name)); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Instance renamed but its handle still names the old instance", err)
		return
	}

	security.GetGlobalAuditLogger().LogResourceAccess(s.extractClientIP(r), "", "RENAME",
		fmt.Sprintf("/api/v1/instances/%s/rename -> %s", instanceID, req.Name))

	s.writeJSON(w, http.StatusOK, map[string]string{
		"old_name": instanceID,
		"new_name": req.Name,
		"message":  fmt.Sprintf("Instance %s renamed to %s", instanceID, req.Name),
	})
}

// renamedHandle returns the handle an instance should carry after a rename.
//
// The provider is asked first: its ListInstances derives handles from what is
// on disk now, so it answers with the paths the rename produced rather than
// the ones it replaced.
//
// If it cannot answer, the old handle is carried over with the new ID, minus
// any metadata whose value mentions the old name. Dropping such a value is
// safer than keeping it: a stale zfs_dataset points at a path that either does
// not exist or, later, belongs to a different instance entirely.
func renamedHandle(ctx context.Context, prov provider.Provider, old provider.InstanceHandle, newName string) provider.InstanceHandle {
	if handles, err := prov.ListInstances(ctx, provider.InstanceFilter{}); err == nil {
		for _, h := range handles {
			if h.ID == newName {
				return h
			}
		}
	}

	fresh := provider.InstanceHandle{
		ID:       newName,
		Provider: old.Provider,
		Metadata: map[string]interface{}{},
	}
	for k, v := range old.Metadata {
		if str, isString := v.(string); isString && strings.Contains(str, old.ID) {
			continue
		}
		fresh.Metadata[k] = v
	}
	return fresh
}

// handleUpgradeInstance upgrades the base system of a stopped instance to a new release.
//
//	POST /api/v1/instances/{id}/upgrade  {"target_release": "14.3-RELEASE"}
//
// This is a long-running operation; it returns 202 Accepted with a job ID that
// the client can poll via GET /api/v1/jobs/{id}.
func (s *Server) handleUpgradeInstance(w http.ResponseWriter, r *http.Request, instanceID string) {
	var req struct {
		TargetRelease string `json:"target_release"`
	}
	if err := s.decodeJSONBody(r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	if req.TargetRelease == "" {
		s.writeError(w, http.StatusBadRequest, "target_release is required")
		return
	}

	instance, err := s.lookupInstance(r.Context(), instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	up, ok := prov.(provider.UpgradeProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support upgrade", instance.Provider))
		return
	}

	j, err := s.submitJob(r.Context(), "upgrade", fmt.Sprintf("Upgrade %s to %s", instanceID, req.TargetRelease),
		map[string]string{
			"instance_id":    instanceID,
			"target_release": req.TargetRelease,
		},
		func(ctx context.Context, _ *job.Job) error {
			return up.UpgradeInstance(ctx, instance.Handle, req.TargetRelease)
		},
	)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to submit upgrade job", err)
		return
	}

	security.GetGlobalAuditLogger().LogResourceAccess(s.extractClientIP(r), "", "UPGRADE",
		fmt.Sprintf("/api/v1/instances/%s/upgrade -> %s", instanceID, req.TargetRelease))

	s.writeJSON(w, http.StatusAccepted, map[string]string{
		"job_id":         j.ID,
		"instance":       instanceID,
		"target_release": req.TargetRelease,
		"message":        fmt.Sprintf("Upgrade job submitted for %s → %s", instanceID, req.TargetRelease),
	})
}
