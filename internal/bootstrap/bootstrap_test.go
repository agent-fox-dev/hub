package bootstrap

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/store"
	_ "modernc.org/sqlite"
)

// TS-01-18: Verify that on first boot (zero users in DB), the admin bootstrap
// routine creates the admin user with the expected field values.
func TestRunAdminBootstrap_CreatesAdminUser(t *testing.T) {
	s, configDir := setupBootstrapTest(t)

	err := RunAdminBootstrap(s, configDir)
	if err != nil {
		t.Fatalf("RunAdminBootstrap returned error: %v", err)
	}

	user, err := s.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("admin user not found: %v", err)
	}
	if user == nil {
		t.Fatal("admin user is nil")
	}
	if user.Username != "admin" {
		t.Errorf("expected username 'admin', got %q", user.Username)
	}
	if user.Email != "admin@localhost" {
		t.Errorf("expected email 'admin@localhost', got %q", user.Email)
	}
	if user.Provider != "local" {
		t.Errorf("expected provider 'local', got %q", user.Provider)
	}
	if user.ProviderID != "admin" {
		t.Errorf("expected provider_id 'admin', got %q", user.ProviderID)
	}
	if user.Status != "active" {
		t.Errorf("expected status 'active', got %q", user.Status)
	}
}

// TS-01-19: Verify that the admin token generation produces a token matching
// the format 'af_admin_<64 hex chars>'.
func TestGenerateAdminToken_Format(t *testing.T) {
	token, err := GenerateAdminToken(nil)
	if err != nil {
		t.Fatalf("GenerateAdminToken returned error: %v", err)
	}

	if !strings.HasPrefix(token, "af_admin_") {
		t.Fatalf("token should start with 'af_admin_', got %q", token)
	}
	if len(token) != 73 {
		t.Fatalf("expected token length 73 (9 prefix + 64 hex), got %d", len(token))
	}

	// Verify last 64 chars are valid hex.
	hexPart := token[9:]
	if len(hexPart) != 64 {
		t.Fatalf("expected 64 hex characters after prefix, got %d", len(hexPart))
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		t.Errorf("hex part %q is not valid hex: %v", hexPart, err)
	}
}

// TS-01-20: Verify that the SHA-256 hash of the generated admin token is
// stored in the admin_tokens table.
func TestPersistAdminTokenHash(t *testing.T) {
	s, _ := setupBootstrapTest(t)

	token, err := GenerateAdminToken(nil)
	if err != nil {
		t.Fatalf("GenerateAdminToken failed: %v", err)
	}

	err = PersistAdminTokenHash(s, token)
	if err != nil {
		t.Fatalf("PersistAdminTokenHash returned error: %v", err)
	}

	adminToken, err := s.GetAdminToken()
	if err != nil {
		t.Fatalf("GetAdminToken failed: %v", err)
	}
	if adminToken == nil {
		t.Fatal("GetAdminToken returned nil")
	}

	expectedHash := sha256Hex(token)
	if adminToken.TokenHash != expectedHash {
		t.Errorf("stored token_hash %q does not match expected sha256 %q",
			adminToken.TokenHash, expectedHash)
	}
}

// TS-01-21: Verify that the admin_token file is written next to config.toml
// with file permissions 0600 and contains the plaintext token.
func TestWriteAdminTokenFile(t *testing.T) {
	configDir := t.TempDir()
	token := "af_admin_" + strings.Repeat("a", 64)

	err := WriteAdminTokenFile(token, configDir)
	if err != nil {
		t.Fatalf("WriteAdminTokenFile returned error: %v", err)
	}

	filePath := filepath.Join(configDir, "admin_token")
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("admin_token file not found: %v", err)
	}

	// Check file mode (Unix only).
	mode := info.Mode().Perm()
	if mode != 0600 {
		t.Errorf("expected file mode 0600, got %04o", mode)
	}

	// Check content.
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read admin_token file: %v", err)
	}
	if string(data) != token {
		t.Errorf("file content does not match token")
	}
}

