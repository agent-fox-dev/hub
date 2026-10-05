package carrypatch

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// Mirror test infrastructure
// ===========================================================================

// mirrorTestEnv holds the test infrastructure for hub-mode mirror tests.
type mirrorTestEnv struct {
	db            *sql.DB
	queue         *jobqueue.Queue
	workspaceRoot string
	bareOrigin    string // path to the local bare origin repo
	trunk         string // path to the workspace trunk repo
}

// newMirrorTestEnv creates a test environment with:
// - in-memory SQLite DB with workspaces and patches tables
// - a local bare repository as the trunk's origin remote
// - a carry_patch workspace "myws"
func newMirrorTestEnv(t *testing.T) *mirrorTestEnv {
	t.Helper()

	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey: %v", err)
	}

	q, err := jobqueue.New(db, nopLogger())
	if err != nil {
		t.Fatalf("jobqueue.New: %v", err)
	}
	if err := q.Register("rebuild", func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, false, nil
	}, nil); err != nil {
		t.Fatalf("register rebuild: %v", err)
	}

	wsRoot := t.TempDir()

	// Create a local bare origin.
	bareOrigin := t.TempDir()
	runGitCmdR(t, "", "init", "--bare", "-b", "main", bareOrigin)

	// Create the trunk repo.
	trunkPath := filepath.Join(wsRoot, "myws", "trunk")
	if err := os.MkdirAll(trunkPath, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	runGitCmdR(t, "", "init", "-b", "main", trunkPath)
	runGitCmdR(t, trunkPath, "config", "user.name", "Test")
	runGitCmdR(t, trunkPath, "config", "user.email", "test@test.com")
	writeFileHelper(t, filepath.Join(trunkPath, "README.md"), "# test\n")
	runGitCmdR(t, trunkPath, "add", ".")
	runGitCmdR(t, trunkPath, "commit", "-m", "init")
	runGitCmdR(t, trunkPath, "remote", "add", "origin", bareOrigin)
	runGitCmdR(t, trunkPath, "push", "origin", "main")

	// Seed workspace.
	seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
		"carry_patch", "main-int", bareOrigin)

	return &mirrorTestEnv{
		db:            db,
		queue:         q,
		workspaceRoot: wsRoot,
		bareOrigin:    bareOrigin,
		trunk:         trunkPath,
	}
}

// recordingEmitter records emitted events for test assertions.
type recordingEmitter struct {
	mu     sync.Mutex
	events []audit.HubEvent
	err    error // if set, Emit returns this error
}

func (e *recordingEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	if e.err != nil {
		return e.err
	}
	return nil
}

func (e *recordingEmitter) Events() []audit.HubEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	cp := make([]audit.HubEvent, len(e.events))
	copy(cp, e.events)
	return cp
}

// mirrorRefSHA returns the SHA of a ref in a git repo.
func mirrorRefSHA(t *testing.T, repoPath, ref string) string {
	t.Helper()
	return runGitCmdR(t, repoPath, "rev-parse", ref)
}

// mirrorRefExists checks if a ref exists in a git repo.
func mirrorRefExists(t *testing.T, repoPath, ref string) bool {
	t.Helper()
	_, err := gitCmdOutR(repoPath, "rev-parse", "--verify", ref)
	return err == nil
}

// ===========================================================================
// TS-22-34 (integration): Hub-mode mirror force-pushes a created branch
// and a rewritten branch to the local bare origin
// Verifies: 22-REQ-6.1
// ===========================================================================

func TestMirror_TS22_34_CreateAndRewrite(t *testing.T) {
	env := newMirrorTestEnv(t)
	emitter := &recordingEmitter{}

	getVar := func(scope, slug, key string) (string, error) {
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		// PATCH_BRANCH_SOURCE unset → hub mode (default)
		return "", fmt.Errorf("not found")
	}

	seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")

	// Create p1 on the trunk.
	runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
	addCommitR(t, env.trunk, "p1.txt", "p1 initial")
	tipA := mirrorRefSHA(t, env.trunk, "refs/heads/p1")
	runGitCmdR(t, env.trunk, "checkout", "main")

	hook := NewPostPushRebuildHook(
		env.queue,
		getVar,
		PostPushMirrorDeps{
			ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
			WorkspaceRoot: env.workspaceRoot,
			Audit:         emitter,
		},
	)

	// Run the hook for p1 create.
	hook(env.db, "myws", []string{"p1"})

	// After the first hook run, origin p1 equals the hub tip.
	if !mirrorRefExists(t, env.bareOrigin, "refs/heads/p1") {
		t.Fatal("origin should have p1 after mirror")
	}
	originP1 := mirrorRefSHA(t, env.bareOrigin, "refs/heads/p1")
	if originP1 != tipA {
		t.Errorf("origin p1 = %s; want %s", originP1, tipA)
	}

	// Rewrite p1 with a diverging commit (force push scenario).
	runGitCmdR(t, env.trunk, "checkout", "p1")
	runGitCmdR(t, env.trunk, "reset", "--hard", "HEAD~1")
	addCommitR(t, env.trunk, "p1-rewrite.txt", "p1 rewritten")
	tipB := mirrorRefSHA(t, env.trunk, "refs/heads/p1")
	runGitCmdR(t, env.trunk, "checkout", "main")

	if tipB == tipA {
		t.Fatal("rewrite should produce a different SHA")
	}

	// Run the hook again for the rewrite.
	hook(env.db, "myws", []string{"p1"})

	// After the rewrite, origin p1 equals the new hub tip (forced update).
	originP1 = mirrorRefSHA(t, env.bareOrigin, "refs/heads/p1")
	if originP1 != tipB {
		t.Errorf("origin p1 after rewrite = %s; want %s", originP1, tipB)
	}
}

