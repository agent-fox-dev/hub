package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/aflib/jobqueue"
)

// rebuild drives the executor over a real rebuild repository with the SQL
// patch store; hooks wrap the runners the executor builds.
type rebuild struct {
	t                 *testing.T
	root, slug, trunk string
	base              string
	db                *sql.DB
	h                 *RebuildHandler
	logs              *logSink
	audit             *recordingEmitter

	trunkHook func(GitRunner) GitRunner
	wtHook    func(GitRunner) GitRunner
}

func newRebuild(t *testing.T) *rebuild {
	t.Helper()
	r := &rebuild{t: t, root: t.TempDir(), slug: "ws", db: openDB(t), audit: &recordingEmitter{}}
	r.trunk, r.base = newRebuildRepo(t, r.root, r.slug)
	seedWS(t, r.db, ws{slug: r.slug})
	logger, sink := newLogger()
	r.logs = sink
	r.h = &RebuildHandler{DB: r.db, WorkspaceRoot: r.root, Logger: logger, PatchStore: NewSQLPatchStore(r.db),
		Fetch: noFetch, ResolveAuth: noAuth, Audit: r.audit, NewGitRunner: r.newRunner}
	return r
}

func (r *rebuild) newRunner(path string) (GitRunner, error) {
	g, err := NewGitRunnerFactory()(path)
	if err != nil {
		return nil, err
	}
	if path == r.trunk && r.trunkHook != nil {
		return r.trunkHook(g), nil
	}
	if path != r.trunk && r.wtHook != nil {
		return r.wtHook(g), nil
	}
	return g, nil
}

// patches seeds active patches p1..pn for the branches, in that order.
func (r *rebuild) patches(branches ...string) {
	for i, b := range branches {
		seedPatch(r.t, r.db, r.slug, fmt.Sprintf("p%d", i+1), b, i+1, "active")
	}
}

func (r *rebuild) run(ctx context.Context, strategy, failMode string) (*RebuildResult, bool, error) {
	r.t.Helper()
	raw, _ := json.Marshal(RebuildPayload{WorkspaceSlug: r.slug, Strategy: strategy, FailMode: failMode, SubmittedBy: "operator", IntegrationBranch: "integration"})
	res, retryable, err := r.h.HandleRebuildJob(ctx, raw)
	if res == nil {
		return nil, retryable, err
	}
	return res.(*RebuildResult), retryable, err
}

// runningJob attaches a queue with one running rebuild job and returns the
// job context the executor would get from the queue.
func (r *rebuild) runningJob(group string) (*jobqueue.Queue, context.Context, string) {
	r.t.Helper()
	q := newQueue(r.t, r.db, r.h)
	r.h.Queue = q
	raw, _ := json.Marshal(RebuildPayload{WorkspaceSlug: r.slug, Strategy: StrategyRebase, IntegrationBranch: "integration"})
	id, _, err := q.Enqueue(jobqueue.EnqueueParams{Type: "rebuild", Key: r.slug, Nonce: "orig", Payload: raw, SubmittedBy: "operator", Group: group})
	if err != nil {
		r.t.Fatalf("enqueue: %v", err)
	}
	mustExec(r.t, r.db, `UPDATE jobs SET status = 'running' WHERE id = ?`, id)
	return q, jobqueue.ContextWithJobID(context.Background(), id), id
}

// assertClean checks the per-run worktree is gone, the trunk has no linked
// worktree registered, the lock is free and the rebuild guard ended.
func (r *rebuild) assertClean() {
	r.t.Helper()
	if entries, err := os.ReadDir(filepath.Join(r.root, r.slug, "rebuild")); err == nil && len(entries) > 0 {
		r.t.Errorf("rebuild directory still holds %d entries", len(entries))
	}
	if n := strings.Count(runGit(r.t, r.trunk, "worktree", "list", "--porcelain"), "worktree "); n != 1 {
		r.t.Errorf("%d worktrees registered, want only the trunk", n)
	}
	if lockHeld(r.slug) || testLocks.RebuildActive(r.slug) {
		r.t.Error("lock or rebuild guard still held after the run")
	}
}