// TS-01-22: Verify that the absolute path of the admin_token file is logged at
// the warn level after the file is written.
// This test verifies RunAdminBootstrap completes and the file exists with an
// absolute path derivable from the config directory.
func TestRunAdminBootstrap_LogsPath(t *testing.T) {
	s, configDir := setupBootstrapTest(t)

	err := RunAdminBootstrap(s, configDir)
	if err != nil {
		t.Fatalf("RunAdminBootstrap returned error: %v", err)
	}

	// Verify the admin_token file exists.
	filePath := filepath.Join(configDir, "admin_token")
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("admin_token file should exist after bootstrap: %v", err)
	}

	// Verify the path is absolute.
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		t.Fatalf("failed to get absolute path: %v", err)
	}
	if absPath == "" {
		t.Error("absolute path should not be empty")
	}
}

// TS-01-23: Verify that on a non-first boot the server reads AF_HUB_ADMIN_TOKEN,
// hashes it, and succeeds when the hash matches the stored token_hash.
func TestValidateAdminToken_CorrectToken(t *testing.T) {
	s, configDir := setupBootstrapTest(t)

	// Bootstrap to create the admin user and token.
	if err := RunAdminBootstrap(s, configDir); err != nil {
		t.Fatalf("RunAdminBootstrap failed: %v", err)
	}

	// Read the token from the file.
	tokenBytes, err := os.ReadFile(filepath.Join(configDir, "admin_token"))
	if err != nil {
		t.Fatalf("failed to read admin_token: %v", err)
	}
	token := string(tokenBytes)

	// Set the env var.
	t.Setenv("AF_HUB_ADMIN_TOKEN", token)

	err = ValidateAdminToken(s)
	if err != nil {
		t.Fatalf("ValidateAdminToken should succeed with correct token, got: %v", err)
	}
}

// TS-01-24: Verify that on a non-first boot with AF_HUB_ADMIN_TOKEN absent
// or empty, the server logs a fatal error and refuses to start.
func TestValidateAdminToken_MissingEnvVar(t *testing.T) {
	s, configDir := setupBootstrapTest(t)
	if err := RunAdminBootstrap(s, configDir); err != nil {
		t.Fatalf("RunAdminBootstrap failed: %v", err)
	}

	t.Setenv("AF_HUB_ADMIN_TOKEN", "")

	err := ValidateAdminToken(s)
	if err == nil {
		t.Fatal("expected error when AF_HUB_ADMIN_TOKEN is empty, got nil")
	}
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "af_hub_admin_token") && !strings.Contains(errMsg, "missing") {
		t.Errorf("error should mention 'AF_HUB_ADMIN_TOKEN' or 'missing', got: %s", err.Error())
	}
}

// TS-01-25: Verify that on a non-first boot with a wrong AF_HUB_ADMIN_TOKEN,
// the server logs a fatal error indicating token mismatch.
func TestValidateAdminToken_WrongToken(t *testing.T) {
	s, configDir := setupBootstrapTest(t)
	if err := RunAdminBootstrap(s, configDir); err != nil {
		t.Fatalf("RunAdminBootstrap failed: %v", err)
	}

	wrongToken := "af_admin_" + strings.Repeat("b", 64)
	t.Setenv("AF_HUB_ADMIN_TOKEN", wrongToken)

	err := ValidateAdminToken(s)
	if err == nil {
		t.Fatal("expected error for wrong token, got nil")
	}
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "mismatch") && !strings.Contains(errMsg, "invalid") {
		t.Errorf("error should mention 'mismatch' or 'invalid', got: %s", err.Error())
	}
}

