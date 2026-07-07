// Package bootstrap handles admin user creation, token generation,
// token validation, and token rotation on server startup.
package bootstrap

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/agent-fox/af-hub/internal/store"
	"github.com/sirupsen/logrus"
)

// IsFirstBoot returns true when the users table has zero records.
func IsFirstBoot(s store.Store) (bool, error) {
	count, err := s.CountUsers()
	if err != nil {
		return false, fmt.Errorf("bootstrap: check first boot: %w", err)
	}
	return count == 0, nil
}

// CreateAdminUser creates the bootstrap admin user record.
func CreateAdminUser(s store.Store) (*store.User, error) {
	user, err := s.CreateUser(&store.User{
		Username:   "admin",
		Email:      "admin@localhost",
		Provider:   "local",
		ProviderID: "admin",
		Status:     "active",
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: create admin user: %w", err)
	}
	return user, nil
}

// GenerateAdminToken produces a cryptographically random admin token
// in the format "af_admin_<64 hex chars>". randReader allows injecting
// a custom source for testing; pass nil to use crypto/rand.Reader.
func GenerateAdminToken(randReader io.Reader) (string, error) {
	if randReader == nil {
		randReader = rand.Reader
	}

	buf := make([]byte, 32)
	_, err := io.ReadFull(randReader, buf)
	if err != nil {
		return "", fmt.Errorf("bootstrap: generate admin token: %w", err)
	}

	token := "af_admin_" + hex.EncodeToString(buf)
	return token, nil
}

// computeSHA256Hex computes the hex-encoded SHA-256 hash of a string.
func computeSHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// PersistAdminTokenHash computes the SHA-256 hash of the plaintext token
// and stores it in the admin_tokens table.
func PersistAdminTokenHash(s store.Store, plaintext string) error {
	hash := computeSHA256Hex(plaintext)
	_, err := s.CreateAdminToken(&store.AdminToken{
		TokenHash: hash,
	})
	if err != nil {
		return fmt.Errorf("bootstrap: persist admin token hash: %w", err)
	}
	return nil
}

// UpdateAdminTokenHash replaces the existing admin token hash (used for
// rotation).
func UpdateAdminTokenHash(s store.Store, plaintext string) error {
	existing, err := s.GetAdminToken()
	if err != nil {
		return fmt.Errorf("bootstrap: update admin token hash: get existing: %w", err)
	}

	hash := computeSHA256Hex(plaintext)
	existing.TokenHash = hash
	_, err = s.UpdateAdminToken(existing)
	if err != nil {
		return fmt.Errorf("bootstrap: update admin token hash: %w", err)
	}
	return nil
}

// WriteAdminTokenFile writes the plaintext token to a file named
// "admin_token" in the given directory with file mode 0600.
// Uses atomic write (temp file + rename) to avoid partial writes and
// to correctly detect directory permission errors.
func WriteAdminTokenFile(plaintext, configDir string) error {
	filePath := filepath.Join(configDir, "admin_token")

	// Write to a temporary file in the same directory first, then rename.
	// This ensures we detect directory permission errors (creating a new
	// file in a read-only directory fails) and avoids partial writes.
	tmpFile, err := os.CreateTemp(configDir, ".admin_token.tmp.*")
	if err != nil {
		return fmt.Errorf("bootstrap: write admin token file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write([]byte(plaintext)); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: write admin token file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: write admin token file: %w", err)
	}

	// Set the correct file permissions before rename.
	if err := os.Chmod(tmpPath, 0600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: write admin token file: chmod: %w", err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("bootstrap: write admin token file: %w", err)
	}

	return nil
}

// RunAdminBootstrap orchestrates the full first-boot admin bootstrap:
// create admin user, generate token, persist hash, write file, log path.
// On failure to write the file, the admin user is cleaned up.
func RunAdminBootstrap(s store.Store, configDir string) error {
	// Step 1: Create the admin user.
	user, err := CreateAdminUser(s)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	// Step 2: Generate the admin token.
	token, err := GenerateAdminToken(nil)
	if err != nil {
		// Clean up the admin user.
		_ = s.DeleteUser(user.ID)
		return fmt.Errorf("bootstrap: %w", err)
	}

	// Step 3: Persist the token hash.
	err = PersistAdminTokenHash(s, token)
	if err != nil {
		// Clean up the admin user.
		_ = s.DeleteUser(user.ID)
		return fmt.Errorf("bootstrap: %w", err)
	}

	// Step 4: Write the plaintext token to file.
	err = WriteAdminTokenFile(token, configDir)
	if err != nil {
		// Clean up: remove any partial file.
		os.Remove(filepath.Join(configDir, "admin_token"))
		// Clean up the admin user.
		_ = s.DeleteUser(user.ID)
		return fmt.Errorf("bootstrap: %w", err)
	}

	// Step 5: Log the absolute path of the admin_token file at warn level.
	absPath, err := filepath.Abs(filepath.Join(configDir, "admin_token"))
	if err != nil {
		absPath = filepath.Join(configDir, "admin_token")
	}
	logrus.Warnf("admin token written to: %s", absPath)

	return nil
}

// ValidateAdminToken reads AF_HUB_ADMIN_TOKEN from the environment,
// hashes it, and compares against the stored hash. Returns nil on
// success and a descriptive error on any failure.
func ValidateAdminToken(s store.Store) error {
	envToken := os.Getenv("AF_HUB_ADMIN_TOKEN")
	if envToken == "" {
		return fmt.Errorf("AF_HUB_ADMIN_TOKEN environment variable is missing or empty")
	}

	storedToken, err := s.GetAdminToken()
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("corrupted token state: no token found in admin_tokens table")
		}
		return fmt.Errorf("bootstrap: validate admin token: %w", err)
	}

	envHash := computeSHA256Hex(envToken)
	if envHash != storedToken.TokenHash {
		return fmt.Errorf("admin token mismatch: provided token does not match stored hash")
	}

	return nil
}

// RotateAdminToken generates a new admin token, writes it to the file,
// and updates the stored hash. Used by --reset-admin-token.
// The file is written before the DB is updated so that a file write
// failure leaves the old token intact.
func RotateAdminToken(s store.Store, configDir string) error {
	// Step 1: Generate a new token.
	token, err := GenerateAdminToken(nil)
	if err != nil {
		return fmt.Errorf("bootstrap: rotate: %w", err)
	}

	// Step 2: Write the file first — if this fails, the old DB hash
	// remains intact.
	err = WriteAdminTokenFile(token, configDir)
	if err != nil {
		return fmt.Errorf("bootstrap: rotate: %w", err)
	}

	// Step 3: Update the DB hash.
	err = UpdateAdminTokenHash(s, token)
	if err != nil {
		// Rollback: we already wrote the file, but the DB update failed.
		// This is an edge case; the old token file was already overwritten.
		return fmt.Errorf("bootstrap: rotate: update hash: %w", err)
	}

	// Step 4: Log the absolute path.
	absPath, err := filepath.Abs(filepath.Join(configDir, "admin_token"))
	if err != nil {
		absPath = filepath.Join(configDir, "admin_token")
	}
	logrus.Warnf("admin token rotated, written to: %s", absPath)

	return nil
}
