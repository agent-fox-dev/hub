package workspace

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// handleRecloneWorkspace handles POST /api/v1/workspaces/:slug/reclone.
// It executes the archive flow (push local commits to upstream, logging a
// warning if the push fails), deletes the local clone directory, atomically
// resets workspace state (clone_status='pending', sync_status='idle', clears
// sync_error and upstream_head_sha), enqueues a clone job, and returns the
// workspace with clone_status='pending' and status='active'.
//
// The workspace status is never changed by this handler — it remains 'active'
// throughout the entire reclone lifecycle (13-PROP-5).
//
// Requirements: 13-REQ-7
func handleRecloneWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// ---- Auth check (13-REQ-7.E5, 13-REQ-8) ----
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "workspaces:sync") {
			return respondError(c, http.StatusForbidden,
				"PAT requires workspaces:sync scope to reclone workspaces")
		}

		slug := c.Param("slug")

		// ---- Look up workspace (13-REQ-7.E6) ----
		ws, err := getWorkspaceBySlug(db, slug)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}
		if ws == nil {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		// ---- Reject concurrent reclone (13-REQ-7.E7) ----
		if ws.CloneStatus == "pending" || ws.CloneStatus == "cloning" {
			return respondError(c, http.StatusConflict,
				"clone operation already in progress; cannot reclone while clone_status is '"+ws.CloneStatus+"'")
		}

		// ---- Archive flow: push local commits (13-REQ-7.1, 13-REQ-7.E1) ----
		repoPath := filepath.Join(defaultWorkspaceRoot, slug, "trunk")

		// Record current HEAD SHA before deletion for logging purposes.
		if archiveHeadFn != nil {
			headSHA, headErr := archiveHeadFn(repoPath)
			if headErr != nil {
				log.Printf("warning: reclone %q: failed to read HEAD SHA: %v", slug, headErr)
			} else {
				log.Printf("reclone %q: recording HEAD SHA %s before deletion", slug, headSHA)
			}
		}

		// Attempt to push local commits to upstream.
		// 13-ERR-11: If push fails, log a warning and continue with reclone.
		if archiveOpenAndPushFn != nil {
			if pushErr := archiveOpenAndPushFn(repoPath, ws.GitURL); pushErr != nil {
				log.Printf("warning: reclone %q: archive push failed: %v", slug, pushErr)
			}
		}

		// ---- Delete local clone directory (13-REQ-7.E2) ----
		wsDir := filepath.Join(defaultWorkspaceRoot, slug)
		if err := os.RemoveAll(wsDir); err != nil {
			// 13-ERR-12: Directory deletion failed. Do not enqueue clone job;
			// workspace state is not transitioned to 'pending'.
			return respondError(c, http.StatusInternalServerError,
				fmt.Sprintf("failed to delete workspace directory: %v", err))
		}

		// ---- Atomic DB update (13-REQ-7.1, 13-REQ-7.2, 13-PROP-5) ----
		// Set clone_status='pending', sync_status='idle', clear sync_error and
		// upstream_head_sha. Workspace status remains 'active' — never modified.
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err = db.Exec(
			`UPDATE workspaces SET clone_status = 'pending', sync_status = 'idle', sync_error = NULL, upstream_head_sha = NULL, updated_at = ? WHERE slug = ?`,
			now, slug,
		)
		if err != nil {
			return respondError(c, http.StatusInternalServerError,
				"failed to update workspace state for reclone")
		}

		// ---- Enqueue clone job (13-REQ-7.1, 13-REQ-7.4) ----
		// The queue may be nil during tests that don't initialize it.
		if defaultQueue != nil {
			defaultQueue.Enqueue(CloneJob{
				Slug:   ws.Slug,
				GitURL: ws.GitURL,
				Branch: ws.Branch,
			})
		}

		// ---- Return updated workspace (13-REQ-7.1) ----
		updated, err := getWorkspaceBySlug(db, slug)
		if err != nil || updated == nil {
			return respondError(c, http.StatusInternalServerError,
				"failed to read workspace after reclone")
		}
		return respondWorkspace(c, http.StatusOK, updated, db)
	}
}
