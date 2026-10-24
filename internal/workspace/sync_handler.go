package workspace

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// SyncFetchAndCompareFuncType performs the complete fetch-and-compare step
// of a sync operation: opens the local repository, fetches from upstream,
// reads the upstream HEAD SHA, and compares it against localHeadSHA using
// commit-graph ancestry checks.
//
// Parameters:
//   - ctx: context for cancellation and timeout
//   - repoPath: local repository path (<WORKSPACE_ROOT>/<slug>/trunk/)
//   - auth: optional transport.AuthMethod for fetch authentication (nil = public)
//   - branch: workspace branch name (nil = default branch)
//   - localHeadSHA: current local integration branch HEAD SHA (empty if not set)
//
// Returns:
//   - upstreamHeadSHA: the upstream HEAD SHA after fetch
//   - outcome: "up_to_date", "fast_forward", or "diverged"
//   - err: non-nil on fetch failure, repo open failure, auth failure, etc.
type SyncFetchAndCompareFuncType func(ctx context.Context, repoPath string, auth transport.AuthMethod, branch *string, localHeadSHA string) (upstreamHeadSHA string, outcome string, err error)

// SyncUpdateLocalRefFuncType fast-forwards the local integration branch ref
// to newSHA. Called only when the fetch-and-compare step returns "fast_forward".
//
// Parameters:
//   - repoPath: local repository path
//   - branch: workspace branch name (nil = default branch)
//   - newSHA: the SHA to fast-forward to (upstream HEAD)
//
// Returns an error if the ref update fails (13-REQ-4.E6).
type SyncUpdateLocalRefFuncType func(repoPath string, branch *string, newSHA string) error

// syncFetchAndCompareFn is the injectable function for the fetch-and-compare
// step of sync. Tests replace it to simulate various sync outcomes (up-to-date,
// fast-forward, diverged, fetch errors, context cancellation).
// The production default is set during server init via InitSyncFunctions.
var syncFetchAndCompareFn SyncFetchAndCompareFuncType

// syncUpdateLocalRefFn is the injectable function for updating the local
// integration branch ref during fast-forward. Tests replace it to simulate
// ref update failures.
// The production default is set during server init via InitSyncFunctions.
var syncUpdateLocalRefFn SyncUpdateLocalRefFuncType

// InitSyncFunctions sets the production implementations of sync git operation
// functions. Called during server init alongside InitCloneQueue.
func InitSyncFunctions() {
	syncFetchAndCompareFn = defaultSyncFetchAndCompareFn
	syncUpdateLocalRefFn = defaultSyncUpdateLocalRefFn
}

