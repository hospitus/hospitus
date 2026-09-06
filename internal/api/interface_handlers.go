package api

import (
	"fmt"
	"net/http"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/jail"
)

// NetworkInterfaceRequest contains parameters for adding a network interface
type NetworkInterfaceRequest struct {
	Name        string   `json:"name,omitempty"`
	Bridge      string   `json:"bridge"`
	IPv4Address string   `json:"ipv4_address,omitempty"`
	IPv4Gateway string   `json:"ipv4_gateway,omitempty"`
	IPv6Address string   `json:"ipv6_address,omitempty"`
	IPv6Gateway string   `json:"ipv6_gateway,omitempty"`
	DHCPv4      bool     `json:"dhcpv4,omitempty"`
	DHCPv6      bool     `json:"dhcpv6,omitempty"`
	SLAAC       bool     `json:"slaac,omitempty"`
	MTU         int      `json:"mtu,omitempty"`
	MAC         string   `json:"mac,omitempty"`
	DNSServers  []string `json:"dns_servers,omitempty"`
	Primary     bool     `json:"primary,omitempty"`
	Description string   `json:"description,omitempty"`
}

// NetworkInterfaceInfo contains information about a network interface
type NetworkInterfaceInfo struct {
	Name          string   `json:"name"`
	Bridge        string   `json:"bridge"`
	IPv4Address   string   `json:"ipv4_address,omitempty"`
	IPv4Gateway   string   `json:"ipv4_gateway,omitempty"`
	IPv6Address   string   `json:"ipv6_address,omitempty"`
	IPv6Gateway   string   `json:"ipv6_gateway,omitempty"`
	DHCPv4        bool     `json:"dhcpv4,omitempty"`
	DHCPv6        bool     `json:"dhcpv6,omitempty"`
	SLAAC         bool     `json:"slaac,omitempty"`
	MTU           int      `json:"mtu,omitempty"`
	MAC           string   `json:"mac,omitempty"`
	DNSServers    []string `json:"dns_servers,omitempty"`
	Primary       bool     `json:"primary"`
	Description   string   `json:"description,omitempty"`
	HostInterface string   `json:"host_interface,omitempty"`
	JailInterface string   `json:"jail_interface,omitempty"`
}

// handleInterfaces handles network interface operations for a jail.
//
// Routes:
//
//	GET /api/v1/instances/{id}/interfaces - List interfaces
//	POST /api/v1/instances/{id}/interfaces - Add interface
//	DELETE /api/v1/instances/{id}/interfaces/{name} - Remove interface
func (s *Server) handleInterfaces(w http.ResponseWriter, r *http.Request, instanceID string, parts []string) {
	ctx := r.Context()

	// Get instance
	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Interfaces only work with jail provider
	if instance.Provider != "jail" {
		s.writeError(w, http.StatusBadRequest, "Network interface management is only supported for jails")
		return
	}

	// Get jail provider
	prov, err := s.registry.Get("jail")
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Jail provider not available")
		return
	}

	jailProv, ok := prov.(*jail.JailProvider)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "Invalid jail provider")
		return
	}

	// Parse path: /api/v1/instances/{id}/interfaces[/{name}]
	interfaceName := ""
	if len(parts) >= 6 {
		interfaceName = parts[5]
	}

	switch {
	case interfaceName == "" && r.Method == http.MethodGet:
		s.handleListInterfaces(w, r, jailProv, instance.Handle)

	case interfaceName == "" && r.Method == http.MethodPost:
		s.handleAddInterface(w, r, jailProv, instance.Handle)

	case interfaceName != "" && r.Method == http.MethodDelete:
		s.handleRemoveInterface(w, r, jailProv, instance.Handle, interfaceName)

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) handleListInterfaces(w http.ResponseWriter, r *http.Request, jailProv *jail.JailProvider, handle provider.InstanceHandle) {
	ctx := r.Context()

	interfaces, err := jailProv.ListNetworkInterfaces(ctx, handle)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to list interfaces: %v", err))
		return
	}

	// Convert to API format. Use a non-nil slice so an empty list serializes as
	// [] rather than null.
	result := make([]NetworkInterfaceInfo, 0, len(interfaces))
	for i := range interfaces {
		result = append(result, networkInterfaceToInfo(interfaces[i]))
	}

	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleAddInterface(w http.ResponseWriter, r *http.Request, jailProv *jail.JailProvider, handle provider.InstanceHandle) {
	ctx := r.Context()

	var req NetworkInterfaceRequest
	if err := s.decodeJSONBody(r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	if req.Bridge == "" {
		s.writeError(w, http.StatusBadRequest, "Bridge is required")
		return
	}

	// Convert request to jail.NetworkInterface
	iface := jail.NetworkInterface{
		Name:        req.Name,
		Bridge:      req.Bridge,
		IPv4Address: req.IPv4Address,
		IPv4Gateway: req.IPv4Gateway,
		IPv6Address: req.IPv6Address,
		IPv6Gateway: req.IPv6Gateway,
		DHCPv4:      req.DHCPv4,
		DHCPv6:      req.DHCPv6,
		SLAAC:       req.SLAAC,
		MTU:         req.MTU,
		MAC:         req.MAC,
		DNSServers:  req.DNSServers,
		Primary:     req.Primary,
		Description: req.Description,
	}

	result, err := jailProv.AddNetworkInterface(ctx, handle, iface)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to add interface: %v", err))
		return
	}

	s.writeJSON(w, http.StatusCreated, networkInterfaceToInfo(*result))
}

func (s *Server) handleRemoveInterface(w http.ResponseWriter, r *http.Request, jailProv *jail.JailProvider, handle provider.InstanceHandle, interfaceName string) {
	ctx := r.Context()

	if err := jailProv.RemoveNetworkInterface(ctx, handle, interfaceName); err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to remove interface: %v", err))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// networkInterfaceToInfo converts jail.NetworkInterface to NetworkInterfaceInfo
func networkInterfaceToInfo(iface jail.NetworkInterface) NetworkInterfaceInfo {
	return NetworkInterfaceInfo{
		Name:          iface.Name,
		Bridge:        iface.Bridge,
		IPv4Address:   iface.IPv4Address,
		IPv4Gateway:   iface.IPv4Gateway,
		IPv6Address:   iface.IPv6Address,
		IPv6Gateway:   iface.IPv6Gateway,
		DHCPv4:        iface.DHCPv4,
		DHCPv6:        iface.DHCPv6,
		SLAAC:         iface.SLAAC,
		MTU:           iface.MTU,
		MAC:           iface.MAC,
		DNSServers:    iface.DNSServers,
		Primary:       iface.Primary,
		Description:   iface.Description,
		HostInterface: iface.HostInterface,
		JailInterface: iface.JailInterface,
	}
}
