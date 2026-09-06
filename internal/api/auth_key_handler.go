package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hospitus/hospitus/internal/auth"
	"github.com/hospitus/hospitus/internal/datastore"
	"github.com/hospitus/hospitus/internal/security"
	"github.com/hospitus/hospitus/pkg/logging"
)

// persistAPIKey writes the manager's current record for keyID to the datastore
// so keys created at runtime survive a restart. No-op when the datastore is not
// the concrete SQLite store.
// persistAPIKey writes a newly created key to the datastore.
//
// It answers whether the key is now durable. A datastore that cannot hold it
// is not a detail to log and move on from: the caller is about to be handed a
// credential, and one that vanishes at the next restart is worse than none.
func (s *Server) persistAPIKey(ctx context.Context, authMgr *auth.AuthManager, keyID string) error {
	ds, ok := s.datastore.(*datastore.Datastore)
	if !ok {
		// No persistence configured at all — an in-memory store, which is a
		// deliberate deployment choice rather than a failure.
		return nil
	}
	k, ok := authMgr.GetAPIKey(keyID)
	if !ok {
		return fmt.Errorf("key %s vanished between creation and persistence", keyID)
	}
	rec := datastore.APIKeyRecord{
		ID:          k.ID,
		Name:        k.Name,
		HashedKey:   k.HashedKey,
		Permissions: k.Permissions,
		CreatedAt:   k.CreatedAt,
		ExpiresAt:   k.ExpiresAt,
	}
	if err := ds.SaveAPIKey(ctx, rec); err != nil {
		return fmt.Errorf("failed to persist API key %s: %w", keyID, err)
	}
	return nil
}

// authAddAPIKeyRequest is the request body for creating a new API key.
type authAddAPIKeyRequest struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	TTLDays     int      `json:"ttl_days"` // 0 = no expiry
}

