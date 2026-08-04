package secrets

import (
	"database/sql"
	"fmt"
	"strings"
)

// GetVariableValue retrieves the plaintext value for a variable identified by
// ownerType, ownerID, and key. It performs a case-insensitive key lookup,
// base64-decodes the stored value, and returns the plaintext string.
//
// Returns NotFoundError if the key does not exist for the given owner.
// Returns an error if the key is empty, the stored base64 value is malformed,
// or a database error occurs.
//
// This method mirrors GetSecretValue but reads from the variables table.
// Unlike secrets, variable values may be returned through the API and are
// used for configuration lookups (e.g., CHECK_COMMAND in merge operations).
func (s *Store) GetVariableValue(ownerType, ownerID, key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("key must not be empty")
	}

	var encoded string
	err := s.db.QueryRow(
		"SELECT value FROM variables WHERE owner_type = ? AND owner_id = ? AND UPPER(key) = ?",
		ownerType, ownerID, strings.ToUpper(key),
	).Scan(&encoded)
	if err == sql.ErrNoRows {
		return "", &NotFoundError{Key: key}
	}
	if err != nil {
		return "", fmt.Errorf("get variable %q: %w", key, err)
	}

	decoded, err := DecodeValue(encoded)
	if err != nil {
		return "", fmt.Errorf("decode variable %q: %w", key, err)
	}

	return decoded, nil
}
