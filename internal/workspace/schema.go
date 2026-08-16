package workspace

import (
	"database/sql"
	"fmt"
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
	sync_error        TEXT,
	workspace_mode    TEXT NOT NULL DEFAULT 'standard',
	upstream_url      TEXT,
	integration_branch TEXT
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

// carryPatchFieldDDL contains ALTER TABLE statements that add the three
// carry-patch columns to an existing workspaces table. Each statement is
// executed individually; existing columns are skipped so the migration is
// idempotent (15-REQ-1.E1).
var carryPatchFieldDDL = []string{
	`ALTER TABLE workspaces ADD COLUMN workspace_mode TEXT NOT NULL DEFAULT 'standard'`,
	`ALTER TABLE workspaces ADD COLUMN upstream_url TEXT`,
	`ALTER TABLE workspaces ADD COLUMN integration_branch TEXT`,
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

	// Apply sync field migrations idempotently. Query existing columns first
	// so we only run ALTER TABLE for genuinely missing columns, avoiding
	// noisy "duplicate column" errors on every startup (13-REQ-1.E1).
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

	// Apply carry-patch field migrations idempotently (15-REQ-1.1).
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

	return nil
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
