package carrypatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// ===========================================================================
// Spec 01, task 5: the integration ref is moved with update-ref, the trunk is
// never touched (Exceptions A and B aside).
// ===========================================================================

// hookRunner wraps a real GitRunner, recording trunk-relevant calls and
// letting a test intercept cherry-pick and merge.
type hookRunner struct {
	GitRunner
	mu           sync.Mutex
	runs         [][]string
	hardResets   []string
	onCherryPick func(ctx context.Context, sha string) error
	onMerge      func(ctx context.Context, ref string) error
}

func (r *hookRunner) Run(ctx context.Context, args ...string) (string, error) {
	r.mu.Lock()
	r.runs = append(r.runs, append([]string(nil), args...))
	r.mu.Unlock()
	return r.GitRunner.Run(ctx, args...)
}

func (r *hookRunner) HardReset(ctx context.Context, ref string) error {
	r.mu.Lock()
	r.hardResets = append(r.hardResets, ref)
	r.mu.Unlock()
	return r.GitRunner.HardReset(ctx, ref)
}

func (r *hookRunner) CherryPick(ctx context.Context, sha string) error {
	if r.onCherryPick != nil {
		if err := r.onCherryPick(ctx, sha); err != nil {
			return err
		}
	}
	return r.GitRunner.CherryPick(ctx, sha)
}

func (r *hookRunner) MergeNoFF(ctx context.Context, ref, message string) error {
	if r.onMerge != nil {
		if err := r.onMerge(ctx, ref); err != nil {
			return err
		}
	}
	return r.GitRunner.MergeNoFF(ctx, ref, message)
}

// resetCalls returns every reset the runner was asked to run, through Run or
// HardReset.
func (r *hookRunner) resetCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.hardResets...)
	for _, a := range r.runs {
		if len(a) > 0 && a[0] == "reset" {
			out = append(out, strings.Join(a, " "))
		}
	}
	return out
}

// realEnv is a real-repository rebuild environment (setupCarryPatchRepo).
type realEnv struct {
	t       *testing.T
	root    string
	slug    string
	trunk   string
	baseSHA string
	h       *RebuildHandler
	logs    *capturedLog

	trunkRunner *hookRunner
	wtRunner    *hookRunner
}

func newRealEnv(t *testing.T) *realEnv {
	t.Helper()
	return newRealEnvAt(t, t.TempDir(), "ws-real")
}

// newRealEnvAt builds the real-repository environment under an existing
// workspace root, for tests that share the root with other fixtures.
func newRealEnvAt(t *testing.T, root, slug string) *realEnv {
	t.Helper()
	e := &realEnv{t: t, root: root, slug: slug, logs: &capturedLog{}}
	e.trunk, e.baseSHA = setupCarryPatchRepo(t, e.root, e.slug)
	runGitCmd(t, e.trunk, "update-ref", "refs/remotes/upstream/HEAD", e.baseSHA)
	e.h = &RebuildHandler{
		PatchStore:    newMockPatchStore(nil),
		WorkspaceRoot: e.root,
		Logger:        slog.New(e.logs),
		NewGitRunner:  e.newRunner,
		Fetch:         func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth:   func(_ string) (transport.AuthMethod, error) { return nil, nil },
	}
	return e
}

func (e *realEnv) newRunner(path string) (GitRunner, error) {
	inner, err := NewGitRunnerFactory()(path)
	if err != nil {
		return nil, err
	}
	hr := &hookRunner{GitRunner: inner}
	if path == e.trunk {
		e.trunkRunner = hr
	} else {
		e.wtRunner = hr
	}
	return hr, nil
}

// setPatches registers the given branches as active patches in order.
func (e *realEnv) setPatches(branches ...string) {
	var patches []Patch
	for i, b := range branches {
		patches = append(patches, Patch{ID: fmt.Sprintf("p%d", i), BranchName: b, Position: i + 1, Status: PatchStatusActive})
	}
	e.h.PatchStore = newMockPatchStore(patches)
}

func (e *realEnv) run(ctx context.Context, jobID, strategy, failMode string) (any, bool, error) {
	e.t.Helper()
	payload, _ := json.Marshal(RebuildPayload{
		WorkspaceSlug:     e.slug,
		Strategy:          strategy,
		FailMode:          failMode,
		IntegrationBranch: "integration",
	})
	return e.h.HandleRebuildJob(jobqueue.ContextWithJobID(ctx, jobID), payload)
}