// authAPIKeyResponse is a safe representation of an API key (no hash).
type authAPIKeyResponse struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  time.Time  `json:"last_used_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Permissions []string   `json:"permissions"`
}

// authAddAPIKeyResult is returned after creating a key (includes the plaintext key once).
type authAddAPIKeyResult struct {
	authAPIKeyResponse
	APIKey string `json:"api_key"` // Only returned once on creation
}

// handleAuthKeys handles API key management endpoints.
//
// GET    /api/v1/auth/keys       - List all API keys (no secret values)
// POST   /api/v1/auth/keys       - Create a new API key
// DELETE /api/v1/auth/keys/{id}  - Revoke an API key
func (s *Server) handleAuthKeys(w http.ResponseWriter, r *http.Request, keyID string) {
	// Auth keys require an already-authenticated admin (wildcard permission) to manage.
	if !HasPermission(r, "*") {
		s.writeError(w, http.StatusForbidden, "Admin permission required to manage API keys")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleListAuthKeys(w, r)
	case http.MethodPost:
		s.handleCreateAuthKey(w, r)
	case http.MethodDelete:
		if keyID == "" {
			s.writeError(w, http.StatusBadRequest, "Key ID required for revocation")
			return
		}
		s.handleRevokeAuthKey(w, r, keyID)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) handleListAuthKeys(w http.ResponseWriter, r *http.Request) {
	// Only AuthManager supports listing; SimpleAuthProvider keys are ephemeral.
	authMgr, ok := s.authProvider.(*auth.AuthManager)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, "API key management not available with SimpleAuthProvider")
		return
	}

	keys := authMgr.ListAPIKeys()
	result := make([]authAPIKeyResponse, 0, len(keys))
	for i := range keys {
		k := &keys[i]
		result = append(result, authAPIKeyResponse{
			ID:          k.ID,
			Name:        k.Name,
			CreatedAt:   k.CreatedAt,
			LastUsedAt:  k.LastUsedAt,
			ExpiresAt:   k.ExpiresAt,
			Permissions: k.Permissions,
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"keys": result,
	})
}

func (s *Server) handleCreateAuthKey(w http.ResponseWriter, r *http.Request) {
	authMgr, ok := s.authProvider.(*auth.AuthManager)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, "API key management not available with SimpleAuthProvider")
		return
	}

	var req authAddAPIKeyRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		s.writeLoggedError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if req.Name == "" {
		s.writeError(w, http.StatusBadRequest, "Name is required")
		return
	}

	// Generate a new API key
	plaintext, err := auth.GenerateAPIKey()
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to generate API key", err)
		return
	}

	// Generate a unique key ID using crypto/rand (not predictable time-based)
	keyID, err := auth.GenerateAPIKey()
	if err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to generate key ID", err)
		return
	}
	// Drop the prefix and keep 16 random characters: enough to be unique, short
	// enough to read in a listing.
	keyID = "key-" + keyID[len(auth.APIKeyPrefix):len(auth.APIKeyPrefix)+16]

	// Compute expiration if TTL is set
	var expiresAt *time.Time
	if req.TTLDays > 0 {
		t := time.Now().Add(time.Duration(req.TTLDays) * 24 * time.Hour)
		expiresAt = &t
	}

	// Register the key atomically, including any expiry, so a concurrent
	// validation never observes the key without its expiration set.
	if err := authMgr.AddAPIKey(keyID, req.Name, plaintext, req.Permissions, expiresAt); err != nil {
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to register API key", err)
		return
	}

	// Persist the key so it survives a daemon restart. Without this, keys
	// created via the API live only in memory and vanish on restart — so a
	// failure here is reported rather than logged, and the half-created key
	// is taken back out of the manager.
	if err := s.persistAPIKey(r.Context(), authMgr, keyID); err != nil {
		if revokeErr := authMgr.RevokeAPIKey(keyID); revokeErr != nil {
			s.logger.Warn("Could not revoke the key that failed to persist",
				"key_id", keyID, logging.FieldError, revokeErr)
		}
		s.writeLoggedError(w, http.StatusInternalServerError, "Failed to persist API key", err)
		return
	}

	// Read back the timestamp the manager actually stored so the response
	// reflects the persisted CreatedAt rather than a second, slightly-later
	// time.Now().
	createdAt := time.Now()
	if k, ok := authMgr.GetAPIKey(keyID); ok {
		createdAt = k.CreatedAt
	}

	// Audit log the key creation
	clientIP := s.extractClientIP(r)
	// Fire-and-forget audit logging; failures must not block the request.
	_ = security.GetGlobalAuditLogger().Log(&security.AuditEvent{
		EventType: security.EventAPIKeyGenerated,
		Severity:  security.SeverityWarning,
		ClientIP:  clientIP,
		Message:   fmt.Sprintf("API key generated: %s (%s)", keyID, req.Name),
		Details: map[string]interface{}{
			"key_id":      keyID,
			"name":        req.Name,
			"permissions": req.Permissions,
			"has_expiry":  expiresAt != nil,
		},
	})

	result := authAddAPIKeyResult{
		authAPIKeyResponse: authAPIKeyResponse{
			ID:          keyID,
			Name:        req.Name,
			CreatedAt:   createdAt,
			ExpiresAt:   expiresAt,
			Permissions: req.Permissions,
		},
		APIKey: plaintext,
	}

	s.writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleRevokeAuthKey(w http.ResponseWriter, r *http.Request, keyID string) {
	authMgr, ok := s.authProvider.(*auth.AuthManager)
	if !ok {
		s.writeError(w, http.StatusNotImplemented, "API key management not available with SimpleAuthProvider")
		return
	}

	ds, persistent := s.datastore.(*datastore.Datastore)

	// In-memory first, then the persisted copy.
	//
	// There is no order that makes this atomic — two stores, no transaction
	// across them — so the question is which half-done state is safer, and for
	// a revocation it is the one where the key has already stopped working.
	// Deleting the row first would leave a live credential in memory while the
	// operator reads an error; this way the key is dead immediately and the
	// only risk is that it returns at the next restart.
	//
	// Which is why the request is idempotent rather than guarded by an
	// existence check up front. After a failed store delete the key is no
	// longer in memory, so a "does it exist?" gate answered 404 and the
	// operator had no way left to finish the job — the one state from which a
	// retry actually matters.
	_, inMemory := authMgr.GetAPIKey(keyID)
	if inMemory {
		if err := authMgr.RevokeAPIKey(keyID); err != nil {
			s.writeLoggedError(w, http.StatusInternalServerError, "Failed to revoke API key", err)
			return
		}
	}

	stored := false
	if persistent {
		// Only looked up when the key is not in memory — the retry path.
		// DeleteAPIKey is a DELETE on an id, which succeeds whether or not the
		// row is there, so it cannot answer "was it present?" on its own; the
		// listing is what tells a genuine 404 apart from a second attempt at
		// the same revocation.
		if !inMemory {
			keys, err := ds.ListAPIKeys(r.Context())
			if err != nil {
				s.writeLoggedError(w, http.StatusInternalServerError, "Failed to read the stored API keys", err)
				return
			}
			for i := range keys {
				if keys[i].ID == keyID {
					stored = true
					break
				}
			}
		}

		if inMemory || stored {
			if err := ds.DeleteAPIKey(r.Context(), keyID); err != nil {
				s.writeLoggedError(w, http.StatusInternalServerError,
					"API key is revoked now but its stored copy remains; it will come back on restart — retry this request", err)
				return
			}
		}
	}

	// Nowhere at all: nothing to revoke.
	if !inMemory && !stored {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("API key not found: %s", keyID))
		return
	}

	clientIP := s.extractClientIP(r)
	// Fire-and-forget audit logging; failures must not block the request.
	_ = security.GetGlobalAuditLogger().Log(&security.AuditEvent{
		EventType: security.EventAPIKeyRevoked,
		Severity:  security.SeverityWarning,
		ClientIP:  clientIP,
		Message:   fmt.Sprintf("API key revoked: %s", keyID),
		Details:   map[string]interface{}{"key_id": keyID},
	})

	s.writeJSON(w, http.StatusOK, map[string]string{
		"status":  "success",
		"message": "API key revoked successfully",
	})
}

// handleAuthKeysRoute dispatches auth key management requests.
func (s *Server) handleAuthKeysRoute(w http.ResponseWriter, r *http.Request) {
	// Extract key ID from path: /api/v1/auth/keys/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/keys")
	path = strings.TrimPrefix(path, "/")
	// The key ID is a single path segment; reject any deeper sub-path rather
	// than treating "id/extra" as a (bogus) key ID.
	if strings.Contains(path, "/") {
		s.writeError(w, http.StatusNotFound, "Not found")
		return
	}
	s.handleAuthKeys(w, r, path)
}
