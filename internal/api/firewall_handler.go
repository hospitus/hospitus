package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hospitus/hospitus/pkg/firewall"
	"github.com/hospitus/hospitus/pkg/provider"
)

// InitializeFirewall initializes the firewall manager on the server
func (s *Server) InitializeFirewall(ctx context.Context) error {
	mgr, err := firewall.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create firewall manager: %w", err)
	}

	if err := mgr.Initialize(ctx); err != nil {
		return fmt.Errorf("failed to initialize firewall manager: %w", err)
	}

	s.firewallMgr = mgr
	return nil
}

// handleFirewall handles firewall/port forwarding endpoints
//
// POST /api/v1/firewall/expose - Add port forwarding rule
// DELETE /api/v1/firewall/expose - Remove port forwarding rule
// GET /api/v1/firewall/expose/{instance} - List port forwarding rules
func (s *Server) handleFirewall(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")

	// /api/v1/firewall/expose
	if len(parts) >= 4 && parts[3] == "expose" {
		switch r.Method {
		case http.MethodPost:
			s.handleExposePort(w, r)
		case http.MethodGet:
			if len(parts) >= 5 {
				s.handleListExposedPorts(w, r, parts[4])
			} else {
				s.writeError(w, http.StatusBadRequest, "Instance name required")
			}
		case http.MethodDelete:
			s.handleUnexposePort(w, r)
		default:
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	// /api/v1/firewall/nat
	if len(parts) >= 4 && parts[3] == "nat" {
		switch r.Method {
		case http.MethodPost:
			s.handleSetupNAT(w, r)
		case http.MethodDelete:
			s.handleRemoveNAT(w, r)
		default:
			s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	s.writeError(w, http.StatusNotFound, "Not found")
}

// ExposePortRequest represents a request to expose a port
type ExposePortRequest struct {
	Instance   string `json:"instance"`
	Provider   string `json:"provider"`
	Protocol   string `json:"protocol"`
	HostPort   int    `json:"host_port"`
	TargetPort int    `json:"target_port"`
	TargetIP   string `json:"target_ip"`
}

// handleExposePort adds a port forwarding rule
func (s *Server) handleExposePort(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.firewallMgr == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Firewall manager not initialized")
		return
	}

	var req ExposePortRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// Validate required fields
	if req.Instance == "" {
		s.writeError(w, http.StatusBadRequest, "Instance name is required")
		return
	}
	if req.HostPort == 0 || req.TargetPort == 0 {
		s.writeError(w, http.StatusBadRequest, "Both host_port and target_port are required")
		return
	}
	// The range too, not just "not zero": a negative or out-of-range port
	// reached the firewall backend and surfaced as an opaque pf or ipfw
	// failure instead of a bad request.
	if !validPort(req.HostPort) || !validPort(req.TargetPort) {
		s.writeError(w, http.StatusBadRequest, "host_port and target_port must be between 1 and 65535")
		return
	}
	// Fill in the guest address when the caller left it out. Most instances
	// carry one; a guest that took its address from DHCP does not, because
	// nothing reports the lease back, and then only the caller knows it.
	if req.TargetIP == "" {
		req.TargetIP = s.resolveInstanceIP(ctx, req.Instance)
	}
	if req.TargetIP == "" {
		s.writeError(w, http.StatusBadRequest,
			"target_ip is required: this instance has no recorded address, which is "+
				"normal for a DHCP guest — pass the address it reports (hospitus <provider> "+
				"expose add --target-ip)")
		return
	}

	// Default to TCP
	protocol := firewall.ProtocolTCP
	if req.Protocol == "udp" {
		protocol = firewall.ProtocolUDP
	}

	// Default provider
	if req.Provider == "" {
		req.Provider = "jail"
	}

	// Create port mapping
	mapping, err := s.firewallMgr.ExposePort(ctx,
		req.Instance,
		req.Provider,
		protocol,
		req.HostPort,
		req.TargetPort,
		req.TargetIP,
	)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to expose port", err)
		return
	}

	s.writeJSON(w, http.StatusCreated, mapping)
}

// UnexposePortRequest represents a request to remove port forwarding
type UnexposePortRequest struct {
	Instance string `json:"instance"`
	HostPort int    `json:"host_port"`
	Protocol string `json:"protocol"`
}

// handleUnexposePort removes a port forwarding rule
func (s *Server) handleUnexposePort(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.firewallMgr == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Firewall manager not initialized")
		return
	}

	var req UnexposePortRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if req.Instance == "" {
		s.writeError(w, http.StatusBadRequest, "Instance name is required")
		return
	}
	if req.HostPort == 0 {
		s.writeError(w, http.StatusBadRequest, "host_port is required")
		return
	}

	protocol := firewall.ProtocolTCP
	if req.Protocol == "udp" {
		protocol = firewall.ProtocolUDP
	}

	if err := s.firewallMgr.UnexposePort(ctx, req.Instance, req.HostPort, protocol); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to unexpose port", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleListExposedPorts lists all exposed ports for an instance
func (s *Server) handleListExposedPorts(w http.ResponseWriter, r *http.Request, instance string) {
	ctx := r.Context()

	if s.firewallMgr == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Firewall manager not initialized")
		return
	}

	mappings, err := s.firewallMgr.ListExposedPorts(ctx, instance)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list exposed ports", err)
		return
	}

	s.writeJSON(w, http.StatusOK, mappings)
}

