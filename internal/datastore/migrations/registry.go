package migrations

// All contains all database migrations in order.
// New migrations should be appended to this slice.
var All = []Migration{
	{
		Version:     1,
		Description: "Initial schema",
		Up: `
			CREATE TABLE IF NOT EXISTS instances (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL UNIQUE,
				provider TEXT NOT NULL,
				state TEXT NOT NULL,
				spec TEXT NOT NULL,
				handle TEXT NOT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				started_at TIMESTAMP,
				labels TEXT,
				annotations TEXT
			);

			CREATE INDEX IF NOT EXISTS idx_instances_provider ON instances(provider);
			CREATE INDEX IF NOT EXISTS idx_instances_state ON instances(state);
			CREATE INDEX IF NOT EXISTS idx_instances_created_at ON instances(created_at);

			CREATE TABLE IF NOT EXISTS events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				instance_id TEXT NOT NULL,
				type TEXT NOT NULL,
				state TEXT NOT NULL,
				message TEXT,
				metadata TEXT,
				timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE
			);

			CREATE INDEX IF NOT EXISTS idx_events_instance_id ON events(instance_id);
			CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);
			CREATE INDEX IF NOT EXISTS idx_events_type ON events(type);
		`,
		Down: `
			DROP TABLE IF EXISTS events;
			DROP TABLE IF EXISTS instances;
		`,
	},
	{
		Version:     2,
		Description: "Add instance_name index for faster lookups",
		Up: `
			CREATE INDEX IF NOT EXISTS idx_instances_name ON instances(name);
		`,
		Down: `
			DROP INDEX IF EXISTS idx_instances_name;
		`,
	},
	{
		Version:     3,
		Description: "Add backup_config column to instances table",
		Up: `
			ALTER TABLE instances ADD COLUMN backup_config TEXT;
		`,
		Down: `
			-- No-op: down-then-up cannot round-trip. SQLite (as shipped on the
			-- supported hosts) cannot DROP COLUMN, so the backup_config column
			-- stays; re-running Up afterwards fails with "duplicate column
			-- name". This migration is released and must not be edited.
		`,
	},
	{
		Version:     4,
		Description: "Add stacks and stack_instances tables for stack persistence",
		Up: `
			CREATE TABLE IF NOT EXISTS stacks (
				name TEXT PRIMARY KEY,
				status TEXT NOT NULL DEFAULT 'pending',
				manifest TEXT,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			);

			CREATE TABLE IF NOT EXISTS stack_instances (
				stack_name TEXT NOT NULL,
				instance_name TEXT NOT NULL,
				instance_id TEXT NOT NULL,
				provider TEXT NOT NULL,
				depends_on TEXT,
				status TEXT NOT NULL DEFAULT 'pending',
				health TEXT NOT NULL DEFAULT 'unknown',
				handle TEXT NOT NULL,
				deploy_order INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (stack_name, instance_name),
				FOREIGN KEY (stack_name) REFERENCES stacks(name) ON DELETE CASCADE
			);

			CREATE INDEX IF NOT EXISTS idx_stack_instances_stack ON stack_instances(stack_name);
			CREATE INDEX IF NOT EXISTS idx_stack_instances_instance ON stack_instances(instance_id);
			CREATE INDEX IF NOT EXISTS idx_stacks_status ON stacks(status);
		`,
		Down: `
			DROP TABLE IF EXISTS stack_instances;
			DROP TABLE IF EXISTS stacks;
		`,
	},
	{
		Version:     5,
		Description: "Add jobs table for async job tracking",
		Up: `
			CREATE TABLE IF NOT EXISTS jobs (
				id TEXT PRIMARY KEY,
				type TEXT NOT NULL,
				description TEXT NOT NULL,
				status TEXT NOT NULL DEFAULT 'pending',
				progress REAL NOT NULL DEFAULT 0,
				message TEXT,
				result TEXT,
				error TEXT,
				metadata TEXT,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				started_at TIMESTAMP,
				completed_at TIMESTAMP
			);

			CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
			CREATE INDEX IF NOT EXISTS idx_jobs_type ON jobs(type);
			CREATE INDEX IF NOT EXISTS idx_jobs_created_at ON jobs(created_at);
		`,
		Down: `
			DROP TABLE IF EXISTS jobs;
		`,
	},
	{
		Version:     6,
		Description: "Extract frequently queried columns from InstanceSpec JSON blob",
		Up: `
			ALTER TABLE instances ADD COLUMN cpus INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE instances ADD COLUMN memory_mb INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE instances ADD COLUMN os_type TEXT;
			ALTER TABLE instances ADD COLUMN arch TEXT;
			ALTER TABLE instances ADD COLUMN image TEXT;

			-- Backfill the new columns from the existing spec JSON blob so rows
			-- created before this migration are queryable by the SQL filters that
			-- read these columns (not just rows updated later).
			UPDATE instances SET
				cpus = COALESCE(json_extract(spec, '$.cpus'), 0),
				memory_mb = COALESCE(json_extract(spec, '$.memory_mb'), 0),
				os_type = json_extract(spec, '$.os_type'),
				arch = json_extract(spec, '$.arch'),
				image = json_extract(spec, '$.image');

			CREATE INDEX IF NOT EXISTS idx_instances_cpus ON instances(cpus);
			CREATE INDEX IF NOT EXISTS idx_instances_memory_mb ON instances(memory_mb);
			CREATE INDEX IF NOT EXISTS idx_instances_os_type ON instances(os_type);
			CREATE INDEX IF NOT EXISTS idx_instances_arch ON instances(arch);
		`,
		Down: `
			-- No-op: down-then-up cannot round-trip. SQLite (as shipped on the
			-- supported hosts) cannot DROP COLUMN, so the extracted columns
			-- stay; re-running Up afterwards fails with "duplicate column
			-- name". This migration is released and must not be edited.
		`,
	},
	{
		Version:     7,
		Description: "Add api_keys table so API keys created at runtime survive restarts",
		Up: `
			CREATE TABLE IF NOT EXISTS api_keys (
				id           TEXT PRIMARY KEY,
				name         TEXT NOT NULL,
				hashed_key   TEXT NOT NULL,
				permissions  TEXT NOT NULL DEFAULT '[]',
				created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				last_used_at TIMESTAMP,
				expires_at   TIMESTAMP
			);
		`,
		Down: `
			DROP TABLE IF EXISTS api_keys;
		`,
	},
	{
		Version:     8,
		Description: "Add backups table so backup records survive a restart",
		Up: `
			CREATE TABLE IF NOT EXISTS backups (
				id            TEXT PRIMARY KEY,
				instance_id   TEXT NOT NULL,
				record        TEXT NOT NULL,
				created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			);
			CREATE INDEX IF NOT EXISTS idx_backups_instance ON backups(instance_id);
		`,
		Down: `
			DROP TABLE IF EXISTS backups;
		`,
	},
}
