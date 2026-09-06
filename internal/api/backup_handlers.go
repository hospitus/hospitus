package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hospitus/hospitus/internal/security"
	"github.com/hospitus/hospitus/pkg/backup"
	"github.com/hospitus/hospitus/pkg/validation"
)

// BackupConfigRequest represents a request to configure backups
type BackupConfigRequest struct {
	Enabled           bool   `json:"enabled"`
	Schedule          string `json:"schedule"`
	Destination       string `json:"destination,omitempty"`
	Compression       string `json:"compression,omitempty"`
	EncryptionKeyFile string `json:"encryption_key_file,omitempty"`
	PreBackupHook     string `json:"pre_backup_hook,omitempty"`
	PostBackupHook    string `json:"post_backup_hook,omitempty"`
	// No omitempty: it is a no-op on a struct field, and this request is only
	// ever decoded, so the tag promised something it could not do.
	Retention RetentionConfigSpec `json:"retention"`
}

// RetentionConfigSpec represents retention configuration
type RetentionConfigSpec struct {
	KeepLast    int    `json:"keep_last,omitempty"`
	KeepHourly  int    `json:"keep_hourly,omitempty"`
	KeepDaily   int    `json:"keep_daily,omitempty"`
	KeepWeekly  int    `json:"keep_weekly,omitempty"`
	KeepMonthly int    `json:"keep_monthly,omitempty"`
	MinAge      string `json:"min_age,omitempty"`
}

// BackupInfoResponse represents a backup in API responses
type BackupInfoResponse struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id"`
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	// A pointer, because omitempty does nothing for a struct type: a backup
	// still running serialized "completed_at": "0001-01-01T00:00:00Z", which
	// every client had to know to read as "not finished".
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	Size         int64      `json:"size"`
	SnapshotName string     `json:"snapshot_name"`
	Destination  string     `json:"destination,omitempty"`
	Compressed   bool       `json:"compressed"`
	Encrypted    bool       `json:"encrypted"`
	Error        string     `json:"error,omitempty"`
	Verified     bool       `json:"verified"`
}

// CreateBackupRequest represents a request to create a backup
type CreateBackupRequest struct {
	Type string `json:"type"` // snapshot, full, incremental
}

// RestoreBackupRequest represents a request to restore from backup
type RestoreBackupRequest struct {
	TargetInstanceID string `json:"target_instance_id"`
}

// handleBackups handles backup-related requests for all instances
func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) {
	if s.backupManager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Backup manager not initialized")
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Expected: api/v1/backups[/backupID[/action]]

	switch len(parts) {
	case 3: // /api/v1/backups
		if r.Method == http.MethodGet {
			s.handleListAllBackups(w, r)
		} else {
			s.writeMethodNotAllowed(w, http.MethodGet)
		}
	case 4: // /api/v1/backups/{backupID}
		backupID := parts[3]
		switch r.Method {
		case http.MethodGet:
			s.handleGetBackup(w, r, backupID)
		case http.MethodDelete:
			s.handleDeleteBackup(w, r, backupID)
		default:
			s.writeMethodNotAllowed(w, http.MethodGet, http.MethodDelete)
		}
	case 5: // /api/v1/backups/{backupID}/{action}
		backupID := parts[3]
		action := parts[4]
		switch action {
		case "verify":
			s.handleVerifyBackup(w, r, backupID)
		case "restore":
			s.handleRestoreBackup(w, r, backupID)
		default:
			s.writeError(w, http.StatusNotFound, "Unknown action")
		}
	default:
		s.writeError(w, http.StatusNotFound, "Not found")
	}
}

// handleInstanceBackups handles backup operations for a specific instance
func (s *Server) handleInstanceBackups(w http.ResponseWriter, r *http.Request, instanceID string, parts []string) {
	if s.backupManager == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Backup manager not initialized")
		return
	}

	// Here, not in each handler: CreateBackup validates the name itself, but
	// ListBackups and CreateSnapshot do not, so a name like "bad.name" reached
	// the dataset resolver and came back as a 500 where it is a 400.
	if err := validation.ValidateInstanceName(instanceID); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid instance ID", err)
		return
	}

	// parts is the suffix split after /api/v1/instances/{id}/
	// parts[0] == "backups", len==1 means /backups, len==2 means /backups/{name-or-config}
	switch len(parts) {
	case 1: // /api/v1/instances/{id}/backups
		switch r.Method {
		case http.MethodGet:
			s.handleListInstanceBackups(w, r, instanceID)
		case http.MethodPost:
			s.handleCreateInstanceBackup(w, r, instanceID)
		default:
			s.writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
		}
	case 2: // /api/v1/instances/{id}/backups/config
		if parts[1] == "config" {
			switch r.Method {
			case http.MethodGet:
				s.handleGetBackupConfig(w, r, instanceID)
			case http.MethodPut, http.MethodPost:
				s.handleSetBackupConfig(w, r, instanceID)
			case http.MethodDelete:
				s.handleDeleteBackupConfig(w, r, instanceID)
			default:
				s.writeMethodNotAllowed(w, http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete)
			}
		} else {
			s.writeError(w, http.StatusNotFound, "Not found")
		}
	default:
		s.writeError(w, http.StatusNotFound, "Not found")
	}
}