// ===========================================================================
// TS-22-35 (integration): The rebuild enqueue runs first and the mirror
// runs afterwards, including when AUTO_REBUILD_AFTER_PUSH is false
// Verifies: 22-REQ-6.2
// ===========================================================================

func TestMirror_TS22_35_EnqueueBeforeMirror(t *testing.T) {
	env := newMirrorTestEnv(t)
	emitter := &recordingEmitter{}

	seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")

	// Create p1 on the trunk.
	runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
	addCommitR(t, env.trunk, "p1.txt", "p1 initial")
	runGitCmdR(t, env.trunk, "checkout", "main")

	// Scenario 1: rebuild enqueued (default AUTO_REBUILD_AFTER_PUSH).
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}

	hook := NewPostPushRebuildHook(
		env.queue,
		getVar,
		PostPushMirrorDeps{
			ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
			WorkspaceRoot: env.workspaceRoot,
			Audit:         emitter,
		},
	)

	hook(env.db, "myws", []string{"p1"})

	// Rebuild should be enqueued.
	count := countActiveRebuildJobs(t, env.db, "myws")
	if count < 1 {
		t.Errorf("expected >= 1 rebuild job; got %d", count)
	}

	// Origin should have p1 (mirror ran after enqueue).
	if !mirrorRefExists(t, env.bareOrigin, "refs/heads/p1") {
		t.Fatal("origin should have p1 after mirror")
	}

	// Scenario 2: AUTO_REBUILD_AFTER_PUSH=false — no rebuild but mirror still runs.
	// Reset: delete origin p1 and clear jobs.
	runGitCmdR(t, env.bareOrigin, "branch", "-D", "p1")
	mustExecMirror(t, env.db, `DELETE FROM jobs WHERE type = 'rebuild'`)

	getVarNoRebuild := func(scope, slug, key string) (string, error) {
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		if key == "AUTO_REBUILD_AFTER_PUSH" {
			return "false", nil
		}
		return "", fmt.Errorf("not found")
	}

	hook2 := NewPostPushRebuildHook(
		env.queue,
		getVarNoRebuild,
		PostPushMirrorDeps{
			ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
			WorkspaceRoot: env.workspaceRoot,
			Audit:         emitter,
		},
	)

	hook2(env.db, "myws", []string{"p1"})

	// No rebuild job should be enqueued.
	count2 := countActiveRebuildJobs(t, env.db, "myws")
	if count2 != 0 {
		t.Errorf("expected 0 rebuild jobs with AUTO_REBUILD_AFTER_PUSH=false; got %d", count2)
	}

	// Origin should still have p1 (mirror ran even without rebuild).
	if !mirrorRefExists(t, env.bareOrigin, "refs/heads/p1") {
		t.Fatal("origin should have p1 even when AUTO_REBUILD_AFTER_PUSH=false")
	}
}

// ===========================================================================
// TS-22-36 (integration): A failing mirror does not prevent the rebuild or
// the other branches, and an enqueue failure does not stop the mirror
// Verifies: 22-REQ-6.3
// ===========================================================================

