package workspace

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/auth"
)

// errorDetail is the inner error object in the standard JSON error envelope.
type errorDetail struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// errorResponse is the standard JSON error envelope.
type errorResponse struct {
	Error errorDetail `json:"error"`
}

// writeError writes a JSON error envelope response.
func writeError(c echo.Context, code int, message string) error {
	return c.JSON(code, errorResponse{
		Error: errorDetail{
			Code:    code,
			Message: message,
		},
	})
}

// getAuthContext extracts the AuthContext from Echo's context, set by the
// auth middleware. Returns nil if not present.
func getAuthContext(c echo.Context) *auth.AuthContext {
	v := c.Get(string(auth.AuthContextKey))
	if v == nil {
		return nil
	}
	ac, ok := v.(auth.AuthContext)
	if !ok {
		return nil
	}
	return &ac
}

// RegisterRoutes registers all workspace and workspace token HTTP handlers
// on the given Echo route group. The group should already have the auth
// middleware applied.
//
// Routes registered:
//
//	POST   /workspaces              - Create workspace (user API key only)
//	GET    /workspaces              - List workspaces (user: own; admin: all)
//	GET    /workspaces/:slug        - Get workspace by slug
//	POST   /workspaces/:slug/tokens - Create workspace token
//	GET    /workspaces/:slug/tokens - List workspace tokens
//	DELETE /workspaces/:slug/tokens/:token_id - Revoke workspace token
func RegisterRoutes(g *echo.Group, db *sql.DB) {
	store := NewStore(db)
	h := &handler{store: store}

	g.POST("/workspaces", h.createWorkspace)
	g.GET("/workspaces", h.listWorkspaces)
	g.GET("/workspaces/:slug", h.getWorkspaceBySlug)
	g.POST("/workspaces/:slug/tokens", h.createWorkspaceToken)
	g.GET("/workspaces/:slug/tokens", h.listWorkspaceTokens)
	g.DELETE("/workspaces/:slug/tokens/:token_id", h.revokeWorkspaceToken)
}

// handler holds handler dependencies.
type handler struct {
	store *Store
}

// createWorkspace handles POST /api/v1/workspaces.
// Only user API key authentication is permitted (403 for admin, workspace token).
func (h *handler) createWorkspace(c echo.Context) error {
	ac := getAuthContext(c)
	if ac == nil {
		return writeError(c, http.StatusUnauthorized, "authentication required")
	}

	// Only user API keys may create workspaces.
	if ac.CredentialType == auth.CredentialTypeAdmin {
		return writeError(c, http.StatusForbidden, "admin tokens cannot create workspaces")
	}
	if ac.CredentialType == auth.CredentialTypeWorkspaceToken {
		return writeError(c, http.StatusForbidden, "workspace tokens cannot create workspaces")
	}

	// Parse request body. Use json.NewDecoder directly to work regardless of
	// Content-Type header and to silently ignore unknown fields (Echo default).
	var req CreateWorkspaceRequest
	decoder := json.NewDecoder(c.Request().Body)
	if err := decoder.Decode(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "invalid request body")
	}

	// Validate slug.
	if err := ValidateSlug(req.Slug); err != nil {
		return writeError(c, http.StatusBadRequest, err.Error())
	}

	// Validate git_url.
	if err := ValidateGitURL(req.GitURL); err != nil {
		return writeError(c, http.StatusBadRequest, err.Error())
	}

	// Validate branch.
	if err := ValidateBranch(req.Branch); err != nil {
		return writeError(c, http.StatusBadRequest, err.Error())
	}

	// Validate team_id if provided.
	if req.TeamID != nil {
		if err := h.store.ValidateTeamExists(c.Request().Context(), *req.TeamID); err != nil {
			if errors.Is(err, ErrTeamNotFound) {
				return writeError(c, http.StatusBadRequest, "team not found or not active")
			}
			return writeError(c, http.StatusInternalServerError, "internal server error")
		}
	}

	// Insert workspace with owner_user_id set to the authenticated user's ID.
	ws := Workspace{
		Slug:        req.Slug,
		GitURL:      req.GitURL,
		Branch:      req.Branch,
		TeamID:      req.TeamID,
		OwnerUserID: ac.UserID,
	}

	created, err := h.store.InsertWorkspace(c.Request().Context(), ws)
	if err != nil {
		if errors.Is(err, ErrSlugConflict) {
			return writeError(c, http.StatusConflict, "workspace slug already exists")
		}
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusCreated, created)
}

