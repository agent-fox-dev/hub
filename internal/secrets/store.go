package secrets

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
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

// ---------------------------------------------------------------------------
// Secrets CRUD
// ---------------------------------------------------------------------------

// CreateSecrets creates one or more secrets in the given scope.
// It validates entries, checks the per-scope limit, encodes values as base64,
// and inserts rows within a single write transaction.
func (s *Store) CreateSecrets(ownerType, ownerID string, entries []EntryInput) ([]SecretEntry, error) {
	if err := ValidateEntries(entries); err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Check per-scope limit.
	var count int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ?",
		ownerType, ownerID,
	).Scan(&count); err != nil {
		return nil, fmt.Errorf("count secrets: %w", err)
	}
	if count+len(entries) > MaxEntriesPerScope {
		return nil, fmt.Errorf("maximum of %d entries per scope exceeded", MaxEntriesPerScope)
	}

	// Check for cross-request duplicate keys (case-insensitive).
	for _, e := range entries {
		var existing int
		if err := tx.QueryRow(
			"SELECT COUNT(*) FROM secrets WHERE owner_type = ? AND owner_id = ? AND UPPER(key) = ?",
			ownerType, ownerID, strings.ToUpper(e.Key),
		).Scan(&existing); err != nil {
			return nil, fmt.Errorf("check existing key: %w", err)
		}
		if existing > 0 {
			return nil, &ConflictError{Key: e.Key}
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	result := make([]SecretEntry, 0, len(entries))

	for _, e := range entries {
		encoded := EncodeValue(e.Value)
		if _, err := tx.Exec(
			"INSERT INTO secrets (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			ownerType, ownerID, e.Key, encoded, now, now,
		); err != nil {
			return nil, fmt.Errorf("insert secret %q: %w", e.Key, err)
		}
		result = append(result, SecretEntry{
			Key:       e.Key,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return result, nil
}

// ListSecrets lists all secrets in the given scope.
// Returns key and timestamps only; values are never returned.
// Results are sorted alphabetically ascending by key (case-insensitive).
func (s *Store) ListSecrets(ownerType, ownerID string) ([]SecretEntry, error) {
	rows, err := s.db.Query(
		"SELECT key, created_at, updated_at FROM secrets WHERE owner_type = ? AND owner_id = ? ORDER BY UPPER(key) ASC",
		ownerType, ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	defer rows.Close()

	var result []SecretEntry
	for rows.Next() {
		var e SecretEntry
		if err := rows.Scan(&e.Key, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan secret: %w", err)
		}
		result = append(result, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate secrets: %w", err)
	}
	if result == nil {
		result = []SecretEntry{}
	}
	return result, nil
}

// UpdateSecret updates the value of an existing secret.
// Performs case-insensitive key lookup and returns the originally stored key casing.
func (s *Store) UpdateSecret(ownerType, ownerID, key, value string) (*SecretEntry, error) {
	if err := ValidateValue(value); err != nil {
		return nil, err
	}

	encoded := EncodeValue(value)
	now := time.Now().UTC().Format(time.RFC3339)

	// Find the actual stored key (case-insensitive lookup).
	var storedKey, createdAt string
	err := s.db.QueryRow(
		"SELECT key, created_at FROM secrets WHERE owner_type = ? AND owner_id = ? AND UPPER(key) = ?",
		ownerType, ownerID, strings.ToUpper(key),
	).Scan(&storedKey, &createdAt)
	if err == sql.ErrNoRows {
		return nil, &NotFoundError{Key: key}
	}
	if err != nil {
		return nil, fmt.Errorf("lookup secret: %w", err)
	}

	if _, err := s.db.Exec(
		"UPDATE secrets SET value = ?, updated_at = ? WHERE owner_type = ? AND owner_id = ? AND key = ?",
		encoded, now, ownerType, ownerID, storedKey,
	); err != nil {
		return nil, fmt.Errorf("update secret: %w", err)
	}

	return &SecretEntry{
		Key:       storedKey,
		CreatedAt: createdAt,
		UpdatedAt: now,
	}, nil
}

// DeleteSecret deletes a secret by key (case-insensitive lookup).
func (s *Store) DeleteSecret(ownerType, ownerID, key string) error {
	res, err := s.db.Exec(
		"DELETE FROM secrets WHERE owner_type = ? AND owner_id = ? AND UPPER(key) = ?",
		ownerType, ownerID, strings.ToUpper(key),
	)
	if err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return &NotFoundError{Key: key}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Variables CRUD
// ---------------------------------------------------------------------------

// CreateVariables creates one or more variables in the given scope.
// It validates entries, checks the per-scope limit, encodes values as base64,
// and inserts rows within a single write transaction.
func (s *Store) CreateVariables(ownerType, ownerID string, entries []EntryInput) ([]VariableEntry, error) {
	if err := ValidateEntries(entries); err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Check per-scope limit.
	var count int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM variables WHERE owner_type = ? AND owner_id = ?",
		ownerType, ownerID,
	).Scan(&count); err != nil {
		return nil, fmt.Errorf("count variables: %w", err)
	}
	if count+len(entries) > MaxEntriesPerScope {
		return nil, fmt.Errorf("maximum of %d entries per scope exceeded", MaxEntriesPerScope)
	}

	// Check for cross-request duplicate keys (case-insensitive).
	for _, e := range entries {
		var existing int
		if err := tx.QueryRow(
			"SELECT COUNT(*) FROM variables WHERE owner_type = ? AND owner_id = ? AND UPPER(key) = ?",
			ownerType, ownerID, strings.ToUpper(e.Key),
		).Scan(&existing); err != nil {
			return nil, fmt.Errorf("check existing key: %w", err)
		}
		if existing > 0 {
			return nil, &ConflictError{Key: e.Key}
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	result := make([]VariableEntry, 0, len(entries))

	for _, e := range entries {
		encoded := EncodeValue(e.Value)
		if _, err := tx.Exec(
			"INSERT INTO variables (owner_type, owner_id, key, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			ownerType, ownerID, e.Key, encoded, now, now,
		); err != nil {
			return nil, fmt.Errorf("insert variable %q: %w", e.Key, err)
		}
		result = append(result, VariableEntry{
			Key:       e.Key,
			Value:     e.Value,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return result, nil
}

// ListVariables lists all variables in the given scope.
// Returns key, decoded value, and timestamps, sorted alphabetically ascending
// by key (case-insensitive).
func (s *Store) ListVariables(ownerType, ownerID string) ([]VariableEntry, error) {
	rows, err := s.db.Query(
		"SELECT key, value, created_at, updated_at FROM variables WHERE owner_type = ? AND owner_id = ? ORDER BY UPPER(key) ASC",
		ownerType, ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list variables: %w", err)
	}
	defer rows.Close()

	var result []VariableEntry
	for rows.Next() {
		var e VariableEntry
		var encoded string
		if err := rows.Scan(&e.Key, &encoded, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan variable: %w", err)
		}
		decoded, err := DecodeValue(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode variable %q: %w", e.Key, err)
		}
		e.Value = decoded
		result = append(result, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate variables: %w", err)
	}
	if result == nil {
		result = []VariableEntry{}
	}
	return result, nil
}

// UpdateVariable updates the value of an existing variable.
// Performs case-insensitive key lookup and returns the originally stored key casing.
func (s *Store) UpdateVariable(ownerType, ownerID, key, value string) (*VariableEntry, error) {
	if err := ValidateValue(value); err != nil {
		return nil, err
	}

	encoded := EncodeValue(value)
	now := time.Now().UTC().Format(time.RFC3339)

	// Find the actual stored key (case-insensitive lookup).
	var storedKey, createdAt string
	err := s.db.QueryRow(
		"SELECT key, created_at FROM variables WHERE owner_type = ? AND owner_id = ? AND UPPER(key) = ?",
		ownerType, ownerID, strings.ToUpper(key),
	).Scan(&storedKey, &createdAt)
	if err == sql.ErrNoRows {
		return nil, &NotFoundError{Key: key}
	}
	if err != nil {
		return nil, fmt.Errorf("lookup variable: %w", err)
	}

	if _, err := s.db.Exec(
		"UPDATE variables SET value = ?, updated_at = ? WHERE owner_type = ? AND owner_id = ? AND key = ?",
		encoded, now, ownerType, ownerID, storedKey,
	); err != nil {
		return nil, fmt.Errorf("update variable: %w", err)
	}

	return &VariableEntry{
		Key:       storedKey,
		Value:     value,
		CreatedAt: createdAt,
		UpdatedAt: now,
	}, nil
}

// DeleteVariable deletes a variable by key (case-insensitive lookup).
func (s *Store) DeleteVariable(ownerType, ownerID, key string) error {
	res, err := s.db.Exec(
		"DELETE FROM variables WHERE owner_type = ? AND owner_id = ? AND UPPER(key) = ?",
		ownerType, ownerID, strings.ToUpper(key),
	)
	if err != nil {
		return fmt.Errorf("delete variable: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return &NotFoundError{Key: key}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Variable Resolution
// ---------------------------------------------------------------------------

// ResolveVariables fetches and merges variables across user, org (if non-empty),
// and workspace tiers using resolution order (workspace > org > user).
// Returns the merged set sorted alphabetically ascending by key (case-insensitive)
// with an origin field indicating which tier each value came from.
func (s *Store) ResolveVariables(userID, orgID, workspaceSlug string) ([]ResolvedVariableEntry, error) {
	// Build merged map: key (uppercased) -> ResolvedVariableEntry.
	// Process tiers from lowest to highest priority so higher tiers overwrite.
	merged := make(map[string]ResolvedVariableEntry)

	// User tier (lowest priority).
	userVars, err := s.listVariablesRaw("user", userID)
	if err != nil {
		return nil, fmt.Errorf("resolve user variables: %w", err)
	}
	for _, v := range userVars {
		merged[strings.ToUpper(v.Key)] = ResolvedVariableEntry{
			Key:       v.Key,
			Value:     v.Value,
			Origin:    "user",
			CreatedAt: v.CreatedAt,
			UpdatedAt: v.UpdatedAt,
		}
	}

	// Org tier (middle priority) — skip if orgID is empty.
	if orgID != "" {
		orgVars, err := s.listVariablesRaw("org", orgID)
		if err != nil {
			return nil, fmt.Errorf("resolve org variables: %w", err)
		}
		for _, v := range orgVars {
			merged[strings.ToUpper(v.Key)] = ResolvedVariableEntry{
				Key:       v.Key,
				Value:     v.Value,
				Origin:    "org",
				CreatedAt: v.CreatedAt,
				UpdatedAt: v.UpdatedAt,
			}
		}
	}

	// Workspace tier (highest priority).
	wsVars, err := s.listVariablesRaw("workspace", workspaceSlug)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace variables: %w", err)
	}
	for _, v := range wsVars {
		merged[strings.ToUpper(v.Key)] = ResolvedVariableEntry{
			Key:       v.Key,
			Value:     v.Value,
			Origin:    "workspace",
			CreatedAt: v.CreatedAt,
			UpdatedAt: v.UpdatedAt,
		}
	}

	// Collect and sort by key (case-insensitive).
	result := make([]ResolvedVariableEntry, 0, len(merged))
	for _, v := range merged {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToUpper(result[i].Key) < strings.ToUpper(result[j].Key)
	})

	return result, nil
}

// listVariablesRaw fetches variables for a scope and decodes values.
// Used internally by ResolveVariables.
func (s *Store) listVariablesRaw(ownerType, ownerID string) ([]VariableEntry, error) {
	return s.ListVariables(ownerType, ownerID)
}

// ---------------------------------------------------------------------------
// Cascading Deletion
// ---------------------------------------------------------------------------

// DeleteUserCascade deletes a user and all associated secrets and variables
// within a single database transaction.
func (s *Store) DeleteUserCascade(userID string) error {
	return s.cascadeDelete("DELETE FROM users WHERE id = ?", "user", userID)
}

// DeleteOrgCascade deletes an org and all associated secrets and variables
// within a single database transaction.
func (s *Store) DeleteOrgCascade(orgID string) error {
	return s.cascadeDelete("DELETE FROM orgs WHERE id = ?", "org", orgID)
}

// DeleteWorkspaceCascade deletes a workspace and all associated secrets and
// variables within a single database transaction.
func (s *Store) DeleteWorkspaceCascade(slug string) error {
	return s.cascadeDelete("DELETE FROM workspaces WHERE slug = ?", "workspace", slug)
}

// cascadeDelete wraps parent resource deletion and child secrets/variables
// deletion in a single database transaction.
func (s *Store) cascadeDelete(parentSQL, ownerType, ownerID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete the parent resource.
	if _, err := tx.Exec(parentSQL, ownerID); err != nil {
		return fmt.Errorf("delete parent: %w", err)
	}

	// Test hook — fires after parent is deleted but before children.
	if s.TestHookAfterParentDelete != nil {
		if err := s.TestHookAfterParentDelete(); err != nil {
			return fmt.Errorf("hook after parent delete: %w", err)
		}
	}

	// Delete child secrets.
	if _, err := tx.Exec(
		"DELETE FROM secrets WHERE owner_type = ? AND owner_id = ?",
		ownerType, ownerID,
	); err != nil {
		return fmt.Errorf("delete secrets: %w", err)
	}

	// Delete child variables.
	if _, err := tx.Exec(
		"DELETE FROM variables WHERE owner_type = ? AND owner_id = ?",
		ownerType, ownerID,
	); err != nil {
		return fmt.Errorf("delete variables: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

// ConflictError is returned when a key already exists at the same scope.
type ConflictError struct {
	Key string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("key %q already exists", e.Key)
}

// NotFoundError is returned when a key is not found at the given scope.
type NotFoundError struct {
	Key string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("key %q not found", e.Key)
}
