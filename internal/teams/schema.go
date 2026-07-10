// Package teams implements team management for af-hub, providing team CRUD,
// lifecycle state management, and membership endpoints.
package teams

import (
	"database/sql"
	"embed"
)

// MigrationsFS holds the embedded SQL migration files for the teams schema.
// When implemented, this will be populated via a //go:embed directive pointing
// to the migrations directory containing sequentially-numbered .sql files.
//
// Adapts spec 03-REQ-1.3 to the project's boot-time DDL pattern (see
// docs/errata/03_migration_and_schema_divergences.md).
var MigrationsFS embed.FS

// InitSchema applies the teams and team_members DDL to the given database.
// It creates tables idempotently and sets up partial UNIQUE indexes.
//
// The function reads SQL from MigrationsFS and executes each file in
// ascending numeric-prefix order. Calling InitSchema multiple times on
// the same database is safe — CREATE TABLE IF NOT EXISTS and
// CREATE UNIQUE INDEX IF NOT EXISTS ensure idempotency.
func InitSchema(db *sql.DB) error {
	return nil // stub: schema creation not yet implemented
}
