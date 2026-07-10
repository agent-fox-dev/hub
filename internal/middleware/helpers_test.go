package middleware_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupTestDB creates a temporary SQLite database for testing.
// Returns a *sql.DB with WAL, foreign_keys, and busy_timeout pragmas applied.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	// Apply pragmas as InitDatabase would.
	if _, err := database.Exec("PRAGMA journal_mode = WAL"); err != nil {
		t.Fatalf("failed to set WAL: %v", err)
	}
	if _, err := database.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("failed to set foreign_keys: %v", err)
	}
	if _, err := database.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		t.Fatalf("failed to set busy_timeout: %v", err)
	}

	t.Cleanup(func() { database.Close() })
	return database
}

// setupBrokenDB returns a *sql.DB that is closed, so queries will fail.
// Used to verify that structurally invalid tokens are rejected without
// any DB queries — if a DB query is attempted, it will produce a 503
// or error instead of the expected 401.
func setupBrokenDB(t *testing.T) *sql.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "broken.db")

	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	// Close immediately so any subsequent queries fail.
	database.Close()

	// Remove the file to ensure even a reconnect attempt fails.
	os.Remove(dbPath)

	return database
}

// ---------------------------------------------------------------------------
// Full-schema helpers for auth middleware credential verification tests
// (task group 6)
// ---------------------------------------------------------------------------

// authTestSchema is the full DDL needed for auth middleware tests, covering
// all tables referenced during token verification: users, admin_tokens,
// api_keys, teams, workspaces, workspace_tokens.
const authTestSchema = `
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
    id              TEXT PRIMARY KEY,
    key_id          TEXT NOT NULL UNIQUE,
    secret_hash     TEXT NOT NULL,
    user_id         TEXT NOT NULL REFERENCES users(id),
    expires_at      TEXT,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f', 'now') || 'Z'),
    revoked_at      TEXT,
    expires_in_days INTEGER
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

CREATE INDEX IF NOT EXISTS idx_api_keys_key_id ON api_keys(key_id);
CREATE INDEX IF NOT EXISTS idx_workspace_tokens_token_id ON workspace_tokens(token_id);
CREATE INDEX IF NOT EXISTS idx_users_provider ON users(provider, provider_id);
CREATE INDEX IF NOT EXISTS idx_workspaces_slug ON workspaces(slug);
CREATE INDEX IF NOT EXISTS idx_teams_slug ON teams(slug);
`

// setupAuthTestDBWithSchema creates a temporary SQLite database with all
// tables needed for auth middleware credential verification tests.
// Returns a *sql.DB with WAL, foreign_keys, busy_timeout pragmas applied
// and full schema created.
func setupAuthTestDBWithSchema(t *testing.T) *sql.DB {
	t.Helper()
	db := setupTestDB(t)
	if _, err := db.Exec(authTestSchema); err != nil {
		t.Fatalf("failed to create auth test schema: %v", err)
	}
	return db
}

// sha256hex returns the hex-encoded SHA-256 hash of the given string.
func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// nowISO returns the current UTC time as an ISO 8601 string.
func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// pastISO returns an ISO 8601 timestamp for the given duration in the past.
func pastISO(d time.Duration) string {
	return time.Now().UTC().Add(-d).Format(time.RFC3339Nano)
}

// futureISO returns an ISO 8601 timestamp for the given duration in the future.
func futureISO(d time.Duration) string {
	return time.Now().UTC().Add(d).Format(time.RFC3339Nano)
}

// insertUser inserts a user row into the test database.
func insertUser(t *testing.T, db *sql.DB, userID, username, status string) {
	t.Helper()
	now := nowISO()
	_, err := db.Exec(
		`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
		 VALUES (?, ?, ?, '', ?, 'local', ?, ?, ?)`,
		userID, username, username+"@test.com", status, "local_"+userID, now, now,
	)
	if err != nil {
		t.Fatalf("insertUser(%s, %s, %s): %v", userID, username, status, err)
	}
}

// insertAdminTokenHash inserts an admin_tokens row with SHA-256 of the given suffix.
// Returns the token hash that was stored.
func insertAdminTokenHash(t *testing.T, db *sql.DB, suffix string) string {
	t.Helper()
	tokenHash := sha256hex(suffix)
	now := nowISO()
	_, err := db.Exec(
		"INSERT INTO admin_tokens (id, token_hash, created_at) VALUES (?, ?, ?)",
		"admin-tok-"+suffix[:8], tokenHash, now,
	)
	if err != nil {
		t.Fatalf("insertAdminTokenHash: %v", err)
	}
	return tokenHash
}