// files returns the files tracked at ref.
func files(t *testing.T, dir, ref string) string {
	t.Helper()
	return runGit(t, dir, "ls-tree", "--name-only", "-r", ref)
}

// A rebase rebuild cherry-picks each patch in position order onto the
// upstream base inside a per-run worktree, then moves the integration ref;
// the trunk checkout is never touched and the worktree is removed.
func TestRebuild_RebaseAppliesPatchesInPositionOrder(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/patch-b", "feature/patch-a")
	headBefore, statusBefore := refOf(r.trunk, "HEAD"), runGit(t, r.trunk, "status", "--porcelain")

	res, retryable, err := r.run(context.Background(), StrategyRebase, "")
	if err != nil || retryable {
		t.Fatalf("run: retryable=%v err=%v", retryable, err)
	}
	if res.PatchesApplied != 2 || res.Strategy != StrategyRebase || res.FailMode != FailModeFailFast || res.UpstreamHeadSHA != r.base || res.PreviousIntegrationHeadSHA != r.base {
		t.Errorf("result = %+v", res)
	}
	if res.IntegrationHeadSHA != refOf(r.trunk, "integration") {
		t.Error("integration ref does not point at the result head")
	}
	if len(res.PatchResults) != 2 || res.PatchResults[0].BranchName != "feature/patch-b" || res.PatchResults[0].SourceSHA != refOf(r.trunk, "feature/patch-b") || res.PatchResults[0].NewHeadSHA == nil || res.PatchResults[1].Status != "success" {
		t.Errorf("patch results = %+v", res.PatchResults)
	}
	if subjects := runGit(t, r.trunk, "log", "--format=%s", r.base+"..integration"); subjects != "patch-a change\npatch-b change" {
		t.Errorf("integration history = %q", subjects)
	}
	if refOf(r.trunk, "HEAD") != headBefore || runGit(t, r.trunk, "status", "--porcelain") != statusBefore || runGit(t, r.trunk, "symbolic-ref", "HEAD") != "refs/heads/main" {
		t.Error("trunk checkout changed")
	}
	if runGit(t, r.trunk, "config", "rerere.enabled") != "true" || runGit(t, r.trunk, "config", "rerere.autoupdate") != "true" {
		t.Error("rerere not configured on the repository")
	}
	r.assertClean()
	if ev := r.audit.ofType("hub.rebuild.complete"); len(ev) != 1 || ev[0].Metadata["patches_applied"] != 2 || ev[0].ActorType != "system" || ev[0].Workspace != "ws" {
		t.Errorf("complete event = %+v", ev)
	}
	if !strings.Contains(r.logs.String(), "rebuild worktree created") || !strings.Contains(r.logs.String(), "rebuild worktree removed") {
		t.Errorf("worktree lifecycle not logged: %s", r.logs.String())
	}
}

func TestRebuild_MergeStrategyMergesNoFFNamingTheBranch(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/patch-a")
	if _, _, err := r.run(context.Background(), StrategyMerge, ""); err != nil {
		t.Fatal(err)
	}
	if subject := runGit(t, r.trunk, "log", "-1", "--format=%s", "integration"); subject != "Merge branch 'feature/patch-a'" {
		t.Errorf("merge subject = %q", subject)
	}
	if parents := runGit(t, r.trunk, "log", "-1", "--format=%P", "integration"); len(strings.Fields(parents)) != 2 {
		t.Errorf("merge parents = %q", parents)
	}
	r.assertClean()
}

