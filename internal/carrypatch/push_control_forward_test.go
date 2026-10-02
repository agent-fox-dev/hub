package carrypatch

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitserver"
)

// ===========================================================================
// Forward-mode test infrastructure
// ===========================================================================

// forwardTestEnv extends rejectTestEnv with a local bare origin repository.
type forwardTestEnv struct {
	*rejectTestEnv
	bareOrigin string // path to the local bare origin repo
}

// newForwardTestEnv creates a full git server test environment with:
// - a local bare repository as the trunk's origin remote
// - forward mode enabled (PATCH_BRANCH_SOURCE=origin, PUSH_PATCHES_TO_ORIGIN=true)
// - the carry-patch pre-receive hook registered
func newForwardTestEnv(t *testing.T, getVar GetVariableFunc) *forwardTestEnv {
	t.Helper()

	// Create a local bare repository to act as the fork.
	bareOrigin := t.TempDir()
	runGitCmdR(t, "", "init", "--bare", "-b", "main", bareOrigin)

	gitURL := bareOrigin

	env := newRejectTestEnv(t, gitURL, getVar)

	// Add the bare origin as the trunk's origin remote.
	// First, push the trunk's initial commit to the bare origin so it has content.
	runGitCmdR(t, env.trunk, "remote", "add", "origin", bareOrigin)
	runGitCmdR(t, env.trunk, "push", "origin", "main")

	return &forwardTestEnv{
		rejectTestEnv: env,
		bareOrigin:    bareOrigin,
	}
}

// forwardGetVar returns a GetVariableFunc for forward mode.
func forwardGetVar() GetVariableFunc {
	return func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}
}

// fwdRefExists checks if a ref exists in a git repo.
func fwdRefExists(t *testing.T, repoPath, ref string) bool {
	t.Helper()
	_, err := gitCmdOutR(repoPath, "rev-parse", "--verify", ref)
	return err == nil
}

// fwdRefSHA returns the SHA of a ref in a git repo.
func fwdRefSHA(t *testing.T, repoPath, ref string) string {
	t.Helper()
	return runGitCmdR(t, repoPath, "rev-parse", ref)
}

// ===========================================================================
// TS-22-21 (integration): Forward mode pushes a create and a fast-forward
// update to a local bare origin before writing the hub ref
// Verifies: 22-REQ-4.1
// ===========================================================================

func TestForwardMode_TS22_21_CreateAndFastForward(t *testing.T) {
	getVar := forwardGetVar()
	env := newForwardTestEnv(t, getVar)

	// Register the hook with workspace root so it can open the trunk.
	gitserver.RegisterPreReceiveHook(nil) // clear first
	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable:   getVar,
		ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
		WorkspaceRoot: env.workspaceRoot,
	})
	gitserver.RegisterPreReceiveHook(hook)

	// Seed registered patch branch p1.
	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Clone the hub.
	clone := t.TempDir()
	runGitCmdR(t, "", "clone", env.remote, clone)

	// Test 1: Create p1 at commit A.
	runGitCmdR(t, clone, "checkout", "-b", "p1")
	addCommitR(t, clone, "p1.txt", "p1 initial")
	commitA := fwdRefSHA(t, clone, "HEAD")

	out, err := gitCmdOutR(clone, "push", "origin", "p1")
	if err != nil {
		t.Fatalf("push create p1 failed: %v\n%s", err, out)
	}

	// After the first push, p1 equals A in both the hub trunk and the bare origin.
	trunkP1 := fwdRefSHA(t, env.trunk, "refs/heads/p1")
	if trunkP1 != commitA {
		t.Errorf("trunk p1 = %s; want %s", trunkP1, commitA)
	}
	originP1 := fwdRefSHA(t, env.bareOrigin, "refs/heads/p1")
	if originP1 != commitA {
		t.Errorf("origin p1 = %s; want %s", originP1, commitA)
	}

	// Test 2: Fast-forward update p1 to commit B.
	addCommitR(t, clone, "p1-update.txt", "p1 update")
	commitB := fwdRefSHA(t, clone, "HEAD")

	out2, err2 := gitCmdOutR(clone, "push", "origin", "p1")
	if err2 != nil {
		t.Fatalf("push update p1 failed: %v\n%s", err2, out2)
	}

	trunkP1 = fwdRefSHA(t, env.trunk, "refs/heads/p1")
	if trunkP1 != commitB {
		t.Errorf("trunk p1 after update = %s; want %s", trunkP1, commitB)
	}
	originP1 = fwdRefSHA(t, env.bareOrigin, "refs/heads/p1")
	if originP1 != commitB {
		t.Errorf("origin p1 after update = %s; want %s", originP1, commitB)
	}

	// Test 3: Already-up-to-date forward succeeds.
	// Seed the origin with the same commit (it already has it from the forward).
	// Push the same commit again — should succeed (already up to date).
	out3, err3 := gitCmdOutR(clone, "push", "origin", "p1")
	// git push returns success when everything is up to date.
	if err3 != nil {
		// "Everything up-to-date" is not an error for git push.
		if !strings.Contains(out3, "Everything up-to-date") {
			t.Fatalf("push already-up-to-date failed: %v\n%s", err3, out3)
		}
	}
}

