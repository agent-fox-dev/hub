package carrypatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wslock"
)

// ===========================================================================
// Recovery test helpers
// ===========================================================================

type fetchCall struct {
	RepoPath string
	Branch   string
	Auth     transport.AuthMethod
}

type stubFetchRecorder struct {
	calls []fetchCall
	err   error
	// realFetch, when set, is called after recording the call.
	realFetch SingleBranchFetchFunc
}

func (s *stubFetchRecorder) fetch(ctx context.Context, repoPath, branch string, auth transport.AuthMethod) error {
	s.calls = append(s.calls, fetchCall{RepoPath: repoPath, Branch: branch, Auth: auth})
	if s.err != nil {
		return s.err
	}
	if s.realFetch != nil {
		return s.realFetch(ctx, repoPath, branch, auth)
	}
	return nil
}

// newTestRecoveryService creates a RecoveryService for testing.
func newTestRecoveryService(
	workspaceRoot string,
	getVariable GetVariableFunc,
	resolveAuth ResolveAuthFunc,
	fetch SingleBranchFetchFunc,
) *RecoveryService {
	return &RecoveryService{
		NewGitRunner:  NewGitRunnerFactory(),
		WorkspaceRoot: workspaceRoot,
		GetVariable:   getVariable,
		ResolveAuth:   resolveAuth,
		Fetch:         fetch,
		LockFunc:      wslock.TryLock,
	}
}

// snapshotRefs captures the current state of key refs for a branch.
func snapshotRefs(t *testing.T, trunkDir, branch string) map[string]string {
	t.Helper()
	refs := map[string]string{}
	refs["heads"] = revParse(t, trunkDir, "refs/heads/"+branch)
	refs["remotes"] = revParse(t, trunkDir, "refs/remotes/origin/"+branch)
	refs["backup"] = revParse(t, trunkDir, "refs/hub/replaced/"+branch)
	return refs
}

