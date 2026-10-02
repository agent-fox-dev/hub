package carrypatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ===========================================================================
// TS-21-3 (integration): In origin mode a branch pushed to the fork after
// the clone is fetched and given a local ref.
// Verifies: 21-REQ-1.3, 21-REQ-3.1
// ===========================================================================

func TestTS21_3_OriginFetchBranchPushedAfterClone(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "ts21-3"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Record tracking refs and tags before.
	trackingRefsBefore := listTrackingRefs(t, trunkDir)
	tagsBefore := listTags(t, trunkDir)

	// Push a branch to the fork AFTER the clone, so the trunk has neither
	// refs/heads/late nor refs/remotes/origin/late.
	lateSHA := createBranchOnBare(t, forkDir, "late", "late.txt", "late content", "add late")

	// Verify neither ref exists in the trunk.
	if revParse(t, trunkDir, "refs/heads/late") != "" {
		t.Fatal("refs/heads/late should not exist before the test")
	}
	if revParse(t, trunkDir, "refs/remotes/origin/late") != "" {
		t.Fatal("refs/remotes/origin/late should not exist before the test")
	}

	getVar := func(_, _, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		return "", fmt.Errorf("not found")
	}

	// Use the real single-branch fetch function and a nil credential resolver
	// (local file:// protocol needs no auth).
	hook := NewBranchResolverHook(
		NewGitRunnerFactory(),
		workspaceRoot,
		getVar,
		func(_ string) (transport.AuthMethod, error) { return nil, nil },
		DefaultSingleBranchFetch(),
	)

	method, err := hook(context.Background(), slug, "late")
	if err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if method != "origin_fetch" {
		t.Errorf("method = %q; want %q", method, "origin_fetch")
	}

	// Verify refs/remotes/origin/late exists and equals the fork tip.
	trackingRef := revParse(t, trunkDir, "refs/remotes/origin/late")
	if trackingRef != lateSHA {
		t.Errorf("refs/remotes/origin/late = %s; want %s", trackingRef, lateSHA)
	}

	// Verify refs/heads/late equals the fork tip.
	localRef := revParse(t, trunkDir, "refs/heads/late")
	if localRef != lateSHA {
		t.Errorf("refs/heads/late = %s; want %s", localRef, lateSHA)
	}

	// Verify no other fork branch gained a tracking ref.
	trackingRefsAfter := listTrackingRefs(t, trunkDir)
	for ref := range trackingRefsAfter {
		if ref == "refs/remotes/origin/late" {
			continue // expected new ref
		}
		if _, existed := trackingRefsBefore[ref]; !existed {
			t.Errorf("unexpected new tracking ref: %s", ref)
		}
	}

	// Verify no tag was fetched.
	tagsAfter := listTags(t, trunkDir)
	if len(tagsAfter) != len(tagsBefore) {
		t.Errorf("tags changed: before=%v after=%v", tagsBefore, tagsAfter)
	}
}

// ===========================================================================
// TS-21-12 (unit): The fetch function is called with one refspec, no tags,
// no prune and credentials from ResolveCloneAuth.
// Verifies: 21-REQ-3.1
// ===========================================================================

func TestTS21_12_FetchCalledWithCorrectParams(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "ts21-12"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)
	_ = forkDir
	_ = trunkDir

	getVar := func(_, _, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		return "", fmt.Errorf("not found")
	}

	type fetchRecord struct {
		repoPath string
		branch   string
		auth     transport.AuthMethod
	}
	var recorded []fetchRecord

	// A known auth method to verify it's passed through.
	expectedAuth := &testAuthMethod{token: "test-token-123"}

	var credCalls int32

	recordingFetch := func(ctx context.Context, repoPath, branch string, auth transport.AuthMethod) error {
		recorded = append(recorded, fetchRecord{
			repoPath: repoPath,
			branch:   branch,
			auth:     auth,
		})
		// Return an error so the branch is "not found" (we're testing the call params, not the result).
		return ErrBranchNotOnOrigin
	}

	credResolver := func(s string) (transport.AuthMethod, error) {
		atomic.AddInt32(&credCalls, 1)
		if s != slug {
			t.Errorf("credential resolver called with slug %q; want %q", s, slug)
		}
		return expectedAuth, nil
	}

	hook := NewBranchResolverHook(
		NewGitRunnerFactory(),
		workspaceRoot,
		getVar,
		credResolver,
		recordingFetch,
	)

	// The branch doesn't exist anywhere, so the resolver will reach step 3.
	_, err := hook(context.Background(), slug, "feat")
	if err == nil {
		t.Fatal("expected error for missing branch, got nil")
	}

	// Verify the fetch stub was called exactly once.
	if len(recorded) != 1 {
		t.Fatalf("fetch calls = %d; want 1", len(recorded))
	}

	rec := recorded[0]

	// Verify the repo path is the trunk path.
	expectedPath := filepath.Join(workspaceRoot, slug, "trunk")
	if rec.repoPath != expectedPath {
		t.Errorf("fetch repoPath = %q; want %q", rec.repoPath, expectedPath)
	}

	// Verify the branch name is passed.
	if rec.branch != "feat" {
		t.Errorf("fetch branch = %q; want %q", rec.branch, "feat")
	}

	// Verify the auth value is the one returned by the credential resolver.
	if rec.auth != expectedAuth {
		t.Errorf("fetch auth = %v; want %v", rec.auth, expectedAuth)
	}

	// Verify the credential resolver was called once.
	if atomic.LoadInt32(&credCalls) != 1 {
		t.Errorf("credential resolver calls = %d; want 1", credCalls)
	}
}

