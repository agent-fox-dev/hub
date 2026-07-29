package secrets

import (
	"database/sql"
	"fmt"
	"strings"
)

// GetSecretValue retrieves the plaintext value for a secret identified by
// ownerType, ownerID, and key. It performs a case-insensitive key lookup,
// base64-decodes the stored value, and returns the plaintext string.
//
// Returns NotFoundError if the key does not exist for the given owner.
// Returns an error if the key is empty, the stored base64 value is malformed,
// or a database error occurs.
//
// This method is internal-only and must never be exposed via any HTTP endpoint.
// Secret values must never be returned through the API.
//
// Requirements: 09-REQ-5.1, 09-REQ-5.2, 09-REQ-5.3
func (s *Store) GetSecretValue(ownerType, ownerID, key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("key must not be empty")
	}

	var encoded string
	err := s.db.QueryRow(
		"SELECT value FROM secrets WHERE owner_type = ? AND owner_id = ? AND UPPER(key) = ?",
		ownerType, ownerID, strings.ToUpper(key),
	).Scan(&encoded)
	if err == sql.ErrNoRows {
		return "", &NotFoundError{Key: key}
	}
	if err != nil {
		return "", fmt.Errorf("get secret %q: %w", key, err)
	}

	decoded, err := DecodeValue(encoded)
	if err != nil {
		return "", fmt.Errorf("decode secret %q: %w", key, err)
	}

	return decoded, nil
}