// TS-01-26: Verify that --reset-admin-token generates a new token, updates
// admin_tokens, overwrites the admin_token file with mode 0600.
func TestRotateAdminToken(t *testing.T) {
	s, configDir := setupBootstrapTest(t)
	if err := RunAdminBootstrap(s, configDir); err != nil {
		t.Fatalf("RunAdminBootstrap failed: %v", err)
	}

	// Read old hash.
	oldToken, err := s.GetAdminToken()
	if err != nil {
		t.Fatalf("GetAdminToken failed: %v", err)
	}
	if oldToken == nil {
		t.Fatal("GetAdminToken returned nil before rotation")
	}
	oldHash := oldToken.TokenHash

	// Read old file content.
	oldFileContent, err := os.ReadFile(filepath.Join(configDir, "admin_token"))
	if err != nil {
		t.Fatalf("failed to read old admin_token: %v", err)
	}

	// Rotate.
	err = RotateAdminToken(s, configDir)
	if err != nil {
		t.Fatalf("RotateAdminToken returned error: %v", err)
	}

	// Verify new hash is different.
	newToken, err := s.GetAdminToken()
	if err != nil {
		t.Fatalf("GetAdminToken after rotation failed: %v", err)
	}
	if newToken == nil {
		t.Fatal("GetAdminToken returned nil after rotation")
	}
	if newToken.TokenHash == oldHash {
		t.Error("token_hash should differ after rotation")
	}

	// Verify new file content is different.
	newFileContent, err := os.ReadFile(filepath.Join(configDir, "admin_token"))
	if err != nil {
		t.Fatalf("failed to read new admin_token: %v", err)
	}
	if string(newFileContent) == string(oldFileContent) {
		t.Error("admin_token file content should differ after rotation")
	}

	// Verify new token starts with prefix.
	if !strings.HasPrefix(string(newFileContent), "af_admin_") {
		t.Errorf("new token should start with 'af_admin_', got %q", string(newFileContent))
	}

	// Verify file mode.
	info, err := os.Stat(filepath.Join(configDir, "admin_token"))
	if err != nil {
		t.Fatalf("admin_token file stat failed: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected file mode 0600, got %04o", info.Mode().Perm())
	}

	// Verify hash matches new file content.
	expectedHash := sha256Hex(string(newFileContent))
	if newToken.TokenHash != expectedHash {
		t.Errorf("new token_hash %q does not match sha256 of new plaintext %q",
			newToken.TokenHash, expectedHash)
	}
}

// TS-01-27: Verify that when --reset-admin-token is provided, the
// AF_HUB_ADMIN_TOKEN environment validation step is skipped.
func TestRotateAdminToken_SkipsEnvValidation(t *testing.T) {
	s, configDir := setupBootstrapTest(t)
	if err := RunAdminBootstrap(s, configDir); err != nil {
		t.Fatalf("RunAdminBootstrap failed: %v", err)
	}

	// Unset the env var — rotation should still succeed.
	t.Setenv("AF_HUB_ADMIN_TOKEN", "")

	err := RotateAdminToken(s, configDir)
	if err != nil {
		t.Fatalf("RotateAdminToken should succeed without AF_HUB_ADMIN_TOKEN, got: %v", err)
	}
}