func TestMirror_TS22_36_FailingMirrorAndEnqueue(t *testing.T) {
	env := newMirrorTestEnv(t)
	emitter := &recordingEmitter{}

	seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")
	seedPatch(t, env.db, "p2-id", "myws", "p2", 2, "active")

	// Create p1 and p2 on the trunk.
	runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
	addCommitR(t, env.trunk, "p1.txt", "p1 commit")
	runGitCmdR(t, env.trunk, "checkout", "main")

	runGitCmdR(t, env.trunk, "checkout", "-b", "p2")
	addCommitR(t, env.trunk, "p2.txt", "p2 commit")
	runGitCmdR(t, env.trunk, "checkout", "main")

	// Make origin refuse p2 by creating a pre-receive hook on the bare origin.
	hookDir := filepath.Join(env.bareOrigin, "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a pre-receive hook that rejects pushes to refs/heads/p2.
	hookScript := `#!/bin/sh
while read oldrev newrev refname; do
  if [ "$refname" = "refs/heads/p2" ]; then
    echo "refusing p2" >&2
    exit 1
  fi
done
exit 0
`
	hookPath := filepath.Join(hookDir, "pre-receive")
	writeFileHelper(t, hookPath, hookScript)
	if err := os.Chmod(hookPath, 0o755); err != nil {
		t.Fatal(err)
	}

	getVar := func(scope, slug, key string) (string, error) {
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}

	hook := NewPostPushRebuildHook(
		env.queue,
		getVar,
		PostPushMirrorDeps{
			ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
			WorkspaceRoot: env.workspaceRoot,
			Audit:         emitter,
		},
	)

	// Run the hook with both branches.
	hook(env.db, "myws", []string{"p1", "p2"})

	// A rebuild job should be enqueued despite p2 failure.
	count := countActiveRebuildJobs(t, env.db, "myws")
	if count < 1 {
		t.Errorf("expected >= 1 rebuild job; got %d", count)
	}

	// p1 should be mirrored to origin.
	if !mirrorRefExists(t, env.bareOrigin, "refs/heads/p1") {
		t.Error("origin should have p1")
	}

	// p2 should NOT be on origin (rejected by pre-receive hook).
	if mirrorRefExists(t, env.bareOrigin, "refs/heads/p2") {
		t.Error("origin should NOT have p2 (rejected)")
	}

	// Now test: enqueue failure does not stop the mirror.
	// Remove the pre-receive hook so p2 can be mirrored.
	if err := os.Remove(hookPath); err != nil {
		t.Fatal(err)
	}

	// Close the queue to make enqueue fail.
	env.queue.Stop()

	// Create a new queue that will fail on enqueue.
	failDB := openTestDB(t)
	if err := jobqueue.InitSchema(failDB); err != nil {
		t.Fatal(err)
	}
	if err := jobqueue.MigrateGroupKey(failDB); err != nil {
		t.Fatal(err)
	}
	failQ, err := jobqueue.New(failDB, nopLogger())
	if err != nil {
		t.Fatal(err)
	}
	// Don't register rebuild handler — enqueue will fail with "unknown job type".

	hook2 := NewPostPushRebuildHook(
		failQ,
		getVar,
		PostPushMirrorDeps{
			ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
			WorkspaceRoot: env.workspaceRoot,
			Audit:         emitter,
		},
	)

	hook2(env.db, "myws", []string{"p1", "p2"})

	// Both branches should be mirrored despite enqueue failure.
	if !mirrorRefExists(t, env.bareOrigin, "refs/heads/p1") {
		t.Error("origin should have p1 after enqueue failure")
	}
	if !mirrorRefExists(t, env.bareOrigin, "refs/heads/p2") {
		t.Error("origin should have p2 after enqueue failure")
	}
}

// ===========================================================================
// TS-22-37 (integration): No mirror for disabled variable, origin source,
// unregistered branch, integration branch or standard workspace
// Verifies: 22-REQ-6.4
// ===========================================================================

func TestMirror_TS22_37_NoMirrorCases(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		branch    string
		getVar    GetVariableFunc
		seedPatch bool
		patchName string
		intBranch string
	}{
		{
			name:   "variable unset",
			mode:   "carry_patch",
			branch: "p1",
			getVar: func(_, _, key string) (string, error) {
				return "", fmt.Errorf("not found")
			},
			seedPatch: true,
			patchName: "p1",
			intBranch: "main-int",
		},
		{
			name:   "variable True (capitalized)",
			mode:   "carry_patch",
			branch: "p1",
			getVar: func(_, _, key string) (string, error) {
				if key == "PUSH_PATCHES_TO_ORIGIN" {
					return "True", nil
				}
				return "", fmt.Errorf("not found")
			},
			seedPatch: true,
			patchName: "p1",
			intBranch: "main-int",
		},
		{
			name:   "source origin with variable true",
			mode:   "carry_patch",
			branch: "p1",
			getVar: func(_, _, key string) (string, error) {
				if key == "PUSH_PATCHES_TO_ORIGIN" {
					return "true", nil
				}
				if key == "PATCH_BRANCH_SOURCE" {
					return "origin", nil
				}
				return "", fmt.Errorf("not found")
			},
			seedPatch: true,
			patchName: "p1",
			intBranch: "main-int",
		},
		{
			name:   "unregistered branch",
			mode:   "carry_patch",
			branch: "feature",
			getVar: func(_, _, key string) (string, error) {
				if key == "PUSH_PATCHES_TO_ORIGIN" {
					return "true", nil
				}
				return "", fmt.Errorf("not found")
			},
			seedPatch: false,
			intBranch: "main-int",
		},
		{
			name:   "integration branch",
			mode:   "carry_patch",
			branch: "main-int",
			getVar: func(_, _, key string) (string, error) {
				if key == "PUSH_PATCHES_TO_ORIGIN" {
					return "true", nil
				}
				return "", fmt.Errorf("not found")
			},
			seedPatch: true,
			patchName: "main-int",
			intBranch: "main-int",
		},
		{
			name:   "standard workspace",
			mode:   "standard",
			branch: "p1",
			getVar: func(_, _, key string) (string, error) {
				if key == "PUSH_PATCHES_TO_ORIGIN" {
					return "true", nil
				}
				return "", fmt.Errorf("not found")
			},
			seedPatch: true,
			patchName: "p1",
			intBranch: "main-int",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			createWorkspacesTable(t, db)
			createPatchesTable(t, db)

			if err := jobqueue.InitSchema(db); err != nil {
				t.Fatal(err)
			}
			if err := jobqueue.MigrateGroupKey(db); err != nil {
				t.Fatal(err)
			}
			q, err := jobqueue.New(db, nopLogger())
			if err != nil {
				t.Fatal(err)
			}
			if err := q.Register("rebuild", func(_ context.Context, _ json.RawMessage) (any, bool, error) {
				return nil, false, nil
			}, nil); err != nil {
				t.Fatal(err)
			}

			wsRoot := t.TempDir()
			bareOrigin := t.TempDir()
			runGitCmdR(t, "", "init", "--bare", "-b", "main", bareOrigin)

			trunkPath := filepath.Join(wsRoot, "myws", "trunk")
			if err := os.MkdirAll(trunkPath, 0o755); err != nil {
				t.Fatal(err)
			}
			runGitCmdR(t, "", "init", "-b", "main", trunkPath)
			runGitCmdR(t, trunkPath, "config", "user.name", "Test")
			runGitCmdR(t, trunkPath, "config", "user.email", "test@test.com")
			writeFileHelper(t, filepath.Join(trunkPath, "README.md"), "# test\n")
			runGitCmdR(t, trunkPath, "add", ".")
			runGitCmdR(t, trunkPath, "commit", "-m", "init")
			runGitCmdR(t, trunkPath, "remote", "add", "origin", bareOrigin)
			runGitCmdR(t, trunkPath, "push", "origin", "main")

			// Create the branch on the trunk.
			runGitCmdR(t, trunkPath, "checkout", "-b", tc.branch)
			addCommitR(t, trunkPath, tc.branch+".txt", tc.branch+" commit")
			runGitCmdR(t, trunkPath, "checkout", "main")

			seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
				tc.mode, tc.intBranch, bareOrigin)

			if tc.seedPatch {
				name := tc.patchName
				if name == "" {
					name = tc.branch
				}
				seedPatch(t, db, "p-"+name, "myws", name, 1, "active")
			}

			emitter := &recordingEmitter{}
			hook := NewPostPushRebuildHook(
				q,
				tc.getVar,
				PostPushMirrorDeps{
					ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
					WorkspaceRoot: wsRoot,
					Audit:         emitter,
				},
			)

			hook(db, "myws", []string{tc.branch})

			// The origin bare repository should NOT receive the branch
			// (only "main" should exist from the initial push).
			refs, _ := gitCmdOutR(bareOrigin, "for-each-ref", "--format=%(refname)", "refs/heads/")
			for _, line := range strings.Split(refs, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || line == "refs/heads/main" {
					continue
				}
				t.Errorf("unexpected ref on origin: %s", line)
			}
		})
	}
}

