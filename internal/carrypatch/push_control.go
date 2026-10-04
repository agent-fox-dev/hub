package carrypatch

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitserver"
)

// PreReceiveHookDeps holds the dependencies for the carry-patch pre-receive hook.
type PreReceiveHookDeps struct {
	GetVariable   GetVariableFunc
	ResolveAuth   ResolveAuthFunc
	WorkspaceRoot string
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

// PushContextFunc is a seam for testing: it wraps repo.PushContext.
type PushContextFunc func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error

// defaultPushContext calls repo.PushContext directly.
func defaultPushContext(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
	return repo.PushContext(ctx, opts)
}

// newPreReceiveHookInternalWithPush is the shared implementation with a push seam.
func newPreReceiveHookInternalWithPush(deps PreReceiveHookDeps, logger *slog.Logger, pushFn PushContextFunc) gitserver.PreReceiveHookFunc {
	return newPreReceiveHookCore(deps, logger, pushFn)
}

// newPreReceiveHookInternal is the shared implementation.
func newPreReceiveHookInternal(deps PreReceiveHookDeps, logger *slog.Logger) gitserver.PreReceiveHookFunc {
	return newPreReceiveHookCore(deps, logger, defaultPushContext)
}

// newPreReceiveHookCore is the core implementation.
func newPreReceiveHookCore(deps PreReceiveHookDeps, logger *slog.Logger, pushFn PushContextFunc) gitserver.PreReceiveHookFunc {
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

		// Forward mode (22-REQ-4): forward the push to the fork
		// before the hub writes its ref.

		// 22-REQ-4.7: Deletes in forward mode are rejected without
		// contacting origin.
		if upd.New == plumbing.ZeroHash {
			logger.Info("push control: rejecting delete of registered patch branch in forward mode",
				"slug", slug,
				"branch", branchName,
				"user", actorID,
			)
			return fmt.Errorf("branch is synced from origin; delete it on the fork instead")
		}

		// 22-REQ-4.3: Resolve origin credentials.
		if deps.ResolveAuth == nil {
			// 22-REQ-4.4: No resolver configured.
			return fmt.Errorf("failed to resolve origin credentials")
		}
		auth, authErr := deps.ResolveAuth(slug)
		if authErr != nil {
			// 22-REQ-4.4: Credential resolution failure rejects
			// without contacting origin.
			return fmt.Errorf("failed to resolve origin credentials")
		}

		// 22-REQ-4.1, 22-REQ-4.2: Forward the create or update to
		// the fork via a temporary ref.
		if fwdErr := forwardToOrigin(ctx, deps.WorkspaceRoot, slug, branchName, upd.New, auth, logger, pushFn); fwdErr != nil {
			// 22-REQ-4.5, 22-REQ-8.2: Forward failure rejects the
			// update. Never accept silently.
			safeErr := stripUserinfo(fwdErr.Error())
			logger.Warn("push control: forward to origin failed",
				"slug", slug,
				"branch", branchName,
				"user", actorID,
				"error", safeErr,
			)
			return fmt.Errorf("origin rejected push: %s", safeErr)
		}

		// Forward succeeded — let the hub write its ref.
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

// forwardTimeout is the maximum time allowed for a forward push to origin.
const forwardTimeout = 120 * time.Second

// forwardToOrigin pushes the commit identified by newHash to the fork's
// refs/heads/<branchName> via the trunk's origin remote. It uses a temporary
// ref refs/hub/forward/<branchName> to make the unreferenced commit
// resolvable by go-git's push, and removes the temporary ref on both
// success and failure.
func forwardToOrigin(
	ctx context.Context,
	workspaceRoot, slug, branchName string,
	newHash plumbing.Hash,
	auth transport.AuthMethod,
	logger *slog.Logger,
	pushFn PushContextFunc,
) error {
	trunkPath := filepath.Join(workspaceRoot, slug, "trunk")
	repo, err := git.PlainOpen(trunkPath)
	if err != nil {
		return fmt.Errorf("open trunk: %w", err)
	}

	// 22-REQ-4.2: Create a temporary ref so the commit is resolvable.
	tempRefName := plumbing.ReferenceName("refs/hub/forward/" + branchName)
	tempRef := plumbing.NewHashReference(tempRefName, newHash)
	if err := repo.Storer.SetReference(tempRef); err != nil {
		return fmt.Errorf("create temp ref: %w", err)
	}
	// Always clean up the temporary ref.
	defer func() {
		if rmErr := repo.Storer.RemoveReference(tempRefName); rmErr != nil {
			logger.Warn("push control: failed to remove temp ref",
				"ref", string(tempRefName),
				"error", rmErr.Error(),
			)
		}
	}()

	// 22-REQ-4.3: Bound the forward by a 120 second timeout.
	pushCtx, cancel := context.WithTimeout(ctx, forwardTimeout)
	defer cancel()

	// 22-REQ-4.1: Push with Force: false and no '+' in the refspec.
	refspec := config.RefSpec(fmt.Sprintf("%s:refs/heads/%s", tempRefName, branchName))
	opts := &git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{refspec},
		Auth:       auth,
		Force:      false,
	}

	pushErr := pushFn(pushCtx, repo, opts)
	if pushErr != nil && pushErr != git.NoErrAlreadyUpToDate {
		return pushErr
	}

	return nil
}

// zeroHashStr is the zero hash as a string for comparisons.
var zeroHashStr = plumbing.ZeroHash.String()
