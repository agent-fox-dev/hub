package workspace

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// OrgMembershipCheckFunc is the signature for org membership checks.
// Returns (0, "") on success, or (httpCode, message) on failure.
type OrgMembershipCheckFunc func(db *sql.DB, userID, orgID string) (int, string)

// orgMembershipCheckFn is the function used to check org membership.
// Defaults to checkOrgMembership. Tests can replace it to inject service
// errors or timeouts — see TS-03-E14.
var orgMembershipCheckFn OrgMembershipCheckFunc = checkOrgMembership

// respondError writes a JSON error envelope {"error":{"code":N,"message":"..."}}
// and sets the HTTP status code. Delegates to apikit.WriteAPIError for a
// consistent error format across the platform.
func respondError(c echo.Context, code int, message string) error {
	return apikit.WriteAPIError(c, code, message)
}

// respondWorkspace writes a workspace JSON object as the response body.
// It computes the hub_url from the workspace's org and the configured external URL.
func respondWorkspace(c echo.Context, code int, ws *Workspace, db *sql.DB) error {
	hubURL := buildHubURL(db, ws)
	return c.JSON(code, workspaceResponse(ws, hubURL))
}

// workspaceResponse converts a Workspace to a JSON-serializable map.
// hubURL is included as-is; pass nil for workspaces without a hub URL.
func workspaceResponse(ws *Workspace, hubURL *string) map[string]any {
	return map[string]any{
		"slug":         ws.Slug,
		"git_url":      ws.GitURL,
		"hub_url":      hubURL,
		"branch":       ws.Branch,
		"owner_id":     ws.OwnerID,
		"org_id":       ws.OrgID,
		"status":       ws.Status,
		"display_name": ws.DisplayName,
		"description":  ws.Description,
		"clone_status": ws.CloneStatus,
		"head_sha":     ws.HeadSHA,
		"clone_error":  ws.CloneError,
		"created_at":   ws.CreatedAt,
		"updated_at":   ws.UpdatedAt,
	}
}

// buildHubURL constructs the hub git server URL for a workspace.
// Returns nil if the external URL is not configured or the workspace has no org.
func buildHubURL(db *sql.DB, ws *Workspace) *string {
	if defaultExternalURL == "" || ws.OrgID == nil {
		return nil
	}
	var orgSlug string
	err := db.QueryRow("SELECT slug FROM orgs WHERE id = ?", *ws.OrgID).Scan(&orgSlug)
	if err != nil {
		return nil
	}
	url := defaultExternalURL + "/git/" + orgSlug + "/" + ws.Slug + ".git"
	return &url
}