// ===========================================================================
// TS-22-22 (integration): The temporary refs/hub/forward/<branch> ref is
// absent after a successful and after a failed forward
// Verifies: 22-REQ-4.2
// ===========================================================================

func TestForwardMode_TS22_22_TempRefCleanedUp(t *testing.T) {
	getVar := forwardGetVar()
	env := newForwardTestEnv(t, getVar)

	gitserver.RegisterPreReceiveHook(nil)
	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable:   getVar,
		ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
		WorkspaceRoot: env.workspaceRoot,
	})
	gitserver.RegisterPreReceiveHook(hook)

	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Clone.
	clone := t.TempDir()
	runGitCmdR(t, "", "clone", env.remote, clone)

	// Test 1: Successful forward — temp ref should be absent.
	runGitCmdR(t, clone, "checkout", "-b", "p1")
	addCommitR(t, clone, "p1.txt", "p1 commit")

	out, err := gitCmdOutR(clone, "push", "origin", "p1")
	if err != nil {
		t.Fatalf("push p1 failed: %v\n%s", err, out)
	}

	// Check no refs/hub/forward/ refs exist in the trunk.
	forwardRefs, _ := gitCmdOutR(env.trunk, "for-each-ref", "--format=%(refname)", "refs/hub/forward/")
	if strings.TrimSpace(forwardRefs) != "" {
		t.Errorf("expected no refs/hub/forward/ refs after success; got: %s", forwardRefs)
	}

	// Test 2: Failed forward (non-fast-forward) — temp ref should be absent.
	// Advance origin's p1 ahead of the hub.
	// Create a divergent commit on the bare origin.
	tmpClone := t.TempDir()
	runGitCmdR(t, "", "clone", env.bareOrigin, tmpClone)
	runGitCmdR(t, tmpClone, "checkout", "p1")
	addCommitR(t, tmpClone, "diverge.txt", "diverge on origin")
	runGitCmdR(t, tmpClone, "push", "origin", "p1")

	// Now push a different commit from the hub client — should fail (non-ff).
	addCommitR(t, clone, "p1-diverge.txt", "diverge on hub")
	_, pushErr := gitCmdOutR(clone, "push", "origin", "p1")
	if pushErr == nil {
		t.Fatal("expected push to fail due to non-fast-forward")
	}

	// Check no refs/hub/forward/ refs exist in the trunk after failure.
	forwardRefs2, _ := gitCmdOutR(env.trunk, "for-each-ref", "--format=%(refname)", "refs/hub/forward/")
	if strings.TrimSpace(forwardRefs2) != "" {
		t.Errorf("expected no refs/hub/forward/ refs after failure; got: %s", forwardRefs2)
	}
}

// ===========================================================================
// TS-22-23 (unit): Forward uses ResolveCloneAuth credentials, a non-force
// refspec and a 120 second deadline
// Verifies: 22-REQ-4.3
// ===========================================================================