// handleSyncWorkspace handles POST /api/v1/workspaces/:slug/sync.
// It validates preconditions, takes the workspace lock, performs fetch and
// fast-forward operations, and records the outcome on the workspace: a
// success sets upstream_head_sha and last_sync_at and clears sync_error, a
// failure sets sync_error. A sync in progress is only the lock; a second
// sync meanwhile is refused with 409 workspace_busy.
//
// Requirements: 13-REQ-3, 13-REQ-4, 13-REQ-9
func handleSyncWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// ---- Auth check (13-REQ-3.E1) ----
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if !hasWriteAccess(auth) {
			return respondError(c, http.StatusForbidden,
				"PAT requires workspaces:write scope to sync workspaces")
		}

		slug := c.Param("slug")

		// ---- Precondition checks (13-REQ-3) ----

		// 13-REQ-3.5: Workspace must exist and the caller must own it or
		// hold an admin token (spec 35: a member of its org gets 403, anyone
		// else 404).
		ws, access := lookupWorkspaceForAuth(c, db, slug, auth, wsaccess.NeedWrite)
		if ws == nil {
			return nil // Response already written by lookupWorkspaceForAuth.
		}

		// 13-REQ-3.1, 13-REQ-3.2: the workspace must be ready.
		if ws.Status != "ready" {
			return respondError(c, http.StatusBadRequest,
				"workspace is not ready; sync requires a ready workspace")
		}

		// 13-REQ-3.3: the sync_enabled setting must be true.
		if !ws.Settings.SyncEnabled {
			return respondError(c, http.StatusBadRequest,
				"sync is disabled for this workspace; set the sync_enabled setting to true to enable")
		}

		// ---- carry_patch mode delegation (16-REQ-5) ----
		// If the workspace is in carry_patch mode and a carry-patch sync hook
		// is registered, delegate to it. The extension handles upstream fetch,
		// merge detection, and auto-rebuild enqueue independently of the
		// standard sync flow.
		//
		// The carry-patch fields are returned in addition to the standard
		// sync fields, so the extension returns them as a map and this
		// handler merges them into the workspace response. The workspace row
		// is re-read first so that upstream_head_sha and last_sync_at reflect
		// the writes the extension just made.
		if ws.WorkspaceMode == "carry_patch" && syncExtension != nil {
			repoPath := filepath.Join(defaultWorkspaceRoot, slug, "trunk")
			extras, handled, hookErr := syncExtension(c, slug, repoPath)
			if hookErr != nil {
				return hookErr
			}
			if handled {
				updated, readErr := getWorkspaceBySlug(db, slug)
				if readErr != nil || updated == nil {
					return respondError(c, http.StatusInternalServerError,
						"failed to read workspace after sync")
				}
				return respondWorkspaceWithExtras(c, http.StatusOK, updated, db, access, extras)
			}
			// If not handled, fall through to standard sync.
		}

		// The fetch and fast-forward touch the shared trunk; refuse to run
		// while a rebuild, merge, reclone, or archive holds the workspace.
		unlock, locked := wslock.TryLock(slug)
		if !locked {
			return respondErrorWithType(c, http.StatusConflict,
				"another operation is running on this workspace; retry later", "workspace_busy")
		}
		defer unlock()

		// ---- Deferred cleanup (13-REQ-4.5) ----
		// Record a sync_error if no outcome was recorded before returning.
		// This handles panics, context cancellation, and any code path that
		// fails to record one.
		syncCompleted := false
		defer func() {
			if !syncCompleted {
				errMsg := "sync interrupted unexpectedly"
				if c.Request().Context().Err() != nil {
					errMsg = fmt.Sprintf("sync interrupted: %v", c.Request().Context().Err())
				}
				if setErr := recordSyncFailure(db, slug, nil, errMsg); setErr != nil {
					log.Printf("ERROR: deferred cleanup failed for workspace %q: %v", slug, setErr)
				}
			}
		}()

		// ---- Common setup for git operations ----

		// Open repo path.
		repoPath := filepath.Join(defaultWorkspaceRoot, slug, "trunk")

		// Resolve fetch credentials (13-REQ-4.1).
		store := secrets.NewStore(db)
		fetchAuth, authErr := resolveCloneAuth(store, slug)
		if authErr != nil {
			// 13-REQ-4.E5: Credential resolution failure.
			syncError := fmt.Sprintf("credential resolution failed: %v", authErr)
			_ = recordSyncFailure(db, slug, nil, syncError)
			syncCompleted = true
			return respondError(c, http.StatusBadGateway,
				"sync failed: credential resolution error")
		}

		// ---- Check for reset-to-upstream mode (13-REQ-6) ----
		if c.QueryParam("reset_to_upstream") == "true" {
			return handleResetToUpstream(c, db, slug, ws, access, repoPath, fetchAuth, &syncCompleted)
		}

		// ---- Standard sync: git operations (13-REQ-4.1 through 13-REQ-4.4) ----

		// Check for context cancellation before fetch.
		if c.Request().Context().Err() != nil {
			errMsg := fmt.Sprintf("sync interrupted: %v", c.Request().Context().Err())
			_ = recordSyncFailure(db, slug, nil, errMsg)
			syncCompleted = true
			return respondError(c, http.StatusGatewayTimeout,
				"sync interrupted: request context cancelled")
		}

		// Get current local HEAD SHA for comparison.
		localHeadSHA := ""
		if ws.HeadSHA != nil {
			localHeadSHA = *ws.HeadSHA
		}

		// Perform fetch and compare (13-REQ-4.1 through 13-REQ-4.4).
		if syncFetchAndCompareFn == nil {
			syncError := "sync system not initialized"
			_ = recordSyncFailure(db, slug, nil, syncError)
			syncCompleted = true
			return respondError(c, http.StatusBadGateway, "sync system not initialized")
		}
		upstreamHeadSHA, outcome, fetchErr := syncFetchAndCompareFn(
			c.Request().Context(), repoPath, fetchAuth, ws.Branch, localHeadSHA,
		)

		// Check for context cancellation after fetch attempt.
		if c.Request().Context().Err() != nil {
			errMsg := fmt.Sprintf("sync interrupted: %v", c.Request().Context().Err())
			_ = recordSyncFailure(db, slug, nil, errMsg)
			syncCompleted = true
			return respondError(c, http.StatusGatewayTimeout,
				"sync interrupted: request context cancelled")
		}

		if fetchErr != nil {
			// 13-REQ-4.E2: Fetch failure (network, auth, or repo open).
			syncError := fmt.Sprintf("upstream fetch failed: %v", fetchErr)
			_ = recordSyncFailure(db, slug, nil, syncError)
			syncCompleted = true
			return respondError(c, http.StatusBadGateway,
				"sync failed: upstream fetch error")
		}

		// ---- Process sync outcome ----

		switch outcome {
		case "up_to_date":
			// 13-REQ-4.2: Upstream HEAD equals local HEAD.
			if err := recordSyncSuccess(db, slug, nil, &upstreamHeadSHA); err != nil {
				syncError := fmt.Sprintf("failed to update sync status: %v", err)
				_ = recordSyncFailure(db, slug, nil, syncError)
				syncCompleted = true
				return respondError(c, http.StatusInternalServerError, "failed to update sync status")
			}
			syncCompleted = true
			updated, err := getWorkspaceBySlug(db, slug)
			if err != nil || updated == nil {
				return respondError(c, http.StatusInternalServerError, "failed to read workspace after sync")
			}
			// 18-REQ-1.5: Emit hub.workspace.sync audit event.
			emitHubAudit(c, audit.HubEvent{
				EventType:    "hub.workspace.sync",
				ResourceType: "workspace",
				ResourceID:   slug,
				Action:       "sync",
				Workspace:    slug,
				Metadata:     map[string]any{"result": "up_to_date"},
			})
			return respondWorkspace(c, http.StatusOK, updated, db, access)

		case "fast_forward":
			// 13-REQ-4.3: Fast-forward the integration branch.
			if refErr := syncUpdateLocalRefFn(repoPath, ws.Branch, upstreamHeadSHA); refErr != nil {
				// 13-REQ-4.E6: Ref update failure.
				syncError := fmt.Sprintf("ref update failed: %v", refErr)
				_ = recordSyncFailure(db, slug, nil, syncError)
				syncCompleted = true
				return respondError(c, http.StatusBadGateway,
					"sync failed: could not fast-forward integration branch")
			}

			// Update head_sha, upstream_head_sha, sync_error, last_sync_at.
			if err := recordSyncSuccess(db, slug, &upstreamHeadSHA, &upstreamHeadSHA); err != nil {
				syncError := fmt.Sprintf("failed to update sync status: %v", err)
				_ = recordSyncFailure(db, slug, nil, syncError)
				syncCompleted = true
				return respondError(c, http.StatusInternalServerError, "failed to update sync status")
			}
			syncCompleted = true
			updated, err := getWorkspaceBySlug(db, slug)
			if err != nil || updated == nil {
				return respondError(c, http.StatusInternalServerError, "failed to read workspace after sync")
			}
			// 18-REQ-1.5: Emit hub.workspace.sync audit event.
			emitHubAudit(c, audit.HubEvent{
				EventType:    "hub.workspace.sync",
				ResourceType: "workspace",
				ResourceID:   slug,
				Action:       "sync",
				Workspace:    slug,
				Metadata:     map[string]any{"result": "fast_forward"},
			})
			return respondWorkspace(c, http.StatusOK, updated, db, access)

		case "diverged":
			// 13-REQ-4.4: Upstream has diverged (force-push detected).
			syncError := "upstream history has diverged; use --reset-to-upstream to recover"
			_ = recordSyncFailure(db, slug, &upstreamHeadSHA, syncError)
			syncCompleted = true
			return respondError(c, http.StatusConflict, syncError)

		default:
			syncError := fmt.Sprintf("unexpected sync outcome: %s", outcome)
			_ = recordSyncFailure(db, slug, nil, syncError)
			syncCompleted = true
			return respondError(c, http.StatusInternalServerError, "unexpected sync outcome")
		}
	}
}

