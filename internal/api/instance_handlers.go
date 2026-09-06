package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/internal/security"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// handleInstances dispatches instance collection requests.
//
// GET /api/v1/instances - List instances
// POST /api/v1/instances - Create instance
func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListInstances(w, r)
	case http.MethodPost:
		s.handleCreateInstance(w, r)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleListInstances lists instances with optional filtering.
//
// GET /api/v1/instances?provider=qemu&state=running
//
// Query Parameters:
//   - provider: Filter by provider name
//   - state: Filter by state (can specify multiple)
//   - label: Filter by label (format: key=value)
func (s *Server) handleListInstances(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse query parameters
	filter := datastore.InstanceFilter{
		Provider: r.URL.Query().Get("provider"),
	}

	// SECURITY: Validate provider name
	if filter.Provider != "" {
		if err := validation.ValidateProviderName(filter.Provider); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid provider", err)
			return
		}
	}

	// Parse state filters
	if states := r.URL.Query()["state"]; len(states) > 0 {
		// SECURITY: Limit number of state filters to prevent DoS
		if len(states) > 20 {
			s.writeError(w, http.StatusBadRequest, "Too many state filters (max 20)")
			return
		}
		validStates := map[string]provider.InstanceState{
			"running":  provider.StateRunning,
			"stopped":  provider.StateStopped,
			"starting": provider.StateStarting,
			"stopping": provider.StateStopping,
			"creating": provider.StateCreating,
			"deleting": provider.StateDeleting,
			"paused":   provider.StatePaused,
			"error":    provider.StateError,
		}
		for _, state := range states {
			if st, ok := validStates[state]; ok {
				filter.States = append(filter.States, st)
			} else {
				s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid state filter: %s", state))
				return
			}
		}
	}

	// Parse label filters
	if labels := r.URL.Query()["label"]; len(labels) > 0 {
		// SECURITY: Limit number of label filters to prevent DoS
		if len(labels) > 50 {
			s.writeError(w, http.StatusBadRequest, "Too many label filters (max 50)")
			return
		}
		filter.Labels = make(map[string]string)
		for _, label := range labels {
			parts := strings.SplitN(label, "=", 2)
			if len(parts) != 2 || parts[0] == "" {
				s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid label filter: %s (expected key=value)", label))
				return
			}
			if err := validation.ValidateLabel(parts[0], parts[1]); err != nil {
				s.writeLoggedError(w, http.StatusBadRequest, "Invalid label", err)
				return
			}
			filter.Labels[parts[0]] = parts[1]
		}
	}

	// Query datastore
	instances, err := s.datastore.ListInstances(ctx, filter)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list instances", err)
		return
	}

	// Ask each provider what its instance is doing, as the single-instance
	// handler does. The stored state is only as fresh as the last operation
	// hospitus itself performed, and an instance can stop without being told to:
	// a container whose process exits is reported "exited" by podman, "stopped"
	// by "hospitus podman info", and "running" by "hospitus podman list" — the one
	// place an operator looks first.
	for i := range instances {
		prov, err := s.registry.Get(instances[i].Provider)
		if err != nil {
			s.logger.Warn("No provider to ask about this instance; listing its stored state",
				logging.FieldInstance, instances[i].Name, logging.FieldProvider, instances[i].Provider,
				logging.FieldError, err)
			continue
		}
		state, err := prov.GetInstanceState(ctx, instances[i].Handle)
		if err != nil {
			// Leave the stored state rather than inventing one. A provider
			// that cannot answer says nothing about whether the instance runs.
			// Say so: silence here makes a stale "running" impossible to
			// account for, since the list looks the same either way.
			s.logger.Warn("Provider could not report the state; listing the stored one",
				logging.FieldInstance, instances[i].Name, logging.FieldProvider, instances[i].Provider,
				logging.FieldError, err)
			continue
		}
		instances[i].State = state
		s.askProviderForAddress(ctx, prov, instances[i])
	}

	s.writeJSON(w, http.StatusOK, instances)
}