// ===========================================================================
// TS-21-13 (integration): An already-up-to-date fetch is success and the
// tracking-ref check continues.
// Verifies: 21-REQ-3.2
// ===========================================================================

func TestTS21_13_AlreadyUpToDateFetchIsSuccess(t *testing.T) {
	// Case A: tracking ref present after fetch returns NoErrAlreadyUpToDate
	// → method is origin_fetch and local ref is created.
	t.Run("tracking_ref_present", func(t *testing.T) {
		workspaceRoot := t.TempDir()
		slug := "ts21-13a"

		forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

		// Push a branch to the fork after the clone.
		lateSHA := createBranchOnBare(t, forkDir, "late", "late.txt", "late content", "add late")

		getVar := func(_, _, key string) (string, error) {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			return "", fmt.Errorf("not found")
		}

		// A fetch stub that returns NoErrAlreadyUpToDate but first manually
		// creates the tracking ref (simulating a previous fetch).
		fetchStub := func(ctx context.Context, repoPath, branch string, auth transport.AuthMethod) error {
			// Manually create the tracking ref to simulate a previous fetch.
			runGitCmd(t, trunkDir, "fetch", forkDir, "+refs/heads/late:refs/remotes/origin/late")
			return git.NoErrAlreadyUpToDate
		}

		hook := NewBranchResolverHook(
			NewGitRunnerFactory(),
			workspaceRoot,
			getVar,
			func(_ string) (transport.AuthMethod, error) { return nil, nil },
			fetchStub,
		)

		method, err := hook(context.Background(), slug, "late")
		if err != nil {
			t.Fatalf("hook returned error: %v", err)
		}
		if method != "origin_fetch" {
			t.Errorf("method = %q; want %q", method, "origin_fetch")
		}

		// Verify refs/heads/late was created at the tracking tip.
		localRef := revParse(t, trunkDir, "refs/heads/late")
		if localRef != lateSHA {
			t.Errorf("refs/heads/late = %s; want %s", localRef, lateSHA)
		}
	})

	// Case B: tracking ref absent after fetch returns NoErrAlreadyUpToDate
	// → result is branch not found, not a fetch failure.
	t.Run("tracking_ref_absent", func(t *testing.T) {
		workspaceRoot := t.TempDir()
		slug := "ts21-13b"

		_, _, _ = setupForkAndTrunk(t, workspaceRoot, slug)

		getVar := func(_, _, key string) (string, error) {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			return "", fmt.Errorf("not found")
		}

		// A fetch stub that returns NoErrAlreadyUpToDate without creating
		// any tracking ref.
		fetchStub := func(ctx context.Context, repoPath, branch string, auth transport.AuthMethod) error {
			return git.NoErrAlreadyUpToDate
		}

		hook := NewBranchResolverHook(
			NewGitRunnerFactory(),
			workspaceRoot,
			getVar,
			func(_ string) (transport.AuthMethod, error) { return nil, nil },
			fetchStub,
		)

		_, err := hook(context.Background(), slug, "nonexistent")
		if err == nil {
			t.Fatal("expected error for missing branch, got nil")
		}
		if !isBranchResolveKind(err, "not_found") {
			t.Errorf("expected not_found error, got: %v", err)
		}
	})
}

// ===========================================================================
// TS-21-25 (integration): The workspace lock is taken before any write on
// the create and fetch paths and held until resolution finishes.
// Verifies: 21-REQ-7.1
// ===========================================================================