// NATRequest represents a request to setup NAT
type NATRequest struct {
	Instance      string `json:"instance"`
	Provider      string `json:"provider"`
	SourceNetwork string `json:"source_network"`
	OutInterface  string `json:"out_interface"`
}

// handleSetupNAT configures NAT for an instance
func (s *Server) handleSetupNAT(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.firewallMgr == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Firewall manager not initialized")
		return
	}

	var req NATRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if req.Instance == "" {
		s.writeError(w, http.StatusBadRequest, "Instance name is required")
		return
	}
	if req.SourceNetwork == "" {
		s.writeError(w, http.StatusBadRequest, "source_network is required")
		return
	}
	if req.OutInterface == "" {
		s.writeError(w, http.StatusBadRequest, "out_interface is required")
		return
	}

	if req.Provider == "" {
		req.Provider = "jail"
	}

	rule, err := s.firewallMgr.SetupNAT(ctx, req.Instance, req.Provider, req.SourceNetwork, req.OutInterface)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to setup NAT", err)
		return
	}

	s.writeJSON(w, http.StatusCreated, rule)
}

// RemoveNATRequest represents a request to remove NAT
type RemoveNATRequest struct {
	Instance string `json:"instance"`
}

// handleRemoveNAT removes NAT configuration for an instance
func (s *Server) handleRemoveNAT(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.firewallMgr == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Firewall manager not initialized")
		return
	}

	var req RemoveNATRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if req.Instance == "" {
		s.writeError(w, http.StatusBadRequest, "Instance name is required")
		return
	}

	if err := s.firewallMgr.RemoveNAT(ctx, req.Instance); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to remove NAT", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// resolveInstanceIP returns the address recorded for an instance, or "" when
// there is none to find.
//
// A DHCP guest is the case with none: the lease is handed out by dnsmasq and
// never reported back, so the spec still holds the literal "dhcp" it was
// created with. Treat that as unknown rather than passing it on as an address.
func (s *Server) resolveInstanceIP(ctx context.Context, name string) string {
	inst, err := s.datastore.GetInstanceByName(ctx, name)
	if err != nil || inst == nil {
		return ""
	}
	for i := range inst.Spec.Networks {
		n := &inst.Spec.Networks[i]
		if n.IPv4 == "" || n.IPv4 == "dhcp" {
			continue
		}
		// Specs store either a bare address or CIDR; the firewall wants the
		// address on its own.
		if idx := strings.Index(n.IPv4, "/"); idx >= 0 {
			return n.IPv4[:idx]
		}
		return n.IPv4
	}

	// Nothing declared: a guest that took its address from DHCP has none in the
	// spec. Ask the provider, which reads it from the leases or the ARP table,
	// rather than making the caller look it up and pass it in.
	prov, err := s.registry.Get(inst.Provider)
	if err != nil {
		return ""
	}
	addresses, ok := prov.(provider.InstanceAddressProvider)
	if !ok {
		return ""
	}
	ips, err := addresses.InstanceAddresses(ctx, inst.Handle)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return ips[0].String()
}
