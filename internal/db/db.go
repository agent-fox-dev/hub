// Package db handles SQLite database initialization for af-hub.
// It opens or creates the database at the configured path, applies
// required pragmas (WAL, foreign_keys, busy_timeout), and creates
// all tables and indexes using CREATE TABLE/INDEX IF NOT EXISTS.
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

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
	// Step 1: Create parent directories if needed (mkdir -p equivalent).
	parentDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory %q: %w", parentDir, err)
	}

	// Step 2: Open or create the SQLite database using modernc.org/sqlite driver.
	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database at %q: %w", dbPath, err)
	}

	// Verify the connection is usable.
	if err := database.Ping(); err != nil {
		database.Close()
		return nil, fmt.Errorf("failed to open database at %q: %w", dbPath, err)
	}

	// Step 3: Apply pragmas in order. Each failure identifies the specific PRAGMA.
	pragmas := []struct {
		name string
		stmt string
	}{
		{"journal_mode", "PRAGMA journal_mode = WAL"},
		{"foreign_keys", "PRAGMA foreign_keys = ON"},
		{"busy_timeout", "PRAGMA busy_timeout = 5000"},
	}

	for _, p := range pragmas {
		if _, err := database.Exec(p.stmt); err != nil {
			database.Close()
			return nil, fmt.Errorf("failed to apply PRAGMA %s: %w", p.name, err)
		}
	}

	// Step 4: Create all tables and indexes.
	if err := createSchema(database); err != nil {
		database.Close()
		return nil, fmt.Errorf("failed to create database schema: %w", err)
	}

	return database, nil
}

// createSchema executes all CREATE TABLE IF NOT EXISTS and CREATE INDEX IF NOT EXISTS
// statements for the af-hub database schema.
//
// 7 tables: users, admin_tokens, api_keys, teams, team_members, workspaces, workspace_tokens
// 5 indexes: idx_api_keys_key_id, idx_workspace_tokens_token_id, idx_users_provider,
//
//	idx_workspaces_slug, idx_teams_slug
//
// Note: REQ-3.3 says "eight tables" but lists only 7 names. The correct count
// is 7 tables — this is a known spec error (see reviewer findings and errata).
func createSchema(db *sql.DB) error {
	const schema = `
	CREATE TABLE IF NOT EXISTS users (
		id          TEXT PRIMARY KEY,
		username    TEXT NOT NULL UNIQUE,
		email       TEXT NOT NULL,
		full_name   TEXT NOT NULL DEFAULT '',
		status      TEXT NOT NULL DEFAULT 'active',
		provider    TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z'),
		updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z'),
		UNIQUE (provider, provider_id)
	);

	CREATE TABLE IF NOT EXISTS admin_tokens (
		id          TEXT PRIMARY KEY,
		token_hash  TEXT NOT NULL,
		created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z')
	);

	CREATE TABLE IF NOT EXISTS api_keys (
		id          TEXT PRIMARY KEY,
		key_id      TEXT NOT NULL UNIQUE,
		secret_hash TEXT NOT NULL,
		user_id     TEXT NOT NULL REFERENCES users(id),
		expires_at  TEXT,
		created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z'),
		revoked_at  TEXT
	);

	CREATE TABLE IF NOT EXISTS teams (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL UNIQUE,
		slug        TEXT NOT NULL UNIQUE,
		url         TEXT NOT NULL DEFAULT '',
		status      TEXT NOT NULL DEFAULT 'active',
		created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z'),
		updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z')
	);

	CREATE TABLE IF NOT EXISTS team_members (
		team_id    TEXT NOT NULL REFERENCES teams(id),
		user_id    TEXT NOT NULL REFERENCES users(id),
		created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z'),
		PRIMARY KEY (team_id, user_id)
	);

	CREATE TABLE IF NOT EXISTS workspaces (
		id         TEXT PRIMARY KEY,
		slug       TEXT NOT NULL UNIQUE,
		git_url    TEXT NOT NULL,
		branch     TEXT,
		owner_id   TEXT NOT NULL REFERENCES users(id),
		team_id    TEXT REFERENCES teams(id),
		status     TEXT NOT NULL DEFAULT 'active',
		created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z'),
		updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z')
	);

	CREATE TABLE IF NOT EXISTS workspace_tokens (
		id           TEXT PRIMARY KEY,
		token_id     TEXT NOT NULL UNIQUE,
		secret_hash  TEXT NOT NULL,
		workspace_id TEXT NOT NULL REFERENCES workspaces(id),
		user_id      TEXT NOT NULL REFERENCES users(id),
		label        TEXT,
		expires_at   TEXT,
		created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z'),
		revoked_at   TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_api_keys_key_id
		ON api_keys(key_id);

	CREATE INDEX IF NOT EXISTS idx_workspace_tokens_token_id
		ON workspace_tokens(token_id);

	CREATE INDEX IF NOT EXISTS idx_users_provider
		ON users(provider, provider_id);

	CREATE INDEX IF NOT EXISTS idx_workspaces_slug
		ON workspaces(slug);

	CREATE INDEX IF NOT EXISTS idx_teams_slug
		ON teams(slug);
	`

	_, err := db.Exec(schema)
	return err
}
