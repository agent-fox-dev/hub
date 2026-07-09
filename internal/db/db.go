// Package db handles SQLite database initialization with WAL mode.
package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // Pure-Go SQLite driver; registers "sqlite" driver.
)

// OpenDatabase opens a SQLite database at the given path, enables WAL mode,
// and returns the database handle. On any failure it returns a nil handle and
// a non-nil error; no open connection is left behind.
func OpenDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}

	// Verify the connection is usable (sql.Open may defer actual opening).
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: ping %s: %w", path, err)
	}

	// Enable WAL mode immediately after opening, before any schema work.
	var journalMode string
	err = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&journalMode)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("db: enable WAL mode: %w", err)
	}
	if journalMode != "wal" {
		db.Close()
		return nil, fmt.Errorf("db: expected journal_mode 'wal', got %q", journalMode)
	}

	// Enable foreign key enforcement — SQLite disables it by default.
	// This must be set per-connection before any schema or DML statements
	// that depend on FK constraints (e.g. workspaces.team_id → teams(id)).
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("db: enable foreign_keys: %w", err)
	}

	return db, nil
}

// InitSchema creates all required tables using CREATE TABLE IF NOT EXISTS.
// It must be called after OpenDatabase (which enables WAL mode first).
// On any failure, it returns a non-nil error identifying the failing table.
func InitSchema(db *sql.DB) error {
	tables := []struct {
		name string
		ddl  string
	}{
		{
			name: "users",
			ddl: `CREATE TABLE IF NOT EXISTS users (
				id TEXT PRIMARY KEY,
				username TEXT UNIQUE NOT NULL,
				email TEXT,
				full_name TEXT,
				provider TEXT NOT NULL,
				provider_id TEXT NOT NULL,
				status TEXT DEFAULT 'active',
				created_at TEXT,
				updated_at TEXT,
				UNIQUE(provider, provider_id)
			)`,
		},
		{
			name: "teams",
			ddl: `CREATE TABLE IF NOT EXISTS teams (
				id TEXT PRIMARY KEY,
				name TEXT UNIQUE NOT NULL,
				slug TEXT UNIQUE NOT NULL,
				url TEXT UNIQUE NOT NULL,
				status TEXT DEFAULT 'active',
				created_at TEXT,
				created_by TEXT REFERENCES users(id)
			)`,
		},
		{
			name: "team_members",
			ddl: `CREATE TABLE IF NOT EXISTS team_members (
				team_id TEXT REFERENCES teams(id),
				user_id TEXT REFERENCES users(id),
				role TEXT NOT NULL,
				created_at TEXT,
				granted_by TEXT REFERENCES users(id),
				PRIMARY KEY (user_id, team_id)
			)`,
		},
		{
			name: "api_keys",
			ddl: `CREATE TABLE IF NOT EXISTS api_keys (
				id TEXT PRIMARY KEY,
				key_id TEXT UNIQUE,
				key_hash TEXT,
				team_id TEXT REFERENCES teams(id),
				user_id TEXT REFERENCES users(id),
				role TEXT,
				label TEXT,
				expires_at TEXT,
				revoked_at TEXT,
				created_at TEXT
			)`,
		},
		{
			name: "admin_tokens",
			ddl: `CREATE TABLE IF NOT EXISTS admin_tokens (
				id TEXT PRIMARY KEY,
				token_hash TEXT,
				created_at TEXT
			)`,
		},
		{
			// workspaces: maps to a git repository (+ optional branch),
			// owned by a user, optionally belonging to a team.
			// Depends on: users table (owner_id FK), teams table (team_id FK).
			name: "workspaces",
			ddl: `CREATE TABLE IF NOT EXISTS workspaces (
				id TEXT PRIMARY KEY,
				slug TEXT UNIQUE NOT NULL,
				git_url TEXT NOT NULL,
				branch TEXT,
				owner_id TEXT NOT NULL REFERENCES users(id),
				team_id TEXT REFERENCES teams(id),
				status TEXT NOT NULL DEFAULT 'active',
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
		},
	}

	for _, t := range tables {
		if _, err := db.Exec(t.ddl); err != nil {
			return fmt.Errorf("db: create table %s: %w", t.name, err)
		}
	}

	return nil
}
