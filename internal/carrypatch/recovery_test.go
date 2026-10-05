package carrypatch

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
	"github.com/agent-fox-dev/hub/internal/jobqueue"
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

// ===========================================================================
// Issue #45: reset scenarios without a remote
//
// The fork tip is written straight into refs/remotes/origin/<branch> and the
// fetch is a no-op stub, so a scenario needs no bare repository. Every
// scenario has one patch branch, "feat", whose local state depends on the
// action the reset is expected to take.
// ===========================================================================

type resetScenario struct {
	root     string
	slug     string
	branch   string
	tr       *refTrunk
	forkTip  string
	localTip string // empty when there is no local branch (ActionCreated)
	other    string // an unrelated commit, used to simulate a concurrent writer
}

func newResetScenario(t *testing.T, action string) *resetScenario {
	t.Helper()
	root := t.TempDir()
	const slug = "reset-ws"
	dir := filepath.Join(root, slug, "trunk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	runGitCmd(t, "", "init", "-b", "main", dir)
	configGitUserCmd(t, dir)
	writeFileHelper(t, filepath.Join(dir, "file.txt"), "hello")
	runGitCmd(t, dir, "add", ".")
	runGitCmd(t, dir, "commit", "-m", "initial")
	tr := &refTrunk{dir: dir, base: runGitCmd(t, dir, "rev-parse", "HEAD")}

	s := &resetScenario{root: root, slug: slug, branch: "feat", tr: tr}
	s.forkTip = tr.commit(t, tr.base, "fork tip")
	switch action {
	case ActionCreated:
		// No local branch.
	case ActionNone:
		s.localTip = s.forkTip
		runGitCmd(t, dir, "branch", s.branch, s.localTip)
	case ActionFastForwarded:
		s.localTip = tr.base
		runGitCmd(t, dir, "branch", s.branch, s.localTip)
	case ActionReplaced:
		s.localTip = tr.commit(t, tr.base, "local tip")
		runGitCmd(t, dir, "branch", s.branch, s.localTip)
	default:
		t.Fatalf("unknown action %q", action)
	}
	runGitCmd(t, dir, "update-ref", "refs/remotes/origin/"+s.branch, s.forkTip)
	s.other = tr.commit(t, tr.base, "interloper")
	return s
}

func (s *resetScenario) branchRef() string { return "refs/heads/" + s.branch }

func (s *resetScenario) patch() ResetPatchInfo {
	return ResetPatchInfo{
		ID:                "p1",
		BranchName:        s.branch,
		Status:            PatchStatusActive,
		IntegrationBranch: "main",
	}
}

// service builds a RecoveryService with every required dependency set; the
// fetch does nothing because the tracking ref is already in place.
func (s *resetScenario) service(runner GitRunner) *RecoveryService {
	return &RecoveryService{
		NewGitRunner:  func(string) (GitRunner, error) { return runner, nil },
		WorkspaceRoot: s.root,
		Fetch: func(context.Context, string, string, transport.AuthMethod) error {
			return nil
		},
		LockFunc: func(string) (func(), bool) { return func() {}, true },
	}
}

func (s *resetScenario) reset(t *testing.T, svc *RecoveryService) (ResetResult, error) {
	t.Helper()
	return svc.RunReset(context.Background(), s.slug, s.patch(), &apikit.AuthInfo{UserID: "user-1"})
}

// recordingEnqueuer is a RebuildEnqueuer that records every job it is given.
type recordingEnqueuer struct {
	calls []jobqueue.EnqueueParams
}

func (q *recordingEnqueuer) Enqueue(p jobqueue.EnqueueParams) (string, bool, error) {
	q.calls = append(q.calls, p)
	return "job-recorded", false, nil
}

// captureLogs routes the default slog logger into a buffer for the test.
func captureLogs(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// ===========================================================================
// Issue #45 finding 4: ReadReplacedSHA reports a lookup failure instead of
// rendering it as "no backup" (23-REQ-1: the handler logs it at warn level)
// ===========================================================================

func TestRecovery_ReadReplacedSHA_LookupFailures_Issue45(t *testing.T) {
	root := t.TempDir()
	const slug = "lookup-fail"
	if err := os.MkdirAll(filepath.Join(root, slug, "trunk"), 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	const backupSHA = "aabbccddee00112233445566778899aabbccddee"
	ctx := context.Background()

	runnerReturning := func(out string, err error) func(string) (GitRunner, error) {
		return func(string) (GitRunner, error) {
			m := newMockGitRunner()
			m.RunFunc = func(context.Context, ...string) (string, error) { return out, err }
			return m, nil
		}
	}

	failures := []struct {
		name    string
		factory func(string) (GitRunner, error)
		wantErr error // when set, errors.Is must hold
	}{
		{
			name:    "runner_construction_error",
			factory: func(string) (GitRunner, error) { return nil, errRunnerBroken },
			wantErr: errRunnerBroken,
		},
		{
			name:    "rev_parse_exit_128",
			factory: runnerReturning("", &gitcmd.GitError{Args: []string{"rev-parse"}, ExitCode: 128, Stderr: "fatal: not a git repository"}),
		},
		{
			name:    "git_cannot_be_run",
			factory: runnerReturning("", errRunnerBroken),
			wantErr: errRunnerBroken,
		},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			svc := &RecoveryService{NewGitRunner: tc.factory, WorkspaceRoot: root}
			sha, found, err := svc.ReadReplacedSHA(ctx, slug, "feature/x")
			if err == nil {
				t.Fatal("error = nil; a lookup failure must be reported, not rendered as no backup")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v; want it to wrap %v", err, tc.wantErr)
			}
			if found || sha != "" {
				t.Errorf("sha = %q, found = %v; want empty and false on failure", sha, found)
			}
		})
	}

	t.Run("missing_ref_is_not_a_failure", func(t *testing.T) {
		// git rev-parse --verify --quiet exits 1 for a ref that does not exist.
		missing := &gitcmd.GitError{Args: []string{"rev-parse"}, ExitCode: 1}
		svc := &RecoveryService{NewGitRunner: runnerReturning("", missing), WorkspaceRoot: root}
		sha, found, err := svc.ReadReplacedSHA(ctx, slug, "feature/x")
		if err != nil || found || sha != "" {
			t.Errorf("got (%q, %v, %v); want (\"\", false, nil)", sha, found, err)
		}
	})

	t.Run("existing_ref", func(t *testing.T) {
		svc := &RecoveryService{NewGitRunner: runnerReturning(backupSHA+"\n", nil), WorkspaceRoot: root}
		sha, found, err := svc.ReadReplacedSHA(ctx, slug, "feature/x")
		if err != nil || !found || sha != backupSHA {
			t.Errorf("got (%q, %v, %v); want (%q, true, nil)", sha, found, err, backupSHA)
		}
	})

	t.Run("missing_trunk_is_not_a_failure", func(t *testing.T) {
		svc := &RecoveryService{
			NewGitRunner:  func(string) (GitRunner, error) { return nil, errRunnerBroken },
			WorkspaceRoot: root,
		}
		sha, found, err := svc.ReadReplacedSHA(ctx, "no-such-slug", "feature/x")
		if err != nil || found || sha != "" {
			t.Errorf("got (%q, %v, %v); want (\"\", false, nil)", sha, found, err)
		}
	})
}

var errRunnerBroken = errors.New("git binary is broken")

// ===========================================================================
// Issue #45 finding 6: a reset without its fetch or lock is a configuration
// error, never a silent success on a stale tracking ref
// ===========================================================================

func TestRecovery_RunReset_RequiredDependencies_Issue45(t *testing.T) {
	wantOther := func(t *testing.T, err error, wantMessage string) {
		t.Helper()
		var resetErr *RecoveryError
		if !errors.As(err, &resetErr) {
			t.Fatalf("error = %v (%T); want *RecoveryError", err, err)
		}
		if resetErr.Kind != RecoveryErrOther {
			t.Errorf("kind = %q; want %q", resetErr.Kind, RecoveryErrOther)
		}
		if !strings.Contains(resetErr.Message, wantMessage) {
			t.Errorf("message = %q; want it to contain %q", resetErr.Message, wantMessage)
		}
	}

	t.Run("nil_fetch", func(t *testing.T) {
		// The tracking ref is stale but present: without a fetch the reset
		// would create the branch from it and report success.
		s := newResetScenario(t, ActionCreated)
		svc := s.service(newRealGitRunner(t, s.tr.dir))
		svc.Fetch = nil

		result, err := s.reset(t, svc)
		if err == nil {
			t.Fatalf("RunReset succeeded with action %q; want a configuration error", result.Action)
		}
		wantOther(t, err, "origin fetch is not configured")
		if got := revParse(t, s.tr.dir, s.branchRef()); got != "" {
			t.Errorf("branch was created at %s; nothing may be written", got)
		}
	})

	t.Run("nil_lock", func(t *testing.T) {
		s := newResetScenario(t, ActionCreated)
		svc := s.service(newRealGitRunner(t, s.tr.dir))
		svc.LockFunc = nil

		_, err := s.reset(t, svc)
		if err == nil {
			t.Fatal("RunReset succeeded; want a configuration error")
		}
		wantOther(t, err, "lock function not configured")
		if got := revParse(t, s.tr.dir, s.branchRef()); got != "" {
			t.Errorf("branch was created at %s; nothing may be written", got)
		}
	})

	t.Run("nil_resolve_auth_means_anonymous", func(t *testing.T) {
		// A nil ResolveAuth is anonymous origin access (spec 20, erratum 10);
		// the fetch still runs, with no credentials.
		s := newResetScenario(t, ActionCreated)
		sf := &stubFetchRecorder{}
		svc := s.service(newRealGitRunner(t, s.tr.dir))
		svc.ResolveAuth = nil
		svc.Fetch = sf.fetch

		result, err := s.reset(t, svc)
		if err != nil {
			t.Fatalf("RunReset failed: %v", err)
		}
		if len(sf.calls) != 1 || sf.calls[0].Auth != nil {
			t.Errorf("fetch calls = %+v; want one call with nil auth", sf.calls)
		}
		if result.Action != ActionCreated {
			t.Errorf("action = %q; want %q", result.Action, ActionCreated)
		}
	})
}

// ===========================================================================
// Issue #45 finding 5: a hard-reset failure after the branch moved is not
// fatal. The reset reports the move, persists in_sync, enqueues the rebuild
// and logs the stale work tree at error level.
// ===========================================================================

func TestRecovery_RunReset_HardResetFailureAfterMove_Issue45(t *testing.T) {
	for _, action := range []string{ActionCreated, ActionFastForwarded, ActionReplaced} {
		t.Run(action, func(t *testing.T) {
			s := newResetScenario(t, action)
			// HEAD names the branch about to move, so the work tree must follow it.
			s.tr.pointHeadAt(t, s.branch)

			db := openTestDB(t)
			createPatchesTable(t, db)
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := db.Exec(
				`INSERT INTO patches (id, workspace_slug, branch_name, position, status, origin_sync_state, added_at, updated_at)
				 VALUES ('p1', ?, ?, 1, 'active', 'diverged', ?, ?)`,
				s.slug, s.branch, now, now,
			); err != nil {
				t.Fatalf("seed patch: %v", err)
			}

			queue := &recordingEnqueuer{}
			svc := s.service(&failingResetRunner{GitRunner: newRealGitRunner(t, s.tr.dir)})
			svc.GetVariable = func(_, _, key string) (string, error) {
				if key == "PATCH_BRANCH_SOURCE" {
					return "origin", nil
				}
				return "", nil
			}
			svc.PatchStore = NewSQLPatchStore(db)
			svc.Queue = queue
			logs := captureLogs(t)

			result, err := s.reset(t, svc)
			if err != nil {
				t.Fatalf("RunReset returned %v; the branch moved, so the reset must report it", err)
			}
			if result.Action != action {
				t.Errorf("action = %q; want %q", result.Action, action)
			}
			if got := refSHA(t, s.tr.dir, s.branchRef()); got != s.forkTip {
				t.Errorf("branch = %s; want fork tip %s", got, s.forkTip)
			}
			if !result.RebuildTriggered || len(queue.calls) != 1 {
				t.Errorf("rebuild_triggered = %v, enqueue calls = %d; want a rebuild for the moved branch",
					result.RebuildTriggered, len(queue.calls))
			}

			var state string
			if err := db.QueryRow(`SELECT origin_sync_state FROM patches WHERE id = 'p1'`).Scan(&state); err != nil {
				t.Fatalf("query patch: %v", err)
			}
			if state != StateInSync {
				t.Errorf("origin_sync_state = %q; want %q", state, StateInSync)
			}

			found := false
			for _, line := range strings.Split(logs.String(), "\n") {
				if strings.Contains(line, "hard reset") {
					found = true
					if !strings.Contains(line, "level=ERROR") {
						t.Errorf("hard-reset failure logged below error level: %s", line)
					}
					if !strings.Contains(line, s.slug) || !strings.Contains(line, s.branch) {
						t.Errorf("hard-reset log should name the slug and branch: %s", line)
					}
				}
			}
			if !found {
				t.Errorf("no log line mentions the failed hard reset; logs:\n%s", logs.String())
			}
		})
	}
}

// ===========================================================================
// TS-23-23 (command level): every branch move is one update-ref carrying the
// new and the expected old SHA; the work tree follows with a hard reset only
// when HEAD is on the branch; no checkout, branch or reset command runs.
//
// Verifies: 23-REQ-3.7
// ===========================================================================

func TestRecovery_CASCommands_TS2323(t *testing.T) {
	cases := []struct {
		action  string
		wantOld func(s *resetScenario) string // expected-old SHA; "" means no write
	}{
		{ActionCreated, func(*resetScenario) string { return zeroSHA }},
		{ActionFastForwarded, func(s *resetScenario) string { return s.localTip }},
		{ActionReplaced, func(s *resetScenario) string { return s.localTip }},
		{ActionNone, func(*resetScenario) string { return "" }},
	}

	for _, tc := range cases {
		for _, headOnBranch := range []bool{true, false} {
			name := tc.action + "/head_elsewhere"
			if headOnBranch {
				name = tc.action + "/head_on_branch"
			}
			t.Run(name, func(t *testing.T) {
				s := newResetScenario(t, tc.action)
				if headOnBranch {
					s.tr.pointHeadAt(t, s.branch)
				}
				rec := &recordingGitRunner{real: newRealGitRunner(t, s.tr.dir)}

				result, err := s.reset(t, s.service(rec))
				if err != nil {
					t.Fatalf("RunReset failed: %v", err)
				}
				if result.Action != tc.action {
					t.Fatalf("action = %q; want %q", result.Action, tc.action)
				}

				var branchWrites [][]string
				for _, call := range rec.runCalls {
					if len(call) == 0 {
						continue
					}
					switch call[0] {
					case "checkout", "switch", "branch", "reset", "merge", "rebase", "cherry-pick":
						t.Errorf("unexpected command: git %s", strings.Join(call, " "))
					case "update-ref":
						if len(call) > 1 && call[1] == s.branchRef() {
							branchWrites = append(branchWrites, call)
						}
					}
				}

				wantOld := tc.wantOld(s)
				if wantOld == "" {
					if len(branchWrites) != 0 {
						t.Errorf("branch writes = %v; want none", branchWrites)
					}
				} else {
					want := []string{"update-ref", s.branchRef(), s.forkTip, wantOld}
					if len(branchWrites) != 1 || strings.Join(branchWrites[0], " ") != strings.Join(want, " ") {
						t.Errorf("branch writes = %v; want exactly [%v]", branchWrites, want)
					}
				}

				wantResets := 0
				if headOnBranch && tc.action != ActionNone {
					wantResets = 1
				}
				if len(rec.hardResetCalls) != wantResets {
					t.Errorf("hard resets = %v; want %d", rec.hardResetCalls, wantResets)
				}
			})
		}
	}
}

// ===========================================================================
// TS-23-24 (command level): a failed compare-and-swap is classified from the
// ref's state afterwards. A moved or deleted ref is ref_changed; any other
// write failure, including one where the ref cannot be re-read, is a plain
// failure. A backup already written stays.
//
// Verifies: 23-REQ-3.8, 23-REQ-9.2
// ===========================================================================

func TestRecovery_CASFailureClassification_TS2324(t *testing.T) {
	casFailure := func(args []string) error {
		return &gitcmd.GitError{
			Args:     args,
			ExitCode: 128,
			Stderr:   "fatal: update_ref failed for ref '" + args[1] + "': cannot lock ref",
		}
	}

	// interference runs in place of the compare-and-swap update-ref. It may
	// change the repository first and returns the error the write reports.
	type interference func(t *testing.T, s *resetScenario, real GitRunner, args []string) error

	moveRef := func(t *testing.T, s *resetScenario, real GitRunner, args []string) error {
		if _, err := real.Run(context.Background(), "update-ref", s.branchRef(), s.other); err != nil {
			t.Fatalf("simulate concurrent move: %v", err)
		}
		_, err := real.Run(context.Background(), args...)
		if err == nil {
			t.Fatal("the compare-and-swap should have failed after the concurrent move")
		}
		return err
	}
	deleteRef := func(t *testing.T, s *resetScenario, real GitRunner, args []string) error {
		if _, err := real.Run(context.Background(), "update-ref", "-d", s.branchRef()); err != nil {
			t.Fatalf("simulate concurrent delete: %v", err)
		}
		_, err := real.Run(context.Background(), args...)
		if err == nil {
			t.Fatal("the compare-and-swap should have failed after the concurrent delete")
		}
		return err
	}
	otherFailure := func(_ *testing.T, _ *resetScenario, _ GitRunner, args []string) error {
		return casFailure(args)
	}

	cases := []struct {
		name         string
		action       string
		interfere    interference
		breakLookup  bool // rev-parse of the branch fails once the write has failed
		wantKind     RecoveryErrorKind
		wantBackupAt func(s *resetScenario) string
	}{
		{name: "create_other_failure", action: ActionCreated, interfere: otherFailure, wantKind: RecoveryErrOther},
		{name: "create_ref_appeared", action: ActionCreated, interfere: moveRef, wantKind: RecoveryErrRefChanged},
		{name: "fast_forward_other_failure", action: ActionFastForwarded, interfere: otherFailure, wantKind: RecoveryErrOther},
		{name: "fast_forward_ref_moved", action: ActionFastForwarded, interfere: moveRef, wantKind: RecoveryErrRefChanged},
		{name: "fast_forward_ref_deleted", action: ActionFastForwarded, interfere: deleteRef, wantKind: RecoveryErrRefChanged},
		{name: "fast_forward_lookup_also_fails", action: ActionFastForwarded, interfere: otherFailure, breakLookup: true, wantKind: RecoveryErrOther},
		{name: "replace_other_failure", action: ActionReplaced, interfere: otherFailure, wantKind: RecoveryErrOther,
			wantBackupAt: func(s *resetScenario) string { return s.localTip }},
		{name: "replace_ref_moved", action: ActionReplaced, interfere: moveRef, wantKind: RecoveryErrRefChanged,
			wantBackupAt: func(s *resetScenario) string { return s.localTip }},
		{name: "replace_ref_deleted", action: ActionReplaced, interfere: deleteRef, wantKind: RecoveryErrRefChanged,
			wantBackupAt: func(s *resetScenario) string { return s.localTip }},
		{name: "replace_lookup_also_fails", action: ActionReplaced, interfere: otherFailure, breakLookup: true, wantKind: RecoveryErrOther,
			wantBackupAt: func(s *resetScenario) string { return s.localTip }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newResetScenario(t, tc.action)
			real := newRealGitRunner(t, s.tr.dir)

			writeFailed := false
			rec := &recordingGitRunner{real: real}
			rec.runOverride = func(ctx context.Context, args ...string) (string, error) {
				if len(args) == 4 && args[0] == "update-ref" && args[1] == s.branchRef() {
					writeFailed = true
					return "", tc.interfere(t, s, real, args)
				}
				if tc.breakLookup && writeFailed && args[0] == "rev-parse" {
					return "", &gitcmd.GitError{Args: args, ExitCode: 128, Stderr: "fatal: unable to read refs"}
				}
				return real.Run(ctx, args...)
			}

			_, err := s.reset(t, s.service(rec))
			var resetErr *RecoveryError
			if !errors.As(err, &resetErr) {
				t.Fatalf("error = %v (%T); want *RecoveryError", err, err)
			}
			if resetErr.Kind != tc.wantKind {
				t.Errorf("kind = %q; want %q (error: %v)", resetErr.Kind, tc.wantKind, resetErr)
			}
			if tc.wantKind == RecoveryErrOther && !strings.Contains(resetErr.Message, "failed to update patch branch "+s.branch) {
				t.Errorf("message = %q; want it to name the branch", resetErr.Message)
			}
			if tc.wantBackupAt != nil {
				if got := revParse(t, s.tr.dir, "refs/hub/replaced/"+s.branch); got != tc.wantBackupAt(s) {
					t.Errorf("backup ref = %q; want %q (a backup already written stays)", got, tc.wantBackupAt(s))
				}
			}
		})
	}
}

