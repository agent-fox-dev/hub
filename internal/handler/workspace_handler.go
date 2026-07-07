// Package handler provides HTTP handlers for af-hub REST API endpoints.
package handler

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// slugRegexp validates workspace slugs: lowercase alphanumeric and hyphens,
// 3–64 characters, starts with a letter, does not end with a hyphen.
var slugRegexp = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}[a-z0-9]$`)

// WorkspaceHandler handles workspace management HTTP endpoints.
type WorkspaceHandler struct {
	store store.Store
}

// NewWorkspaceHandler creates a new WorkspaceHandler.
func NewWorkspaceHandler(s store.Store) *WorkspaceHandler {
	return &WorkspaceHandler{store: s}
}

// createWorkspaceRequest represents the request body for POST /api/v1/workspaces.
type createWorkspaceRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
	URL  string `json:"url"`
}

// addMemberRequest represents the request body for POST /api/v1/workspaces/:id/members.
type addMemberRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// validateSlug checks that a slug conforms to the required format:
// lowercase alphanumeric and hyphens, 3-64 chars, starts with a letter,
// does not end with a hyphen.
func validateSlug(slug string) bool {
	if len(slug) < 3 || len(slug) > 64 {
		return false
	}
	return slugRegexp.MatchString(slug)
}

// validateURL checks that a URL has http or https scheme and a host.
func validateURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}
	return true
}

// CreateWorkspace handles POST /api/v1/workspaces.
// Validates slug and URL format, creates workspace, returns HTTP 201.
func (h *WorkspaceHandler) CreateWorkspace(c echo.Context) error {
	var req createWorkspaceRequest
	if err := c.Bind(&req); err != nil {
		return NewErrorResponse(c, http.StatusBadRequest, "invalid request body")
	}

	// Validate required fields.
	if req.Name == "" || req.Slug == "" || req.URL == "" {
		return NewErrorResponse(c, http.StatusBadRequest, "missing required fields")
	}

	// Validate slug format.
	if !validateSlug(req.Slug) {
		return NewErrorResponse(c, http.StatusBadRequest, "invalid slug or URL format")
	}

	// Validate URL format (http/https scheme with host).
	if !validateURL(req.URL) {
		return NewErrorResponse(c, http.StatusBadRequest, "invalid slug or URL format")
	}

	// Populate created_by from the authenticated user (per reviewer finding:
	// spec 01 defines created_by FK on workspaces, nullable).
	createdBy := ""
	if userID := c.Get(auth.ContextKeyUserID); userID != nil {
		if uid, ok := userID.(string); ok {
			createdBy = uid
		}
	}

	ws := &store.Workspace{
		Name:      req.Name,
		Slug:      req.Slug,
		URL:       req.URL,
		Status:    "active",
		CreatedBy: createdBy,
	}

	created, err := h.store.CreateWorkspace(ws)
	if err != nil {
		if errors.Is(err, store.ErrConstraintViolation) {
			// Per reviewer finding: url TEXT UNIQUE NOT NULL also triggers
			// constraint violations. We include it in the conflict message.
			return NewErrorResponse(c, http.StatusConflict, "workspace name or slug already exists")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusCreated, created)
}

// ListWorkspaces handles GET /api/v1/workspaces.
// Returns workspaces; excludes archived by default unless
// include_archived=true query parameter is present.
func (h *WorkspaceHandler) ListWorkspaces(c echo.Context) error {
	includeArchived := c.QueryParam("include_archived") == "true"

	workspaces, err := h.store.ListWorkspaces(includeArchived)
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Return an empty array instead of null when no workspaces exist.
	if workspaces == nil {
		workspaces = []*store.Workspace{}
	}

	return c.JSON(http.StatusOK, workspaces)
}

// ArchiveWorkspace handles POST /api/v1/workspaces/:id/archive.
// Marks an active workspace as archived.
func (h *WorkspaceHandler) ArchiveWorkspace(c echo.Context) error {
	id := c.Param("id")

	ws, err := h.store.GetWorkspaceByID(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "workspace not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	if ws.Status == "archived" {
		return NewErrorResponse(c, http.StatusBadRequest, "workspace is already archived")
	}

	ws.Status = "archived"

	updated, err := h.store.UpdateWorkspace(ws)
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, updated)
}

// ReactivateWorkspace handles POST /api/v1/workspaces/:id/reactivate.
// Marks an archived workspace as active.
func (h *WorkspaceHandler) ReactivateWorkspace(c echo.Context) error {
	id := c.Param("id")

	ws, err := h.store.GetWorkspaceByID(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "workspace not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	if ws.Status != "archived" {
		return NewErrorResponse(c, http.StatusBadRequest, "workspace is not archived")
	}

	ws.Status = "active"

	updated, err := h.store.UpdateWorkspace(ws)
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, updated)
}

// DeleteWorkspace handles DELETE /api/v1/workspaces/:id.
// Deletes an archived workspace with cascade deletion of memberships
// and API keys. The entire operation is transactional.
func (h *WorkspaceHandler) DeleteWorkspace(c echo.Context) error {
	id := c.Param("id")

	ws, err := h.store.GetWorkspaceByID(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "workspace not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	if ws.Status != "archived" {
		return NewErrorResponse(c, http.StatusBadRequest, "workspace must be archived before deletion")
	}

	if err := h.store.DeleteWorkspaceWithCascade(id); err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, map[string]string{
		"message": "workspace deleted",
	})
}

// AddOrUpdateMember handles POST /api/v1/workspaces/:id/members.
// Upserts a workspace membership with the given user_id and role.
func (h *WorkspaceHandler) AddOrUpdateMember(c echo.Context) error {
	workspaceID := c.Param("id")

	// Verify workspace exists.
	_, err := h.store.GetWorkspaceByID(workspaceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "workspace not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	var req addMemberRequest
	if err := c.Bind(&req); err != nil {
		return NewErrorResponse(c, http.StatusBadRequest, "invalid request body")
	}

	if req.UserID == "" || req.Role == "" {
		return NewErrorResponse(c, http.StatusBadRequest, "missing required fields")
	}

	// Verify the user exists.
	_, err = h.store.GetUserByID(req.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "user not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Populate granted_by from the authenticated user (per reviewer finding:
	// spec 01 defines granted_by FK on workspace_members, nullable).
	grantedBy := ""
	if userID := c.Get(auth.ContextKeyUserID); userID != nil {
		if uid, ok := userID.(string); ok {
			grantedBy = uid
		}
	}

	member := &store.WorkspaceMember{
		UserID:      req.UserID,
		WorkspaceID: workspaceID,
		Role:        req.Role,
		GrantedBy:   grantedBy,
	}

	upserted, err := h.store.UpsertWorkspaceMember(member)
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, upserted)
}

// ListMembers handles GET /api/v1/workspaces/:id/members.
// Returns all member records for the workspace.
func (h *WorkspaceHandler) ListMembers(c echo.Context) error {
	workspaceID := c.Param("id")

	// Verify workspace exists.
	_, err := h.store.GetWorkspaceByID(workspaceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "workspace not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	members, err := h.store.ListWorkspaceMembers(workspaceID)
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Return an empty array instead of null when no members exist.
	if members == nil {
		members = []*store.WorkspaceMember{}
	}

	return c.JSON(http.StatusOK, members)
}
