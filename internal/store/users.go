package store

import (
	"database/sql"
	"fmt"
)

// CreateUser inserts a new user record. The ID and timestamps are generated
// automatically. Returns the created user or a wrapped error.
func (s *sqliteStore) CreateUser(u *User) (*User, error) {
	u.ID = newID()
	now := nowRFC3339()
	u.CreatedAt = now
	u.UpdatedAt = now

	_, err := s.db.Exec(
		`INSERT INTO users (id, username, email, full_name, provider, provider_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.Email, u.FullName, u.Provider, u.ProviderID, u.Status, u.CreatedAt, u.UpdatedAt,
	)
	if err != nil {
		if isConstraintError(err) {
			return nil, fmt.Errorf("store: create user: %w: %v", ErrConstraintViolation, err)
		}
		return nil, fmt.Errorf("store: create user: %w", err)
	}
	return u, nil
}

// GetUserByID retrieves a user by primary key. Returns ErrNotFound if the
// user does not exist.
func (s *sqliteStore) GetUserByID(id string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		`SELECT id, username, email, full_name, provider, provider_id, status, created_at, updated_at
		 FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Username, &u.Email, &u.FullName, &u.Provider, &u.ProviderID, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get user by id: %w", err)
	}
	return u, nil
}

// GetUserByUsername retrieves a user by their unique username. Returns
// ErrNotFound if the user does not exist.
func (s *sqliteStore) GetUserByUsername(username string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		`SELECT id, username, email, full_name, provider, provider_id, status, created_at, updated_at
		 FROM users WHERE username = ?`, username,
	).Scan(&u.ID, &u.Username, &u.Email, &u.FullName, &u.Provider, &u.ProviderID, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get user by username: %w", err)
	}
	return u, nil
}

// GetUserByProviderID retrieves a user by their (provider, provider_id)
// composite key. Returns ErrNotFound if not found.
func (s *sqliteStore) GetUserByProviderID(provider, providerID string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(
		`SELECT id, username, email, full_name, provider, provider_id, status, created_at, updated_at
		 FROM users WHERE provider = ? AND provider_id = ?`, provider, providerID,
	).Scan(&u.ID, &u.Username, &u.Email, &u.FullName, &u.Provider, &u.ProviderID, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get user by provider id: %w", err)
	}
	return u, nil
}

// UpdateUser updates a user record's mutable fields (username, email,
// full_name, status). The updated_at timestamp is refreshed automatically.
func (s *sqliteStore) UpdateUser(u *User) (*User, error) {
	u.UpdatedAt = nowRFC3339()
	result, err := s.db.Exec(
		`UPDATE users SET username = ?, email = ?, full_name = ?, status = ?, updated_at = ?
		 WHERE id = ?`,
		u.Username, u.Email, u.FullName, u.Status, u.UpdatedAt, u.ID,
	)
	if err != nil {
		if isConstraintError(err) {
			return nil, fmt.Errorf("store: update user: %w: %v", ErrConstraintViolation, err)
		}
		return nil, fmt.Errorf("store: update user: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("store: update user: %w", err)
	}
	if rows == 0 {
		return nil, ErrNotFound
	}
	return u, nil
}

// DeleteUser removes a user by ID. Returns ErrNotFound if the user does
// not exist.
func (s *sqliteStore) DeleteUser(id string) error {
	result, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete user: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete user: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ListUsers returns all user records.
func (s *sqliteStore) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(
		`SELECT id, username, email, full_name, provider, provider_id, status, created_at, updated_at
		 FROM users ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.FullName, &u.Provider, &u.ProviderID, &u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: list users: scan: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	return users, nil
}

// CountUsers returns the number of user records.
func (s *sqliteStore) CountUsers() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return count, nil
}
