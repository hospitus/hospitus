// Package datastore persists what Hospitus knows about the instances, stacks, jobs,
// API keys and backups it manages, so a daemon restart finds them again.
//
// Storage is a single SQLite file: one file to back up, no server to run, and
// transactions the providers do not have to reimplement. The schema is built by
// ordered migrations in the migrations subpackage; an applied migration is never
// edited, only followed by another.
package datastore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3" // SQLite driver

	"github.com/hospitus/hospitus/internal/crypto"
	"github.com/hospitus/hospitus/internal/datastore/migrations"
	"github.com/hospitus/hospitus/pkg/logging"
	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/validation"
)

// Datastore manages persistent storage of instance metadata.
//
// Thread Safety:
// Concurrency is serialized entirely by the database/sql connection pool;
// there is no in-process mutex. Operations that must be atomic across
// multiple statements open an explicit transaction (see RenameInstance).
type Datastore struct {
	db        *sql.DB
	path      string
	encryptor *crypto.Encryptor // nil if encryption not configured
	logger    *slog.Logger
}

// Instance represents a stored instance with full metadata.
//
// This is a denormalized view combining data from multiple tables
// for easier querying and API responses.
type Instance struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Provider    string                  `json:"provider"`
	State       provider.InstanceState  `json:"state"`
	Spec        provider.InstanceSpec   `json:"spec"`
	Handle      provider.InstanceHandle `json:"handle"`
	CreatedAt   time.Time               `json:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
	StartedAt   *time.Time              `json:"started_at,omitempty"`
	Labels      map[string]string       `json:"labels"`
	Annotations map[string]string       `json:"annotations"`
	Backup      *InstanceBackupConfig   `json:"backup,omitempty"`
}

// InstanceBackupConfig is a backup configuration as it is stored.
//
// It holds the whole configuration, not the part that fits in a summary: a
// schedule persisted without its retention policy or its hooks comes back after
// a restart as a different configuration from the one that was set.
//
// The column is JSON, so a row written before a field existed decodes with that
// field at its zero value.
type InstanceBackupConfig struct {
	Enabled        bool   `json:"enabled"`
	Schedule       string `json:"schedule"`
	Destination    string `json:"destination,omitempty"`
	Compression    string `json:"compression,omitempty"`
	PreBackupHook  string `json:"pre_backup_hook,omitempty"`
	PostBackupHook string `json:"post_backup_hook,omitempty"`
	KeepLast       int    `json:"keep_last,omitempty"`
	KeepHourly     int    `json:"keep_hourly,omitempty"`
	KeepDaily      int    `json:"keep_daily,omitempty"`
	KeepWeekly     int    `json:"keep_weekly,omitempty"`
	KeepMonthly    int    `json:"keep_monthly,omitempty"`
}

// Event represents an instance lifecycle event.
//
// Events provide an audit trail of all operations performed on instances.
// This is useful for:
//   - Debugging (why did this instance stop?)
//   - Compliance (who created this instance?)
//   - Analytics (how long do instances run?)
type Event struct {
	ID         int64                  `json:"id"`
	InstanceID string                 `json:"instance_id"`
	Type       EventType              `json:"type"`
	State      provider.InstanceState `json:"state"`
	Message    string                 `json:"message"`
	Metadata   map[string]interface{} `json:"metadata"`
	Timestamp  time.Time              `json:"timestamp"`
}

// EventType represents the type of lifecycle event.
type EventType string

const (
	EventTypeCreated   EventType = "created"
	EventTypeStarted   EventType = "started"
	EventTypeStopped   EventType = "stopped"
	EventTypeRestarted EventType = "restarted"
	EventTypeDeleted   EventType = "deleted"
	EventTypeError     EventType = "error"
	EventTypeUpdated   EventType = "updated"
)

// withForeignKeys appends the mattn/go-sqlite3 DSN parameter that turns on
// foreign key enforcement, preserving any existing query parameters.
func withForeignKeys(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	// _foreign_keys: SQLite disables enforcement per connection, which makes
	// every ON DELETE CASCADE inert.
	//
	// _journal_mode=WAL: in the default DELETE mode a writer excludes every
	// reader, so a backup or an image fetch holding a transaction blocked the
	// event and job writes behind it until they timed out. WAL lets them run
	// together. It applies to the file, not the connection, and is a no-op for
	// :memory:.
	//
	// _busy_timeout: the driver's 5s default is short for a pool of ten
	// connections around one file.
	params := "_foreign_keys=on&_journal_mode=WAL&_busy_timeout=10000&_synchronous=NORMAL"
	if path == ":memory:" {
		params = "_foreign_keys=on&_busy_timeout=10000"
	}
	return path + sep + params
}

// NewDatastore creates a new datastore instance.
//
// The path should point to the SQLite database file. If the file doesn't
// exist, it will be created. If path is ":memory:", an in-memory database
// is used (useful for testing).
func NewDatastore(path string, encryptor *crypto.Encryptor) (*Datastore, error) {
	// SQLite disables foreign key enforcement per connection by default, which
	// makes every ON DELETE CASCADE inert. Enable it via a DSN parameter so it
	// is applied to every connection the pool opens, not just the first.
	db, err := sql.Open("sqlite3", withForeignKeys(path))
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool
	// Why these values?
	//   - MaxOpenConns: Limit to 10 to avoid file lock contention
	//   - MaxIdleConns: Keep 5 warm for quick reuse
	//   - ConnMaxLifetime: Recycle connections every hour to prevent stale locks
	//
	// Special case: SQLite :memory: databases are per-connection.
	// With multiple connections, each gets its own isolated in-memory database,
	// so migrations applied on connection A are invisible on connection B.
	// Force a single connection to share one in-memory database.
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(5)
	}
	db.SetConnMaxLifetime(time.Hour)

	ds := &Datastore{
		db:        db,
		path:      path,
		encryptor: encryptor,
		logger:    logging.WithComponent("datastore"),
	}

	// Initialize schema
	if err := ds.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return ds, nil
}

// initSchema applies database migrations to bring the schema up to date.
func (ds *Datastore) initSchema() error {
	migrator := migrations.NewMigrator(ds.db, ds.logger)
	return migrator.Migrate(context.Background(), migrations.All, 0)
}

// MigrationStatus returns the current migration status.
func (ds *Datastore) MigrationStatus(ctx context.Context) (*migrations.Status, error) {
	migrator := migrations.NewMigrator(ds.db, ds.logger)
	return migrator.Status(ctx, migrations.All)
}

// Migrate applies pending migrations up to the target version (0 = all).
func (ds *Datastore) Migrate(ctx context.Context, targetVersion int) error {
	migrator := migrations.NewMigrator(ds.db, ds.logger)
	return migrator.Migrate(ctx, migrations.All, targetVersion)
}

// Rollback rolls back migrations to the target version.
func (ds *Datastore) Rollback(ctx context.Context, targetVersion int) error {
	migrator := migrations.NewMigrator(ds.db, ds.logger)
	return migrator.Rollback(ctx, migrations.All, targetVersion)
}

// CreateInstance stores a new instance in the datastore.
//
// This should be called after the provider successfully creates the instance.
// If the instance already exists, returns an error.
func (ds *Datastore) CreateInstance(ctx context.Context, instance *Instance) error {
	// Serialize complex fields to JSON
	specJSON, err := json.Marshal(instance.Spec)
	if err != nil {
		return fmt.Errorf("failed to marshal spec: %w", err)
	}

	handleJSON, err := json.Marshal(instance.Handle)
	if err != nil {
		return fmt.Errorf("failed to marshal handle: %w", err)
	}

	labelsJSON, err := json.Marshal(instance.Labels)
	if err != nil {
		return fmt.Errorf("failed to marshal labels: %w", err)
	}

	annotationsJSON, err := json.Marshal(instance.Annotations)
	if err != nil {
		return fmt.Errorf("failed to marshal annotations: %w", err)
	}

	backupJSON := "{}"
	if instance.Backup != nil {
		data, err := json.Marshal(instance.Backup)
		if err != nil {
			return fmt.Errorf("failed to marshal backup config: %w", err)
		}
		backupJSON = string(data)
	}

	// Set timestamps
	now := time.Now()
	instance.CreatedAt = now
	instance.UpdatedAt = now

	// Insert instance with extracted columns for efficient querying
	query := `
		INSERT INTO instances (id, name, provider, state, spec, handle, created_at, updated_at, labels, annotations, backup_config,
		                       cpus, memory_mb, os_type, arch, image)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err = ds.db.ExecContext(ctx, query,
		instance.ID,
		instance.Name,
		instance.Provider,
		instance.State,
		string(specJSON),
		string(handleJSON),
		instance.CreatedAt,
		instance.UpdatedAt,
		string(labelsJSON),
		string(annotationsJSON),
		backupJSON,
		instance.Spec.CPUs,
		instance.Spec.MemoryMB,
		instance.Spec.OSType,
		instance.Spec.Arch,
		instance.Spec.Image,
	)
	if err != nil {
		return fmt.Errorf("failed to insert instance: %w", err)
	}

	// Record creation event
	if err := ds.recordEvent(ctx, instance.ID, EventTypeCreated, instance.State, "Instance created", nil); err != nil {
		ds.logger.Warn("failed to record event", "instance_id", instance.ID, logging.FieldError, err)
	}

	return nil
}

