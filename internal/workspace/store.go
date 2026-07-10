package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors for the workspace and token repositories.
var (
	// ErrSlugConflict is returned when a workspace slug already exists.
	ErrSlugConflict = errors.New("workspace slug already exists")

	// ErrNotFound is returned when the requested workspace or token is not found.
	ErrNotFound = errors.New("not found")

	// ErrTeamNotFound is returned when the team_id does not reference an active team.
	ErrTeamNotFound = errors.New("team not found or not active")

	// ErrTokenIDConflict is returned when a generated token_id collides with
	// an existing record in workspace_tokens.
	ErrTokenIDConflict = errors.New("token_id already exists")
)

// Store provides data access methods for workspaces and workspace tokens.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store backed by the given database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// formatTime formats a time.Time value as RFC3339 in UTC.
func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// InsertWorkspace inserts a new workspace record into the workspaces table.
// The owner_user_id is stored in the DB column 'owner_id', and updated_at
// is set equal to created_at in the INSERT statement.
//
// Returns ErrSlugConflict if the slug already exists (UNIQUE constraint violation).
// Propagates other database errors as-is.
func (s *Store) InsertWorkspace(ctx context.Context, w Workspace) (Workspace, error) {
	id := uuid.New().String()
	now := formatTime(time.Now().UTC())

	// Note: DB column is 'owner_id' but the API field is 'owner_user_id'.
	// The 'status' column is required by the schema but not exposed in the API.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspaces (id, slug, git_url, branch, owner_id, team_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		id, w.Slug, w.GitURL, w.Branch, w.OwnerUserID, w.TeamID, now, now,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return Workspace{}, ErrSlugConflict
		}
		return Workspace{}, fmt.Errorf("insert workspace: %w", err)
	}

	return Workspace{
		ID:          id,
		Slug:        w.Slug,
		GitURL:      w.GitURL,
		Branch:      w.Branch,
		TeamID:      w.TeamID,
		OwnerUserID: w.OwnerUserID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// GetWorkspaceBySlug retrieves a workspace by its slug.
// Returns ErrNotFound if no workspace with the given slug exists.
//
// Note: The DB column 'owner_id' is aliased to 'owner_user_id' for the
// Workspace struct mapping.
func (s *Store) GetWorkspaceBySlug(ctx context.Context, slug string) (Workspace, error) {
	var ws Workspace
	err := s.db.QueryRowContext(ctx,
		`SELECT id, slug, git_url, branch, team_id, owner_id, created_at, updated_at
		 FROM workspaces
		 WHERE slug = ?`,
		slug,
	).Scan(&ws.ID, &ws.Slug, &ws.GitURL, &ws.Branch, &ws.TeamID, &ws.OwnerUserID, &ws.CreatedAt, &ws.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Workspace{}, ErrNotFound
		}
		return Workspace{}, fmt.Errorf("get workspace by slug: %w", err)
	}
	return ws, nil
}