// runGitMayFail executes a git command and returns stdout and error.
func runGitMayFail(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ===========================================================================
// TS-23-15: Reset on a busy workspace answers 409 workspace_busy
// ===========================================================================

func TestRecovery_BusyWorkspace_TS2315(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "busy-ws"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a patch branch on the fork and trunk.
	branch := "feature/busy"
	createBranchOnBare(t, forkDir, branch, "busy.txt", "busy", "busy commit")

	// Create the branch locally too.
	runGitCmd(t, trunkDir, "fetch", "origin", branch)
	runGitCmd(t, trunkDir, "branch", branch, "refs/remotes/origin/"+branch)

	sf := &stubFetchRecorder{}
	svc := newTestRecoveryService(workspaceRoot, nil, nil, sf.fetch)

	// Hold the lock.
	unlock, ok := wslock.TryLock(slug)
	if !ok {
		t.Fatal("failed to acquire lock for test setup")
	}
	defer unlock()

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p1",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}

	_, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
	if err == nil {
		t.Fatal("expected error for busy workspace")
	}

	var resetErr *RecoveryError
	if !errors.As(err, &resetErr) {
		t.Fatalf("expected RecoveryError, got %T: %v", err, err)
	}
	if resetErr.Kind != RecoveryErrBusy {
		t.Errorf("kind = %q; want %q", resetErr.Kind, RecoveryErrBusy)
	}

	// Fetch should not have been called.
	if len(sf.calls) != 0 {
		t.Errorf("fetch called %d times; want 0", len(sf.calls))
	}

	// Refs should be unchanged.
	localSHA := revParse(t, trunkDir, "refs/heads/"+branch)
	if localSHA == "" {
		t.Error("expected local branch to exist")
	}
}

// ===========================================================================
// TS-23-17: Reset fetches the single branch with resolved origin credentials
// ===========================================================================

func TestRecovery_FetchesSingleBranch_TS2317(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "fetch-test"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a branch on the fork.
	branch := "feature/fetch"
	createBranchOnBare(t, forkDir, branch, "fetch.txt", "fetch content", "fork commit")

	// Also create the branch locally on the trunk at the same commit.
	runGitCmd(t, trunkDir, "fetch", "origin", branch)
	runGitCmd(t, trunkDir, "branch", branch, "refs/remotes/origin/"+branch)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p1",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}

	// Test both modes.
	for _, mode := range []string{"hub", "origin"} {
		t.Run("mode_"+mode, func(t *testing.T) {
			sf := &stubFetchRecorder{
				realFetch: DefaultSingleBranchFetch(),
			}

			getVar := func(scope, s, key string) (string, error) {
				if key == "PATCH_BRANCH_SOURCE" {
					return mode, nil
				}
				return "", nil
			}

			svc := newTestRecoveryService(workspaceRoot, getVar, nil, sf.fetch)

			_, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
			if err != nil {
				t.Fatalf("RunReset failed: %v", err)
			}

			// Verify fetch was called with the correct branch.
			if len(sf.calls) != 1 {
				t.Fatalf("fetch called %d times; want 1", len(sf.calls))
			}
			call := sf.calls[0]
			if call.Branch != branch {
				t.Errorf("fetch branch = %q; want %q", call.Branch, branch)
			}
			expectedRepoPath := filepath.Join(workspaceRoot, slug, "trunk")
			if call.RepoPath != expectedRepoPath {
				t.Errorf("fetch repoPath = %q; want %q", call.RepoPath, expectedRepoPath)
			}
		})
	}

	// Test credential resolution.
	t.Run("credentials", func(t *testing.T) {
		sf := &stubFetchRecorder{
			realFetch: DefaultSingleBranchFetch(),
		}

		resolveAuth := func(s string) (transport.AuthMethod, error) {
			return nil, nil // no credentials
		}

		svc := newTestRecoveryService(workspaceRoot, nil, resolveAuth, sf.fetch)

		_, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
		if err != nil {
			t.Fatalf("RunReset failed: %v", err)
		}

		if len(sf.calls) != 1 {
			t.Fatalf("fetch called %d times; want 1", len(sf.calls))
		}
		// Auth should be nil (no credentials).
		if sf.calls[0].Auth != nil {
			t.Errorf("fetch auth = %v; want nil", sf.calls[0].Auth)
		}
	})
}

// ===========================================================================
// TS-23-18: Credential and fetch failures answer classified errors
// ===========================================================================

func TestRecovery_CredentialAndFetchFailures_TS2318(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "fail-test"

	_, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a local branch and a backup ref.
	branch := "feature/fail"
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "fail.txt", "fail", "fail commit")
	localTip := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, localTip)
	runGitCmd(t, trunkDir, "checkout", "main")

	refsBefore := snapshotRefs(t, trunkDir, branch)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p1",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}

	t.Run("credential_failure", func(t *testing.T) {
		resolveAuth := func(s string) (transport.AuthMethod, error) {
			return nil, fmt.Errorf("secret-token-xyz")
		}
		sf := &stubFetchRecorder{}
		svc := newTestRecoveryService(workspaceRoot, nil, resolveAuth, sf.fetch)

		_, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
		if err == nil {
			t.Fatal("expected error for credential failure")
		}

		var resetErr *RecoveryError
		if !errors.As(err, &resetErr) {
			t.Fatalf("expected RecoveryError, got %T: %v", err, err)
		}
		if resetErr.Kind != RecoveryErrCredentialFailed {
			t.Errorf("kind = %q; want %q", resetErr.Kind, RecoveryErrCredentialFailed)
		}

		// Error message should not contain the secret.
		if strings.Contains(resetErr.Message, "secret-token-xyz") {
			t.Error("error message should not contain the secret")
		}

		// Fetch should not have been called.
		if len(sf.calls) != 0 {
			t.Errorf("fetch called %d times; want 0", len(sf.calls))
		}

		// All refs unchanged.
		refsAfter := snapshotRefs(t, trunkDir, branch)
		for k, v := range refsBefore {
			if refsAfter[k] != v {
				t.Errorf("ref %q changed: %q -> %q", k, v, refsAfter[k])
			}
		}
	})

	t.Run("fetch_failure", func(t *testing.T) {
		sf := &stubFetchRecorder{
			err: fmt.Errorf("dial tcp 10.0.0.1 refused"),
		}
		svc := newTestRecoveryService(workspaceRoot, nil, nil, sf.fetch)

		_, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
		if err == nil {
			t.Fatal("expected error for fetch failure")
		}

		var resetErr *RecoveryError
		if !errors.As(err, &resetErr) {
			t.Fatalf("expected RecoveryError, got %T: %v", err, err)
		}
		if resetErr.Kind != RecoveryErrFetchFailed {
			t.Errorf("kind = %q; want %q", resetErr.Kind, RecoveryErrFetchFailed)
		}

		// Error message should not contain the underlying error.
		if strings.Contains(resetErr.Message, "dial tcp") {
			t.Error("error message should not contain underlying error text")
		}

		// All refs unchanged.
		refsAfter := snapshotRefs(t, trunkDir, branch)
		for k, v := range refsBefore {
			if refsAfter[k] != v {
				t.Errorf("ref %q changed: %q -> %q", k, v, refsAfter[k])
			}
		}
	})
}