// insertAPIKey inserts an api_keys row for the given user, with SHA-256 of
// the raw secret stored as secret_hash.
// expiresAt and revokedAt may be nil for NULL.
func insertAPIKey(t *testing.T, db *sql.DB, keyID, secret, userID string, expiresAt, revokedAt *string) {
	t.Helper()
	secretHash := sha256hex(secret)
	now := nowISO()
	_, err := db.Exec(
		`INSERT INTO api_keys (id, key_id, secret_hash, user_id, expires_at, created_at, revoked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"apikey-"+keyID, keyID, secretHash, userID, expiresAt, now, revokedAt,
	)
	if err != nil {
		t.Fatalf("insertAPIKey(%s, user=%s): %v", keyID, userID, err)
	}
}

// insertWorkspace inserts a workspace row into the test database.
func insertWorkspace(t *testing.T, db *sql.DB, wsID, slug, ownerID string) {
	t.Helper()
	now := nowISO()
	_, err := db.Exec(
		`INSERT INTO workspaces (id, slug, git_url, branch, owner_id, team_id, status, created_at, updated_at)
		 VALUES (?, ?, 'https://github.com/test/repo.git', NULL, ?, NULL, 'active', ?, ?)`,
		wsID, slug, ownerID, now, now,
	)
	if err != nil {
		t.Fatalf("insertWorkspace(%s, %s): %v", wsID, slug, err)
	}
}

// insertWorkspaceToken inserts a workspace_tokens row with SHA-256 of the raw
// secret stored as secret_hash.
// expiresAt and revokedAt may be nil for NULL.
func insertWorkspaceToken(t *testing.T, db *sql.DB, tokenID, secret, wsID, userID string, expiresAt, revokedAt *string) {
	t.Helper()
	secretHash := sha256hex(secret)
	now := nowISO()
	_, err := db.Exec(
		`INSERT INTO workspace_tokens (id, token_id, secret_hash, workspace_id, user_id, label, expires_at, created_at, revoked_at)
		 VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?)`,
		"wstok-"+tokenID, tokenID, secretHash, wsID, userID, expiresAt, now, revokedAt,
	)
	if err != nil {
		t.Fatalf("insertWorkspaceToken(%s, ws=%s, user=%s): %v", tokenID, wsID, userID, err)
	}
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string {
	return &s
}

// setupContentionDB creates a file-based SQLite database with the auth schema
// and a very short busy_timeout. It also opens a second connection that holds
// a write transaction to create contention. Returns the primary *sql.DB for
// middleware use (will experience SQLITE_BUSY on queries) and a cleanup
// function.
//
// Uses DELETE journal mode (not WAL) so that an exclusive write lock on one
// connection blocks readers on the other connection.
func setupContentionDB(t *testing.T) *sql.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "contention.db")

	// Primary connection: the one the auth middleware will use.
	primary, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open primary DB: %v", err)
	}

	// Use DELETE journal mode so writers block readers.
	if _, err := primary.Exec("PRAGMA journal_mode = DELETE"); err != nil {
		t.Fatalf("failed to set DELETE journal mode: %v", err)
	}
	if _, err := primary.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("failed to set foreign_keys: %v", err)
	}
	// Very short busy_timeout so we get SQLITE_BUSY quickly.
	if _, err := primary.Exec("PRAGMA busy_timeout = 1"); err != nil {
		t.Fatalf("failed to set busy_timeout: %v", err)
	}
	if _, err := primary.Exec(authTestSchema); err != nil {
		t.Fatalf("failed to create schema on primary: %v", err)
	}

	// Insert valid test data via primary before locking.
	now := nowISO()
	_, err = primary.Exec(
		`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
		 VALUES ('contention-user', 'contuser', 'cont@test.com', '', 'active', 'local', 'local_contuser', ?, ?)`,
		now, now,
	)
	if err != nil {
		t.Fatalf("failed to insert contention test user: %v", err)
	}

	// Insert a valid API key.
	apiSecret := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" // 32 chars
	_, err = primary.Exec(
		`INSERT INTO api_keys (id, key_id, secret_hash, user_id, expires_at, created_at, revoked_at)
		 VALUES ('contention-key', 'contkey1', ?, 'contention-user', NULL, ?, NULL)`,
		sha256hex(apiSecret), now,
	)
	if err != nil {
		t.Fatalf("failed to insert contention API key: %v", err)
	}

	// Open a second connection and hold a write lock.
	blocker, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open blocker DB: %v", err)
	}
	if _, err := blocker.Exec("PRAGMA journal_mode = DELETE"); err != nil {
		t.Fatalf("blocker: failed to set DELETE journal mode: %v", err)
	}

	// Start a write transaction that holds the exclusive lock.
	tx, err := blocker.Begin()
	if err != nil {
		t.Fatalf("failed to begin blocker transaction: %v", err)
	}
	// Do a write to escalate to EXCLUSIVE lock.
	if _, err := tx.Exec("INSERT INTO admin_tokens (id, token_hash, created_at) VALUES ('blocker', 'fake', ?)"+
		"", now); err != nil {
		t.Fatalf("blocker write failed: %v", err)
	}

	t.Cleanup(func() {
		tx.Rollback()
		blocker.Close()
		primary.Close()
	})

	return primary
}

// randomAlphanumeric generates a random alphanumeric string of the given length.
// Uses a simple deterministic approach based on index for reproducibility in tests.
func deterministicAlphanumeric(length int, seed int) string {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[(seed*31+i*17)%len(charset)]
	}
	return string(result)
}

// deterministicHex generates a deterministic hex string of the given length.
func deterministicHex(length int, seed int) string {
	const charset = "0123456789abcdef"
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[(seed*13+i*7)%len(charset)]
	}
	return string(result)
}

// Ensure fmt is used (referenced by some test helpers above).
var _ = fmt.Sprintf
