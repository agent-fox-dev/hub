package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// pushHookTestEnv holds the dependencies for push hook tests.
type pushHookTestEnv struct {
	db    *sql.DB
	queue *jobqueue.Queue
}

// newPushHookTestEnv creates a test environment with the workspaces, patches,
// and jobs tables set up for push hook tests.
func newPushHookTestEnv(t *testing.T) *pushHookTestEnv {
	t.Helper()

	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey() returned error: %v", err)
	}

	q, qDB := newTestQueue(t)
	// The newTestQueue creates its own DB, but we need to use one shared DB.
	// Instead, create queue against our shared DB.
	_ = qDB
	q2, err := jobqueue.New(db, nopLogger())
	if err != nil {
		t.Fatalf("jobqueue.New() returned error: %v", err)
	}

	// Register the rebuild handler so rebuild-type jobs can be enqueued.
	if err := q2.Register("rebuild", func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, false, nil
	}, nil); err != nil {
		t.Fatalf("register rebuild: %v", err)
	}

	_ = q // discard the separate-db queue

	return &pushHookTestEnv{
		db:    db,
		queue: q2,
	}
}

// countActiveRebuildJobs returns the number of queued or running rebuild jobs
// for the given workspace slug.
func countActiveRebuildJobs(t *testing.T, db *sql.DB, slug string) int {
	t.Helper()
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM jobs WHERE type = 'rebuild' AND key = ? AND status IN ('queued', 'running')`,
		slug,
	).Scan(&count)
	if err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return count
}

// pushHookVarNotFound is a GetVariableFunc that always returns not-found.
func pushHookVarNotFound(_, _, _ string) (string, error) {
	return "", &pushHookNotFoundErr{}
}

type pushHookNotFoundErr struct{}

func (e *pushHookNotFoundErr) Error() string { return "not found" }

// TS-NS-1: Push to a registered patch branch in a carry_patch workspace
// auto-enqueues a rebuild job when AUTO_REBUILD_AFTER_PUSH is unset (default true).
// Requirement: NS-REQ-1
func TestPostPushHook_RegisteredPatchBranch_EnqueuesRebuild(t *testing.T) {
	env := newPushHookTestEnv(t)

	seedWorkspace(t, env.db, "myws", "user-1", "active", "ready", "carry_patch", "main")
	seedPatch(t, env.db, "patch-1", "myws", "feature/my-patch", 1, "active")

	// Verify no rebuild jobs exist before.
	if count := countActiveRebuildJobs(t, env.db, "myws"); count != 0 {
		t.Fatalf("expected 0 rebuild jobs before push; got %d", count)
	}

	// Simulate push to the registered patch branch.
	postPushRebuildHook(env.db, "myws", []string{"feature/my-patch"}, env.queue, pushHookVarNotFound)

	// After push, a rebuild job should be enqueued.
	count := countActiveRebuildJobs(t, env.db, "myws")
	if count < 1 {
		t.Errorf("expected >= 1 rebuild job after push to registered patch branch; got %d", count)
	}
}

// TS-NS-2: When AUTO_REBUILD_AFTER_PUSH='false', no rebuild is enqueued.
// Requirement: NS-REQ-2
func TestPostPushHook_AutoRebuildDisabled_NoRebuild(t *testing.T) {
	env := newPushHookTestEnv(t)

	seedWorkspace(t, env.db, "myws", "user-1", "active", "ready", "carry_patch", "main")
	seedPatch(t, env.db, "patch-1", "myws", "feature/my-patch", 1, "active")

	// GetVariable that returns "false" for AUTO_REBUILD_AFTER_PUSH.
	getVar := func(scope, slug, key string) (string, error) {
		if key == "AUTO_REBUILD_AFTER_PUSH" {
			return "false", nil
		}
		return "", &pushHookNotFoundErr{}
	}

	postPushRebuildHook(env.db, "myws", []string{"feature/my-patch"}, env.queue, getVar)

	count := countActiveRebuildJobs(t, env.db, "myws")
	if count != 0 {
		t.Errorf("expected 0 rebuild jobs after push with AUTO_REBUILD_AFTER_PUSH=false; got %d", count)
	}
}

// TS-NS-3: Push to a branch NOT registered in the patches table does not
// enqueue a rebuild, even for a carry_patch workspace.
// Requirement: NS-REQ-3
func TestPostPushHook_UnregisteredBranch_NoRebuild(t *testing.T) {
	env := newPushHookTestEnv(t)

	seedWorkspace(t, env.db, "myws", "user-1", "active", "ready", "carry_patch", "main")
	seedPatch(t, env.db, "patch-1", "myws", "feature/my-patch", 1, "active")

	// Push to a branch that is NOT a registered patch.
	postPushRebuildHook(env.db, "myws", []string{"feature/unregistered"}, env.queue, pushHookVarNotFound)

	count := countActiveRebuildJobs(t, env.db, "myws")
	if count != 0 {
		t.Errorf("expected 0 rebuild jobs after push to unregistered branch; got %d", count)
	}
}

// TS-NS-4: Duplicate rebuild job is silently suppressed when one already
// exists (queued or running).
// Requirement: NS-REQ-4
func TestPostPushHook_DuplicateJob_Suppressed(t *testing.T) {
	env := newPushHookTestEnv(t)

	seedWorkspace(t, env.db, "myws", "user-1", "active", "ready", "carry_patch", "main")
	seedPatch(t, env.db, "patch-1", "myws", "feature/my-patch", 1, "active")

	// First push — should enqueue a rebuild.
	postPushRebuildHook(env.db, "myws", []string{"feature/my-patch"}, env.queue, pushHookVarNotFound)
	countBefore := countActiveRebuildJobs(t, env.db, "myws")
	if countBefore != 1 {
		t.Fatalf("expected 1 rebuild job after first push; got %d", countBefore)
	}

	// Second push — should NOT create a duplicate.
	postPushRebuildHook(env.db, "myws", []string{"feature/my-patch"}, env.queue, pushHookVarNotFound)
	countAfter := countActiveRebuildJobs(t, env.db, "myws")
	if countAfter != 1 {
		t.Errorf("expected exactly 1 rebuild job after second push (duplicate suppressed); got %d", countAfter)
	}
}

// TS-NS-5: Push to a standard (non-carry_patch) workspace never triggers rebuild.
// Requirement: NS-REQ-5
func TestPostPushHook_StandardWorkspace_NoRebuild(t *testing.T) {
	env := newPushHookTestEnv(t)

	seedWorkspace(t, env.db, "myws", "user-1", "active", "ready", "standard", "main")
	// Even if a branch name matches a patch row, no rebuild for standard workspaces.
	seedPatch(t, env.db, "patch-1", "myws", "feature/my-patch", 1, "active")

	postPushRebuildHook(env.db, "myws", []string{"feature/my-patch"}, env.queue, pushHookVarNotFound)

	count := countActiveRebuildJobs(t, env.db, "myws")
	if count != 0 {
		t.Errorf("expected 0 rebuild jobs after push to standard workspace; got %d", count)
	}
}