// ===========================================================================
// TS-23-19: A branch missing on the fork answers missing_on_origin
// ===========================================================================

func TestRecovery_MissingOnOrigin_TS2319(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "missing-origin"

	_, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a local branch that does NOT exist on the fork.
	branch := "feature/local-only"
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "local.txt", "local only", "local only commit")
	localTip := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	// Create a backup ref too.
	runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, localTip)
	runGitCmd(t, trunkDir, "checkout", "main")

	refsBefore := snapshotRefs(t, trunkDir, branch)

	sf := &stubFetchRecorder{
		err: ErrBranchNotOnOrigin,
	}
	svc := newTestRecoveryService(workspaceRoot, nil, nil, sf.fetch)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p1",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}

	_, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
	if err == nil {
		t.Fatal("expected error for missing branch")
	}

	var resetErr *RecoveryError
	if !errors.As(err, &resetErr) {
		t.Fatalf("expected RecoveryError, got %T: %v", err, err)
	}
	if resetErr.Kind != RecoveryErrMissingOnOrigin {
		t.Errorf("kind = %q; want %q", resetErr.Kind, RecoveryErrMissingOnOrigin)
	}

	// All refs unchanged.
	refsAfter := snapshotRefs(t, trunkDir, branch)
	for k, v := range refsBefore {
		if refsAfter[k] != v {
			t.Errorf("ref %q changed: %q -> %q", k, v, refsAfter[k])
		}
	}
}

// ===========================================================================
// TS-23-20: Reset creates a missing local branch and reports none for same commit
// ===========================================================================

func TestRecovery_CreateAndNone_TS2320(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "create-none"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a branch on the fork.
	branch := "feature/create"
	createBranchOnBare(t, forkDir, branch, "create.txt", "create content", "fork create commit")

	// Get the fork tip.
	tmpDir := filepath.Join(t.TempDir(), "tmp-fork-tip")
	cloneRepo(t, forkDir, tmpDir)
	runGitCmd(t, tmpDir, "checkout", branch)
	forkTip := runGitCmd(t, tmpDir, "rev-parse", "HEAD")

	// Create a backup ref in the trunk (should be unchanged).
	mainSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, mainSHA)
	backupBefore := revParse(t, trunkDir, "refs/hub/replaced/"+branch)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p1",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}

	t.Run("missing_local_creates", func(t *testing.T) {
		sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
		svc := newTestRecoveryService(workspaceRoot, nil, nil, sf.fetch)

		result, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
		if err != nil {
			t.Fatalf("RunReset failed: %v", err)
		}

		if result.Action != ActionCreated {
			t.Errorf("action = %q; want %q", result.Action, ActionCreated)
		}

		// Local branch should now exist at the fork tip.
		localSHA := revParse(t, trunkDir, "refs/heads/"+branch)
		if localSHA != forkTip {
			t.Errorf("local SHA = %q; want %q", localSHA, forkTip)
		}

		// Backup should be unchanged.
		backupAfter := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
		if backupAfter != backupBefore {
			t.Errorf("backup changed: %q -> %q", backupBefore, backupAfter)
		}
	})

	t.Run("same_commit_none", func(t *testing.T) {
		// Now the local branch is at the fork tip. Reset should be none.
		sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
		svc := newTestRecoveryService(workspaceRoot, nil, nil, sf.fetch)

		result, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
		if err != nil {
			t.Fatalf("RunReset failed: %v", err)
		}

		if result.Action != ActionNone {
			t.Errorf("action = %q; want %q", result.Action, ActionNone)
		}

		// Backup should still be unchanged.
		backupAfter := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
		if backupAfter != backupBefore {
			t.Errorf("backup changed: %q -> %q", backupBefore, backupAfter)
		}
	})
}

// ===========================================================================
// TS-23-21: A fast-forward reset moves the branch and leaves backup untouched
// ===========================================================================

