package workspace

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// sqliteErrorCode is an interface that matches SQLite driver errors exposing
// an integer error code. This avoids importing the concrete driver error type.
type sqliteErrorCode interface {
	error
	Code() int
}

// isUniqueConstraintError checks whether err is a SQLite unique constraint
// violation (SQLITE_CONSTRAINT_UNIQUE = 2067 or SQLITE_CONSTRAINT_PRIMARYKEY = 1555).
func isUniqueConstraintError(err error) bool {
	var sqlErr sqliteErrorCode
	if errors.As(err, &sqlErr) {
		code := sqlErr.Code()
		return code == 2067 || code == 1555 // UNIQUE or PRIMARYKEY constraint
	}
	return false
}

// Workspace represents a workspace record in the workspaces table.
type Workspace struct {
	Slug        string
	GitURL      string
	Branch      *string // nullable
	OwnerID     string
	OrgID       *string // nullable
	Status      string
	DisplayName string
	Description string
	CloneStatus string  // pending, cloning, ready, failed, archived
	HeadSHA     *string // nullable: 40-char hex SHA when clone_status is ready or archived
	CloneError  *string // nullable: error message when clone_status is failed
	CreatedAt   string
	UpdatedAt   string
}

// insertWorkspace inserts a new workspace record into the workspaces table.
// It sets created_at and updated_at to the current time in RFC 3339 format.
// If CloneStatus is empty, it defaults to "pending".
func insertWorkspace(db *sql.DB, ws *Workspace) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ws.CreatedAt = now
	ws.UpdatedAt = now
	if ws.CloneStatus == "" {
		ws.CloneStatus = "pending"
	}

	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, branch, owner_id, org_id, status, display_name, description, clone_status, head_sha, clone_error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ws.Slug, ws.GitURL, ws.Branch, ws.OwnerID, ws.OrgID, ws.Status, ws.DisplayName, ws.Description, ws.CloneStatus, ws.HeadSHA, ws.CloneError, ws.CreatedAt, ws.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert workspace %q: %w", ws.Slug, err)
	}
	return nil
}

// getWorkspaceBySlug retrieves a single workspace by slug.
// Returns nil, nil if the workspace is not found.
func getWorkspaceBySlug(db *sql.DB, slug string) (*Workspace, error) {
	ws := &Workspace{}
	err := db.QueryRow(
		`SELECT slug, git_url, branch, owner_id, org_id, status, display_name, description, clone_status, head_sha, clone_error, created_at, updated_at
		 FROM workspaces WHERE slug = ?`,
		slug,
	).Scan(&ws.Slug, &ws.GitURL, &ws.Branch, &ws.OwnerID, &ws.OrgID, &ws.Status, &ws.DisplayName, &ws.Description, &ws.CloneStatus, &ws.HeadSHA, &ws.CloneError, &ws.CreatedAt, &ws.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get workspace %q: %w", slug, err)
	}
	return ws, nil
}

// listWorkspacesByOwner retrieves all workspaces owned by the given user.
// If includeArchived is false, only active workspaces are returned.
// Results are ordered by created_at descending.
func listWorkspacesByOwner(db *sql.DB, ownerID string, includeArchived bool) ([]*Workspace, error) {
	query := `SELECT slug, git_url, branch, owner_id, org_id, status, display_name, description, clone_status, head_sha, clone_error, created_at, updated_at
		 FROM workspaces WHERE owner_id = ?`
	args := []any{ownerID}

	if !includeArchived {
		query += ` AND status != 'archived'`
	}
	query += ` ORDER BY created_at DESC`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list workspaces for owner %q: %w", ownerID, err)
	}
	defer rows.Close()

	return scanWorkspaces(rows)
}

// listAllWorkspaces retrieves all workspaces (admin use).
// If includeArchived is false, only active workspaces are returned.
// Results are ordered by created_at descending.
func listAllWorkspaces(db *sql.DB, includeArchived bool) ([]*Workspace, error) {
	query := `SELECT slug, git_url, branch, owner_id, org_id, status, display_name, description, clone_status, head_sha, clone_error, created_at, updated_at
		 FROM workspaces`

	if !includeArchived {
		query += ` WHERE status != 'archived'`
	}
	query += ` ORDER BY created_at DESC`

	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("list all workspaces: %w", err)
	}
	defer rows.Close()

	return scanWorkspaces(rows)
}

