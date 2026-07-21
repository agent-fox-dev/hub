package workspace

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// respondError writes a JSON error envelope {"error":{"code":N,"message":"..."}}
// and sets the HTTP status code. Delegates to apikit.WriteAPIError for a
// consistent error format across the platform.
func respondError(c echo.Context, code int, message string) error {
	return apikit.WriteAPIError(c, code, message)
}

// respondWorkspace writes a workspace JSON object as the response body.
func respondWorkspace(c echo.Context, code int, ws *Workspace) error {
	return c.JSON(code, workspaceResponse(ws))
}

// workspaceResponse converts a Workspace to a JSON-serializable map.
func workspaceResponse(ws *Workspace) map[string]any {
	return map[string]any{
		"slug":         ws.Slug,
		"git_url":      ws.GitURL,
		"branch":       ws.Branch,
		"owner_id":     ws.OwnerID,
		"org_id":       ws.OrgID,
		"status":       ws.Status,
		"display_name": ws.DisplayName,
		"description":  ws.Description,
		"created_at":   ws.CreatedAt,
		"updated_at":   ws.UpdatedAt,
	}
}

// createWorkspaceRequest represents the JSON body of a create workspace request.
type createWorkspaceRequest struct {
	Slug        string  `json:"slug"`
	GitURL      string  `json:"git_url"`
	Branch      *string `json:"branch"`
	OrgID       *string `json:"org_id"`
	DisplayName *string `json:"display_name"` // nullable: nil or empty → slug
	Description *string `json:"description"`  // nullable: nil → ""
}

// normalizeDisplayName returns the display name to store. If input is nil or
// empty, returns the slug value as the default.
func normalizeDisplayName(slug string, input *string) string {
	if input == nil || *input == "" {
		return slug
	}
	return *input
}

// normalizeDescription returns the description to store. If input is nil,
// returns empty string as the default.
func normalizeDescription(input *string) string {
	if input == nil {
		return ""
	}
	return *input
}

// lookupWorkspaceForAuth retrieves a workspace and enforces ownership-based access.
// Admin credentials can access any workspace; non-admin credentials can only access
// workspaces they own. Returns the workspace and nil error on success; on failure
// writes an error response and returns nil workspace with the response error.
func lookupWorkspaceForAuth(c echo.Context, db *sql.DB, slug string, auth *AuthInfo) (*Workspace, error) {
	ws, err := getWorkspaceBySlug(db, slug)
	if err != nil {
		return nil, respondError(c, http.StatusInternalServerError, "internal server error")
	}
	if ws == nil {
		return nil, respondError(c, http.StatusNotFound, "workspace not found")
	}

	// Admin can access any workspace.
	if auth.CredType == CredentialAdmin {
		return ws, nil
	}

	// Non-admin: must own the workspace.
	if ws.OwnerID != auth.UserID {
		// Return 404 instead of 403 to prevent slug enumeration.
		return nil, respondError(c, http.StatusNotFound, "workspace not found")
	}

	return ws, nil
}

// handleCreateWorkspace handles POST /api/v1/workspaces.
func handleCreateWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		// Admin tokens cannot create workspaces.
		if auth.CredType == CredentialAdmin {
			return respondError(c, http.StatusForbidden, "admin tokens cannot create workspaces; a real user is required as owner")
		}

		// PAT must have workspaces:create scope.
		if auth.CredType == CredentialPAT && !auth.hasPermission("workspaces:create") {
			return respondError(c, http.StatusForbidden, "PAT requires workspaces:create scope to create workspaces")
		}

		// Parse request body.
		var req createWorkspaceRequest
		if c.Request().Body == nil {
			return respondError(c, http.StatusBadRequest, "request body is required")
		}
		if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
			return respondError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		}

		// Validate slug.
		if err := validateSlug(req.Slug); err != nil {
			return respondError(c, http.StatusBadRequest, "invalid slug: "+err.Error())
		}

		// Validate git_url.
		if err := validateGitURL(req.GitURL); err != nil {
			return respondError(c, http.StatusBadRequest, "invalid git_url: "+err.Error())
		}

		// Validate branch if provided.
		if req.Branch != nil {
			if err := validateBranch(*req.Branch); err != nil {
				return respondError(c, http.StatusBadRequest, "invalid branch: "+err.Error())
			}
		}

		// Validate display_name length if provided.
		displayName := normalizeDisplayName(req.Slug, req.DisplayName)
		if len(displayName) > 128 {
			return respondError(c, http.StatusBadRequest, "display_name must not exceed 128 characters")
		}

		// Validate description length if provided.
		description := normalizeDescription(req.Description)
		if len(description) > 1024 {
			return respondError(c, http.StatusBadRequest, "description must not exceed 1024 characters")
		}

		// Validate org_id if provided.
		if req.OrgID != nil && *req.OrgID != "" {
			orgCode, orgMsg := checkOrgMembership(db, auth.UserID, *req.OrgID)
			if orgCode != 0 {
				return respondError(c, orgCode, orgMsg)
			}
		}

		// Check slug uniqueness.
		existing, err := getWorkspaceBySlug(db, req.Slug)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}
		if existing != nil {
			return respondError(c, http.StatusConflict, "workspace with slug '"+req.Slug+"' already exists")
		}

		// Create workspace.
		ws := &Workspace{
			Slug:        req.Slug,
			GitURL:      req.GitURL,
			Branch:      req.Branch,
			OwnerID:     auth.UserID,
			OrgID:       req.OrgID,
			Status:      "active",
			DisplayName: displayName,
			Description: description,
		}

		if err := insertWorkspace(db, ws); err != nil {
			// Handle unique constraint violation (concurrent duplicate slug insert).
			if isUniqueConstraintError(err) {
				return respondError(c, http.StatusConflict, "workspace with slug '"+req.Slug+"' already exists")
			}
			return respondError(c, http.StatusInternalServerError, "failed to create workspace")
		}

		return respondWorkspace(c, http.StatusCreated, ws)
	}
}