// handleResetToUpstream implements the reset-to-upstream recovery path
// (13-REQ-6). It fetches from upstream, force-updates the local integration
// branch ref to upstream HEAD (ignoring ancestry), and updates workspace state.
//
// This function is called from handleSyncWorkspace when reset_to_upstream=true.
// The syncCompleted pointer allows this function to coordinate with the
// deferred cleanup in the parent handler.
func handleResetToUpstream(c echo.Context, db *sql.DB, slug string, ws *Workspace, access wsaccess.Access, repoPath string, fetchAuth transport.AuthMethod, syncCompleted *bool) error {
	// Verify sync functions are initialized.
	if syncFetchAndCompareFn == nil {
		syncError := "sync system not initialized"
		_ = recordSyncFailure(db, slug, nil, syncError)
		*syncCompleted = true
		return respondError(c, http.StatusBadGateway, "sync system not initialized")
	}

	// Get current local HEAD SHA for comparison.
	localHeadSHA := ""
	if ws.HeadSHA != nil {
		localHeadSHA = *ws.HeadSHA
	}

	// Step 1: Fetch from upstream (13-REQ-6.1).
	// We reuse syncFetchAndCompareFn to perform the fetch. The outcome
	// (up_to_date, fast_forward, diverged) is ignored — reset-to-upstream
	// always force-updates the local ref regardless of ancestry.
	upstreamHeadSHA, _, fetchErr := syncFetchAndCompareFn(
		c.Request().Context(), repoPath, fetchAuth, ws.Branch, localHeadSHA,
	)

	if fetchErr != nil {
		// 13-REQ-6.E1: Fetch failure — set error, do NOT modify head_sha.
		syncError := fmt.Sprintf("reset-to-upstream fetch failed: %v", fetchErr)
		_ = recordSyncFailure(db, slug, nil, syncError)
		*syncCompleted = true
		return respondError(c, http.StatusBadGateway,
			"reset-to-upstream failed: upstream fetch error")
	}

	// Step 2: Force-update the local integration branch ref (13-REQ-6.1).
	if syncUpdateLocalRefFn == nil {
		syncError := "sync system not initialized"
		_ = recordSyncFailure(db, slug, nil, syncError)
		*syncCompleted = true
		return respondError(c, http.StatusBadGateway, "sync system not initialized")
	}

	if refErr := syncUpdateLocalRefFn(repoPath, ws.Branch, upstreamHeadSHA); refErr != nil {
		// 13-REQ-6.E2: Ref update failure — set error, do NOT update head_sha.
		syncError := fmt.Sprintf("reset-to-upstream ref update failed: %v", refErr)
		_ = recordSyncFailure(db, slug, nil, syncError)
		*syncCompleted = true
		return respondError(c, http.StatusBadGateway,
			"reset-to-upstream failed: could not update integration branch")
	}

	// Step 3: Update workspace state (13-REQ-6.1, 13-PROP-8).
	// Set head_sha=upstream HEAD, upstream_head_sha=upstream HEAD,
	// last_sync_at=now, sync_error=NULL.
	if err := recordSyncSuccess(db, slug, &upstreamHeadSHA, &upstreamHeadSHA); err != nil {
		syncError := fmt.Sprintf("failed to update workspace state: %v", err)
		_ = recordSyncFailure(db, slug, nil, syncError)
		*syncCompleted = true
		return respondError(c, http.StatusInternalServerError,
			"reset-to-upstream failed: could not update workspace state")
	}

	*syncCompleted = true
	updated, err := getWorkspaceBySlug(db, slug)
	if err != nil || updated == nil {
		return respondError(c, http.StatusInternalServerError,
			"failed to read workspace after reset-to-upstream")
	}
	// 18-REQ-1.5: Emit hub.workspace.sync audit event for reset-to-upstream.
	emitHubAudit(c, audit.HubEvent{
		EventType:    "hub.workspace.sync",
		ResourceType: "workspace",
		ResourceID:   slug,
		Action:       "sync",
		Workspace:    slug,
		Metadata:     map[string]any{"result": "reset_to_upstream"},
	})
	return respondWorkspace(c, http.StatusOK, updated, db, access)
}

