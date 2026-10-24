package carrypatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// Resolution method constants. These mirror the workspace package constants
// but are defined here to avoid importing workspace from carrypatch.
const (
	resolutionLocal          = "local"
	resolutionOriginTracking = "origin_tracking"
	resolutionOriginFetch    = "origin_fetch"
)

// SingleBranchFetchFunc fetches a single branch from the fork's origin remote
// into the trunk repository. Used by the branch resolver when
// PATCH_BRANCH_SOURCE is "origin" and neither the local nor tracking ref exists.
type SingleBranchFetchFunc func(ctx context.Context, repoPath, branch string, auth transport.AuthMethod) error

// ParsePatchBranchSource reads the PATCH_BRANCH_SOURCE variable and returns
// "origin" if the exact string "origin" is set, or "hub" for any other value,
// an unset variable, or a lookup error. This is the shared helper that both
// sync_handlers.go and the branch resolver use.
//
// Only the exact, case-sensitive string "origin" selects origin mode.
// Everything else means hub.
func ParsePatchBranchSource(getVariable GetVariableFunc, slug string) string {
	if getVariable == nil {
		return "hub"
	}
	val, _ := getVariable("workspace", slug, "PATCH_BRANCH_SOURCE")
	if val == "origin" {
		return "origin"
	}
	return "hub"
}

// NewBranchResolverHook returns the BranchResolverFunc that resolves
// a branch name in a workspace trunk.
//
// PATCH_BRANCH_SOURCE is read once per request: when the context
// carries a wsaccess request scope, the first resolution in that request reads
// the variable and every later resolution in the same request (the remaining
// elements of a batch) reuses that value. Without a request scope each call
// reads the variable itself.
//
// Parameters:
//   - newRunner: factory for creating a GitRunner for a repo path
//   - workspaceRoot: the root directory containing workspace directories
//   - getVariable: reads workspace settings (PATCH_BRANCH_SOURCE)
//   - resolveAuth: resolves origin credentials for fork fetches
//   - fetch: single-branch fetch function for origin mode
func NewBranchResolverHook(
	newRunner func(string) (GitRunner, error),
	workspaceRoot string,
	getVariable GetVariableFunc,
	resolveAuth ResolveAuthFunc,
	fetch SingleBranchFetchFunc,
) BranchResolverFunc {
	return newBranchResolverHookWithLockFunc(
		newRunner, workspaceRoot, getVariable, resolveAuth, fetch,
		host.TryLock,
	)
}

// newBranchResolverHookWithLockFunc is a test seam that allows injecting a
// custom lock function. The lockFunc has the same signature as host.TryLock.
func newBranchResolverHookWithLockFunc(
	newRunner func(string) (GitRunner, error),
	workspaceRoot string,
	getVariable GetVariableFunc,
	resolveAuth ResolveAuthFunc,
	fetch SingleBranchFetchFunc,
	lockFunc func(slug string) (unlock func(), ok bool),
) func(ctx context.Context, slug, branch string) (string, error) {
	return func(ctx context.Context, slug, branch string) (string, error) {
		repoPath := filepath.Join(workspaceRoot, slug, "trunk")
		runner, err := newRunner(repoPath)
		if err != nil {
			return "", fmt.Errorf("branch resolver: open runner for %s: %w", slug, err)
		}

		// Read PATCH_BRANCH_SOURCE once per request. The memo
		// lives in the request scope the handler installs on the context, so
		// every element of a batch resolves in the same mode.
		mode := requestScoped(ctx, "PATCH_BRANCH_SOURCE/"+slug, func() string {
			return ParsePatchBranchSource(getVariable, slug)
		})

		// Step 1: Check refs/heads/<name>^{commit}.
		method, err := resolveStep1(ctx, runner, branch)
		if err == nil {
			return method, nil
		}

		// Step 1 failed — we need to write, so take the lock.
		// Take the workspace lock before writing anything.
		unlock, ok := lockFunc(slug)
		if !ok {
			return "", newBranchResolveError(BranchResolveKindWorkspaceBusy,
				fmt.Errorf("workspace %s is locked", slug))
		}
		defer unlock()

		// Re-check step 1 under the lock: another request may have created
		// the branch meanwhile.
		method, err = resolveStep1(ctx, runner, branch)
		if err == nil {
			return method, nil
		}

		// Step 2: Check refs/remotes/origin/<name>^{commit}.
		trackingSHA, err := revParseRef(ctx, runner, "refs/remotes/origin/"+branch)
		if err == nil {
			// Tracking ref exists. Create local branch via CAS update-ref.
			return createLocalBranch(ctx, runner, slug, branch, trackingSHA, resolutionOriginTracking)
		}

		// Step 3: Only in origin mode, fetch the branch from the fork.
		if mode == "origin" {
			return resolveOriginFetch(ctx, runner, slug, branch, repoPath, resolveAuth, fetch)
		}

		// Neither local nor tracking ref found, and hub mode — not found.
		return "", newBranchResolveError(BranchResolveKindNotFound,
			fmt.Errorf("branch %q not found in workspace %s", branch, slug))
	}
}

