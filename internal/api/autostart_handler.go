package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
)

// This file contains handlers for auto-start functionality.

// AutoStartInstanceInfo represents auto-start information for an instance
type AutoStartInstanceInfo struct {
	ID        string                   `json:"id"`
	Provider  string                   `json:"provider"`
	AutoStart provider.AutoStartConfig `json:"autostart"`
}

// handleAutoStart handles GET /api/v1/autostart
// Returns list of all instances configured for auto-start across all providers
func (s *Server) handleAutoStart(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListAutoStart(w, r)
	case http.MethodPost:
		s.handleTriggerAutoStart(w, r)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleListAutoStart lists all instances configured for auto-start
func (s *Server) handleListAutoStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var allInstances []AutoStartInstanceInfo

	// Get all providers that support auto-start
	providerList := s.registry.List()
	for i := range providerList {
		info := &providerList[i]
		prov, err := s.registry.Get(info.Name)
		if err != nil {
			continue
		}

		// Check if provider implements AutoStartProvider
		autoStartProv, ok := prov.(provider.AutoStartProvider)
		if !ok {
			continue
		}

		// Get auto-start instances from this provider
		handles, err := autoStartProv.ListAutoStartInstances(ctx)
		if err != nil {
			s.logger.Warn("Failed to list auto-start instances for provider", logging.FieldProvider, info.Name, logging.FieldError, err)
			continue
		}

		for _, handle := range handles {
			config, err := autoStartProv.GetAutoStart(ctx, handle)
			if err != nil {
				s.logger.Warn("Failed to get auto-start config for instance", logging.FieldProvider, info.Name, logging.FieldInstance, handle.ID, logging.FieldError, err)
				continue
			}

			allInstances = append(allInstances, AutoStartInstanceInfo{
				ID:        handle.ID,
				Provider:  info.Name,
				AutoStart: *config,
			})
		}
	}

	// Sort by priority
	sort.Slice(allInstances, func(i, j int) bool {
		if allInstances[i].AutoStart.Priority != allInstances[j].AutoStart.Priority {
			return allInstances[i].AutoStart.Priority < allInstances[j].AutoStart.Priority
		}
		return allInstances[i].ID < allInstances[j].ID
	})

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"instances": allInstances,
		"count":     len(allInstances),
	})
}

// handleTriggerAutoStart triggers auto-start for all configured instances
func (s *Server) handleTriggerAutoStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var errors []string
	var started int

	// Get all providers that support auto-start
	providerList := s.registry.List()
	for i := range providerList {
		info := &providerList[i]
		prov, err := s.registry.Get(info.Name)
		if err != nil {
			continue
		}

		// Check if provider implements AutoStartProvider
		autoStartProv, ok := prov.(provider.AutoStartProvider)
		if !ok {
			continue
		}

		// Start auto-start instances for this provider
		// Counted before the call, and only added once it returns without
		// error: reading the list afterwards reported every configured
		// instance as started, whether or not it came up.
		handles, listErr := autoStartProv.ListAutoStartInstances(ctx)
		if listErr != nil {
			// Reported and left out of the count: a partial list would have
			// under- or over-stated what this provider started, and saying
			// nothing hid that the number is unreliable.
			errors = append(errors, fmt.Sprintf("%s: %v", info.Name, listErr))
			continue
		}

		if err := autoStartProv.StartAutoStartInstances(ctx); err != nil {
			errors = append(errors, err.Error())
			continue
		}
		started += len(handles)
	}

	response := map[string]interface{}{
		"started": started,
		"message": "Auto-start triggered",
	}

	if len(errors) > 0 {
		response["errors"] = errors
		s.writeJSON(w, http.StatusPartialContent, response)
		return
	}

	s.writeJSON(w, http.StatusOK, response)
}