// trunkSnapshot captures what a rebuild must not change in the trunk.
func (e *realEnv) trunkSnapshot() [4]string {
	e.t.Helper()
	sample, err := os.ReadFile(filepath.Join(e.trunk, "base.txt"))
	if err != nil {
		e.t.Fatalf("read sample file: %v", err)
	}
	return [4]string{
		runGitCmd(e.t, e.trunk, "symbolic-ref", "HEAD"),
		runGitCmd(e.t, e.trunk, "rev-parse", "HEAD"),
		runGitCmd(e.t, e.trunk, "status", "--porcelain"),
		string(sample),
	}
}

func (e *realEnv) worktreeCount() int {
	e.t.Helper()
	return strings.Count(runGitCmd(e.t, e.trunk, "worktree", "list", "--porcelain"), "worktree ")
}

// gitTry runs git and returns the combined output and the error, without
// failing the test.
func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// scenario describes one rebuild outcome against the real repository.
type scenario struct {
	name     string
	branches []string
	strategy string
	failMode string
	cancel   bool // cancel the context inside the first patch step
	inject   bool // fail the first patch step with a non-context error
}

// execute runs the scenario and returns the handler outcome.
func (e *realEnv) execute(sc scenario) (any, bool, error) {
	e.t.Helper()
	e.setPatches(sc.branches...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The worktree runner is created during the run; intercept its steps.
	base := e.h.NewGitRunner
	e.h.NewGitRunner = func(path string) (GitRunner, error) {
		r, err := base(path)
		if err != nil {
			return nil, err
		}
		if path != e.trunk {
			hr := r.(*hookRunner)
			step := func(ctx context.Context) error {
				if sc.cancel {
					cancel()
					return context.Canceled
				}
				if sc.inject {
					return errors.New("injected git failure")
				}
				return nil
			}
			hr.onCherryPick = func(ctx context.Context, _ string) error { return step(ctx) }
			hr.onMerge = func(ctx context.Context, _ string) error { return step(ctx) }
		}
		return r, nil
	}
	defer func() { e.h.NewGitRunner = base }()

	strategy := sc.strategy
	if strategy == "" {
		strategy = StrategyRebase
	}
	return e.run(ctx, "job-"+strings.ReplaceAll(sc.name, " ", "-"), strategy, sc.failMode)
}

var coreScenarios = []scenario{
	{name: "success", branches: []string{"feature/patch-a", "feature/patch-b"}},
	{name: "fail-fast conflict", branches: []string{"feature/patch-a", "feature/conflict"}, failMode: FailModeFailFast},
	{name: "continue conflict", branches: []string{"feature/patch-a", "feature/conflict", "feature/patch-b"}, failMode: FailModeContinue},
	{name: "transient failure", branches: []string{"feature/patch-a"}, inject: true},
	{name: "cancelled", branches: []string{"feature/patch-a"}, cancel: true},
}

// ===========================================================================
// TS-01-4: the worktree directory and registration are gone after success,
// conflict, transient failure and cancellation. Requirement: 01-REQ-1.4
// ===========================================================================

func TestTrunkUntouched_TS_01_4_WorktreeGoneAfterEveryOutcome(t *testing.T) {
	for _, sc := range []scenario{coreScenarios[0], coreScenarios[1], coreScenarios[3], coreScenarios[4]} {
		t.Run(sc.name, func(t *testing.T) {
			e := newRealEnv(t)
			_, retryable, err := e.execute(sc)

			var te *TransientError
			switch sc.name {
			case "success":
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
			case "fail-fast conflict":
				if err == nil || errors.As(err, &te) || !strings.Contains(err.Error(), "conflict") {
					t.Fatalf("expected a non-transient conflict error, got %v", err)
				}
			default:
				if !errors.As(err, &te) || !retryable {
					t.Fatalf("expected retryable *TransientError, got retryable=%v err=%v", retryable, err)
				}
			}

			wtPath := filepath.Join(e.root, e.slug, "rebuild", "job-"+strings.ReplaceAll(sc.name, " ", "-"))
			if _, statErr := os.Stat(wtPath); !os.IsNotExist(statErr) {
				t.Errorf("worktree directory %s still exists (err=%v)", wtPath, statErr)
			}
			if n := e.worktreeCount(); n != 1 {
				t.Errorf("git worktree list shows %d entries, want only the trunk:\n%s", n,
					runGitCmd(t, e.trunk, "worktree", "list"))
			}
			if wslock.RebuildActive(e.slug) {
				t.Error("guard still set")
			}
			assertLockFree(t, e.slug)
		})
	}
}

// ===========================================================================
// TS-01-11: the trunk's HEAD, status and sample file are identical before and
// after success, fail-fast conflict, continue-mode conflict and cancellation.
// Requirement: 01-REQ-2.2
// ===========================================================================

func TestTrunkUntouched_TS_01_11_TrunkSnapshotUnchanged(t *testing.T) {
	for _, sc := range coreScenarios {
		t.Run(sc.name, func(t *testing.T) {
			e := newRealEnv(t)
			before := e.trunkSnapshot()
			_, _, _ = e.execute(sc)
			if after := e.trunkSnapshot(); after != before {
				t.Errorf("trunk changed:\n before=%q\n after =%q", before, after)
			}
			if resets := e.trunkRunner.resetCalls(); len(resets) != 0 {
				t.Errorf("trunk runner ran reset: %v", resets)
			}
			if out, _ := gitTry(e.trunk, "show-ref", "refs/heads/_rebuild_temp"); out != "" {
				t.Errorf("_rebuild_temp exists: %q", out)
			}
		})
	}
}

// ===========================================================================
// TS-01-12 (property): for any generated rebuild the trunk checkout is
// unchanged. Requirement: 01-REQ-2.2
// ===========================================================================

func TestTrunkUntouched_TS_01_12_Property(t *testing.T) {
	rng := rand.New(rand.NewSource(20260101))
	pool := []string{"feature/patch-a", "feature/patch-b", "feature/conflict", "feature/missing-1", "feature/missing-2"}
	iterations := 14
	if testing.Short() {
		iterations = 4
	}
	for i := 0; i < iterations; i++ {
		n := 1 + rng.Intn(5)
		perm := rng.Perm(len(pool))[:n]
		var branches []string
		for _, p := range perm {
			branches = append(branches, pool[p])
		}
		sc := scenario{
			name:     fmt.Sprintf("gen-%d", i),
			branches: branches,
			strategy: []string{StrategyRebase, StrategyMerge}[rng.Intn(2)],
			failMode: []string{FailModeFailFast, FailModeContinue}[rng.Intn(2)],
			cancel:   rng.Intn(4) == 0,
		}
		t.Run(sc.name, func(t *testing.T) {
			e := newRealEnv(t)
			before := e.trunkSnapshot()
			_, _, _ = e.execute(sc)
			if after := e.trunkSnapshot(); after != before {
				t.Errorf("cfg %+v: trunk changed:\n before=%q\n after =%q", sc, before, after)
			}
			if n := e.worktreeCount(); n != 1 {
				t.Errorf("cfg %+v: %d worktrees registered after the run", sc, n)
			}
		})
	}
}

// ===========================================================================
// TS-01-13: the integration branch is updated with update-ref to the worktree
// HEAD SHA and never with branch -f. Requirement: 01-REQ-2.3
// ===========================================================================

func TestTrunkUntouched_TS_01_13_UpdateRefToWorktreeHead(t *testing.T) {
	e := newWtEnv(t, onePatch())
	base := upstreamBaseMock(wtBaseSHA, wtHeadSHA, wtCommitSHA)
	e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
		if len(args) == 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			return "abc123", nil
		}
		return base(ctx, args...)
	}

	res, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))
	if err != nil {
		t.Fatalf("HandleRebuildJob: %v", err)
	}
	if got := res.(*RebuildResult).IntegrationHeadSHA; got != "abc123" {
		t.Errorf("IntegrationHeadSHA = %q, want abc123", got)
	}
	if len(e.trunk.UpdateRefCalls) != 1 {
		t.Fatalf("expected one UpdateRef on the trunk runner, got %v", e.trunk.UpdateRefCalls)
	}
	if c := e.trunk.UpdateRefCalls[0]; c.Ref != "refs/heads/deploy" || c.SHA != "abc123" {
		t.Errorf("UpdateRef = %+v, want refs/heads/deploy abc123", c)
	}
	for _, c := range e.trunk.RunCalls {
		if len(c.Args) > 0 && (c.Args[0] == "branch" || c.Args[0] == "checkout") {
			t.Errorf("trunk ran %v", c.Args)
		}
	}
	assertTrunkUntouched(t, e.trunk)
	if len(e.trunk.HardResetCalls) != 0 {
		t.Errorf("trunk must not be reset when it is not on the integration branch: %v", e.trunk.HardResetCalls)
	}
}

