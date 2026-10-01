package carrypatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// ===========================================================================
// Test environment for the worktree-based rebuild (spec 01, task 3)
// ===========================================================================

const (
	wtBaseSHA   = "aaaa000000000000000000000000000000000001"
	wtHeadSHA   = "cccc000000000000000000000000000000000001"
	wtCommitSHA = "bbbb000000000000000000000000000000000001"
)

// capturedLog is a slog.Handler that stores every record.
type capturedLog struct {
	mu      sync.Mutex
	records []capturedRecord
}

type capturedRecord struct {
	level slog.Level
	text  string // message plus flattened attributes
}

func (c *capturedLog) Enabled(context.Context, slog.Level) bool { return true }
func (c *capturedLog) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *capturedLog) WithGroup(string) slog.Handler            { return c }
func (c *capturedLog) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	c.mu.Lock()
	c.records = append(c.records, capturedRecord{level: r.Level, text: sb.String()})
	c.mu.Unlock()
	return nil
}

// has reports whether a record of the given level contains all substrings.
func (c *capturedLog) has(level slog.Level, subs ...string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if r.level != level {
			continue
		}
		ok := true
		for _, s := range subs {
			if !strings.Contains(r.text, s) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// orderedEvents is a concurrency-safe event list shared by several mocks.
type orderedEvents struct {
	mu     sync.Mutex
	events []string
}

func (o *orderedEvents) add(e string) {
	o.mu.Lock()
	o.events = append(o.events, e)
	o.mu.Unlock()
}

func (o *orderedEvents) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

func (o *orderedEvents) index(prefix string) int {
	for i, e := range o.snapshot() {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

// wtEnv wires a RebuildHandler with distinct trunk and worktree mocks.
type wtEnv struct {
	t       *testing.T
	root    string
	slug    string
	trunk   *mockGitRunner
	wt      *mockGitRunner
	patches *mockPatchStore
	h       *RebuildHandler
	events  *orderedEvents
	logs    *capturedLog

	mu              sync.Mutex
	runnerPaths     []string
	dirExistedAtNew map[string]bool
	wtRunnerErr     error
}

func newWtEnv(t *testing.T, patches []Patch) *wtEnv {
	t.Helper()
	e := &wtEnv{
		t:               t,
		root:            t.TempDir(),
		slug:            "wsx",
		trunk:           newMockGitRunner(),
		wt:              newMockGitRunner(),
		patches:         newMockPatchStore(patches),
		events:          &orderedEvents{},
		logs:            &capturedLog{},
		dirExistedAtNew: map[string]bool{},
	}
	run := upstreamBaseMock(wtBaseSHA, wtHeadSHA, wtCommitSHA)
	e.trunk.RunFunc = run
	e.wt.RunFunc = run
	e.trunk.Recorder = func(ev string) { e.events.add("trunk:" + ev) }
	e.wt.Recorder = func(ev string) { e.events.add("wt:" + ev) }
	// Simulate git: creating a worktree makes the directory, removing it
	// deletes the directory.
	e.trunk.OnWorktreeAdd = func(_ context.Context, path, _ string) error {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(path, "marker"), []byte("x"), 0o644)
	}
	e.trunk.OnWorktreeRemove = func(_ context.Context, path string) error {
		return os.RemoveAll(path)
	}
	e.h = &RebuildHandler{
		PatchStore:    e.patches,
		WorkspaceRoot: e.root,
		Logger:        slog.New(e.logs),
		NewGitRunner:  e.newRunner,
		Fetch:         func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth:   func(_ string) (transport.AuthMethod, error) { return nil, nil },
	}
	return e
}

func (e *wtEnv) trunkPath() string { return filepath.Join(e.root, e.slug, "trunk") }

func (e *wtEnv) newRunner(path string) (GitRunner, error) {
	if path == e.trunkPath() {
		return e.trunk, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runnerPaths = append(e.runnerPaths, path)
	_, err := os.Stat(path)
	e.dirExistedAtNew[path] = err == nil
	if e.wtRunnerErr != nil {
		return nil, e.wtRunnerErr
	}
	return e.wt, nil
}

func (e *wtEnv) payload(strategy, failMode string) json.RawMessage {
	b, _ := json.Marshal(RebuildPayload{
		WorkspaceSlug: e.slug,
		Strategy:      strategy,
		FailMode:      failMode,
		SubmittedBy:   "operator",
	})
	return b
}

func (e *wtEnv) rebuildDir() string { return filepath.Join(e.root, e.slug, "rebuild") }

// assertWorktreeGone checks the worktree directory of the (single) WorktreeAdd
// call no longer exists.
func (e *wtEnv) assertWorktreeGone() {
	e.t.Helper()
	for _, c := range e.trunk.WorktreeAddCalls {
		if _, err := os.Stat(c.Path); !os.IsNotExist(err) {
			e.t.Errorf("worktree directory %s still exists (stat err=%v)", c.Path, err)
		}
	}
}

// assertRemovedThenPruned checks WorktreeRemove is immediately followed by
// WorktreePrune on the trunk runner.
func (e *wtEnv) assertRemovedThenPruned() {
	e.t.Helper()
	ri := e.events.index("trunk:WorktreeRemove")
	if ri < 0 {
		e.t.Fatalf("WorktreeRemove not called; events=%v", e.events.snapshot())
	}
	evs := e.events.snapshot()
	if ri+1 >= len(evs) || evs[ri+1] != "trunk:WorktreePrune" {
		e.t.Errorf("WorktreePrune must directly follow WorktreeRemove; events=%v", evs)
	}
}

func assertLockFree(t *testing.T, slug string) {
	t.Helper()
	unlock, ok := wslock.TryLock(slug)
	if !ok {
		t.Fatalf("workspace lock for %q is still held", slug)
	}
	unlock()
}

// forbiddenTrunkCommands are the commands the trunk runner must never run
// during an ordinary rebuild (01-REQ-2.1).
var forbiddenTrunkCommands = []string{"checkout", "reset", "cherry-pick", "merge", "rebase", "log", "commit", "rerere", "diff", "branch"}

func assertTrunkUntouched(t *testing.T, trunk *mockGitRunner) {
	t.Helper()
	trunk.mu.Lock()
	defer trunk.mu.Unlock()
	for _, c := range trunk.RunCalls {
		if len(c.Args) == 0 {
			continue
		}
		for _, f := range forbiddenTrunkCommands {
			if c.Args[0] == f {
				t.Errorf("trunk runner ran forbidden command %v", c.Args)
			}
		}
		for _, a := range c.Args {
			if a == "_rebuild_temp" {
				t.Errorf("trunk runner touched _rebuild_temp: %v", c.Args)
			}
		}
	}
	if len(trunk.CherryPickCalls) != 0 || len(trunk.MergeNoFFCalls) != 0 {
		t.Errorf("trunk runner ran cherry-pick/merge: %v %v", trunk.CherryPickCalls, trunk.MergeNoFFCalls)
	}
}

func onePatch() []Patch {
	return []Patch{{ID: "p1", WorkspaceID: "ws", BranchName: "feature/a", Position: 1, Status: PatchStatusActive}}
}

// ===========================================================================
// TS-01-1: worktree created with WorktreeAdd at an absolute path on the base
// ===========================================================================

func TestWorktreeRebuild_TS_01_1_CreatesAbsoluteWorktreeOnBase(t *testing.T) {
	e := newWtEnv(t, onePatch())
	// A relative workspace root: the path handed to git must still be absolute.
	e.h.WorkspaceRoot = "ws-rel-" + filepath.Base(e.root)
	trunkRel := filepath.Join(e.h.WorkspaceRoot, e.slug, "trunk")
	e.h.NewGitRunner = func(p string) (GitRunner, error) {
		if p == trunkRel {
			return e.trunk, nil
		}
		return e.wt, nil
	}
	e.trunk.OnWorktreeAdd = nil // do not create anything relative to the package dir

	ctx := jobqueue.ContextWithJobID(context.Background(), "job-42")
	if _, _, err := e.h.HandleRebuildJob(ctx, e.payload(StrategyRebase, "")); err != nil {
		t.Fatalf("HandleRebuildJob: %v", err)
	}

	if len(e.trunk.WorktreeAddCalls) != 1 {
		t.Fatalf("expected 1 WorktreeAdd on trunk runner, got %d", len(e.trunk.WorktreeAddCalls))
	}
	if len(e.wt.WorktreeAddCalls) != 0 {
		t.Errorf("WorktreeAdd must not be called on the worktree runner")
	}
	c := e.trunk.WorktreeAddCalls[0]
	if !filepath.IsAbs(c.Path) {
		t.Errorf("worktree path %q is not absolute", c.Path)
	}
	want, _ := filepath.Abs(filepath.Join(e.h.WorkspaceRoot, e.slug, "rebuild", "job-42"))
	if c.Path != want {
		t.Errorf("worktree path = %q, want %q", c.Path, want)
	}
	if c.Commit != wtBaseSHA {
		t.Errorf("worktree commit = %q, want upstream base %q", c.Commit, wtBaseSHA)
	}
}

// ===========================================================================
// TS-01-2: random ID without a job ID; empty root derives from trunk parent
// ===========================================================================

func TestWorktreeRebuild_TS_01_2_RandomIDAndTrunkParent(t *testing.T) {
	tmp := t.TempDir()
	slug := filepath.Join(tmp, "slug", "trunk") // with an empty root the trunk path is the slug
	mock := newMockGitRunner()
	mock.RunFunc = upstreamBaseMock(wtBaseSHA, wtHeadSHA, wtCommitSHA)
	h := &RebuildHandler{
		PatchStore:   newMockPatchStore(onePatch()),
		NewGitRunner: func(string) (GitRunner, error) { return mock, nil },
		Fetch:        func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth:  func(_ string) (transport.AuthMethod, error) { return nil, nil },
	}
	payload, _ := json.Marshal(RebuildPayload{WorkspaceSlug: slug, Strategy: StrategyRebase})

	var paths []string
	for i := 0; i < 2; i++ {
		if _, _, err := h.HandleRebuildJob(context.Background(), payload); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if len(mock.WorktreeAddCalls) <= i {
			t.Fatalf("run %d: WorktreeAdd not called", i)
		}
		paths = append(paths, mock.WorktreeAddCalls[i].Path)
	}
	for _, p := range paths {
		if filepath.Dir(p) != filepath.Join(tmp, "slug", "rebuild") {
			t.Errorf("worktree dir = %q, want %q", filepath.Dir(p), filepath.Join(tmp, "slug", "rebuild"))
		}
		if filepath.Base(p) == "" || filepath.Base(p) == "." {
			t.Errorf("empty id in %q", p)
		}
	}
	if paths[0] == paths[1] {
		t.Errorf("two runs without job ID produced the same path %q", paths[0])
	}
}

// ===========================================================================
// TS-01-3: patch application goes through the worktree runner
// ===========================================================================

func TestWorktreeRebuild_TS_01_3_WorktreeRunnerDoesPatchWork(t *testing.T) {
	for _, strategy := range []string{StrategyRebase, StrategyMerge} {
		t.Run(strategy, func(t *testing.T) {
			e := newWtEnv(t, onePatch())
			// Both strategies hit a conflict that rerere resolves (diff is
			// empty), so rerere/diff/continue/commit all run.
			e.wt.CherryPickFunc = func(_ context.Context, _ string) error { return &CherryPickConflictError{} }
			e.wt.MergeNoFFFunc = func(_ context.Context, _ string) error { return &MergeNoFFConflictError{} }
			base := e.wt.RunFunc
			e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "diff" {
					return "", nil // nothing left unresolved
				}
				return base(ctx, args...)
			}

			if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(strategy, "")); err != nil {
				t.Fatalf("HandleRebuildJob: %v", err)
			}

			e.mu.Lock()
			paths := append([]string(nil), e.runnerPaths...)
			existed := e.dirExistedAtNew
			e.mu.Unlock()
			if len(e.trunk.WorktreeAddCalls) != 1 || len(paths) != 1 || paths[0] != e.trunk.WorktreeAddCalls[0].Path {
				t.Fatalf("NewGitRunner worktree paths = %v, WorktreeAdd calls = %v", paths, e.trunk.WorktreeAddCalls)
			}
			if !existed[paths[0]] {
				t.Error("worktree directory did not exist when NewGitRunner was called")
			}

			joined := func(m *mockGitRunner) []string {
				var out []string
				for _, c := range m.RunCalls {
					out = append(out, strings.Join(c.Args, " "))
				}
				return out
			}
			wtCmds := strings.Join(joined(e.wt), "\n")
			for _, want := range []string{"rerere", "diff --name-only --diff-filter=U", "rev-parse HEAD"} {
				if !strings.Contains(wtCmds, want) {
					t.Errorf("worktree runner never ran %q; got:\n%s", want, wtCmds)
				}
			}
			if strategy == StrategyRebase {
				if !strings.Contains(wtCmds, "log --reverse") || !strings.Contains(wtCmds, "cherry-pick --continue") {
					t.Errorf("rebase: expected log and cherry-pick --continue on worktree runner:\n%s", wtCmds)
				}
				if len(e.wt.CherryPickCalls) == 0 {
					t.Error("rebase: cherry-pick not run on worktree runner")
				}
			} else {
				if !strings.Contains(wtCmds, "commit --no-edit") {
					t.Errorf("merge: expected commit --no-edit on worktree runner:\n%s", wtCmds)
				}
				if len(e.wt.MergeNoFFCalls) == 0 {
					t.Error("merge: MergeNoFF not run on worktree runner")
				}
			}
			assertTrunkUntouched(t, e.trunk)
			if len(e.trunk.UpdateRefCalls) != 1 || e.trunk.UpdateRefCalls[0].Ref != "refs/heads/deploy" {
				t.Errorf("expected one trunk UpdateRef of refs/heads/deploy, got %v", e.trunk.UpdateRefCalls)
			}
		})
	}
}

// ===========================================================================
// TS-01-5: for any exit path the worktree is removed and pruned
// ===========================================================================

func TestWorktreeRebuild_TS_01_5_EveryExitPathRemovesWorktree(t *testing.T) {
	conflictDiff := func(m *mockGitRunner) {
		base := m.RunFunc
		m.RunFunc = func(ctx context.Context, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "diff" {
				return "conflict.txt", nil
			}
			return base(ctx, args...)
		}
	}
	scenarios := []struct {
		name     string
		failMode string
		setup    func(e *wtEnv, cancel context.CancelFunc)
		wantErr  bool
	}{
		{name: "success"},
		{name: "cherry-pick error", wantErr: true, setup: func(e *wtEnv, _ context.CancelFunc) {
			e.wt.CherryPickFunc = func(context.Context, string) error { return errors.New("boom") }
		}},
		{name: "fail-fast conflict", wantErr: true, setup: func(e *wtEnv, _ context.CancelFunc) {
			e.wt.CherryPickFunc = func(context.Context, string) error { return &CherryPickConflictError{} }
			conflictDiff(e.wt)
		}},
		{name: "continue-mode reset failure", failMode: FailModeContinue, wantErr: true, setup: func(e *wtEnv, _ context.CancelFunc) {
			e.wt.CherryPickFunc = func(context.Context, string) error { return &CherryPickConflictError{} }
			conflictDiff(e.wt)
			e.wt.HardResetFunc = func(context.Context, string) error { return errors.New("reset failed") }
		}},
		{name: "cancel in cherry-pick", wantErr: true, setup: func(e *wtEnv, cancel context.CancelFunc) {
			e.wt.CherryPickFunc = func(ctx context.Context, _ string) error { cancel(); return ctx.Err() }
		}},
		{name: "context error from log", wantErr: true, setup: func(e *wtEnv, _ context.CancelFunc) {
			base := e.wt.RunFunc
			e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "log" {
					return "", context.Canceled
				}
				return base(ctx, args...)
			}
		}},
		{name: "rev-parse HEAD failure", wantErr: true, setup: func(e *wtEnv, _ context.CancelFunc) {
			base := e.wt.RunFunc
			e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
				if len(args) == 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
					return "", errors.New("rev-parse failed")
				}
				return base(ctx, args...)
			}
		}},
		{name: "worktree runner creation failure", wantErr: true, setup: func(e *wtEnv, _ context.CancelFunc) {
			e.wtRunnerErr = errors.New("cannot open worktree")
		}},
		{name: "update-ref failure", wantErr: true, setup: func(e *wtEnv, _ context.CancelFunc) {
			e.trunk.UpdateRefErr = errors.New("update-ref failed")
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			e := newWtEnv(t, onePatch())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if sc.setup != nil {
				sc.setup(e, cancel)
			}
			_, _, err := e.h.HandleRebuildJob(ctx, e.payload(StrategyRebase, sc.failMode))
			if (err != nil) != sc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, sc.wantErr)
			}
			if len(e.trunk.WorktreeAddCalls) != 1 {
				t.Fatalf("WorktreeAdd calls = %d, want 1", len(e.trunk.WorktreeAddCalls))
			}
			e.assertRemovedThenPruned()
			e.assertWorktreeGone()
			assertLockFree(t, e.slug)
		})
	}
}

