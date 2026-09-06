package api

import (
	"fmt"
	"net/http"
)

// BridgeInfoResponse represents the API response for bridge information
type BridgeInfoResponse struct {
	Name      string   `json:"name"`
	Members   []string `json:"members"`
	IPAddress string   `json:"ip_address,omitempty"`
	MTU       int      `json:"mtu"`
	State     string   `json:"state"`
	MacAddr   string   `json:"mac_address,omitempty"`
}

func (s *Server) handleListBridges(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if s.bridges == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Network manager not available")
		return
	}

	bridges, err := s.bridges.ListBridges(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to list bridges: %v", err))
		return
	}

	// Convert to API format
	result := make([]BridgeInfoResponse, 0, len(bridges))
	for _, b := range bridges {
		result = append(result, BridgeInfoResponse{
			Name:      b.Name,
			Members:   b.Members,
			IPAddress: b.IPAddress,
			MTU:       b.MTU,
			State:     b.State,
			MacAddr:   b.MacAddress,
		})
	}

	s.writeJSON(w, http.StatusOK, result)
}
