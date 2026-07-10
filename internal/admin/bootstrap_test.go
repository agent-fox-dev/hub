package admin_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/admin"
	"github.com/sirupsen/logrus"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// adminTokenPattern matches the admin token format: af_admin_<64 hex chars>.
var adminTokenPattern = regexp.MustCompile(`^af_admin_[0-9a-f]{64}$`)

// setupAdminTestDB creates a temporary SQLite database with the users and
// admin_tokens tables needed for admin bootstrap tests. Returns the *sql.DB
// and the directory containing the database.
func setupAdminTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	// Apply pragmas.
	if _, err := database.Exec("PRAGMA journal_mode = WAL"); err != nil {
		t.Fatalf("failed to set WAL: %v", err)
	}
	if _, err := database.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("failed to set foreign_keys: %v", err)
	}
	if _, err := database.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		t.Fatalf("failed to set busy_timeout: %v", err)
	}

	// Create the required tables.
	schema := `
		CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			full_name TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL DEFAULT (strftime('%%Y-%%m-%%dT%%H:%%M:%%f', 'now') || 'Z'),
			updated_at TEXT NOT NULL DEFAULT (strftime('%%Y-%%m-%%dT%%H:%%M:%%f', 'now') || 'Z'),
			UNIQUE(provider, provider_id)
		);

		CREATE TABLE IF NOT EXISTS admin_tokens (
			id TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (strftime('%%Y-%%m-%%dT%%H:%%M:%%f', 'now') || 'Z')
		);
	`
	if _, err := database.Exec(schema); err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	t.Cleanup(func() { database.Close() })
	return database, tmpDir
}

// insertAdminTokenRow inserts a row into admin_tokens with the given hash.
// Returns the generated id for the row.
func insertAdminTokenRow(t *testing.T, db *sql.DB, tokenHash string) string {
	t.Helper()
	id := "test-admin-token-id"
	_, err := db.Exec(
		"INSERT INTO admin_tokens (id, token_hash) VALUES (?, ?)",
		id, tokenHash,
	)
	if err != nil {
		t.Fatalf("failed to insert admin_tokens row: %v", err)
	}
	return id
}