// ===========================================================================
// TS-22-38 (unit): Branch deletions are never mirrored
// Verifies: 22-REQ-6.5
// ===========================================================================

func TestMirror_TS22_38_DeletesNotMirrored(t *testing.T) {
	// This test verifies that extractPushedBranches excludes deletes,
	// so the post-push hook never receives deleted branches.
	// We test this by verifying that the hook, when called with an empty
	// branches list (as would happen after a delete-only push), does not
	// mirror anything.

	env := newMirrorTestEnv(t)
	emitter := &recordingEmitter{}

	seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")

	// Create p1 on both trunk and origin.
	runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
	addCommitR(t, env.trunk, "p1.txt", "p1 commit")
	runGitCmdR(t, env.trunk, "push", "origin", "p1")
	runGitCmdR(t, env.trunk, "checkout", "main")

	// Verify p1 exists on origin.
	if !mirrorRefExists(t, env.bareOrigin, "refs/heads/p1") {
		t.Fatal("p1 should exist on origin before test")
	}

	getVar := func(_, _, key string) (string, error) {
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}

	hook := NewPostPushRebuildHook(
		env.queue,
		getVar,
		PostPushMirrorDeps{
			ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
			WorkspaceRoot: env.workspaceRoot,
			Audit:         emitter,
		},
	)

	// Call the hook with an empty branches list (simulating a delete-only push
	// where extractPushedBranches returns nothing).
	hook(env.db, "myws", []string{})

	// p1 should still exist on origin (not deleted).
	if !mirrorRefExists(t, env.bareOrigin, "refs/heads/p1") {
		t.Error("p1 should still exist on origin after delete-only push")
	}
}

// ===========================================================================
// TS-22-39 (unit): Mirror works only when its dependencies are supplied;
// with any nil it is disabled and rebuild behaviour is unchanged
// Verifies: 22-REQ-6.6
// ===========================================================================