// privilegedProviderConfig names a provider-config construct that gives its
// caller authority over the host, or "" when the config carries none.
//
// Each of these is acted on by the daemon, as root, outside any jail: exec.start
// and exec.stop are commands, path is the jail's own root directory, mount.fstab
// is a list of mounts jail(8) performs, and a mounts entry with a host_path is a
// nullfs mount of any directory on the host. A caller who sets one can read and
// write the host filesystem — more than authority over instances, which is what
// the separate exec and admin permissions exist to keep apart.
func privilegedProviderConfig(config map[string]interface{}) string {
	for key := range config {
		if privilegedProviderConfigKey(key) {
			return key
		}
	}

	// A request decoded from JSON carries []interface{}; a stack instance
	// converted in Go carries []map[string]interface{}. Reading only the
	// first shape let every stack mount through unchecked.
	for _, mount := range mountEntries(config["mounts"]) {
		if hostPath, _ := mount["host_path"].(string); hostPath != "" {
			return "a mount from " + hostPath
		}
	}
	return ""
}

// mountEntries normalises the two shapes a "mounts" provider-config value
// arrives in.
func mountEntries(value interface{}) []map[string]interface{} {
	switch mounts := value.(type) {
	case []map[string]interface{}:
		return mounts
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(mounts))
		for _, m := range mounts {
			if mount, ok := m.(map[string]interface{}); ok {
				out = append(out, mount)
			}
		}
		return out
	default:
		return nil
	}
}

// privilegedProviderConfigKey reports whether one provider_config key hands the
// caller host authority.
//
// jail(8) runs every exec.* hook on the host as root, not only exec.start, and
// exec.consolelog names a host file to append to. path and mount.fstab name
// host paths; devfs_ruleset, securelevel, enforce_statfs and allow.mount*
// weaken the jail boundary itself.
func privilegedProviderConfigKey(key string) bool {
	switch key {
	case "exec.clean", "exec.timeout", "exec.fib", "exec.jail_user":
		return false
	case "path", "mount", "mount.fstab", "devfs_ruleset", "securelevel", "enforce_statfs", "allow.vmm":
		return true
	}
	return strings.HasPrefix(key, "exec.") || strings.HasPrefix(key, "allow.mount")
}

// reservedHandleMetadata lists the keys providers read back from the handle to
// find what they operate on: the dataset a jail lives on, the directory a QEMU
// VM is removed with, the socket its QMP commands go to. A PATCH that merged
// one of them in would point the next delete at any path on the host.
var reservedHandleMetadata = []string{"zfs_dataset", "vm_dir", "qmp_socket", "mountpoint", "disk_path", "vnet_epair", "os_release"}

// refusePrivilegedConfig reports whether the request must stop because it asks
// for host authority with a key that does not hold it.
//
// A request that never passed through authentication — a daemon started with
// --allow-no-auth — carries no permissions and is not constrained here: that
// mode is insecure by declaration, and the route middleware has already let it
// through.
func (s *Server) refusePrivilegedConfig(w http.ResponseWriter, r *http.Request, config map[string]interface{}) bool {
	construct := privilegedProviderConfig(config)
	if construct == "" {
		return false
	}
	perms, authenticated := permissionsFromContext(r.Context())
	if !authenticated || hasPerm(perms, "admin") {
		return false
	}
	security.GetGlobalAuditLogger().LogAuthFailure(s.extractClientIP(r),
		"privileged provider config without admin: "+construct)
	s.writeError(w, http.StatusForbidden,
		fmt.Sprintf("%s gives the caller the host filesystem and needs an admin key", construct))
	return true
}

// discardCreatedInstance deletes an instance the provider made but the datastore
// could not record.
//
// A record that is not written leaves a jail, VM or container on the host that
// no API call reaches: it does not appear in a listing, cannot be stopped, and
// holds its name against the next attempt. The provider resource follows the
// record it was created for.
func (s *Server) discardCreatedInstance(ctx context.Context, prov provider.Provider, handle provider.InstanceHandle, cause error) error {
	// The request's context may already be done — a client that gave up is one
	// way the write fails — so the cleanup gets a context of its own.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()

	if delErr := prov.DeleteInstance(cleanupCtx, handle, true); delErr != nil {
		s.logger.Error("Instance created but neither recorded nor removed",
			logging.FieldInstance, handle.ID, logging.FieldError, delErr)
		return errors.Join(cause, fmt.Errorf("the instance could not be removed either: %w", delErr))
	}
	return cause
}

