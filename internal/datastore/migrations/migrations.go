package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/hospitus/hospitus/pkg/logging"
)

// Migration represents a database schema migration.
type Migration struct {
	Version     int
	Description string
	Up          string
	Down        string
}

// Migrator handles database migrations.
type Migrator struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewMigrator creates a new migrator instance.
func NewMigrator(db *sql.DB, logger *slog.Logger) *Migrator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Migrator{
		db:     db,
		logger: logger.With(logging.FieldComponent, "migrator"),
	}
}

// Init creates the schema_versions table if it doesn't exist.
func (m *Migrator) Init(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS schema_versions (
		version INTEGER PRIMARY KEY,
		description TEXT NOT NULL,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err := m.db.ExecContext(ctx, schema)
	if err != nil {
		return fmt.Errorf("failed to create schema_versions table: %w", err)
	}
	return nil
}

// CurrentVersion returns the current schema version.
func (m *Migrator) CurrentVersion(ctx context.Context) (int, error) {
	var version int
	err := m.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_versions").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("failed to get current version: %w", err)
	}
	return version, nil
}

// AppliedVersions returns all applied migration versions.
func (m *Migrator) AppliedVersions(ctx context.Context) ([]int, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT version FROM schema_versions ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("failed to query versions: %w", err)
	}
	defer rows.Close()

	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("failed to scan version: %w", err)
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// Migrate applies all pending migrations up to the target version.
// If targetVersion is 0, applies all pending migrations.
func (m *Migrator) Migrate(ctx context.Context, migrations []Migration, targetVersion int) error {
	if err := m.Init(ctx); err != nil {
		return err
	}

	currentVersion, err := m.CurrentVersion(ctx)
	if err != nil {
		return err
	}

	// Sort migrations by version
	sorted := make([]Migration, len(migrations))
	copy(sorted, migrations)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Version < sorted[j].Version
	})

	// Determine target
	if targetVersion == 0 && len(sorted) > 0 {
		targetVersion = sorted[len(sorted)-1].Version
	}

	// Apply pending migrations
	applied := 0
	reachedVersion := currentVersion
	for _, migration := range sorted {
		if migration.Version <= currentVersion {
			continue
		}
		if migration.Version > targetVersion {
			break
		}

		if err := m.applyMigration(ctx, migration); err != nil {
			return fmt.Errorf("migration %d failed: %w", migration.Version, err)
		}
		applied++
		reachedVersion = migration.Version
	}

	if applied > 0 {
		m.logger.Info("migrations applied", "count", applied, "current_version", reachedVersion)
	} else {
		m.logger.Info("schema up to date", "version", currentVersion)
	}

	return nil
}

// Rollback rolls back migrations to the target version.
func (m *Migrator) Rollback(ctx context.Context, migrations []Migration, targetVersion int) error {
	if err := m.Init(ctx); err != nil {
		return err
	}

	currentVersion, err := m.CurrentVersion(ctx)
	if err != nil {
		return err
	}

	if targetVersion >= currentVersion {
		return nil
	}

	// Sort migrations by version descending for rollback
	sorted := make([]Migration, len(migrations))
	copy(sorted, migrations)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Version > sorted[j].Version
	})

	// Rollback migrations
	rolledBack := 0
	for _, migration := range sorted {
		if migration.Version <= targetVersion {
			break
		}
		if migration.Version > currentVersion {
			continue
		}

		if err := m.rollbackMigration(ctx, migration); err != nil {
			return fmt.Errorf("rollback %d failed: %w", migration.Version, err)
		}
		rolledBack++
	}

	if rolledBack > 0 {
		m.logger.Info("migrations rolled back", "count", rolledBack, "current_version", targetVersion)
	}

	return nil
}

func (m *Migrator) applyMigration(ctx context.Context, migration Migration) error {
	m.logger.Info("applying migration", "version", migration.Version, "description", migration.Description)

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Execute up migration
	if _, err := tx.ExecContext(ctx, migration.Up); err != nil {
		return fmt.Errorf("failed to execute up migration: %w", err)
	}

	// Record version
	_, err = tx.ExecContext(ctx,
		"INSERT INTO schema_versions (version, description, applied_at) VALUES (?, ?, ?)",
		migration.Version, migration.Description, time.Now())
	if err != nil {
		return fmt.Errorf("failed to record version: %w", err)
	}

	return tx.Commit()
}

func (m *Migrator) rollbackMigration(ctx context.Context, migration Migration) error {
	m.logger.Info("rolling back migration", "version", migration.Version, "description", migration.Description)

	if migration.Down == "" {
		return fmt.Errorf("migration %d has no down migration", migration.Version)
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Execute down migration
	if _, err := tx.ExecContext(ctx, migration.Down); err != nil {
		return fmt.Errorf("failed to execute down migration: %w", err)
	}

	// Remove version record
	if _, err := tx.ExecContext(ctx, "DELETE FROM schema_versions WHERE version = ?", migration.Version); err != nil {
		return fmt.Errorf("failed to remove version record: %w", err)
	}

	return tx.Commit()
}

// Status returns the migration status.
type Status struct {
	CurrentVersion int
	LatestVersion  int
	PendingCount   int
	Applied        []AppliedMigration
}

// AppliedMigration represents an applied migration.
type AppliedMigration struct {
	Version     int
	Description string
	AppliedAt   time.Time
}

// Status returns the current migration status.
func (m *Migrator) Status(ctx context.Context, migrations []Migration) (*Status, error) {
	if err := m.Init(ctx); err != nil {
		return nil, err
	}

	currentVersion, err := m.CurrentVersion(ctx)
	if err != nil {
		return nil, err
	}

	// Get applied migrations
	rows, err := m.db.QueryContext(ctx, "SELECT version, description, applied_at FROM schema_versions ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("failed to query applied migrations: %w", err)
	}
	defer rows.Close()

	var applied []AppliedMigration
	for rows.Next() {
		var am AppliedMigration
		if err := rows.Scan(&am.Version, &am.Description, &am.AppliedAt); err != nil {
			return nil, fmt.Errorf("failed to scan applied migration: %w", err)
		}
		applied = append(applied, am)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Calculate latest and pending
	latestVersion := 0
	for _, mig := range migrations {
		if mig.Version > latestVersion {
			latestVersion = mig.Version
		}
	}

	pendingCount := 0
	for _, mig := range migrations {
		if mig.Version > currentVersion {
			pendingCount++
		}
	}

	return &Status{
		CurrentVersion: currentVersion,
		LatestVersion:  latestVersion,
		PendingCount:   pendingCount,
		Applied:        applied,
	}, nil
}