func TestMirror_TS22_39_NilDependencies(t *testing.T) {
	type depCase struct {
		name string
		deps PostPushMirrorDeps
	}

	cases := []depCase{
		{
			name: "all supplied",
			deps: PostPushMirrorDeps{
				ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
				WorkspaceRoot: "PLACEHOLDER", // replaced per test
				Audit:         &recordingEmitter{},
			},
		},
		{
			name: "nil resolver",
			deps: PostPushMirrorDeps{
				ResolveAuth:   nil,
				WorkspaceRoot: "PLACEHOLDER",
				Audit:         &recordingEmitter{},
			},
		},
		{
			name: "empty workspace root",
			deps: PostPushMirrorDeps{
				ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
				WorkspaceRoot: "",
				Audit:         &recordingEmitter{},
			},
		},
		{
			name: "nil emitter",
			deps: PostPushMirrorDeps{
				ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
				WorkspaceRoot: "PLACEHOLDER",
				Audit:         nil,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			createWorkspacesTable(t, db)
			createPatchesTable(t, db)

			if err := jobqueue.InitSchema(db); err != nil {
				t.Fatal(err)
			}
			if err := jobqueue.MigrateGroupKey(db); err != nil {
				t.Fatal(err)
			}
			q, err := jobqueue.New(db, nopLogger())
			if err != nil {
				t.Fatal(err)
			}
			if err := q.Register("rebuild", func(_ context.Context, _ json.RawMessage) (any, bool, error) {
				return nil, false, nil
			}, nil); err != nil {
				t.Fatal(err)
			}

			wsRoot := t.TempDir()
			bareOrigin := t.TempDir()
			runGitCmdR(t, "", "init", "--bare", "-b", "main", bareOrigin)

			trunkPath := filepath.Join(wsRoot, "myws", "trunk")
			if err := os.MkdirAll(trunkPath, 0o755); err != nil {
				t.Fatal(err)
			}
			runGitCmdR(t, "", "init", "-b", "main", trunkPath)
			runGitCmdR(t, trunkPath, "config", "user.name", "Test")
			runGitCmdR(t, trunkPath, "config", "user.email", "test@test.com")
			writeFileHelper(t, filepath.Join(trunkPath, "README.md"), "# test\n")
			runGitCmdR(t, trunkPath, "add", ".")
			runGitCmdR(t, trunkPath, "commit", "-m", "init")
			runGitCmdR(t, trunkPath, "remote", "add", "origin", bareOrigin)
			runGitCmdR(t, trunkPath, "push", "origin", "main")

			// Create p1 on the trunk.
			runGitCmdR(t, trunkPath, "checkout", "-b", "p1")
			addCommitR(t, trunkPath, "p1.txt", "p1 commit")
			runGitCmdR(t, trunkPath, "checkout", "main")

			seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
				"carry_patch", "main-int", bareOrigin)
			seedPatch(t, db, "p1-id", "myws", "p1", 1, "active")

			getVar := func(_, _, key string) (string, error) {
				if key == "PUSH_PATCHES_TO_ORIGIN" {
					return "true", nil
				}
				return "", fmt.Errorf("not found")
			}

			deps := tc.deps
			if deps.WorkspaceRoot == "PLACEHOLDER" {
				deps.WorkspaceRoot = wsRoot
			}

			hook := NewPostPushRebuildHook(q, getVar, deps)
			hook(db, "myws", []string{"p1"})

			// Rebuild should always be enqueued.
			count := countActiveRebuildJobs(t, db, "myws")
			if count < 1 {
				t.Errorf("expected >= 1 rebuild job; got %d", count)
			}

			// Only when all deps are supplied should origin have p1.
			expectMirror := tc.name == "all supplied"
			hasMirror := mirrorRefExists(t, bareOrigin, "refs/heads/p1")
			if expectMirror && !hasMirror {
				t.Error("origin should have p1 when all deps are supplied")
			}
			if !expectMirror && hasMirror {
				t.Error("origin should NOT have p1 when a dependency is nil/empty")
			}
		})
	}
}

// ===========================================================================
// TS-22-40 (unit): A mirror failure emits hub.patch.mirror_failed with the
// required fields and credential-free error
// Verifies: 22-REQ-7.1
// ===========================================================================