// handleCreateInstance creates a new instance.
//
// POST /api/v1/instances
//
// Request Body: InstanceSpec (JSON)
// Response: Created instance
func (s *Server) handleCreateInstance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse request body
	var spec provider.InstanceSpec
	if err := s.decodeJSONBody(r, &spec); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if err := validation.ValidateInstanceSpec(spec); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid instance spec", err)
		return
	}

	if s.refusePrivilegedConfig(w, r, spec.ProviderConfig) {
		return
	}

	// Validate spec
	if spec.Name == "" {
		s.writeError(w, http.StatusBadRequest, "Instance name is required")
		return
	}

	// Determine provider (from spec or query parameter)
	providerName := spec.Labels["provider"]
	if providerName == "" {
		providerName = r.URL.Query().Get("provider")
	}
	if providerName == "" {
		s.writeError(w, http.StatusBadRequest, "Provider must be specified")
		return
	}

	prov, err := s.registry.Get(providerName)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("Provider not found: %s", providerName))
		return
	}

	// Check for ghost instance (exists in DB but not on disk)
	existing, err := s.datastore.GetInstanceByName(ctx, spec.Name)
	if err != nil {
		s.logger.Warn("Failed to check for existing instance", logging.FieldInstance, spec.Name, logging.FieldError, err)
	}
	if existing != nil {
		// Ask the provider that owns the existing record, not the one this
		// request names. A jail called "web" asked about by the qemu provider
		// answers "unknown", and the record and firewall rules of a jail that is
		// running perfectly well are then deleted as a ghost's.
		existingProv, provErr := s.registry.Get(existing.Provider)
		if provErr != nil {
			s.writeError(w, http.StatusConflict,
				fmt.Sprintf("Instance %s already exists under provider %s", spec.Name, existing.Provider))
			return
		}
		state, stateErr := existingProv.GetInstanceState(ctx, existing.Handle)
		if stateErr != nil || state == provider.StateUnknown {
			// Ghost instance - clean it up
			s.logger.Warn("Cleaning up ghost instance from datastore", logging.FieldInstance, spec.Name, "state", state, "state_err", stateErr)
			if delErr := s.datastore.DeleteInstance(ctx, existing.ID); delErr != nil {
				s.logger.Warn("Failed to delete ghost instance", logging.FieldInstance, spec.Name, logging.FieldError, delErr)
			}

			if fwErr := s.cleanupFirewallRules(ctx, existing.Name); fwErr != nil {
				s.logger.Warn("Failed to cleanup ghost firewall rules", logging.FieldInstance, existing.Name, logging.FieldError, fwErr)
			}
		} else {
			// Real instance exists
			s.writeError(w, http.StatusConflict, fmt.Sprintf("Instance %s already exists", spec.Name))
			return
		}
	}

	// Check if client wants streaming output (for progress messages)
	acceptHeader := r.Header.Get("Accept")
	wantsStream := strings.Contains(acceptHeader, "text/plain") || strings.Contains(acceptHeader, "text/event-stream")

	if wantsStream {
		// Disable write deadline for streaming (instance creation can take minutes for large images)
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Time{}); err != nil {
			s.logger.Warn("Failed to disable write deadline for streaming create request", logging.FieldInstance, spec.Name, logging.FieldProvider, providerName, logging.FieldError, err)
		}

		// Stream creation progress
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "ERROR: Streaming not supported\n")
			return
		}

		streamCtx, stream, stopStreaming := s.withStreamingLogger(ctx, w, flusher, "instance.create", spec.Name, providerName)
		defer stopStreaming()

		// Create instance via provider
		stream.Printf("Creating instance %s with provider %s...\n", spec.Name, providerName)

		handle, err := prov.CreateInstance(streamCtx, spec)
		// The provider has finished logging: drain its lines before any terminal
		// line so the client reads them in order.
		stopStreaming()
		if err != nil {
			stream.Printf("ERROR: Failed to create instance: %v\n", err)
			return
		}

		// Store in datastore
		instance := &datastore.Instance{
			ID:          handle.ID,
			Name:        spec.Name,
			Provider:    providerName,
			State:       provider.StateStopped,
			Spec:        spec,
			Handle:      handle,
			Labels:      spec.Labels,
			Annotations: spec.Annotations,
		}

		if err := s.datastore.CreateInstance(ctx, instance); err != nil {
			err = s.discardCreatedInstance(ctx, prov, handle, err)
			stream.Printf("ERROR: Failed to store instance: %v\n", err)
			return
		}

		// Send success with JSON
		jsonData, _ := json.Marshal(instance)
		stream.Printf("SUCCESS: %s\n", string(jsonData))
	} else {
		// Traditional JSON response
		// Create instance via provider
		handle, err := prov.CreateInstance(ctx, spec)
		if err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to create instance", err)
			return
		}

		// Store in datastore
		instance := &datastore.Instance{
			ID:          handle.ID,
			Name:        spec.Name,
			Provider:    providerName,
			State:       provider.StateStopped,
			Spec:        spec,
			Handle:      handle,
			Labels:      spec.Labels,
			Annotations: spec.Annotations,
		}

		if err := s.datastore.CreateInstance(ctx, instance); err != nil {
			err = s.discardCreatedInstance(ctx, prov, handle, err)
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to store instance", err)
			return
		}

		s.writeJSON(w, http.StatusCreated, instance)
	}
}

