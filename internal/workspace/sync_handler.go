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

	"github.com/agent-fox-dev/hub/internal/secrets"
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
// It validates preconditions, sets sync_status='syncing', performs fetch
// and fast-forward operations, and updates workspace state accordingly.
//
// Requirements: 13-REQ-3, 13-REQ-4, 13-REQ-9
func handleSyncWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// ---- Auth check (13-REQ-3.E1) ----
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return respondError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "workspaces:sync") {
			return respondError(c, http.StatusForbidden,
				"PAT requires workspaces:sync scope to sync workspaces")
		}

		slug := c.Param("slug")

		// ---- Precondition checks (13-REQ-3) ----

		// 13-REQ-3.5: Workspace must exist.
		ws, err := getWorkspaceBySlug(db, slug)
		if err != nil {
			return respondError(c, http.StatusInternalServerError, "internal server error")
		}
		if ws == nil {
			return respondError(c, http.StatusNotFound, "workspace not found")
		}

		// 13-REQ-3.1: Workspace status must be 'active'.
		if ws.Status != "active" {
			return respondError(c, http.StatusBadRequest,
				"workspace is not active; sync requires an active workspace")
		}

		// 13-REQ-3.2: Clone status must be 'ready'.
		if ws.CloneStatus != "ready" {
			return respondError(c, http.StatusBadRequest,
				"workspace clone is not ready; clone_status must be 'ready' to sync")
		}

		// 13-REQ-3.3: Sync mode must not be 'disabled'.
		if ws.SyncMode == "disabled" {
			return respondError(c, http.StatusBadRequest,
				"sync is disabled for this workspace; update sync_mode to enable")
		}

		// 13-REQ-3.4 / 13-REQ-9.E1: Reject concurrent syncs.
		if ws.SyncStatus == "syncing" {
			return respondError(c, http.StatusConflict,
				"sync already in progress for this workspace")
		}

		// ---- carry_patch mode delegation (16-REQ-5) ----
		// If the workspace is in carry_patch mode and a carry-patch sync hook
		// is registered, delegate to it. The hook handles upstream fetch,
		// merge detection, and auto-rebuild enqueue independently of the
		// standard sync flow.
		if ws.WorkspaceMode == "carry_patch" && carryPatchSyncHook != nil {
			repoPath := filepath.Join(defaultWorkspaceRoot, slug, "trunk")
			handled, hookErr := carryPatchSyncHook(c, slug, repoPath)
			if hookErr != nil {
				return hookErr
			}
			if handled {
				return nil
			}
			// If not handled, fall through to standard sync.
		}

		// ---- Set sync_status='syncing' (13-REQ-4.1, 13-REQ-9.1) ----
		if err := setSyncStatus(db, slug, "syncing", nil, nil, nil); err != nil {
			// 13-REQ-9.E2: DB failure at transition start.
			return respondError(c, http.StatusInternalServerError,
				"failed to update sync status")
		}

		// ---- Deferred cleanup (13-REQ-4.5) ----
		// Register deferred function that sets sync_status='error' if we
		// haven't set a final state before returning. This handles panics,
		// context cancellation, and any code path that fails to set a
		// terminal sync_status.
		syncCompleted := false
		defer func() {
			if !syncCompleted {
				errMsg := "sync interrupted unexpectedly"
				if c.Request().Context().Err() != nil {
					errMsg = fmt.Sprintf("sync interrupted: %v", c.Request().Context().Err())
				}
				if setErr := setSyncStatus(db, slug, "error", nil, &errMsg, nil); setErr != nil {
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
			_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
			syncCompleted = true
			return respondError(c, http.StatusBadGateway,
				"sync failed: credential resolution error")
		}

		// ---- Check for reset-to-upstream mode (13-REQ-6) ----
		if c.QueryParam("reset_to_upstream") == "true" {
			return handleResetToUpstream(c, db, slug, ws, repoPath, fetchAuth, &syncCompleted)
		}

		// ---- Standard sync: git operations (13-REQ-4.1 through 13-REQ-4.4) ----

		// Check for context cancellation before fetch.
		if c.Request().Context().Err() != nil {
			errMsg := fmt.Sprintf("sync interrupted: %v", c.Request().Context().Err())
			_ = setSyncStatus(db, slug, "error", nil, &errMsg, nil)
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
			_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
			syncCompleted = true
			return respondError(c, http.StatusBadGateway, "sync system not initialized")
		}
		upstreamHeadSHA, outcome, fetchErr := syncFetchAndCompareFn(
			c.Request().Context(), repoPath, fetchAuth, ws.Branch, localHeadSHA,
		)

		// Check for context cancellation after fetch attempt.
		if c.Request().Context().Err() != nil {
			errMsg := fmt.Sprintf("sync interrupted: %v", c.Request().Context().Err())
			_ = setSyncStatus(db, slug, "error", nil, &errMsg, nil)
			syncCompleted = true
			return respondError(c, http.StatusGatewayTimeout,
				"sync interrupted: request context cancelled")
		}

		if fetchErr != nil {
			// 13-REQ-4.E2: Fetch failure (network, auth, or repo open).
			syncError := fmt.Sprintf("upstream fetch failed: %v", fetchErr)
			_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
			syncCompleted = true
			return respondError(c, http.StatusBadGateway,
				"sync failed: upstream fetch error")
		}

		// ---- Process sync outcome ----

		now := time.Now().UTC().Format(time.RFC3339Nano)

		switch outcome {
		case "up_to_date":
			// 13-REQ-4.2: Upstream HEAD equals local HEAD.
			if err := setSyncStatus(db, slug, "idle", &upstreamHeadSHA, nil, &now); err != nil {
				syncError := fmt.Sprintf("failed to update sync status: %v", err)
				_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
				syncCompleted = true
				return respondError(c, http.StatusInternalServerError, "failed to update sync status")
			}
			syncCompleted = true
			updated, err := getWorkspaceBySlug(db, slug)
			if err != nil || updated == nil {
				return respondError(c, http.StatusInternalServerError, "failed to read workspace after sync")
			}
			return respondWorkspace(c, http.StatusOK, updated, db)

		case "fast_forward":
			// 13-REQ-4.3: Fast-forward the integration branch.
			if refErr := syncUpdateLocalRefFn(repoPath, ws.Branch, upstreamHeadSHA); refErr != nil {
				// 13-REQ-4.E6: Ref update failure.
				syncError := fmt.Sprintf("ref update failed: %v", refErr)
				_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
				syncCompleted = true
				return respondError(c, http.StatusBadGateway,
					"sync failed: could not fast-forward integration branch")
			}

			// Update head_sha, upstream_head_sha, sync_status, last_sync_at.
			if err := setSyncStatusWithHeadSHA(db, slug, "idle", &upstreamHeadSHA, &upstreamHeadSHA, nil, &now); err != nil {
				syncError := fmt.Sprintf("failed to update sync status: %v", err)
				_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
				syncCompleted = true
				return respondError(c, http.StatusInternalServerError, "failed to update sync status")
			}
			syncCompleted = true
			updated, err := getWorkspaceBySlug(db, slug)
			if err != nil || updated == nil {
				return respondError(c, http.StatusInternalServerError, "failed to read workspace after sync")
			}
			return respondWorkspace(c, http.StatusOK, updated, db)

		case "diverged":
			// 13-REQ-4.4: Upstream has diverged (force-push detected).
			syncError := "upstream history has diverged; use --reset-to-upstream to recover"
			_ = setSyncStatusDiverged(db, slug, &upstreamHeadSHA, &syncError)
			syncCompleted = true
			return respondError(c, http.StatusConflict, syncError)

		default:
			syncError := fmt.Sprintf("unexpected sync outcome: %s", outcome)
			_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
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
func handleResetToUpstream(c echo.Context, db *sql.DB, slug string, ws *Workspace, repoPath string, fetchAuth transport.AuthMethod, syncCompleted *bool) error {
	// Verify sync functions are initialized.
	if syncFetchAndCompareFn == nil {
		syncError := "sync system not initialized"
		_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
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
		_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
		*syncCompleted = true
		return respondError(c, http.StatusBadGateway,
			"reset-to-upstream failed: upstream fetch error")
	}

	// Step 2: Force-update the local integration branch ref (13-REQ-6.1).
	if syncUpdateLocalRefFn == nil {
		syncError := "sync system not initialized"
		_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
		*syncCompleted = true
		return respondError(c, http.StatusBadGateway, "sync system not initialized")
	}

	if refErr := syncUpdateLocalRefFn(repoPath, ws.Branch, upstreamHeadSHA); refErr != nil {
		// 13-REQ-6.E2: Ref update failure — set error, do NOT update head_sha.
		syncError := fmt.Sprintf("reset-to-upstream ref update failed: %v", refErr)
		_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
		*syncCompleted = true
		return respondError(c, http.StatusBadGateway,
			"reset-to-upstream failed: could not update integration branch")
	}

	// Step 3: Update workspace state (13-REQ-6.1, 13-PROP-8).
	// Set head_sha=upstream HEAD, upstream_head_sha=upstream HEAD,
	// sync_status='idle', last_sync_at=now, sync_error=NULL.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := setSyncStatusWithHeadSHA(db, slug, "idle", &upstreamHeadSHA, &upstreamHeadSHA, nil, &now); err != nil {
		syncError := fmt.Sprintf("failed to update workspace state: %v", err)
		_ = setSyncStatus(db, slug, "error", nil, &syncError, nil)
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
	return respondWorkspace(c, http.StatusOK, updated, db)
}

// setSyncStatus updates the sync_status, upstream_head_sha, sync_error, and
// optionally last_sync_at for the workspace identified by slug.
// Only updates upstream_head_sha and last_sync_at if non-nil.
func setSyncStatus(db *sql.DB, slug, syncStatus string, upstreamHeadSHA *string, syncError *string, lastSyncAt *string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if upstreamHeadSHA != nil && lastSyncAt != nil {
		_, err := db.Exec(
			`UPDATE workspaces SET sync_status = ?, upstream_head_sha = ?, sync_error = ?, last_sync_at = ?, updated_at = ? WHERE slug = ?`,
			syncStatus, upstreamHeadSHA, syncError, lastSyncAt, now, slug,
		)
		return err
	}
	if upstreamHeadSHA != nil {
		_, err := db.Exec(
			`UPDATE workspaces SET sync_status = ?, upstream_head_sha = ?, sync_error = ?, updated_at = ? WHERE slug = ?`,
			syncStatus, upstreamHeadSHA, syncError, now, slug,
		)
		return err
	}
	if lastSyncAt != nil {
		_, err := db.Exec(
			`UPDATE workspaces SET sync_status = ?, sync_error = ?, last_sync_at = ?, updated_at = ? WHERE slug = ?`,
			syncStatus, syncError, lastSyncAt, now, slug,
		)
		return err
	}
	_, err := db.Exec(
		`UPDATE workspaces SET sync_status = ?, sync_error = ?, updated_at = ? WHERE slug = ?`,
		syncStatus, syncError, now, slug,
	)
	return err
}

// setSyncStatusWithHeadSHA updates sync_status, head_sha, upstream_head_sha,
// sync_error, and last_sync_at. Used after a successful fast-forward to update
// both the local head_sha and upstream tracking state.
func setSyncStatusWithHeadSHA(db *sql.DB, slug, syncStatus string, headSHA, upstreamHeadSHA *string, syncError *string, lastSyncAt *string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`UPDATE workspaces SET sync_status = ?, head_sha = ?, upstream_head_sha = ?, sync_error = ?, last_sync_at = ?, updated_at = ? WHERE slug = ?`,
		syncStatus, headSHA, upstreamHeadSHA, syncError, lastSyncAt, now, slug,
	)
	return err
}

// setSyncStatusDiverged updates sync_status to 'error', sets upstream_head_sha,
// and records the divergence error message. Does NOT modify head_sha or last_sync_at
// (13-REQ-4.4, 13-PROP-2, 13-PROP-7).
func setSyncStatusDiverged(db *sql.DB, slug string, upstreamHeadSHA *string, syncError *string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`UPDATE workspaces SET sync_status = 'error', upstream_head_sha = ?, sync_error = ?, updated_at = ? WHERE slug = ?`,
		upstreamHeadSHA, syncError, now, slug,
	)
	return err
}
