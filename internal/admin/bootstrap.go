// Package admin handles admin token bootstrap, validation, and rotation
// for af-hub server startup. It manages the admin user account, admin
// token generation, SHA-256 hash storage, and the admin_token plaintext
// file lifecycle.
//
// Implementation will be added in task group 11.
package admin

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	// Stub: returns nil result and nil error.
	// Implementation will be added in task group 11.
	return nil, nil
}

// GenerateAdminToken creates a cryptographically random admin token of
// format af_admin_<64 hex chars>. The 64 hex chars are derived from 32
// random bytes encoded as lowercase hexadecimal.
//
// Returns the full plaintext token string.
func GenerateAdminToken() (string, error) {
	// Stub: returns empty string and nil error.
	// Implementation will be added in task group 11.
	return "", nil
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
	// Stub: returns nil (no-op).
	// Implementation will be added in task group 11.
	return nil
}
