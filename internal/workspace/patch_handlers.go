package workspace

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// validPatchStatuses defines the accepted patch status values (15-REQ-10.3).
var validPatchStatuses = map[string]bool{
	"active":          true,
	"merged_upstream": true,
	"conflict":        true,
	"disabled":        true,
	"deleted":         true,
}

// mapBranchResolveError maps a classified resolver error to the appropriate
// HTTP response. The batchIndex is -1 for single-patch requests; for batch
// requests it is the zero-based index of the failing element (only the 400
// message gets the "patch[i]: " prefix).
func mapBranchResolveError(c echo.Context, err error, slug, branch string, batchIndex int) error {
	kind, classified := classifyBranchResolveError(err)
	switch {
	case classified && kind == BranchResolveKindNotFound:
		msg := "branch does not exist in repository or on origin"
		if batchIndex >= 0 {
			msg = fmt.Sprintf("patch[%d]: %s", batchIndex, msg)
		}
		return respondError(c, http.StatusBadRequest, msg)

	case classified && kind == BranchResolveKindOriginFetchFailed:
		return respondErrorWithType(c, http.StatusBadGateway,
			"origin fetch failed", "origin_fetch_failed")

	case classified && kind == BranchResolveKindOriginCredentials:
		return respondError(c, http.StatusBadGateway,
			"failed to resolve origin credentials")

	case classified && kind == BranchResolveKindWorkspaceBusy:
		return respondErrorWithType(c, http.StatusConflict,
			"another operation is running on this workspace; retry later", "workspace_busy")

	default:
		// Unclassified or unknown kind: internal error.
		slog.Error("branch resolver: internal error",
			"slug", slug,
			"branch", branch,
			"error", err.Error(),
		)
		return respondError(c, http.StatusInternalServerError, "internal server error")
	}
}

// requirePatchReadScope checks that the caller has patches:read scope.
// Returns the auth info if authorized, or writes an error response and
// returns nil.
func requirePatchReadScope(c echo.Context) *apikit.AuthInfo {
	auth := apikit.GetAuthInfo(c)
	if auth == nil {
		_ = respondError(c, http.StatusUnauthorized, "authentication required")
		return nil
	}
	if isPAT(auth) && !hasScope(auth, "patches:read", "patches:write") {
		_ = respondError(c, http.StatusForbidden, "PAT requires patches:read scope")
		return nil
	}
	return auth
}

// requirePatchWriteScope checks that the caller has patches:write scope.
// Returns the auth info if authorized, or writes an error response and
// returns nil.
func requirePatchWriteScope(c echo.Context) *apikit.AuthInfo {
	auth := apikit.GetAuthInfo(c)
	if auth == nil {
		_ = respondError(c, http.StatusUnauthorized, "authentication required")
		return nil
	}
	if isPAT(auth) && !hasScope(auth, "patches:write") {
		_ = respondError(c, http.StatusForbidden, "PAT requires patches:write scope")
		return nil
	}
	return auth
}

// lookupPatchWorkspace resolves the workspace for a patch endpoint, enforcing
// ownership (404 for non-owners, admin bypass). Returns nil after writing the
// error response when the workspace is not accessible.
func lookupPatchWorkspace(c echo.Context, db *sql.DB, slug string, auth *apikit.AuthInfo) *Workspace {
	ws, _ := lookupWorkspaceForAuth(c, db, slug, auth)
	return ws
}

// addPatchRequest represents a single patch add request body.
type addPatchRequest struct {
	BranchName      string  `json:"branch_name"`
	Position        *int    `json:"position"`
	UpstreamPRURL   *string `json:"upstream_pr_url"`
	Description     *string `json:"description"`
	SkipBranchCheck *bool   `json:"skip_branch_check"`
	IfNotExists     *bool   `json:"if_not_exists"`
}