// In fail-fast mode an unresolved conflict marks the patch, stops the run
// without touching the integration ref and is not retried.
func TestRebuild_ConflictFailFastMarksPatchAndStops(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/patch-a", "feature/conflict", "feature/patch-b")

	res, retryable, err := r.run(context.Background(), StrategyRebase, "")
	if res != nil || retryable || err == nil || err.Error() != `conflict in patch "feature/conflict": base.txt` {
		t.Fatalf("run = %v, retryable=%v, err=%v", res, retryable, err)
	}
	patches, _ := r.h.PatchStore.ListPatches(context.Background(), r.slug)
	if patches[1].Status != PatchStatusConflict || !equalStrings(patches[1].ConflictFiles, []string{"base.txt"}) || patches[2].Status != PatchStatusActive {
		t.Errorf("patches after conflict = %+v", patches)
	}
	if refOf(r.trunk, "integration") != r.base {
		t.Error("integration ref moved on a failed run")
	}
	if ev := r.audit.ofType("hub.rebuild.fail"); len(ev) != 1 || !strings.Contains(ev[0].Metadata["reason"].(string), "feature/conflict") {
		t.Errorf("fail event = %+v", ev)
	}
	r.assertClean()

	// Merge strategy reports the same conflict.
	mustExec(t, r.db, `UPDATE patches SET status = 'active', conflict_files = NULL`)
	if _, _, err := r.run(context.Background(), StrategyMerge, ""); err == nil || !strings.Contains(err.Error(), "feature/conflict") {
		t.Errorf("merge conflict = %v", err)
	}
	r.assertClean()
}

// In continue mode a conflict is recorded, the worktree returns to the last
// good state and the remaining patches are applied.
func TestRebuild_ContinueModeRecordsConflictAndCarriesOn(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/conflict", "feature/patch-a")
	for _, strategy := range []string{StrategyRebase, StrategyMerge} {
		res, retryable, err := r.run(context.Background(), strategy, FailModeContinue)
		if err != nil || retryable {
			t.Fatalf("%s: retryable=%v err=%v", strategy, retryable, err)
		}
		if res.PatchesConflicted != 1 || res.PatchesApplied != 1 || res.FailMode != FailModeContinue || res.PatchResults[0].Status != "conflict" || !equalStrings(res.PatchResults[0].ConflictFiles, []string{"base.txt"}) {
			t.Errorf("%s: result = %+v", strategy, res)
		}
		if got := files(t, r.trunk, "integration"); !strings.Contains(got, "patch-a.txt") || runGit(t, r.trunk, "show", "integration:base.txt") != "upstream version" {
			t.Errorf("%s: integration tree = %q", strategy, got)
		}
		if patchStatuses(t, r.db, r.slug)["feature/conflict"] != PatchStatusConflict {
			t.Errorf("%s: conflicting patch not marked", strategy)
		}
		r.assertClean()
	}
}

// Merged, disabled and unresolvable patches are skipped with their reason;
// merged ones are soft-deleted after the run and positions compacted.
func TestRebuild_SkipsByStatusAndSoftDeletesMerged(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/merged", "feature/off", "feature/nope", "feature/patch-a")
	mustExec(t, r.db, `UPDATE patches SET status = 'merged_upstream' WHERE id = 'p1'`)
	mustExec(t, r.db, `UPDATE patches SET status = 'disabled' WHERE id = 'p2'`)

	res, _, err := r.run(context.Background(), StrategyRebase, "")
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, pr := range res.PatchResults {
		reasons[pr.BranchName] = pr.SkippedReason
	}
	want := map[string]string{"feature/merged": "merged_upstream", "feature/off": "disabled", "feature/nope": "branch_not_found", "feature/patch-a": ""}
	for b, reason := range want {
		if reasons[b] != reason {
			t.Errorf("%s skipped_reason = %q, want %q", b, reasons[b], reason)
		}
	}
	if res.PatchesSkipped != 3 || res.PatchesApplied != 1 || res.PatchesRemoved != 1 {
		t.Errorf("counts = %+v", res)
	}
	if got := patchStatuses(t, r.db, r.slug); got["feature/merged"] != "deleted" || got["feature/off"] != "disabled" {
		t.Errorf("statuses = %v", got)
	}
	if got := positions(t, r.db, r.slug); !equalStrings(got, []string{"feature/off", "feature/nope", "feature/patch-a"}) {
		t.Errorf("positions after compaction = %v", got)
	}
}

