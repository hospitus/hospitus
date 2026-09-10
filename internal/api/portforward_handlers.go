package api

import (
	"fmt"
	"net/http"

	"github.com/hospitus/hospitus/pkg/provider"
)

// handlePortForwards handles port-forwarding operations for QEMU VMs.
//
// GET    /api/v1/instances/{id}/port-forwards         - List rules
// POST   /api/v1/instances/{id}/port-forwards         - Add rule
// DELETE /api/v1/instances/{id}/port-forwards         - Remove rule
func (s *Server) handlePortForwards(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	instanceID := r.PathValue("id")
	if instanceID == "" {
		s.writeError(w, http.StatusBadRequest, "Instance ID is required")
		return
	}

	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	pfProv, ok := prov.(provider.PortForwardProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, "Provider does not support port forwarding")
		return
	}

	switch r.Method {
	case http.MethodGet:
		rules, err := pfProv.ListPortForwards(ctx, instance.Handle)
		if err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list port forwards", err)
			return
		}
		if rules == nil {
			rules = []provider.PortForward{}
		}
		s.writeJSON(w, http.StatusOK, map[string]interface{}{"port_forwards": rules})

	case http.MethodPost:
		var req provider.PortForward
		if err := s.decodeJSONBody(w, r, &req); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
			return
		}
		if !validPort(req.HostPort) {
			s.writeError(w, http.StatusBadRequest, "host_port must be between 1 and 65535")
			return
		}
		if !validPort(req.GuestPort) {
			s.writeError(w, http.StatusBadRequest, "guest_port must be between 1 and 65535")
			return
		}
		if req.Protocol == "" {
			req.Protocol = "tcp"
		}
		if req.Protocol != "tcp" && req.Protocol != "udp" {
			s.writeError(w, http.StatusBadRequest, "protocol must be tcp or udp")
			return
		}
		if err := pfProv.AddPortForward(ctx, instance.Handle, req); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to add port forward", err)
			return
		}
		s.writeJSON(w, http.StatusCreated, map[string]interface{}{"port_forward": req})

	case http.MethodDelete:
		var req struct {
			Protocol string `json:"protocol"`
			HostPort int    `json:"host_port"`
		}
		if err := s.decodeJSONBody(w, r, &req); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
			return
		}
		// The same range the POST branch checks — through the same helper, so
		// the two cannot drift. Zero is outside it, so it needs no separate
		// "required" check.
		if !validPort(req.HostPort) {
			s.writeError(w, http.StatusBadRequest, "host_port must be between 1 and 65535")
			return
		}
		if req.Protocol == "" {
			req.Protocol = "tcp"
		}
		// The same allowlist as the POST branch. A rule is identified by host
		// port and protocol, so "sctp" matched nothing, removed nothing, and
		// still answered 204 — the caller was told the rule was gone.
		if req.Protocol != "tcp" && req.Protocol != "udp" {
			s.writeError(w, http.StatusBadRequest, "protocol must be tcp or udp")
			return
		}
		if err := pfProv.RemovePortForward(ctx, instance.Handle, req.Protocol, req.HostPort); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to remove port forward", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