func TestForwardMode_TS22_23_PushOptionsAndDeadline(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	wsRoot := t.TempDir()
	trunkPath := filepath.Join(wsRoot, "myws", "trunk")

	// Create a trunk repo with an origin remote.
	if err := os.MkdirAll(trunkPath, 0o755); err != nil {
		t.Fatal(err)
	}
	bareOrigin := t.TempDir()
	runGitCmdR(t, "", "init", "--bare", "-b", "main", bareOrigin)
	runGitCmdR(t, "", "init", "-b", "main", trunkPath)
	runGitCmdR(t, trunkPath, "config", "user.name", "Test")
	runGitCmdR(t, trunkPath, "config", "user.email", "test@test.com")
	writeFileHelper(t, filepath.Join(trunkPath, "README.md"), "# test\n")
	runGitCmdR(t, trunkPath, "add", ".")
	runGitCmdR(t, trunkPath, "commit", "-m", "init")
	runGitCmdR(t, trunkPath, "remote", "add", "origin", bareOrigin)
	runGitCmdR(t, trunkPath, "push", "origin", "main")

	seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
		"carry_patch", "main-int", bareOrigin)
	seedPatch(t, db, "p1", "myws", "feature/a", 1, "active")

	// Track resolver calls and push options.
	var resolverCalled bool
	var resolverSlug string
	var mu sync.Mutex
	var capturedOpts *git.PushOptions
	var capturedCtx context.Context

	resolver := func(slug string) (transport.AuthMethod, error) {
		mu.Lock()
		defer mu.Unlock()
		resolverCalled = true
		resolverSlug = slug
		return nil, nil
	}

	// Create a push seam that captures the options.
	pushSeam := func(ctx context.Context, repo *git.Repository, opts *git.PushOptions) error {
		mu.Lock()
		defer mu.Unlock()
		capturedOpts = opts
		capturedCtx = ctx
		// Actually perform the push so the test is realistic.
		err := repo.PushContext(ctx, opts)
		return err
	}

	hook := newPreReceiveHookForTest(PreReceiveHookDeps{
		GetVariable:   forwardGetVar(),
		ResolveAuth:   resolver,
		WorkspaceRoot: wsRoot,
	}, slog.Default(), pushSeam)

	// Create a commit on the branch in the trunk so the push has something.
	runGitCmdR(t, trunkPath, "checkout", "-b", "feature/a")
	addCommitR(t, trunkPath, "feature-a.txt", "feature a commit")
	newHash := plumbing.NewHash(fwdRefSHA(t, trunkPath, "HEAD"))
	runGitCmdR(t, trunkPath, "checkout", "main")

	ctx := context.Background()
	actor := &apikit.AuthInfo{UserID: "user-1"}
	upd := gitserver.RefUpdate{
		Name: plumbing.ReferenceName("refs/heads/feature/a"),
		Old:  plumbing.ZeroHash,
		New:  newHash,
	}

	err := hook(ctx, db, "myws", actor, upd)
	if err != nil {
		t.Fatalf("hook returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Assert resolver was called with the workspace slug.
	if !resolverCalled {
		t.Error("resolver was not called")
	}
	if resolverSlug != "myws" {
		t.Errorf("resolver slug = %q; want %q", resolverSlug, "myws")
	}

	// Assert push options.
	if capturedOpts == nil {
		t.Fatal("push options were not captured")
	}
	if capturedOpts.RemoteName != "origin" {
		t.Errorf("RemoteName = %q; want %q", capturedOpts.RemoteName, "origin")
	}
	if capturedOpts.Force {
		t.Error("Force should be false")
	}
	if len(capturedOpts.RefSpecs) != 1 {
		t.Fatalf("expected 1 refspec; got %d", len(capturedOpts.RefSpecs))
	}
	refspec := string(capturedOpts.RefSpecs[0])
	if strings.HasPrefix(refspec, "+") {
		t.Errorf("refspec starts with '+' (force): %s", refspec)
	}

	// Assert the context has a deadline about 120 seconds from now.
	if capturedCtx == nil {
		t.Fatal("context was not captured")
	}
	deadline, ok := capturedCtx.Deadline()
	if !ok {
		t.Fatal("context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining < 115*time.Second || remaining > 125*time.Second {
		t.Errorf("deadline remaining = %v; want ~120s", remaining)
	}
}

// ===========================================================================
// TS-22-24 (unit): Credential resolution failure rejects the ref without
// contacting origin
// Verifies: 22-REQ-4.4
// ===========================================================================

func TestForwardMode_TS22_24_CredentialFailureRejects(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	wsRoot := t.TempDir()
	trunkPath := filepath.Join(wsRoot, "myws", "trunk")
	if err := os.MkdirAll(trunkPath, 0o755); err != nil {
		t.Fatal(err)
	}

	bareOrigin := t.TempDir()
	runGitCmdR(t, "", "init", "--bare", "-b", "main", bareOrigin)
	runGitCmdR(t, "", "init", "-b", "main", trunkPath)
	runGitCmdR(t, trunkPath, "config", "user.name", "Test")
	runGitCmdR(t, trunkPath, "config", "user.email", "test@test.com")
	writeFileHelper(t, filepath.Join(trunkPath, "README.md"), "# test\n")
	runGitCmdR(t, trunkPath, "add", ".")
	runGitCmdR(t, trunkPath, "commit", "-m", "init")
	runGitCmdR(t, trunkPath, "remote", "add", "origin", bareOrigin)
	runGitCmdR(t, trunkPath, "push", "origin", "main")

	seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
		"carry_patch", "main-int", bareOrigin)
	seedPatch(t, db, "p1", "myws", "feature/a", 1, "active")

	// Resolver returns an error.
	resolver := func(slug string) (transport.AuthMethod, error) {
		return nil, fmt.Errorf("credential store unavailable")
	}

	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable:   forwardGetVar(),
		ResolveAuth:   resolver,
		WorkspaceRoot: wsRoot,
	})

	ctx := context.Background()
	actor := &apikit.AuthInfo{UserID: "user-1"}
	upd := gitserver.RefUpdate{
		Name: plumbing.ReferenceName("refs/heads/feature/a"),
		Old:  plumbing.ZeroHash,
		New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
	}

	err := hook(ctx, db, "myws", actor, upd)
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if err.Error() != "failed to resolve origin credentials" {
		t.Errorf("error = %q; want %q", err.Error(), "failed to resolve origin credentials")
	}

	// Verify origin received nothing new.
	originRefs, _ := gitCmdOutR(bareOrigin, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if strings.Contains(originRefs, "feature/a") {
		t.Error("origin should not have feature/a after credential failure")
	}
}