// buildHubURLMap builds hub URLs for a batch of workspaces in a single query.
func buildHubURLMap(db *sql.DB, workspaces []*Workspace) map[string]*string {
	result := make(map[string]*string, len(workspaces))
	if defaultExternalURL == "" {
		return result
	}

	orgIDs := make(map[string]bool)
	for _, ws := range workspaces {
		if ws.OrgID != nil {
			orgIDs[*ws.OrgID] = true
		}
	}
	if len(orgIDs) == 0 {
		return result
	}

	orgSlugs := make(map[string]string)
	for id := range orgIDs {
		var slug string
		if err := db.QueryRow("SELECT slug FROM orgs WHERE id = ?", id).Scan(&slug); err == nil {
			orgSlugs[id] = slug
		}
	}

	for _, ws := range workspaces {
		if ws.OrgID != nil {
			if orgSlug, ok := orgSlugs[*ws.OrgID]; ok {
				url := defaultExternalURL + "/git/" + orgSlug + "/" + ws.Slug + ".git"
				result[ws.Slug] = &url
			}
		}
	}
	return result
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
func lookupWorkspaceForAuth(c echo.Context, db *sql.DB, slug string, auth *apikit.AuthInfo) (*Workspace, error) {
	ws, err := getWorkspaceBySlug(db, slug)
	if err != nil {
		return nil, respondError(c, http.StatusInternalServerError, "internal server error")
	}
	if ws == nil {
		return nil, respondError(c, http.StatusNotFound, "workspace not found")
	}

	if isAdmin(auth) {
		return ws, nil
	}

	if ws.OwnerID != auth.UserID {
		return nil, respondError(c, http.StatusNotFound, "workspace not found")
	}

	return ws, nil
}

// handleCreateWorkspace handles POST /api/v1/workspaces.
func handleCreateWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		if isAdmin(auth) {
			return respondError(c, http.StatusForbidden, "admin tokens cannot create workspaces; a real user is required as owner")
		}

		if isPAT(auth) && !hasScope(auth, "workspaces:create") {
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

		// Validate org_id if provided, or auto-assign personal org if omitted.
		if req.OrgID != nil && *req.OrgID != "" {
			// Explicit org_id: validate membership as before (04-REQ-8.2).
			orgCode, orgMsg := orgMembershipCheckFn(db, auth.UserID, *req.OrgID)
			if orgCode != 0 {
				return respondError(c, orgCode, orgMsg)
			}
		} else {
			// No org_id: look up user's personal org (04-REQ-8.1).
			personalOrgID, err := lookupPersonalOrg(db, auth.UserID)
			if err == errNoPersonalOrg {
				// 04-REQ-8.3: user has no personal org.
				return respondError(c, http.StatusBadRequest,
					"user has no personal organization; contact an administrator")
			}
			if err != nil {
				// 04-REQ-8.E1: database error during personal org lookup.
				return respondError(c, http.StatusInternalServerError, "internal server error")
			}
			req.OrgID = &personalOrgID
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
			CloneStatus: "pending",
		}

		if err := insertWorkspace(db, ws); err != nil {
			// Handle unique constraint violation (concurrent duplicate slug insert).
			if isUniqueConstraintError(err) {
				return respondError(c, http.StatusConflict, "workspace with slug '"+req.Slug+"' already exists")
			}
			return respondError(c, http.StatusInternalServerError, "failed to create workspace")
		}

		// Enqueue a clone job for the newly created workspace.
		// The queue may be nil during tests that don't initialize it.
		if defaultQueue != nil {
			defaultQueue.Enqueue(CloneJob{
				Slug:   ws.Slug,
				GitURL: ws.GitURL,
				Branch: ws.Branch,
			})
		}

		return respondWorkspace(c, http.StatusCreated, ws, db)
	}
}