func TestRecovery_FastForward_TS2321(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "ff-test"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a branch on the fork with two commits.
	branch := "feature/ff"
	tmpDir := filepath.Join(t.TempDir(), "tmp-fork-ff")
	cloneRepo(t, forkDir, tmpDir)
	runGitCmd(t, tmpDir, "checkout", "-b", branch)
	commitFile(t, tmpDir, "ff1.txt", "ff1", "ff commit 1")
	firstCommit := runGitCmd(t, tmpDir, "rev-parse", "HEAD")
	commitFile(t, tmpDir, "ff2.txt", "ff2", "ff commit 2")
	forkTip := runGitCmd(t, tmpDir, "rev-parse", "HEAD")
	runGitCmd(t, tmpDir, "push", "origin", branch)

	// Set the local branch at the first commit (behind the fork).
	runGitCmd(t, trunkDir, "fetch", "origin", branch)
	runGitCmd(t, trunkDir, "branch", branch, firstCommit)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p1",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}

	t.Run("with_existing_backup", func(t *testing.T) {
		// Create an older backup ref.
		mainSHA := runGitCmd(t, trunkDir, "rev-parse", "refs/heads/main")
		runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, mainSHA)
		backupBefore := revParse(t, trunkDir, "refs/hub/replaced/"+branch)

		// Reset the local branch back to firstCommit for this subtest.
		runGitCmd(t, trunkDir, "update-ref", "refs/heads/"+branch, firstCommit)

		sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
		svc := newTestRecoveryService(workspaceRoot, nil, nil, sf.fetch)

		result, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
		if err != nil {
			t.Fatalf("RunReset failed: %v", err)
		}

		if result.Action != ActionFastForwarded {
			t.Errorf("action = %q; want %q", result.Action, ActionFastForwarded)
		}

		// Local branch should be at the fork tip.
		localSHA := revParse(t, trunkDir, "refs/heads/"+branch)
		if localSHA != forkTip {
			t.Errorf("local SHA = %q; want %q", localSHA, forkTip)
		}

		// Backup should be unchanged.
		backupAfter := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
		if backupAfter != backupBefore {
			t.Errorf("backup changed: %q -> %q", backupBefore, backupAfter)
		}
	})

	t.Run("without_backup", func(t *testing.T) {
		// Remove any backup ref.
		runGitMayFail(trunkDir, "update-ref", "-d", "refs/hub/replaced/"+branch)

		// Reset the local branch back to firstCommit.
		runGitCmd(t, trunkDir, "update-ref", "refs/heads/"+branch, firstCommit)

		sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
		svc := newTestRecoveryService(workspaceRoot, nil, nil, sf.fetch)

		result, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
		if err != nil {
			t.Fatalf("RunReset failed: %v", err)
		}

		if result.Action != ActionFastForwarded {
			t.Errorf("action = %q; want %q", result.Action, ActionFastForwarded)
		}

		// Backup should still not exist.
		backupAfter := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
		if backupAfter != "" {
			t.Errorf("backup should not exist, got %q", backupAfter)
		}
	})
}

// ===========================================================================
// TS-23-22: Diverged and fork-behind resets write backup, move branch, report replaced
// ===========================================================================