// instanceColumns is the ordered SELECT column list shared by every instance
// query so that scanInstance can deserialize any of them identically.
const instanceColumns = "id, name, provider, state, spec, handle, created_at, updated_at, started_at, labels, annotations, backup_config"

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanInstance scans a single row selected with instanceColumns into an
// Instance, deserializing its JSON-encoded columns. The raw Scan error
// (including sql.ErrNoRows) is returned unwrapped so callers can apply their
// own not-found convention.
func scanInstance(row rowScanner) (*Instance, error) {
	var instance Instance
	var specJSON, handleJSON, labelsJSON, annotationsJSON string
	var backupJSON sql.NullString
	var startedAt sql.NullTime

	if err := row.Scan(
		&instance.ID,
		&instance.Name,
		&instance.Provider,
		&instance.State,
		&specJSON,
		&handleJSON,
		&instance.CreatedAt,
		&instance.UpdatedAt,
		&startedAt,
		&labelsJSON,
		&annotationsJSON,
		&backupJSON,
	); err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(specJSON), &instance.Spec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal spec: %w", err)
	}
	if err := json.Unmarshal([]byte(handleJSON), &instance.Handle); err != nil {
		return nil, fmt.Errorf("failed to unmarshal handle: %w", err)
	}
	if err := json.Unmarshal([]byte(labelsJSON), &instance.Labels); err != nil {
		return nil, fmt.Errorf("failed to unmarshal labels: %w", err)
	}
	if err := json.Unmarshal([]byte(annotationsJSON), &instance.Annotations); err != nil {
		return nil, fmt.Errorf("failed to unmarshal annotations: %w", err)
	}
	if backupJSON.Valid && backupJSON.String != "" && backupJSON.String != "{}" {
		instance.Backup = &InstanceBackupConfig{}
		if err := json.Unmarshal([]byte(backupJSON.String), instance.Backup); err != nil {
			return nil, fmt.Errorf("failed to unmarshal backup config: %w", err)
		}
	}
	if startedAt.Valid {
		instance.StartedAt = &startedAt.Time
	}

	return &instance, nil
}