// ===========================================================================
// Issue #45 finding 7: classifyCASError reads the ref's state, not the text
// of the git error
// ===========================================================================

func TestClassifyCASError_FromRefState_Issue45(t *testing.T) {
	const (
		branch = "feat"
		ref    = "refs/heads/feat"
		old    = "1111111111111111111111111111111111111111"
		other  = "2222222222222222222222222222222222222222"
	)
	// git prints "update_ref failed" for every failed update-ref, a lost race
	// or not, so the text proves nothing about which it was.
	writeErr := &gitcmd.GitError{ExitCode: 128, Stderr: "fatal: update_ref failed for ref '" + ref + "': cannot lock ref"}
	missing := &gitcmd.GitError{ExitCode: 1}
	broken := &gitcmd.GitError{ExitCode: 128, Stderr: "fatal: unable to read refs"}

	cases := []struct {
		name        string
		expectedOld string
		lookupOut   string
		lookupErr   error
		wantKind    RecoveryErrorKind
	}{
		{"ref_unchanged_is_not_a_race", old, old, nil, RecoveryErrOther},
		{"ref_moved", old, other, nil, RecoveryErrRefChanged},
		{"ref_deleted", old, "", missing, RecoveryErrRefChanged},
		{"create_but_ref_exists", zeroSHA, other, nil, RecoveryErrRefChanged},
		{"create_and_ref_still_missing", zeroSHA, "", missing, RecoveryErrOther},
		{"lookup_fails", old, "", broken, RecoveryErrOther},
		{"create_and_lookup_fails", zeroSHA, "", broken, RecoveryErrOther},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockGitRunner()
			m.RunFunc = func(context.Context, ...string) (string, error) { return tc.lookupOut, tc.lookupErr }

			got := classifyCASError(context.Background(), m, branch, ref, tc.expectedOld, writeErr)

			if got.Kind != tc.wantKind {
				t.Fatalf("kind = %q; want %q", got.Kind, tc.wantKind)
			}
			if tc.wantKind == RecoveryErrOther {
				if !errors.Is(got, writeErr) {
					t.Errorf("error %v should wrap the write failure", got)
				}
				if got.Message != "failed to update patch branch "+branch {
					t.Errorf("message = %q", got.Message)
				}
			} else if got.Message != "patch branch changed during reset; retry" {
				t.Errorf("message = %q", got.Message)
			}
		})
	}
}

