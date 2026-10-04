package workspace

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// handleResetPatchToOrigin handles POST /api/v1/workspaces/:slug/patches/:id/reset-to-origin (23-REQ-2).
// It moves a patch branch to the fork's current tip regardless of PATCH_DIVERGENCE_POLICY.
func handleResetPatchToOrigin(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// 23-REQ-9.4: Use only the shared auth helpers.
		auth := requirePatchWriteScope(c)
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

		// 23-REQ-2.3: Same 400 messages as handleRestorePatch.
		if ws.Status != "active" {
			return respondError(c, http.StatusBadRequest, "workspace is not active")
		}
		if ws.WorkspaceMode != "carry_patch" {
			return respondError(c, http.StatusBadRequest, "workspace is not in carry_patch mode")
		}

		// 23-REQ-2.4: Clone must be ready.
		if ws.CloneStatus != "ready" {
			return respondError(c, http.StatusConflict, "workspace clone is not ready")
		}

		// 23-REQ-2.8: Check for nil hook before any git operation.
		hook := getRecoveryHook()
		if hook == nil {
			return respondError(c, http.StatusInternalServerError, "patch reset is not configured")
		}

		// 23-REQ-2.5: Unknown patch id → 404.
		p, err := getPatch(db, slug, patchID)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}
		if p == nil {
			return respondError(c, http.StatusNotFound, "patch not found")
		}

		// 23-REQ-2.5: Integration branch check.
		if ws.IntegrationBranch != nil && p.BranchName == *ws.IntegrationBranch {
			return respondError(c, http.StatusBadRequest, "patch branch is the integration branch")
		}

		// 23-REQ-2.6: Only active, conflict and disabled patches are eligible.
		switch p.Status {
		case "active", "conflict", "disabled":
			// eligible
		default:
			return respondError(c, http.StatusConflict,
				fmt.Sprintf("patch status %s cannot be reset", p.Status))
		}

		// Build the patch info for the hook.
		integrationBranch := ""
		if ws.IntegrationBranch != nil {
			integrationBranch = *ws.IntegrationBranch
		}
		patchInfo := ResetPatchInfo{
			ID:                p.ID,
			BranchName:        p.BranchName,
			Status:            p.Status,
			IntegrationBranch: integrationBranch,
		}

		// 23-REQ-2.7: The hook takes the workspace lock internally.
		result, resetErr := hook.RunReset(c.Request().Context(), slug, patchInfo, auth)
		if resetErr != nil {
			return mapResetError(c, resetErr, p.BranchName)
		}

		// Re-read the patch to get the latest state after the reset.
		p, err = getPatch(db, slug, patchID)
		if err != nil || p == nil {
			return respondError(c, http.StatusInternalServerError, "failed to fetch updated patch")
		}

		// Build the response: patch object + replaced_sha + rebuild fields.
		resp := patchResponse(p)

		// Add replaced_sha when the backup ref exists after the reset.
		sha, found, readErr := hook.ReadReplacedSHA(c.Request().Context(), slug, p.BranchName)
		if readErr != nil {
			slog.Warn("failed to read replaced SHA after reset",
				"slug", slug,
				"branch", p.BranchName,
				"error", readErr,
			)
		} else if found {
			resp["replaced_sha"] = sha
		}

		resp["rebuild_triggered"] = result.RebuildTriggered
		if result.RebuildJobID != "" {
			resp["rebuild_job_id"] = result.RebuildJobID
		}

		// 23-REQ-10.1: Emit hub.patch.reset audit event on success.
		resetMeta := map[string]any{
			"branch_name": p.BranchName,
			"action":      string(result.Action),
			"origin_sha":  result.OriginSHA,
		}
		// 23-REQ-10.2: Omit local_sha when there was no local branch.
		if result.LocalSHA != "" {
			resetMeta["local_sha"] = result.LocalSHA
		}
		// 23-REQ-10.2: Include replaced_sha only when action is replaced.
		if result.Action == ResetActionReplaced {
			resetMeta["replaced_sha"] = result.ReplacedSHA
		}

		emitHubAudit(c, audit.HubEvent{
			EventType:    audit.EventPatchReset,
			ResourceType: "patch",
			ResourceID:   p.BranchName,
			Action:       "reset",
			Workspace:    slug,
			Metadata:     resetMeta,
		})

		// 23-REQ-10.3: For action replaced, also emit hub.patch.replace with trigger.
		if result.Action == ResetActionReplaced {
			emitHubAudit(c, audit.HubEvent{
				EventType:    audit.EventPatchReplace,
				ResourceType: "patch",
				ResourceID:   p.BranchName,
				Action:       "replace",
				Workspace:    slug,
				Metadata: map[string]any{
					"branch_name":  p.BranchName,
					"replaced_sha": result.ReplacedSHA,
					"origin_sha":   result.OriginSHA,
					"trigger":      "reset_to_origin",
				},
			})
		}

		return c.JSON(http.StatusOK, resp)
	}
}

// mapResetError maps a classified ResetError to the appropriate HTTP response.
// 23-REQ-9.2: Each error kind maps to a specific status code, error type and message.
func mapResetError(c echo.Context, err error, branchName string) error {
	var resetErr *ResetError
	if !errors.As(err, &resetErr) {
		// Unclassified error: internal server error.
		slog.Error("reset: unclassified error",
			"branch", branchName,
			"error", err,
		)
		return respondError(c, http.StatusInternalServerError,
			fmt.Sprintf("failed to update patch branch %s", branchName))
	}

	switch resetErr.Kind {
	case ResetErrBusy:
		return respondErrorWithType(c, http.StatusConflict,
			"another operation is running on this workspace; retry later", "workspace_busy")
	case ResetErrMissingOnOrigin:
		return respondErrorWithType(c, http.StatusConflict,
			"branch does not exist on origin", "missing_on_origin")
	case ResetErrFetchFailed:
		return respondErrorWithType(c, http.StatusBadGateway,
			"origin fetch failed", "origin_fetch_failed")
	case ResetErrCredentialFailed:
		return respondError(c, http.StatusBadGateway,
			"failed to resolve origin credentials")
	case ResetErrRefChanged:
		return respondErrorWithType(c, http.StatusConflict,
			"patch branch changed during reset; retry", "ref_changed")
	default:
		return respondError(c, http.StatusInternalServerError,
			fmt.Sprintf("failed to update patch branch %s", branchName))
	}
}