func TestRecovery_Replaced_TS2322(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "replaced-test"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	branch := "feature/diverge"

	// Create a branch on the fork.
	forkTip := createBranchOnBare(t, forkDir, branch, "fork.txt", "fork content", "fork diverge commit")

	// Create a diverged local branch (different commit from fork).
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "local.txt", "local diverge content", "local diverge commit")
	localTip1 := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "checkout", "main")

	// Fetch origin so the tracking ref exists.
	runGitCmd(t, trunkDir, "fetch", "origin", branch)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p1",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}

	t.Run("diverged_first_reset", func(t *testing.T) {
		sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
		svc := newTestRecoveryService(workspaceRoot, nil, nil, sf.fetch)

		result, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
		if err != nil {
			t.Fatalf("RunReset failed: %v", err)
		}

		if result.Action != ActionReplaced {
			t.Errorf("action = %q; want %q", result.Action, ActionReplaced)
		}

		// Backup should be the old local tip.
		backupSHA := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
		if backupSHA != localTip1 {
			t.Errorf("backup = %q; want %q (old local tip)", backupSHA, localTip1)
		}

		// Local branch should be at the fork tip.
		localSHA := revParse(t, trunkDir, "refs/heads/"+branch)
		if localSHA != forkTip {
			t.Errorf("local SHA = %q; want %q", localSHA, forkTip)
		}

		// Result should carry replaced_sha.
		if result.ReplacedSHA != localTip1 {
			t.Errorf("result.ReplacedSHA = %q; want %q", result.ReplacedSHA, localTip1)
		}
	})

	t.Run("repeated_replaced_overwrites_backup", func(t *testing.T) {
		// Add a new commit on the local branch to create a new divergence.
		runGitCmd(t, trunkDir, "checkout", branch)
		commitFile(t, trunkDir, "local2.txt", "local diverge 2", "local diverge commit 2")
		localTip2 := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
		runGitCmd(t, trunkDir, "checkout", "main")

		sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
		svc := newTestRecoveryService(workspaceRoot, nil, nil, sf.fetch)

		result, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
		if err != nil {
			t.Fatalf("RunReset failed: %v", err)
		}

		if result.Action != ActionReplaced {
			t.Errorf("action = %q; want %q", result.Action, ActionReplaced)
		}

		// Backup should now be the newer local tip, overwriting the old one.
		backupSHA := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
		if backupSHA != localTip2 {
			t.Errorf("backup = %q; want %q (newer local tip)", backupSHA, localTip2)
		}
		if backupSHA == localTip1 {
			t.Error("backup should have been overwritten")
		}
	})

	t.Run("fork_behind_also_replaced", func(t *testing.T) {
		// Create a scenario where the fork tip is a strict ancestor of the local tip.
		// Make a new commit on the local branch that is ahead of the fork.
		runGitCmd(t, trunkDir, "checkout", branch)
		commitFile(t, trunkDir, "ahead.txt", "ahead of fork", "ahead of fork commit")
		localTipAhead := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
		runGitCmd(t, trunkDir, "checkout", "main")

		sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
		svc := newTestRecoveryService(workspaceRoot, nil, nil, sf.fetch)

		result, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
		if err != nil {
			t.Fatalf("RunReset failed: %v", err)
		}

		if result.Action != ActionReplaced {
			t.Errorf("action = %q; want %q", result.Action, ActionReplaced)
		}

		// Backup should be the ahead local tip.
		backupSHA := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
		if backupSHA != localTipAhead {
			t.Errorf("backup = %q; want %q", backupSHA, localTipAhead)
		}

		// Local branch should be at the fork tip.
		localSHA := revParse(t, trunkDir, "refs/heads/"+branch)
		if localSHA != forkTip {
			t.Errorf("local SHA = %q; want %q", localSHA, forkTip)
		}
	})
}

// ===========================================================================
// TS-23-23: Every branch move uses compare-and-swap update-ref and working tree follows
// ===========================================================================

func TestRecovery_CASAndWorktree_TS2323(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "cas-test"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	branch := "feature/cas"

	// Create a branch on the fork.
	forkTip := createBranchOnBare(t, forkDir, branch, "cas.txt", "cas content", "fork cas commit")

	// Create a diverged local branch and check it out (so it's the trunk's HEAD).
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "local-cas.txt", "local cas content", "local cas commit")
	runGitCmd(t, trunkDir, "fetch", "origin", branch)
	// The branch is now checked out.

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p1",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}

	sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}
	svc := newTestRecoveryService(workspaceRoot, nil, nil, sf.fetch)

	result, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
	if err != nil {
		t.Fatalf("RunReset failed: %v", err)
	}

	if result.Action != ActionReplaced {
		t.Errorf("action = %q; want %q", result.Action, ActionReplaced)
	}

	// The branch should be at the fork tip.
	localSHA := revParse(t, trunkDir, "refs/heads/"+branch)
	if localSHA != forkTip {
		t.Errorf("local SHA = %q; want %q", localSHA, forkTip)
	}

	// HEAD should point at the branch.
	headRef := runGitCmd(t, trunkDir, "symbolic-ref", "HEAD")
	if headRef != "refs/heads/"+branch {
		t.Errorf("HEAD = %q; want refs/heads/%s", headRef, branch)
	}

	// Working tree should be clean and match the fork tip.
	status := gitStatus(t, trunkDir)
	if status != "" {
		t.Errorf("working tree is dirty: %s", status)
	}

	// The cas.txt file from the fork should exist.
	content, err := os.ReadFile(filepath.Join(trunkDir, "cas.txt"))
	if err != nil {
		t.Fatalf("failed to read cas.txt: %v", err)
	}
	if string(content) != "cas content" {
		t.Errorf("cas.txt content = %q; want %q", string(content), "cas content")
	}

	// The local-cas.txt file should NOT exist (it was on the old local branch).
	if _, err := os.Stat(filepath.Join(trunkDir, "local-cas.txt")); !os.IsNotExist(err) {
		t.Error("local-cas.txt should not exist after reset to fork tip")
	}
}