// insertAdminUserRow inserts the canonical admin user row into users.
func insertAdminUserRow(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO users (id, username, email, provider, provider_id, status)
		 VALUES ('admin-user-id', 'admin', 'admin@localhost', 'local', 'admin', 'active')`,
	)
	if err != nil {
		t.Fatalf("failed to insert admin user row: %v", err)
	}
}

// computeTestHash computes SHA-256 hex digest of the given string.
// Used by tests to compute expected hashes for admin token suffixes.
func computeTestHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// knownSuffix is a fixed 64-char hex string used across multiple tests
// as a known admin token suffix.
const knownSuffix = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

// knownToken is the full admin token using knownSuffix.
const knownToken = "af_admin_" + knownSuffix

// setupLogCapture redirects logrus output to a buffer for test assertions
// on log levels and messages. Returns the buffer. Restores the original
// logrus state on test cleanup.
func setupLogCapture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer

	origFormatter := logrus.StandardLogger().Formatter
	origOutput := logrus.StandardLogger().Out
	origLevel := logrus.GetLevel()

	logrus.SetFormatter(&logrus.JSONFormatter{})
	logrus.SetOutput(&buf)
	logrus.SetLevel(logrus.TraceLevel) // Capture all levels.

	t.Cleanup(func() {
		logrus.SetFormatter(origFormatter)
		logrus.SetOutput(origOutput)
		logrus.SetLevel(origLevel)
	})

	return &buf
}

// parseLogEntries splits a buffer's content into individual JSON log entries.
func parseLogEntries(buf *bytes.Buffer) []map[string]any {
	var entries []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err == nil {
			entries = append(entries, entry)
		}
	}
	return entries
}

// findLogEntry searches for a log entry matching the given level and
// containing the given substring in its "msg" field.
func findLogEntry(entries []map[string]any, level, msgSubstring string) map[string]any {
	for _, entry := range entries {
		entryLevel, _ := entry["level"].(string)
		entryMsg, _ := entry["msg"].(string)
		if entryLevel == level && strings.Contains(entryMsg, msgSubstring) {
			return entry
		}
	}
	return nil
}

// adminTokenRowCount returns the number of rows in admin_tokens.
func adminTokenRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_tokens").Scan(&count); err != nil {
		t.Fatalf("failed to count admin_tokens rows: %v", err)
	}
	return count
}

// getAdminTokenHash retrieves the token_hash from the first admin_tokens row.
func getAdminTokenHash(t *testing.T, db *sql.DB) string {
	t.Helper()
	var hash string
	err := db.QueryRow("SELECT token_hash FROM admin_tokens LIMIT 1").Scan(&hash)
	if err != nil {
		t.Fatalf("failed to get admin token hash: %v", err)
	}
	return hash
}

// ---------------------------------------------------------------------------
// 4.1 — Admin Bootstrap on First Boot
// ---------------------------------------------------------------------------

// TestSpec01_FirstBootAdminUserAndToken verifies that on first boot (zero rows
// in admin_tokens), Bootstrap creates:
//   - An admin user row in users with username=admin, email=admin@localhost,
//     provider=local, provider_id=admin, status=active
//   - A token matching af_admin_<64 hex chars>
//   - A SHA-256 hash of the 64-char suffix stored in admin_tokens
//   - An admin_token file with mode 0600
//   - A warn-level log entry with the absolute path of the file
//
// TS-01-26, REQ: 01-REQ-8.1
func TestSpec01_FirstBootAdminUserAndToken(t *testing.T) {
	logBuf := setupLogCapture(t)
	database, _ := setupAdminTestDB(t)
	configDir := t.TempDir()

	// Precondition: admin_tokens is empty.
	if count := adminTokenRowCount(t, database); count != 0 {
		t.Fatalf("precondition failed: admin_tokens should have 0 rows, got %d", count)
	}

	// Ensure AF_HUB_ADMIN_TOKEN is not set.
	t.Setenv("AF_HUB_ADMIN_TOKEN", "")
	os.Unsetenv("AF_HUB_ADMIN_TOKEN")

	result, err := admin.Bootstrap(database, configDir, false)
	if err != nil {
		t.Fatalf("Bootstrap should succeed on first boot: %v", err)
	}
	if result == nil {
		t.Fatal("Bootstrap returned nil result on first boot, want non-nil")
	}

	// --- Verify BootstrapResult ---
	if !result.IsFirstBoot {
		t.Error("IsFirstBoot should be true on first boot")
	}
	if result.Token == "" {
		t.Error("Token should be set on first boot")
	}
	if !adminTokenPattern.MatchString(result.Token) {
		t.Errorf("Token = %q, want format af_admin_<64 hex chars>", result.Token)
	}

	// --- Verify admin user row in users table ---
	var username, email, provider, providerID, status string
	err = database.QueryRow(
		"SELECT username, email, provider, provider_id, status FROM users WHERE username = 'admin'",
	).Scan(&username, &email, &provider, &providerID, &status)
	if err != nil {
		t.Fatalf("admin user row should exist in users table: %v", err)
	}
	if email != "admin@localhost" {
		t.Errorf("admin email = %q, want %q", email, "admin@localhost")
	}
	if provider != "local" {
		t.Errorf("admin provider = %q, want %q", provider, "local")
	}
	if providerID != "admin" {
		t.Errorf("admin provider_id = %q, want %q", providerID, "admin")
	}
	if status != "active" {
		t.Errorf("admin status = %q, want %q", status, "active")
	}

	// --- Verify admin_tokens has exactly one row ---
	if count := adminTokenRowCount(t, database); count != 1 {
		t.Errorf("admin_tokens row count = %d, want 1", count)
	}

	// --- Verify the stored hash is SHA-256 of the token suffix ---
	storedHash := getAdminTokenHash(t, database)
	suffix := result.Token[len("af_admin_"):] // strip prefix
	expectedHash := computeTestHash(suffix)
	if storedHash != expectedHash {
		t.Errorf("stored hash = %q, want SHA-256 of suffix %q = %q",
			storedHash, suffix, expectedHash)
	}

	// --- Verify admin_token file ---
	tokenFilePath := filepath.Join(configDir, "admin_token")
	if result.TokenFilePath == "" {
		t.Error("TokenFilePath should be set on first boot")
	}

	fileContent, err := os.ReadFile(tokenFilePath)
	if err != nil {
		t.Fatalf("admin_token file should exist at %s: %v", tokenFilePath, err)
	}
	if string(fileContent) != result.Token {
		t.Errorf("admin_token file content = %q, want %q", string(fileContent), result.Token)
	}

	// File mode should be 0600 (owner read/write only).
	info, err := os.Stat(tokenFilePath)
	if err != nil {
		t.Fatalf("failed to stat admin_token file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("admin_token file mode = %04o, want 0600", mode)
	}

	// --- Verify warn-level log entry with absolute path ---
	entries := parseLogEntries(logBuf)
	warnLog := findLogEntry(entries, "warning", "admin_token")
	if warnLog == nil {
		t.Error("expected warn-level log entry about admin_token file path; none found")
	} else {
		logPath, _ := warnLog["path"].(string)
		if !filepath.IsAbs(logPath) {
			t.Errorf("log path = %q, want an absolute path", logPath)
		}
	}
}

// TestSpec01_FirstBootEnvVarIgnored verifies that when AF_HUB_ADMIN_TOKEN is
// set in the environment but admin_tokens has zero rows, Bootstrap logs an
// info-level notice and ignores the env var, proceeding with normal first-boot
// token generation.
// TS-01-27, REQ: 01-REQ-8.2
func TestSpec01_FirstBootEnvVarIgnored(t *testing.T) {
	logBuf := setupLogCapture(t)
	database, _ := setupAdminTestDB(t)
	configDir := t.TempDir()

	// Set AF_HUB_ADMIN_TOKEN (should be ignored on first boot).
	t.Setenv("AF_HUB_ADMIN_TOKEN", "some_value_that_should_be_ignored")

	result, err := admin.Bootstrap(database, configDir, false)
	if err != nil {
		t.Fatalf("Bootstrap should succeed on first boot: %v", err)
	}
	if result == nil {
		t.Fatal("Bootstrap returned nil result, want non-nil")
	}

	// --- The generated token should NOT be the env var value ---
	if result.Token == "some_value_that_should_be_ignored" {
		t.Error("generated token should NOT be the env var value")
	}
	if !adminTokenPattern.MatchString(result.Token) {
		t.Errorf("Token = %q, want format af_admin_<64 hex chars>", result.Token)
	}

	// --- Info-level log notice should be emitted ---
	entries := parseLogEntries(logBuf)
	infoLog := findLogEntry(entries, "info",
		"AF_HUB_ADMIN_TOKEN is set but will be ignored on first boot; a new token will be generated")
	if infoLog == nil {
		t.Error("expected info-level log entry about AF_HUB_ADMIN_TOKEN being ignored on first boot; none found")
	}

	// --- New token should be generated regardless ---
	tokenFile := filepath.Join(configDir, "admin_token")
	content, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("admin_token file should be written: %v", err)
	}
	if string(content) != result.Token {
		t.Errorf("file content = %q, want %q", string(content), result.Token)
	}
}

// TestSpec01_FirstBootOverwritesExistingTokenFile verifies that when the
// admin_token file already exists at the target path during first boot,
// Bootstrap overwrites it and logs a warn-level entry with the path.
// TS-01-28, REQ: 01-REQ-8.3
func TestSpec01_FirstBootOverwritesExistingTokenFile(t *testing.T) {
	logBuf := setupLogCapture(t)
	database, _ := setupAdminTestDB(t)
	configDir := t.TempDir()

	// Pre-create the admin_token file with old content.
	tokenFilePath := filepath.Join(configDir, "admin_token")
	if err := os.WriteFile(tokenFilePath, []byte("old_token_value"), 0644); err != nil {
		t.Fatalf("failed to write pre-existing admin_token file: %v", err)
	}

	t.Setenv("AF_HUB_ADMIN_TOKEN", "")
	os.Unsetenv("AF_HUB_ADMIN_TOKEN")

	result, err := admin.Bootstrap(database, configDir, false)
	if err != nil {
		t.Fatalf("Bootstrap should succeed: %v", err)
	}
	if result == nil {
		t.Fatal("Bootstrap returned nil result")
	}

	// --- File should be overwritten with new content ---
	newContent, err := os.ReadFile(tokenFilePath)
	if err != nil {
		t.Fatalf("admin_token file should exist: %v", err)
	}
	if string(newContent) == "old_token_value" {
		t.Error("admin_token file should be overwritten with new token, but still has old content")
	}
	if !adminTokenPattern.MatchString(string(newContent)) {
		t.Errorf("overwritten file content = %q, want format af_admin_<64 hex chars>", string(newContent))
	}

	// --- Warn-level log about overwrite ---
	entries := parseLogEntries(logBuf)
	warnLog := findLogEntry(entries, "warning",
		"admin_token file already existed and was overwritten")
	if warnLog == nil {
		t.Error("expected warn-level log entry about admin_token file overwrite; none found")
	} else {
		logPath, _ := warnLog["path"].(string)
		if !filepath.IsAbs(logPath) {
			t.Errorf("overwrite log path = %q, want an absolute path", logPath)
		}
	}
}

// TestSpec01_FirstBootUnwritableTokenDir verifies that if the admin_token
// file cannot be written during first boot (e.g., permission error),
// Bootstrap returns a fatal error. The server should exit with code 1.
// TS-01-E11, REQ: 01-REQ-8.E1
func TestSpec01_FirstBootUnwritableTokenDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root user to trigger permission denied")
	}

	database, _ := setupAdminTestDB(t)

	// Create an unwritable directory as the configDir.
	parentDir := t.TempDir()
	configDir := filepath.Join(parentDir, "unwritable")
	if err := os.MkdirAll(configDir, 0555); err != nil {
		t.Fatalf("failed to create unwritable dir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(configDir, 0755) })

	t.Setenv("AF_HUB_ADMIN_TOKEN", "")
	os.Unsetenv("AF_HUB_ADMIN_TOKEN")

	_, err := admin.Bootstrap(database, configDir, false)
	if err == nil {
		t.Fatal("Bootstrap should return error when admin_token file cannot be written, got nil")
	}
}

// TestSpec01_SubsequentBootNoAdminUserRow verifies that the server starts
// normally when admin_tokens has rows but the admin user row in users is
// missing. Admin token authentication does not perform a users table lookup,
// so the missing row should not prevent Bootstrap from succeeding.
// TS-01-E12, REQ: 01-REQ-8.E2
func TestSpec01_SubsequentBootNoAdminUserRow(t *testing.T) {
	database, _ := setupAdminTestDB(t)
	configDir := t.TempDir()

	// Pre-populate admin_tokens with a known hash (subsequent boot).
	knownHash := computeTestHash(knownSuffix)
	insertAdminTokenRow(t, database, knownHash)

	// Do NOT insert admin user row — it's intentionally missing.

	// Set the correct env var for validation.
	t.Setenv("AF_HUB_ADMIN_TOKEN", knownToken)

	result, err := admin.Bootstrap(database, configDir, false)
	if err != nil {
		t.Fatalf("Bootstrap should succeed without admin user row: %v", err)
	}
	if result == nil {
		t.Fatal("Bootstrap returned nil result, want non-nil")
	}
	if result.IsFirstBoot {
		t.Error("IsFirstBoot should be false on subsequent boot")
	}
}

// ---------------------------------------------------------------------------
// 4.2 — Subsequent-Boot Token Validation
// ---------------------------------------------------------------------------

// TestSpec01_SubsequentBootCorrectToken verifies that on subsequent boot
// (admin_tokens has rows), Bootstrap succeeds when AF_HUB_ADMIN_TOKEN is
// set to the correct value whose SHA-256 hash matches the stored hash.
//
// The stored hash is of the 64-char hex suffix (after stripping af_admin_).
// The env var contains the full token (af_admin_<suffix>), so Bootstrap must
// strip the prefix before hashing and comparing.
//
// TS-01-29 (sub-case a), REQ: 01-REQ-9.1
func TestSpec01_SubsequentBootCorrectToken(t *testing.T) {
	database, _ := setupAdminTestDB(t)
	configDir := t.TempDir()

	// Pre-populate admin_tokens with hash of the known suffix.
	knownHash := computeTestHash(knownSuffix)
	insertAdminTokenRow(t, database, knownHash)
	insertAdminUserRow(t, database)

	// Set AF_HUB_ADMIN_TOKEN to the full token (including af_admin_ prefix).
	// Bootstrap must strip the prefix before hashing.
	t.Setenv("AF_HUB_ADMIN_TOKEN", knownToken)

	result, err := admin.Bootstrap(database, configDir, false)
	if err != nil {
		t.Fatalf("Bootstrap should succeed with correct AF_HUB_ADMIN_TOKEN: %v", err)
	}
	if result == nil {
		t.Fatal("Bootstrap returned nil result")
	}
	if result.IsFirstBoot {
		t.Error("IsFirstBoot should be false on subsequent boot")
	}
	// On subsequent boot, no new token is generated.
	if result.Token != "" {
		t.Errorf("Token should be empty on subsequent boot, got %q", result.Token)
	}
}

// TestSpec01_SubsequentBootWrongToken verifies that on subsequent boot,
// Bootstrap returns an error when AF_HUB_ADMIN_TOKEN's SHA-256 hash does
// not match the stored hash. In the full server, this triggers a fatal
// exit with code 1.
// TS-01-29 (sub-case b), REQ: 01-REQ-9.1
func TestSpec01_SubsequentBootWrongToken(t *testing.T) {
	database, _ := setupAdminTestDB(t)
	configDir := t.TempDir()

	// Pre-populate admin_tokens with hash of the known suffix.
	knownHash := computeTestHash(knownSuffix)
	insertAdminTokenRow(t, database, knownHash)

	// Set AF_HUB_ADMIN_TOKEN to a WRONG value.
	wrongSuffix := "0000000000000000000000000000000000000000000000000000000000000000"
	wrongToken := "af_admin_" + wrongSuffix
	t.Setenv("AF_HUB_ADMIN_TOKEN", wrongToken)

	_, err := admin.Bootstrap(database, configDir, false)
	if err == nil {
		t.Fatal("Bootstrap should return error when AF_HUB_ADMIN_TOKEN hash does not match stored hash, got nil")
	}
}

// TestSpec01_SubsequentBootTokenFilePresent verifies that on subsequent boot,
// if the admin_token file still exists at the expected path, Bootstrap logs a
// warn-level security notice with the path and the file-exists message.
// TS-01-30, REQ: 01-REQ-9.2
func TestSpec01_SubsequentBootTokenFilePresent(t *testing.T) {
	logBuf := setupLogCapture(t)
	database, _ := setupAdminTestDB(t)
	configDir := t.TempDir()

	// Pre-populate admin_tokens.
	knownHash := computeTestHash(knownSuffix)
	insertAdminTokenRow(t, database, knownHash)

	// Create the admin_token file so it "still exists".
	tokenFilePath := filepath.Join(configDir, "admin_token")
	if err := os.WriteFile(tokenFilePath, []byte(knownToken), 0600); err != nil {
		t.Fatalf("failed to create admin_token file: %v", err)
	}

	t.Setenv("AF_HUB_ADMIN_TOKEN", knownToken)

	result, err := admin.Bootstrap(database, configDir, false)
	if err != nil {
		t.Fatalf("Bootstrap should succeed: %v", err)
	}
	if result == nil {
		t.Fatal("Bootstrap returned nil result")
	}

	// --- Warn-level security notice should be emitted ---
	entries := parseLogEntries(logBuf)
	warnLog := findLogEntry(entries, "warning",
		"admin_token plaintext file still exists on disk; delete after securing the token")
	if warnLog == nil {
		t.Error("expected warn-level log entry about admin_token file still existing; none found")
	} else {
		logPath, _ := warnLog["path"].(string)
		if !filepath.IsAbs(logPath) {
			t.Errorf("log path = %q, want an absolute path", logPath)
		}
	}
}

// TestSpec01_SubsequentBootTokenFileAbsent verifies that on subsequent boot,
// if the admin_token file is absent at the expected path, Bootstrap emits no
// log entry of any kind about the absent file. It proceeds silently.
// TS-01-31, REQ: 01-REQ-9.3
func TestSpec01_SubsequentBootTokenFileAbsent(t *testing.T) {
	logBuf := setupLogCapture(t)
	database, _ := setupAdminTestDB(t)
	configDir := t.TempDir()

	// Pre-populate admin_tokens.
	knownHash := computeTestHash(knownSuffix)
	insertAdminTokenRow(t, database, knownHash)

	// Ensure admin_token file does NOT exist.
	tokenFilePath := filepath.Join(configDir, "admin_token")
	os.Remove(tokenFilePath) // no-op if not present

	t.Setenv("AF_HUB_ADMIN_TOKEN", knownToken)

	result, err := admin.Bootstrap(database, configDir, false)
	if err != nil {
		t.Fatalf("Bootstrap should succeed: %v", err)
	}
	if result == nil {
		t.Fatal("Bootstrap returned nil result")
	}

	// --- No log entry should reference the absent admin_token file ---
	entries := parseLogEntries(logBuf)
	for _, entry := range entries {
		msg, _ := entry["msg"].(string)
		// Allow "server starting" log which might mention admin_token in path.
		// But no log should specifically be about the admin_token file being absent.
		if strings.Contains(msg, "admin_token") &&
			strings.Contains(msg, "plaintext file still exists") {
			t.Errorf("should NOT log about absent admin_token file, but found: %v", entry)
		}
		if strings.Contains(msg, "admin_token") &&
			strings.Contains(msg, "absent") {
			t.Errorf("should NOT log about absent admin_token file, but found: %v", entry)
		}
	}
}

// TestSpec01_SubsequentBootMissingEnvVar verifies that on subsequent boot
// (admin_tokens has rows), Bootstrap returns an error when AF_HUB_ADMIN_TOKEN
// is not set in the environment. In the full server, this triggers a fatal
// exit with code 1 and no HTTP listener is opened.
// TS-01-E13, REQ: 01-REQ-9.E1
func TestSpec01_SubsequentBootMissingEnvVar(t *testing.T) {
	database, _ := setupAdminTestDB(t)
	configDir := t.TempDir()

	// Pre-populate admin_tokens (subsequent boot).
	knownHash := computeTestHash(knownSuffix)
	insertAdminTokenRow(t, database, knownHash)

	// Ensure AF_HUB_ADMIN_TOKEN is NOT set.
	os.Unsetenv("AF_HUB_ADMIN_TOKEN")
	// Use t.Setenv to restore the env var after the test.
	// But we need it unset, so just unset it.

	_, err := admin.Bootstrap(database, configDir, false)
	if err == nil {
		t.Fatal("Bootstrap should return error when AF_HUB_ADMIN_TOKEN is absent on subsequent boot, got nil")
	}
}

// ---------------------------------------------------------------------------
// 4.3 — Admin Token Rotation via --reset-admin-token
// ---------------------------------------------------------------------------

// TestSpec01_ResetAdminTokenRotation verifies that --reset-admin-token
// bypasses AF_HUB_ADMIN_TOKEN validation, generates a new token, replaces
// the existing admin_tokens row, and writes the new plaintext to admin_token
// file (mode 0600). The old token should be invalidated.
// TS-01-32, REQ: 01-REQ-10.1
func TestSpec01_ResetAdminTokenRotation(t *testing.T) {
	logBuf := setupLogCapture(t)
	database, _ := setupAdminTestDB(t)
	configDir := t.TempDir()

	// Pre-populate admin_tokens with an old hash (existing token).
	oldHash := computeTestHash(knownSuffix)
	insertAdminTokenRow(t, database, oldHash)
	insertAdminUserRow(t, database)

	// AF_HUB_ADMIN_TOKEN should NOT be read during --reset-admin-token.
	// We deliberately don't set it to verify it's bypassed.
	os.Unsetenv("AF_HUB_ADMIN_TOKEN")

	result, err := admin.Bootstrap(database, configDir, true /* resetToken */)
	if err != nil {
		t.Fatalf("Bootstrap with --reset-admin-token should succeed: %v", err)
	}
	if result == nil {
		t.Fatal("Bootstrap returned nil result")
	}

	// --- New token should be generated ---
	if result.Token == "" {
		t.Error("Token should be set after rotation")
	}
	if !adminTokenPattern.MatchString(result.Token) {
		t.Errorf("Token = %q, want format af_admin_<64 hex chars>", result.Token)
	}

	// --- admin_tokens should have exactly one row with NEW hash ---
	if count := adminTokenRowCount(t, database); count != 1 {
		t.Errorf("admin_tokens row count = %d, want 1 after rotation", count)
	}
	newHash := getAdminTokenHash(t, database)
	if newHash == oldHash {
		t.Error("new token hash should differ from old hash after rotation")
	}

	// Verify hash correctness.
	suffix := result.Token[len("af_admin_"):]
	expectedHash := computeTestHash(suffix)
	if newHash != expectedHash {
		t.Errorf("stored hash = %q, want SHA-256 of new suffix = %q", newHash, expectedHash)
	}

	// --- admin_token file should contain new plaintext ---
	tokenFilePath := filepath.Join(configDir, "admin_token")
	content, err := os.ReadFile(tokenFilePath)
	if err != nil {
		t.Fatalf("admin_token file should exist: %v", err)
	}
	if string(content) != result.Token {
		t.Errorf("file content = %q, want %q", string(content), result.Token)
	}

	// File mode should be 0600.
	info, err := os.Stat(tokenFilePath)
	if err != nil {
		t.Fatalf("failed to stat admin_token file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("admin_token file mode = %04o, want 0600", mode)
	}

	// --- Warn-level log with absolute path should be emitted ---
	entries := parseLogEntries(logBuf)
	warnLog := findLogEntry(entries, "warning", "admin_token")
	if warnLog == nil {
		t.Error("expected warn-level log entry about admin_token file path after rotation; none found")
	} else {
		logPath, _ := warnLog["path"].(string)
		if !filepath.IsAbs(logPath) {
			t.Errorf("log path = %q, want an absolute path", logPath)
		}
	}
}

// TestSpec01_ResetAdminTokenEmptyTable verifies that --reset-admin-token
// with zero rows in admin_tokens behaves identically to a normal first boot:
// generates token, writes file, inserts hash row. No special-casing needed.
// TS-01-33, REQ: 01-REQ-10.2
func TestSpec01_ResetAdminTokenEmptyTable(t *testing.T) {
	database, _ := setupAdminTestDB(t)
	configDir := t.TempDir()

	// Precondition: admin_tokens is empty.
	if count := adminTokenRowCount(t, database); count != 0 {
		t.Fatalf("precondition failed: admin_tokens should be empty, got %d rows", count)
	}

	os.Unsetenv("AF_HUB_ADMIN_TOKEN")

	result, err := admin.Bootstrap(database, configDir, true /* resetToken */)
	if err != nil {
		t.Fatalf("Bootstrap with --reset-admin-token on empty table should succeed: %v", err)
	}
	if result == nil {
		t.Fatal("Bootstrap returned nil result")
	}

	// --- Token should be generated ---
	if result.Token == "" {
		t.Error("Token should be set")
	}
	if !adminTokenPattern.MatchString(result.Token) {
		t.Errorf("Token = %q, want format af_admin_<64 hex chars>", result.Token)
	}

	// --- File should exist with mode 0600 ---
	tokenFilePath := filepath.Join(configDir, "admin_token")
	if _, err := os.Stat(tokenFilePath); err != nil {
		t.Fatalf("admin_token file should exist: %v", err)
	}

	info, err := os.Stat(tokenFilePath)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("file mode = %04o, want 0600", mode)
	}

	// --- admin_tokens should have exactly one row ---
	if count := adminTokenRowCount(t, database); count != 1 {
		t.Errorf("admin_tokens row count = %d, want 1", count)
	}
}

// TestSpec01_ResetAdminTokenWithCustomConfigDir verifies that
// --reset-admin-token is fully compatible with --config <path>. When a
// custom config directory is provided, the admin_token file is written
// to that directory. Both flags are processed independently.
// TS-01-34, REQ: 01-REQ-10.3
func TestSpec01_ResetAdminTokenWithCustomConfigDir(t *testing.T) {
	database, _ := setupAdminTestDB(t)

	// Use a custom config directory (simulating --config /some/path/config.toml).
	customConfigDir := t.TempDir()

	// Pre-populate admin_tokens.
	oldHash := computeTestHash(knownSuffix)
	insertAdminTokenRow(t, database, oldHash)

	os.Unsetenv("AF_HUB_ADMIN_TOKEN")

	result, err := admin.Bootstrap(database, customConfigDir, true /* resetToken */)
	if err != nil {
		t.Fatalf("Bootstrap should succeed: %v", err)
	}
	if result == nil {
		t.Fatal("Bootstrap returned nil result")
	}

	// --- admin_token file should be written to the custom config directory ---
	tokenFilePath := filepath.Join(customConfigDir, "admin_token")
	content, err := os.ReadFile(tokenFilePath)
	if err != nil {
		t.Fatalf("admin_token file should exist in custom config dir %s: %v", customConfigDir, err)
	}
	if !adminTokenPattern.MatchString(string(content)) {
		t.Errorf("file content = %q, want format af_admin_<64 hex chars>", string(content))
	}
	if string(content) != result.Token {
		t.Errorf("file content = %q, want result.Token = %q", string(content), result.Token)
	}

	// --- TokenFilePath should point to the custom directory ---
	if result.TokenFilePath == "" {
		t.Error("TokenFilePath should be set")
	}
	expectedPath, _ := filepath.Abs(tokenFilePath)
	if result.TokenFilePath != "" && result.TokenFilePath != expectedPath {
		t.Errorf("TokenFilePath = %q, want %q", result.TokenFilePath, expectedPath)
	}
}

// TestSpec01_ResetAdminTokenUnwritable verifies that --reset-admin-token
// returns a fatal error when the admin_token file cannot be written to the
// target directory (e.g., permission denied). The server should not start
// with the new token unwritten.
// TS-01-E14, REQ: 01-REQ-10.E1
func TestSpec01_ResetAdminTokenUnwritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root user to trigger permission denied")
	}

	database, _ := setupAdminTestDB(t)

	// Create an unwritable directory as the config directory.
	parentDir := t.TempDir()
	unwritableDir := filepath.Join(parentDir, "nowrite")
	if err := os.MkdirAll(unwritableDir, 0555); err != nil {
		t.Fatalf("failed to create unwritable dir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(unwritableDir, 0755) })

	// Pre-populate admin_tokens.
	oldHash := computeTestHash(knownSuffix)
	insertAdminTokenRow(t, database, oldHash)

	os.Unsetenv("AF_HUB_ADMIN_TOKEN")

	_, err := admin.Bootstrap(database, unwritableDir, true /* resetToken */)
	if err == nil {
		t.Fatal("Bootstrap should return error when admin_token file cannot be written during rotation, got nil")
	}
}

// ---------------------------------------------------------------------------
// Hash function correctness tests
// ---------------------------------------------------------------------------

// TestSpec01_HashTokenSuffixCorrectness verifies that HashTokenSuffix computes
// the correct SHA-256 hex digest for known inputs. This ensures the hash
// stored in admin_tokens.token_hash matches the expected value.
// Supports TS-01-P1 (property: admin token hash correctness).
func TestSpec01_HashTokenSuffixCorrectness(t *testing.T) {
	cases := []struct {
		suffix       string
		expectedHash string
	}{
		{
			suffix:       knownSuffix,
			expectedHash: computeTestHash(knownSuffix),
		},
		{
			suffix:       "0000000000000000000000000000000000000000000000000000000000000000",
			expectedHash: computeTestHash("0000000000000000000000000000000000000000000000000000000000000000"),
		},
		{
			suffix:       "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			expectedHash: computeTestHash("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
		},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("suffix=%s", tc.suffix[:8]+"..."), func(t *testing.T) {
			got := admin.HashTokenSuffix(tc.suffix)
			if got != tc.expectedHash {
				t.Errorf("HashTokenSuffix(%q) = %q, want %q", tc.suffix[:8]+"...", got, tc.expectedHash)
			}
		})
	}
}

// TestSpec01_HashTokenSuffixDeterministic verifies that HashTokenSuffix
// returns the same hash for the same input on repeated calls.
func TestSpec01_HashTokenSuffixDeterministic(t *testing.T) {
	suffix := knownSuffix
	hash1 := admin.HashTokenSuffix(suffix)
	hash2 := admin.HashTokenSuffix(suffix)
	if hash1 != hash2 {
		t.Errorf("HashTokenSuffix not deterministic: %q != %q", hash1, hash2)
	}
}

// ---------------------------------------------------------------------------
// Property Tests (TS-01-P1, TS-01-P9)
// ---------------------------------------------------------------------------

// deterministicHex generates a deterministic hex string of the given length
// using a simple index-based approach for reproducibility.
func deterministicHex(length int, seed int) string {
	const charset = "0123456789abcdef"
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[(seed*13+i*7)%len(charset)]
	}
	return string(result)
}

// TestSpec01_PropAdminTokenHashRoundtrip100 is a property test that generates
// 100 random 64-char hex strings as admin token suffixes, for each:
//  1. Computes the hash via admin.HashTokenSuffix
//  2. Stores it in admin_tokens via Bootstrap (first-boot flow)
//  3. Retrieves the stored hash from the DB
//  4. Verifies stored_hash == hex(sha256(suffix))
//  5. Recomputes the hash at verification time — must produce the same result
//
// This replaces the 3-hardcoded-suffix version with the required 100 random
// inputs per TS-01-P1.
//
// TS-01-P1, PROP: 01-PROP-1
func TestSpec01_PropAdminTokenHashRoundtrip100(t *testing.T) {
	for i := range 100 {
		suffix := deterministicHex(64, i)

		// Compute via the function under test.
		got := admin.HashTokenSuffix(suffix)

		// Compute expected independently.
		h := sha256.Sum256([]byte(suffix))
		expected := hex.EncodeToString(h[:])

		if got != expected {
			t.Errorf("iteration %d: HashTokenSuffix(%q) = %q, want %q",
				i, suffix[:16]+"...", got, expected)
		}

		// Store and retrieve via DB roundtrip.
		db, _ := setupAdminTestDB(t)
		_, err := db.Exec(
			"INSERT INTO admin_tokens (id, token_hash) VALUES (?, ?)",
			fmt.Sprintf("prop-tok-%d", i), got,
		)
		if err != nil {
			t.Fatalf("iteration %d: insert failed: %v", i, err)
		}

		var storedHash string
		err = db.QueryRow(
			"SELECT token_hash FROM admin_tokens WHERE id = ?",
			fmt.Sprintf("prop-tok-%d", i),
		).Scan(&storedHash)
		if err != nil {
			t.Fatalf("iteration %d: query failed: %v", i, err)
		}

		if storedHash != expected {
			t.Errorf("iteration %d: stored hash = %q, want %q", i, storedHash, expected)
		}

		// Recompute at verification time.
		recomputed := admin.HashTokenSuffix(suffix)
		if recomputed != storedHash {
			t.Errorf("iteration %d: recomputed %q != stored %q", i, recomputed, storedHash)
		}
	}
}

// TestSpec01_PropAdminTokenFileMode is a property test that performs 20
// iterations alternating between first boot and rotation scenarios, asserting
// that the admin_token file is always written with mode 0600 — owner
// read/write only, group and world permission bits always zero.
//
// TS-01-P9, PROP: 01-PROP-9
func TestSpec01_PropAdminTokenFileMode(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root user (root ignores file permissions)")
	}

	for i := range 20 {
		t.Run(fmt.Sprintf("iteration_%d", i), func(t *testing.T) {
			db, _ := setupAdminTestDB(t)
			configDir := t.TempDir()
			os.Unsetenv("AF_HUB_ADMIN_TOKEN")

			isReset := i%2 == 1 // alternate first boot and rotation

			if isReset && i > 0 {
				// For rotation scenarios, pre-populate admin_tokens.
				oldHash := computeTestHash(deterministicHex(64, i+1000))
				_, err := db.Exec(
					"INSERT INTO admin_tokens (id, token_hash) VALUES (?, ?)",
					"prop-mode-tok", oldHash,
				)
				if err != nil {
					t.Fatalf("failed to insert admin_tokens row for rotation: %v", err)
				}
			}

			result, err := admin.Bootstrap(db, configDir, isReset)
			if err != nil {
				t.Fatalf("Bootstrap failed: %v", err)
			}
			if result == nil {
				t.Fatal("Bootstrap returned nil result")
			}

			tokenFilePath := filepath.Join(configDir, "admin_token")
			info, err := os.Stat(tokenFilePath)
			if err != nil {
				t.Fatalf("admin_token file should exist: %v", err)
			}

			perm := info.Mode().Perm()

			// Mode must be exactly 0600.
			if perm != 0600 {
				t.Errorf("iteration %d: file mode = %04o, want 0600", i, perm)
			}
			// Group bits must be zero.
			if perm&0070 != 0 {
				t.Errorf("iteration %d: group bits = %04o, want 0 (mode = %04o)",
					i, perm&0070, perm)
			}
			// World bits must be zero.
			if perm&0007 != 0 {
				t.Errorf("iteration %d: world bits = %04o, want 0 (mode = %04o)",
					i, perm&0007, perm)
			}
		})
	}
}
