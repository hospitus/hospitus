package api

import (
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
		// Logged, not returned: the underlying error names host interfaces and
		// ifconfig output, which an unauthenticated caller has no business
		// reading.
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list bridges", err)
		return
	}

	// Convert to API format
	result := make([]BridgeInfoResponse, 0, len(bridges))
	for _, b := range bridges {
		// Non-nil for the same reason the outer slice is: a bridge with no
		// members serialized "members": null, and a client iterating it
		// without a nil check failed on the one bridge that was empty.
		members := b.Members
		if members == nil {
			members = []string{}
		}
		result = append(result, BridgeInfoResponse{
			Name:      b.Name,
			Members:   members,
			IPAddress: b.IPAddress,
			MTU:       b.MTU,
			State:     b.State,
			MacAddr:   b.MacAddress,
		})
	}

	s.writeJSON(w, http.StatusOK, result)
}