// checkOrgMembership verifies that the org exists and the user is a member.
// Returns (0, "") if the check passes, or (httpCode, message) on failure.
func checkOrgMembership(db *sql.DB, userID, orgID string) (int, string) {
	// Try to query the orgs table (apikit schema uses 'orgs').
	var exists int
	err := db.QueryRow("SELECT COUNT(*) FROM orgs WHERE id = ?", orgID).Scan(&exists)
	if err != nil {
		// Table might not exist or query failed — treat as org not found.
		return http.StatusBadRequest, "organization not found"
	}
	if exists == 0 {
		return http.StatusBadRequest, "organization not found"
	}

	// Check membership.
	var isMember int
	err = db.QueryRow("SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID).Scan(&isMember)
	if err != nil || isMember == 0 {
		return http.StatusForbidden, "user is not a member of the specified organization"
	}

	return 0, ""
}

// handleListWorkspaces handles GET /api/v1/workspaces.
func handleListWorkspaces(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		// PAT must have a scope that implies read access.
		// workspaces:read, workspaces:create, and workspaces:write imply read.
		// workspaces:delete does NOT imply read — return 404 (anti-enumeration).
		if auth.CredType == CredentialPAT && !auth.hasReadAccess() {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		includeArchived := c.QueryParam("include_archived") == "true"

		var workspaces []*Workspace

		if auth.CredType == CredentialAdmin {
			workspaces, err = listAllWorkspaces(db, includeArchived)
		} else {
			workspaces, err = listWorkspacesByOwner(db, auth.UserID, includeArchived)
		}
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}

		// Build response array.
		result := make([]map[string]any, 0, len(workspaces))
		for _, ws := range workspaces {
			result = append(result, workspaceResponse(ws))
		}

		return c.JSON(http.StatusOK, result)
	}
}

// handleGetWorkspace handles GET /api/v1/workspaces/:slug.
func handleGetWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		// PAT must have a scope that implies read access.
		// workspaces:read, workspaces:create, and workspaces:write imply read.
		// workspaces:delete does NOT imply read — return 404 (anti-enumeration).
		if auth.CredType == CredentialPAT && !auth.hasReadAccess() {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		slug := c.Param("slug")
		ws, _ := lookupWorkspaceForAuth(c, db, slug, auth)
		if ws == nil {
			return nil // Response already written by lookupWorkspaceForAuth.
		}

		return respondWorkspace(c, http.StatusOK, ws)
	}
}

// handleArchiveWorkspace handles POST /api/v1/workspaces/:slug/archive.
func handleArchiveWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		// PATs require workspaces:write scope to archive.
		// PATs without write access get 404 (anti-enumeration).
		if auth.CredType == CredentialPAT && !auth.hasWriteAccess() {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		slug := c.Param("slug")
		ws, _ := lookupWorkspaceForAuth(c, db, slug, auth)
		if ws == nil {
			return nil // Response already written by lookupWorkspaceForAuth.
		}

		if ws.Status == "archived" {
			return respondError(c, http.StatusBadRequest, "workspace is already archived")
		}

		updated, err := updateWorkspaceStatus(db, slug, "archived")
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "failed to archive workspace")
		}

		return respondWorkspace(c, http.StatusOK, updated)
	}
}

// handleReactivateWorkspace handles POST /api/v1/workspaces/:slug/reactivate.
func handleReactivateWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		// PATs require workspaces:write scope to reactivate.
		// PATs without write access get 404 (anti-enumeration).
		if auth.CredType == CredentialPAT && !auth.hasWriteAccess() {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		slug := c.Param("slug")
		ws, _ := lookupWorkspaceForAuth(c, db, slug, auth)
		if ws == nil {
			return nil // Response already written by lookupWorkspaceForAuth.
		}

		if ws.Status == "active" {
			return respondError(c, http.StatusBadRequest, "workspace is already active")
		}

		updated, err := updateWorkspaceStatus(db, slug, "active")
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "failed to reactivate workspace")
		}

		return respondWorkspace(c, http.StatusOK, updated)
	}
}

// handleDeleteWorkspace handles DELETE /api/v1/workspaces/:slug.
func handleDeleteWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth, err := getAuth(c)
		if err != nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		// PATs require workspaces:delete scope to delete.
		// PATs without delete access (including workspaces:write) get 404 (anti-enumeration).
		if auth.CredType == CredentialPAT && !auth.hasDeleteAccess() {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		slug := c.Param("slug")
		ws, _ := lookupWorkspaceForAuth(c, db, slug, auth)
		if ws == nil {
			return nil // Response already written by lookupWorkspaceForAuth.
		}

		// Only archived workspaces can be deleted.
		if ws.Status != "archived" {
			return respondError(c, http.StatusBadRequest, "only archived workspaces can be deleted")
		}

		if err := deleteWorkspace(db, slug); err != nil {
			return respondError(c, http.StatusInternalServerError, "failed to delete workspace")
		}

		return c.NoContent(http.StatusNoContent)
	}
}
