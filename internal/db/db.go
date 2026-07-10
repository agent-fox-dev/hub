// Package db handles SQLite database initialization for af-hub.
// It opens or creates the database at the configured path, applies
// required pragmas (WAL, foreign_keys, busy_timeout), and creates
// all tables and indexes using CREATE TABLE/INDEX IF NOT EXISTS.
//
// Implementation will be added in task group 10.
package db

import "database/sql"

// InitDatabase opens or creates the SQLite database at the given path,
// creates parent directories if needed (mkdir -p equivalent), applies
// pragmas in order (journal_mode=WAL, foreign_keys=ON, busy_timeout=5000),
// and creates all tables and indexes.
//
// Returns a *sql.DB handle or an error. Fatal errors (directory creation
// failure, DB open failure, PRAGMA failure) should be handled by the caller
// (typically by logging fatal and exiting).
//
// The function does NOT call SetMaxOpenConns or SetMaxIdleConns — it relies
// on the busy_timeout pragma for write contention handling.
func InitDatabase(dbPath string) (*sql.DB, error) {
	// Stub: returns nil DB and nil error.
	// Implementation will be added in task group 10.
	return nil, nil
}

// ResetReadyzCounter resets the readyz failure counter to 0.
// Exported for testing purposes.
func ResetReadyzCounter() {
	// Stub: no-op until task group 10.
}
