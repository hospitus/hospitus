package datastore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// APIKeyRecord is a persisted API key. The plaintext key is never stored — only
// its bcrypt hash.
type APIKeyRecord struct {
	ID          string
	Name        string
	HashedKey   string
	Permissions []string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	ExpiresAt   *time.Time
}

// SaveAPIKey inserts or updates an API key.
func (ds *Datastore) SaveAPIKey(ctx context.Context, k APIKeyRecord) error {
	perms, err := json.Marshal(k.Permissions)
	if err != nil {
		return fmt.Errorf("failed to marshal permissions: %w", err)
	}
	_, err = ds.db.ExecContext(ctx, `
		INSERT INTO api_keys (id, name, hashed_key, permissions, created_at, last_used_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			hashed_key = excluded.hashed_key,
			permissions = excluded.permissions,
			last_used_at = excluded.last_used_at,
			expires_at = excluded.expires_at
	`, k.ID, k.Name, k.HashedKey, string(perms), k.CreatedAt, nullTime(k.LastUsedAt), nullTime(k.ExpiresAt))
	if err != nil {
		return fmt.Errorf("failed to save api key %s: %w", k.ID, err)
	}
	return nil
}

// DeleteAPIKey removes an API key.
func (ds *Datastore) DeleteAPIKey(ctx context.Context, id string) error {
	if _, err := ds.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id); err != nil {
		return fmt.Errorf("failed to delete api key %s: %w", id, err)
	}
	return nil
}

// ListAPIKeys returns all persisted API keys.
func (ds *Datastore) ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error) {
	rows, err := ds.db.QueryContext(ctx,
		`SELECT id, name, hashed_key, permissions, created_at, last_used_at, expires_at FROM api_keys`)
	if err != nil {
		return nil, fmt.Errorf("failed to list api keys: %w", err)
	}
	defer rows.Close()

	var out []APIKeyRecord
	for rows.Next() {
		var (
			k        APIKeyRecord
			permsRaw string
			lastUsed sql.NullTime
			expires  sql.NullTime
		)
		if err := rows.Scan(&k.ID, &k.Name, &k.HashedKey, &permsRaw, &k.CreatedAt, &lastUsed, &expires); err != nil {
			return nil, fmt.Errorf("failed to scan api key: %w", err)
		}
		if err := json.Unmarshal([]byte(permsRaw), &k.Permissions); err != nil {
			k.Permissions = nil
		}
		if lastUsed.Valid {
			k.LastUsedAt = &lastUsed.Time
		}
		if expires.Valid {
			k.ExpiresAt = &expires.Time
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func nullTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return *t
}