// ===========================================================================
// TS-22-25 (integration): A non-fast-forward forward is rejected with the
// origin error text and leaves hub and fork refs unchanged
// Verifies: 22-REQ-4.5, 22-REQ-8.2
// ===========================================================================

func TestForwardMode_TS22_25_NonFastForwardRejected(t *testing.T) {
	getVar := forwardGetVar()
	env := newForwardTestEnv(t, getVar)

	gitserver.RegisterPreReceiveHook(nil)
	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable:   getVar,
		ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
		WorkspaceRoot: env.workspaceRoot,
	})
	gitserver.RegisterPreReceiveHook(hook)

	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Clone and create p1.
	clone := t.TempDir()
	runGitCmdR(t, "", "clone", env.remote, clone)
	runGitCmdR(t, clone, "checkout", "-b", "p1")
	addCommitR(t, clone, "p1.txt", "p1 initial")

	// Push p1 to hub (and origin via forward).
	out, err := gitCmdOutR(clone, "push", "origin", "p1")
	if err != nil {
		t.Fatalf("initial push failed: %v\n%s", err, out)
	}

	hubP1Before := fwdRefSHA(t, env.trunk, "refs/heads/p1")

	// Advance origin's p1 ahead of the hub with a divergent commit.
	tmpClone := t.TempDir()
	runGitCmdR(t, "", "clone", env.bareOrigin, tmpClone)
	runGitCmdR(t, tmpClone, "checkout", "p1")
	addCommitR(t, tmpClone, "origin-ahead.txt", "origin moved ahead")
	runGitCmdR(t, tmpClone, "push", "origin", "p1")
	originP1Before := fwdRefSHA(t, env.bareOrigin, "refs/heads/p1")

	// Now push a different commit from the hub client — should fail (non-ff).
	addCommitR(t, clone, "hub-diverge.txt", "hub diverge")
	out2, err2 := gitCmdOutR(clone, "push", "origin", "p1")
	if err2 == nil {
		t.Fatal("expected push to fail due to non-fast-forward")
	}

	// The client should show 'remote rejected' with 'origin rejected push: '.
	if !strings.Contains(out2, "remote rejected") {
		t.Errorf("expected 'remote rejected' in output; got:\n%s", out2)
	}
	if !strings.Contains(out2, "origin rejected push: ") {
		t.Errorf("expected 'origin rejected push: ' in output; got:\n%s", out2)
	}

	// Hub p1 still equals old SHA.
	hubP1After := fwdRefSHA(t, env.trunk, "refs/heads/p1")
	if hubP1After != hubP1Before {
		t.Errorf("hub p1 changed: %s → %s", hubP1Before, hubP1After)
	}

	// Origin p1 still equals its own tip.
	originP1After := fwdRefSHA(t, env.bareOrigin, "refs/heads/p1")
	if originP1After != originP1Before {
		t.Errorf("origin p1 changed: %s → %s", originP1Before, originP1After)
	}
}

