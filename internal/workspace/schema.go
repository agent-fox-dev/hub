package workspace

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// createTableSQL is the DDL for the workspaces table.
const createTableSQL = `
CREATE TABLE IF NOT EXISTS workspaces (
	slug              TEXT PRIMARY KEY,
	git_url           TEXT NOT NULL,
	branch            TEXT,
	owner_id          TEXT NOT NULL,
	org_id            TEXT,
	status            TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','cloning','ready','failed','archived')),
	display_name      TEXT NOT NULL DEFAULT '',
	description       TEXT NOT NULL DEFAULT '',
	head_sha          TEXT,
	clone_error       TEXT,
	created_at        TEXT NOT NULL,
	updated_at        TEXT NOT NULL,
	upstream_head_sha TEXT,
	last_sync_at      TEXT,
	sync_error        TEXT,
	workspace_mode    TEXT NOT NULL DEFAULT 'standard',
	upstream_url      TEXT,
	integration_branch TEXT,
	settings          TEXT NOT NULL DEFAULT '{}'
)`

// syncFieldDDL contains ALTER TABLE statements that add the sync-related
// columns to an existing workspaces table. Each statement is executed
// individually; "duplicate column name" errors are logged and treated as
// no-ops so the migration is idempotent.
var syncFieldDDL = []string{
	`ALTER TABLE workspaces ADD COLUMN upstream_head_sha TEXT`,
	`ALTER TABLE workspaces ADD COLUMN last_sync_at TEXT`,
	`ALTER TABLE workspaces ADD COLUMN sync_error TEXT`,
}

// carryPatchFieldDDL contains ALTER TABLE statements that add the three
// carry-patch columns to an existing workspaces table. Each statement is
// executed individually; existing columns are skipped so the migration is
// idempotent.
var carryPatchFieldDDL = []string{
	`ALTER TABLE workspaces ADD COLUMN workspace_mode TEXT NOT NULL DEFAULT 'standard'`,
	`ALTER TABLE workspaces ADD COLUMN upstream_url TEXT`,
	`ALTER TABLE workspaces ADD COLUMN integration_branch TEXT`,
	// The settings document (see Settings); '{}' means every default.
	`ALTER TABLE workspaces ADD COLUMN settings TEXT NOT NULL DEFAULT '{}'`,
}

// createPatchesTableSQL is the DDL for the patches table.
const createPatchesTableSQL = `
CREATE TABLE IF NOT EXISTS patches (
	id              TEXT PRIMARY KEY,
	workspace_slug  TEXT NOT NULL,
	branch_name     TEXT NOT NULL,
	position        INTEGER NOT NULL,
	status          TEXT NOT NULL DEFAULT 'active',
	conflict_files  TEXT,
	upstream_pr_url TEXT,
	description     TEXT,
	deleted_at      TEXT,
	added_at        TEXT NOT NULL,
	updated_at      TEXT NOT NULL,
	UNIQUE(workspace_slug, branch_name),
	UNIQUE(workspace_slug, position)
)`

// patchFieldDDL contains ALTER TABLE statements that add columns to an
// existing patches table. Each statement is executed individually; existing
// columns are skipped so the migration is idempotent.
var patchFieldDDL = []string{
	`ALTER TABLE patches ADD COLUMN conflict_files TEXT`,
	`ALTER TABLE patches ADD COLUMN deleted_at TEXT`,
	`ALTER TABLE patches ADD COLUMN origin_sync_state TEXT`,
	`ALTER TABLE patches ADD COLUMN origin_sha TEXT`,
	`ALTER TABLE patches ADD COLUMN origin_synced_at TEXT`,
}