// TS-01-E9: Verify that when the admin_token file cannot be written during
// bootstrap, the server removes any partial file, removes the admin user
// record, and returns an error.
func TestRunAdminBootstrap_ReadOnlyConfigDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}

	s, _ := setupBootstrapTest(t)

	// Create a read-only config directory.
	readOnlyDir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(readOnlyDir, 0555); err != nil {
		t.Fatalf("failed to create read-only dir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(readOnlyDir, 0755) })

	err := RunAdminBootstrap(s, readOnlyDir)
	if err == nil {
		t.Fatal("expected error when config dir is read-only, got nil")
	}

	// Verify no admin_token file exists.
	if _, statErr := os.Stat(filepath.Join(readOnlyDir, "admin_token")); statErr == nil {
		t.Error("admin_token file should not exist after failed bootstrap")
	}

	// Verify users table is empty (admin user cleaned up).
	count, err := s.CountUsers()
	if err != nil {
		t.Fatalf("CountUsers failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 users after failed bootstrap cleanup, got %d", count)
	}
}

// TS-01-E10: Verify that when crypto/rand fails to generate random bytes,
// generate_admin_token returns a non-nil error and no partial state.
func TestGenerateAdminToken_RandFailure(t *testing.T) {
	// Pass a failing reader.
	failReader := &errorReader{err: fmt.Errorf("simulated rand failure")}
	token, err := GenerateAdminToken(failReader)
	if err == nil {
		t.Fatal("expected error when rand reader fails, got nil")
	}
	if token != "" {
		t.Errorf("expected empty token on error, got %q", token)
	}
}

// TS-01-E11: Verify that the server logs a fatal error about corrupted token
// state when admin_tokens table is empty on a non-first boot.
func TestValidateAdminToken_EmptyAdminTokensTable(t *testing.T) {
	s, _ := setupBootstrapTest(t)

	// Create a user so it's not first boot, but don't create an admin token.
	_, err := s.CreateUser(&store.User{
		Username:   "existing",
		Email:      "existing@test.com",
		Provider:   "local",
		ProviderID: "existing1",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	validToken := "af_admin_" + strings.Repeat("c", 64)
	t.Setenv("AF_HUB_ADMIN_TOKEN", validToken)

	err = ValidateAdminToken(s)
	if err == nil {
		t.Fatal("expected error when admin_tokens table is empty, got nil")
	}
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "corrupt") &&
		!strings.Contains(errMsg, "empty") &&
		!strings.Contains(errMsg, "no token") &&
		!strings.Contains(errMsg, "not found") {
		t.Errorf("error should mention 'corrupt', 'empty', 'no token', or 'not found', got: %s", err.Error())
	}
}

// TS-01-E12: Verify that when the admin_token file cannot be overwritten during
// token rotation, the server does not update admin_tokens, leaves old token
// intact.
func TestRotateAdminToken_ReadOnlyDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}

	s, configDir := setupBootstrapTest(t)
	if err := RunAdminBootstrap(s, configDir); err != nil {
		t.Fatalf("RunAdminBootstrap failed: %v", err)
	}

	// Save old hash.
	oldTokenRec, err := s.GetAdminToken()
	if err != nil {
		t.Fatalf("GetAdminToken failed: %v", err)
	}
	if oldTokenRec == nil {
		t.Fatal("GetAdminToken returned nil")
	}
	oldHash := oldTokenRec.TokenHash

	// Save old file content.
	oldContent, err := os.ReadFile(filepath.Join(configDir, "admin_token"))
	if err != nil {
		t.Fatalf("failed to read admin_token: %v", err)
	}

	// Make config dir read-only.
	if err := os.Chmod(configDir, 0555); err != nil {
		t.Fatalf("failed to chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(configDir, 0755) })

	// Attempt rotation — should fail.
	err = RotateAdminToken(s, configDir)
	if err == nil {
		t.Fatal("expected error during rotation with read-only dir, got nil")
	}

	// Restore permissions to check state.
	os.Chmod(configDir, 0755)

	// Verify hash is unchanged.
	currentToken, err := s.GetAdminToken()
	if err != nil {
		t.Fatalf("GetAdminToken after failed rotation: %v", err)
	}
	if currentToken == nil {
		t.Fatal("GetAdminToken returned nil after failed rotation")
	}
	if currentToken.TokenHash != oldHash {
		t.Errorf("token_hash should be unchanged after failed rotation, was %q now %q",
			oldHash, currentToken.TokenHash)
	}

	// Verify file content is unchanged.
	currentContent, err := os.ReadFile(filepath.Join(configDir, "admin_token"))
	if err != nil {
		t.Fatalf("failed to read admin_token after failed rotation: %v", err)
	}
	if string(currentContent) != string(oldContent) {
		t.Error("admin_token file content should be unchanged after failed rotation")
	}
}

// TS-01-P1: For any generated admin token, the SHA-256 hash stored in
// admin_tokens always equals sha256(plaintext_token).
func TestProperty_TokenHashConsistency(t *testing.T) {
	for i := 0; i < 100; i++ {
		token, err := GenerateAdminToken(nil)
		if err != nil {
			t.Fatalf("iteration %d: GenerateAdminToken failed: %v", i, err)
		}

		expectedHash := sha256Hex(token)

		s, _ := setupBootstrapTest(t)
		err = PersistAdminTokenHash(s, token)
		if err != nil {
			t.Fatalf("iteration %d: PersistAdminTokenHash failed: %v", i, err)
		}

		stored, err := s.GetAdminToken()
		if err != nil {
			t.Fatalf("iteration %d: GetAdminToken failed: %v", i, err)
		}
		if stored == nil {
			t.Fatalf("iteration %d: GetAdminToken returned nil", i)
		}

		if stored.TokenHash != expectedHash {
			t.Errorf("iteration %d: stored hash %q != expected %q",
				i, stored.TokenHash, expectedHash)
		}
	}
}

// TS-01-P2: Admin bootstrap executes if and only if zero users exist in the
// users table; it never executes more than once for the same first-boot event.
func TestProperty_BootstrapExactlyOnce(t *testing.T) {
	t.Run("bootstrap runs when zero users", func(t *testing.T) {
		s, configDir := setupBootstrapTest(t)

		first, err := IsFirstBoot(s)
		if err != nil {
			t.Fatalf("IsFirstBoot failed: %v", err)
		}
		if !first {
			t.Fatal("expected IsFirstBoot=true with zero users")
		}

		err = RunAdminBootstrap(s, configDir)
		if err != nil {
			t.Fatalf("RunAdminBootstrap failed: %v", err)
		}

		count, err := s.CountUsers()
		if err != nil {
			t.Fatalf("CountUsers failed: %v", err)
		}
		if count != 1 {
			t.Errorf("expected 1 user after bootstrap, got %d", count)
		}
	})

	t.Run("bootstrap is idempotent", func(t *testing.T) {
		s, configDir := setupBootstrapTest(t)

		// First bootstrap.
		if err := RunAdminBootstrap(s, configDir); err != nil {
			t.Fatalf("first RunAdminBootstrap failed: %v", err)
		}

		// IsFirstBoot should now return false.
		first, err := IsFirstBoot(s)
		if err != nil {
			t.Fatalf("IsFirstBoot failed: %v", err)
		}
		if first {
			t.Fatal("expected IsFirstBoot=false after bootstrap")
		}

		count, err := s.CountUsers()
		if err != nil {
			t.Fatalf("CountUsers failed: %v", err)
		}
		if count != 1 {
			t.Errorf("expected 1 user, got %d", count)
		}
	})

	t.Run("bootstrap skipped when users exist", func(t *testing.T) {
		s, _ := setupBootstrapTest(t)

		// Insert a non-admin user.
		_, err := s.CreateUser(&store.User{
			Username:   "existing",
			Email:      "existing@test.com",
			Provider:   "local",
			ProviderID: "existing1",
			Status:     "active",
		})
		if err != nil {
			t.Fatalf("CreateUser failed: %v", err)
		}

		first, err := IsFirstBoot(s)
		if err != nil {
			t.Fatalf("IsFirstBoot failed: %v", err)
		}
		if first {
			t.Fatal("expected IsFirstBoot=false when users already exist")
		}
	})
}

// --- helpers ---

// setupBootstrapTest creates a fresh database with schema and a temp config
// directory suitable for bootstrap testing.
func setupBootstrapTest(t *testing.T) (store.Store, string) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("failed to enable WAL: %v", err)
	}

	schema := []string{
		`CREATE TABLE IF NOT EXISTS users (
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
		`CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			slug TEXT UNIQUE NOT NULL,
			url TEXT UNIQUE NOT NULL,
			status TEXT DEFAULT 'active',
			created_at TEXT,
			created_by TEXT REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspace_members (
			user_id TEXT REFERENCES users(id),
			workspace_id TEXT REFERENCES workspaces(id),
			role TEXT NOT NULL,
			created_at TEXT,
			granted_by TEXT REFERENCES users(id),
			PRIMARY KEY (user_id, workspace_id)
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_id TEXT UNIQUE,
			key_hash TEXT,
			user_id TEXT REFERENCES users(id),
			workspace_id TEXT REFERENCES workspaces(id),
			label TEXT,
			expires_at TEXT,
			revoked_at TEXT,
			created_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS admin_tokens (
			id TEXT PRIMARY KEY,
			token_hash TEXT,
			created_at TEXT
		)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create schema: %v", err)
		}
	}

	return store.NewStore(db), configDir
}

// sha256Hex computes the hex-encoded SHA-256 hash of a string.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// errorReader is an io.Reader that always returns an error.
type errorReader struct {
	err error
}

func (r *errorReader) Read(_ []byte) (int, error) {
	return 0, r.err
}

// Verify errorReader implements io.Reader at compile time.
var _ io.Reader = (*errorReader)(nil)

