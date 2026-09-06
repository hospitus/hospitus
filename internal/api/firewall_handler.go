package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/hospitus/hospitus/pkg/firewall"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
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

	// /api/v1/firewall/expose[/{instance}]
	//
	// Exactly four or five segments. ">= 4" accepted anything deeper, so a GET
	// to .../expose/web/extra listed the exposed ports of "web" for a resource
	// the caller never named — the same shape the interfaces, media, services
	// and volume routes were each fixed for.
	if (len(parts) == 4 || len(parts) == 5) && parts[3] == "expose" {
		switch r.Method {
		case http.MethodPost:
			// Four segments only. handleExposePort names the instance from the
			// body and ignores parts[4], so a POST to .../expose/web answered
			// 201 for a rule created against whatever the body said — not the
			// instance the caller had put in the path.
			if len(parts) != 4 {
				s.writeError(w, http.StatusNotFound, "Not found")
				return
			}
			s.handleExposePort(w, r)
		case http.MethodGet:
			// The one route that reads the fifth segment.
			if len(parts) == 5 {
				s.handleListExposedPorts(w, r, parts[4])
			} else {
				s.writeError(w, http.StatusBadRequest, "Instance name required")
			}
		case http.MethodDelete:
			if len(parts) != 4 {
				s.writeError(w, http.StatusNotFound, "Not found")
				return
			}
			s.handleUnexposePort(w, r)
		default:
			s.writeMethodNotAllowed(w, http.MethodGet, http.MethodPost, http.MethodDelete)
		}
		return
	}

	// /api/v1/firewall/nat — four segments and no more.
	if len(parts) == 4 && parts[3] == "nat" {
		switch r.Method {
		case http.MethodPost:
			s.handleSetupNAT(w, r)
		case http.MethodDelete:
			s.handleRemoveNAT(w, r)
		default:
			s.writeMethodNotAllowed(w, http.MethodPost, http.MethodDelete)
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
	// And it has to be a name. Only emptiness was refused, so a malformed one
	// reached the firewall manager and came back as 500 for a caller mistake.
	if err := validation.ValidateInstanceName(req.Instance); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid instance name", err)
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
		ip, unsupported, err := s.resolveInstanceIP(ctx, req.Instance)
		if err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Could not determine the instance address", err)
			return
		}
		if unsupported {
			s.writeError(w, http.StatusNotImplemented,
				"this provider cannot report an instance's addresses: pass target_ip "+
					"(hospitus <provider> expose add --target-ip)")
			return
		}
		req.TargetIP = ip
	}
	if req.TargetIP == "" {
		s.writeError(w, http.StatusBadRequest,
			"target_ip is required: this instance has no recorded address, which is "+
				"normal for a DHCP guest — pass the address it reports (hospitus <provider> "+
				"expose add --target-ip)")
		return
	}

	// Parsed here, not only inside PortMapping.Validate: a malformed address is
	// the caller's mistake, and reaching the manager for it turned a typo into
	// 500 Internal Server Error.
	if net.ParseIP(req.TargetIP) == nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("target_ip %q is not an IP address", req.TargetIP))
		return
	}

	protocol, err := parseFirewallProtocol(req.Protocol)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
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