// handleAutoStartInstance handles requests for a specific instance's auto-start config
// GET /api/v1/autostart/{provider}/{id} - Get auto-start config
// PUT /api/v1/autostart/{provider}/{id} - Set auto-start config
// DELETE /api/v1/autostart/{provider}/{id} - Disable auto-start
func (s *Server) handleAutoStartInstance(w http.ResponseWriter, r *http.Request) {
	// Parse path: /api/v1/autostart/{provider}/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/autostart/")
	parts := strings.SplitN(path, "/", 2)

	if len(parts) < 2 {
		s.writeError(w, http.StatusBadRequest, "Invalid path: expected /api/v1/autostart/{provider}/{id}")
		return
	}

	providerName := parts[0]
	instanceID := parts[1]

	// SplitN keeps everything after the provider name in parts[1], so
	// ".../autostart/mock/" gave an empty id and ".../autostart/mock/a/b" gave
	// "a/b" — both reached the provider as a handle the caller never named.
	if instanceID == "" || strings.Contains(instanceID, "/") {
		s.writeError(w, http.StatusBadRequest, "Invalid path: expected /api/v1/autostart/{provider}/{id}")
		return
	}

	prov, err := s.registry.Get(providerName)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "Provider not found: "+providerName)
		return
	}

	// Check if provider implements AutoStartProvider
	autoStartProv, ok := prov.(provider.AutoStartProvider)
	if !ok {
		// "Provider does not support <feature>" is 501 across the API surface
		// (snapshots, clone, exec, checkpoints, export/import, ...).
		s.writeError(w, http.StatusNotImplemented, "Provider does not support auto-start: "+providerName)
		return
	}

	handle := provider.InstanceHandle{
		ID:       instanceID,
		Provider: providerName,
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGetAutoStart(w, r, autoStartProv, handle)
	case http.MethodPut:
		s.handleSetAutoStart(w, r, autoStartProv, handle)
	case http.MethodDelete:
		s.handleDisableAutoStart(w, r, autoStartProv, handle)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleGetAutoStart gets the auto-start configuration for an instance
func (s *Server) handleGetAutoStart(w http.ResponseWriter, r *http.Request, prov provider.AutoStartProvider, handle provider.InstanceHandle) {
	ctx := r.Context()

	config, err := prov.GetAutoStart(ctx, handle)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Failed to get auto-start config", err)
		return
	}

	s.writeJSON(w, http.StatusOK, AutoStartInstanceInfo{
		ID:        handle.ID,
		Provider:  handle.Provider,
		AutoStart: *config,
	})
}

// handleSetAutoStart sets the auto-start configuration for an instance
func (s *Server) handleSetAutoStart(w http.ResponseWriter, r *http.Request, prov provider.AutoStartProvider, handle provider.InstanceHandle) {
	ctx := r.Context()

	var config provider.AutoStartConfig
	if err := s.decodeJSONBody(w, r, &config); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if err := prov.SetAutoStart(ctx, handle, config); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to set auto-start config", err)
		return
	}

	// Same shape as the GET: the CLI reads the answer back to confirm what it
	// stored, and a {message, config} body left it printing an empty name and
	// zeroes for a priority and delay that had been set.
	s.writeJSON(w, http.StatusOK, AutoStartInstanceInfo{
		ID:        handle.ID,
		Provider:  handle.Provider,
		AutoStart: config,
	})
}

// handleDisableAutoStart disables auto-start for an instance
func (s *Server) handleDisableAutoStart(w http.ResponseWriter, r *http.Request, prov provider.AutoStartProvider, handle provider.InstanceHandle) {
	ctx := r.Context()

	// Set enabled to false
	config := provider.AutoStartConfig{
		Enabled:  false,
		Priority: 50,
		DelayMS:  0,
	}

	if err := prov.SetAutoStart(ctx, handle, config); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to disable auto-start", err)
		return
	}

	// The same shape the GET and the set return, not a bare message: a client
	// that reads the response to refresh its view got nothing to read from
	// this one route.
	s.writeJSON(w, http.StatusOK, AutoStartInstanceInfo{
		ID:        handle.ID,
		Provider:  handle.Provider,
		AutoStart: config,
	})
}

// StartAutoStartInstances starts all instances configured for auto-start
// This is called when the server starts
func (s *Server) StartAutoStartInstances() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	s.logger.Info("Starting auto-start instances")

	// Get all providers that support auto-start
	providerList := s.registry.List()
	for i := range providerList {
		info := &providerList[i]
		prov, err := s.registry.Get(info.Name)
		if err != nil {
			continue
		}

		// Check if provider implements AutoStartProvider
		autoStartProv, ok := prov.(provider.AutoStartProvider)
		if !ok {
			continue
		}

		// Get count of auto-start instances
		handles, err := autoStartProv.ListAutoStartInstances(ctx)
		if err != nil {
			s.logger.Warn("Failed to list auto-start instances for provider", logging.FieldProvider, info.Name, logging.FieldError, err)
			continue
		}

		if len(handles) == 0 {
			continue
		}

		s.logger.Info("Starting auto-start instances for provider", logging.FieldProvider, info.Name, "count", len(handles))

		// Start auto-start instances for this provider
		if err := autoStartProv.StartAutoStartInstances(ctx); err != nil {
			s.logger.Warn("Auto-start instances failed for provider",
				logging.FieldProvider, info.Name,
				logging.FieldError, err)
		}
	}

	// Re-synchronize states to update datastore with successfully started instances
	s.SyncInstanceStates()

	return nil
}
