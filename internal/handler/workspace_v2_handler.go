// workspace_v2_handler.go — workspace handler for the spec 07 workspace entity
// schema (git_url, branch, owner_id, team_id).
//
// The V2 suffix distinguishes this from the existing WorkspaceHandler that
// operates on the legacy workspace schema (name, url, created_by). Once
// spec 06 (team_rename) completes, the legacy handler will be renamed and
// this handler will become the primary workspace handler.
package handler

import (
	"net/http"

	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// WorkspaceV2Handler handles workspace management endpoints per the spec 07
// workspace entity schema (git_url, branch, owner_id, team_id).
type WorkspaceV2Handler struct {
	store store.Store
}

// NewWorkspaceV2Handler creates a new WorkspaceV2Handler.
func NewWorkspaceV2Handler(s store.Store) *WorkspaceV2Handler {
	return &WorkspaceV2Handler{store: s}
}

// CreateWorkspaceV2 handles POST /api/v1/workspaces per spec 07.
//
// Validates slug and git_url format, checks team existence and membership
// when team_id is provided, creates a workspace with owner_id from the
// authenticated user context, and responds with HTTP 201.
//
// Stub: returns 501 Not Implemented until task group 10.
func (h *WorkspaceV2Handler) CreateWorkspaceV2(c echo.Context) error {
	return NewErrorResponse(c, http.StatusNotImplemented, "not implemented")
}
