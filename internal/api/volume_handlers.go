package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/jail"
	"github.com/hospitus/hospitus/pkg/storage"
	"github.com/hospitus/hospitus/pkg/validation"
)

// CreateVolumeRequest contains parameters for creating a volume
type CreateVolumeRequest struct {
	Name        string `json:"name"`
	Size        string `json:"size,omitempty"`
	Quota       string `json:"quota,omitempty"`
	Reservation string `json:"reservation,omitempty"`
	Compression string `json:"compression,omitempty"`
	Description string `json:"description,omitempty"`
}

// VolumeInfo contains information about a volume
type VolumeInfo struct {
	Name string `json:"name"`
	// Size is the dataset's refquota: the space its own data may occupy,
	// excluding snapshots. Quota is the wider ceiling that includes them.
	Size        int64  `json:"size,omitempty"`
	Used        int64  `json:"used"`
	Available   int64  `json:"available"`
	Quota       int64  `json:"quota,omitempty"`
	Reservation int64  `json:"reservation,omitempty"`
	Compression string `json:"compression,omitempty"`
	Mountpoint  string `json:"mountpoint,omitempty"`
	Dataset     string `json:"dataset,omitempty"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

// AttachVolumeRequest contains parameters for attaching a volume
type AttachVolumeRequest struct {
	MountPoint string `json:"mount_point"`
}

// handleVolumes handles volume list and create operations.
//
// Routes:
//
//	GET /api/v1/volumes - List all volumes
//	POST /api/v1/volumes - Create a new volume
func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if !s.requireStorage(w) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		volumes, err := s.storage.ListVolumes(ctx)
		if err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list volumes", err)
			return
		}

		result := make([]VolumeInfo, 0, len(volumes))
		for i := range volumes {
			result = append(result, storageVolumeToInfo(volumes[i]))
		}

		s.writeJSON(w, http.StatusOK, result)

	case http.MethodPost:
		var req CreateVolumeRequest
		if err := s.decodeJSONBody(w, r, &req); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
			return
		}

		if req.Name == "" {
			s.writeError(w, http.StatusBadRequest, "Volume name is required")
			return
		}
		if err := validation.ValidateInstanceName(req.Name); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid volume name", err)
			return
		}
		// The description becomes a ZFS user property, read back by parsing
		// the tab-separated lines of "zfs get -H": a newline or tab in it
		// would come back as a different property, or truncated.
		if strings.ContainsAny(req.Description, "\t\r\n") {
			s.writeError(w, http.StatusBadRequest, "description cannot contain tabs or newlines")
			return
		}
		if len(req.Description) > 1024 {
			s.writeError(w, http.StatusBadRequest, "description too long (max 1024 characters)")
			return
		}

		vol, err := s.storage.CreateVolume(ctx, req.Name, createOptionsFromRequest(req))
		if err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to create volume", err)
			return
		}

		s.writeJSON(w, http.StatusCreated, storageVolumeToInfo(*vol))

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// requireStorage answers the request when no storage backend exists, and reports
// whether the caller may proceed.
func (s *Server) requireStorage(w http.ResponseWriter) bool {
	if s.storage != nil {
		return true
	}
	s.writeError(w, http.StatusNotImplemented,
		"Volumes require a ZFS storage backend, which this host does not provide")
	return false
}

// createOptionsFromRequest maps the API request onto the backend options.
// Reservation and compression are ZFS properties rather than first-class
// options, which is why they travel in Properties.
func createOptionsFromRequest(req CreateVolumeRequest) storage.VolumeOptions {
	opts := storage.VolumeOptions{
		Quota:      req.Quota,
		Properties: map[string]string{},
	}
	// Size caps the volume's own data. It becomes a refquota rather than
	// VolumeOptions.Size, which would make a zvol — a block device, not the
	// filesystem a jail volume is mounted from.
	if req.Size != "" {
		opts.Properties["refquota"] = req.Size
	}
	if req.Reservation != "" {
		opts.Properties["reservation"] = req.Reservation
	}
	if req.Compression != "" {
		opts.Properties["compression"] = req.Compression
	}
	// A ZFS user property, so the description survives the round trip: the
	// field was accepted, dropped on the floor, and the 201 came back with an
	// empty description. getProperties reads "zfs get all", which includes it.
	if req.Description != "" {
		opts.Properties[volumeDescriptionProperty] = req.Description
	}
	return opts
}

// volumeDescriptionProperty is where a volume's description lives on the
// dataset. ZFS user properties are namespaced by a colon.
const volumeDescriptionProperty = "hospitus:description"

// handleVolumeDetail handles operations on a specific volume.
//
// Routes:
//
//	GET /api/v1/volumes/{name} - Get volume info
//	DELETE /api/v1/volumes/{name} - Delete volume
func (s *Server) handleVolumeDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Extract volume name from path
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		s.writeError(w, http.StatusBadRequest, "Invalid path")
		return
	}

	volumeName := parts[3]

	// The API calls the backend directly, so nothing else stands between this
	// name and a ZFS dataset path: "data@snapshot" named a snapshot to destroy
	// and "a/b" a child dataset. The same grammar instances use.
	if err := validation.ValidateInstanceName(volumeName); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid volume name", err)
		return
	}

	if !s.requireStorage(w) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		vol, err := s.storage.GetVolume(ctx, volumeName)
		if err != nil {
			s.writeLoggedError(w, http.StatusNotFound, "Volume not found", err)
			return
		}

		s.writeJSON(w, http.StatusOK, storageVolumeToInfo(*vol))

	case http.MethodDelete:
		// force removes the volume along with its snapshots and clones; without
		// it ZFS refuses to destroy a dataset that still has dependents.
		force := r.URL.Query().Get("force") == "true"
		opts := storage.DeleteOptions{Force: force, Recursive: force}

		if err := s.storage.DeleteVolume(ctx, volumeName, opts); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to delete volume", err)
			return
		}

		w.WriteHeader(http.StatusNoContent)

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// handleInstanceVolumes handles volume attachment operations for an instance.
//
// Routes:
//
//	GET /api/v1/instances/{id}/volumes - List attached volumes
//	POST /api/v1/instances/{id}/volumes/{volume} - Attach volume
//	DELETE /api/v1/instances/{id}/volumes/{volume} - Detach volume
func (s *Server) handleInstanceVolumes(w http.ResponseWriter, r *http.Request, instanceID string, parts []string) {
	ctx := r.Context()

	// Get instance
	instance, err := s.lookupInstance(ctx, instanceID)
	if err != nil {
		s.writeLoggedError(w, http.StatusNotFound, "Instance not found", err)
		return
	}

	// Volumes only work with jail provider
	if instance.Provider != "jail" {
		s.writeError(w, http.StatusBadRequest, "Volume management is only supported for jails")
		return
	}

	// Get jail provider
	prov, err := s.registry.Get("jail")
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Jail provider not available")
		return
	}

	// The capability interface, not *jail.JailProvider: the concrete type meant
	// anything wrapping a jail provider — a decorator, a test double — was told
	// 501 for volume operations it implements. handleFreeBSD reaches rctl and
	// VNET the same way.
	jailProv, ok := prov.(provider.NamedVolumeProvider)
	if !ok {
		// The provider does not offer this, which is not a server fault: 500
		// told the caller to retry something that will never work.
		s.writeError(w, http.StatusNotImplemented, "Provider does not support named volumes")
		return
	}

	// parts is the route suffix passed by handleInstanceDetail, i.e.
	// ["volumes"] or ["volumes", "{volume}"] — not the full path. The volume
	// name (when present) is therefore at index 1, matching handleInstanceBackups.
	volumeName := volumeNameFromParts(parts)
	if volumeName != "" {
		if err := validation.ValidateInstanceName(volumeName); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid volume name", err)
			return
		}
	}

	// Get jail name from handle
	jailName := instance.Handle.ID
	if jailName == "" {
		jailName = instance.Name
	}

	switch {
	case volumeName == "" && r.Method == http.MethodGet:
		// List attached volumes - get volume and check its MountedTo field
		volumes, err := jailProv.ListVolumes(ctx)
		if err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to list volumes", err)
			return
		}

		// Filter to volumes mounted to this jail
		// Non-nil: a jail with nothing mounted answered "null" while the
		// volume list endpoint answers [] for the same emptiness.
		mounts := make([]jail.VolumeMountInfo, 0)
		for i := range volumes {
			for _, m := range volumes[i].MountedTo {
				if m.JailName == jailName {
					mounts = append(mounts, m)
				}
			}
		}

		s.writeJSON(w, http.StatusOK, mounts)

	case volumeName != "" && r.Method == http.MethodPost:
		// Attach volume
		var req AttachVolumeRequest
		if err := s.decodeJSONBody(w, r, &req); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
			return
		}

		if req.MountPoint == "" {
			s.writeError(w, http.StatusBadRequest, "Mount point is required")
			return
		}

		if err := jailProv.MountVolumeToJail(ctx, volumeName, jailName, req.MountPoint, false); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to attach volume", err)
			return
		}

		w.WriteHeader(http.StatusNoContent)

	case volumeName != "" && r.Method == http.MethodDelete:
		// Detach volume
		if err := jailProv.UnmountVolumeFromJail(ctx, volumeName, jailName); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to detach volume", err)
			return
		}

		w.WriteHeader(http.StatusNoContent)

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// storageVolumeToInfo renders a backend volume for the API. Reservation and
// compression are ZFS properties, so they are read back from Properties rather
// than from dedicated fields.
func storageVolumeToInfo(vol storage.Volume) VolumeInfo {
	createdAt := ""
	if !vol.Created.IsZero() {
		// UTC and RFC3339: "Z" in a layout is a literal character, not a zone,
		// so a non-UTC Created was stamped with a Z it did not have.
		createdAt = vol.Created.UTC().Format(time.RFC3339)
	}

	info := VolumeInfo{
		Name:        vol.Name,
		Description: vol.Properties[volumeDescriptionProperty],
		Used:        vol.Used,
		Available:   vol.Available,
		Quota:       vol.Quota,
		Mountpoint:  vol.Path,
		Dataset:     vol.FullName,
		CreatedAt:   createdAt,
	}
	if compression, ok := vol.Properties["compression"]; ok && compression != "off" {
		info.Compression = compression
	}
	if reservation, ok := vol.Properties["reservation"]; ok {
		info.Reservation = parseVolumeSize(reservation)
	}
	if refquota, ok := vol.Properties["refquota"]; ok {
		info.Size = parseVolumeSize(refquota)
	}
	return info
}

// volumeNameFromParts extracts the volume name from the route suffix passed by
// handleInstanceDetail (["volumes"] or ["volumes", "{volume}"]). It returns ""
// when no volume segment is present.
func volumeNameFromParts(parts []string) string {
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// parseVolumeSize reads a ZFS byte count. A property ZFS reports as "none" or
// that cannot be parsed means "not set", which is zero here.
func parseVolumeSize(value string) int64 {
	size, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return size
}
