package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

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

	// A known resource reached with the wrong method is a 405, not a 404: the
	// default answered "Not found" for GET /checkpoint, which exists.
	case len(parts) == 1:
		s.writeMethodNotAllowed(w, http.MethodPost)

	case len(parts) == 2:
		s.writeMethodNotAllowed(w, http.MethodDelete)

	case len(parts) == 3 && parts[2] == "restore":
		s.writeMethodNotAllowed(w, http.MethodPost)

	default:
		s.writeError(w, http.StatusNotFound, "Not found")
	}
}

// handleCheckpointList lists all checkpoints for an instance.
//
//	GET /api/v1/instances/{id}/checkpoints
func (s *Server) handleCheckpointList(w http.ResponseWriter, r *http.Request, instanceID string) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, http.MethodGet)
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

	// Detached: the instance is already paused, and a client that hung up
	// cancels the context this write rides on — leaving the row saying
	// "running" for an instance that is not.
	postPause, cancelPostPause := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
	defer cancelPostPause()
	if err := s.datastore.UpdateInstanceState(postPause, instance.ID, provider.StatePaused); err != nil {
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

	// Detached, like the pause above.
	postResume, cancelPostResume := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
	defer cancelPostResume()
	if err := s.datastore.UpdateInstanceState(postResume, instance.ID, provider.StateRunning); err != nil {
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

	// The datastore first. A stale record can hold req.Name while the provider
	// side of that name is free: the provider rename then succeeded, the row
	// update was rejected as a duplicate, and the two disagreed about what the
	// instance is called — with the handle naming one and the record the other.
	existing, err := s.datastore.GetInstanceByName(r.Context(), req.Name)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to check the new name", err)
		return
	}
	if existing != nil {
		s.writeError(w, http.StatusConflict, fmt.Sprintf("Instance with name %s already exists", req.Name))
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
	// instance.ID, not the path value: lookupInstance accepts a name as well as
	// an id, and RenameInstance updates WHERE id = ?, so a rename addressed by
	// name updated no row at all — after the provider had already renamed.
	newHandle := renamedHandle(r.Context(), prov, instance.Handle, req.Name)
	if err := s.datastore.RenameInstance(r.Context(), instance.ID, req.Name, newHandle); err != nil {
		// The provider has already renamed, and the check above cannot prevent
		// this: two requests naming the same target reach their own provider,
		// whose locks are owner-scoped and do not coordinate, and the second
		// datastore write is then refused by the unique name. Undo the provider
		// rename rather than leave the two disagreeing.
		//
		// Addressed by newHandle, not instance.Handle: the old handle names
		// something the provider no longer has. On a detached context, because
		// this must finish whether or not the client is still listening.
		rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
		defer cancelRollback()

		if rbErr := rp.RenameInstance(rollbackCtx, newHandle, instance.Handle.ID); rbErr != nil {
			s.writeLoggedError(w, http.StatusInternalServerError,
				fmt.Sprintf("Renamed to %s at the provider but not recorded, and the rename could not be undone: the provider now calls it %s while the datastore still says %s",
					req.Name, req.Name, instance.Name),
				errors.Join(err, rbErr))
			return
		}
		s.writeLoggedError(w, http.StatusInternalServerError,
			"Could not record the rename; the instance was put back under its original name", err)
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
