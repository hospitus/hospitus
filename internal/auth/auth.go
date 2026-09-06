// Package auth provides authentication and authorization for HOSPITUS API.
//
// This package implements API key authentication and future support for
// JWT tokens, OAuth2, and other authentication methods.
//
// Security Features:
//   - Bcrypt hashing and comparison for stored keys (AuthManager, production)
//   - Constant-time comparison for the development provider (SimpleAuthProvider)
//
// Rate limiting and audit logging of authentication live in the HTTP layer
// (internal/api middleware and internal/security), not here.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// AuthProvider defines the interface for API key authentication.
// Both AuthManager (production, bcrypt-based) and SimpleAuthProvider
// (development, plaintext) implement this interface.
type AuthProvider interface {
	// ValidateKey checks if an API key is valid.
	// Returns true if the key is valid, false otherwise.
	ValidateKey(key string) bool

	// GetPermissions returns the permissions associated with an API key and
	// whether the key exists. The boolean distinguishes a valid key that has
	// no permissions (nil, true) from an unknown/invalid key (nil, false).
	GetPermissions(key string) ([]string, bool)
}

// APIKey represents an API key with metadata
type APIKey struct {
	ID          string
	Name        string
	HashedKey   string
	CreatedAt   time.Time
	LastUsedAt  time.Time
	ExpiresAt   *time.Time
	Permissions []string
}

// AuthManager manages API keys and authentication
type AuthManager struct {
	keys map[string]*APIKey // key ID -> APIKey
	mu   sync.RWMutex
}

// NewAuthManager creates a new authentication manager
func NewAuthManager() *AuthManager {
	return &AuthManager{
		keys: make(map[string]*APIKey),
	}
}

// APIKeyPrefix marks a string as one of our API keys.
//
// Anything that slices a key apart reads its length from here rather than
// counting the characters, so the prefix can change without leaving a stale
// offset behind.
const APIKeyPrefix = "hsp_"

// GenerateAPIKey generates a new random API key.
//
// Format: APIKeyPrefix + base64url(32 random bytes), so the encoded part is 43
// characters.
// Example: "hsp_HXeWpgNaCuW2_ooMgcaMNf0MMez14AagFjJcAZdhFVs"
func GenerateAPIKey() (string, error) {
	// Generate 32 random bytes
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(randomBytes)

	return APIKeyPrefix + encoded, nil
}

// HashAPIKey hashes an API key for secure storage
func HashAPIKey(key string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(key), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash API key: %w", err)
	}
	return string(hashed), nil
}

// AddAPIKey adds a new API key to the manager. Passing a non-nil expiresAt
// registers the key and its expiration atomically under a single lock, so a
// concurrent validation never observes the key without its expiry set.
func (am *AuthManager) AddAPIKey(id, name, key string, permissions []string, expiresAt *time.Time) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	// Hash the key for storage
	hashedKey, err := HashAPIKey(key)
	if err != nil {
		return err
	}

	am.keys[id] = &APIKey{
		ID:        id,
		Name:      name,
		HashedKey: hashedKey,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
		// Copy: the caller keeps its slice, and a later append or rewrite there
		// would change what this key is allowed to do.
		Permissions: append([]string(nil), permissions...),
	}

	return nil
}

// LoadAPIKey inserts an already-hashed key into the manager without re-hashing.
// Used to rehydrate persisted keys at startup.
func (am *AuthManager) LoadAPIKey(k *APIKey) {
	am.mu.Lock()
	defer am.mu.Unlock()
	stored := *k
	stored.Permissions = append([]string(nil), k.Permissions...)
	am.keys[k.ID] = &stored
}

// GetAPIKey returns a copy of the stored key record (including its bcrypt hash)
// so callers can persist it. Returns false if no key has that ID.
func (am *AuthManager) GetAPIKey(id string) (*APIKey, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()
	k, ok := am.keys[id]
	if !ok {
		return nil, false
	}
	return cloneKey(k), true
}

// ValidateAPIKey validates an API key by bcrypt-comparing it against every
// stored key hash.
//
// Returns the APIKey if valid, nil if invalid. bcrypt's comparison is itself
// constant-time for a given hash, but the lookup is a linear scan over all
// stored keys rather than a single constant-time comparison.
func (am *AuthManager) ValidateAPIKey(key string) (*APIKey, error) {
	if key == "" {
		return nil, fmt.Errorf("empty API key")
	}

	// Use read lock for the bcrypt comparison phase — each bcrypt call takes ~100ms,
	// so holding a write lock would serialize all concurrent requests.
	am.mu.RLock()
	var matchedKey *APIKey
	var expired bool
	for _, apiKey := range am.keys {
		err := bcrypt.CompareHashAndPassword([]byte(apiKey.HashedKey), []byte(key))
		if err == nil {
			matchedKey = apiKey
			// Read ExpiresAt while still holding the lock — reading it after
			// RUnlock races with any goroutine mutating the key.
			if apiKey.ExpiresAt != nil && time.Now().After(*apiKey.ExpiresAt) {
				expired = true
			}
			break
		}
	}
	am.mu.RUnlock()

	if matchedKey == nil {
		return nil, fmt.Errorf("invalid API key")
	}

	// Check expiry (captured under the lock above) before updating last-used time
	if expired {
		return nil, fmt.Errorf("API key expired")
	}

	// The lock is dropped between the match and this update, so the key can be
	// revoked or replaced in between. The pointer captured above stays valid
	// either way, and returning it would authorize a key that no longer exists.
	am.mu.Lock()
	current, ok := am.keys[matchedKey.ID]
	if !ok || current != matchedKey {
		am.mu.Unlock()
		return nil, fmt.Errorf("invalid API key")
	}
	if current.ExpiresAt != nil && time.Now().After(*current.ExpiresAt) {
		am.mu.Unlock()
		return nil, fmt.Errorf("API key expired")
	}
	current.LastUsedAt = time.Now()
	result := cloneKey(current)
	am.mu.Unlock()

	return result, nil
}