// ValidateTeamExists checks that the given team_id exists in the teams table
// and has active status. Returns ErrTeamNotFound if the team does not exist
// or is not active.
func (s *Store) ValidateTeamExists(ctx context.Context, teamID string) error {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM teams WHERE id = ? AND status = 'active'`,
		teamID,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTeamNotFound
		}
		return fmt.Errorf("validate team exists: %w", err)
	}
	return nil
}

// ListWorkspaces retrieves workspaces ordered by created_at ASC, id ASC.
// If ownerUserID is non-nil, only workspaces owned by that user are returned.
// If ownerUserID is nil (admin), all workspaces are returned.
// Returns an empty slice (not nil) when no results are found.
func (s *Store) ListWorkspaces(ctx context.Context, ownerUserID *string) ([]Workspace, error) {
	var rows *sql.Rows
	var err error

	if ownerUserID != nil {
		// User: return only their workspaces.
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, slug, git_url, branch, team_id, owner_id, created_at, updated_at
			 FROM workspaces
			 WHERE owner_id = ?
			 ORDER BY created_at ASC, id ASC`,
			*ownerUserID,
		)
	} else {
		// Admin: return all workspaces.
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, slug, git_url, branch, team_id, owner_id, created_at, updated_at
			 FROM workspaces
			 ORDER BY created_at ASC, id ASC`,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()

	// Always return an empty slice, never nil.
	workspaces := make([]Workspace, 0)
	for rows.Next() {
		var ws Workspace
		if err := rows.Scan(&ws.ID, &ws.Slug, &ws.GitURL, &ws.Branch, &ws.TeamID, &ws.OwnerUserID, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan workspace: %w", err)
		}
		workspaces = append(workspaces, ws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspaces: %w", err)
	}

	return workspaces, nil
}

// WorkspaceToken represents a workspace token record in the database.
// This is an internal type used by the Store layer; the API response types
// (TokenCreateResponse, TokenListItem) are defined in types.go.
type WorkspaceToken struct {
	ID          string
	TokenID     string
	SecretHash  string
	WorkspaceID string
	UserID      string // DB column 'user_id' — the creating user
	Label       *string
	ExpiresAt   *string
	CreatedAt   string
	RevokedAt   *string
}

// InsertWorkspaceToken inserts a workspace token record into workspace_tokens.
// Returns ErrTokenIDConflict if the token_id already exists (UNIQUE constraint).
// Propagates other database errors as-is.
//
// Note: The DB column is 'user_id' (not 'creator_user_id' as referenced in
// some spec documents). See reviewer finding for 04-REQ-9.1.
func (s *Store) InsertWorkspaceToken(ctx context.Context, t WorkspaceToken) (WorkspaceToken, error) {
	id := uuid.New().String()
	now := formatTime(time.Now().UTC())

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workspace_tokens (id, token_id, secret_hash, workspace_id, user_id, label, expires_at, created_at, revoked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		id, t.TokenID, t.SecretHash, t.WorkspaceID, t.UserID,
		t.Label, t.ExpiresAt, now,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return WorkspaceToken{}, ErrTokenIDConflict
		}
		return WorkspaceToken{}, fmt.Errorf("insert workspace token: %w", err)
	}

	t.ID = id
	t.CreatedAt = now
	t.RevokedAt = nil
	return t, nil
}

// ListWorkspaceTokens retrieves all tokens for a workspace, including expired
// and revoked tokens. Results are ordered by created_at ASC, id ASC.
// The secret_hash is never included in the result.
// Returns an empty slice (not nil) when no tokens exist.
func (s *Store) ListWorkspaceTokens(ctx context.Context, workspaceID string) ([]TokenListItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT token_id, label, created_at, expires_at, revoked_at
		 FROM workspace_tokens
		 WHERE workspace_id = ?
		 ORDER BY created_at ASC, id ASC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("list workspace tokens: %w", err)
	}
	defer rows.Close()

	// Always return an empty slice, never nil.
	tokens := make([]TokenListItem, 0)
	for rows.Next() {
		var t TokenListItem
		if err := rows.Scan(&t.TokenID, &t.Label, &t.CreatedAt, &t.ExpiresAt, &t.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan workspace token: %w", err)
		}
		tokens = append(tokens, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace tokens: %w", err)
	}

	return tokens, nil
}

// RevokeWorkspaceToken revokes a workspace token by setting revoked_at to the
// current timestamp. The token must belong to the specified workspace.
//
// Idempotent: if the token is already revoked (revoked_at is already set),
// returns nil (no error). The original revoked_at timestamp is preserved.
//
// Returns ErrNotFound if the token_id does not exist or belongs to a different
// workspace (information hiding: cross-workspace token IDs are treated as
// not found).
func (s *Store) RevokeWorkspaceToken(ctx context.Context, workspaceID, tokenID string) error {
	now := formatTime(time.Now().UTC())

	// Try to revoke the token (only updates if not already revoked).
	result, err := s.db.ExecContext(ctx,
		`UPDATE workspace_tokens
		 SET revoked_at = ?
		 WHERE token_id = ? AND workspace_id = ? AND revoked_at IS NULL`,
		now, tokenID, workspaceID,
	)
	if err != nil {
		return fmt.Errorf("revoke workspace token: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if rowsAffected == 0 {
		// Either the token doesn't exist, belongs to a different workspace,
		// or is already revoked. Check which case.
		var exists int
		err := s.db.QueryRowContext(ctx,
			`SELECT 1 FROM workspace_tokens WHERE token_id = ? AND workspace_id = ?`,
			tokenID, workspaceID,
		).Scan(&exists)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("check token existence: %w", err)
		}
		// Token exists and belongs to this workspace but 0 rows affected →
		// already revoked. This is the idempotent case: return nil.
		return nil
	}

	return nil
}

// isUniqueConstraintError checks if a database error is a UNIQUE constraint
// violation, which occurs for slug collisions and token_id collisions.
func isUniqueConstraintError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "unique constraint")
}