// updateWorkspaceStatus updates the status of a workspace and refreshes updated_at.
// Returns the updated workspace, or nil if no workspace with the given slug exists.
func updateWorkspaceStatus(db *sql.DB, slug, status string) (*Workspace, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := db.Exec(
		`UPDATE workspaces SET status = ?, updated_at = ? WHERE slug = ?`,
		status, now, slug,
	)
	if err != nil {
		return nil, fmt.Errorf("update workspace %q status: %w", slug, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("update workspace %q: rows affected: %w", slug, err)
	}
	if affected == 0 {
		return nil, nil
	}
	return getWorkspaceBySlug(db, slug)
}

// updateWorkspaceRow updates all mutable fields of a workspace and refreshes
// updated_at. This is used by the PATCH handler after loading the current state
// and applying only the provided field changes to the in-memory struct.
// Returns the updated workspace, or an error if the update fails.
func updateWorkspaceRow(db *sql.DB, slug, displayName, description string, orgID *string) (*Workspace, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`UPDATE workspaces SET display_name = ?, description = ?, org_id = ?, updated_at = ? WHERE slug = ?`,
		displayName, description, orgID, now, slug,
	)
	if err != nil {
		return nil, fmt.Errorf("update workspace %q: %w", slug, err)
	}
	return getWorkspaceBySlug(db, slug)
}

// archiveWorkspaceDB sets both status and clone_status to "archived",
// records the head_sha (may be nil for workspaces archived from pending/failed),
// clears clone_error, and refreshes updated_at. Returns the updated workspace.
func archiveWorkspaceDB(db *sql.DB, slug string, headSHA *string) (*Workspace, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`UPDATE workspaces SET status = 'archived', clone_status = 'archived', head_sha = ?, clone_error = NULL, updated_at = ? WHERE slug = ?`,
		headSHA, now, slug,
	)
	if err != nil {
		return nil, fmt.Errorf("archive workspace %q: %w", slug, err)
	}
	return getWorkspaceBySlug(db, slug)
}

// reactivateWorkspaceDB sets status to "active", clone_status to "pending",
// clears clone_error, and refreshes updated_at. Returns the updated workspace.
// Used by the reactivate handler to atomically reset all fields before
// enqueuing a reclone job.
func reactivateWorkspaceDB(db *sql.DB, slug string) (*Workspace, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`UPDATE workspaces SET status = 'active', clone_status = 'pending', clone_error = NULL, updated_at = ? WHERE slug = ?`,
		now, slug,
	)
	if err != nil {
		return nil, fmt.Errorf("reactivate workspace %q: %w", slug, err)
	}
	return getWorkspaceBySlug(db, slug)
}

// deleteWorkspace physically removes a workspace row and cascade-deletes all
// associated secrets and variables within a single database transaction
// (07-REQ-17.1, 07-REQ-17.2).
func deleteWorkspace(db *sql.DB, slug string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete workspace %q: begin tx: %w", slug, err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(`DELETE FROM workspaces WHERE slug = ?`, slug)
	if err != nil {
		return fmt.Errorf("delete workspace %q: %w", slug, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete workspace %q: rows affected: %w", slug, err)
	}
	if affected == 0 {
		return fmt.Errorf("workspace %q not found", slug)
	}

	// Cascade-delete associated secrets and variables (07-REQ-17.1).
	if _, err := tx.Exec(`DELETE FROM secrets WHERE owner_type = 'workspace' AND owner_id = ?`, slug); err != nil {
		return fmt.Errorf("delete workspace %q secrets: %w", slug, err)
	}
	if _, err := tx.Exec(`DELETE FROM variables WHERE owner_type = 'workspace' AND owner_id = ?`, slug); err != nil {
		return fmt.Errorf("delete workspace %q variables: %w", slug, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete workspace %q: commit: %w", slug, err)
	}
	return nil
}

// scanWorkspaces scans rows into a slice of Workspace pointers.
func scanWorkspaces(rows *sql.Rows) ([]*Workspace, error) {
	var workspaces []*Workspace
	for rows.Next() {
		ws := &Workspace{}
		if err := rows.Scan(&ws.Slug, &ws.GitURL, &ws.Branch, &ws.OwnerID, &ws.OrgID, &ws.Status, &ws.DisplayName, &ws.Description, &ws.CloneStatus, &ws.HeadSHA, &ws.CloneError, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan workspace row: %w", err)
		}
		workspaces = append(workspaces, ws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace rows: %w", err)
	}
	return workspaces, nil
}