// ===========================================================================
// TS-01-14: the previous head is read immediately before the update, under the
// lock, and recorded; empty when the branch is absent. Requirement: 01-REQ-2.4
// ===========================================================================

func TestTrunkUntouched_TS_01_14_PreviousHeadReadBeforeUpdate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		exists bool
		want   string
	}{
		{"exists", true, "old1"},
		{"absent", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newWtEnv(t, onePatch())
			e.trackState()
			base := upstreamBaseMock(wtBaseSHA, wtHeadSHA, wtCommitSHA)
			e.trunk.RunFunc = func(ctx context.Context, args ...string) (string, error) {
				if strings.Join(args, " ") == "rev-parse --verify refs/heads/deploy" {
					if tc.exists {
						return "old1", nil
					}
					return "", errors.New("fatal: Needed a single revision")
				}
				return base(ctx, args...)
			}

			res, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))
			if err != nil {
				t.Fatalf("HandleRebuildJob: %v", err)
			}
			if got := res.(*RebuildResult).PreviousIntegrationHeadSHA; got != tc.want {
				t.Errorf("previous_integration_head_sha = %q, want %q", got, tc.want)
			}

			ri, read := e.find(hasPrefix("trunk:run rev-parse --verify refs/heads/deploy|"))
			ui, update := e.find(hasPrefix("trunk:UpdateRef refs/heads/deploy|"))
			if ri < 0 || ui < 0 {
				t.Fatalf("read or update missing; events=%v", e.events.snapshot())
			}
			if ui != ri+1 {
				t.Errorf("update must immediately follow the read (read at %d, update at %d); events=%v", ri, ui, e.events.snapshot())
			}
			if !strings.Contains(read, "locked=true") || !strings.Contains(update, "locked=true") {
				t.Errorf("read and update must run under the lock:\n %s\n %s", read, update)
			}
		})
	}
}