// handleListAllBackups lists all backups across all instances
func (s *Server) handleListAllBackups(w http.ResponseWriter, r *http.Request) {
	if s.backupManager == nil {
		s.writeJSON(w, http.StatusOK, []BackupInfoResponse{})
		return
	}

	backups := s.backupManager.ListAllBackups()
	response := make([]BackupInfoResponse, 0, len(backups))
	for _, b := range backups {
		response = append(response, backupToResponse(b))
	}
	s.writeJSON(w, http.StatusOK, response)
}

// handleListInstanceBackups lists all backups for an instance
func (s *Server) handleListInstanceBackups(w http.ResponseWriter, r *http.Request, instanceID string) {
	backups := s.backupManager.ListBackups(instanceID)
	response := make([]BackupInfoResponse, 0, len(backups))

	for _, b := range backups {
		response = append(response, backupToResponse(b))
	}

	s.writeJSON(w, http.StatusOK, response)
}

// handleCreateInstanceBackup creates a new backup
func (s *Server) handleCreateInstanceBackup(w http.ResponseWriter, r *http.Request, instanceID string) {
	// An absent body means a snapshot, which is the common case. A body that is
	// present and malformed is a mistake, and turning it into a snapshot performs
	// state-changing work the caller did not ask for.
	var req CreateBackupRequest
	bodySent := true
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		if !errors.Is(err, io.EOF) {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
			return
		}
		bodySent = false
		req.Type = "snapshot"
	}

	// A body that decodes but names no type is not the same as no body at all.
	// JSON null — as the whole document, or as the value of "type" — decodes
	// without error and leaves Type empty, and the empty case below then took
	// a snapshot for a caller who had asked for something else.
	if bodySent && req.Type == "" {
		s.writeError(w, http.StatusBadRequest,
			`a backup type is required when a body is sent: "snapshot", "full" or "incremental"`)
		return
	}

	ctx := r.Context()
	var info *backup.BackupInfo
	var err error

	switch req.Type {
	case "snapshot":
		info, err = s.backupManager.CreateSnapshot(ctx, instanceID)
	case "full":
		info, err = s.backupManager.CreateBackup(ctx, instanceID, backup.BackupTypeFull)
	case "incremental":
		info, err = s.backupManager.CreateBackup(ctx, instanceID, backup.BackupTypeIncr)
	default:
		s.writeError(w, http.StatusBadRequest, "Invalid backup type")
		return
	}

	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Backup failed", err)
		return
	}

	s.writeJSON(w, http.StatusCreated, backupToResponse(info))
}

// handleGetBackup gets details of a specific backup
func (s *Server) handleGetBackup(w http.ResponseWriter, r *http.Request, backupID string) {
	if s.backupManager == nil {
		s.writeError(w, http.StatusNotFound, "Backup not found")
		return
	}
	info := s.backupManager.GetBackup(backupID)
	if info == nil {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("Backup not found: %s", backupID))
		return
	}
	s.writeJSON(w, http.StatusOK, backupToResponse(info))
}

// handleDeleteBackup deletes a backup
func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request, backupID string) {
	ctx := r.Context()

	// The same two checks handleGetBackup makes, in the same order. The router
	// guards a nil manager too, but this handler does not depend on that: the
	// sibling does not either, and a lookup on a nil manager panics.
	if s.backupManager == nil {
		s.writeError(w, http.StatusNotFound, "Backup not found")
		return
	}
	// Looked up first, so a backup that is not there reads as 404 rather than
	// as an internal failure.
	if s.backupManager.GetBackup(backupID) == nil {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("Backup not found: %s", backupID))
		return
	}

	if err := s.backupManager.DeleteBackup(ctx, backupID); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to delete backup", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleVerifyBackup verifies a backup's integrity
func (s *Server) handleVerifyBackup(w http.ResponseWriter, r *http.Request, backupID string) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	if s.backupManager == nil || s.backupManager.GetBackup(backupID) == nil {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("Backup not found: %s", backupID))
		return
	}

	ctx := r.Context()
	if err := s.backupManager.VerifyBackup(ctx, backupID); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Verification failed", err)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "verified"})
}

// handleRestoreBackup restores from a backup
func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request, backupID string) {
	if r.Method != http.MethodPost {
		s.writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	var req RestoreBackupRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if s.backupManager == nil || s.backupManager.GetBackup(backupID) == nil {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("Backup not found: %s", backupID))
		return
	}

	// Checked here: the manager uses this as a datastore id, and a malformed
	// one came back as 500 for what is the caller's mistake. An empty value
	// means "restore in place" and stays allowed.
	if req.TargetInstanceID != "" {
		if err := validation.ValidateInstanceName(req.TargetInstanceID); err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid target_instance_id", err)
			return
		}
	}

	ctx := r.Context()
	if err := s.backupManager.Restore(ctx, backupID, req.TargetInstanceID); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Restore failed", err)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

