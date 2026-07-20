package workspace

import (
	"database/sql"
	"time"
)

// Workspace represents a workspace record in the workspaces table.
type Workspace struct {
	Slug      string
	GitURL    string
	Branch    *string // nullable
	OwnerID   string
	OrgID     *string // nullable
	Status    string
	CreatedAt string
	UpdatedAt string
}

// insertWorkspace inserts a new workspace record into the workspaces table.
// It sets created_at and updated_at to the current time in RFC 3339 format.
func insertWorkspace(db *sql.DB, ws *Workspace) error {
	now := time.Now().UTC().Format(time.RFC3339)
	ws.CreatedAt = now
	ws.UpdatedAt = now
	panic("not implemented")
}

// deleteWorkspace physically removes a workspace row from the workspaces table.
func deleteWorkspace(db *sql.DB, slug string) error {
	panic("not implemented")
}