// handleAddPatch handles POST /api/v1/workspaces/:slug/patches (15-REQ-8).
// Supports single object or array body for batch insertion.
// When if_not_exists is true and the branch already exists, returns 200 with
// the existing record instead of 409.
func handleAddPatch(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requirePatchWriteScope(c)
		if auth == nil {
			return nil
		}

		slug := c.Param("slug")

		// Look up workspace (owner or admin only).
		ws := lookupPatchWorkspace(c, db, slug, auth)
		if ws == nil {
			return nil
		}
		if ws.Status != "active" {
			return respondError(c, http.StatusBadRequest, "workspace is not active")
		}

		// 15-REQ-8.3: Reject for standard workspaces.
		if ws.WorkspaceMode != "carry_patch" {
			return respondError(c, http.StatusBadRequest, "workspace is not in carry_patch mode")
		}

		if c.Request().Body == nil {
			return respondError(c, http.StatusBadRequest, "request body is required")
		}

		// Read the raw body to determine if it's an array or object.
		var rawBody json.RawMessage
		if err := json.NewDecoder(c.Request().Body).Decode(&rawBody); err != nil {
			return respondError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		}

		// 21-REQ-1.5: Scope the request so a request-scoped value (the
		// PATCH_BRANCH_SOURCE mode read by the branch-check hook) is read once
		// for the whole request, not once per batch element.
		c.SetRequest(c.Request().WithContext(wsaccess.WithRequestScope(c.Request().Context())))

		// Detect array vs object.
		trimmed := bytes.TrimSpace(rawBody)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			return handleAddPatchBatch(c, db, slug, ws, trimmed)
		}

		// Single object path.
		var req addPatchRequest
		if err := json.Unmarshal(trimmed, &req); err != nil {
			return respondError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		}

		return handleAddPatchSingle(c, db, slug, ws, req)
	}
}

// handleAddPatchSingle handles the single-object add patch path.
func handleAddPatchSingle(c echo.Context, db *sql.DB, slug string, ws *Workspace, req addPatchRequest) error {
	// 15-REQ-8.6: branch_name required and must be a valid git ref name.
	if req.BranchName == "" {
		return respondError(c, http.StatusBadRequest, "branch_name is required")
	}
	if err := validateBranch(req.BranchName); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid branch_name: "+err.Error())
	}

	// 15-REQ-8.5: Reject if branch_name equals integration_branch.
	if ws.IntegrationBranch != nil && req.BranchName == *ws.IntegrationBranch {
		return respondError(c, http.StatusBadRequest, "branch_name cannot be the integration branch")
	}

	// Validate branch existence in the git repo unless skip_branch_check is set.
	skipCheck := req.SkipBranchCheck != nil && *req.SkipBranchCheck
	var branchResolution string
	if branchCheckHook != nil {
		if skipCheck {
			branchResolution = ResolutionSkipped
		} else {
			method, err := branchCheckHook(c.Request().Context(), slug, req.BranchName)
			if err != nil {
				return mapBranchResolveError(c, err, slug, req.BranchName, -1)
			}
			branchResolution = method
		}
	}

	ifNotExists := req.IfNotExists != nil && *req.IfNotExists

	// 15-REQ-8.4: Check if branch_name already exists.
	exists, err := branchExistsInPatches(db, slug, req.BranchName)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "internal server error")
	}
	if exists {
		if ifNotExists {
			// Return the existing record with 200.
			existing, err := getPatchByBranch(db, slug, req.BranchName)
			if err != nil || existing == nil {
				return respondError(c, http.StatusInternalServerError, "internal server error")
			}
			return c.JSON(http.StatusOK, patchResponse(existing))
		}
		return respondError(c, http.StatusConflict, "branch already exists in patch list")
	}

	// 15-REQ-8.E3: Reject position < 1.
	if req.Position != nil && *req.Position < 1 {
		return respondError(c, http.StatusBadRequest, "position must be >= 1")
	}

	// Build patch.
	p := &Patch{
		WorkspaceSlug: slug,
		BranchName:    req.BranchName,
		UpstreamPRURL: req.UpstreamPRURL,
		Description:   req.Description,
	}
	if req.Position != nil {
		p.Position = *req.Position
	}

	// Insert with position handling.
	p, err = addPatchWithPosition(db, p)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "failed to add patch: "+err.Error())
	}

	// 18-REQ-3.1: Emit hub.patch.create audit event.
	auditMeta := map[string]any{
		"branch_name": p.BranchName,
		"position":    p.Position,
	}
	// 21-REQ-8: Add branch_resolution when a hook is registered.
	if branchResolution != "" {
		auditMeta["branch_resolution"] = branchResolution
	}
	emitHubAudit(c, audit.HubEvent{
		EventType:    "hub.patch.create",
		ResourceType: "patch",
		ResourceID:   p.BranchName,
		Action:       "create",
		Workspace:    slug,
		Metadata:     auditMeta,
	})

	return c.JSON(http.StatusCreated, patchResponse(p))
}