// handleGetBackupConfig gets backup configuration for an instance
func (s *Server) handleGetBackupConfig(w http.ResponseWriter, r *http.Request, instanceID string) {
	config := s.backupManager.GetConfig(instanceID)
	if config == nil {
		s.writeError(w, http.StatusNotFound, "No backup configuration found")
		return
	}

	retention := map[string]interface{}{
		"keep_last":    config.Retention.KeepLast,
		"keep_hourly":  config.Retention.KeepHourly,
		"keep_daily":   config.Retention.KeepDaily,
		"keep_weekly":  config.Retention.KeepWeekly,
		"keep_monthly": config.Retention.KeepMonthly,
	}
	// min_age is parsed and persisted above but was left out of the answer, so
	// a client that set it read back a retention policy without it.
	if config.Retention.MinAge > 0 {
		retention["min_age"] = config.Retention.MinAge.String()
	}

	response := map[string]interface{}{
		"enabled":     config.Enabled,
		"schedule":    config.Schedule,
		"destination": config.Destination,
		"compression": config.Compression,
		"retention":   retention,
	}

	s.writeJSON(w, http.StatusOK, response)
}

// handleSetBackupConfig sets backup configuration for an instance
func (s *Server) handleSetBackupConfig(w http.ResponseWriter, r *http.Request, instanceID string) {
	var req BackupConfigRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := backup.ValidateCompression(req.Compression, req.Destination); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid compression", err)
		return
	}
	// Encryption is not implemented. Accepting a key file and sending the stream
	// in plaintext is worse than refusing: the caller believes the backup is
	// encrypted. ZFS native encryption on the dataset is the supported route,
	// and a raw send of an encrypted dataset stays encrypted end to end.
	if req.EncryptionKeyFile != "" {
		s.writeError(w, http.StatusBadRequest,
			"encryption_key_file is not implemented; encrypt the dataset with ZFS native encryption instead")
		return
	}

	// A hook is an executable the daemon runs as root when the backup fires.
	// validateHookPath confines it to an approved directory, so this is not
	// command injection — but choosing which approved program runs with the
	// daemon's authority is the same escalation refusePrivilegedConfig exists
	// to stop, and this route asks only for "write".
	if req.PreBackupHook != "" || req.PostBackupHook != "" {
		perms, authenticated := permissionsFromContext(r.Context())
		if !authenticated || !hasPerm(perms, "admin") {
			security.GetGlobalAuditLogger().LogAuthFailure(s.extractClientIP(r),
				"backup hook without admin: "+instanceID)
			s.writeError(w, http.StatusForbidden,
				"a backup hook runs as the daemon and needs an admin key")
			return
		}
	}

	config := &backup.BackupConfig{
		Enabled:           req.Enabled,
		Schedule:          req.Schedule,
		Destination:       req.Destination,
		Compression:       req.Compression,
		EncryptionKeyFile: req.EncryptionKeyFile,
		PreBackupHook:     req.PreBackupHook,
		PostBackupHook:    req.PostBackupHook,
		Retention: backup.RetentionPolicy{
			KeepLast:    req.Retention.KeepLast,
			KeepHourly:  req.Retention.KeepHourly,
			KeepDaily:   req.Retention.KeepDaily,
			KeepWeekly:  req.Retention.KeepWeekly,
			KeepMonthly: req.Retention.KeepMonthly,
		},
	}

	if req.Retention.MinAge != "" {
		d, err := time.ParseDuration(req.Retention.MinAge)
		if err != nil {
			s.writeLoggedError(w, http.StatusBadRequest, "Invalid retention min_age", err)
			return
		}
		// ParseDuration takes "-5m" happily. A negative bound was stored and
		// then left out of the answer, which only reports min_age above zero —
		// so the caller set a policy and read back one without it.
		if d < 0 {
			s.writeError(w, http.StatusBadRequest, "retention min_age must not be negative")
			return
		}
		config.Retention.MinAge = d
	}

	if err := s.backupManager.Configure(instanceID, config); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to configure backup", err)
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"status": "configured"})
}

// handleDeleteBackupConfig removes backup configuration for an instance
func (s *Server) handleDeleteBackupConfig(w http.ResponseWriter, r *http.Request, instanceID string) {
	s.backupManager.RemoveConfig(instanceID)
	w.WriteHeader(http.StatusNoContent)
}

// backupToResponse converts a BackupInfo to API response
func backupToResponse(b *backup.BackupInfo) BackupInfoResponse {
	return BackupInfoResponse{
		ID:           b.ID,
		InstanceID:   b.InstanceID,
		Type:         string(b.Type),
		Status:       string(b.Status),
		CreatedAt:    b.CreatedAt,
		CompletedAt:  completedAt(b.CompletedAt),
		Size:         b.Size,
		SnapshotName: b.SnapshotName,
		Destination:  b.Destination,
		Compressed:   b.Compressed,
		Encrypted:    b.Encrypted,
		Error:        b.Error,
		Verified:     b.Verified,
	}
}

// completedAt renders a zero completion time as absent rather than as the year 1.
func completedAt(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