// parseFirewallProtocol turns the request's protocol field into a rule.
//
// Only the exact string "udp" used to select UDP and everything else fell
// through to TCP, so "UDP", "sctp" and the typo "upd" all installed a TCP rule.
// On the unexpose side that silence was worse: removing "upd" reported success
// while looking for a TCP rule, and the UDP rule the caller meant to close
// stayed open. An empty field still means TCP, which is what every existing
// client relies on.
func parseFirewallProtocol(proto string) (firewall.Protocol, error) {
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "", "tcp":
		return firewall.ProtocolTCP, nil
	case "udp":
		return firewall.ProtocolUDP, nil
	default:
		return "", fmt.Errorf("unsupported protocol %q: use tcp or udp", proto)
	}
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
	// And it has to be a name. Only emptiness was refused, so a malformed one
	// reached the firewall manager and came back as 500 for a caller mistake.
	if err := validation.ValidateInstanceName(req.Instance); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid instance name", err)
		return
	}
	// The same range the expose side checks. Zero is outside it, so this also
	// covers the "required" case the bare == 0 test used to.
	if !validPort(req.HostPort) {
		s.writeError(w, http.StatusBadRequest, "host_port must be between 1 and 65535")
		return
	}

	protocol, err := parseFirewallProtocol(req.Protocol)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
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

	// The same check the four mutating handlers apply: a malformed name is the
	// caller's mistake, and reaching the manager for it turned a 400 into a 500.
	if err := validation.ValidateInstanceName(instance); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid instance name", err)
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
	// And it has to be a name. Only emptiness was refused, so a malformed one
	// reached the firewall manager and came back as 500 for a caller mistake.
	if err := validation.ValidateInstanceName(req.Instance); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid instance name", err)
		return
	}
	if req.SourceNetwork == "" {
		s.writeError(w, http.StatusBadRequest, "source_network is required")
		return
	}
	// A CIDR, checked here for the same reason the ports are: a malformed
	// value reached pf or ipfw and came back as an opaque 500.
	if _, _, err := net.ParseCIDR(req.SourceNetwork); err != nil {
		s.writeError(w, http.StatusBadRequest,
			fmt.Sprintf("source_network %q is not a CIDR block (e.g. 10.0.0.0/24)", req.SourceNetwork))
		return
	}
	if req.OutInterface == "" {
		s.writeError(w, http.StatusBadRequest, "out_interface is required")
		return
	}
	// Checked here, like the addresses above: the name goes into a pf or ipfw
	// rule, and a malformed one came back as an opaque 500 from the backend.
	if err := validation.ValidateInterfaceName(req.OutInterface); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid out_interface", err)
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
	// And it has to be a name. Only emptiness was refused, so a malformed one
	// reached the firewall manager and came back as 500 for a caller mistake.
	if err := validation.ValidateInstanceName(req.Instance); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid instance name", err)
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
func (s *Server) resolveInstanceIP(ctx context.Context, name string) (ip string, unsupported bool, err error) {
	inst, err := s.datastore.GetInstanceByName(ctx, name)
	if err != nil {
		// The daemon failed, not the caller: folding this into "no address"
		// answered 400 for a datastore that could not be read.
		return "", false, fmt.Errorf("could not look up instance %s: %w", name, err)
	}
	if inst == nil {
		return "", false, nil
	}
	for i := range inst.Spec.Networks {
		n := &inst.Spec.Networks[i]
		if n.IPv4 == "" || n.IPv4 == "dhcp" {
			continue
		}
		// Specs store either a bare address or CIDR; the firewall wants the
		// address on its own.
		if idx := strings.Index(n.IPv4, "/"); idx >= 0 {
			return n.IPv4[:idx], false, nil
		}
		return n.IPv4, false, nil
	}

	// Nothing declared: a guest that took its address from DHCP has none in the
	// spec. Ask the provider, which reads it from the leases or the ARP table,
	// rather than making the caller look it up and pass it in.
	prov, err := s.registry.Get(inst.Provider)
	if err != nil {
		return "", false, fmt.Errorf("provider %s is not registered: %w", inst.Provider, err)
	}
	// Reported separately: a provider that cannot answer at all is a capability
	// the daemon does not have, which is a 501 — not the caller's bad request.
	addresses, ok := prov.(provider.InstanceAddressProvider)
	if !ok {
		return "", true, nil
	}
	ips, err := addresses.InstanceAddresses(ctx, inst.Handle)
	if err != nil {
		return "", false, fmt.Errorf("could not read the addresses of %s: %w", name, err)
	}
	if len(ips) == 0 {
		// Genuinely no address, which is the caller's to supply.
		return "", false, nil
	}
	return ips[0].String(), false, nil
}
