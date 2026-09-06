package api

import (
	"fmt"
	"net/http"

	"github.com/hospitus/hospitus/pkg/provider"
)

// handleMedia handles media operations for VM instances.
//
// GET /api/v1/instances/{id}/media - List media devices
// POST /api/v1/instances/{id}/media - Insert media
// DELETE /api/v1/instances/{id}/media/{device} - Eject media
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request, instanceID string, parts []string) {
	ctx := r.Context()

	// Get instance from datastore
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

	// Check if provider supports media operations
	mediaProv, ok := prov.(provider.MediaProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, "Provider does not support media operations")
		return
	}

	// Route based on method and path
	switch r.Method {
	case http.MethodGet:
		// List media devices
		media, err := mediaProv.ListMedia(ctx, instance.Handle)
		if err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list media", err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"media": media,
		})

	case http.MethodPost:
		// Insert media
		var spec provider.MediaSpec
		if err := s.decodeJSONBody(w, r, &spec); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
			return
		}

		if err := mediaProv.InsertMedia(ctx, instance.Handle, spec); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to insert media", err)
			return
		}

		s.writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "Media inserted successfully",
		})

	case http.MethodDelete:
		// Eject media - device ID in path: /api/v1/instances/{id}/media/{device}
		if len(parts) < 6 {
			s.writeError(w, http.StatusBadRequest, "Device ID required for eject operation")
			return
		}
		deviceID := parts[5]

		if err := mediaProv.EjectMedia(ctx, instance.Handle, deviceID); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to eject media", err)
			return
		}

		s.writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": fmt.Sprintf("Media ejected from device %s", deviceID),
		})

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleBootOrder handles boot order operations for VM instances.
//
// GET /api/v1/instances/{id}/boot-order - Get boot order
// POST /api/v1/instances/{id}/boot-order - Set boot order
func (s *Server) handleBootOrder(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

	// Get instance from datastore
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

	// Check if provider supports media operations (boot order is part of MediaProvider)
	mediaProv, ok := prov.(provider.MediaProvider)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, "Provider does not support boot order operations")
		return
	}

	switch r.Method {
	case http.MethodGet:
		order, err := mediaProv.GetBootOrder(ctx, instance.Handle)
		if err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to get boot order", err)
			return
		}
		s.writeJSON(w, http.StatusOK, order)

	case http.MethodPost:
		// Set boot order
		var order provider.BootOrder
		if err := s.decodeJSONBody(w, r, &order); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
			return
		}

		if err := mediaProv.SetBootOrder(ctx, instance.Handle, order); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to set boot order", err)
			return
		}

		s.writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "Boot order updated",
		})

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