// handleInstanceDetail handles operations on a specific instance.
//
// GET /api/v1/instances/{id} - Get instance details
// DELETE /api/v1/instances/{id} - Delete instance
// POST /api/v1/instances/{id}/start - Start instance
// POST /api/v1/instances/{id}/stop - Stop instance
// POST /api/v1/instances/{id}/restart - Restart instance
func (s *Server) handleInstanceDetail(w http.ResponseWriter, r *http.Request) {
	// Extract instance ID from path parameter (Go 1.22+)
	instanceID := r.PathValue("id")
	if instanceID == "" {
		s.writeError(w, http.StatusBadRequest, "Invalid path: instance ID cannot be empty")
		return
	}

	// Extract action and remaining path parts from URL suffix.
	// Go 1.22+ routing handles common actions via explicit routes,
	// but this handler also serves as a catch-all for less common sub-paths.
	action := ""
	var parts []string
	if strings.HasPrefix(r.URL.Path, "/api/v1/instances/"+instanceID+"/") {
		suffix := strings.TrimPrefix(r.URL.Path, "/api/v1/instances/"+instanceID+"/")
		parts = strings.Split(suffix, "/")
		if len(parts) > 0 && parts[0] != "" {
			action = parts[0]
		}
	}

	// Route to appropriate handler
	switch {
	case action == "" && r.Method == http.MethodGet:
		s.handleGetInstance(w, r, instanceID)
	case action == "" && r.Method == http.MethodPatch:
		s.handleUpdateInstance(w, r, instanceID)
	case action == "" && r.Method == http.MethodDelete:
		s.handleDeleteInstance(w, r, instanceID)
	case action == "start" && r.Method == http.MethodPost:
		s.handleStartInstance(w, r, instanceID)
	case action == "stop" && r.Method == http.MethodPost:
		s.handleStopInstance(w, r, instanceID)
	case action == "restart" && r.Method == http.MethodPost:
		s.handleRestartInstance(w, r, instanceID)
	case action == "events" && r.Method == http.MethodGet:
		s.handleGetInstanceEvents(w, r, instanceID)
	case action == "snapshots":
		// Handle snapshot operations
		s.handleSnapshots(w, r, instanceID, parts)
	case action == "clone":
		// Handle clone operations
		s.handleClone(w, r, instanceID, parts)
	case action == "freebsd":
		// Handle FreeBSD-specific features (rctl, VNET)
		s.handleFreeBSD(w, r, instanceID, parts)
	case action == "export":
		// Handle export operations
		s.handleExport(w, r, instanceID)
	case action == "exec" && r.Method == http.MethodPost:
		// Handle exec operations
		s.handleExec(w, r, instanceID)
	case action == "console" && r.Method == http.MethodGet:
		// Handle console info or WebSocket upgrade
		if strings.HasSuffix(r.URL.Path, "/ws") || r.Header.Get("Upgrade") == "websocket" {
			s.handleWebSocketConsole(w, r, instanceID)
		} else {
			s.handleConsoleInfo(w, r, instanceID)
		}
	case action == "metrics" && r.Method == http.MethodGet:
		s.handleInstanceMetrics(w, r, instanceID)
	case action == "health" && r.Method == http.MethodGet:
		// Handle health check
		s.handleInstanceHealth(w, r, instanceID)
	case action == "media":
		// Handle media operations (insert, eject, list)
		s.handleMedia(w, r, instanceID, parts)
	case action == "boot-order":
		// Handle boot order operations
		s.handleBootOrder(w, r, instanceID)
	case action == "services":
		// Handle service management operations
		s.handleServices(w, r, instanceID, parts)
	case action == "volumes":
		// Handle volume attachment operations
		s.handleInstanceVolumes(w, r, instanceID, parts)
	case action == "interfaces":
		// Handle network interface operations
		s.handleInterfaces(w, r, instanceID, parts)
	case action == "backups":
		// Handle backup operations
		s.handleInstanceBackups(w, r, instanceID, parts)
	case action == "checkpoint":
		// Handle checkpoint create/restore/delete
		s.handleCheckpoint(w, r, instanceID, parts)
	case action == "checkpoints":
		s.handleCheckpointList(w, r, instanceID)
	case action == "pause" && r.Method == http.MethodPost:
		s.handlePauseInstance(w, r, instanceID)
	case action == "resume" && r.Method == http.MethodPost:
		s.handleResumeInstance(w, r, instanceID)
	case action == "rename" && r.Method == http.MethodPost:
		s.handleRenameInstance(w, r, instanceID)
	case action == "upgrade" && r.Method == http.MethodPost:
		s.handleUpgradeInstance(w, r, instanceID)
	default:
		s.writeError(w, http.StatusNotFound, "Not found")
	}
}