// handleAddPatchBatch handles batch insertion of patches from a JSON array body.
// All patches are inserted atomically; if any fails, the entire batch rolls back.
func handleAddPatchBatch(c echo.Context, db *sql.DB, slug string, ws *Workspace, rawBody []byte) error {
	var reqs []addPatchRequest
	if err := json.Unmarshal(rawBody, &reqs); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
	}

	if len(reqs) == 0 {
		return respondError(c, http.StatusBadRequest, "batch request must contain at least one patch")
	}

	// Validate each patch in the batch: name, position, integration branch.
	for i, req := range reqs {
		if req.BranchName == "" {
			return respondError(c, http.StatusBadRequest, fmt.Sprintf("patch[%d]: branch_name is required", i))
		}
		if err := validateBranch(req.BranchName); err != nil {
			return respondError(c, http.StatusBadRequest, fmt.Sprintf("patch[%d]: invalid branch_name: %v", i, err))
		}
		if ws.IntegrationBranch != nil && req.BranchName == *ws.IntegrationBranch {
			return respondError(c, http.StatusBadRequest, fmt.Sprintf("patch[%d]: branch_name cannot be the integration branch", i))
		}
		if req.Position != nil && *req.Position < 1 {
			return respondError(c, http.StatusBadRequest, fmt.Sprintf("patch[%d]: position must be >= 1", i))
		}
	}

	// 21-REQ-6.1: Resolve all elements in array order before any insert.
	for i, req := range reqs {
		skipCheck := req.SkipBranchCheck != nil && *req.SkipBranchCheck
		if !skipCheck && branchCheckHook != nil {
			_, err := branchCheckHook(c.Request().Context(), slug, req.BranchName)
			if err != nil {
				return mapBranchResolveError(c, err, slug, req.BranchName, i)
			}
		}
	}

	// Check for duplicate branch names within the batch.
	seenBranches := make(map[string]bool, len(reqs))
	for i, req := range reqs {
		if seenBranches[req.BranchName] {
			return respondError(c, http.StatusConflict, fmt.Sprintf("patch[%d]: duplicate branch_name %q in batch", i, req.BranchName))
		}
		seenBranches[req.BranchName] = true
	}

	// Check for conflicts with existing patches.
	for i, req := range reqs {
		exists, err := branchExistsInPatches(db, slug, req.BranchName)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}
		if exists {
			return respondError(c, http.StatusConflict, fmt.Sprintf("patch[%d]: branch %q already exists in patch list", i, req.BranchName))
		}
	}

	// Build patches for batch insertion.
	patches := make([]*Patch, 0, len(reqs))
	for _, req := range reqs {
		p := &Patch{
			WorkspaceSlug: slug,
			BranchName:    req.BranchName,
			UpstreamPRURL: req.UpstreamPRURL,
			Description:   req.Description,
		}
		if req.Position != nil {
			p.Position = *req.Position
		}
		patches = append(patches, p)
	}

	// Insert all patches atomically.
	patches, err := addPatchesBatch(db, patches)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "failed to add patches: "+err.Error())
	}

	// Build response array.
	result := make([]map[string]any, 0, len(patches))
	for _, p := range patches {
		result = append(result, patchResponse(p))
	}

	return c.JSON(http.StatusCreated, result)
}

// handleGetPatch handles GET /api/v1/workspaces/:slug/patches/:id (23-REQ-1).
// Returns the patch object with replaced_sha when a backup ref exists.
func handleGetPatch(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requirePatchReadScope(c)
		if auth == nil {
			return nil
		}

		slug := c.Param("slug")
		patchID := c.Param("id")

		// Owner or admin only; 404 for non-owners.
		ws := lookupPatchWorkspace(c, db, slug, auth)
		if ws == nil {
			return nil
		}

		// Look up the patch by ID — includes soft-deleted patches.
		p, err := getPatch(db, slug, patchID)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}
		if p == nil {
			return respondError(c, http.StatusNotFound, "patch not found")
		}

		resp := patchResponse(p)

		// Add replaced_sha when a recovery hook is registered and the
		// backup ref exists. Errors are logged and never fail the request.
		hook := getRecoveryHook()
		if hook != nil {
			sha, found, err := hook.ReadReplacedSHA(c.Request().Context(), slug, p.BranchName)
			if err != nil {
				slog.Warn("failed to read replaced SHA",
					"slug", slug,
					"branch", p.BranchName,
					"error", err,
				)
			} else if found {
				resp["replaced_sha"] = sha
			}
		}

		return c.JSON(http.StatusOK, resp)
	}
}