// GetInstance retrieves an instance by ID.
func (ds *Datastore) GetInstance(ctx context.Context, id string) (*Instance, error) {
	query := "SELECT " + instanceColumns + " FROM instances WHERE id = ?"

	instance, err := scanInstance(ds.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("instance not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query instance: %w", err)
	}

	return instance, nil
}

// GetInstanceByName retrieves an instance by name.
// Returns nil, nil if the instance does not exist (unlike GetInstance which returns an error).
func (ds *Datastore) GetInstanceByName(ctx context.Context, name string) (*Instance, error) {
	query := "SELECT " + instanceColumns + " FROM instances WHERE name = ?"

	instance, err := scanInstance(ds.db.QueryRowContext(ctx, query, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // Instance not found - return nil, nil (not an error)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query instance: %w", err)
	}

	return instance, nil
}

// UpdateInstanceState updates the state of an instance.
//
// This is called frequently as instances transition through states. State,
// updated_at and (when transitioning to running) started_at are all set in a
// single statement so no update can be silently dropped.
func (ds *Datastore) UpdateInstanceState(ctx context.Context, id string, state provider.InstanceState) error {
	now := time.Now()
	query := `
		UPDATE instances
		SET state = ?,
		    updated_at = ?,
		    started_at = CASE WHEN ? = ? THEN ? ELSE started_at END
		WHERE id = ?
	`

	result, err := ds.db.ExecContext(ctx, query, state, now, state, provider.StateRunning, now, id)
	if err != nil {
		return fmt.Errorf("failed to update instance state: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("instance not found: %s", id)
	}

	// Record state change event
	if err := ds.recordEvent(ctx, id, EventTypeUpdated, state, fmt.Sprintf("State changed to %s", state), nil); err != nil {
		ds.logger.Warn("failed to record event", "instance_id", id, logging.FieldError, err)
	}

	return nil
}

// UpdateInstanceSpec updates the spec of an instance.
//
// This is called after instance start to update the spec with allocated network info.
func (ds *Datastore) UpdateInstanceSpec(ctx context.Context, id string, spec provider.InstanceSpec) error {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("failed to marshal spec: %w", err)
	}

	// Keep the extracted columns (used by ListInstances SQL filters) in sync
	// with the new spec, otherwise those filters would query stale values.
	query := `
		UPDATE instances
		SET spec = ?, updated_at = ?,
		    cpus = ?, memory_mb = ?, os_type = ?, arch = ?, image = ?
		WHERE id = ?
	`

	result, err := ds.db.ExecContext(ctx, query, string(specJSON), time.Now(),
		spec.CPUs, spec.MemoryMB, spec.OSType, spec.Arch, spec.Image, id)
	if err != nil {
		return fmt.Errorf("failed to update instance spec: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("instance not found: %s", id)
	}

	return nil
}

// UpdateInstanceHandle updates an instance's handle (including metadata).
// This is called when provider config is updated via jail set command.
func (ds *Datastore) UpdateInstanceHandle(ctx context.Context, id string, handle provider.InstanceHandle) error {
	handleJSON, err := json.Marshal(handle)
	if err != nil {
		return fmt.Errorf("failed to marshal handle: %w", err)
	}

	query := `
		UPDATE instances
		SET handle = ?, updated_at = ?
		WHERE id = ?
	`

	result, err := ds.db.ExecContext(ctx, query, string(handleJSON), time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update instance handle: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("instance not found: %s", id)
	}

	return nil
}

// UpdateBackupConfig updates an instance's backup configuration.
func (ds *Datastore) UpdateBackupConfig(ctx context.Context, id string, config *InstanceBackupConfig) error {
	backupJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal backup config: %w", err)
	}

	query := `
		UPDATE instances
		SET backup_config = ?, updated_at = ?
		WHERE id = ?
	`

	result, err := ds.db.ExecContext(ctx, query, string(backupJSON), time.Now(), id)
	if err != nil {
		return fmt.Errorf("failed to update backup config: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("instance not found: %s", id)
	}

	return nil
}

// RenameInstance updates the name (and corresponding id) of an instance.
// Both the id and the name columns are set to newName because bhyve uses the name
// as the primary key (the VM directory name).
func (ds *Datastore) RenameInstance(ctx context.Context, oldName, newName string) error {
	tx, err := ds.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin rename transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Renaming rewrites the instance primary key. Child rows (events) reference
	// instances(id); with foreign keys enforced, defer the checks until commit
	// so the parent and children can be updated together without transiently
	// orphaning the child rows.
	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return fmt.Errorf("failed to defer foreign keys: %w", err)
	}

	result, err := tx.ExecContext(ctx,
		`UPDATE instances SET id = ?, name = ?, updated_at = ? WHERE id = ?`,
		newName, newName, time.Now(), oldName)
	if err != nil {
		return fmt.Errorf("failed to rename instance: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("instance not found: %s", oldName)
	}

	// Repoint child events to the new instance id.
	if _, err := tx.ExecContext(ctx,
		`UPDATE events SET instance_id = ? WHERE instance_id = ?`, newName, oldName); err != nil {
		return fmt.Errorf("failed to update events during rename: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit rename: %w", err)
	}
	return nil
}

// DeleteInstance removes an instance from the datastore.
//
// This should be called after the provider successfully deletes the instance.
// Cascade delete will also remove associated events.
func (ds *Datastore) DeleteInstance(ctx context.Context, id string) error {
	// Record deletion event before deleting
	if err := ds.recordEvent(ctx, id, EventTypeDeleted, provider.StateDeleting, "Instance deleted", nil); err != nil {
		ds.logger.Warn("failed to record event", "instance_id", id, logging.FieldError, err)
	}

	query := "DELETE FROM instances WHERE id = ?"
	result, err := ds.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete instance: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("instance not found: %s", id)
	}

	return nil
}

// ListInstances returns all instances matching the filter.
//
// Filters:
//   - Provider: Filter by provider name
//   - States: Filter by instance states (OR logic)
//   - Labels: Filter by labels (AND logic)
func (ds *Datastore) ListInstances(ctx context.Context, filter InstanceFilter) ([]*Instance, error) {
	// Build dynamic query based on filters
	query := "SELECT " + instanceColumns + " FROM instances WHERE 1=1"
	args := []interface{}{}

	if filter.Provider != "" {
		query += " AND provider = ?"
		args = append(args, filter.Provider)
	}

	// Filter by states (OR logic), using parameter placeholders (never string
	// concatenation of user-supplied values).
	if len(filter.States) > 0 {
		placeholders := make([]string, len(filter.States))
		for i, state := range filter.States {
			placeholders[i] = "?"
			args = append(args, state)
		}
		query += " AND state IN (" + strings.Join(placeholders, ",") + ")"
	}

	// Filter by labels using SQLite json_extract (SQL-level filtering)
	for key, value := range filter.Labels {
		// SECURITY: Validate label key to prevent JSON path injection.
		// Keys like "$.foo[0]" or "$.foo']" could manipulate json_extract.
		if err := validation.ValidateLabel(key, value); err != nil {
			return nil, fmt.Errorf("invalid label key %q: %w", key, err)
		}
		query += " AND json_extract(labels, ?) = ?"
		args = append(args, "$."+key, value)
	}

	// Filter by extracted columns (efficient SQL queries, no JSON parsing needed)
	if filter.MinCPUs != nil {
		query += " AND cpus >= ?"
		args = append(args, *filter.MinCPUs)
	}
	if filter.MaxCPUs != nil {
		query += " AND cpus <= ?"
		args = append(args, *filter.MaxCPUs)
	}
	if filter.MinMemoryMB != nil {
		query += " AND memory_mb >= ?"
		args = append(args, *filter.MinMemoryMB)
	}
	if filter.MaxMemoryMB != nil {
		query += " AND memory_mb <= ?"
		args = append(args, *filter.MaxMemoryMB)
	}
	if filter.OSType != "" {
		query += " AND os_type = ?"
		args = append(args, filter.OSType)
	}
	if filter.Arch != "" {
		query += " AND arch = ?"
		args = append(args, filter.Arch)
	}
	if filter.Image != "" {
		query += " AND image LIKE ?"
		// Escape SQLite LIKE wildcards in user input to prevent injection.
		escaped := strings.ReplaceAll(filter.Image, "%", "\\%")
		escaped = strings.ReplaceAll(escaped, "_", "\\_")
		args = append(args, "%"+escaped+"%")
	}

	// Order by creation time (newest first)
	query += " ORDER BY created_at DESC"

	// Execute query
	rows, err := ds.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query instances: %w", err)
	}
	defer rows.Close()

	var instances []*Instance
	for rows.Next() {
		instance, err := scanInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan instance: %w", err)
		}
		instances = append(instances, instance)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating instances: %w", err)
	}

	return instances, nil
}