func TestRebuild_FirstRebuildCreatesIntegrationBranch(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/patch-a")
	runGit(t, r.trunk, "branch", "-D", "integration")
	res, _, err := r.run(context.Background(), StrategyRebase, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.PreviousIntegrationHeadSHA != "" || refOf(r.trunk, "integration") != res.IntegrationHeadSHA {
		t.Errorf("result = %+v, integration = %s", res, refOf(r.trunk, "integration"))
	}
}

// When the trunk has the integration branch checked out, moving its ref
// leaves the checkout behind, so the trunk is reset to the new tip; that is
// the only case the trunk is reset.
func TestRebuild_TrunkFollowsCheckedOutIntegrationBranch(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/patch-a")
	runGit(t, r.trunk, "checkout", "-q", "integration")
	res, _, err := r.run(context.Background(), StrategyRebase, "")
	if err != nil {
		t.Fatal(err)
	}
	if refOf(r.trunk, "HEAD") != res.IntegrationHeadSHA {
		t.Error("checkout not brought to the new integration tip")
	}
	if _, err := os.Stat(filepath.Join(r.trunk, "patch-a.txt")); err != nil {
		t.Error("working tree not reset to the new tip")
	}
	if !strings.Contains(r.logs.String(), "reset trunk checkout to the new integration tip") {
		t.Error("reset not logged")
	}
}

// A failing integration ref update ends phase two: no soft-deletion, no
// compaction, no push; the run is retried.
func TestRebuild_UpdateRefFailureStopsPhaseTwo(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/merged", "feature/patch-a")
	mustExec(t, r.db, `UPDATE patches SET status = 'merged_upstream' WHERE id = 'p1'`)
	r.trunkHook = func(g GitRunner) GitRunner {
		return &spyGit{GitRunner: g, onUpdateRef: func(ref, sha string) error { return errors.New("ref locked") }}
	}
	pushed := false
	r.h.PushIntegration = func(context.Context, string, string, string) error { pushed = true; return nil }
	r.h.GetVariable = varsOf(map[string]string{"REBUILD_PUSH_INTEGRATION_BRANCH": "true"}, nil)

	res, retryable, err := r.run(context.Background(), StrategyRebase, "")
	var te *TransientError
	if res != nil || !retryable || !errors.As(err, &te) || !strings.Contains(err.Error(), "refs/heads/integration") {
		t.Fatalf("run = %v, retryable=%v, err=%v", res, retryable, err)
	}
	if patchStatuses(t, r.db, r.slug)["feature/merged"] != "merged_upstream" || pushed {
		t.Error("phase two continued after the failed ref update")
	}
	r.assertClean()
}

// Pushing the integration branch is opt-in by setting; a failed push is
// audited and does not fail the run.
func TestRebuild_PushIntegrationOptIn(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/patch-a")
	var pushes []string
	var pushErr error
	r.h.PushIntegration = func(_ context.Context, slug, repoPath, branch string) error {
		pushes = append(pushes, slug+"|"+repoPath+"|"+branch)
		return pushErr
	}
	vars := map[string]string{}
	r.h.GetVariable = varsOf(vars, nil)

	if res, _, err := r.run(context.Background(), StrategyRebase, ""); err != nil || res.IntegrationBranchPushed || len(pushes) != 0 {
		t.Errorf("unset variable: pushed=%v pushes=%v err=%v", res != nil && res.IntegrationBranchPushed, pushes, err)
	}
	vars["REBUILD_PUSH_INTEGRATION_BRANCH"] = "true"
	res, _, err := r.run(context.Background(), StrategyRebase, "")
	if err != nil || !res.IntegrationBranchPushed || !equalStrings(pushes, []string{"ws|" + r.trunk + "|integration"}) {
		t.Errorf("opted in: result=%+v pushes=%v err=%v", res, pushes, err)
	}
	pushErr = errors.New("remote refused")
	res, _, err = r.run(context.Background(), StrategyRebase, "")
	if err != nil || res.IntegrationBranchPushed {
		t.Errorf("failed push: result=%+v err=%v", res, err)
	}
	if ev := r.audit.ofType("hub.rebuild.push_failed"); len(ev) != 1 || ev[0].Metadata["reason"] != "remote refused" || ev[0].Metadata["integration_branch"] != "integration" {
		t.Errorf("push_failed event = %+v", ev)
	}
}

// A cancelled run is a retryable failure, never a skip or a conflict; the
// worktree is still removed, with a context that outlives the cancellation,
// and the failure is audited.
func TestRebuild_CancellationIsTransientAndCleansUp(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/patch-a", "feature/patch-b")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.wtHook = func(g GitRunner) GitRunner {
		return &spyGit{GitRunner: g, onCherryPick: func(string) error { cancel(); return ctx.Err() }}
	}
	res, retryable, err := r.run(ctx, StrategyRebase, "")
	if res != nil || !retryable || err == nil {
		t.Fatalf("run = %v, retryable=%v, err=%v", res, retryable, err)
	}
	if got := patchStatuses(t, r.db, r.slug); got["feature/patch-a"] != "active" {
		t.Errorf("cancellation changed a patch status: %v", got)
	}
	if refOf(r.trunk, "integration") != r.base {
		t.Error("integration ref moved on a cancelled run")
	}
	r.assertClean()
	if len(r.audit.ofType("hub.rebuild.fail")) != 1 {
		t.Error("fail event not emitted after cancellation")
	}
}