// handleListPatches handles GET /api/v1/workspaces/:slug/patches (15-REQ-9).
func handleListPatches(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requirePatchReadScope(c)
		if auth == nil {
			return nil
		}

		slug := c.Param("slug")

		// 15-REQ-9.E1: workspace not found (or not owned) returns 404.
		ws := lookupPatchWorkspace(c, db, slug, auth)
		if ws == nil {
			return nil
		}

		// 15-REQ-9.E2: Standard workspaces return an empty array (they have no patches).
		if ws.WorkspaceMode != "carry_patch" {
			return c.JSON(http.StatusOK, []map[string]any{})
		}

		patches, err := listPatches(db, slug)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}

		result := make([]map[string]any, 0, len(patches))
		for _, p := range patches {
			result = append(result, patchResponse(p))
		}

		return c.JSON(http.StatusOK, result)
	}
}

// handleUpdatePatch handles PATCH /api/v1/workspaces/:slug/patches/:id (15-REQ-10).
func handleUpdatePatch(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requirePatchWriteScope(c)
		if auth == nil {
			return nil
		}

		slug := c.Param("slug")
		patchID := c.Param("id")

		if ws := lookupPatchWorkspace(c, db, slug, auth); ws == nil {
			return nil
		}

		// Look up the existing patch.
		p, err := getPatch(db, slug, patchID)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}
		// 15-REQ-10.2: patch not found.
		if p == nil {
			return respondError(c, http.StatusNotFound, "patch not found")
		}

		// Parse request body.
		var req struct {
			Position      *int    `json:"position"`
			Status        *string `json:"status"`
			Description   *string `json:"description"`
			UpstreamPRURL *string `json:"upstream_pr_url"`
		}
		if c.Request().Body != nil {
			if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
				return respondError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			}
		}

		// 15-REQ-10.3: Validate status. "deleted" is a rebuild outcome (soft
		// delete with deleted_at) and cannot be set directly; use DELETE to
		// remove a patch and POST .../restore to bring a soft-deleted one back.
		if req.Status != nil {
			if !validPatchStatuses[*req.Status] || *req.Status == "deleted" {
				return respondError(c, http.StatusBadRequest, "invalid status value; must be one of: active, merged_upstream, conflict, disabled")
			}
			if p.Status == "deleted" {
				return respondError(c, http.StatusConflict, "patch is soft-deleted; restore it first")
			}
			p.Status = *req.Status
		}

		// 15-REQ-10.E3: Validate position range.
		if req.Position != nil {
			count, err := patchCount(db, slug)
			if err != nil {
				return respondError(c, http.StatusInternalServerError, "internal server error")
			}
			if *req.Position < 1 || *req.Position > count {
				return respondError(c, http.StatusBadRequest, "position out of range")
			}
		}

		// Apply non-position field updates.
		if req.Description != nil {
			p.Description = req.Description
		}
		if req.UpstreamPRURL != nil {
			p.UpstreamPRURL = req.UpstreamPRURL
		}

		// Handle position change separately since it requires shifting.
		if req.Position != nil && *req.Position != p.Position {
			if err := updatePatchPosition(db, slug, patchID, *req.Position); err != nil {
				return respondError(c, http.StatusInternalServerError, "failed to update patch position")
			}
			p.Position = *req.Position
		}

		// Update other fields (status, description, upstream_pr_url, updated_at).
		p, err = updatePatch(db, p)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "failed to update patch")
		}

		// Re-fetch to get the authoritative state.
		p, err = getPatch(db, slug, patchID)
		if err != nil || p == nil {
			return respondError(c, http.StatusInternalServerError, "failed to fetch updated patch")
		}

		return c.JSON(http.StatusOK, patchResponse(p))
	}
}