// checkOrgMembership verifies that the org exists and the user is a member.
// Returns (0, "") if the check passes, or (httpCode, message) on failure.
// Returns 500 on actual database/service errors (query failure, table missing),
// 400 if the org does not exist, and 403 if the user is not a member.
func checkOrgMembership(db *sql.DB, userID, orgID string) (int, string) {
	// Try to query the orgs table (apikit schema uses 'orgs').
	var exists int
	err := db.QueryRow("SELECT COUNT(*) FROM orgs WHERE id = ?", orgID).Scan(&exists)
	if err != nil {
		// Table might not exist or query failed — this is a service error.
		return http.StatusInternalServerError, "organization membership check failed"
	}
	if exists == 0 {
		return http.StatusBadRequest, "organization not found"
	}

	// Check membership.
	var isMember int
	err = db.QueryRow("SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID).Scan(&isMember)
	if err != nil {
		// Query failed — this is a service error.
		return http.StatusInternalServerError, "organization membership check failed"
	}
	if isMember == 0 {
		return http.StatusForbidden, "user is not a member of the specified organization"
	}

	return 0, ""
}

// errNoPersonalOrg is a sentinel error returned by lookupPersonalOrg when the
// user has no org with owner_id matching their user ID.
var errNoPersonalOrg = errors.New("no personal organization found")

// lookupPersonalOrg queries the orgs table for an org owned by the given user.
// Returns the org id on success, errNoPersonalOrg when no row exists, or a
// wrapped database error on query failure.
//
// When multiple rows exist (data inconsistency, 04-REQ-8.E2) the first result
// is returned and a warning is logged.
func lookupPersonalOrg(db *sql.DB, userID string) (string, error) {
	rows, err := db.Query("SELECT id FROM orgs WHERE owner_id = ?", userID)
	if err != nil {
		return "", fmt.Errorf("personal org lookup: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", fmt.Errorf("personal org lookup: %w", err)
		}
		return "", errNoPersonalOrg
	}

	var orgID string
	if err := rows.Scan(&orgID); err != nil {
		return "", fmt.Errorf("personal org lookup scan: %w", err)
	}

	// 04-REQ-8.E2: log warning if multiple personal orgs exist.
	if rows.Next() {
		log.Printf("WARNING: user %s has multiple personal organizations; using first result", userID)
	}

	return orgID, nil
}

// updatePatchFields tracks which mutable fields were included in a PATCH body.
// It uses explicit "set" flags to distinguish absent fields from provided ones
// (including null values). This allows partial updates where absent fields
// remain unchanged while null values are normalized to defaults.
type updatePatchFields struct {
	SetDisplayName bool
	DisplayName    *string // nil = JSON null, non-nil = provided value
	SetDescription bool
	Description    *string // nil = JSON null, non-nil = provided value
	SetOrgID       bool
	OrgID          *string // nil = JSON null, non-nil = provided value
}

// handleUpdateWorkspace handles PATCH /api/v1/workspaces/:slug.
// It supports partial updates of mutable workspace fields (display_name,
// description, org_id) while rejecting attempts to modify immutable fields
// (slug, git_url, branch, owner_id).
func handleUpdateWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		if isPAT(auth) && !hasWriteAccess(auth) {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		slug := c.Param("slug")

		// Parse request body as raw JSON to detect present vs absent fields.
		if c.Request().Body == nil {
			return respondError(c, http.StatusBadRequest, "request body is required")
		}
		var rawBody map[string]json.RawMessage
		if err := json.NewDecoder(c.Request().Body).Decode(&rawBody); err != nil {
			return respondError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		}

		// Reject immutable fields in the PATCH body.
		immutableFields := []string{"slug", "git_url", "branch", "owner_id"}
		for _, field := range immutableFields {
			if _, present := rawBody[field]; present {
				return respondError(c, http.StatusBadRequest, field+" is immutable and cannot be updated")
			}
		}

		// Parse mutable fields from the raw body.
		var fields updatePatchFields
		mutableCount := 0

		if raw, ok := rawBody["display_name"]; ok {
			fields.SetDisplayName = true
			mutableCount++
			if string(raw) != "null" {
				var s string
				if err := json.Unmarshal(raw, &s); err != nil {
					return respondError(c, http.StatusBadRequest, "invalid display_name value")
				}
				fields.DisplayName = &s
			}
		}

		if raw, ok := rawBody["description"]; ok {
			fields.SetDescription = true
			mutableCount++
			if string(raw) != "null" {
				var s string
				if err := json.Unmarshal(raw, &s); err != nil {
					return respondError(c, http.StatusBadRequest, "invalid description value")
				}
				fields.Description = &s
			}
		}

		if raw, ok := rawBody["org_id"]; ok {
			fields.SetOrgID = true
			mutableCount++
			if string(raw) != "null" {
				var s string
				if err := json.Unmarshal(raw, &s); err != nil {
					return respondError(c, http.StatusBadRequest, "invalid org_id value")
				}
				fields.OrgID = &s
			}
		}

		// Reject empty body (no mutable fields provided).
		if mutableCount == 0 {
			return respondError(c, http.StatusBadRequest, "request body must contain at least one updatable field")
		}

		// Look up workspace and verify ownership / anti-enumeration.
		ws, _ := lookupWorkspaceForAuth(c, db, slug, auth)
		if ws == nil {
			return nil // Response already written by lookupWorkspaceForAuth.
		}

		// Archived workspace cannot be updated — must be reactivated first.
		if ws.Status == "archived" {
			return respondError(c, http.StatusBadRequest, "workspace is archived and must be reactivated before updating")
		}

		// Validate and normalize provided fields, applying them to the loaded workspace.
		if fields.SetDisplayName {
			dn := normalizeDisplayName(slug, fields.DisplayName)
			if len(dn) > 128 {
				return respondError(c, http.StatusBadRequest, "display_name must not exceed 128 characters")
			}
			ws.DisplayName = dn
		}

		if fields.SetDescription {
			desc := normalizeDescription(fields.Description)
			if len(desc) > 1024 {
				return respondError(c, http.StatusBadRequest, "description must not exceed 1024 characters")
			}
			ws.Description = desc
		}

		if fields.SetOrgID {
			if fields.OrgID != nil && *fields.OrgID != "" {
				// Verify org membership before updating.
				orgCode, orgMsg := orgMembershipCheckFn(db, auth.UserID, *fields.OrgID)
				if orgCode != 0 {
					return respondError(c, orgCode, orgMsg)
				}
				ws.OrgID = fields.OrgID
			} else {
				// null or empty → remove org association.
				ws.OrgID = nil
			}
		}

		// Persist the update: write all mutable fields (unchanged ones retain
		// their loaded values) and refresh updated_at.
		updated, err := updateWorkspaceRow(db, slug, ws.DisplayName, ws.Description, ws.OrgID)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "failed to update workspace")
		}

		return respondWorkspace(c, http.StatusOK, updated, db)
	}
}

