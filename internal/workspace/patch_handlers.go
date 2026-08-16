package workspace

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// validPatchStatuses defines the accepted patch status values (15-REQ-10.3).
var validPatchStatuses = map[string]bool{
	"active":          true,
	"merged_upstream": true,
	"conflict":        true,
	"disabled":        true,
}

// requirePatchReadScope checks that the caller has patches:read scope.
// Returns nil if authorized, or writes an error response and returns it.
func requirePatchReadScope(c echo.Context) error {
	auth := apikit.GetAuthInfo(c)
	if auth == nil {
		return respondError(c, http.StatusUnauthorized, "authentication required")
	}
	if isPAT(auth) && !hasScope(auth, "patches:read", "patches:write") {
		return respondError(c, http.StatusForbidden, "PAT requires patches:read scope")
	}
	return nil
}

// requirePatchWriteScope checks that the caller has patches:write scope.
// Returns nil if authorized, or writes an error response and returns it.
func requirePatchWriteScope(c echo.Context) error {
	auth := apikit.GetAuthInfo(c)
	if auth == nil {
		return respondError(c, http.StatusUnauthorized, "authentication required")
	}
	if isPAT(auth) && !hasScope(auth, "patches:write") {
		return respondError(c, http.StatusForbidden, "PAT requires patches:write scope")
	}
	return nil
}

// handleAddPatch handles POST /api/v1/workspaces/:slug/patches (15-REQ-8).
func handleAddPatch(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		if err := requirePatchWriteScope(c); err != nil {
			return err
		}

		slug := c.Param("slug")

		// Look up workspace.
		ws, err := getWorkspaceBySlug(db, slug)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}
		if ws == nil || ws.Status != "active" {
			return respondError(c, http.StatusBadRequest, "workspace not found or not active")
		}

		// 15-REQ-8.3: Reject for standard workspaces.
		if ws.WorkspaceMode != "carry_patch" {
			return respondError(c, http.StatusBadRequest, "workspace is not in carry_patch mode")
		}

		// Parse request body.
		var req struct {
			BranchName    string  `json:"branch_name"`
			Position      *int    `json:"position"`
			UpstreamPRURL *string `json:"upstream_pr_url"`
			Description   *string `json:"description"`
		}
		if c.Request().Body == nil {
			return respondError(c, http.StatusBadRequest, "request body is required")
		}
		if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
			return respondError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		}

		// 15-REQ-8.6: branch_name required.
		if req.BranchName == "" {
			return respondError(c, http.StatusBadRequest, "branch_name is required")
		}

		// 15-REQ-8.5: Reject if branch_name equals integration_branch.
		if ws.IntegrationBranch != nil && req.BranchName == *ws.IntegrationBranch {
			return respondError(c, http.StatusBadRequest, "branch_name cannot be the integration branch")
		}

		// 15-REQ-8.4: Reject if branch_name already exists.
		exists, err := branchExistsInPatches(db, slug, req.BranchName)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}
		if exists {
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

		return c.JSON(http.StatusCreated, patchResponse(p))
	}
}

// handleListPatches handles GET /api/v1/workspaces/:slug/patches (15-REQ-9).
func handleListPatches(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		if err := requirePatchReadScope(c); err != nil {
			return err
		}

		slug := c.Param("slug")

		// Look up workspace.
		ws, err := getWorkspaceBySlug(db, slug)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}
		// 15-REQ-9.E1: workspace not found returns 404.
		if ws == nil {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		// 15-REQ-9.E2: standard workspace returns empty array.
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
		if err := requirePatchWriteScope(c); err != nil {
			return err
		}

		slug := c.Param("slug")
		patchID := c.Param("id")

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

		// 15-REQ-10.3: Validate status.
		if req.Status != nil {
			if !validPatchStatuses[*req.Status] {
				return respondError(c, http.StatusBadRequest, "invalid status value; must be one of: active, merged_upstream, conflict, disabled")
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
		if err := requirePatchWriteScope(c); err != nil {
			return err
		}

		slug := c.Param("slug")
		patchID := c.Param("id")

		// 15-REQ-11.1, 15-REQ-11.2: Delete and compact.
		if err := deletePatchAndCompact(db, slug, patchID); err != nil {
			// Check if the error indicates not found.
			if err.Error() == "patch \""+patchID+"\" not found in workspace \""+slug+"\"" {
				return respondError(c, http.StatusNotFound, "patch not found")
			}
			return respondError(c, http.StatusInternalServerError, "failed to delete patch")
		}

		return c.NoContent(http.StatusNoContent)
	}
}

// handleReorderPatches handles POST /api/v1/workspaces/:slug/patches/reorder (15-REQ-12).
func handleReorderPatches(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		if err := requirePatchWriteScope(c); err != nil {
			return err
		}

		slug := c.Param("slug")

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