// ===========================================================================
// TS-01-15: the trunk is hard-reset only when its HEAD is the integration
// branch. Requirement: 01-REQ-2.5
// ===========================================================================

func TestTrunkUntouched_TS_01_15_ExceptionA_ResetOnlyWhenCheckedOut(t *testing.T) {
	t.Run("integration checked out", func(t *testing.T) {
		e := newRealEnv(t)
		runGitCmd(t, e.trunk, "checkout", "integration")
		oldTip := runGitCmd(t, e.trunk, "rev-parse", "HEAD")
		e.setPatches("feature/patch-a")

		res, _, err := e.run(context.Background(), "job-a", StrategyRebase, "")
		if err != nil {
			t.Fatalf("rebuild: %v", err)
		}
		newTip := res.(*RebuildResult).IntegrationHeadSHA
		if newTip == oldTip {
			t.Fatal("integration tip did not move")
		}
		if got := runGitCmd(t, e.trunk, "rev-parse", "HEAD"); got != newTip {
			t.Errorf("trunk HEAD = %s, want new tip %s", got, newTip)
		}
		if got := runGitCmd(t, e.trunk, "symbolic-ref", "HEAD"); got != "refs/heads/integration" {
			t.Errorf("trunk HEAD moved off the integration branch: %s", got)
		}
		if st := runGitCmd(t, e.trunk, "status", "--porcelain"); st != "" {
			t.Errorf("trunk status not clean: %q", st)
		}
		if _, err := os.Stat(filepath.Join(e.trunk, "patch-a.txt")); err != nil {
			t.Errorf("trunk files do not match the new tip: %v", err)
		}
		if !e.logs.has(slog.LevelInfo, "reset", e.slug) {
			t.Errorf("no info log records the trunk reset; logs=%+v", e.logs.records)
		}
		if resets := e.trunkRunner.resetCalls(); len(resets) != 1 {
			t.Errorf("expected exactly one reset in the trunk, got %v", resets)
		}
	})

	t.Run("other branch", func(t *testing.T) {
		e := newRealEnv(t)
		before := e.trunkSnapshot()
		e.setPatches("feature/patch-a")
		if _, _, err := e.run(context.Background(), "job-b", StrategyRebase, ""); err != nil {
			t.Fatalf("rebuild: %v", err)
		}
		if resets := e.trunkRunner.resetCalls(); len(resets) != 0 {
			t.Errorf("trunk reset although another branch is checked out: %v", resets)
		}
		if after := e.trunkSnapshot(); after != before {
			t.Errorf("trunk changed: %q -> %q", before, after)
		}
		if e.logs.has(slog.LevelInfo, "reset") {
			t.Error("a reset was logged although none ran")
		}
	})
}

