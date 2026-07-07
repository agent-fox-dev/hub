package store

import (
	"database/sql"
	"fmt"
	"time"
)

// CreateAPIKey inserts a new API key record. The ID and timestamp are
// generated automatically.
func (s *sqliteStore) CreateAPIKey(k *APIKey) (*APIKey, error) {
	k.ID = newID()
	k.CreatedAt = nowRFC3339()

	var expiresAt, revokedAt sql.NullString
	if k.ExpiresAt != nil {
		expiresAt = sql.NullString{String: k.ExpiresAt.UTC().Format(time.RFC3339), Valid: true}
	}
	if k.RevokedAt != nil {
		revokedAt = sql.NullString{String: k.RevokedAt.UTC().Format(time.RFC3339), Valid: true}
	}

	_, err := s.db.Exec(
		`INSERT INTO api_keys (id, key_id, key_hash, user_id, workspace_id, role, label, expires_at, revoked_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		k.ID, k.KeyID, k.KeyHash, k.UserID, k.WorkspaceID, k.Role, k.Label,
		expiresAt, revokedAt, k.CreatedAt,
	)
	if err != nil {
		if isConstraintError(err) {
			return nil, fmt.Errorf("store: create api key: %w: %v", ErrConstraintViolation, err)
		}
		return nil, fmt.Errorf("store: create api key: %w", err)
	}
	return k, nil
}

// GetAPIKeyByID retrieves an API key by primary key. Returns ErrNotFound
// if not found.
func (s *sqliteStore) GetAPIKeyByID(id string) (*APIKey, error) {
	return s.scanAPIKey(s.db.QueryRow(
		`SELECT id, key_id, key_hash, user_id, workspace_id, role, label, expires_at, revoked_at, created_at
		 FROM api_keys WHERE id = ?`, id,
	))
}

// GetAPIKeyByKeyID retrieves an API key by its public key_id. Returns
// ErrNotFound if not found.
func (s *sqliteStore) GetAPIKeyByKeyID(keyID string) (*APIKey, error) {
	return s.scanAPIKey(s.db.QueryRow(
		`SELECT id, key_id, key_hash, user_id, workspace_id, role, label, expires_at, revoked_at, created_at
		 FROM api_keys WHERE key_id = ?`, keyID,
	))
}

// RevokeAPIKey sets the revoked_at timestamp on an API key. Returns
// ErrNotFound if the key does not exist.
func (s *sqliteStore) RevokeAPIKey(id string) error {
	now := nowRFC3339()
	result, err := s.db.Exec(
		`UPDATE api_keys SET revoked_at = ? WHERE id = ?`, now, id,
	)
	if err != nil {
		return fmt.Errorf("store: revoke api key: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: revoke api key: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAPIKey removes an API key by ID.
func (s *sqliteStore) DeleteAPIKey(id string) error {
	result, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete api key: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete api key: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAPIKeys returns all API key records.
func (s *sqliteStore) ListAPIKeys() ([]*APIKey, error) {
	return s.queryAPIKeys(
		`SELECT id, key_id, key_hash, user_id, workspace_id, role, label, expires_at, revoked_at, created_at
		 FROM api_keys ORDER BY created_at ASC`,
	)
}

// ListAPIKeysByUserID returns all API keys belonging to a given user.
func (s *sqliteStore) ListAPIKeysByUserID(userID string) ([]*APIKey, error) {
	return s.queryAPIKeys(
		`SELECT id, key_id, key_hash, user_id, workspace_id, role, label, expires_at, revoked_at, created_at
		 FROM api_keys WHERE user_id = ? ORDER BY created_at ASC`, userID,
	)
}

// CountAPIKeysByWorkspaceID returns the number of API keys for a workspace.
func (s *sqliteStore) CountAPIKeysByWorkspaceID(workspaceID string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM api_keys WHERE workspace_id = ?`, workspaceID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: count api keys by workspace: %w", err)
	}
	return count, nil
}

// UpdateAPIKeyHash replaces the key_hash for the API key identified by keyID.
func (s *sqliteStore) UpdateAPIKeyHash(keyID string, newHash string) error {
	result, err := s.db.Exec(
		`UPDATE api_keys SET key_hash = ? WHERE key_id = ?`, newHash, keyID,
	)
	if err != nil {
		return fmt.Errorf("store: update api key hash: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update api key hash: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// scanAPIKey scans a single api_keys row into an APIKey struct.
func (s *sqliteStore) scanAPIKey(row *sql.Row) (*APIKey, error) {
	k := &APIKey{}
	var expiresAt, revokedAt, role sql.NullString
	err := row.Scan(
		&k.ID, &k.KeyID, &k.KeyHash, &k.UserID, &k.WorkspaceID,
		&role, &k.Label, &expiresAt, &revokedAt, &k.CreatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get api key: %w", err)
	}
	if role.Valid {
		k.Role = role.String
	}
	if expiresAt.Valid {
		t, err := time.Parse(time.RFC3339, expiresAt.String)
		if err == nil {
			k.ExpiresAt = &t
		}
	}
	if revokedAt.Valid {
		t, err := time.Parse(time.RFC3339, revokedAt.String)
		if err == nil {
			k.RevokedAt = &t
		}
	}
	return k, nil
}

// queryAPIKeys runs a multi-row query and returns a slice of APIKey structs.
func (s *sqliteStore) queryAPIKeys(query string, args ...any) ([]*APIKey, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query api keys: %w", err)
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		k := &APIKey{}
		var expiresAt, revokedAt, role sql.NullString
		if err := rows.Scan(
			&k.ID, &k.KeyID, &k.KeyHash, &k.UserID, &k.WorkspaceID,
			&role, &k.Label, &expiresAt, &revokedAt, &k.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: query api keys: scan: %w", err)
		}
		if role.Valid {
			k.Role = role.String
		}
		if expiresAt.Valid {
			t, err := time.Parse(time.RFC3339, expiresAt.String)
			if err == nil {
				k.ExpiresAt = &t
			}
		}
		if revokedAt.Valid {
			t, err := time.Parse(time.RFC3339, revokedAt.String)
			if err == nil {
				k.RevokedAt = &t
			}
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: query api keys: %w", err)
	}
	return keys, nil
}