// resolveStep1 checks if refs/heads/<name>^{commit} resolves.
func resolveStep1(ctx context.Context, runner GitRunner, branch string) (string, error) {
	_, err := revParseRef(ctx, runner, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	return resolutionLocal, nil
}

// revParseRef runs git rev-parse --verify --end-of-options <ref>^{commit}.
// Returns the SHA on success, or an error if the ref does not exist.
func revParseRef(ctx context.Context, runner GitRunner, ref string) (string, error) {
	sha, err := runner.Run(ctx, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return "", fmt.Errorf("rev-parse returned empty for %s", ref)
	}
	return sha, nil
}

// createLocalBranch creates refs/heads/<name> at sha using a compare-and-swap
// update-ref. If the CAS fails because the ref now exists, it re-runs step 1
// and reports "local". Any other failure is an internal error.
func createLocalBranch(ctx context.Context, runner GitRunner, slug, branch, sha, method string) (string, error) {
	// Use update-ref with the all-zero old value for CAS.
	_, err := runner.Run(ctx, "update-ref", "refs/heads/"+branch, sha, zeroSHA)
	if err != nil {
		// If CAS fails because the ref now exists, report local.
		existingSHA, checkErr := revParseRef(ctx, runner, "refs/heads/"+branch)
		if checkErr == nil && existingSHA != "" {
			return resolutionLocal, nil
		}
		// Any other update-ref failure is an internal error.
		slog.Error("branch resolver: update-ref failed",
			"slug", slug,
			"branch", branch,
			"error", err.Error(),
		)
		return "", fmt.Errorf("branch resolver: update-ref refs/heads/%s in %s: %w", branch, slug, err)
	}
	return method, nil
}

// ErrBranchNotOnOrigin is a sentinel error returned by SingleBranchFetchFunc
// when the fork does not have the requested branch. The resolver matches it
// with errors.Is, so the fetch function may return it directly or wrapped; it
// is never recognised from error text. The match classifies the failure as
// "branch not found" rather than "origin fetch failed".
var ErrBranchNotOnOrigin = errors.New("branch not found on origin")

// resolveOriginFetch fetches a single branch from the fork and, if the
// tracking ref now exists, creates the local branch. This is step 3 of the
// resolution order, only called in origin mode.
//
// The caller must hold the workspace lock before calling this function.
func resolveOriginFetch(ctx context.Context, runner GitRunner, slug, branch, repoPath string, resolveAuth ResolveAuthFunc, fetch SingleBranchFetchFunc) (string, error) {
	// Resolve origin credentials first.
	auth, err := resolveAuth(slug)
	if err != nil {
		slog.Error("branch resolver: failed to resolve origin credentials",
			"slug", slug,
			"branch", branch,
			"error", err.Error(),
		)
		return "", newBranchResolveError(BranchResolveKindOriginCredentials, err)
	}

	// Fetch the single branch from the fork. NoErrAlreadyUpToDate is success.
	err = fetch(ctx, repoPath, branch, auth)
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		err = nil
	}
	if err != nil {
		// Classify "branch not found on origin" from the sentinel error,
		// never from error text.
		if errors.Is(err, ErrBranchNotOnOrigin) {
			return "", newBranchResolveError(BranchResolveKindNotFound,
				fmt.Errorf("branch %q not found on origin for workspace %s", branch, slug))
		}
		// Any other fetch failure is an origin fetch failure.
		slog.Error("branch resolver: origin fetch failed",
			"slug", slug,
			"branch", branch,
			"error", err.Error(),
		)
		return "", newBranchResolveError(BranchResolveKindOriginFetchFailed, err)
	}

	// Fetch succeeded (including NoErrAlreadyUpToDate). Re-check the
	// tracking ref.
	trackingSHA, err := revParseRef(ctx, runner, "refs/remotes/origin/"+branch)
	if err != nil {
		// Tracking ref still absent after a successful fetch → not found.
		return "", newBranchResolveError(BranchResolveKindNotFound,
			fmt.Errorf("branch %q not found after fetch for workspace %s", branch, slug))
	}

	// Create the local branch at the tracking tip.
	return createLocalBranch(ctx, runner, slug, branch, trackingSHA, resolutionOriginFetch)
}