// handleRemovePatch handles DELETE /api/v1/workspaces/:slug/patches/:id (15-REQ-11).
func handleRemovePatch(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requirePatchWriteScope(c)
		if auth == nil {
			return nil
		}

		slug := c.Param("slug")
		patchID := c.Param("id")

		if ws := lookupPatchWorkspace(c, db, slug, auth); ws == nil {
			return nil
		}

		// Look up patch before deletion for audit metadata (18-REQ-3.2)
		// and for the branch name needed by backup-ref cleanup (23-REQ-7.1).
		patchInfo, _ := getPatch(db, slug, patchID)

		// 15-REQ-11.1, 15-REQ-11.2: Delete and compact.
		if err := deletePatchAndCompact(db, slug, patchID); err != nil {
			// Check if the error indicates not found.
			if err.Error() == "patch \""+patchID+"\" not found in workspace \""+slug+"\"" {
				return respondError(c, http.StatusNotFound, "patch not found")
			}
			return respondError(c, http.StatusInternalServerError, "failed to delete patch")
		}

		// 18-REQ-3.2: Emit hub.patch.delete audit event.
		if patchInfo != nil {
			emitHubAudit(c, audit.HubEvent{
				EventType:    "hub.patch.delete",
				ResourceType: "patch",
				ResourceID:   patchInfo.BranchName,
				Action:       "delete",
				Workspace:    slug,
				Metadata: map[string]any{
					"branch_name": patchInfo.BranchName,
				},
			})
		}

		// 23-REQ-7.1: Remove the backup ref for the deleted patch.
		// Best effort: a failure never changes the response (the row is
		// already gone). No workspace lock is taken (23-REQ-7.5).
		if patchInfo != nil {
			hook := getRecoveryHook()
			if hook != nil {
				if err := hook.RemoveBackup(c.Request().Context(), slug, patchInfo.BranchName); err != nil {
					slog.Warn("failed to remove backup ref on patch delete",
						"slug", slug,
						"branch", patchInfo.BranchName,
						"error", err,
					)
				}
			}
		}

		return c.NoContent(http.StatusNoContent)
	}
}

// handleRestorePatch handles POST /api/v1/workspaces/:slug/patches/:id/restore.
// It transitions a soft-deleted patch back to active status and clears deleted_at.
func handleRestorePatch(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requirePatchWriteScope(c)
		if auth == nil {
			return nil
		}

		slug := c.Param("slug")
		patchID := c.Param("id")

		// Look up workspace (owner or admin only).
		ws := lookupPatchWorkspace(c, db, slug, auth)
		if ws == nil {
			return nil
		}
		if ws.Status != "active" {
			return respondError(c, http.StatusBadRequest, "workspace is not active")
		}
		if ws.WorkspaceMode != "carry_patch" {
			return respondError(c, http.StatusBadRequest, "workspace is not in carry_patch mode")
		}

		// Look up the patch by ID — must include deleted patches.
		row := db.QueryRow(
			`SELECT `+patchSelectColumns+` FROM patches WHERE workspace_slug = ? AND id = ?`,
			slug, patchID,
		)
		p, scanErr := scanPatch(row)
		if scanErr != nil {
			return respondError(c, http.StatusNotFound, "patch not found")
		}

		// Patch must be in 'deleted' status to restore.
		if p.Status != "deleted" {
			return respondError(c, http.StatusBadRequest, "patch is not in deleted status")
		}

		// Restore: set status to active, clear deleted_at, assign position at end.
		now := time.Now().UTC().Format(time.RFC3339)

		// Get max position for non-deleted patches.
		var maxPos sql.NullInt64
		err := db.QueryRow(
			`SELECT MAX(position) FROM patches WHERE workspace_slug = ? AND (status != 'deleted' OR status IS NULL)`,
			slug,
		).Scan(&maxPos)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}
		newPosition := 1
		if maxPos.Valid {
			newPosition = int(maxPos.Int64) + 1
		}

		_, err = db.Exec(
			`UPDATE patches SET status = 'active', deleted_at = NULL, position = ?, updated_at = ? WHERE id = ? AND workspace_slug = ?`,
			newPosition, now, patchID, slug,
		)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "failed to restore patch")
		}

		// Re-fetch the restored patch.
		p, err = getPatch(db, slug, patchID)
		if err != nil || p == nil {
			return respondError(c, http.StatusInternalServerError, "failed to fetch restored patch")
		}

		return c.JSON(http.StatusOK, patchResponse(p))
	}
}

// handleReorderPatches handles POST /api/v1/workspaces/:slug/patches/reorder (15-REQ-12).
func handleReorderPatches(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := requirePatchWriteScope(c)
		if auth == nil {
			return nil
		}

		slug := c.Param("slug")

		if ws := lookupPatchWorkspace(c, db, slug, auth); ws == nil {
			return nil
		}

		// Parse request body.
		var req struct {
			PatchIDs []string `json:"patch_ids"`
		}
		if c.Request().Body == nil {
			return respondError(c, http.StatusBadRequest, "request body is required")
		}
		if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
			return respondError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		}

		// Reorder with validation.
		patches, err := reorderPatches(db, slug, req.PatchIDs)
		if err != nil {
			return respondError(c, http.StatusBadRequest, err.Error())
		}

		result := make([]map[string]any, 0, len(patches))
		for _, p := range patches {
			result = append(result, patchResponse(p))
		}

		return c.JSON(http.StatusOK, result)
	}
}