// ===========================================================================
// TS-01-6: failing git worktree remove falls back to RemoveAll and prune
// ===========================================================================

func TestWorktreeRebuild_TS_01_6_RemoveFailureFallsBack(t *testing.T) {
	run := func(removeFails bool) (*wtEnv, any, error) {
		e := newWtEnv(t, onePatch())
		if removeFails {
			e.trunk.WorktreeRemoveErr = errors.New("worktree remove failed")
			e.trunk.OnWorktreeRemove = nil // git did not remove anything
		}
		res, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))
		return e, res, err
	}
	_, res1, err1 := run(false)
	e2, res2, err2 := run(true)

	if err1 != nil || err2 != nil {
		t.Fatalf("errors: ok-run=%v, failing-remove-run=%v", err1, err2)
	}
	if !reflect.DeepEqual(res1, res2) {
		t.Errorf("job result differs when removal fails:\n ok:   %+v\n fail: %+v", res1, res2)
	}
	if len(e2.trunk.WorktreeAddCalls) != 1 {
		t.Fatalf("WorktreeAdd calls = %d, want 1", len(e2.trunk.WorktreeAddCalls))
	}
	e2.assertWorktreeGone()
	e2.assertRemovedThenPruned()
	path := e2.trunk.WorktreeAddCalls[0].Path
	if !e2.logs.has(slog.LevelWarn, e2.slug, path) {
		t.Errorf("expected a warning naming slug %q and path %q; records=%+v", e2.slug, path, e2.logs.records)
	}
}

