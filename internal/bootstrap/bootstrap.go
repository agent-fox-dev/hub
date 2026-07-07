// Package bootstrap handles admin user creation, token generation,
// token validation, and token rotation on server startup.
package bootstrap

import (
	"io"

	"github.com/agent-fox/af-hub/internal/store"
)

// IsFirstBoot returns true when the users table has zero records.
func IsFirstBoot(s *store.Store) (bool, error) {
	// Stub — implementation in a later task group.
	return false, nil
}

// CreateAdminUser creates the bootstrap admin user record.
func CreateAdminUser(s *store.Store) (*store.User, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// GenerateAdminToken produces a cryptographically random admin token
// in the format "af_admin_<64 hex chars>". randReader allows injecting
// a custom source for testing; pass nil to use crypto/rand.Reader.
func GenerateAdminToken(randReader io.Reader) (string, error) {
	// Stub — implementation in a later task group.
	return "", nil
}

// PersistAdminTokenHash computes the SHA-256 hash of the plaintext token
// and stores it in the admin_tokens table.
func PersistAdminTokenHash(s *store.Store, plaintext string) error {
	// Stub — implementation in a later task group.
	return nil
}

// UpdateAdminTokenHash replaces the existing admin token hash (used for
// rotation).
func UpdateAdminTokenHash(s *store.Store, plaintext string) error {
	// Stub — implementation in a later task group.
	return nil
}

// WriteAdminTokenFile writes the plaintext token to a file named
// "admin_token" in the given directory with file mode 0600.
func WriteAdminTokenFile(plaintext, configDir string) error {
	// Stub — implementation in a later task group.
	return nil
}

// RunAdminBootstrap orchestrates the full first-boot admin bootstrap:
// create admin user, generate token, persist hash, write file, log path.
func RunAdminBootstrap(s *store.Store, configDir string) error {
	// Stub — implementation in a later task group.
	return nil
}

// ValidateAdminToken reads AF_HUB_ADMIN_TOKEN from the environment,
// hashes it, and compares against the stored hash. Returns nil on
// success and a descriptive error on any failure.
func ValidateAdminToken(s *store.Store) error {
	// Stub — implementation in a later task group.
	return nil
}

// RotateAdminToken generates a new admin token, writes it to the file,
// and updates the stored hash. Used by --reset-admin-token.
func RotateAdminToken(s *store.Store, configDir string) error {
	// Stub — implementation in a later task group.
	return nil
}
