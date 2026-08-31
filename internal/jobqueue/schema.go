package jobqueue

import (
	"database/sql"
	"fmt"
)

// createJobsTableSQL is the DDL for the jobs table.
// The caller's *sql.DB must be configured with PRAGMA journal_mode=WAL and
// PRAGMA busy_timeout before calling InitSchema. InitSchema does NOT set WAL
// mode itself; concurrent workers may encounter SQLITE_BUSY errors under load
// if the caller has not configured WAL mode.
const createJobsTableSQL = `
CREATE TABLE IF NOT EXISTS jobs (
	id           TEXT    NOT NULL PRIMARY KEY,
	type         TEXT    NOT NULL,
	key          TEXT    NOT NULL,
	group_key    TEXT    NOT NULL DEFAULT '',
	nonce        TEXT    NOT NULL,
	status       TEXT    NOT NULL DEFAULT 'queued',
	payload      TEXT    NOT NULL DEFAULT '{}',
	result       TEXT,
	error        TEXT,
	progress     TEXT,
	retry_count  INTEGER NOT NULL DEFAULT 0,
	available_at TEXT    NOT NULL,
	submitted_by TEXT    NOT NULL,
	created_at   TEXT    NOT NULL,
	updated_at   TEXT    NOT NULL
);`

// Index DDL statements. Each uses CREATE INDEX IF NOT EXISTS for idempotency.
const (
	createIdxNonceSQL = `CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_nonce
		ON jobs (nonce);`

	createIdxTypeKeyStatusSQL = `CREATE INDEX IF NOT EXISTS idx_jobs_type_key_status
		ON jobs (type, key, status);`

	createIdxStatusAvailableAtSQL = `CREATE INDEX IF NOT EXISTS idx_jobs_status_available_at
		ON jobs (status, available_at);`

	createIdxTypeCreatedAtSQL = `CREATE INDEX IF NOT EXISTS idx_jobs_type_created_at
		ON jobs (type, created_at);`
)

// InitSchema creates the jobs table and required indexes using
// CREATE TABLE IF NOT EXISTS and CREATE INDEX IF NOT EXISTS.
// The caller's *sql.DB must be configured with PRAGMA journal_mode=WAL
// and PRAGMA busy_timeout before calling this function.
//
// InitSchema is idempotent: calling it multiple times on the same database
// produces the same schema as calling it once, and existing data is preserved.
func InitSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("jobqueue: InitSchema: db must not be nil")
	}

	stmts := []string{
		createJobsTableSQL,
		createIdxNonceSQL,
		createIdxTypeKeyStatusSQL,
		createIdxStatusAvailableAtSQL,
		createIdxTypeCreatedAtSQL,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("jobqueue: InitSchema: %w", err)
		}
	}

	return nil
}