// ===========================================================================
// TS-23-24: Lost compare-and-swap race answers ref_changed
// ===========================================================================

func TestRecovery_RefChanged_TS2324(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "race-test"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	branch := "feature/race"

	// Create a branch on the fork.
	createBranchOnBare(t, forkDir, branch, "race.txt", "race content", "fork race commit")

	// Create a diverged local branch.
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "local-race.txt", "local race content", "local race commit")
	runGitCmd(t, trunkDir, "checkout", "main")

	// Fetch origin.
	runGitCmd(t, trunkDir, "fetch", "origin", branch)

	ctx := context.Background()
	patchInfo := ResetPatchInfo{
		ID:                "p1",
		BranchName:        branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}

	t.Run("lost_race", func(t *testing.T) {
		// We'll use a custom git runner that intercepts the CAS update-ref
		// and moves the branch between SHA resolution and the write.
		raceTriggered := false
		sf := &stubFetchRecorder{realFetch: DefaultSingleBranchFetch()}

		svc := &RecoveryService{
			NewGitRunner:  NewGitRunnerFactory(),
			WorkspaceRoot: workspaceRoot,
			Fetch:         sf.fetch,
			LockFunc:      wslock.TryLock,
		}

		// We need to simulate a race. We'll use a wrapper runner that
		// intercepts the CAS update-ref for the branch move.
		origFactory := svc.NewGitRunner
		svc.NewGitRunner = func(repoPath string) (GitRunner, error) {
			runner, err := origFactory(repoPath)
			if err != nil {
				return nil, err
			}
			return &racingGitRunner{
				GitRunner: runner,
				trunkDir:  trunkDir,
				branch:    branch,
				triggered: &raceTriggered,
			}, nil
		}

		_, err := svc.RunReset(ctx, slug, patchInfo, &apikit.AuthInfo{UserID: "user-1"})
		if err == nil {
			t.Fatal("expected error for lost race")
		}

		var resetErr *RecoveryError
		if !errors.As(err, &resetErr) {
			t.Fatalf("expected RecoveryError, got %T: %v", err, err)
		}
		if resetErr.Kind != RecoveryErrRefChanged {
			t.Errorf("kind = %q; want %q", resetErr.Kind, RecoveryErrRefChanged)
		}

		// The backup should have been written before the failed move.
		backupSHA := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
		if backupSHA == "" {
			t.Error("backup should have been written before the failed move")
		}
	})
}

// racingGitRunner wraps a GitRunner and intercepts the CAS update-ref for
// the branch move to simulate a concurrent push.
type racingGitRunner struct {
	GitRunner
	trunkDir  string
	branch    string
	triggered *bool
}

func (r *racingGitRunner) Run(ctx context.Context, args ...string) (string, error) {
	// Intercept the CAS update-ref for the branch move (not the backup write).
	// The backup write targets refs/hub/replaced/..., the branch move targets refs/heads/...
	if len(args) >= 4 && args[0] == "update-ref" &&
		args[1] == "refs/heads/"+r.branch &&
		!*r.triggered {
		// Move the branch to a different commit to simulate a race.
		*r.triggered = true

		// Use raw git commands to create a new commit on the branch.
		cmd := exec.Command("git", "checkout", r.branch)
		cmd.Dir = r.trunkDir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		cmd.CombinedOutput()

		os.WriteFile(filepath.Join(r.trunkDir, "race-interloper.txt"), []byte("interloper"), 0o644)

		cmd = exec.Command("git", "add", ".")
		cmd.Dir = r.trunkDir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		cmd.CombinedOutput()

		cmd = exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@test.com", "commit", "-m", "interloper commit")
		cmd.Dir = r.trunkDir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		cmd.CombinedOutput()

		cmd = exec.Command("git", "checkout", "main")
		cmd.Dir = r.trunkDir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		cmd.CombinedOutput()
	}
	return r.GitRunner.Run(ctx, args...)
}

// ===========================================================================
// TS-23-61: carrypatch implements the hook, workspace and carrypatch don't import each other
// ===========================================================================

