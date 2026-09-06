package auth

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGenerateAPIKey(t *testing.T) {
	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("Failed to generate API key: %v", err)
	}

	// Check format
	if len(key) < 10 {
		t.Errorf("API key too short: %d characters", len(key))
	}

	if !strings.HasPrefix(key, APIKeyPrefix) {
		t.Errorf("API key should start with %q, got: %s", APIKeyPrefix, key)
	}

	// Generate multiple keys to ensure randomness
	keys := make(map[string]bool)
	for i := 0; i < 100; i++ {
		k, err := GenerateAPIKey()
		if err != nil {
			t.Fatalf("Failed to generate API key %d: %v", i, err)
		}
		if keys[k] {
			t.Errorf("Duplicate key generated: %s", k)
		}
		keys[k] = true
	}
}

func TestHashAPIKey(t *testing.T) {
	key := APIKeyPrefix + "test_key_123"

	hash1, err := HashAPIKey(key)
	if err != nil {
		t.Fatalf("Failed to hash API key: %v", err)
	}

	// Hash should be different from original
	if hash1 == key {
		t.Error("Hash should not equal original key")
	}

	// Hash should be consistent-ish length (bcrypt)
	if len(hash1) < 50 {
		t.Errorf("Hash too short: %d characters", len(hash1))
	}

	// Hashing same key twice should produce different results (salt)
	hash2, err := HashAPIKey(key)
	if err != nil {
		t.Fatalf("Failed to hash API key second time: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Expected different hashes due to random salt")
	}
}

func TestAuthManager(t *testing.T) {
	am := NewAuthManager()

	// Generate test API key
	apiKey, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("Failed to generate API key: %v", err)
	}

	err = am.AddAPIKey("test-id", "Test Key", apiKey, []string{"*"}, nil)
	if err != nil {
		t.Fatalf("Failed to add API key: %v", err)
	}

	// Validate correct key
	validated, err := am.ValidateAPIKey(apiKey)
	if err != nil {
		t.Errorf("Failed to validate correct API key: %v", err)
	}
	if validated == nil {
		t.Fatal("Expected validated key, got nil")
	}
	if validated.ID != "test-id" {
		t.Errorf("Expected ID 'test-id', got %s", validated.ID)
	}

	// Validate incorrect key
	_, err = am.ValidateAPIKey(APIKeyPrefix + "wrong_key")
	if err == nil {
		t.Error("Expected error for invalid key")
	}

	// A key that matches no stored hash is rejected. ValidateAPIKey does not
	// inspect the prefix; rejection here is purely "no matching key".
	_, err = am.ValidateAPIKey("no_prefix_key")
	if err == nil {
		t.Error("Expected error for key that matches no stored hash")
	}
}

func TestAuthManagerPermissions(t *testing.T) {
	am := NewAuthManager()

	apiKey, _ := GenerateAPIKey()
	am.AddAPIKey("test-id", "Test Key", apiKey, []string{"read", "write"}, nil)

	// Test existing permissions
	if !am.HasPermission("test-id", "read") {
		t.Error("Should have 'read' permission")
	}
	if !am.HasPermission("test-id", "write") {
		t.Error("Should have 'write' permission")
	}

	// Test missing permission
	if am.HasPermission("test-id", "admin") {
		t.Error("Should not have 'admin' permission")
	}

	// Test wildcard permission
	apiKey2, _ := GenerateAPIKey()
	am.AddAPIKey("admin-id", "Admin Key", apiKey2, []string{"*"}, nil)

	if !am.HasPermission("admin-id", "read") {
		t.Error("Wildcard should grant 'read' permission")
	}
	if !am.HasPermission("admin-id", "anything") {
		t.Error("Wildcard should grant any permission")
	}
}

func TestAuthManagerExpiration(t *testing.T) {
	am := NewAuthManager()

	apiKey, _ := GenerateAPIKey()
	err := am.AddAPIKey("test-id", "Test Key", apiKey, []string{"*"}, nil)
	if err != nil {
		t.Fatalf("Failed to add API key: %v", err)
	}

	// Set expiration in the past via the public API (no direct map access).
	past := time.Time{}
	if err := am.SetKeyExpiry("test-id", &past); err != nil {
		t.Fatalf("SetKeyExpiry: %v", err)
	}

	// Should fail validation due to expiration
	_, err = am.ValidateAPIKey(apiKey)
	if err == nil {
		t.Error("Expected error for expired key")
	}
	if err.Error() != "API key expired" {
		t.Errorf("Expected 'API key expired' error, got: %v", err)
	}
}

func TestAuthManagerRevoke(t *testing.T) {
	am := NewAuthManager()

	apiKey, _ := GenerateAPIKey()
	am.AddAPIKey("test-id", "Test Key", apiKey, []string{"*"}, nil)

	// Validate key works
	_, err := am.ValidateAPIKey(apiKey)
	if err != nil {
		t.Fatalf("Key should be valid before revocation: %v", err)
	}

	// Revoke key
	err = am.RevokeAPIKey("test-id")
	if err != nil {
		t.Fatalf("Failed to revoke key: %v", err)
	}

	// Key should no longer work
	_, err = am.ValidateAPIKey(apiKey)
	if err == nil {
		t.Error("Revoked key should not validate")
	}

	// Revoking again should fail
	err = am.RevokeAPIKey("test-id")
	if err == nil {
		t.Error("Expected error when revoking non-existent key")
	}
}