// ===========================================================================
// TS-01-16: a leftover _rebuild_temp is deleted and logged in phase one.
// Requirement: 01-REQ-2.6
// ===========================================================================

func TestTrunkUntouched_TS_01_16_LeftoverRebuildTempDeleted(t *testing.T) {
	e := newRealEnv(t)
	runGitCmd(t, e.trunk, "branch", "_rebuild_temp", "main")
	before := e.trunkSnapshot()
	e.setPatches("feature/patch-a")

	if _, _, err := e.run(context.Background(), "job-16", StrategyRebase, ""); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if out, err := gitTry(e.trunk, "show-ref", "refs/heads/_rebuild_temp"); err == nil {
		t.Errorf("_rebuild_temp still exists: %s", out)
	}
	if !e.logs.has(slog.LevelInfo, "_rebuild_temp", e.slug) {
		t.Errorf("no info log with the slug records the removal; logs=%+v", e.logs.records)
	}
	if after := e.trunkSnapshot(); after != before {
		t.Errorf("trunk changed: %q -> %q", before, after)
	}
}

// ===========================================================================
// TS-01-17: a trunk left on _rebuild_temp mid-cherry-pick is moved to the
// workspace branch, else origin/HEAD, else detached, before the branch is
// deleted. Requirement: 01-REQ-2.7
// ===========================================================================

func TestTrunkUntouched_TS_01_17_ExceptionB_MigrationTargets(t *testing.T) {
	for _, tc := range []struct {
		name        string
		wsBranch    bool
		originHead  bool
		wantSymbolc string // "" = detached
		wantWarn    bool
	}{
		{"workspace branch", true, true, "refs/heads/wsbranch", false},
		{"origin HEAD", false, true, "refs/heads/devbr", false},
		{"detached", false, false, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRealEnv(t)
			runGitCmd(t, e.trunk, "branch", "wsbranch", "main")
			if tc.originHead {
				runGitCmd(t, e.trunk, "branch", "devbr", "main")
				runGitCmd(t, e.trunk, "update-ref", "refs/remotes/origin/devbr", "main")
				runGitCmd(t, e.trunk, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/devbr")
			}
			if tc.wsBranch {
				db := openTestDB(t)
				createWorkspacesTable(t, db)
				seedWorkspace(t, db, e.slug, "alice", "active", "ready", "carry_patch", "integration")
				if _, err := db.Exec(`UPDATE workspaces SET branch = ? WHERE slug = ?`, "wsbranch", e.slug); err != nil {
					t.Fatal(err)
				}
				e.h.DB = db
			}

			// A legacy run left the trunk on _rebuild_temp, mid cherry-pick.
			runGitCmd(t, e.trunk, "checkout", "-b", "_rebuild_temp")
			if _, err := gitTry(e.trunk, "cherry-pick", "feature/conflict"); err == nil {
				t.Fatal("expected the setup cherry-pick to conflict")
			}
			if _, err := gitTry(e.trunk, "rev-parse", "--verify", "-q", "CHERRY_PICK_HEAD"); err != nil {
				t.Fatal("setup: no cherry-pick in progress")
			}
			e.setPatches("feature/patch-a")

			if _, _, err := e.run(context.Background(), "job-17", StrategyRebase, ""); err != nil {
				t.Fatalf("rebuild: %v", err)
			}

			if _, err := gitTry(e.trunk, "rev-parse", "--verify", "-q", "CHERRY_PICK_HEAD"); err == nil {
				t.Error("the in-progress cherry-pick was not aborted")
			}
			sym, err := gitTry(e.trunk, "symbolic-ref", "-q", "HEAD")
			if tc.wantSymbolc == "" {
				if err == nil {
					t.Errorf("expected a detached HEAD, got %s", sym)
				}
			} else if sym != tc.wantSymbolc {
				t.Errorf("trunk HEAD = %q, want %q", sym, tc.wantSymbolc)
			}
			if out, err := gitTry(e.trunk, "show-ref", "refs/heads/_rebuild_temp"); err == nil {
				t.Errorf("_rebuild_temp still exists: %s", out)
			}
			if st := runGitCmd(t, e.trunk, "status", "--porcelain"); st != "" {
				t.Errorf("trunk not clean after migration: %q", st)
			}
			if got := e.logs.has(slog.LevelWarn, "detach"); got != tc.wantWarn {
				t.Errorf("detached warning logged = %v, want %v", got, tc.wantWarn)
			}
		})
	}
}

