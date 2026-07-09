// workspace_v2_handler.go — workspace handler for the spec 07 workspace entity
// schema (git_url, branch, owner_id, team_id).
//
// The V2 suffix distinguishes this from the existing WorkspaceHandler that
// operates on the legacy workspace schema (name, url, created_by). Once
// spec 06 (team_rename) completes, the legacy handler will be renamed and
// this handler will become the primary workspace handler.
package handler

import (
	"errors"
	"net/http"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/agent-fox/af-hub/internal/validator"
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

// createWorkspaceV2Request represents the JSON body for POST /api/v1/workspaces
// per the spec 07 schema. owner_id is intentionally excluded — it is always
// derived from the authenticated user's identity, never from the request body.
type createWorkspaceV2Request struct {
	Slug   string  `json:"slug"`
	GitURL string  `json:"git_url"`
	Branch *string `json:"branch"`
	TeamID *string `json:"team_id"`
}

// CreateWorkspaceV2 handles POST /api/v1/workspaces per spec 07.
//
// Authentication: rejects admin tokens (07-REQ-3.8); requires a real user
// authenticated via API key. owner_id is set from the authenticated user's
// identity, never from the request body (07-REQ-3.9).
//
// Validation order: admin check → body parse → required fields → slug
// format → git_url format → team existence → team membership → create.
func (h *WorkspaceV2Handler) CreateWorkspaceV2(c echo.Context) error {
	// ── 1. Admin token rejection (07-REQ-3.8) ──────────────────────
	// Admin tokens carry system-level privileges but have no real user
	// context. Workspace creation requires a real user as owner.
	if c.Get(auth.ContextKeyAuthMethod) == auth.AuthMethodAdmin {
		return NewErrorResponse(c, http.StatusForbidden,
			"workspace creation requires user authentication")
	}

	// ── 2. Extract owner_id from auth context (07-REQ-3.9) ─────────
	ownerID, ok := c.Get(auth.ContextKeyUserID).(string)
	if !ok || ownerID == "" {
		return NewErrorResponse(c, http.StatusForbidden,
			"workspace creation requires user authentication")
	}

	// ── 3. Parse request body ──────────────────────────────────────
	var req createWorkspaceV2Request
	if err := c.Bind(&req); err != nil {
		// Malformed JSON or wrong Content-Type (07-REQ-3.E1).
		return NewErrorResponse(c, http.StatusBadRequest, "missing required fields")
	}

	// ── 4. Required field check (07-REQ-3.2) ───────────────────────
	if req.Slug == "" || req.GitURL == "" {
		return NewErrorResponse(c, http.StatusBadRequest, "missing required fields")
	}

	// ── 5. Slug format validation (07-REQ-3.3) ─────────────────────
	if !validator.ValidateSlug(req.Slug) {
		return NewErrorResponse(c, http.StatusBadRequest, "invalid slug format")
	}

	// ── 6. git_url format validation (07-REQ-3.4) ──────────────────
	if !validator.ValidateGitURL(req.GitURL) {
		return NewErrorResponse(c, http.StatusBadRequest, "invalid git_url format")
	}

	// ── 7. Team existence and membership checks (07-REQ-3.6, 3.7) ──
	// Normalize: treat empty-string team_id the same as omitted.
	var teamIDParam *string
	if req.TeamID != nil && *req.TeamID != "" {
		teamIDParam = req.TeamID

		exists, err := h.store.TeamExists(*teamIDParam)
		if err != nil {
			return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
		}
		if !exists {
			return NewErrorResponse(c, http.StatusNotFound, "team not found")
		}

		isMember, err := h.store.IsTeamMember(ownerID, *teamIDParam)
		if err != nil {
			return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
		}
		if !isMember {
			return NewErrorResponse(c, http.StatusForbidden, "not a member of this team")
		}
	}

	// ── 8. Create workspace via store (07-REQ-3.1) ─────────────────
	ws, err := h.store.CreateWorkspaceV2(store.CreateWorkspaceParams{
		Slug:    req.Slug,
		GitURL:  req.GitURL,
		Branch:  req.Branch,
		OwnerID: ownerID,
		TeamID:  teamIDParam,
	})
	if err != nil {
		if errors.Is(err, store.ErrDuplicateSlug) {
			return NewErrorResponse(c, http.StatusConflict,
				"workspace slug already exists")
		}
		// Database lock, FK violation, or other failure (07-REQ-3.E2).
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusCreated, ws)
}
