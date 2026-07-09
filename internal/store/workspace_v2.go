package store

import (
	"errors"
	"fmt"
	"strings"
)

// ErrDuplicateSlug is returned when a workspace creation attempt uses a slug
// that already exists in the workspaces table. This is a sentinel error
// distinct from the generic ErrConstraintViolation, allowing callers (e.g.
// the REST handler) to map it specifically to HTTP 409.
//
// Spec: 07-REQ-2.2
var ErrDuplicateSlug = errors.New("workspace slug already exists")

// CreateWorkspaceParams holds the input parameters for creating a new workspace
// entity per the spec 07 schema (git_url, branch, owner_id, team_id).
type CreateWorkspaceParams struct {
	Slug    string
	GitURL  string
	Branch  *string // nil means use repository default branch
	OwnerID string
	TeamID  *string // nil means personal workspace (no team)
}

// WorkspaceV2 represents a workspace record per the spec 07 schema.
// This is the new workspace entity that maps to a git repository context.
//
// The V2 suffix is temporary to avoid conflicting with the existing
// Workspace type during the transition from spec 01 to spec 07.
type WorkspaceV2 struct {
	ID        string  `json:"id"`
	Slug      string  `json:"slug"`
	GitURL    string  `json:"git_url"`
	Branch    *string `json:"branch"`
	OwnerID   string  `json:"owner_id"`
	TeamID    *string `json:"team_id"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
}

// CreateWorkspaceV2 creates a new workspace record per the spec 07 schema.
// It generates a UUID for the ID, sets status to 'active' and created_at to
// the current UTC time, inserts the row, and returns the populated struct.
//
// On a UNIQUE constraint violation (duplicate slug), it returns
// ErrDuplicateSlug. On foreign key violations (e.g. nonexistent team_id or
// owner_id), it returns a wrapped error preserving the SQLite FK message.
// On any other database error (locked, unavailable), it returns the wrapped
// error immediately without retrying.
//
// This function never terminates the process directly — all errors are
// returned as Go error values per 07-REQ-2.3.
func (s *sqliteStore) CreateWorkspaceV2(params CreateWorkspaceParams) (*WorkspaceV2, error) {
	ws := &WorkspaceV2{
		ID:        newID(),
		Slug:      params.Slug,
		GitURL:    params.GitURL,
		Branch:    params.Branch,
		OwnerID:   params.OwnerID,
		TeamID:    params.TeamID,
		Status:    "active",
		CreatedAt: nowRFC3339(),
	}

	_, err := s.db.Exec(
		`INSERT INTO workspaces (id, slug, git_url, branch, owner_id, team_id, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ws.ID, ws.Slug, ws.GitURL, ws.Branch, ws.OwnerID, ws.TeamID, ws.Status, ws.CreatedAt,
	)
	if err != nil {
		msg := strings.ToLower(err.Error())
		// Distinguish UNIQUE constraint violations (duplicate slug) from
		// foreign key violations and other database errors.
		// The workspaces table has only one UNIQUE column besides the PK
		// (slug), so any UNIQUE constraint failure is a duplicate slug.
		if strings.Contains(msg, "unique constraint failed") {
			return nil, fmt.Errorf("store: create workspace v2: %w", ErrDuplicateSlug)
		}
		return nil, fmt.Errorf("store: create workspace v2: %w", err)
	}

	return ws, nil
}