func TestTS21_25_LockTakenBeforeWriteOnFetchPath(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "ts21-25"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a tracking-only branch (for the tracking path).
	createBranchOnBare(t, forkDir, "tracking-branch", "t.txt", "t content", "add t")
	runGitCmd(t, trunkDir, "fetch", "origin")

	// Create a fork-only branch (for the fetch path).
	forkOnlySHA := createBranchOnBare(t, forkDir, "fork-only", "f.txt", "f content", "add f")
	_ = forkOnlySHA

	getVar := func(_, _, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		return "", fmt.Errorf("not found")
	}

	var lockCalls int32
	var lockHeld int32 // 1 when locked, 0 when not
	var fetchObservedLocked bool
	var updateRefObservedLocked bool

	lockFunc := func(s string) (func(), bool) {
		atomic.AddInt32(&lockCalls, 1)
		if s != slug {
			t.Errorf("lock called with slug %q; want %q", s, slug)
		}
		atomic.StoreInt32(&lockHeld, 1)
		return func() {
			atomic.StoreInt32(&lockHeld, 0)
		}, true
	}

	// A fetch stub that records whether the lock is held.
	fetchStub := func(ctx context.Context, repoPath, branch string, auth transport.AuthMethod) error {
		if atomic.LoadInt32(&lockHeld) == 1 {
			fetchObservedLocked = true
		}
		// Simulate a successful fetch by creating the tracking ref.
		runGitCmd(t, trunkDir, "fetch", forkDir, "+refs/heads/fork-only:refs/remotes/origin/fork-only")
		return nil
	}

	// Wrap the runner factory to observe update-ref calls.
	realFactory := NewGitRunnerFactory()
	wrappedFactory := func(repoPath string) (GitRunner, error) {
		real, err := realFactory(repoPath)
		if err != nil {
			return nil, err
		}
		return &lockObservingRunner{
			inner: real,
			onUpdateRef: func() {
				if atomic.LoadInt32(&lockHeld) == 1 {
					updateRefObservedLocked = true
				}
			},
		}, nil
	}

	// Test the tracking-only branch first.
	t.Run("tracking_branch", func(t *testing.T) {
		atomic.StoreInt32(&lockCalls, 0)
		updateRefObservedLocked = false

		hook := newBranchResolverHookWithLockFunc(
			wrappedFactory,
			workspaceRoot,
			getVar,
			func(_ string) (transport.AuthMethod, error) { return nil, nil },
			fetchStub,
			lockFunc,
		)

		method, err := hook(context.Background(), slug, "tracking-branch")
		if err != nil {
			t.Fatalf("hook returned error: %v", err)
		}
		if method != "origin_tracking" {
			t.Errorf("method = %q; want %q", method, "origin_tracking")
		}

		if atomic.LoadInt32(&lockCalls) != 1 {
			t.Errorf("lock calls = %d; want 1", atomic.LoadInt32(&lockCalls))
		}
		if !updateRefObservedLocked {
			t.Error("update-ref was not observed while lock was held")
		}
		// Lock should be released after hook returns.
		if atomic.LoadInt32(&lockHeld) != 0 {
			t.Error("lock is still held after hook returned")
		}
	})

	// Test the fork-only branch.
	t.Run("fork_only_branch", func(t *testing.T) {
		atomic.StoreInt32(&lockCalls, 0)
		fetchObservedLocked = false
		updateRefObservedLocked = false

		hook := newBranchResolverHookWithLockFunc(
			wrappedFactory,
			workspaceRoot,
			getVar,
			func(_ string) (transport.AuthMethod, error) { return nil, nil },
			fetchStub,
			lockFunc,
		)

		method, err := hook(context.Background(), slug, "fork-only")
		if err != nil {
			t.Fatalf("hook returned error: %v", err)
		}
		if method != "origin_fetch" {
			t.Errorf("method = %q; want %q", method, "origin_fetch")
		}

		if atomic.LoadInt32(&lockCalls) != 1 {
			t.Errorf("lock calls = %d; want 1", atomic.LoadInt32(&lockCalls))
		}
		if !fetchObservedLocked {
			t.Error("fetch was not observed while lock was held")
		}
		if !updateRefObservedLocked {
			t.Error("update-ref was not observed while lock was held")
		}
		// Lock should be released after hook returns.
		if atomic.LoadInt32(&lockHeld) != 0 {
			t.Error("lock is still held after hook returned")
		}
	})
}

// ===========================================================================
// Helper: lockObservingRunner wraps a GitRunner and calls a hook on update-ref.
// ===========================================================================

type lockObservingRunner struct {
	inner       GitRunner
	onUpdateRef func()
}

