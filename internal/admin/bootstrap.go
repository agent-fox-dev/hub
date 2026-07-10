// Package admin handles admin token bootstrap, validation, and rotation
// for af-hub server startup. It manages the admin user account, admin
// token generation, SHA-256 hash storage, and the admin_token plaintext
// file lifecycle.
package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// BootstrapResult holds the outcome of the admin bootstrap process.
type BootstrapResult struct {
	// Token is the generated plaintext admin token (af_admin_<64 hex chars>).
	// Set only on first boot or --reset-admin-token; empty on subsequent boot.
	Token string

	// TokenFilePath is the absolute path of the admin_token file.
	// Set when the file is written (first boot or reset).
	TokenFilePath string

	// IsFirstBoot indicates whether this was a first boot (zero admin_tokens rows).
	IsFirstBoot bool
}

// Bootstrap handles the admin token lifecycle during server startup.
//
// Behavior depends on the state of the admin_tokens table and the resetToken flag:
//
// First boot (zero rows in admin_tokens):
//   - If AF_HUB_ADMIN_TOKEN env var is set, logs an info-level notice and ignores it.
//   - Creates admin user row: username=admin, email=admin@localhost, provider=local,
//     provider_id=admin, status=active. Uses INSERT OR IGNORE to handle existing rows
//     (e.g., on --reset-admin-token when user already exists).
//   - Generates a cryptographically random token: af_admin_<64 hex chars>.
//   - Stores SHA-256 hash of the 64-char hex suffix in admin_tokens.
//   - Writes plaintext token to configDir/admin_token (mode 0600).
//   - If file already exists, overwrites and logs a warn-level entry.
//   - Logs the absolute path of the written file at warn level.
//
// Subsequent boot (one or more rows, resetToken=false):
//   - Reads AF_HUB_ADMIN_TOKEN from the environment.
//   - If absent, returns an error (caller should log fatal and exit).
//   - Strips the af_admin_ prefix, computes SHA-256 of the 64-char hex suffix.
//   - Compares against stored hash; returns error on mismatch.
//   - Checks if admin_token file still exists; if present, logs warn-level
//     security notice with path. If absent, emits no log about it.
//   - Does NOT require admin user row in users table to exist.
//
// Reset (resetToken=true):
//   - Bypasses AF_HUB_ADMIN_TOKEN validation entirely (env var not read).
//   - Deletes existing admin_tokens row(s).
//   - Generates new token, stores new hash, writes new plaintext file.
//   - Identical to first-boot flow if admin_tokens is empty.
//
// Returns a BootstrapResult on success, or an error for fatal conditions.
// The caller should log the error at fatal level and exit with code 1.
func Bootstrap(db *sql.DB, configDir string, resetToken bool) (*BootstrapResult, error) {
	// If --reset-admin-token, delete existing rows and fall through to first-boot flow.
	if resetToken {
		if _, err := db.Exec("DELETE FROM admin_tokens"); err != nil {
			return nil, fmt.Errorf("failed to delete existing admin_tokens: %w", err)
		}
	}

	// Count rows in admin_tokens to determine first boot vs subsequent boot.
	var rowCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_tokens").Scan(&rowCount); err != nil {
		return nil, fmt.Errorf("failed to count admin_tokens rows: %w", err)
	}

	if rowCount == 0 {
		return firstBoot(db, configDir)
	}

	// Subsequent boot (resetToken=false, since resetToken=true already deleted rows above).
	return subsequentBoot(db, configDir)
}

