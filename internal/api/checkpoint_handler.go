package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
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
		if err := s.decodeJSONBody(w, r, &req); err != nil {
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
		security.GetGlobalAuditLogger().LogResourceAccess(s.extractClientIP(r), s.auditIdentity(r), "CREATE",
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
		// Audited like the creation above: a restore rolls a running instance
		// back to an earlier state, and only the create left a trail.
		security.GetGlobalAuditLogger().LogResourceAccess(s.extractClientIP(r), s.auditIdentity(r), "RESTORE",
			fmt.Sprintf("/api/v1/instances/%s/checkpoint/%s", instanceID, name))
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
		security.GetGlobalAuditLogger().LogResourceAccess(s.extractClientIP(r), s.auditIdentity(r), "DELETE",
			fmt.Sprintf("/api/v1/instances/%s/checkpoint/%s", instanceID, name))
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
	if err := s.decodeJSONBody(w, r, &req); err != nil {
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

	// The id, the name and the handle in one transaction. The handle is a JSON
	// blob naming the old instance; updating it separately meant a failure
	// there left a row with the new name and a handle pointing at the old one,
	// and every handle-taking operation — delete included, which destroys the
	// dataset the handle names — followed it.
	if err := s.datastore.RenameInstance(r.Context(), instanceID, req.Name,
		renamedHandle(r.Context(), prov, instance.Handle, req.Name)); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Provider renamed but failed to update datastore", err)
		return
	}

	security.GetGlobalAuditLogger().LogResourceAccess(s.extractClientIP(r), s.auditIdentity(r), "RENAME",
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
		// Only when there is an id to look for: strings.Contains(str, "")
		// is true for every value, so a handle with no ID dropped its whole
		// metadata instead of the entries naming the old instance.
		if old.ID != "" {
			if str, isString := v.(string); isString && strings.Contains(str, old.ID) {
				continue
			}
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
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}
	if req.TargetRelease == "" {
		s.writeError(w, http.StatusBadRequest, "target_release is required")
		return
	}
	// The shape FreeBSD publishes, checked here: the value is handed to
	// freebsd-update as the release to fetch, and only emptiness was refused.
	if !freeBSDReleasePattern.MatchString(req.TargetRelease) {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"target_release %q is not a FreeBSD release (expected e.g. 14.3-RELEASE)", req.TargetRelease))
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

	security.GetGlobalAuditLogger().LogResourceAccess(s.extractClientIP(r), s.auditIdentity(r), "UPGRADE",
		fmt.Sprintf("/api/v1/instances/%s/upgrade -> %s", instanceID, req.TargetRelease))

	s.writeJSON(w, http.StatusAccepted, map[string]string{
		"job_id":         j.ID,
		"instance":       instanceID,
		"target_release": req.TargetRelease,
		"message":        fmt.Sprintf("Upgrade job submitted for %s → %s", instanceID, req.TargetRelease),
	})
}

// freeBSDReleasePattern is the whole grammar a target release may use:
// <major>.<minor> followed by RELEASE, RELEASE-pN, STABLE or CURRENT.
var freeBSDReleasePattern = regexp.MustCompile(`^\d{1,2}\.\d{1,2}-(?:RELEASE(?:-p\d{1,3})?|STABLE|CURRENT)$`)
