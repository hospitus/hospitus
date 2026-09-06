package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/hospitus/hospitus/pkg/provider/jail"
	"github.com/hospitus/hospitus/pkg/storage"
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
		if err := s.decodeJSONBody(r, &req); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
			return
		}

		if req.Name == "" {
			s.writeError(w, http.StatusBadRequest, "Volume name is required")
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
	return opts
}

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

	jailProv, ok := prov.(*jail.JailProvider)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "Invalid jail provider")
		return
	}

	// parts is the route suffix passed by handleInstanceDetail, i.e.
	// ["volumes"] or ["volumes", "{volume}"] — not the full path. The volume
	// name (when present) is therefore at index 1, matching handleInstanceBackups.
	volumeName := volumeNameFromParts(parts)

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
		var mounts []jail.VolumeMountInfo
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
		if err := s.decodeJSONBody(r, &req); err != nil {
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
		createdAt = vol.Created.Format("2006-01-02T15:04:05Z")
	}

	info := VolumeInfo{
		Name:       vol.Name,
		Used:       vol.Used,
		Available:  vol.Available,
		Quota:      vol.Quota,
		Mountpoint: vol.Path,
		Dataset:    vol.FullName,
		CreatedAt:  createdAt,
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
