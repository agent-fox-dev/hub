package store

import (
	"database/sql"
	"fmt"
)

// CreateWorkspace inserts a new workspace record. The ID and timestamp are
// generated automatically.
func (s *sqliteStore) CreateWorkspace(w *Workspace) (*Workspace, error) {
	w.ID = newID()
	w.CreatedAt = nowRFC3339()

	_, err := s.db.Exec(
		`INSERT INTO workspaces (id, name, slug, url, status, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.Name, w.Slug, w.URL, w.Status, w.CreatedAt, w.CreatedBy,
	)
	if err != nil {
		if isConstraintError(err) {
			return nil, fmt.Errorf("store: create workspace: %w: %v", ErrConstraintViolation, err)
		}
		return nil, fmt.Errorf("store: create workspace: %w", err)
	}
	return w, nil
}

// GetWorkspaceByID retrieves a workspace by primary key. Returns ErrNotFound
// if it does not exist.
func (s *sqliteStore) GetWorkspaceByID(id string) (*Workspace, error) {
	w := &Workspace{}
	var createdBy sql.NullString
	err := s.db.QueryRow(
		`SELECT id, name, slug, url, status, created_at, created_by
		 FROM workspaces WHERE id = ?`, id,
	).Scan(&w.ID, &w.Name, &w.Slug, &w.URL, &w.Status, &w.CreatedAt, &createdBy)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get workspace by id: %w", err)
	}
	if createdBy.Valid {
		w.CreatedBy = createdBy.String
	}
	return w, nil
}

// GetWorkspaceBySlug retrieves a workspace by its unique slug. Returns
// ErrNotFound if not found.
func (s *sqliteStore) GetWorkspaceBySlug(slug string) (*Workspace, error) {
	w := &Workspace{}
	var createdBy sql.NullString
	err := s.db.QueryRow(
		`SELECT id, name, slug, url, status, created_at, created_by
		 FROM workspaces WHERE slug = ?`, slug,
	).Scan(&w.ID, &w.Name, &w.Slug, &w.URL, &w.Status, &w.CreatedAt, &createdBy)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get workspace by slug: %w", err)
	}
	if createdBy.Valid {
		w.CreatedBy = createdBy.String
	}
	return w, nil
}

// UpdateWorkspace updates a workspace's mutable fields (name, slug, url,
// status).
func (s *sqliteStore) UpdateWorkspace(w *Workspace) (*Workspace, error) {
	result, err := s.db.Exec(
		`UPDATE workspaces SET name = ?, slug = ?, url = ?, status = ? WHERE id = ?`,
		w.Name, w.Slug, w.URL, w.Status, w.ID,
	)
	if err != nil {
		if isConstraintError(err) {
			return nil, fmt.Errorf("store: update workspace: %w: %v", ErrConstraintViolation, err)
		}
		return nil, fmt.Errorf("store: update workspace: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("store: update workspace: %w", err)
	}
	if rows == 0 {
		return nil, ErrNotFound
	}
	return w, nil
}

// DeleteWorkspace removes a workspace by ID. Does not cascade — use
// DeleteWorkspaceWithCascade for transactional deletion of memberships
// and API keys.
func (s *sqliteStore) DeleteWorkspace(id string) error {
	result, err := s.db.Exec(`DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete workspace: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete workspace: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// ListWorkspaces returns all workspace records. When includeArchived is
// false, workspaces with status 'archived' are excluded.
func (s *sqliteStore) ListWorkspaces(includeArchived bool) ([]*Workspace, error) {
	query := `SELECT id, name, slug, url, status, created_at, created_by FROM workspaces`
	if !includeArchived {
		query += ` WHERE status != 'archived'`
	}
	query += ` ORDER BY created_at ASC`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("store: list workspaces: %w", err)
	}
	defer rows.Close()

	var workspaces []*Workspace
	for rows.Next() {
		w := &Workspace{}
		var createdBy sql.NullString
		if err := rows.Scan(&w.ID, &w.Name, &w.Slug, &w.URL, &w.Status, &w.CreatedAt, &createdBy); err != nil {
			return nil, fmt.Errorf("store: list workspaces: scan: %w", err)
		}
		if createdBy.Valid {
			w.CreatedBy = createdBy.String
		}
		workspaces = append(workspaces, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list workspaces: %w", err)
	}
	return workspaces, nil
}

// DeleteWorkspaceWithCascade removes a workspace and its associated
// workspace_members and api_keys records in a single transaction.
func (s *sqliteStore) DeleteWorkspaceWithCascade(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: delete workspace cascade: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Delete associated API keys.
	if _, err := tx.Exec(`DELETE FROM api_keys WHERE workspace_id = ?`, id); err != nil {
		return fmt.Errorf("store: delete workspace cascade: delete api_keys: %w", err)
	}

	// Delete associated workspace members.
	if _, err := tx.Exec(`DELETE FROM workspace_members WHERE workspace_id = ?`, id); err != nil {
		return fmt.Errorf("store: delete workspace cascade: delete members: %w", err)
	}

	// Delete the workspace itself.
	result, err := tx.Exec(`DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete workspace cascade: delete workspace: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete workspace cascade: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete workspace cascade: commit: %w", err)
	}
	return nil
}