// ===========================================================================
// TS-22-26 (integration): An unreachable origin rejects the update, leaves
// the hub ref unchanged and logs a warning without userinfo
// Verifies: 22-REQ-4.5, 22-REQ-4.6
// ===========================================================================

func TestForwardMode_TS22_26_UnreachableOriginRejectsAndLogs(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	wsRoot := t.TempDir()
	trunkPath := filepath.Join(wsRoot, "myws", "trunk")
	if err := os.MkdirAll(trunkPath, 0o755); err != nil {
		t.Fatal(err)
	}

	// Initialize trunk with an origin remote pointing to a nonexistent path.
	runGitCmdR(t, "", "init", "-b", "main", trunkPath)
	runGitCmdR(t, trunkPath, "config", "user.name", "Test")
	runGitCmdR(t, trunkPath, "config", "user.email", "test@test.com")
	writeFileHelper(t, filepath.Join(trunkPath, "README.md"), "# test\n")
	runGitCmdR(t, trunkPath, "add", ".")
	runGitCmdR(t, trunkPath, "commit", "-m", "init")

	// Use a URL with userinfo pointing to a nonexistent path.
	unreachableURL := "/tmp/nonexistent-repo-" + fmt.Sprintf("%d", time.Now().UnixNano())
	runGitCmdR(t, trunkPath, "remote", "add", "origin", unreachableURL)

	// Create a branch to push.
	runGitCmdR(t, trunkPath, "checkout", "-b", "feature/a")
	addCommitR(t, trunkPath, "feature-a.txt", "feature a commit")
	newHash := plumbing.NewHash(fwdRefSHA(t, trunkPath, "HEAD"))
	runGitCmdR(t, trunkPath, "checkout", "main")

	seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
		"carry_patch", "main-int", "https://user:pw@example.com/fork.git")
	seedPatch(t, db, "p1", "myws", "feature/a", 1, "active")

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	hook := newPreReceiveHookWithLogger(PreReceiveHookDeps{
		GetVariable:   forwardGetVar(),
		ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
		WorkspaceRoot: wsRoot,
	}, logger)

	ctx := context.Background()
	actor := &apikit.AuthInfo{UserID: "u-42"}
	upd := gitserver.RefUpdate{
		Name: plumbing.ReferenceName("refs/heads/feature/a"),
		Old:  plumbing.ZeroHash,
		New:  newHash,
	}

	err := hook(ctx, db, "myws", actor, upd)
	if err == nil {
		t.Fatal("expected non-nil error for unreachable origin")
	}

	errMsg := err.Error()
	if !strings.HasPrefix(errMsg, "origin rejected push: ") {
		t.Errorf("error should start with 'origin rejected push: '; got: %s", errMsg)
	}
	// Verify no 'pw' credential text appears in the error.
	if strings.Contains(errMsg, "pw") {
		t.Errorf("error contains credential text 'pw': %s", errMsg)
	}

	// Check warn-level log.
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "WARN") && !strings.Contains(logOutput, "warn") {
		t.Errorf("expected WARN level log; got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "myws") {
		t.Errorf("expected slug 'myws' in log; got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "feature/a") {
		t.Errorf("expected branch 'feature/a' in log; got:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "u-42") {
		t.Errorf("expected user 'u-42' in log; got:\n%s", logOutput)
	}
	// Verify no 'pw' credential text appears in the log.
	if strings.Contains(logOutput, "pw") {
		t.Errorf("log contains credential text 'pw': %s", logOutput)
	}
}

// ===========================================================================
// TS-22-27 (integration): Forward mode rejects deleting a registered patch
// branch without contacting origin
// Verifies: 22-REQ-4.7
// ===========================================================================

func TestForwardMode_TS22_27_DeleteRejected(t *testing.T) {
	getVar := forwardGetVar()
	env := newForwardTestEnv(t, getVar)

	gitserver.RegisterPreReceiveHook(nil)
	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable:   getVar,
		ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
		WorkspaceRoot: env.workspaceRoot,
	})
	gitserver.RegisterPreReceiveHook(hook)

	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Clone and create p1.
	clone := t.TempDir()
	runGitCmdR(t, "", "clone", env.remote, clone)
	runGitCmdR(t, clone, "checkout", "-b", "p1")
	addCommitR(t, clone, "p1.txt", "p1 commit")

	// Push p1 to hub (and origin via forward).
	out, err := gitCmdOutR(clone, "push", "origin", "p1")
	if err != nil {
		t.Fatalf("initial push failed: %v\n%s", err, out)
	}

	// Verify p1 exists on both.
	if !fwdRefExists(t, env.trunk, "refs/heads/p1") {
		t.Fatal("p1 should exist on trunk")
	}
	if !fwdRefExists(t, env.bareOrigin, "refs/heads/p1") {
		t.Fatal("p1 should exist on origin")
	}

	// Now try to delete p1. We need to push something else alongside the
	// delete because go-git can't handle delete-only pushes (no packfile).
	runGitCmdR(t, clone, "checkout", "main")
	runGitCmdR(t, clone, "checkout", "-b", "feature")
	addCommitR(t, clone, "feature.txt", "feature commit")

	out2, _ := gitCmdOutR(clone, "push", "origin", ":refs/heads/p1", "feature")

	// The delete of p1 should be rejected.
	if !strings.Contains(out2, "branch is synced from origin; delete it on the fork instead") {
		t.Errorf("expected delete rejection message; got:\n%s", out2)
	}

	// Verify p1 still exists on both.
	if !fwdRefExists(t, env.trunk, "refs/heads/p1") {
		t.Error("p1 should still exist on trunk after rejected delete")
	}
	if !fwdRefExists(t, env.bareOrigin, "refs/heads/p1") {
		t.Error("p1 should still exist on origin after rejected delete")
	}

	// Verify feature was written (not affected by the rejection).
	if !fwdRefExists(t, env.trunk, "refs/heads/feature") {
		t.Error("feature should exist on trunk")
	}
}

