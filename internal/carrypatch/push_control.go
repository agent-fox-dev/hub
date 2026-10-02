package carrypatch

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitserver"
)

// PreReceiveHookDeps holds the dependencies for the carry-patch pre-receive hook.
type PreReceiveHookDeps struct {
	GetVariable GetVariableFunc
	ResolveAuth ResolveAuthFunc
}

// NewPreReceiveHook returns a gitserver.PreReceiveHookFunc that enforces
// push control for registered carry-patch branches.
//
// The hook is called once per ref update after the pack has been unpacked
// and before the hub writes the ref. It returns nil to allow the write,
// or a non-nil error to reject it (the error text becomes the per-ref
// status message the client sees).
func NewPreReceiveHook(deps PreReceiveHookDeps) gitserver.PreReceiveHookFunc {
	return newPreReceiveHookInternal(deps, slog.Default())
}

// newPreReceiveHookWithLogger is a test seam that allows injecting a custom
// logger for verifying log output in tests.
func newPreReceiveHookWithLogger(deps PreReceiveHookDeps, logger *slog.Logger) gitserver.PreReceiveHookFunc {
	return newPreReceiveHookInternal(deps, logger)
}

// newPreReceiveHookInternal is the shared implementation.
func newPreReceiveHookInternal(deps PreReceiveHookDeps, logger *slog.Logger) gitserver.PreReceiveHookFunc {
	return func(ctx context.Context, db *sql.DB, slug string, actor *apikit.AuthInfo, upd gitserver.RefUpdate) error {
		// Only refs/heads/<name> are candidates for push control.
		refName := string(upd.Name)
		branchName, isBranch := strings.CutPrefix(refName, "refs/heads/")
		if !isBranch {
			return nil
		}

		// Load workspace info. Fail open on errors (22-REQ-8.1).
		wsInfo, err := loadWorkspaceInfo(db, slug)
		if err != nil {
			if err != sql.ErrNoRows {
				logger.Warn("push control: failed to load workspace info",
					"slug", slug,
					"branch", branchName,
					"error", err.Error(),
				)
			}
			return nil
		}

		// 22-REQ-1.4: standard workspaces are not controlled.
		// 22-REQ-1.5: neither variable is read for standard workspaces.
		if wsInfo.mode != "carry_patch" {
			return nil
		}

		// 22-REQ-1.3: skip the integration branch even if a patch row exists.
		if branchName == wsInfo.integrationBranch {
			return nil
		}

		// Check if the branch is a registered patch (status != 'deleted').
		// Fail open on query errors (22-REQ-8.1).
		registered, err := isRegisteredPatchBranch(db, slug, branchName)
		if err != nil {
			logger.Warn("push control: failed to check patch registration",
				"slug", slug,
				"branch", branchName,
				"error", err.Error(),
			)
			return nil
		}
		if !registered {
			return nil
		}

		// 22-REQ-1.2: Read PATCH_BRANCH_SOURCE on every controlled update.
		source := ParsePatchBranchSource(deps.GetVariable, slug)

		// In hub mode, the hook returns nil (no rejection).
		// Hub-mode mirror is handled in the post-push hook (task 6).
		if source != "origin" {
			return nil
		}

		// 22-REQ-1.1: Read PUSH_PATCHES_TO_ORIGIN on every controlled update.
		forwarding := pushPatchesEnabled(deps.GetVariable, slug)

		actorID := "unknown"
		if actor != nil && actor.UserID != "" {
			actorID = actor.UserID
		}

		if !forwarding {
			// Reject mode (22-REQ-3): refuse the push with a message
			// naming the fork URL (userinfo stripped).
			safeURL := stripUserinfo(wsInfo.gitURL)
			msg := fmt.Sprintf("branch is synced from origin; push to %s instead", safeURL)
			logger.Info("push control: rejecting push to registered patch branch",
				"slug", slug,
				"branch", branchName,
				"user", actorID,
			)
			return fmt.Errorf("%s", msg)
		}

		// Forward mode (22-REQ-4): forward the push to the fork.
		// This is a placeholder for task 5; for now reject with a
		// forward-mode message so tests can distinguish modes.
		// Task 5 will implement the actual forwarding logic.

		// Deletes in forward mode are rejected (22-REQ-4 last paragraph).
		if upd.New == plumbing.ZeroHash {
			logger.Info("push control: rejecting delete of registered patch branch in forward mode",
				"slug", slug,
				"branch", branchName,
				"user", actorID,
			)
			return fmt.Errorf("branch is synced from origin; delete it on the fork instead")
		}

		// For now, forward mode is a placeholder that returns nil.
		// Task 5 will implement the actual forwarding.
		return nil
	}
}

// pushPatchesEnabled returns true only when the PUSH_PATCHES_TO_ORIGIN
// variable is exactly the case-sensitive string "true".
// Unset, any other value, or a lookup error means disabled.
func pushPatchesEnabled(getVariable GetVariableFunc, slug string) bool {
	if getVariable == nil {
		return false
	}
	val, err := getVariable("workspace", slug, "PUSH_PATCHES_TO_ORIGIN")
	if err != nil {
		return false
	}
	return val == "true"
}

// isRegisteredPatchBranch checks whether the given branch name is a registered
// patch of the workspace (status != 'deleted').
func isRegisteredPatchBranch(db *sql.DB, slug, branchName string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = ? AND branch_name = ? AND status != 'deleted'`,
		slug, branchName,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// workspaceInfo holds the workspace metadata needed for push control decisions.
type workspaceInfo struct {
	mode              string
	integrationBranch string
	gitURL            string
}

// loadWorkspaceInfo queries the workspace's mode, integration branch and git URL.
func loadWorkspaceInfo(db *sql.DB, slug string) (*workspaceInfo, error) {
	var mode string
	var integrationBranch sql.NullString
	var gitURL string
	err := db.QueryRow(
		`SELECT workspace_mode, integration_branch, git_url FROM workspaces WHERE slug = ?`,
		slug,
	).Scan(&mode, &integrationBranch, &gitURL)
	if err != nil {
		return nil, err
	}
	ib := ""
	if integrationBranch.Valid {
		ib = integrationBranch.String
	}
	return &workspaceInfo{
		mode:              mode,
		integrationBranch: ib,
		gitURL:            gitURL,
	}, nil
}

// stripUserinfo removes the userinfo (user:password@) portion from a URL string.
// If the URL cannot be parsed, it is returned as-is.
func stripUserinfo(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.User = nil
	return u.String()
}

// zeroHashStr is the zero hash as a string for comparisons.
var zeroHashStr = plumbing.ZeroHash.String()
