package datastore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// BackupRecord is one backup as it is stored: the manager's own record, kept
// verbatim so a restart returns the backups it knew rather than an empty list.
type BackupRecord struct {
	ID         string
	InstanceID string
	Record     json.RawMessage
	CreatedAt  time.Time
}

// SaveBackupRecord writes a backup record, replacing any record of the same ID.
func (ds *Datastore) SaveBackupRecord(ctx context.Context, id, instanceID string, record json.RawMessage) error {
	const query = `
		INSERT INTO backups (id, instance_id, record, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET instance_id = excluded.instance_id, record = excluded.record
	`
	if _, err := ds.db.ExecContext(ctx, query, id, instanceID, string(record), time.Now()); err != nil {
		return fmt.Errorf("failed to save backup record %s: %w", id, err)
	}
	return nil
}

// DeleteBackupRecord removes a backup record.
func (ds *Datastore) DeleteBackupRecord(ctx context.Context, id string) error {
	if _, err := ds.db.ExecContext(ctx, `DELETE FROM backups WHERE id = ?`, id); err != nil {
		return fmt.Errorf("failed to delete backup record %s: %w", id, err)
	}
	return nil
}

// ListBackupRecords returns every stored backup record, oldest first.
func (ds *Datastore) ListBackupRecords(ctx context.Context) ([]BackupRecord, error) {
	rows, err := ds.db.QueryContext(ctx,
		`SELECT id, instance_id, record, created_at FROM backups ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("failed to list backup records: %w", err)
	}
	defer rows.Close()

	var records []BackupRecord
	for rows.Next() {
		var r BackupRecord
		var raw string
		if err := rows.Scan(&r.ID, &r.InstanceID, &raw, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to read backup record: %w", err)
		}
		r.Record = json.RawMessage(raw)
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read backup records: %w", err)
	}
	return records, nil
}