// --- Explicit route wrappers (Go 1.22+ path parameters) ---
// These thin wrappers extract path values and delegate to the existing handler methods.

func (s *Server) handleStartInstanceRoute(w http.ResponseWriter, r *http.Request) {
	s.handleStartInstance(w, r, r.PathValue("id"))
}

func (s *Server) handleStopInstanceRoute(w http.ResponseWriter, r *http.Request) {
	s.handleStopInstance(w, r, r.PathValue("id"))
}

func (s *Server) handleRestartInstanceRoute(w http.ResponseWriter, r *http.Request) {
	s.handleRestartInstance(w, r, r.PathValue("id"))
}

func (s *Server) handleGetInstanceEventsRoute(w http.ResponseWriter, r *http.Request) {
	s.handleGetInstanceEvents(w, r, r.PathValue("id"))
}

func (s *Server) handleInstanceHealthRoute(w http.ResponseWriter, r *http.Request) {
	s.handleInstanceHealth(w, r, r.PathValue("id"))
}

func (s *Server) handleExecRoute(w http.ResponseWriter, r *http.Request) {
	s.handleExec(w, r, r.PathValue("id"))
}

func (s *Server) handleInstanceMetricsRoute(w http.ResponseWriter, r *http.Request) {
	s.handleInstanceMetrics(w, r, r.PathValue("id"))
}

func (s *Server) handleInterfacesRoute(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")
	parts := s.extractRouteParts(r, instanceID)
	s.handleInterfaces(w, r, instanceID, parts)
}

func (s *Server) handleServicesRoute(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")
	parts := s.extractRouteParts(r, instanceID)
	s.handleServices(w, r, instanceID, parts)
}

func (s *Server) handleFreeBSDRoute(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")
	parts := s.extractRouteParts(r, instanceID)
	s.handleFreeBSD(w, r, instanceID, parts)
}

func (s *Server) handleMediaRoute(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")
	parts := s.extractRouteParts(r, instanceID)
	s.handleMedia(w, r, instanceID, parts)
}

func (s *Server) handleBootOrderRoute(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("id")
	s.handleBootOrder(w, r, instanceID)
}

// extractRouteParts splits the URL path into parts relative to the instance sub-path.
// For a path like /api/v1/instances/{id}/media/{device}, it returns ["api","v1","instances","{id}","media","{device}"]
// so handlers can index into parts consistently with the catch-all handler pattern.
func (s *Server) extractRouteParts(r *http.Request, instanceID string) []string {
	suffix := strings.TrimPrefix(r.URL.Path, "/api/v1/instances/"+instanceID+"/")
	parts := strings.Split(suffix, "/")
	// Prepend the full prefix parts for consistent indexing
	return append([]string{"api", "v1", "instances", instanceID}, parts...)
}