// firstBoot handles the first-boot flow: create admin user, generate token,
// store hash, write plaintext file.
func firstBoot(db *sql.DB, configDir string) (*BootstrapResult, error) {
	// If AF_HUB_ADMIN_TOKEN is set, log an info notice and ignore it.
	if _, ok := os.LookupEnv("AF_HUB_ADMIN_TOKEN"); ok {
		logrus.Info("AF_HUB_ADMIN_TOKEN is set but will be ignored on first boot; a new token will be generated")
	}

	// Create admin user row using INSERT OR IGNORE to handle existing rows
	// (e.g., on --reset-admin-token when user already exists).
	userID := uuid.New().String()
	_, err := db.Exec(
		`INSERT OR IGNORE INTO users (id, username, email, provider, provider_id, status)
		 VALUES (?, 'admin', 'admin@localhost', 'local', 'admin', 'active')`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create admin user: %w", err)
	}

	// Generate admin token.
	token, err := GenerateAdminToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate admin token: %w", err)
	}

	// Compute and store the hash of the token suffix.
	suffix := token[len("af_admin_"):]
	tokenHash := HashTokenSuffix(suffix)

	tokenRowID := uuid.New().String()
	_, err = db.Exec(
		"INSERT INTO admin_tokens (id, token_hash) VALUES (?, ?)",
		tokenRowID, tokenHash,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert admin token hash: %w", err)
	}

	// Write plaintext token to file.
	tokenFilePath := filepath.Join(configDir, "admin_token")
	absTokenFilePath, err := filepath.Abs(tokenFilePath)
	if err != nil {
		absTokenFilePath = tokenFilePath
	}

	// Check if file already exists — log warn if overwriting.
	if _, statErr := os.Stat(tokenFilePath); statErr == nil {
		logrus.WithField("path", absTokenFilePath).
			Warn("admin_token file already existed and was overwritten")
	}

	if err := WriteAdminTokenFile(tokenFilePath, token); err != nil {
		return nil, fmt.Errorf("failed to write admin_token file: %w", err)
	}

	// Log the absolute path of the written file at warn level.
	logrus.WithField("path", absTokenFilePath).
		Warn("admin_token file written; secure this file and delete after use")

	return &BootstrapResult{
		Token:         token,
		TokenFilePath: absTokenFilePath,
		IsFirstBoot:   true,
	}, nil
}

// subsequentBoot handles subsequent-boot token validation.
func subsequentBoot(db *sql.DB, configDir string) (*BootstrapResult, error) {
	// Read AF_HUB_ADMIN_TOKEN from environment.
	envToken, ok := os.LookupEnv("AF_HUB_ADMIN_TOKEN")
	if !ok || envToken == "" {
		return nil, fmt.Errorf("AF_HUB_ADMIN_TOKEN environment variable is required on subsequent boot but is not set")
	}

	// Strip the af_admin_ prefix before hashing.
	const prefix = "af_admin_"
	if !strings.HasPrefix(envToken, prefix) {
		return nil, fmt.Errorf("AF_HUB_ADMIN_TOKEN must start with %q", prefix)
	}
	suffix := envToken[len(prefix):]

	// Compute SHA-256 of the suffix.
	envHash := HashTokenSuffix(suffix)

	// Retrieve stored hash from admin_tokens.
	var storedHash string
	err := db.QueryRow("SELECT token_hash FROM admin_tokens LIMIT 1").Scan(&storedHash)
	if err != nil {
		return nil, fmt.Errorf("failed to read admin token hash from database: %w", err)
	}

	// Compare hashes using constant-time comparison.
	envHashBytes := []byte(envHash)
	storedHashBytes := []byte(storedHash)
	if subtle.ConstantTimeCompare(envHashBytes, storedHashBytes) != 1 {
		return nil, fmt.Errorf("AF_HUB_ADMIN_TOKEN hash does not match stored hash")
	}

	// Check if admin_token file still exists — warn if present, silence if absent.
	tokenFilePath := filepath.Join(configDir, "admin_token")
	absTokenFilePath, err := filepath.Abs(tokenFilePath)
	if err != nil {
		absTokenFilePath = tokenFilePath
	}

	if _, statErr := os.Stat(tokenFilePath); statErr == nil {
		logrus.WithField("path", absTokenFilePath).
			Warn("admin_token plaintext file still exists on disk; delete after securing the token")
	}

	return &BootstrapResult{
		IsFirstBoot: false,
	}, nil
}

// GenerateAdminToken creates a cryptographically random admin token of
// format af_admin_<64 hex chars>. The 64 hex chars are derived from 32
// random bytes encoded as lowercase hexadecimal.
//
// Returns the full plaintext token string.
func GenerateAdminToken() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	suffix := hex.EncodeToString(randomBytes)
	return "af_admin_" + suffix, nil
}

// HashTokenSuffix computes the SHA-256 hex digest of the given token suffix.
// The suffix is the 64 hex-char portion after the af_admin_ prefix.
//
// This is the canonical hash function for admin tokens. The stored hash in
// admin_tokens.token_hash is always computed by this function. Verification
// also uses this function to recompute the hash from the presented suffix.
func HashTokenSuffix(suffix string) string {
	h := sha256.Sum256([]byte(suffix))
	return hex.EncodeToString(h[:])
}

// WriteAdminTokenFile writes the plaintext admin token to the given path
// with file mode 0600 (owner read/write only).
//
// If the file already exists, it is overwritten. The caller should emit
// a warn-level log entry if overwriting.
//
// Returns an error if the file cannot be written (e.g., permission denied).
func WriteAdminTokenFile(path, token string) error {
	return os.WriteFile(path, []byte(token), 0600)
}