func TestRecovery_PackageBoundaries_TS2361(t *testing.T) {
	// Verify that RecoveryService implements the three operations.
	svc := &RecoveryService{
		NewGitRunner:  NewGitRunnerFactory(),
		WorkspaceRoot: t.TempDir(),
		LockFunc:      wslock.TryLock,
	}

	ctx := context.Background()

	// ReadReplacedSHA should return not-found for a non-existent trunk.
	_, found, err := svc.ReadReplacedSHA(ctx, "nonexistent", "branch")
	if err != nil {
		t.Errorf("ReadReplacedSHA error = %v; want nil", err)
	}
	if found {
		t.Error("ReadReplacedSHA found = true; want false for missing trunk")
	}

	// RemoveBackup should succeed for a non-existent trunk.
	err = svc.RemoveBackup(ctx, "nonexistent", "branch")
	if err != nil {
		t.Errorf("RemoveBackup error = %v; want nil", err)
	}
}

func TestRecovery_ImportGraph_TS2361(t *testing.T) {
	// Verify that internal/workspace does not depend on internal/carrypatch
	// and vice versa. This is a build-time check.
	repoRoot := findRepoRoot(t)

	cmd := exec.Command("go", "list", "-deps", "./internal/workspace")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./internal/workspace failed: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "internal/carrypatch") {
			t.Error("internal/workspace depends on internal/carrypatch")
		}
	}

	cmd = exec.Command("go", "list", "-deps", "./internal/carrypatch")
	cmd.Dir = repoRoot
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./internal/carrypatch failed: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "internal/workspace") {
			t.Error("internal/carrypatch depends on internal/workspace")
		}
	}
}

// findRepoRoot walks up from the current directory to find the go.mod file.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

// ===========================================================================
// ReadReplacedSHA and RemoveBackup tests
// ===========================================================================

func TestRecovery_ReadReplacedSHA(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "read-sha"

	_, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	branch := "feature/read"
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "read.txt", "read", "read commit")
	commitSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "checkout", "main")

	svc := &RecoveryService{
		NewGitRunner:  NewGitRunnerFactory(),
		WorkspaceRoot: workspaceRoot,
	}

	ctx := context.Background()

	t.Run("no_backup", func(t *testing.T) {
		sha, found, err := svc.ReadReplacedSHA(ctx, slug, branch)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if found {
			t.Errorf("found = true; want false")
		}
		if sha != "" {
			t.Errorf("sha = %q; want empty", sha)
		}
	})

	t.Run("with_backup", func(t *testing.T) {
		runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, commitSHA)

		sha, found, err := svc.ReadReplacedSHA(ctx, slug, branch)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if !found {
			t.Error("found = false; want true")
		}
		if sha != commitSHA {
			t.Errorf("sha = %q; want %q", sha, commitSHA)
		}
	})

	t.Run("missing_trunk", func(t *testing.T) {
		sha, found, err := svc.ReadReplacedSHA(ctx, "nonexistent", branch)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if found {
			t.Error("found = true; want false for missing trunk")
		}
		if sha != "" {
			t.Errorf("sha = %q; want empty", sha)
		}
	})
}

func TestRecovery_RemoveBackup(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "remove-backup"

	_, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	branch := "feature/remove"
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "remove.txt", "remove", "remove commit")
	commitSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, commitSHA)
	runGitCmd(t, trunkDir, "checkout", "main")

	svc := &RecoveryService{
		NewGitRunner:  NewGitRunnerFactory(),
		WorkspaceRoot: workspaceRoot,
	}

	ctx := context.Background()

	t.Run("remove_existing", func(t *testing.T) {
		err := svc.RemoveBackup(ctx, slug, branch)
		if err != nil {
			t.Fatalf("error = %v", err)
		}

		// Verify the ref is gone.
		sha := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
		if sha != "" {
			t.Errorf("backup still exists: %q", sha)
		}
	})

	t.Run("remove_missing_ref", func(t *testing.T) {
		// Removing a non-existent ref should succeed.
		err := svc.RemoveBackup(ctx, slug, "nonexistent-branch")
		if err != nil {
			t.Fatalf("error = %v; want nil for missing ref", err)
		}
	})

	t.Run("remove_missing_trunk", func(t *testing.T) {
		err := svc.RemoveBackup(ctx, "nonexistent-slug", branch)
		if err != nil {
			t.Fatalf("error = %v; want nil for missing trunk", err)
		}
	})
}