// lookupInstance finds an instance by ID or name.
// It first tries to look up by ID, then falls back to looking up by name.
func (s *Server) lookupInstance(ctx context.Context, idOrName string) (*datastore.Instance, error) {
	// First try by ID
	instance, err := s.datastore.GetInstance(ctx, idOrName)
	if err == nil && instance != nil {
		return instance, nil
	}

	// Fall back to name lookup
	instance, err = s.datastore.GetInstanceByName(ctx, idOrName)
	if err != nil || instance == nil {
		return nil, fmt.Errorf("instance not found: %s", idOrName)
	}

	return instance, nil
}

// handleGetInstance returns instance details.
func (s *Server) handleGetInstance(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	s.refreshFromProvider(ctx, instance)

	s.writeJSON(w, http.StatusOK, instance)
}

// refreshFromProvider replaces the stored state and address with what the
// provider reports.
//
// The stored spec is only as fresh as the last operation hospitus performed, and
// an address handed out by DHCP is never in it. Each provider works one out —
// bhyve from the dnsmasq leases, the jail from its interfaces — and this is
// where that answer reaches the instance an operator asks about.
func (s *Server) refreshFromProvider(ctx context.Context, instance *datastore.Instance) {
	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		return
	}
	info, err := prov.GetInstanceInfo(ctx, instance.Handle)
	if err != nil {
		// Fall back to the state alone, which fewer providers can fail to give.
		if state, stateErr := prov.GetInstanceState(ctx, instance.Handle); stateErr == nil {
			instance.State = state
		}
		return
	}

	// Not every provider fills State on the info it returns, and an empty one
	// must not overwrite what the datastore knows.
	if info.State != "" {
		instance.State = info.State
	} else if state, stateErr := prov.GetInstanceState(ctx, instance.Handle); stateErr == nil {
		instance.State = state
	}

	if len(info.IPAddresses) > 0 {
		fillFirstEmptyAddress(instance, info.IPAddresses)
		return
	}

	// Podman reports no address on the info it returns, so ask for it the way
	// the instance list does.
	s.askProviderForAddress(ctx, prov, instance)
}

// askProviderForAddress fills in an address the stored spec cannot hold.
//
// It uses the optional InstanceAddressProvider, which asks for the address
// alone: the instance list cannot afford a full GetInstanceInfo per row, which
// for bhyve reads a zfs list per disk. A provider that does not implement it
// leaves the list showing what was declared at creation.
//
// Only a running instance is asked: a DHCP lease and an ARP entry both outlive
// the instance that held them.
func (s *Server) askProviderForAddress(ctx context.Context, prov provider.Provider, instance *datastore.Instance) {
	if instance.State != provider.StateRunning {
		return
	}
	addresses, ok := prov.(provider.InstanceAddressProvider)
	if !ok {
		return
	}
	ips, err := addresses.InstanceAddresses(ctx, instance.Handle)
	if err != nil {
		s.logger.Debug("provider could not report an address",
			logging.FieldInstance, instance.Name, logging.FieldProvider, instance.Provider,
			logging.FieldError, err)
		return
	}
	fillFirstEmptyAddress(instance, ips)
}

// fillFirstEmptyAddress puts the reported address on the first network that
// declares none, leaving a static address as it was written.
//
// An instance may declare no network at all — a podman container joins podman's
// own bridge without anything being asked for at creation — and then there is
// no slot to fill. The address is still what the instance holds, so it gets an
// entry of its own rather than being dropped.
func fillFirstEmptyAddress(instance *datastore.Instance, ips []net.IP) {
	if len(ips) == 0 {
		return
	}
	for i := range instance.Spec.Networks {
		if instance.Spec.Networks[i].IPv4 == "" {
			instance.Spec.Networks[i].IPv4 = ips[0].String()
			return
		}
	}
	if len(instance.Spec.Networks) == 0 {
		instance.Spec.Networks = []provider.NetworkSpec{{IPv4: ips[0].String()}}
	}
}