// handleListWorkspaces handles GET /api/v1/workspaces.
func handleListWorkspaces(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		if isPAT(auth) && !hasReadAccess(auth) {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		includeArchived := c.QueryParam("include_archived") == "true"

		var workspaces []*Workspace
		var err error

		if isAdmin(auth) {
			workspaces, err = listAllWorkspaces(db, includeArchived)
		} else {
			workspaces, err = listWorkspacesByOwner(db, auth.UserID, includeArchived)
		}
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}

		// Build response array.
		hubURLs := buildHubURLMap(db, workspaces)
		result := make([]map[string]any, 0, len(workspaces))
		for _, ws := range workspaces {
			result = append(result, workspaceResponse(ws, hubURLs[ws.Slug]))
		}

		return c.JSON(http.StatusOK, result)
	}
}

// handleGetWorkspace handles GET /api/v1/workspaces/:slug.
func handleGetWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		if isPAT(auth) && !hasReadAccess(auth) {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		slug := c.Param("slug")
		ws, _ := lookupWorkspaceForAuth(c, db, slug, auth)
		if ws == nil {
			return nil // Response already written by lookupWorkspaceForAuth.
		}

		return respondWorkspace(c, http.StatusOK, ws, db)
	}
}

// handleArchiveWorkspace handles POST /api/v1/workspaces/:slug/archive.
// The handler branches on clone_status to determine the archive strategy:
//   - ready:          push to origin, record head_sha, delete workspace dir
//   - cloning:        reject with HTTP 409 (clone in progress)
//   - pending/failed: clean up any partial directory, no git push
func handleArchiveWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		if isPAT(auth) && !hasWriteAccess(auth) {
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

		switch ws.CloneStatus {
		case "cloning":
			// 05-REQ-6.3: Reject archive while clone is in progress.
			return respondError(c, http.StatusConflict,
				"clone in progress; try again after it completes")

		case "ready":
			// Record head_sha, delete workspace directory, then archive.
			// Upstream push is deferred until credential management is implemented.
			repoPath := filepath.Join(defaultWorkspaceRoot, slug, "trunk")

			headSHA, headErr := archiveHeadFn(repoPath)
			if headErr != nil {
				// 05-REQ-6.E2: HEAD read failure aborts the archive;
				// workspace directory is NOT deleted.
				return respondError(c, http.StatusInternalServerError, headErr.Error())
			}

			// Delete workspace directory from disk.
			wsDir := filepath.Join(defaultWorkspaceRoot, slug)
			if rmErr := os.RemoveAll(wsDir); rmErr != nil {
				// 05-REQ-6.E3: Log warning but continue with DB update.
				log.Printf("warning: failed to delete workspace directory %q: %v", wsDir, rmErr)
			}

			// Update DB: status='archived', clone_status='archived', head_sha recorded.
			updated, err := archiveWorkspaceDB(db, slug, &headSHA)
			if err != nil {
				return respondError(c, http.StatusInternalServerError, "failed to archive workspace")
			}
			return respondWorkspace(c, http.StatusOK, updated, db)

		case "pending", "failed":
			// 05-REQ-6.2: No git push; just clean up and archive.
			wsDir := filepath.Join(defaultWorkspaceRoot, slug)
			_ = os.RemoveAll(wsDir) // Ignore not-exist errors.

			updated, err := archiveWorkspaceDB(db, slug, nil)
			if err != nil {
				return respondError(c, http.StatusInternalServerError, "failed to archive workspace")
			}
			return respondWorkspace(c, http.StatusOK, updated, db)

		default:
			// Safety net for unexpected clone_status values.
			updated, err := archiveWorkspaceDB(db, slug, nil)
			if err != nil {
				return respondError(c, http.StatusInternalServerError, "failed to archive workspace")
			}
			return respondWorkspace(c, http.StatusOK, updated, db)
		}
	}
}