// listWorkspaces handles GET /api/v1/workspaces.
// Admin sees all workspaces; user API key sees only their own.
// Workspace tokens are rejected with 403.
func (h *handler) listWorkspaces(c echo.Context) error {
	ac := getAuthContext(c)
	if ac == nil {
		return writeError(c, http.StatusUnauthorized, "authentication required")
	}

	// Workspace tokens cannot list workspaces.
	if ac.CredentialType == auth.CredentialTypeWorkspaceToken {
		return writeError(c, http.StatusForbidden, "workspace tokens cannot list workspaces")
	}

	var ownerFilter *string
	if ac.CredentialType == auth.CredentialTypeAPIKey {
		ownerFilter = &ac.UserID
	}
	// Admin: ownerFilter remains nil → all workspaces.

	workspaces, err := h.store.ListWorkspaces(c.Request().Context(), ownerFilter)
	if err != nil {
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, workspaces)
}

// getWorkspaceBySlug handles GET /api/v1/workspaces/:slug.
// Returns the workspace if the caller is the owner, admin, or a workspace token
// scoped to that workspace. Returns 404 for all caller types if slug doesn't
// exist (information hiding). Returns 403 for non-owner users or mismatched
// workspace tokens.
func (h *handler) getWorkspaceBySlug(c echo.Context) error {
	ac := getAuthContext(c)
	if ac == nil {
		return writeError(c, http.StatusUnauthorized, "authentication required")
	}

	slug := c.Param("slug")

	// Look up workspace first. Return 404 for any caller if not found.
	ws, err := h.store.GetWorkspaceBySlug(c.Request().Context(), slug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return writeError(c, http.StatusNotFound, "workspace not found")
		}
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	// Check authorization based on caller type.
	switch ac.CredentialType {
	case auth.CredentialTypeAdmin:
		// Admin can access any workspace.
		return c.JSON(http.StatusOK, ws)

	case auth.CredentialTypeAPIKey:
		// User API key: must be the owner.
		if ws.OwnerUserID != ac.UserID {
			return writeError(c, http.StatusForbidden, "access denied")
		}
		return c.JSON(http.StatusOK, ws)

	case auth.CredentialTypeWorkspaceToken:
		// Workspace token: context workspace_id must match the resolved workspace's id.
		if ac.WorkspaceID != ws.ID {
			return writeError(c, http.StatusForbidden, "workspace token scope mismatch")
		}
		return c.JSON(http.StatusOK, ws)

	default:
		return writeError(c, http.StatusForbidden, "access denied")
	}
}