func TestMirror_TS22_40_MirrorFailedEvent(t *testing.T) {
	env := newMirrorTestEnv(t)
	emitter := &recordingEmitter{}

	seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")

	// Create p1 on the trunk.
	runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
	addCommitR(t, env.trunk, "p1.txt", "p1 commit")
	runGitCmdR(t, env.trunk, "checkout", "main")

	// Point origin to a URL with userinfo that will fail.
	runGitCmdR(t, env.trunk, "remote", "set-url", "origin", "https://user:pw@nonexistent.example.com/repo.git")

	// Update the workspace git_url to include userinfo.
	mustExecMirror(t, env.db, `UPDATE workspaces SET git_url = 'https://user:pw@nonexistent.example.com/repo.git' WHERE slug = 'myws'`)

	getVar := func(_, _, key string) (string, error) {
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}

	hook := NewPostPushRebuildHook(
		env.queue,
		getVar,
		PostPushMirrorDeps{
			ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
			WorkspaceRoot: env.workspaceRoot,
			Audit:         emitter,
		},
	)

	hook(env.db, "myws", []string{"p1"})

	events := emitter.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	// Find the mirror_failed event.
	var found *audit.HubEvent
	for i := range events {
		if events[i].EventType == audit.EventPatchMirrorFailed {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected hub.patch.mirror_failed event")
	}

	if found.ResourceType != "patch" {
		t.Errorf("resource_type = %q; want %q", found.ResourceType, "patch")
	}
	if found.Action != "mirror" {
		t.Errorf("action = %q; want %q", found.Action, "mirror")
	}
	if found.ActorType != "system" {
		t.Errorf("actor_type = %q; want %q", found.ActorType, "system")
	}
	if found.Workspace != "myws" {
		t.Errorf("workspace = %q; want %q", found.Workspace, "myws")
	}

	// Check metadata.
	branchName, ok := found.Metadata["branch_name"]
	if !ok || branchName != "p1" {
		t.Errorf("metadata.branch_name = %v; want %q", branchName, "p1")
	}
	errStr, ok := found.Metadata["error"]
	if !ok {
		t.Fatal("metadata.error missing")
	}
	errText, _ := errStr.(string)
	if strings.Contains(errText, "user:pw") {
		t.Errorf("error contains credentials: %s", errText)
	}
	if strings.Contains(errText, "pw") {
		t.Errorf("error contains password: %s", errText)
	}
}

// ===========================================================================
// TS-22-41 (unit): The audit package defines the hub.patch.mirror_failed
// constant next to EventRebuildFollowup
// Verifies: 22-REQ-7.2
// ===========================================================================

func TestMirror_TS22_41_AuditConstant(t *testing.T) {
	if audit.EventPatchMirrorFailed != "hub.patch.mirror_failed" {
		t.Errorf("EventPatchMirrorFailed = %q; want %q",
			audit.EventPatchMirrorFailed, "hub.patch.mirror_failed")
	}
}

// ===========================================================================
// TS-22-42 (unit): A successful mirror logs at info and emits nothing;
// a failed mirror logs at warn with slug, branch and error
// Verifies: 22-REQ-7.3, 22-REQ-7.4
// ===========================================================================

func TestMirror_TS22_42_LoggingBehaviour(t *testing.T) {
	// Test 1: Successful mirror — info log, no event.
	t.Run("success", func(t *testing.T) {
		env := newMirrorTestEnv(t)
		emitter := &recordingEmitter{}

		seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")

		runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
		addCommitR(t, env.trunk, "p1.txt", "p1 commit")
		runGitCmdR(t, env.trunk, "checkout", "main")

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		getVar := func(_, _, key string) (string, error) {
			if key == "PUSH_PATCHES_TO_ORIGIN" {
				return "true", nil
			}
			return "", fmt.Errorf("not found")
		}

		hook := newPostPushRebuildHookWithLogger(
			env.queue,
			getVar,
			PostPushMirrorDeps{
				ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
				WorkspaceRoot: env.workspaceRoot,
				Audit:         emitter,
			},
			logger,
		)

		hook(env.db, "myws", []string{"p1"})

		// No mirror_failed event should be emitted.
		events := emitter.Events()
		for _, ev := range events {
			if ev.EventType == audit.EventPatchMirrorFailed {
				t.Error("should not emit mirror_failed on success")
			}
		}

		// Info log should be present.
		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "INFO") && !strings.Contains(logOutput, "info") {
			t.Errorf("expected INFO level log; got:\n%s", logOutput)
		}
	})

	// Test 2: Failed mirror — warn log with slug, branch, error.
	t.Run("failure", func(t *testing.T) {
		env := newMirrorTestEnv(t)
		emitter := &recordingEmitter{}

		seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")

		runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
		addCommitR(t, env.trunk, "p1.txt", "p1 commit")
		runGitCmdR(t, env.trunk, "checkout", "main")

		// Point origin to an unreachable URL.
		runGitCmdR(t, env.trunk, "remote", "set-url", "origin", "https://nonexistent.example.com/repo.git")

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		getVar := func(_, _, key string) (string, error) {
			if key == "PUSH_PATCHES_TO_ORIGIN" {
				return "true", nil
			}
			return "", fmt.Errorf("not found")
		}

		hook := newPostPushRebuildHookWithLogger(
			env.queue,
			getVar,
			PostPushMirrorDeps{
				ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
				WorkspaceRoot: env.workspaceRoot,
				Audit:         emitter,
			},
			logger,
		)

		hook(env.db, "myws", []string{"p1"})

		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "WARN") && !strings.Contains(logOutput, "warn") {
			t.Errorf("expected WARN level log; got:\n%s", logOutput)
		}
		if !strings.Contains(logOutput, "myws") {
			t.Errorf("expected slug 'myws' in log; got:\n%s", logOutput)
		}
		if !strings.Contains(logOutput, "p1") {
			t.Errorf("expected branch 'p1' in log; got:\n%s", logOutput)
		}
	})
}

// ===========================================================================
// TS-22-43 (integration): A nil emitter or an emitter that errors changes
// nothing else about the mirror outcome
// Verifies: 22-REQ-7.5
// ===========================================================================

