package workspace

import (
	"database/sql"
	"fmt"
)

// createTableSQL is the DDL for the workspaces table.
const createTableSQL = `
CREATE TABLE IF NOT EXISTS workspaces (
	slug       TEXT PRIMARY KEY,
	git_url    TEXT NOT NULL,
	branch     TEXT,
	owner_id   TEXT NOT NULL,
	org_id     TEXT,
	status     TEXT NOT NULL DEFAULT 'active',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`

// initSchema creates the workspaces table using CREATE TABLE IF NOT EXISTS.
// It is called during server boot to ensure the schema exists.
func initSchema(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	_, err := db.Exec(createTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create workspaces table: %w", err)
	}
	return nil
}
