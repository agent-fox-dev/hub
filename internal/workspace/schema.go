package workspace

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// createTableSQL is the DDL for the workspaces table.
const createTableSQL = `
CREATE TABLE IF NOT EXISTS workspaces (
	slug              TEXT PRIMARY KEY,
	git_url           TEXT NOT NULL,
	branch            TEXT,
	owner_id          TEXT NOT NULL,
	org_id            TEXT,
	status            TEXT NOT NULL DEFAULT 'active',
	display_name      TEXT NOT NULL DEFAULT '',
	description       TEXT NOT NULL DEFAULT '',
	clone_status      TEXT NOT NULL DEFAULT 'pending' CHECK(clone_status IN ('pending','cloning','ready','failed','archived')),
	head_sha          TEXT,
	clone_error       TEXT,
	created_at        TEXT NOT NULL,
	updated_at        TEXT NOT NULL,
	sync_mode         TEXT NOT NULL DEFAULT 'pull_only',
	sync_status       TEXT NOT NULL DEFAULT 'idle',
	upstream_head_sha TEXT,
	last_sync_at      TEXT,
	sync_error        TEXT
)`

// syncFieldDDL contains ALTER TABLE statements that add the five sync-related
// columns to an existing workspaces table. Each statement is executed
// individually; "duplicate column name" errors are logged and treated as
// no-ops so the migration is idempotent (13-REQ-1.E1).
var syncFieldDDL = []string{
	`ALTER TABLE workspaces ADD COLUMN sync_mode TEXT NOT NULL DEFAULT 'pull_only'`,
	`ALTER TABLE workspaces ADD COLUMN sync_status TEXT NOT NULL DEFAULT 'idle'`,
	`ALTER TABLE workspaces ADD COLUMN upstream_head_sha TEXT`,
	`ALTER TABLE workspaces ADD COLUMN last_sync_at TEXT`,
	`ALTER TABLE workspaces ADD COLUMN sync_error TEXT`,
}

// initSchema creates the workspaces table using CREATE TABLE IF NOT EXISTS
// and applies any pending ALTER TABLE migrations for sync-related columns.
// It is called during server boot to ensure the schema exists.
//
// The ALTER TABLE statements are idempotent: if a column already exists
// (e.g., because the table was freshly created with the column, or the
// migration was previously applied), the DDL error is logged and startup
// continues (13-REQ-1.E1).
func initSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	_, err := db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create workspaces table: %w", err)
	}

	// Apply sync field migrations idempotently. Each ALTER TABLE ADD COLUMN
	// is executed independently; "duplicate column" errors are expected and
	// logged as informational messages rather than fatal errors.
	for _, ddl := range syncFieldDDL {
		if _, err := db.Exec(ddl); err != nil {
			// 13-REQ-1.E1: If the column already exists, log and continue.
			if isDuplicateColumnError(err) {
				log.Printf("INFO: sync schema migration skipped (column already exists): %v", err)
				continue
			}
			return fmt.Errorf("sync schema migration failed: %w", err)
		}
	}

	return nil
}

// isDuplicateColumnError checks whether the error indicates a duplicate
// column in an ALTER TABLE ADD COLUMN statement. SQLite returns this as
// a generic error with a message containing "duplicate column name".
func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}
