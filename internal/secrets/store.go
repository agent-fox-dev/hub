package secrets

import (
	"database/sql"
	"fmt"
)

// Store provides data access for secrets and variables.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store backed by the given database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// CreateSecrets creates one or more secrets in the given scope.
// It validates entries, checks the per-scope limit, encodes values as base64,
// and inserts rows within a single write transaction.
func (s *Store) CreateSecrets(ownerType, ownerID string, entries []EntryInput) ([]SecretEntry, error) {
	return nil, fmt.Errorf("not implemented")
}

// ListSecrets lists all secrets in the given scope.
// Returns key and timestamps only; values are never returned.
func (s *Store) ListSecrets(ownerType, ownerID string) ([]SecretEntry, error) {
	return nil, fmt.Errorf("not implemented")
}

// UpdateSecret updates the value of an existing secret.
func (s *Store) UpdateSecret(ownerType, ownerID, key, value string) (*SecretEntry, error) {
	return nil, fmt.Errorf("not implemented")
}

// DeleteSecret deletes a secret by key.
func (s *Store) DeleteSecret(ownerType, ownerID, key string) error {
	return fmt.Errorf("not implemented")
}

// CreateVariables creates one or more variables in the given scope.
// It validates entries, checks the per-scope limit, encodes values as base64,
// and inserts rows within a single write transaction.
func (s *Store) CreateVariables(ownerType, ownerID string, entries []EntryInput) ([]VariableEntry, error) {
	return nil, fmt.Errorf("not implemented")
}

// ListVariables lists all variables in the given scope.
// Returns key, decoded value, and timestamps.
func (s *Store) ListVariables(ownerType, ownerID string) ([]VariableEntry, error) {
	return nil, fmt.Errorf("not implemented")
}

// UpdateVariable updates the value of an existing variable.
func (s *Store) UpdateVariable(ownerType, ownerID, key, value string) (*VariableEntry, error) {
	return nil, fmt.Errorf("not implemented")
}

// DeleteVariable deletes a variable by key.
func (s *Store) DeleteVariable(ownerType, ownerID, key string) error {
	return fmt.Errorf("not implemented")
}