// Event query limits keep GetEvents bounded regardless of the caller-supplied
// value.
const (
	defaultEventLimit = 100
	maxEventLimit     = 1000
)

// GetEvents returns events for an instance.
func (ds *Datastore) GetEvents(ctx context.Context, instanceID string, limit int) ([]*Event, error) {
	if limit <= 0 {
		limit = defaultEventLimit
	} else if limit > maxEventLimit {
		limit = maxEventLimit
	}

	// Order by timestamp, breaking ties on the monotonic rowid so results are
	// deterministic even when several events share a timestamp.
	query := `
		SELECT id, instance_id, type, state, message, metadata, timestamp
		FROM events
		WHERE instance_id = ?
		ORDER BY timestamp DESC, id DESC
		LIMIT ?
	`

	rows, err := ds.db.QueryContext(ctx, query, instanceID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		var event Event
		var metadataJSON sql.NullString

		err := rows.Scan(
			&event.ID,
			&event.InstanceID,
			&event.Type,
			&event.State,
			&event.Message,
			&metadataJSON,
			&event.Timestamp,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}

		if metadataJSON.Valid && metadataJSON.String != "" {
			if err := json.Unmarshal([]byte(metadataJSON.String), &event.Metadata); err != nil {
				return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
			}
		}

		events = append(events, &event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating events: %w", err)
	}

	return events, nil
}

// recordEvent records an event for an instance.
//
// This is an internal method called by other datastore operations.
// It's fire-and-forget - if event recording fails, we log but don't fail the operation.
func (ds *Datastore) recordEvent(ctx context.Context, instanceID string, eventType EventType, state provider.InstanceState, message string, metadata map[string]interface{}) error {
	var metadataJSON []byte
	var err error

	if metadata != nil {
		metadataJSON, err = json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}
	}

	query := `
		INSERT INTO events (instance_id, type, state, message, metadata, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)
	`

	_, err = ds.db.ExecContext(ctx, query,
		instanceID,
		eventType,
		state,
		message,
		string(metadataJSON),
		time.Now(),
	)

	return err
}

// Close closes the datastore connection.
func (ds *Datastore) Close() error {
	return ds.db.Close()
}

// Ping verifies the database connection is alive and accessible.
// This is useful for health checks and connection validation.
func (ds *Datastore) Ping(ctx context.Context) error {
	return ds.db.PingContext(ctx)
}

// GetPath returns the path to the database file.
func (ds *Datastore) GetPath() string {
	return ds.path
}

// InstanceFilter defines criteria for filtering instances.
type InstanceFilter struct {
	Provider string                   // Filter by provider name
	States   []provider.InstanceState // Filter by states (OR logic)
	Labels   map[string]string        // Filter by labels (AND logic)

	// Extracted column filters (efficient SQL queries)
	MinCPUs     *int   // Filter by minimum CPUs
	MaxCPUs     *int   // Filter by maximum CPUs
	MinMemoryMB *int64 // Filter by minimum memory (MB)
	MaxMemoryMB *int64 // Filter by maximum memory (MB)
	OSType      string // Filter by OS type (freebsd, linux, etc.)
	Arch        string // Filter by architecture (amd64, arm64, etc.)
	Image       string // Filter by image name/pattern
}
