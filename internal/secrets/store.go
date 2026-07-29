package secrets

import (
	"database/sql"
	"fmt"
)

// Store provides data access for secrets and variables.
type Store struct {
	db *sql.DB
	// TestHookAfterParentDelete is called during cascade deletion after the parent
	// row is deleted but before child secrets/variables rows are deleted.
	// If non-nil and it returns an error, the transaction is rolled back.
	// Only used in tests.
	TestHookAfterParentDelete func() error
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

// ResolveVariables fetches and merges variables across user, org (if non-empty),
// and workspace tiers using resolution order (workspace > org > user).
// Returns the merged set sorted alphabetically ascending by key (case-insensitive)
// with an origin field indicating which tier each value came from.
func (s *Store) ResolveVariables(userID, orgID, workspaceSlug string) ([]ResolvedVariableEntry, error) {
	return nil, fmt.Errorf("not implemented")
}

// DeleteUserCascade deletes a user and all associated secrets and variables
// within a single database transaction.
func (s *Store) DeleteUserCascade(userID string) error {
	return fmt.Errorf("not implemented")
}

// DeleteOrgCascade deletes an org and all associated secrets and variables
// within a single database transaction.
func (s *Store) DeleteOrgCascade(orgID string) error {
	return fmt.Errorf("not implemented")
}

// DeleteWorkspaceCascade deletes a workspace and all associated secrets and
// variables within a single database transaction.
func (s *Store) DeleteWorkspaceCascade(slug string) error {
	return fmt.Errorf("not implemented")
}