// Before each run the leftovers of a crashed run under <slug>/rebuild are
// removed and their worktree registrations pruned; a failing worktree add
// leaves nothing behind and is retried.
func TestRebuild_StaleDirectoriesAndFailedWorktreeAdd(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/patch-a")
	stale := filepath.Join(r.root, r.slug, "rebuild", "old-job")
	runGit(t, r.trunk, "worktree", "add", "-q", "--detach", stale, r.base)
	if err := os.RemoveAll(stale); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(stale, "leftover"), "x")
	if _, _, err := r.run(context.Background(), StrategyRebase, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale directory survived the run")
	}
	if !strings.Contains(r.logs.String(), "removed stale rebuild worktree directory") {
		t.Error("stale removal not logged")
	}
	r.assertClean()

	// A file where the rebuild directory should be makes `worktree add` fail.
	rebuildDir := filepath.Join(r.root, r.slug, "rebuild")
	if err := os.RemoveAll(rebuildDir); err != nil {
		t.Fatal(err)
	}
	writeFile(t, rebuildDir, "not a directory")
	res, retryable, err := r.run(context.Background(), StrategyRebase, "")
	if res != nil || !retryable || err == nil || !strings.Contains(err.Error(), "create rebuild worktree") {
		t.Fatalf("run = %v, retryable=%v, err=%v", res, retryable, err)
	}
	if !strings.Contains(r.logs.String(), "cannot list rebuild directory") {
		t.Error("unreadable rebuild directory not logged")
	}
	if lockHeld(r.slug) || testLocks.RebuildActive(r.slug) {
		t.Error("lock or guard held after the failed worktree add")
	}
}

// Phase one runs under the lock and the rebuild guard; patch application
// holds the guard but not the lock; both are released at the end. Failures
// before the worktree exists are transient.
func TestRebuild_LockPhasesAndEarlyFailures(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/patch-a")
	var lockedDuringApply, guardedDuringApply bool
	r.wtHook = func(g GitRunner) GitRunner {
		return &spyGit{GitRunner: g, onCherryPick: func(string) error {
			lockedDuringApply, guardedDuringApply = lockHeld(r.slug), testLocks.RebuildActive(r.slug)
			return nil
		}}
	}
	if _, _, err := r.run(context.Background(), StrategyRebase, ""); err != nil {
		t.Fatal(err)
	}
	if lockedDuringApply || !guardedDuringApply {
		t.Errorf("during application: locked=%v guarded=%v, want free lock and active guard", lockedDuringApply, guardedDuringApply)
	}
	r.assertClean()

	end, _ := testLocks.BeginRebuild(r.slug)
	_, retryable, err := r.run(context.Background(), StrategyRebase, "")
	end()
	if !retryable || err == nil || !strings.Contains(err.Error(), "already active") {
		t.Errorf("guard already set: retryable=%v err=%v", retryable, err)
	}
	r.h.ResolveAuth = func(string) (transport.AuthMethod, error) { return nil, errors.New("no creds") }
	if _, retryable, err := r.run(context.Background(), StrategyRebase, ""); !retryable || err == nil {
		t.Errorf("credential failure: retryable=%v err=%v", retryable, err)
	}
	r.h.ResolveAuth = noAuth
	r.h.Fetch = func(context.Context, string, transport.AuthMethod) error { return errors.New("fetch down") }
	if _, retryable, err := r.run(context.Background(), StrategyRebase, ""); !retryable || err == nil || !strings.Contains(err.Error(), "fetch down") {
		t.Errorf("fetch failure: retryable=%v err=%v", retryable, err)
	}
	r.h.Fetch = noFetch
	runGit(t, r.trunk, "update-ref", "-d", UpstreamHeadRef)
	if _, retryable, err := r.run(context.Background(), StrategyRebase, ""); !retryable || err == nil {
		t.Errorf("no upstream base: retryable=%v err=%v", retryable, err)
	}
	if _, retryable, err := r.h.HandleRebuildJob(context.Background(), json.RawMessage(`{bad`)); retryable || err == nil {
		t.Errorf("invalid payload: retryable=%v err=%v", retryable, err)
	}
	r.assertClean()
}

