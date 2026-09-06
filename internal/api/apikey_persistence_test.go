package api

import (
	"context"
	"testing"

	"github.com/hospitus/hospitus/internal/auth"
	"github.com/hospitus/hospitus/internal/datastore"
)

// TestAPIKeyPersistenceRoundTrip verifies an API key survives a simulated
// restart: it is saved to the datastore and rehydrated into a fresh
// AuthManager, then validates against its plaintext (audit HIGH
// auth_key_handler.go:132: runtime keys were never persisted).
func TestAPIKeyPersistenceRoundTrip(t *testing.T) {
	ctx := context.Background()
	ds, err := datastore.NewDatastore(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ds.Close()

	const plaintext = "nx-plaintext-key-1234567890"
	am := auth.NewAuthManager()
	if err := am.AddAPIKey("k1", "runtime", plaintext, []string{"read"}, nil); err != nil {
		t.Fatal(err)
	}
	k, ok := am.GetAPIKey("k1")
	if !ok {
		t.Fatal("GetAPIKey did not return the created key")
	}
	if err := ds.SaveAPIKey(ctx, datastore.APIKeyRecord{
		ID: k.ID, Name: k.Name, HashedKey: k.HashedKey,
		Permissions: k.Permissions, CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt,
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate a restart: a fresh manager rehydrated from the datastore.
	am2 := auth.NewAuthManager()
	recs, err := ds.ListAPIKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		am2.LoadAPIKey(&auth.APIKey{
			ID: rec.ID, Name: rec.Name, HashedKey: rec.HashedKey,
			Permissions: rec.Permissions, CreatedAt: rec.CreatedAt, ExpiresAt: rec.ExpiresAt,
		})
	}

	perms, ok := am2.GetPermissions(plaintext)
	if !ok {
		t.Fatal("reloaded API key does not validate after restart")
	}
	if len(perms) != 1 || perms[0] != "read" {
		t.Errorf("reloaded permissions = %v, want [read]", perms)
	}

	// After deletion it must no longer validate.
	if err := ds.DeleteAPIKey(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	recs2, _ := ds.ListAPIKeys(ctx)
	if len(recs2) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(recs2))
	}
}