func (r *lockObservingRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) >= 1 && args[0] == "update-ref" {
		if r.onUpdateRef != nil {
			r.onUpdateRef()
		}
	}
	return r.inner.Run(ctx, args...)
}

func (r *lockObservingRunner) CherryPick(ctx context.Context, commitSHA string) error {
	return r.inner.CherryPick(ctx, commitSHA)
}
func (r *lockObservingRunner) MergeNoFF(ctx context.Context, ref, message string) error {
	return r.inner.MergeNoFF(ctx, ref, message)
}
func (r *lockObservingRunner) MergeTree(ctx context.Context, base, head string) (string, error) {
	return r.inner.MergeTree(ctx, base, head)
}
func (r *lockObservingRunner) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	return r.inner.IsAncestor(ctx, ancestor, descendant)
}
func (r *lockObservingRunner) Cherry(ctx context.Context, upstream, head string) ([]string, []string, error) {
	return r.inner.Cherry(ctx, upstream, head)
}
func (r *lockObservingRunner) HardReset(ctx context.Context, ref string) error {
	return r.inner.HardReset(ctx, ref)
}
func (r *lockObservingRunner) WorktreeAdd(ctx context.Context, path, commit string) error {
	return r.inner.WorktreeAdd(ctx, path, commit)
}
func (r *lockObservingRunner) WorktreeRemove(ctx context.Context, path string) error {
	return r.inner.WorktreeRemove(ctx, path)
}
func (r *lockObservingRunner) WorktreePrune(ctx context.Context) error {
	return r.inner.WorktreePrune(ctx)
}
func (r *lockObservingRunner) UpdateRef(ctx context.Context, ref, sha string) error {
	return r.inner.UpdateRef(ctx, ref, sha)
}

var _ GitRunner = (*lockObservingRunner)(nil)

// ===========================================================================
// Helper: testAuthMethod is a simple transport.AuthMethod for testing.
// ===========================================================================

type testAuthMethod struct {
	token string
}

func (a *testAuthMethod) Name() string { return "test" }
func (a *testAuthMethod) String() string {
	return fmt.Sprintf("test-auth(%s)", a.token)
}

// ===========================================================================
// Helper: list tracking refs and tags in a repo.
// ===========================================================================

func listTrackingRefs(t *testing.T, dir string) map[string]string {
	t.Helper()
	cmd := exec.Command("git", "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes/origin/")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git for-each-ref failed: %v\n%s", err, out)
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 {
			refs[parts[0]] = parts[1]
		}
	}
	return refs
}

func listTags(t *testing.T, dir string) map[string]string {
	t.Helper()
	cmd := exec.Command("git", "for-each-ref", "--format=%(refname) %(objectname)", "refs/tags/")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git for-each-ref failed: %v\n%s", err, out)
	}
	tags := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 2 {
			tags[parts[0]] = parts[1]
		}
	}
	return tags
}

// ===========================================================================
// Verify go-git NoMatchingRefSpecError behavior with a real bare fork.
// This is a prerequisite for the implementation — the spec marks this as
// UNVERIFIED.
// ===========================================================================

func TestGoGitNoMatchingRefSpecError(t *testing.T) {
	// Create a bare fork with only a main branch.
	forkDir := filepath.Join(t.TempDir(), "fork.git")
	initBareRepo(t, forkDir)

	tmpClone := filepath.Join(t.TempDir(), "init-clone")
	cloneRepo(t, forkDir, tmpClone)
	commitFile(t, tmpClone, "README.md", "initial", "initial commit")
	runGitCmd(t, tmpClone, "push", "origin", "main")

	// Clone the fork.
	trunkDir := filepath.Join(t.TempDir(), "trunk")
	cloneRepo(t, forkDir, trunkDir)

	// Try to fetch a branch that doesn't exist on the fork.
	repo, err := git.PlainOpen(trunkDir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	remote, err := repo.Remote("origin")
	if err != nil {
		t.Fatalf("Remote: %v", err)
	}

	fetchErr := remote.FetchContext(context.Background(), &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec("+refs/heads/nonexistent:refs/remotes/origin/nonexistent")},
		Tags:       git.NoTags,
	})

	if fetchErr == nil {
		t.Fatal("expected error for nonexistent branch, got nil")
	}

	// Verify it's a NoMatchingRefSpecError.
	var noMatch git.NoMatchingRefSpecError
	if !errors.As(fetchErr, &noMatch) {
		t.Errorf("expected NoMatchingRefSpecError, got %T: %v", fetchErr, fetchErr)
	}

	t.Logf("go-git error for missing branch: %T: %v", fetchErr, fetchErr)
}