// With a queue the executor records the patch results so far after each patch.
func TestRebuild_WritesProgressAfterEachPatch(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/off", "feature/patch-a")
	mustExec(t, r.db, `UPDATE patches SET status = 'disabled' WHERE id = 'p1'`)
	q, ctx, id := r.runningJob("g")
	if _, _, err := r.run(ctx, StrategyRebase, ""); err != nil {
		t.Fatal(err)
	}
	job, _ := q.GetByID(id)
	var progress []PatchResult
	if err := json.Unmarshal(job.Progress, &progress); err != nil || len(progress) != 2 || progress[0].SkippedReason != "disabled" || progress[1].Status != "success" {
		t.Errorf("progress = %s (%v)", job.Progress, err)
	}
	if res, _, _ := r.run(context.Background(), StrategyRebase, ""); res == nil {
		t.Error("run without a job id failed")
	}
}

// The patch tips are snapshotted before application: a branch that moves
// during the run contributes its snapshot only, and the run enqueues one
// follow-up rebuild with its own payload and group key, which the auto
// rebuild settings can suppress.
func TestRebuild_StaleInputsEnqueueFollowup(t *testing.T) {
	cases := []struct {
		name         string
		vars         map[string]string
		moveUpstream bool
		conflict     bool
		wantFollowup bool
		wantStale    []string
	}{
		{"moved patch tip", nil, false, false, true, []string{"feature/patch-a"}},
		{"moved tip suppressed", map[string]string{"AUTO_REBUILD_AFTER_PUSH": "false"}, false, false, false, nil},
		{"moved upstream", map[string]string{"AUTO_REBUILD_AFTER_PUSH": "false"}, true, false, true, []string{}},
		{"moved upstream suppressed", map[string]string{"AUTO_REBUILD_AFTER_PUSH": "false", "AUTO_REBUILD_AFTER_SYNC": "false"}, true, false, false, nil},
		{"fail-fast conflict still checks", nil, false, true, true, []string{"feature/patch-a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRebuild(t)
			branches := []string{"feature/patch-a"}
			if c.conflict {
				branches = append(branches, "feature/conflict")
			}
			r.patches(branches...)
			r.h.GetVariable = varsOf(c.vars, nil)
			q, ctx, id := r.runningJob("group-1")
			moved := false
			r.wtHook = func(g GitRunner) GitRunner {
				return &spyGit{GitRunner: g, onCherryPick: func(string) error {
					if moved {
						return nil
					}
					moved = true
					runGit(t, r.trunk, "checkout", "-q", "feature/patch-a")
					commitFile(t, r.trunk, "late.txt", "late\n", "pushed during the run")
					runGit(t, r.trunk, "checkout", "-q", "main")
					if c.moveUpstream {
						runGit(t, r.trunk, "update-ref", UpstreamHeadRef, refOf(r.trunk, "feature/patch-b"))
					}
					return nil
				}}
			}

			res, _, err := r.run(ctx, StrategyRebase, "")
			if c.conflict != (err != nil) {
				t.Fatalf("run: res=%v err=%v", res, err)
			}
			if !c.conflict && strings.Contains(files(t, r.trunk, "integration"), "late.txt") {
				t.Error("the commit pushed during the run was applied; only the snapshot may be")
			}
			jobs, _ := q.ListByKey("rebuild", r.slug)
			var followups []*jobqueue.Job
			for _, j := range jobs {
				if j.ID != id {
					followups = append(followups, j)
				}
			}
			if (len(followups) == 1) != c.wantFollowup {
				t.Fatalf("follow-ups = %d, want one=%v", len(followups), c.wantFollowup)
			}
			events := r.audit.ofType(EventRebuildFollowup)
			if !c.wantFollowup {
				if len(events) != 0 {
					t.Error("follow-up event without a follow-up")
				}
				return
			}
			f := followups[0]
			var payload RebuildPayload
			_ = json.Unmarshal(f.Payload, &payload)
			if f.Status != jobqueue.StatusQueued || f.GroupKey != "group-1" || f.SubmittedBy != followupSubmitter || payload.SubmittedBy != followupSubmitter || payload.IntegrationBranch != "integration" {
				t.Errorf("follow-up = %+v payload=%+v", f, payload)
			}
			if len(events) != 1 || events[0].Metadata["follow_up_job_id"] != f.ID || events[0].Metadata["upstream_stale"] != c.moveUpstream {
				t.Errorf("follow-up event = %+v", events)
			}
			if got, _ := events[0].Metadata["stale_patches"].([]string); !equalStrings(got, c.wantStale) {
				t.Errorf("stale_patches = %v, want %v", got, c.wantStale)
			}
		})
	}
}