// handleUpdateInstance updates an instance's configuration.
func (s *Server) handleUpdateInstance(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

	// Get existing instance
	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	// Parse update request
	var req struct {
		Spec           *provider.InstanceSpec `json:"spec,omitempty"`
		ProviderConfig map[string]interface{} `json:"provider_config,omitempty"`
	}
	if err := s.decodeJSONBody(r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// Update spec if provided
	if req.Spec != nil {
		// Merge provided spec with existing (only override non-zero values)
		if req.Spec.CPUs > 0 {
			instance.Spec.CPUs = req.Spec.CPUs
		}
		if req.Spec.MemoryMB > 0 {
			instance.Spec.MemoryMB = req.Spec.MemoryMB
		}
		if req.Spec.Description != "" {
			instance.Spec.Description = req.Spec.Description
		}
	}

	// Update provider config if provided
	// SECURITY: Validate provider config keys and values before merging
	if req.ProviderConfig != nil {
		for k, v := range req.ProviderConfig {
			if err := validation.ValidateProviderConfigEntry(k, v); err != nil {
				s.writeLoggedError(w, http.StatusBadRequest, fmt.Sprintf("Invalid provider config %q", k), err)
				return
			}
		}
		for _, reserved := range reservedHandleMetadata {
			if _, ok := req.ProviderConfig[reserved]; ok {
				s.writeError(w, http.StatusBadRequest, fmt.Sprintf("provider config %q is managed by the provider and cannot be set", reserved))
				return
			}
		}
		if s.refusePrivilegedConfig(w, r, req.ProviderConfig) {
			return
		}
		if instance.Handle.Metadata != nil {
			for k, v := range req.ProviderConfig {
				instance.Handle.Metadata[k] = v
			}
		} else {
			instance.Handle.Metadata = req.ProviderConfig
		}
	}

	// Save updated instance spec to datastore. lookupInstance resolves by ID or
	// name, so the datastore writes must use the resolved ID, never the raw
	// path value.
	if err := s.datastore.UpdateInstanceSpec(ctx, instance.ID, instance.Spec); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to update instance spec", err)
		return
	}

	// Save updated handle (with metadata) to datastore
	if req.ProviderConfig != nil {
		if err := s.datastore.UpdateInstanceHandle(ctx, instance.ID, instance.Handle); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to update instance handle", err)
			return
		}
	}

	// Hand the change to the provider. One that keeps its own configuration
	// reads that, not the datastore, when it next starts the instance, so a
	// change recorded here alone is reported as applied and never arrives.
	//
	// The request's provider config is merged in explicitly: above it goes into
	// the handle's metadata, while the spec's own ProviderConfig still holds
	// what the instance was created with.
	applied := instance.Spec
	if len(req.ProviderConfig) > 0 {
		merged := make(map[string]interface{}, len(instance.Spec.ProviderConfig)+len(req.ProviderConfig))
		for k, v := range instance.Spec.ProviderConfig {
			merged[k] = v
		}
		for k, v := range req.ProviderConfig {
			merged[k] = v
		}
		applied.ProviderConfig = merged
	}
	if prov, err := s.registry.Get(instance.Provider); err == nil {
		if reconfigurable, ok := prov.(provider.ReconfigureProvider); ok {
			if err := reconfigurable.Reconfigure(ctx, instance.Handle, applied); err != nil {
				s.writeLoggedError(w, http.StatusInternalServerError,
					"Recorded the change but the provider could not apply it", err)
				return
			}
		} else if req.Spec != nil && (req.Spec.CPUs > 0 || req.Spec.MemoryMB > 0) {
			// A provider without full reconfiguration still applies resource
			// changes through the mandatory SetInstanceResources; without this
			// the PATCH would update only the datastore and report success.
			resources := provider.ResourceSpec{
				CPUs:     instance.Spec.CPUs,
				MemoryMB: instance.Spec.MemoryMB,
			}
			if err := prov.SetInstanceResources(ctx, instance.Handle, resources); err != nil {
				s.writeLoggedError(w, http.StatusInternalServerError,
					"Recorded the change but the provider could not apply it", err)
				return
			}
		}
	}

	s.refreshFromProvider(ctx, instance)

	s.writeJSON(w, http.StatusOK, instance)
}