// ===========================================================================
// TS-01-7: cleanup uses a non-cancelled context with a short deadline
// ===========================================================================

func TestWorktreeRebuild_TS_01_7_CleanupContext(t *testing.T) {
	e := newWtEnv(t, onePatch())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.wt.CherryPickFunc = func(c context.Context, _ string) error { cancel(); return c.Err() }

	type seen struct {
		err      error
		deadline time.Time
		hasDL    bool
	}
	var mu sync.Mutex
	got := map[string]seen{}
	record := func(name string, c context.Context) {
		d, ok := c.Deadline()
		mu.Lock()
		got[name] = seen{err: c.Err(), deadline: d, hasDL: ok}
		mu.Unlock()
	}
	e.trunk.OnWorktreeRemove = func(c context.Context, path string) error {
		record("remove", c)
		return os.RemoveAll(path)
	}
	e.trunk.OnWorktreePrune = func(c context.Context) error { record("prune", c); return nil }

	start := time.Now()
	_, retryable, err := e.h.HandleRebuildJob(ctx, e.payload(StrategyRebase, ""))
	if err == nil || !retryable {
		t.Fatalf("expected retryable error on cancellation, got retryable=%v err=%v", retryable, err)
	}
	for _, name := range []string{"remove", "prune"} {
		s, ok := got[name]
		if !ok {
			t.Fatalf("%s did not run after cancellation", name)
		}
		if s.err != nil {
			t.Errorf("%s ran with a cancelled context: %v", name, s.err)
		}
		if !s.hasDL {
			t.Errorf("%s context has no deadline", name)
		} else if s.deadline.Sub(start) > 2*time.Minute+5*time.Second {
			t.Errorf("%s deadline %v is more than two minutes ahead", name, s.deadline.Sub(start))
		}
	}
	e.assertWorktreeGone()
}