func TestMirror_TS22_43_EmitterErrorsAndNil(t *testing.T) {
	// Test 1: Emitter that returns errors.
	t.Run("error emitter", func(t *testing.T) {
		env := newMirrorTestEnv(t)

		seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")
		seedPatch(t, env.db, "p2-id", "myws", "p2", 2, "active")

		// Create p1 (will fail mirror) and p2 (will succeed).
		runGitCmdR(t, env.trunk, "checkout", "-b", "p2")
		addCommitR(t, env.trunk, "p2.txt", "p2 commit")
		runGitCmdR(t, env.trunk, "checkout", "main")

		runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
		addCommitR(t, env.trunk, "p1.txt", "p1 commit")
		runGitCmdR(t, env.trunk, "checkout", "main")

		// Make p1 fail by adding a pre-receive hook that rejects it.
		hookDir := filepath.Join(env.bareOrigin, "hooks")
		if err := os.MkdirAll(hookDir, 0o755); err != nil {
			t.Fatal(err)
		}
		hookScript := `#!/bin/sh
while read oldrev newrev refname; do
  if [ "$refname" = "refs/heads/p1" ]; then
    echo "refusing p1" >&2
    exit 1
  fi
done
exit 0
`
		hookPath := filepath.Join(hookDir, "pre-receive")
		writeFileHelper(t, hookPath, hookScript)
		if err := os.Chmod(hookPath, 0o755); err != nil {
			t.Fatal(err)
		}

		errEmitter := &recordingEmitter{err: fmt.Errorf("emit failed")}

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		getVar := func(_, _, key string) (string, error) {
			if key == "PUSH_PATCHES_TO_ORIGIN" {
				return "true", nil
			}
			return "", fmt.Errorf("not found")
		}

		hook := newPostPushRebuildHookWithLogger(
			env.queue,
			getVar,
			PostPushMirrorDeps{
				ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
				WorkspaceRoot: env.workspaceRoot,
				Audit:         errEmitter,
			},
			logger,
		)

		hook(env.db, "myws", []string{"p1", "p2"})

		// p2 should be mirrored despite emit error.
		if !mirrorRefExists(t, env.bareOrigin, "refs/heads/p2") {
			t.Error("p2 should be mirrored despite emit error")
		}

		// Rebuild should be enqueued.
		count := countActiveRebuildJobs(t, env.db, "myws")
		if count < 1 {
			t.Errorf("expected >= 1 rebuild job; got %d", count)
		}

		// The emit error should be logged.
		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "emit") {
			t.Errorf("expected 'emit' in log output; got:\n%s", logOutput)
		}
	})

	// Test 2: Nil emitter.
	t.Run("nil emitter", func(t *testing.T) {
		env := newMirrorTestEnv(t)

		seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")

		runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
		addCommitR(t, env.trunk, "p1.txt", "p1 commit")
		runGitCmdR(t, env.trunk, "checkout", "main")

		// Point origin to an unreachable URL to trigger a failure.
		runGitCmdR(t, env.trunk, "remote", "set-url", "origin", "https://nonexistent.example.com/repo.git")

		getVar := func(_, _, key string) (string, error) {
			if key == "PUSH_PATCHES_TO_ORIGIN" {
				return "true", nil
			}
			return "", fmt.Errorf("not found")
		}

		// Pass nil emitter — should not panic.
		hook := NewPostPushRebuildHook(
			env.queue,
			getVar,
			PostPushMirrorDeps{
				ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
				WorkspaceRoot: env.workspaceRoot,
				Audit:         nil,
			},
		)

		// Should not panic.
		hook(env.db, "myws", []string{"p1"})

		// Rebuild should be enqueued.
		count := countActiveRebuildJobs(t, env.db, "myws")
		if count < 1 {
			t.Errorf("expected >= 1 rebuild job; got %d", count)
		}
	})
}

// ===========================================================================
// Mirror applicability is decided before any variable is read or any
// credential is resolved
// Verifies: 22-REQ-1.5, 22-REQ-6.4
// ===========================================================================

func TestMirror_StandardWorkspaceReadsNoVariables(t *testing.T) {
	env := newMirrorTestEnv(t)
	mustExecMirror(t, env.db, `UPDATE workspaces SET workspace_mode = 'standard' WHERE slug = 'myws'`)
	seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")

	var varCalls, authCalls int32
	getVar := func(_, _, key string) (string, error) {
		atomic.AddInt32(&varCalls, 1)
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}
	emitter := &recordingEmitter{}
	deps := PostPushMirrorDeps{
		ResolveAuth: func(string) (transport.AuthMethod, error) {
			atomic.AddInt32(&authCalls, 1)
			return nil, nil
		},
		WorkspaceRoot: env.workspaceRoot,
		Audit:         emitter,
	}

	mirrorBranches(env.db, "myws", []string{"p1"}, deps, getVar, nopLogger())

	if n := atomic.LoadInt32(&varCalls); n != 0 {
		t.Errorf("GetVariable called %d times for a standard workspace; want 0", n)
	}
	if n := atomic.LoadInt32(&authCalls); n != 0 {
		t.Errorf("ResolveAuth called %d times for a standard workspace; want 0", n)
	}
	if evs := emitter.Events(); len(evs) != 0 {
		t.Errorf("expected no events; got %d", len(evs))
	}
}

// ===========================================================================
// A credential-resolution failure is logged and emitted: one
// hub.patch.mirror_failed event per applicable branch, no push attempted
// Verifies: 22-REQ-7.1, 22-REQ-7.3
// ===========================================================================

