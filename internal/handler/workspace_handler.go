// Package handler provides HTTP handlers for af-hub REST API endpoints.
package handler

import (
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// WorkspaceHandler handles workspace management HTTP endpoints.
type WorkspaceHandler struct {
	store store.Store
}

// NewWorkspaceHandler creates a new WorkspaceHandler.
func NewWorkspaceHandler(s store.Store) *WorkspaceHandler {
	panic("not implemented")
}

// CreateWorkspace handles POST /api/v1/workspaces.
// Validates slug and URL format, creates workspace, returns HTTP 201.
func (h *WorkspaceHandler) CreateWorkspace(c echo.Context) error {
	panic("not implemented")
}

// ListWorkspaces handles GET /api/v1/workspaces.
// Returns workspaces; excludes archived by default unless
// include_archived=true query parameter is present.
func (h *WorkspaceHandler) ListWorkspaces(c echo.Context) error {
	panic("not implemented")
}

// ArchiveWorkspace handles POST /api/v1/workspaces/:id/archive.
// Marks an active workspace as archived.
func (h *WorkspaceHandler) ArchiveWorkspace(c echo.Context) error {
	panic("not implemented")
}

// ReactivateWorkspace handles POST /api/v1/workspaces/:id/reactivate.
// Marks an archived workspace as active.
func (h *WorkspaceHandler) ReactivateWorkspace(c echo.Context) error {
	panic("not implemented")
}

// DeleteWorkspace handles DELETE /api/v1/workspaces/:id.
// Deletes an archived workspace with cascade deletion of memberships
// and API keys. The entire operation is transactional.
func (h *WorkspaceHandler) DeleteWorkspace(c echo.Context) error {
	panic("not implemented")
}

// AddOrUpdateMember handles POST /api/v1/workspaces/:id/members.
// Upserts a workspace membership with the given user_id and role.
func (h *WorkspaceHandler) AddOrUpdateMember(c echo.Context) error {
	panic("not implemented")
}

// ListMembers handles GET /api/v1/workspaces/:id/members.
// Returns all member records for the workspace.
func (h *WorkspaceHandler) ListMembers(c echo.Context) error {
	panic("not implemented")
}