// handleReactivateWorkspace handles POST /api/v1/workspaces/:slug/reactivate.
func handleReactivateWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		if isPAT(auth) && !hasWriteAccess(auth) {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		slug := c.Param("slug")
		ws, _ := lookupWorkspaceForAuth(c, db, slug, auth)
		if ws == nil {
			return nil // Response already written by lookupWorkspaceForAuth.
		}

		// 05-REQ-7.E3: Only archived workspaces can be reactivated.
		if ws.Status != "archived" {
			return respondError(c, http.StatusConflict, "workspace is not archived")
		}

		// 05-REQ-7.1: Set status='active', clone_status='pending',
		// clear clone_error, and refresh updated_at.
		updated, err := reactivateWorkspaceDB(db, slug)
		if err != nil {
			// 05-REQ-7.E4: DB failure returns 500 without enqueuing a job.
			return respondError(c, http.StatusInternalServerError, "failed to reactivate workspace")
		}

		// Enqueue a reclone job using the workspace's git_url and branch.
		if defaultQueue != nil {
			defaultQueue.Enqueue(CloneJob{
				Slug:   ws.Slug,
				GitURL: ws.GitURL,
				Branch: ws.Branch,
			})
		}

		return respondWorkspace(c, http.StatusOK, updated, db)
	}
}

// handleDeleteWorkspace handles DELETE /api/v1/workspaces/:slug.
func handleDeleteWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}

		if isPAT(auth) && !hasDeleteAccess(auth) {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		slug := c.Param("slug")
		ws, _ := lookupWorkspaceForAuth(c, db, slug, auth)
		if ws == nil {
			return nil // Response already written by lookupWorkspaceForAuth.
		}

		// 05-REQ-8.E2: Only archived workspaces can be deleted.
		if ws.Status != "archived" {
			return respondError(c, http.StatusConflict, "workspace must be archived before deletion")
		}

		// 05-REQ-8.1: Check whether workspace directory exists and remove it.
		wsDir := filepath.Join(defaultWorkspaceRoot, slug)
		if _, statErr := os.Stat(wsDir); statErr == nil {
			if rmErr := os.RemoveAll(wsDir); rmErr != nil {
				// 05-REQ-8.E5: Log warning but proceed with DB deletion.
				log.Printf("WARN: failed to remove workspace dir %s: %v", wsDir, rmErr)
			}
		}

		if err := deleteWorkspace(db, slug); err != nil {
			return respondError(c, http.StatusInternalServerError, "failed to delete workspace")
		}

		return c.NoContent(http.StatusNoContent)
	}
}