// cloneKey copies a stored key so a caller can read it without the manager's
// lock. The struct copy alone is not enough: Permissions would still alias the
// slice ValidateAPIKey and SetKeyPermissions write to.
func cloneKey(k *APIKey) *APIKey {
	cp := *k
	cp.Permissions = append([]string(nil), k.Permissions...)
	return &cp
}

// HasPermission checks if an API key has a specific permission
func (am *AuthManager) HasPermission(keyID, permission string) bool {
	am.mu.RLock()
	defer am.mu.RUnlock()

	apiKey, ok := am.keys[keyID]
	if !ok {
		return false
	}

	// Check for wildcard permission
	for _, perm := range apiKey.Permissions {
		if perm == "*" || perm == permission {
			return true
		}
	}

	return false
}

// RevokeAPIKey removes an API key
func (am *AuthManager) RevokeAPIKey(id string) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	if _, ok := am.keys[id]; !ok {
		return fmt.Errorf("API key not found")
	}

	delete(am.keys, id)
	return nil
}

// SetKeyExpiry sets the expiration time for an API key.
func (am *AuthManager) SetKeyExpiry(id string, expiresAt *time.Time) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	apiKey, ok := am.keys[id]
	if !ok {
		return fmt.Errorf("API key not found")
	}

	apiKey.ExpiresAt = expiresAt
	return nil
}

// ListAPIKeys returns value copies of all API keys with the bcrypt hash
// cleared. Returning copies rather than the internal *APIKey pointers avoids
// data races with concurrent validation, and clearing HashedKey keeps the
// secret hash out of callers and API responses.
func (am *AuthManager) ListAPIKeys() []APIKey {
	am.mu.RLock()
	defer am.mu.RUnlock()

	keys := make([]APIKey, 0, len(am.keys))
	for _, key := range am.keys {
		cp := cloneKey(key)
		cp.HashedKey = ""
		keys = append(keys, *cp)
	}

	return keys
}

// ValidateKey implements AuthProvider for AuthManager.
// It validates an API key by bcrypt-comparing it against the stored hashes.
func (am *AuthManager) ValidateKey(key string) bool {
	_, err := am.ValidateAPIKey(key)
	return err == nil
}

// GetPermissions implements AuthProvider for AuthManager.
// The second return value reports whether the key exists, so a valid key with
// no permissions (nil, true) is distinguishable from an invalid key (nil, false).
func (am *AuthManager) GetPermissions(key string) ([]string, bool) {
	apiKey, err := am.ValidateAPIKey(key)
	if err != nil {
		return nil, false
	}
	return apiKey.Permissions, true
}

// SimpleAuthProvider provides simple API key authentication for development only.
//
// WARNING: Keys are stored in plaintext in memory. Use only for development
// or testing. In production, use AuthManager with bcrypt-hashed keys.
type SimpleAuthProvider struct {
	validKeys map[string]bool
	mu        sync.RWMutex
}

// NewSimpleAuthProvider creates a simple auth provider
func NewSimpleAuthProvider(keys []string) *SimpleAuthProvider {
	validKeys := make(map[string]bool)
	for _, key := range keys {
		// A blank line in the key file arrives here as an empty string, and an
		// empty entry would authenticate a request that carries no key at all.
		if key == "" {
			continue
		}
		validKeys[key] = true
	}

	return &SimpleAuthProvider{
		validKeys: validKeys,
	}
}

// ValidateKey implements AuthProvider for SimpleAuthProvider.
// It checks if a key is valid using constant-time comparison.
func (sap *SimpleAuthProvider) ValidateKey(key string) bool {
	if key == "" {
		return false
	}

	sap.mu.RLock()
	defer sap.mu.RUnlock()

	// Check each valid key using constant-time comparison
	for validKey := range sap.validKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(validKey)) == 1 {
			return true
		}
	}

	return false
}

// GetPermissions implements AuthProvider for SimpleAuthProvider.
func (sap *SimpleAuthProvider) GetPermissions(key string) ([]string, bool) {
	if sap.ValidateKey(key) {
		return []string{"*"}, true
	}
	return nil, false
}

// AddKey adds a new valid key
func (sap *SimpleAuthProvider) AddKey(key string) {
	sap.mu.Lock()
	defer sap.mu.Unlock()
	sap.validKeys[key] = true
}

// RemoveKey removes a key
func (sap *SimpleAuthProvider) RemoveKey(key string) {
	sap.mu.Lock()
	defer sap.mu.Unlock()
	delete(sap.validKeys, key)
}