// createWorkspaceToken handles POST /api/v1/workspaces/:slug/tokens.
// Only workspace owner API key or admin token can create tokens.
func (h *handler) createWorkspaceToken(c echo.Context) error {
	ac := getAuthContext(c)
	if ac == nil {
		return writeError(c, http.StatusUnauthorized, "authentication required")
	}

	// Workspace tokens cannot create new tokens.
	if ac.CredentialType == auth.CredentialTypeWorkspaceToken {
		return writeError(c, http.StatusForbidden, "workspace tokens cannot create tokens")
	}

	slug := c.Param("slug")

	// Resolve workspace.
	ws, err := h.store.GetWorkspaceBySlug(c.Request().Context(), slug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return writeError(c, http.StatusNotFound, "workspace not found")
		}
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	// Non-owner user API key cannot create tokens.
	if ac.CredentialType == auth.CredentialTypeAPIKey && ws.OwnerUserID != ac.UserID {
		return writeError(c, http.StatusForbidden, "only workspace owner can create tokens")
	}

	// Parse request body.
	var req CreateTokenRequest
	decoder := json.NewDecoder(c.Request().Body)
	if err := decoder.Decode(&req); err != nil {
		// Allow empty body — default values will be used.
		// Only fail on malformed JSON.
		if c.Request().ContentLength > 0 {
			return writeError(c, http.StatusBadRequest, "invalid request body")
		}
	}

	// Normalize label.
	label, err := NormalizeLabel(req.Label)
	if err != nil {
		return writeError(c, http.StatusBadRequest, err.Error())
	}

	// Validate expires.
	expiresDays, err := ValidateExpires(req.Expires)
	if err != nil {
		return writeError(c, http.StatusBadRequest, err.Error())
	}

	// Retry loop for token_id collision (max 3 attempts).
	const maxRetries = 3
	for range maxRetries {
		tokenID, err := GenerateTokenID()
		if err != nil {
			return writeError(c, http.StatusInternalServerError, "internal server error")
		}

		secret, err := GenerateSecret()
		if err != nil {
			return writeError(c, http.StatusInternalServerError, "internal server error")
		}

		secretHash := HashSecret(secret)

		// Determine the creating user ID.
		creatorUserID := ac.UserID
		if ac.CredentialType == auth.CredentialTypeAdmin {
			// Admin creates tokens on behalf of the workspace owner.
			creatorUserID = ws.OwnerUserID
		}

		// Compute expires_at based on current time.
		// InsertWorkspaceToken sets created_at internally.
		expiresAt := ComputeExpiresAt(time.Now().UTC(), expiresDays)

		var expiresAtStr *string
		if expiresAt != nil {
			expiresAtStr = expiresAt
		}

		wt := WorkspaceToken{
			TokenID:     tokenID,
			SecretHash:  secretHash,
			WorkspaceID: ws.ID,
			UserID:      creatorUserID,
			Label:       label,
			ExpiresAt:   expiresAtStr,
		}

		inserted, err := h.store.InsertWorkspaceToken(c.Request().Context(), wt)
		if err != nil {
			if errors.Is(err, ErrTokenIDConflict) {
				continue // retry with new token_id
			}
			return writeError(c, http.StatusInternalServerError, "internal server error")
		}

		// Assemble the plaintext token string.
		plaintextToken := AssembleToken(tokenID, secret)

		resp := TokenCreateResponse{
			Token:     plaintextToken,
			TokenID:   tokenID,
			Label:     label,
			ExpiresAt: expiresAtStr,
			CreatedAt: inserted.CreatedAt,
		}

		return c.JSON(http.StatusCreated, resp)
	}

	// All retries exhausted — collision on all attempts.
	return writeError(c, http.StatusInternalServerError, "failed to generate unique token id")
}

// listWorkspaceTokens handles GET /api/v1/workspaces/:slug/tokens.
// Only workspace owner API key or admin token can list tokens.
func (h *handler) listWorkspaceTokens(c echo.Context) error {
	ac := getAuthContext(c)
	if ac == nil {
		return writeError(c, http.StatusUnauthorized, "authentication required")
	}

	// Workspace tokens cannot list tokens.
	if ac.CredentialType == auth.CredentialTypeWorkspaceToken {
		return writeError(c, http.StatusForbidden, "workspace tokens cannot list tokens")
	}

	slug := c.Param("slug")

	// Resolve workspace.
	ws, err := h.store.GetWorkspaceBySlug(c.Request().Context(), slug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return writeError(c, http.StatusNotFound, "workspace not found")
		}
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	// Non-owner user API key cannot list tokens.
	if ac.CredentialType == auth.CredentialTypeAPIKey && ws.OwnerUserID != ac.UserID {
		return writeError(c, http.StatusForbidden, "access denied")
	}

	tokens, err := h.store.ListWorkspaceTokens(c.Request().Context(), ws.ID)
	if err != nil {
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, tokens)
}

// revokeWorkspaceToken handles DELETE /api/v1/workspaces/:slug/tokens/:token_id.
// Only workspace owner API key or admin token can revoke tokens.
func (h *handler) revokeWorkspaceToken(c echo.Context) error {
	ac := getAuthContext(c)
	if ac == nil {
		return writeError(c, http.StatusUnauthorized, "authentication required")
	}

	// Workspace tokens cannot revoke tokens.
	if ac.CredentialType == auth.CredentialTypeWorkspaceToken {
		return writeError(c, http.StatusForbidden, "workspace tokens cannot revoke tokens")
	}

	slug := c.Param("slug")

	// Resolve workspace.
	ws, err := h.store.GetWorkspaceBySlug(c.Request().Context(), slug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return writeError(c, http.StatusNotFound, "workspace not found")
		}
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	// Non-owner user API key cannot revoke tokens.
	if ac.CredentialType == auth.CredentialTypeAPIKey && ws.OwnerUserID != ac.UserID {
		return writeError(c, http.StatusForbidden, "access denied")
	}

	tokenID := c.Param("token_id")

	err = h.store.RevokeWorkspaceToken(c.Request().Context(), ws.ID, tokenID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return writeError(c, http.StatusNotFound, "token not found")
		}
		return writeError(c, http.StatusInternalServerError, "internal server error")
	}

	return c.NoContent(http.StatusNoContent)
}