// handleDeleteInstance deletes an instance.
func (s *Server) handleDeleteInstance(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

	instance, err := s.datastore.GetInstance(ctx, instanceID)
	if err != nil {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("Instance not found: %s", instanceID))
		return
	}

	prov, err := s.registry.Get(instance.Provider)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Provider not found: %s", instance.Provider))
		return
	}

	// Parse force option
	force := r.URL.Query().Get("force") == "true"

	// Delete via provider
	if err := prov.DeleteInstance(ctx, instance.Handle, force); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to delete instance", err)
		return
	}

	// Clean up firewall rules for this instance
	if err := s.cleanupFirewallRules(ctx, instance.Name); err != nil {
		s.logger.Warn("Failed to clean up firewall rules during delete", logging.FieldInstance, instance.Name, logging.FieldError, err)
		// Don't fail the delete operation, just log the warning
	}

	// Remove from datastore
	if err := s.datastore.DeleteInstance(ctx, instanceID); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to remove instance from datastore", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleStartInstance starts an instance.
func (s *Server) handleStartInstance(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

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

	// Check if client wants streaming output
	acceptHeader := r.Header.Get("Accept")
	wantsStream := strings.Contains(acceptHeader, "text/plain")

	if wantsStream {
		// Disable write deadline for streaming (start can take time for large instances)
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Time{}); err != nil {
			s.logger.Warn("Failed to disable write deadline for streaming start request", logging.FieldInstance, instance.Name, logging.FieldProvider, instance.Provider, logging.FieldError, err)
		}

		// Get flusher for streaming
		flusher, ok := w.(http.Flusher)
		if !ok {
			s.writeError(w, http.StatusInternalServerError, "Streaming not supported")
			return
		}

		// Set headers for streaming
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)

		streamCtx, stream, stopStreaming := s.withStreamingLogger(ctx, w, flusher, "instance.start", instance.Name, instance.Provider)
		defer stopStreaming()

		err = prov.StartInstance(streamCtx, instance.Handle)
		// The provider has finished logging: drain its lines before any terminal
		// line so the client reads them in order.
		stopStreaming()
		if err != nil {
			stream.Printf("ERROR: Failed to start instance: %v\n", err)
			return
		}

		if err := s.datastore.UpdateInstanceState(ctx, instance.ID, provider.StateRunning); err != nil {
			s.logger.Warn("Failed to update instance state after start", "instance", instance.ID, "error", err)
		}

		// Update spec with allocated network info from provider
		s.syncInstanceSpec(ctx, instance.ID, prov, instance.Handle)

		stream.Printf("SUCCESS: Instance started\n")
	} else {
		// Non-streaming mode (legacy)
		if err := prov.StartInstance(ctx, instance.Handle); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to start instance", err)
			return
		}

		if err := s.datastore.UpdateInstanceState(ctx, instance.ID, provider.StateRunning); err != nil {
			s.logger.Warn("Failed to update instance state after start", "instance", instance.ID, "error", err)
		}

		// Update spec with allocated network info from provider
		s.syncInstanceSpec(ctx, instance.ID, prov, instance.Handle)

		s.writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
	}
}

// handleStopInstance stops an instance.
func (s *Server) handleStopInstance(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

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

	// Parse stop options
	force := r.URL.Query().Get("force") == "true"
	opts := provider.StopOptions{
		Force:   force,
		Timeout: 30 * time.Second,
	}

	if err := prov.StopInstance(ctx, instance.Handle, opts); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to stop instance", err)
		return
	}

	if err := s.datastore.UpdateInstanceState(ctx, instance.ID, provider.StateStopped); err != nil {
		s.logger.Warn("Failed to update instance state after stop", "instance", instance.ID, "error", err)
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// handleRestartInstance restarts an instance.
func (s *Server) handleRestartInstance(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

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

	if err := prov.RestartInstance(ctx, instance.Handle); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to restart instance", err)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "restarted"})
}

// handleGetInstanceEvents returns instance events.
func (s *Server) handleGetInstanceEvents(w http.ResponseWriter, r *http.Request, instanceID string) {
	ctx := r.Context()

	// Events are keyed by instance ID; resolve a name in the path first.
	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	// Optional ?limit= query parameter; GetEvents applies a sane default and an
	// upper bound, so an out-of-range or missing value is handled downstream.
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	events, err := s.datastore.GetEvents(ctx, instance.ID, limit)
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to get events", err)
		return
	}

	s.writeJSON(w, http.StatusOK, events)
}