func TestMirror_CredentialFailureEmitsEventPerApplicableBranch(t *testing.T) {
	env := newMirrorTestEnv(t)
	seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")
	seedPatch(t, env.db, "p2-id", "myws", "p2", 2, "active")
	// Integration branch with a patch row, and an unregistered branch:
	// neither is applicable, so neither gets an event.
	seedPatch(t, env.db, "int-id", "myws", "main-int", 3, "active")

	// Both applicable branches exist on the trunk so a push attempt would
	// succeed against the local bare origin if the mirror ignored the
	// credential failure.
	for _, b := range []string{"p1", "p2"} {
		runGitCmdR(t, env.trunk, "checkout", "-b", b)
		addCommitR(t, env.trunk, b+".txt", b+" commit")
		runGitCmdR(t, env.trunk, "checkout", "main")
	}

	getVar := func(_, _, key string) (string, error) {
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}

	var authCalls int32
	emitter := &recordingEmitter{}
	deps := PostPushMirrorDeps{
		ResolveAuth: func(string) (transport.AuthMethod, error) {
			atomic.AddInt32(&authCalls, 1)
			return nil, fmt.Errorf("credential store unavailable: https://alice:pw@host/x")
		},
		WorkspaceRoot: env.workspaceRoot,
		Audit:         emitter,
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mirrorBranches(env.db, "myws", []string{"p1", "p2", "main-int", "feature"}, deps, getVar, logger)

	if n := atomic.LoadInt32(&authCalls); n != 1 {
		t.Errorf("ResolveAuth called %d times; want exactly 1", n)
	}

	events := emitter.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 mirror_failed events (p1, p2); got %d: %+v", len(events), events)
	}
	gotBranches := map[string]bool{}
	for _, ev := range events {
		if ev.EventType != audit.EventPatchMirrorFailed {
			t.Errorf("event type = %q; want %q", ev.EventType, audit.EventPatchMirrorFailed)
		}
		if ev.Workspace != "myws" || ev.ResourceType != "patch" || ev.Action != "mirror" || ev.ActorType != "system" {
			t.Errorf("unexpected event envelope: %+v", ev)
		}
		branch, _ := ev.Metadata["branch_name"].(string)
		gotBranches[branch] = true

		errText, _ := ev.Metadata["error"].(string)
		if !strings.HasPrefix(errText, "failed to resolve origin credentials") {
			t.Errorf("metadata.error = %q; want prefix %q", errText, "failed to resolve origin credentials")
		}
		if strings.Contains(errText, "alice") || strings.Contains(errText, "pw@") {
			t.Errorf("metadata.error contains credentials: %q", errText)
		}
	}
	if !gotBranches["p1"] || !gotBranches["p2"] || len(gotBranches) != 2 {
		t.Errorf("event branches = %v; want exactly p1 and p2", gotBranches)
	}

	// Nothing was pushed.
	for _, b := range []string{"p1", "p2"} {
		if mirrorRefExists(t, env.bareOrigin, "refs/heads/"+b) {
			t.Errorf("origin should not have %s after a credential failure", b)
		}
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "level=WARN") {
		t.Errorf("expected a WARN line; got:\n%s", logOutput)
	}
	if strings.Contains(logOutput, "alice") || strings.Contains(logOutput, "pw@") {
		t.Errorf("log contains credentials: %s", logOutput)
	}
}

// With no applicable branch the credentials are never resolved, so a
// resolver failure cannot produce an event.
func TestMirror_NoApplicableBranchDoesNotResolveCredentials(t *testing.T) {
	env := newMirrorTestEnv(t)
	// Integration branch with a patch row; "feature" has no patch row.
	seedPatch(t, env.db, "int-id", "myws", "main-int", 1, "active")

	getVar := func(_, _, key string) (string, error) {
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}

	var authCalls int32
	emitter := &recordingEmitter{}
	deps := PostPushMirrorDeps{
		ResolveAuth: func(string) (transport.AuthMethod, error) {
			atomic.AddInt32(&authCalls, 1)
			return nil, fmt.Errorf("credential store unavailable")
		},
		WorkspaceRoot: env.workspaceRoot,
		Audit:         emitter,
	}

	mirrorBranches(env.db, "myws", []string{"feature", "main-int"}, deps, getVar, nopLogger())

	if n := atomic.LoadInt32(&authCalls); n != 0 {
		t.Errorf("ResolveAuth called %d times with no applicable branch; want 0", n)
	}
	if evs := emitter.Events(); len(evs) != 0 {
		t.Errorf("expected no events; got %d: %+v", len(evs), evs)
	}
}

// ===========================================================================
// The mirror_failed metadata (and the warning log) is sanitised as free
// text: userinfo embedded in a URL inside a go-git error is removed
// Verifies: 22-REQ-7.1
// ===========================================================================

func TestMirror_FailedEventErrorSanitised(t *testing.T) {
	env := newMirrorTestEnv(t)
	seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")

	runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
	addCommitR(t, env.trunk, "p1.txt", "p1 commit")
	runGitCmdR(t, env.trunk, "checkout", "main")

	// Nothing listens on port 1, so the push fails with a connection error
	// that embeds the remote URL (including its userinfo).
	runGitCmdR(t, env.trunk, "remote", "set-url", "origin", "https://mirroruser:pw@127.0.0.1:1/repo.git")

	getVar := func(_, _, key string) (string, error) {
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}
	emitter := &recordingEmitter{}
	deps := PostPushMirrorDeps{
		ResolveAuth:   func(string) (transport.AuthMethod, error) { return nil, nil },
		WorkspaceRoot: env.workspaceRoot,
		Audit:         emitter,
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mirrorBranches(env.db, "myws", []string{"p1"}, deps, getVar, logger)

	events := emitter.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 mirror_failed event; got %d", len(events))
	}
	errText, _ := events[0].Metadata["error"].(string)
	if !strings.Contains(errText, "127.0.0.1:1") {
		t.Fatalf("expected a connection error mentioning the host; got %q", errText)
	}
	for _, s := range []string{"mirroruser", "pw"} {
		if strings.Contains(errText, s) {
			t.Errorf("metadata.error contains credential text %q: %s", s, errText)
		}
		if strings.Contains(logBuf.String(), s) {
			t.Errorf("log contains credential text %q: %s", s, logBuf.String())
		}
	}
}

// mustExecMirror executes a SQL statement and fails the test on error.
func mustExecMirror(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("mustExecMirror(%q): %v", query, err)
	}
}
