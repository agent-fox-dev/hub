package store

import (
	"database/sql"
	"fmt"
)

// CreateWorkspaceMember inserts a new workspace membership record.
func (s *sqliteStore) CreateWorkspaceMember(m *WorkspaceMember) (*WorkspaceMember, error) {
	m.CreatedAt = nowRFC3339()

	_, err := s.db.Exec(
		`INSERT INTO workspace_members (user_id, workspace_id, role, created_at, granted_by)
		 VALUES (?, ?, ?, ?, ?)`,
		m.UserID, m.WorkspaceID, m.Role, m.CreatedAt, m.GrantedBy,
	)
	if err != nil {
		if isConstraintError(err) {
			return nil, fmt.Errorf("store: create workspace member: %w: %v", ErrConstraintViolation, err)
		}
		return nil, fmt.Errorf("store: create workspace member: %w", err)
	}
	return m, nil
}

// GetWorkspaceMember retrieves a membership by the composite key
// (user_id, workspace_id). Returns ErrNotFound if not found.
func (s *sqliteStore) GetWorkspaceMember(userID, workspaceID string) (*WorkspaceMember, error) {
	m := &WorkspaceMember{}
	var grantedBy sql.NullString
	err := s.db.QueryRow(
		`SELECT user_id, workspace_id, role, created_at, granted_by
		 FROM workspace_members WHERE user_id = ? AND workspace_id = ?`,
		userID, workspaceID,
	).Scan(&m.UserID, &m.WorkspaceID, &m.Role, &m.CreatedAt, &grantedBy)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get workspace member: %w", err)
	}
	if grantedBy.Valid {
		m.GrantedBy = grantedBy.String
	}
	return m, nil
}

// ListWorkspaceMembers returns all membership records for a given workspace.
func (s *sqliteStore) ListWorkspaceMembers(workspaceID string) ([]*WorkspaceMember, error) {
	rows, err := s.db.Query(
		`SELECT user_id, workspace_id, role, created_at, granted_by
		 FROM workspace_members WHERE workspace_id = ? ORDER BY created_at ASC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list workspace members: %w", err)
	}
	defer rows.Close()

	var members []*WorkspaceMember
	for rows.Next() {
		m := &WorkspaceMember{}
		var grantedBy sql.NullString
		if err := rows.Scan(&m.UserID, &m.WorkspaceID, &m.Role, &m.CreatedAt, &grantedBy); err != nil {
			return nil, fmt.Errorf("store: list workspace members: scan: %w", err)
		}
		if grantedBy.Valid {
			m.GrantedBy = grantedBy.String
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list workspace members: %w", err)
	}
	return members, nil
}

// DeleteWorkspaceMember removes a membership by the composite key
// (user_id, workspace_id).
func (s *sqliteStore) DeleteWorkspaceMember(userID, workspaceID string) error {
	result, err := s.db.Exec(
		`DELETE FROM workspace_members WHERE user_id = ? AND workspace_id = ?`,
		userID, workspaceID,
	)
	if err != nil {
		return fmt.Errorf("store: delete workspace member: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete workspace member: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// UpsertWorkspaceMember creates or updates a workspace membership.
// If a membership for the (user_id, workspace_id) pair already exists,
// the role and granted_by fields are updated.
func (s *sqliteStore) UpsertWorkspaceMember(m *WorkspaceMember) (*WorkspaceMember, error) {
	now := nowRFC3339()

	_, err := s.db.Exec(
		`INSERT INTO workspace_members (user_id, workspace_id, role, created_at, granted_by)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, workspace_id) DO UPDATE SET
		   role = excluded.role,
		   granted_by = excluded.granted_by`,
		m.UserID, m.WorkspaceID, m.Role, now, m.GrantedBy,
	)
	if err != nil {
		if isConstraintError(err) {
			return nil, fmt.Errorf("store: upsert workspace member: %w: %v", ErrConstraintViolation, err)
		}
		return nil, fmt.Errorf("store: upsert workspace member: %w", err)
	}

	// Read back the current state (may be the insert or the update).
	return s.GetWorkspaceMember(m.UserID, m.WorkspaceID)
}