// ===========================================================================
// TS-01-9: a failed worktree creation stops the run and leaves nothing
// ===========================================================================

func TestWorktreeRebuild_TS_01_9_WorktreeAddFailure(t *testing.T) {
	e := newWtEnv(t, onePatch())
	em := newCPAuditEmitter()
	e.h.Audit = em
	e.trunk.WorktreeAddErr = errors.New("worktree add failed")
	// git may have created the directory before failing.
	e.trunk.OnWorktreeAdd = func(_ context.Context, path, _ string) error {
		return os.MkdirAll(path, 0o755)
	}

	_, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))
	if err == nil {
		t.Fatal("expected an error when worktree creation fails")
	}
	if len(e.wt.CherryPickCalls) != 0 || len(e.wt.MergeNoFFCalls) != 0 || len(e.trunk.UpdateRefCalls) != 0 {
		t.Error("no patch may be applied and no ref updated when worktree creation fails")
	}
	if len(e.patches.UpdatedPatches) != 0 || len(e.patches.SoftDeletedPatches) != 0 || e.patches.Compacted {
		t.Error("patch bookkeeping must not run")
	}
	e.assertWorktreeGone()
	if entries, _ := os.ReadDir(e.rebuildDir()); len(entries) != 0 {
		t.Errorf("rebuild dir not empty: %v", entries)
	}
	if wslock.RebuildActive(e.slug) {
		t.Error("rebuild guard still set")
	}
	assertLockFree(t, e.slug)
	failed := false
	for _, ev := range em.Events() {
		if ev.EventType == "hub.rebuild.fail" {
			failed = true
		}
	}
	if !failed {
		t.Error("hub.rebuild.fail not emitted")
	}
}