// Without a queue a stale input is only logged; a follow-up that would
// duplicate a queued rebuild is skipped.
func TestRebuild_FollowupWithoutQueueOrWithDuplicate(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/patch-a")
	move := func(string) error {
		if refOf(r.trunk, "feature/patch-a") == refOf(r.trunk, "feature/patch-b") {
			return nil
		}
		runGit(t, r.trunk, "update-ref", "refs/heads/feature/patch-a", refOf(r.trunk, "feature/patch-b"))
		return nil
	}
	r.wtHook = func(g GitRunner) GitRunner { return &spyGit{GitRunner: g, onCherryPick: move} }
	if _, _, err := r.run(context.Background(), StrategyRebase, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.logs.String(), "no job queue") || len(r.audit.ofType(EventRebuildFollowup)) != 0 {
		t.Error("stale input without a queue must be logged only")
	}

	runGit(t, r.trunk, "update-ref", "refs/heads/feature/patch-a", r.base)
	runGit(t, r.trunk, "checkout", "-q", "feature/patch-a")
	commitFile(t, r.trunk, "again.txt", "again\n", "another tip")
	runGit(t, r.trunk, "checkout", "-q", "main")
	_, ctx, _ := r.runningJob("g")
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, r.db, `INSERT INTO jobs (id, type, key, group_key, nonce, status, payload, retry_count, available_at, submitted_by, created_at, updated_at)
		VALUES ('queued-already', 'rebuild', 'ws', 'g', 'queued-already', 'queued', '{}', 0, ?, 'x', ?, ?)`, now, now, now)
	if _, _, err := r.run(ctx, StrategyRebase, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.logs.String(), "follow-up rebuild skipped: a rebuild is already queued") {
		t.Errorf("duplicate follow-up not skipped: %s", r.logs.String())
	}
}

// A nil audit emitter and a nil logger are accepted.
func TestRebuild_NilAuditAndLogger(t *testing.T) {
	r := newRebuild(t)
	r.patches("feature/patch-a")
	r.h.Audit, r.h.Logger = nil, nil
	if _, _, err := r.run(context.Background(), StrategyRebase, ""); err != nil {
		t.Fatal(err)
	}
	r.h.logf("ignored %d", 1)
	if got := splitNonEmpty(" a \n\nb\n"); !equalStrings(got, []string{"a", "b"}) || splitNonEmpty("") != nil {
		t.Errorf("splitNonEmpty = %v", got)
	}
}