// ===========================================================================
// TS-01-18: a failing update-ref stops phase two before bookkeeping or push,
// releases the lock and returns an error. Requirement: 01-REQ-2.8
// ===========================================================================

func TestTrunkUntouched_TS_01_18_UpdateRefFailureStopsPhaseTwo(t *testing.T) {
	e := newWtEnv(t, []Patch{
		{ID: "p-merged", BranchName: "feature/merged", Position: 1, Status: PatchStatusMergedUpstream},
		{ID: "p-active", BranchName: "feature/a", Position: 2, Status: PatchStatusActive},
	})
	e.trunk.UpdateRefErr = errors.New("cannot lock ref")
	em := &hookEmitter{}
	e.h.Audit = em
	pushed := 0
	e.h.PushIntegration = func(context.Context, string, string, string) error { pushed++; return nil }
	e.h.GetVariable = func(_, _, key string) (string, error) {
		if key == "REBUILD_PUSH_INTEGRATION_BRANCH" {
			return "true", nil
		}
		return "", nil
	}

	_, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))
	if err == nil {
		t.Fatal("expected an error when update-ref fails")
	}
	if len(e.patches.SoftDeletedPatches) != 0 || e.patches.Compacted {
		t.Errorf("bookkeeping ran: softDeleted=%v compacted=%v", e.patches.SoftDeletedPatches, e.patches.Compacted)
	}
	if pushed != 0 {
		t.Errorf("PushIntegration called %d times", pushed)
	}
	assertLockFree(t, e.slug)
	if wslock.RebuildActive(e.slug) {
		t.Error("guard still set")
	}
	found := false
	for _, ty := range em.types() {
		if ty == "hub.rebuild.fail" {
			found = true
		}
		if ty == "hub.rebuild.complete" {
			t.Error("hub.rebuild.complete emitted on failure")
		}
	}
	if !found {
		t.Errorf("hub.rebuild.fail not emitted; got %v", em.types())
	}
	e.assertWorktreeGone()
}

// ===========================================================================
// TS-01-30: a rollback during patch application is overwritten by the
// rebuild's update-ref and previous_integration_head_sha records the
// rolled-back SHA. Requirement: 01-REQ-4.7
// ===========================================================================

func TestTrunkUntouched_TS_01_30_RollbackDuringApplicationOverwritten(t *testing.T) {
	e := newRealEnv(t)
	e.setPatches("feature/patch-a")
	rollbackTarget := runGitCmd(t, e.trunk, "rev-parse", "feature/patch-b")
	integrationBefore := runGitCmd(t, e.trunk, "rev-parse", "integration")
	if integrationBefore == rollbackTarget {
		t.Fatal("setup: rollback target equals the integration tip")
	}

	blocked := make(chan struct{})
	release := make(chan struct{})
	base := e.h.NewGitRunner
	e.h.NewGitRunner = func(path string) (GitRunner, error) {
		r, err := base(path)
		if err == nil && path != e.trunk {
			r.(*hookRunner).onCherryPick = func(context.Context, string) error {
				close(blocked)
				<-release
				return nil
			}
		}
		return r, err
	}

	type outcome struct {
		res any
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, _, err := e.run(context.Background(), "job-30", StrategyRebase, "")
		done <- outcome{res, err}
	}()
	<-blocked

	// The rollback takes the workspace lock like the rollback handler does;
	// it must not be blocked by the running rebuild.
	unlock, ok := wslock.TryLock(e.slug)
	if !ok {
		close(release)
		<-done
		t.Fatal("workspace lock is held during patch application")
	}
	runGitCmd(t, e.trunk, "branch", "-f", "integration", rollbackTarget)
	unlock()
	if got := runGitCmd(t, e.trunk, "rev-parse", "integration"); got != rollbackTarget {
		t.Fatalf("rollback did not take effect: %s", got)
	}

	close(release)
	out := <-done
	if out.err != nil {
		t.Fatalf("rebuild: %v", out.err)
	}
	res := out.res.(*RebuildResult)
	if got := runGitCmd(t, e.trunk, "rev-parse", "integration"); got != res.IntegrationHeadSHA {
		t.Errorf("integration = %s, want the rebuild's final head %s", got, res.IntegrationHeadSHA)
	}
	if res.PreviousIntegrationHeadSHA != rollbackTarget {
		t.Errorf("previous_integration_head_sha = %q, want the rolled-back %q", res.PreviousIntegrationHeadSHA, rollbackTarget)
	}
}