// ===========================================================================
// TS-22-28 (integration): A mixed push forwards the patch branch and passes
// an unregistered branch through unchanged
// Verifies: 22-REQ-4.8
// ===========================================================================

func TestForwardMode_TS22_28_MixedPushForwardsOnlyRegistered(t *testing.T) {
	getVar := forwardGetVar()
	env := newForwardTestEnv(t, getVar)

	gitserver.RegisterPreReceiveHook(nil)
	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable:   getVar,
		ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
		WorkspaceRoot: env.workspaceRoot,
	})
	gitserver.RegisterPreReceiveHook(hook)

	// Only p1 is registered.
	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Clone.
	clone := t.TempDir()
	runGitCmdR(t, "", "clone", env.remote, clone)

	// Create p1 (registered) and feature (unregistered).
	runGitCmdR(t, clone, "checkout", "-b", "p1")
	addCommitR(t, clone, "p1.txt", "p1 commit")

	runGitCmdR(t, clone, "checkout", "main")
	runGitCmdR(t, clone, "checkout", "-b", "feature")
	addCommitR(t, clone, "feature.txt", "feature commit")

	// Push both in one push.
	out, err := gitCmdOutR(clone, "push", "origin", "p1", "feature")
	if err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	// Both refs should be written on the hub.
	if !fwdRefExists(t, env.trunk, "refs/heads/p1") {
		t.Error("p1 should exist on trunk")
	}
	if !fwdRefExists(t, env.trunk, "refs/heads/feature") {
		t.Error("feature should exist on trunk")
	}

	// p1 should exist on the origin (forwarded).
	if !fwdRefExists(t, env.bareOrigin, "refs/heads/p1") {
		t.Error("p1 should exist on origin (forwarded)")
	}

	// feature should NOT exist on the origin (not forwarded).
	if fwdRefExists(t, env.bareOrigin, "refs/heads/feature") {
		t.Error("feature should NOT exist on origin (not a registered patch)")
	}
}

// newPreReceiveHookForTest creates a pre-receive hook with a custom push
// function for testing push options and context.
func newPreReceiveHookForTest(deps PreReceiveHookDeps, logger *slog.Logger, push PushContextFunc) gitserver.PreReceiveHookFunc {
	return newPreReceiveHookInternalWithPush(deps, logger, push)
}

// Ensure the unused import is used.
var _ = config.RefSpec("")