// ===========================================================================
// Issue #45 findings 1-3: the reset, the sync and the purge share one
// implementation of the CAS ref move, the rebuild enqueue and the backup-ref
// removal (spec 23 PRD: extract and share, do not duplicate)
// ===========================================================================

// parseGoFile parses a source file of this package.
func parseGoFile(t *testing.T, name string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

// functionsUsing returns the names of the functions in f that contain a
// string literal equal to lit, and, separately, a composite literal of a type
// whose name is typeName.
func functionsUsing(f *ast.File, lit, typeName string) (withLiteral, withComposite map[string]bool) {
	withLiteral, withComposite = map[string]bool{}, map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if x.Kind == token.STRING && x.Value == lit {
					withLiteral[fn.Name.Name] = true
				}
			case *ast.CompositeLit:
				if sel, ok := x.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == typeName {
					withComposite[fn.Name.Name] = true
				}
			}
			return true
		})
	}
	return withLiteral, withComposite
}

// callsOf returns the package-level functions and methods that the function
// or method named fnName calls by bare name.
func callsOf(f *ast.File, fnName string) map[string]bool {
	calls := map[string]bool{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok {
					calls[id.Name] = true
				}
			}
			return true
		})
	}
	return calls
}

func TestSharedHelpers_NotDuplicated_Issue45(t *testing.T) {
	only := func(t *testing.T, file, what string, got map[string]bool, allowed ...string) {
		t.Helper()
		for name := range got {
			ok := false
			for _, a := range allowed {
				ok = ok || name == a
			}
			if !ok {
				t.Errorf("%s: %s appears in %s; it belongs only in %v", file, what, name, allowed)
			}
		}
		// The scan must find the helper itself, or it proves nothing.
		for _, a := range allowed {
			if !got[a] {
				t.Errorf("%s: %s not found in %s; the scan is not looking at the right code", file, what, a)
			}
		}
	}

	t.Run("update_ref_literals", func(t *testing.T) {
		// The three-argument update-ref has one home; deleting the backup ref
		// has one home; neither is re-inlined in the reset or the purge.
		lit, _ := functionsUsing(parseGoFile(t, "patch_refresh.go"), `"update-ref"`, "EnqueueParams")
		only(t, "patch_refresh.go", `"update-ref"`, lit, "casUpdateRef")
		lit, _ = functionsUsing(parseGoFile(t, "recovery.go"), `"update-ref"`, "EnqueueParams")
		only(t, "recovery.go", `"update-ref"`, lit, "removeBackupRef")
		lit, _ = functionsUsing(parseGoFile(t, "purge_refs.go"), `"update-ref"`, "EnqueueParams")
		only(t, "purge_refs.go", `"update-ref"`, lit)
	})

	t.Run("enqueue_params_literals", func(t *testing.T) {
		_, comp := functionsUsing(parseGoFile(t, "sync_handlers.go"), `""`, "EnqueueParams")
		only(t, "sync_handlers.go", "jobqueue.EnqueueParams", comp, "enqueueRebuildJob")
		_, comp = functionsUsing(parseGoFile(t, "recovery.go"), `""`, "EnqueueParams")
		only(t, "recovery.go", "jobqueue.EnqueueParams", comp)
		_, comp = functionsUsing(parseGoFile(t, "purge_refs.go"), `""`, "EnqueueParams")
		only(t, "purge_refs.go", "jobqueue.EnqueueParams", comp)
	})

	t.Run("callers_use_the_shared_helpers", func(t *testing.T) {
		recovery := parseGoFile(t, "recovery.go")
		calls := map[string][]string{
			"RunReset":         {"moveBranchToFork", "enqueueRebuildJob", "autoRebuildEnabledFor"},
			"moveBranchToFork": {"casMoveRef", "classifyCASError"},
			"classifyCASError": {"lookupRefSHA"},
			"RemoveBackup":     {"removeBackupRef"},
			"removeBackupRef":  {"lookupRefSHA"},
			"ReadReplacedSHA":  {"lookupRefSHA"},
		}
		for fn, want := range calls {
			got := callsOf(recovery, fn)
			for _, w := range want {
				if !got[w] {
					t.Errorf("recovery.go: %s does not call %s", fn, w)
				}
			}
		}

		if !callsOf(parseGoFile(t, "purge_refs.go"), "PurgeExpiredDeletedPatchesWithRefs")["removeBackupRef"] {
			t.Error("purge_refs.go: PurgeExpiredDeletedPatchesWithRefs does not call removeBackupRef")
		}
		refresh := parseGoFile(t, "patch_refresh.go")
		if !callsOf(refresh, "refreshOnePatch")["casMoveRef"] || !callsOf(refresh, "refreshOnePatch")["casUpdateRef"] {
			t.Error("patch_refresh.go: refreshOnePatch must use casMoveRef and casUpdateRef")
		}
		sync := parseGoFile(t, "sync_handlers.go")
		if !callsOf(sync, "enqueueRebuildIfNeeded")["enqueueRebuildJob"] || !callsOf(sync, "autoRebuildEnabled")["autoRebuildEnabledFor"] {
			t.Error("sync_handlers.go: the sync wrappers must call the shared enqueue helpers")
		}
	})
}
