package store

import "errors"

// ErrDuplicateSlug is returned when a workspace creation attempt uses a slug
// that already exists in the workspaces table. This is a sentinel error
// distinct from the generic ErrConstraintViolation, allowing callers (e.g.
// the REST handler) to map it specifically to HTTP 409.
//
// Spec: 07-REQ-2.2
var ErrDuplicateSlug = errors.New("workspace slug already exists")

// CreateWorkspaceParams holds the input parameters for creating a new workspace
// entity per the spec 07 schema (git_url, branch, owner_id, team_id).
//
// Stub: real implementation in task group 8.
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
// Stub: task group 8 will replace the existing Workspace struct with this
// schema. The V2 suffix is temporary to avoid conflicting with the existing
// Workspace type during the transition.
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
// Stub: real implementation in task group 8.
func (s *sqliteStore) CreateWorkspaceV2(params CreateWorkspaceParams) (*WorkspaceV2, error) {
	return nil, errors.New("not implemented: CreateWorkspaceV2 — stub for task group 8")
}