// ===========================================================================
// TS-01-10: trunk runner runs no mutating commands; legacy helpers are gone
// ===========================================================================

func TestWorktreeRebuild_TS_01_10_TrunkNotMutatedAndLegacyHelpersGone(t *testing.T) {
	// Successful rebuild.
	e := newWtEnv(t, onePatch())
	if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, "")); err != nil {
		t.Fatal(err)
	}
	assertTrunkUntouched(t, e.trunk)

	// Conflicting rebuild (fail-fast).
	e = newWtEnv(t, onePatch())
	e.wt.CherryPickFunc = func(context.Context, string) error { return &CherryPickConflictError{} }
	base := e.wt.RunFunc
	e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "diff" {
			return "conflict.txt", nil
		}
		return base(ctx, args...)
	}
	if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, "")); err == nil {
		t.Fatal("expected conflict error")
	}
	assertTrunkUntouched(t, e.trunk)

	src, err := os.ReadFile("rebuild_executor.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^func\s+(\([^)]*\)\s*)?(currentCheckout|restoreCheckout|preflightCleanup)\b`)
	if m := re.Find(src); m != nil {
		t.Errorf("legacy function still present: %s", m)
	}
}

// ===========================================================================
// TS-01-19: rerere config on the trunk runner before the worktree is added
// ===========================================================================

