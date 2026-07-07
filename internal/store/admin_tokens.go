package store

import (
	"database/sql"
	"fmt"
)

// CreateAdminToken inserts a new admin token record. The ID and timestamp
// are generated automatically.
func (s *sqliteStore) CreateAdminToken(t *AdminToken) (*AdminToken, error) {
	t.ID = newID()
	t.CreatedAt = nowRFC3339()

	_, err := s.db.Exec(
		`INSERT INTO admin_tokens (id, token_hash, created_at) VALUES (?, ?, ?)`,
		t.ID, t.TokenHash, t.CreatedAt,
	)
	if err != nil {
		if isConstraintError(err) {
			return nil, fmt.Errorf("store: create admin token: %w: %v", ErrConstraintViolation, err)
		}
		return nil, fmt.Errorf("store: create admin token: %w", err)
	}
	return t, nil
}

// GetAdminToken retrieves the first (and typically only) admin token record.
// Returns ErrNotFound if no token exists.
func (s *sqliteStore) GetAdminToken() (*AdminToken, error) {
	t := &AdminToken{}
	err := s.db.QueryRow(
		`SELECT id, token_hash, created_at FROM admin_tokens LIMIT 1`,
	).Scan(&t.ID, &t.TokenHash, &t.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get admin token: %w", err)
	}
	return t, nil
}

// GetAdminTokenByHash retrieves an admin token by its hash value.
// Returns ErrNotFound if no matching token exists.
func (s *sqliteStore) GetAdminTokenByHash(hash string) (*AdminToken, error) {
	t := &AdminToken{}
	err := s.db.QueryRow(
		`SELECT id, token_hash, created_at FROM admin_tokens WHERE token_hash = ?`, hash,
	).Scan(&t.ID, &t.TokenHash, &t.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get admin token by hash: %w", err)
	}
	return t, nil
}

// UpdateAdminToken updates an existing admin token's hash. The created_at
// timestamp is preserved; only the token_hash field is modified.
func (s *sqliteStore) UpdateAdminToken(t *AdminToken) (*AdminToken, error) {
	result, err := s.db.Exec(
		`UPDATE admin_tokens SET token_hash = ? WHERE id = ?`,
		t.TokenHash, t.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: update admin token: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("store: update admin token: %w", err)
	}
	if rows == 0 {
		return nil, ErrNotFound
	}
	return t, nil
}

// DeleteAdminToken removes an admin token by ID.
func (s *sqliteStore) DeleteAdminToken(id string) error {
	result, err := s.db.Exec(`DELETE FROM admin_tokens WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete admin token: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete admin token: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}