// InitSchema creates the workspaces table using CREATE TABLE IF NOT EXISTS
// and applies any pending ALTER TABLE migrations for sync-related columns.
// It is called during server boot to ensure the schema exists.
//
// The ALTER TABLE statements are idempotent: if a column already exists
// (e.g., because the table was freshly created with the column, or the
// migration was previously applied), the DDL error is logged and startup
// continues.
func InitSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	_, err := db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create workspaces table: %w", err)
	}

	// Apply sync field migrations idempotently. Query existing columns first
	// so we only run ALTER TABLE for genuinely missing columns, avoiding
	// noisy "duplicate column" errors on every startup.
	existing, err := existingColumns(db, "workspaces")
	if err != nil {
		return fmt.Errorf("sync schema migration: %w", err)
	}
	for _, ddl := range syncFieldDDL {
		col := extractColumnName(ddl)
		if col != "" && existing[col] {
			continue
		}
		if _, err := db.Exec(ddl); err != nil {
			if isDuplicateColumnError(err) {
				continue
			}
			return fmt.Errorf("sync schema migration failed: %w", err)
		}
	}

	// Apply carry-patch field migrations idempotently.
	// Re-query existing columns to account for columns that may have been
	// added by the CREATE TABLE DDL above (fresh database case).
	existing, err = existingColumns(db, "workspaces")
	if err != nil {
		return fmt.Errorf("carry-patch schema migration: %w", err)
	}
	for _, ddl := range carryPatchFieldDDL {
		col := extractColumnName(ddl)
		if col != "" && existing[col] {
			continue
		}
		if _, err := db.Exec(ddl); err != nil {
			if isDuplicateColumnError(err) {
				continue
			}
			return fmt.Errorf("carry-patch schema migration failed: %w", err)
		}
	}

	// Fold the clone_status, sync_status and sync_mode columns of a table
	// from before the one status column.
	existing, err = existingColumns(db, "workspaces")
	if err != nil {
		return fmt.Errorf("status schema migration: %w", err)
	}
	if err := foldStatusColumns(db, existing); err != nil {
		return fmt.Errorf("status schema migration failed: %w", err)
	}

	// Create the patches table.
	if _, err := db.Exec(createPatchesTableSQL); err != nil {
		return fmt.Errorf("failed to create patches table: %w", err)
	}

	// Apply patches table field migrations idempotently.
	existing, err = existingColumns(db, "patches")
	if err != nil {
		return fmt.Errorf("patches schema migration: %w", err)
	}
	for _, ddl := range patchFieldDDL {
		col := extractColumnName(ddl)
		if col != "" && existing[col] {
			continue
		}
		if _, err := db.Exec(ddl); err != nil {
			if isDuplicateColumnError(err) {
				continue
			}
			return fmt.Errorf("patches schema migration failed: %w", err)
		}
	}

	// The token workspace bindings, which deleteWorkspace reads to retire a
	// deleted workspace's tokens.
	if err := wsaccess.InitBindingSchema(db); err != nil {
		return fmt.Errorf("token binding schema init: %w", err)
	}

	return nil
}

// foldStatusColumns rewrites a workspaces table that still has the
// clone_status, sync_status and sync_mode columns into the current shape, in
// one transaction: status takes the clone state ('archived' for an archived
// workspace), a sync_mode of 'disabled' becomes the sync_enabled setting, a
// sync left 'syncing' by the old hub becomes a sync_error, and the three
// columns go. SQLite cannot change a CHECK constraint, so the table is
// rebuilt and its rows copied. A table without clone_status is left alone.
func foldStatusColumns(db *sql.DB, existing map[string]bool) error {
	if !existing["clone_status"] {
		return nil
	}
	settings := "settings"
	if existing["sync_mode"] {
		settings = "CASE WHEN sync_mode = 'disabled' THEN json_set(settings, '$.sync_enabled', json('false')) ELSE settings END"
	}
	syncError := "sync_error"
	if existing["sync_status"] {
		syncError = "CASE WHEN sync_status = 'syncing' THEN 'sync interrupted by server restart' ELSE sync_error END"
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		strings.Replace(createTableSQL, "IF NOT EXISTS workspaces", "workspaces_fold", 1),
		`INSERT INTO workspaces_fold (slug, git_url, branch, owner_id, org_id, status, display_name, description, head_sha, clone_error, created_at, updated_at, upstream_head_sha, last_sync_at, sync_error, workspace_mode, upstream_url, integration_branch, settings)
		 SELECT slug, git_url, branch, owner_id, org_id, CASE WHEN status = 'archived' THEN 'archived' ELSE clone_status END, display_name, description, head_sha, clone_error, created_at, updated_at, upstream_head_sha, last_sync_at, ` + syncError + `, workspace_mode, upstream_url, integration_branch, ` + settings + `
		 FROM workspaces`,
		`DROP TABLE workspaces`,
		`ALTER TABLE workspaces_fold RENAME TO workspaces`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// existingColumns returns the set of column names present in the given table.
func existingColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// extractColumnName parses "ALTER TABLE ... ADD COLUMN <name> ..." and
// returns the column name, or "" if the DDL doesn't match.
func extractColumnName(ddl string) string {
	const marker = "ADD COLUMN "
	idx := strings.Index(strings.ToUpper(ddl), marker)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(ddl[idx+len(marker):])
	if sp := strings.IndexByte(rest, ' '); sp > 0 {
		return rest[:sp]
	}
	return rest
}

// isDuplicateColumnError checks whether the error indicates a duplicate
// column in an ALTER TABLE ADD COLUMN statement. SQLite returns this as
// a generic error with a message containing "duplicate column name".
func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}
