package api

import (
	"fmt"
	"net/http"
)

// handleProviders lists all registered providers.
//
// GET /api/v1/providers
//
// Response: Array of provider info
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	providers := s.registry.List()

	s.writeJSON(w, http.StatusOK, providers)
}

// handleProviderDetail returns details for a specific provider.
//
// GET /api/v1/providers/{name}
//
// Response: Provider metadata and capabilities
func (s *Server) handleProviderDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	// Extract provider name from path parameter
	providerName := r.PathValue("name")
	if providerName == "" {
		s.writeError(w, http.StatusBadRequest, "Invalid path: provider name cannot be empty")
		return
	}

	prov, err := s.registry.Get(providerName)
	if err != nil {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("Provider not found: %s", providerName))
		return
	}

	// Get provider metadata and capabilities
	metadata := prov.Metadata()
	capabilities := prov.Capabilities()

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"metadata":     metadata,
		"capabilities": capabilities,
		"available":    s.registry.IsAvailable(providerName),
	})
}