func TestWorktreeRebuild_TS_01_19_RerereConfigBeforeWorktreeAdd(t *testing.T) {
	e := newWtEnv(t, onePatch())
	if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, "")); err != nil {
		t.Fatal(err)
	}
	add := e.events.index("trunk:WorktreeAdd")
	enabled := e.events.index("trunk:run config rerere.enabled true")
	auto := e.events.index("trunk:run config rerere.autoupdate true")
	if add < 0 || enabled < 0 || auto < 0 {
		t.Fatalf("missing events: add=%d enabled=%d auto=%d; events=%v", add, enabled, auto, e.events.snapshot())
	}
	if enabled > add || auto > add {
		t.Errorf("rerere config must precede WorktreeAdd: enabled=%d autoupdate=%d add=%d", enabled, auto, add)
	}
	for _, ev := range e.events.snapshot() {
		if strings.Contains(ev, "worktreeConfig") {
			t.Errorf("extensions.worktreeConfig must never be touched: %s", ev)
		}
	}
}

// ===========================================================================
// TS-01-54: cancellation discards the worktree and returns a TransientError
// ===========================================================================

func TestWorktreeRebuild_TS_01_54_CancellationIsTransient(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *wtEnv, cancel context.CancelFunc)
	}{
		{"job context cancelled in cherry-pick", func(e *wtEnv, cancel context.CancelFunc) {
			e.wt.CherryPickFunc = func(ctx context.Context, _ string) error { cancel(); return ctx.Err() }
		}},
		{"context error without cancelling job context", func(e *wtEnv, _ context.CancelFunc) {
			e.wt.CherryPickFunc = func(context.Context, string) error { return context.DeadlineExceeded }
		}},
		{"context error from git log is not branch_not_found", func(e *wtEnv, _ context.CancelFunc) {
			base := e.wt.RunFunc
			e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "log" {
					return "", context.Canceled
				}
				return base(ctx, args...)
			}
		}},
		{"cancellation during rerere is not a resolved conflict", func(e *wtEnv, cancel context.CancelFunc) {
			e.wt.CherryPickFunc = func(context.Context, string) error { return &CherryPickConflictError{} }
			base := e.wt.RunFunc
			e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "rerere" {
					cancel()
					return "", ctx.Err()
				}
				return base(ctx, args...)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newWtEnv(t, onePatch())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tc.setup(e, cancel)

			res, retryable, err := e.h.HandleRebuildJob(ctx, e.payload(StrategyRebase, ""))
			var te *TransientError
			if !errors.As(err, &te) {
				t.Fatalf("expected *TransientError, got %T: %v (result %v)", err, err, res)
			}
			if !retryable {
				t.Error("expected retryable=true")
			}
			e.assertWorktreeGone()
			e.assertRemovedThenPruned()
			if wslock.RebuildActive(e.slug) {
				t.Error("guard still set")
			}
			assertTrunkUntouched(t, e.trunk)
			if len(e.trunk.UpdateRefCalls) != 0 {
				t.Error("integration ref must not be updated on cancellation")
			}
			if len(e.patches.UpdatedPatches) != 0 {
				t.Errorf("no patch status may change on cancellation: %v", e.patches.UpdatedPatches)
			}
		})
	}
}

