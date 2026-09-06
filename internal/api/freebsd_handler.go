package api

import (
	"fmt"
	"net/http"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/internal/security"
	"github.com/hospitus/hospitus/pkg/provider/jail"
	"github.com/hospitus/hospitus/pkg/validation"
)

// handleFreeBSD handles FreeBSD-specific operations.
//
// Endpoints:
//
//	GET    /api/v1/instances/{id}/freebsd/rctl       - Get resource limits
//	POST   /api/v1/instances/{id}/freebsd/rctl       - Set resource limits
//	DELETE /api/v1/instances/{id}/freebsd/rctl       - Remove resource limits
//	GET    /api/v1/instances/{id}/freebsd/vnet       - Get VNET status
//	POST   /api/v1/instances/{id}/freebsd/vnet       - Enable VNET
//	DELETE /api/v1/instances/{id}/freebsd/vnet       - Disable VNET
//
// SECURITY: All operations require authentication and validate instance names.
// parts parameter is the URL path split: ["api", "v1", "instances", "{id}", "freebsd", ...]
func (s *Server) handleFreeBSD(w http.ResponseWriter, r *http.Request, instanceID string, parts []string) {
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

	// Check if provider is jail (FreeBSD-specific features)
	jailProvider, ok := prov.(*jail.JailProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, fmt.Sprintf("Provider %s does not support FreeBSD features", instance.Provider))
		return
	}

	// Route based on feature
	// parts: ["api", "v1", "instances", "{id}", "freebsd", "{feature}"]
	if len(parts) < 6 {
		s.writeError(w, http.StatusNotFound, "Not found")
		return
	}

	feature := parts[5]
	switch feature {
	case "rctl":
		s.handleRctl(w, r, instanceID, instance, jailProvider)
	case "vnet":
		s.handleVnet(w, r, instanceID, instance, jailProvider)
	default:
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("Unknown FreeBSD feature: %s", feature))
	}
}

// handleRctl handles rctl (resource control) operations.
func (s *Server) handleRctl(w http.ResponseWriter, r *http.Request, instanceID string, instance *datastore.Instance, jailProvider *jail.JailProvider) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetRctl(w, r, instanceID, instance, jailProvider)
	case http.MethodPost:
		s.handleSetRctl(w, r, instanceID, instance, jailProvider)
	case http.MethodDelete:
		s.handleRemoveRctl(w, r, instanceID, instance, jailProvider)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleGetRctl retrieves current resource limits.
func (s *Server) handleGetRctl(w http.ResponseWriter, r *http.Request, instanceID string, instance *datastore.Instance, jailProvider *jail.JailProvider) {
	limits, err := jailProvider.GetResourceLimits(r.Context(), instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to get resource limits", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(clientIP, "", "GET", fmt.Sprintf("/api/v1/instances/%s/freebsd/rctl", instanceID))

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instance": instanceID,
		"limits":   limits,
		"count":    len(limits),
	})
}

// handleSetRctl sets resource limits.
func (s *Server) handleSetRctl(w http.ResponseWriter, r *http.Request, instanceID string, instance *datastore.Instance, jailProvider *jail.JailProvider) {
	// Parse request body
	var req struct {
		Limits []jail.ResourceLimit `json:"limits"`
	}

	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// Validate limits
	if len(req.Limits) == 0 {
		s.writeError(w, http.StatusBadRequest, "No limits specified")
		return
	}

	if err := jailProvider.SetResourceLimits(r.Context(), instance.Handle, req.Limits); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to set resource limits", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(
		clientIP,
		"",
		"SET_RCTL",
		fmt.Sprintf("/api/v1/instances/%s/freebsd/rctl", instanceID),
	)

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instance": instanceID,
		"limits":   req.Limits,
		"message":  fmt.Sprintf("Resource limits set successfully for instance %s", instanceID),
	})
}

// handleRemoveRctl removes all resource limits.
func (s *Server) handleRemoveRctl(w http.ResponseWriter, r *http.Request, instanceID string, instance *datastore.Instance, jailProvider *jail.JailProvider) {
	if err := jailProvider.RemoveResourceLimits(r.Context(), instance.Handle); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to remove resource limits", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(
		clientIP,
		"",
		"REMOVE_RCTL",
		fmt.Sprintf("/api/v1/instances/%s/freebsd/rctl", instanceID),
	)

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instance": instanceID,
		"message":  fmt.Sprintf("Resource limits removed successfully from instance %s", instanceID),
	})
}

// handleVnet handles VNET (virtual network stack) operations.
func (s *Server) handleVnet(w http.ResponseWriter, r *http.Request, instanceID string, instance *datastore.Instance, jailProvider *jail.JailProvider) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetVnet(w, r, instanceID, instance, jailProvider)
	case http.MethodPost:
		s.handleEnableVnet(w, r, instanceID, instance, jailProvider)
	case http.MethodDelete:
		s.handleDisableVnet(w, r, instanceID, instance, jailProvider)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleGetVnet retrieves VNET status.
func (s *Server) handleGetVnet(w http.ResponseWriter, r *http.Request, instanceID string, instance *datastore.Instance, jailProvider *jail.JailProvider) {
	status, err := jailProvider.GetVNETStatus(r.Context(), instance.Handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to get VNET status", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(clientIP, "", "GET", fmt.Sprintf("/api/v1/instances/%s/freebsd/vnet", instanceID))

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instance": instanceID,
		"vnet":     status,
	})
}

// handleEnableVnet enables VNET for a jail.
func (s *Server) handleEnableVnet(w http.ResponseWriter, r *http.Request, instanceID string, instance *datastore.Instance, jailProvider *jail.JailProvider) {
	// Parse request body
	var config jail.VNETConfig
	if err := s.decodeJSONBody(w, r, &config); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if err := jailProvider.EnableVNET(r.Context(), instance.Handle, config); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to enable VNET", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(
		clientIP,
		"",
		"ENABLE_VNET",
		fmt.Sprintf("/api/v1/instances/%s/freebsd/vnet", instanceID),
	)

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instance": instanceID,
		"vnet":     config,
		"message":  fmt.Sprintf("VNET enabled successfully for instance %s", instanceID),
	})
}

// handleDisableVnet disables VNET for a jail.
func (s *Server) handleDisableVnet(w http.ResponseWriter, r *http.Request, instanceID string, instance *datastore.Instance, jailProvider *jail.JailProvider) {
	if err := jailProvider.DisableVNET(r.Context(), instance.Handle); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to disable VNET", err)
		return
	}

	// Log access
	clientIP := s.extractClientIP(r)
	security.GetGlobalAuditLogger().LogResourceAccess(
		clientIP,
		"",
		"DISABLE_VNET",
		fmt.Sprintf("/api/v1/instances/%s/freebsd/vnet", instanceID),
	)

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instance": instanceID,
		"message":  fmt.Sprintf("VNET disabled successfully for instance %s", instanceID),
	})
}
