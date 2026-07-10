// Package teams implements team management for af-hub, providing team CRUD,
// lifecycle state management, and membership endpoints.
package teams

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// MigrationsFS holds the embedded SQL migration files for the teams schema.
// Files are stored under migrations/ with sequential numeric prefixes
// (e.g. 001_create_teams.sql) and executed in ascending order.
//
// Adapts spec 03-REQ-1.3 to the project's boot-time DDL pattern (see
// docs/errata/03_migration_and_schema_divergences.md).
//
//go:embed migrations/*.sql
var MigrationsFS embed.FS

// InitSchema applies the teams and team_members DDL to the given database.
// It reads SQL files from MigrationsFS and executes each in ascending
// numeric-prefix order. Calling InitSchema multiple times is safe because
// the SQL uses CREATE TABLE IF NOT EXISTS and CREATE UNIQUE INDEX IF NOT EXISTS.
func InitSchema(db *sql.DB) error {
	entries, err := fs.ReadDir(MigrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("reading migration files: %w", err)
	}

	// Collect and sort SQL files by name (numeric prefix ensures order).
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	// Execute each migration file in order.
	for _, f := range files {
		content, err := fs.ReadFile(MigrationsFS, "migrations/"+f)
		if err != nil {
			return fmt.Errorf("reading migration file %s: %w", f, err)
		}
		if _, err := db.Exec(string(content)); err != nil {
			return fmt.Errorf("executing migration %s: %w", f, err)
		}
	}

	return nil
}