// recordSyncFailure records a failed sync: sync_error takes msg and, when
// upstreamHeadSHA is given, upstream_head_sha takes it. head_sha and
// last_sync_at are left as they are (13-REQ-4.4, 13-PROP-2, 13-PROP-7).
func recordSyncFailure(db *sql.DB, slug string, upstreamHeadSHA *string, msg string) error {
	now := time.Now().UTC().Format(timestampFormat)
	_, err := db.Exec(
		`UPDATE workspaces SET sync_error = ?, upstream_head_sha = COALESCE(?, upstream_head_sha), updated_at = ? WHERE slug = ?`,
		msg, upstreamHeadSHA, now, slug,
	)
	return err
}

// recordSyncSuccess records a successful sync: last_sync_at is now,
// sync_error is cleared, upstream_head_sha takes upstreamHeadSHA, and
// head_sha takes headSHA when given (after a fast-forward or reset).
func recordSyncSuccess(db *sql.DB, slug string, headSHA, upstreamHeadSHA *string) error {
	now := time.Now().UTC().Format(timestampFormat)
	_, err := db.Exec(
		`UPDATE workspaces SET sync_error = NULL, last_sync_at = ?, upstream_head_sha = ?, head_sha = COALESCE(?, head_sha), updated_at = ? WHERE slug = ?`,
		now, upstreamHeadSHA, headSHA, now, slug,
	)
	return err
}
