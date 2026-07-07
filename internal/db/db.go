// Package db handles SQLite database initialization with WAL mode.
package db

import (
	"database/sql"
)

// OpenDatabase opens a SQLite database at the given path, enables WAL mode,
// and returns the database handle.
func OpenDatabase(path string) (*sql.DB, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// InitSchema creates all required tables using CREATE TABLE IF NOT EXISTS.
func InitSchema(db *sql.DB) error {
	// Stub — implementation in a later task group.
	return nil
}