// ===========================================================================
// Real-repository check of the worktree flow (supports 01-REQ-1.4, 2.2)
// ===========================================================================

func TestWorktreeRebuild_RealRepo_TrunkUntouchedAndWorktreeGone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		branches []string
		failMode string
		wantErr  bool
	}{
		{"success", []string{"feature/patch-a", "feature/patch-b"}, "", false},
		{"fail-fast conflict", []string{"feature/patch-a", "feature/conflict"}, FailModeFailFast, true},
		{"continue conflict", []string{"feature/patch-a", "feature/conflict"}, FailModeContinue, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			slug := "ws-real"
			trunk, baseSHA := setupCarryPatchRepo(t, root, slug)
			runGitCmd(t, trunk, "update-ref", "refs/remotes/upstream/HEAD", baseSHA)

			var patches []Patch
			for i, b := range tc.branches {
				patches = append(patches, Patch{ID: fmt.Sprintf("p%d", i), BranchName: b, Position: i + 1, Status: PatchStatusActive})
			}
			h := &RebuildHandler{
				PatchStore:    newMockPatchStore(patches),
				WorkspaceRoot: root,
				NewGitRunner:  NewGitRunnerFactory(),
				Fetch:         func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
				ResolveAuth:   func(_ string) (transport.AuthMethod, error) { return nil, nil },
			}
			snap := func() [4]string {
				return [4]string{
					runGitCmd(t, trunk, "symbolic-ref", "HEAD"),
					runGitCmd(t, trunk, "rev-parse", "HEAD"),
					runGitCmd(t, trunk, "status", "--porcelain"),
					runGitCmd(t, trunk, "show", "HEAD:base.txt"),
				}
			}
			before := snap()

			payload, _ := json.Marshal(RebuildPayload{WorkspaceSlug: slug, Strategy: StrategyRebase, FailMode: tc.failMode, IntegrationBranch: "integration"})
			ctx := jobqueue.ContextWithJobID(context.Background(), "job-real")
			_, _, err := h.HandleRebuildJob(ctx, payload)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}

			if after := snap(); after != before {
				t.Errorf("trunk changed:\n before=%q\n after =%q", before, after)
			}
			list := runGitCmd(t, trunk, "worktree", "list", "--porcelain")
			if n := strings.Count(list, "worktree "); n != 1 {
				t.Errorf("expected only the trunk in git worktree list, got:\n%s", list)
			}
			if _, statErr := os.Stat(filepath.Join(root, slug, "rebuild", "job-real")); !os.IsNotExist(statErr) {
				t.Errorf("worktree directory still exists (err=%v)", statErr)
			}
			if tc.wantErr {
				return
			}
			// The integration branch holds the applied patch, without a checkout.
			if out := runGitCmd(t, trunk, "show", "integration:patch-a.txt"); out != "patch a content" {
				t.Errorf("integration:patch-a.txt = %q", out)
			}
			if out := runGitCmd(t, trunk, "branch", "--list", "_rebuild_temp"); strings.TrimSpace(out) != "" {
				t.Errorf("_rebuild_temp branch exists: %q", out)
			}
		})
	}
}