func TestAuthManagerList(t *testing.T) {
	am := NewAuthManager()

	// Add multiple keys
	for i := 0; i < 3; i++ {
		key, _ := GenerateAPIKey()
		am.AddAPIKey(
			string(rune('A'+i)),
			"Test Key",
			key,
			[]string{"*"},
			nil,
		)
	}

	keys := am.ListAPIKeys()
	if len(keys) != 3 {
		t.Errorf("Expected 3 keys, got %d", len(keys))
	}

	// Verify ListAPIKeys does not expose the bcrypt hash (secret material).
	for _, key := range keys {
		if key.HashedKey != "" {
			t.Error("ListAPIKeys should not expose the hashed key")
		}
	}
}

func TestSimpleAuthProvider(t *testing.T) {
	keys := []string{"key1", "key2", "key3"}
	provider := NewSimpleAuthProvider(keys)

	// Test valid keys
	for _, key := range keys {
		if !provider.ValidateKey(key) {
			t.Errorf("Key %s should be valid", key)
		}
	}

	// Test invalid key
	if provider.ValidateKey("invalid") {
		t.Error("Invalid key should not validate")
	}

	// Test add key
	provider.AddKey("key4")
	if !provider.ValidateKey("key4") {
		t.Error("Newly added key should validate")
	}

	// Test remove key
	provider.RemoveKey("key1")
	if provider.ValidateKey("key1") {
		t.Error("Removed key should not validate")
	}
}

func TestConstantTimeComparison(t *testing.T) {
	// This test verifies that SimpleAuthProvider uses constant-time comparison
	// We can't directly test timing, but we can verify it works correctly

	provider := NewSimpleAuthProvider([]string{"secret_key_123"})

	// Test exact match
	if !provider.ValidateKey("secret_key_123") {
		t.Error("Exact match should validate")
	}

	// Test similar but wrong keys (would be vulnerable to timing attacks)
	wrongKeys := []string{
		"secret_key_124",  // Last char different
		"secret_key_12",   // Shorter
		"secret_key_1234", // Longer
		"Secret_key_123",  // Case different
		"",                // Empty
	}

	for _, wrongKey := range wrongKeys {
		if provider.ValidateKey(wrongKey) {
			t.Errorf("Wrong key should not validate: %s", wrongKey)
		}
	}
}

func TestAuthManager_ValidateKeyAndGetPermissions(t *testing.T) {
	am := NewAuthManager()

	key, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	// AddAPIKey takes the plaintext key (hashes internally)
	if err := am.AddAPIKey("test-id", "Test Key", key, []string{"read", "write"}, nil); err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}

	// ValidateKey (interface wrapper)
	if !am.ValidateKey(key) {
		t.Error("ValidateKey should return true for valid key")
	}
	if am.ValidateKey("wrong-key") {
		t.Error("ValidateKey should return false for invalid key")
	}

	// GetPermissions
	perms, ok := am.GetPermissions(key)
	if !ok {
		t.Error("GetPermissions should report the key exists")
	}
	if len(perms) != 2 {
		t.Errorf("expected 2 permissions, got %d: %v", len(perms), perms)
	}

	// GetPermissions for invalid key
	perms, ok = am.GetPermissions("bad-key")
	if ok {
		t.Error("GetPermissions should report a bad key does not exist")
	}
	if perms != nil {
		t.Errorf("expected nil for invalid key, got %v", perms)
	}
}

func TestSimpleAuthProvider_GetPermissions(t *testing.T) {
	sap := NewSimpleAuthProvider([]string{"key1", "key2"})

	// Valid key → ["*"], true
	perms, ok := sap.GetPermissions("key1")
	if !ok {
		t.Error("GetPermissions should report the key exists")
	}
	if len(perms) != 1 || perms[0] != "*" {
		t.Errorf("expected [*], got %v", perms)
	}

	// Invalid key → nil, false
	perms, ok = sap.GetPermissions("bad-key")
	if ok {
		t.Error("GetPermissions should report a bad key does not exist")
	}
	if perms != nil {
		t.Errorf("expected nil for invalid key, got %v", perms)
	}
}

func TestListAPIKeysNoRaceNoHashLeak(t *testing.T) {
	am := NewAuthManager()
	key, _ := GenerateAPIKey()
	if err := am.AddAPIKey("id-1", "k", key, []string{"read"}, nil); err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}

	var wg sync.WaitGroup
	// Concurrently validate (which touches map entries) while listing.
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			// Read the record back: returning the stored pointer only races once
			// a caller touches its fields, so discarding it proves nothing.
			k, err := am.ValidateAPIKey(key)
			if err != nil {
				t.Errorf("ValidateAPIKey: %v", err)
				return
			}
			_ = k.LastUsedAt
			_ = k.ExpiresAt
			for range k.Permissions {
			}
		}()
		go func() {
			defer wg.Done()
			for _, k := range am.ListAPIKeys() {
				if k.HashedKey != "" {
					t.Error("ListAPIKeys leaked bcrypt hash")
				}
			}
		}()
	}
	wg.Wait()
}

func TestGetPermissionsValidKeyNoPermissions(t *testing.T) {
	am := NewAuthManager()
	key, _ := GenerateAPIKey()
	// Valid key deliberately created with no permissions.
	if err := am.AddAPIKey("id-1", "k", key, nil, nil); err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}

	perms, ok := am.GetPermissions(key)
	if !ok {
		t.Error("a valid key with no permissions must still report exists=true")
	}
	if perms != nil {
		t.Errorf("expected nil permissions, got %v", perms)
	}

	// An unknown key must report exists=false.
	if _, ok := am.GetPermissions(APIKeyPrefix + "unknown"); ok {
		t.Error("unknown key must report exists=false")
	}
}
