package carrypatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// ===========================================================================
// Lock-phase tests for the rebuild (spec 01, task 4)
// ===========================================================================

// lockHeld reports whether the workspace lock for slug is currently held.
func lockHeld(slug string) bool {
	unlock, ok := wslock.TryLock(slug)
	if ok {
		unlock()
	}
	return !ok
}

// hookEmitter is an audit.Emitter that calls fn for every event.
type hookEmitter struct {
	mu     sync.Mutex
	events []audit.HubEvent
	fn     func(audit.HubEvent)
}

func (h *hookEmitter) Emit(_ context.Context, ev audit.HubEvent) error {
	if h.fn != nil {
		h.fn(ev)
	}
	h.mu.Lock()
	h.events = append(h.events, ev)
	h.mu.Unlock()
	return nil
}

func (h *hookEmitter) types() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		out = append(out, e.EventType)
	}
	return out
}

// trackState makes every trunk and worktree runner event carry the workspace
// lock state and the rebuild guard state observed at the time of the call:
// "<runner>:<event>|locked=<bool>|active=<bool>".
func (e *wtEnv) trackState() {
	rec := func(prefix string) func(string) {
		return func(ev string) {
			e.events.add(fmt.Sprintf("%s:%s|locked=%v|active=%v", prefix, ev, lockHeld(e.slug), wslock.RebuildActive(e.slug)))
		}
	}
	e.trunk.Recorder = rec("trunk")
	e.wt.Recorder = rec("wt")
}

// mark records a named, state-annotated event from a callback.
func (e *wtEnv) mark(name string) {
	e.events.add(fmt.Sprintf("%s|locked=%v|active=%v", name, lockHeld(e.slug), wslock.RebuildActive(e.slug)))
}

// find returns the index and text of the first event for which pred is true.
func (e *wtEnv) find(pred func(string) bool) (int, string) {
	for i, ev := range e.events.snapshot() {
		if pred(ev) {
			return i, ev
		}
	}
	return -1, ""
}

func hasPrefix(p string) func(string) bool {
	return func(s string) bool { return strings.HasPrefix(s, p) }
}

// ===========================================================================
// TS-01-24: credentials first, then the blocking lock, BeginRebuild under it
// ===========================================================================

func TestRebuildPhases_TS_01_24_CredentialsThenLockThenGuard(t *testing.T) {
	e := newWtEnv(t, onePatch())
	e.h.ResolveAuth = func(string) (transport.AuthMethod, error) {
		e.mark("creds")
		return nil, nil
	}
	e.h.Fetch = func(context.Context, string, transport.AuthMethod) error {
		// The first step after Lock + BeginRebuild.
		e.mark("fetch")
		return nil
	}

	if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, "")); err != nil {
		t.Fatalf("HandleRebuildJob: %v", err)
	}

	ci, creds := e.find(hasPrefix("creds"))
	fi, fetch := e.find(hasPrefix("fetch"))
	if ci < 0 || fi < 0 || ci > fi {
		t.Fatalf("expected creds before fetch; events=%v", e.events.snapshot())
	}
	if !strings.Contains(creds, "locked=false") || !strings.Contains(creds, "active=false") {
		t.Errorf("credentials must be resolved before the lock and the guard: %s", creds)
	}
	if !strings.Contains(fetch, "locked=true") || !strings.Contains(fetch, "active=true") {
		t.Errorf("BeginRebuild must run under the lock before the fetch: %s", fetch)
	}
	if wslock.RebuildActive(e.slug) {
		t.Error("guard still set after return")
	}
	assertLockFree(t, e.slug)
}

// ===========================================================================
// TS-01-25: a credential failure is a TransientError before lock and guard
// ===========================================================================

func TestRebuildPhases_TS_01_25_CredentialFailureIsTransient(t *testing.T) {
	e := newWtEnv(t, onePatch())
	em := &hookEmitter{}
	e.h.Audit = em
	e.h.ResolveAuth = func(string) (transport.AuthMethod, error) {
		if lockHeld(e.slug) || wslock.RebuildActive(e.slug) {
			t.Error("credentials resolved while lock or guard held")
		}
		return nil, errors.New("no credentials")
	}
	fetched := false
	e.h.Fetch = func(context.Context, string, transport.AuthMethod) error { fetched = true; return nil }

	_, retryable, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))
	var te *TransientError
	if !errors.As(err, &te) || !retryable {
		t.Fatalf("expected retryable *TransientError, got retryable=%v err=%v", retryable, err)
	}
	if fetched {
		t.Error("fetch must not run after a credential failure")
	}
	if wslock.RebuildActive(e.slug) {
		t.Error("guard set after credential failure")
	}
	assertLockFree(t, e.slug)
	found := false
	for _, ty := range em.types() {
		if ty == "hub.rebuild.fail" {
			found = true
		}
	}
	if !found {
		t.Errorf("hub.rebuild.fail not emitted; got %v", em.types())
	}
}

// ===========================================================================
// TS-01-26: phase one runs its steps in order under the lock, then unlocks
// ===========================================================================

func TestRebuildPhases_TS_01_26_PhaseOneOrderUnderLock(t *testing.T) {
	e := newWtEnv(t, onePatch())
	e.trackState()
	e.h.Fetch = func(context.Context, string, transport.AuthMethod) error {
		e.mark("fetch")
		return nil
	}
	e.h.NewGitRunner = func(path string) (GitRunner, error) {
		if path == e.trunkPath() {
			e.mark("newTrunkRunner")
			return e.trunk, nil
		}
		return e.newRunner(path)
	}

	if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, "")); err != nil {
		t.Fatalf("HandleRebuildJob: %v", err)
	}

	steps := []struct {
		name string
		pred func(string) bool
	}{
		{"fetch", hasPrefix("fetch")},
		{"trunk runner", hasPrefix("newTrunkRunner")},
		{"resolveUpstreamBase", func(s string) bool {
			return strings.HasPrefix(s, "trunk:run rev-parse --verify refs/remotes/upstream/HEAD")
		}},
		{"stale cleanup (prune)", hasPrefix("trunk:WorktreePrune")},
		{"rerere config", hasPrefix("trunk:run config rerere.enabled true")},
		{"rerere autoupdate", hasPrefix("trunk:run config rerere.autoupdate true")},
	}
	prev := -1
	for _, s := range steps {
		i, ev := e.find(s.pred)
		if i < 0 {
			t.Fatalf("step %q not found; events=%v", s.name, e.events.snapshot())
		}
		if i <= prev {
			t.Errorf("step %q out of order (index %d after %d); events=%v", s.name, i, prev, e.events.snapshot())
		}
		prev = i
		if !strings.Contains(ev, "locked=true") {
			t.Errorf("step %q must run under the lock: %s", s.name, ev)
		}
		if !strings.Contains(ev, "active=true") {
			t.Errorf("step %q must run under the guard: %s", s.name, ev)
		}
	}
	ai, add := e.find(hasPrefix("trunk:WorktreeAdd"))
	if ai <= prev {
		t.Errorf("WorktreeAdd must follow phase one; events=%v", e.events.snapshot())
	}
	if !strings.Contains(add, "locked=false") {
		t.Errorf("lock must be released before WorktreeAdd: %s", add)
	}
}

// ===========================================================================
// TS-01-27: sync, rollback and rerere forget proceed during patch application
// ===========================================================================

func TestRebuildPhases_TS_01_27_OperationsProceedDuringPatchApplication(t *testing.T) {
	const slug = "wsx"
	env := newFullTestEnv(t)
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000009",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)
	newUpstream := "dddd000000000000000000000000000000000001"
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "rev-parse" {
			return newUpstream, nil
		}
		return "", nil
	}

	previousSHA := "prev000000000000000000000000000000000001"
	resultJSON := []byte(fmt.Sprintf(`{"previous_integration_head_sha":%q,"strategy":"rebase"}`, previousSHA))
	seedRebuildJobWithResult(t, env.db, "job-prev", "completed", slug, "rebase", time.Now(), resultJSON)
	setupRRCacheDir(t, env.workspaceRoot, slug, []rrCacheEntry{
		{hash: "aabbccdd1", preimage: "<<<<<<< src/config.go\nours\n=======\ntheirs\n>>>>>>>"},
	})

	// The rebuild blocks inside its first cherry-pick.
	e := newWtEnv(t, onePatch())
	blocked := make(chan struct{})
	release := make(chan struct{})
	e.wt.CherryPickFunc = func(context.Context, string) error {
		close(blocked)
		<-release
		return nil
	}

	type outcome struct {
		res any
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))
		done <- outcome{res, err}
	}()

	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("rebuild never reached the patch step")
	}
	released := false
	releaseOnce := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer releaseOnce()

	if lockHeld(slug) {
		t.Error("workspace lock is held during patch application")
	}
	if !wslock.RebuildActive(slug) {
		t.Error("guard must be set during patch application")
	}

	auth := rebuildUserAuth("alice")
	base := "/api/v1/workspaces/" + slug
	rec := env.doRequest(t, http.MethodPost, base+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Errorf("sync status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}
	var stored string
	if err := env.db.QueryRow(`SELECT upstream_head_sha FROM workspaces WHERE slug = ?`, slug).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != newUpstream {
		t.Errorf("upstream_head_sha = %q, want %q", stored, newUpstream)
	}
	rec = env.doRequest(t, http.MethodPost, base+"/rebuilds/job-prev/rollback", "", auth)
	if rec.Code != http.StatusOK {
		t.Errorf("rollback status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}
	rec = env.doRequest(t, http.MethodDelete, base+"/rerere/src/config.go", "", auth)
	if rec.Code != http.StatusNoContent {
		t.Errorf("rerere forget status = %d; want 204; body: %s", rec.Code, rec.Body.String())
	}

	releaseOnce()
	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("rebuild failed: %v", o.err)
		}
		res, ok := o.res.(*RebuildResult)
		if !ok {
			t.Fatalf("result type %T", o.res)
		}
		if res.UpstreamHeadSHA != wtBaseSHA {
			t.Errorf("rebuild base = %q, want phase-one base %q", res.UpstreamHeadSHA, wtBaseSHA)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("rebuild did not finish")
	}
	if wslock.RebuildActive(slug) {
		t.Error("guard still set after the run")
	}
}

// ===========================================================================
// TS-01-28: phase two re-acquires the lock for ref update, bookkeeping, push
// ===========================================================================

// lockSpyStore reports the lock state at soft-delete and compaction.
type lockSpyStore struct {
	*mockPatchStore
	onSoftDelete func()
	onCompact    func()
}

func (s *lockSpyStore) SoftDeletePatch(ctx context.Context, id string) error {
	s.onSoftDelete()
	return s.mockPatchStore.SoftDeletePatch(ctx, id)
}

func (s *lockSpyStore) CompactPositions(ctx context.Context, slug string) error {
	s.onCompact()
	return s.mockPatchStore.CompactPositions(ctx, slug)
}

func TestRebuildPhases_TS_01_28_PhaseTwoUnderSecondLock(t *testing.T) {
	patches := []Patch{
		{ID: "p1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", BranchName: "feature/merged", Position: 2, Status: PatchStatusMergedUpstream},
	}
	e := newWtEnv(t, patches)
	e.trackState()
	e.h.PatchStore = &lockSpyStore{
		mockPatchStore: e.patches,
		onSoftDelete:   func() { e.mark("softDelete") },
		onCompact:      func() { e.mark("compact") },
	}
	e.h.GetVariable = func(_, _, key string) (string, error) {
		if key == "REBUILD_PUSH_INTEGRATION_BRANCH" {
			return "true", nil
		}
		return "", nil
	}
	e.h.PushIntegration = func(context.Context, string, string, string) error {
		e.mark("push")
		return nil
	}

	if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, "")); err != nil {
		t.Fatalf("HandleRebuildJob: %v", err)
	}

	isPrevRead := func(s string) bool {
		return strings.HasPrefix(s, "trunk:run rev-parse --verify") && strings.Contains(s, "deploy|")
	}
	steps := []struct {
		name string
		pred func(string) bool
	}{
		{"read previous head", isPrevRead},
		{"update-ref", hasPrefix("trunk:UpdateRef")},
		{"soft delete", hasPrefix("softDelete")},
		{"compact", hasPrefix("compact")},
		{"push", hasPrefix("push")},
	}
	prev := -1
	for _, s := range steps {
		i, ev := e.find(s.pred)
		if i < 0 {
			t.Fatalf("step %q not found; events=%v", s.name, e.events.snapshot())
		}
		if i <= prev {
			t.Errorf("step %q out of order; events=%v", s.name, e.events.snapshot())
		}
		prev = i
		if !strings.Contains(ev, "locked=true") {
			t.Errorf("step %q must run under the lock: %s", s.name, ev)
		}
	}
	// Patch application ran before phase two, outside any lock.
	ci, cp := e.find(hasPrefix("wt:cherry-pick"))
	ri, _ := e.find(isPrevRead)
	if ci < 0 || ci > ri {
		t.Errorf("patch application must precede phase two; events=%v", e.events.snapshot())
	}
	if !strings.Contains(cp, "locked=false") {
		t.Errorf("cherry-pick ran under the lock: %s", cp)
	}
	assertLockFree(t, e.slug)
}

// ===========================================================================
// TS-01-29: for any early return the lock is released and worktree work runs
// outside any lock
// ===========================================================================

type failingListStore struct{ *mockPatchStore }

func (failingListStore) ListPatches(context.Context, string) ([]Patch, error) {
	return nil, errors.New("list failed")
}

func TestRebuildPhases_TS_01_29_NoLockAroundWorktreeWorkForAnyFailure(t *testing.T) {
	base := func(m *mockGitRunner, match func(args []string) bool, err error) {
		prev := m.RunFunc
		m.RunFunc = func(ctx context.Context, args ...string) (string, error) {
			if match(args) {
				return "", err
			}
			return prev(ctx, args...)
		}
	}
	scenarios := []struct {
		name  string
		setup func(e *wtEnv, cancel context.CancelFunc)
	}{
		{"success", func(*wtEnv, context.CancelFunc) {}},
		{"fetch fails", func(e *wtEnv, _ context.CancelFunc) {
			e.h.Fetch = func(context.Context, string, transport.AuthMethod) error { return errors.New("fetch failed") }
		}},
		{"trunk runner creation fails", func(e *wtEnv, _ context.CancelFunc) {
			e.h.NewGitRunner = func(path string) (GitRunner, error) {
				if path == e.trunkPath() {
					return nil, errors.New("no trunk")
				}
				return e.newRunner(path)
			}
		}},
		{"base resolution fails", func(e *wtEnv, _ context.CancelFunc) {
			e.trunk.RunFunc = func(context.Context, ...string) (string, error) { return "", errors.New("rev-parse failed") }
		}},
		{"worktree add fails", func(e *wtEnv, _ context.CancelFunc) {
			e.trunk.WorktreeAddErr = errors.New("add failed")
		}},
		{"worktree runner creation fails", func(e *wtEnv, _ context.CancelFunc) {
			e.wtRunnerErr = errors.New("cannot open worktree")
		}},
		{"list patches fails", func(e *wtEnv, _ context.CancelFunc) {
			e.h.PatchStore = failingListStore{e.patches}
		}},
		{"cherry-pick fails", func(e *wtEnv, _ context.CancelFunc) {
			e.wt.CherryPickFunc = func(context.Context, string) error { return errors.New("boom") }
		}},
		{"cancelled in cherry-pick", func(e *wtEnv, cancel context.CancelFunc) {
			e.wt.CherryPickFunc = func(ctx context.Context, _ string) error { cancel(); return ctx.Err() }
		}},
		{"final HEAD read fails", func(e *wtEnv, _ context.CancelFunc) {
			base(e.wt, func(a []string) bool { return len(a) == 2 && a[0] == "rev-parse" && a[1] == "HEAD" }, errors.New("rev-parse failed"))
		}},
		{"update-ref fails", func(e *wtEnv, _ context.CancelFunc) {
			e.trunk.UpdateRefErr = errors.New("update-ref failed")
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			e := newWtEnv(t, onePatch())
			e.trackState()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sc.setup(e, cancel)

			_, _, _ = e.h.HandleRebuildJob(ctx, e.payload(StrategyRebase, ""))

			assertLockFree(t, e.slug)
			if wslock.RebuildActive(e.slug) {
				t.Error("guard still set")
			}
			for _, ev := range e.events.snapshot() {
				worktreeWork := strings.HasPrefix(ev, "wt:") ||
					strings.HasPrefix(ev, "trunk:WorktreeAdd") ||
					strings.HasPrefix(ev, "trunk:WorktreeRemove")
				if worktreeWork && !strings.Contains(ev, "locked=false") {
					t.Errorf("worktree work ran under the lock: %s", ev)
				}
			}
		})
	}
}

// ===========================================================================
// TS-01-31: after phase two: worktree removal, complete event, guard end
// ===========================================================================

func TestRebuildPhases_TS_01_31_OrderAfterPhaseTwo(t *testing.T) {
	e := newWtEnv(t, onePatch())
	e.trackState()
	e.h.Audit = &hookEmitter{fn: func(ev audit.HubEvent) { e.mark("emit:" + ev.EventType) }}

	if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, "")); err != nil {
		t.Fatalf("HandleRebuildJob: %v", err)
	}

	ui, _ := e.find(hasPrefix("trunk:UpdateRef"))
	ri, remove := e.find(hasPrefix("trunk:WorktreeRemove"))
	ei, emit := e.find(hasPrefix("emit:hub.rebuild.complete"))
	if ui < 0 || ri < 0 || ei < 0 || !(ui < ri && ri < ei) {
		t.Fatalf("expected UpdateRef < WorktreeRemove < complete event; events=%v", e.events.snapshot())
	}
	if !strings.Contains(remove, "locked=false") {
		t.Errorf("worktree removed under the lock: %s", remove)
	}
	if !strings.Contains(emit, "locked=false") || !strings.Contains(emit, "active=true") {
		t.Errorf("complete event must be emitted unlocked while the guard is set: %s", emit)
	}
	if wslock.RebuildActive(e.slug) {
		t.Error("guard must be ended on return")
	}
}

// ===========================================================================
// TS-01-33: BeginRebuild on a guarded slug fails; the rebuild is retryable
// ===========================================================================

func TestRebuildPhases_TS_01_33_GuardAlreadySet(t *testing.T) {
	e := newWtEnv(t, onePatch())
	other, ok := wslock.BeginRebuild(e.slug)
	if !ok {
		t.Fatal("BeginRebuild failed")
	}
	defer other()

	end, ok := wslock.BeginRebuild(e.slug)
	if ok {
		t.Fatal("second BeginRebuild must return ok=false")
	}
	end()
	if !wslock.RebuildActive(e.slug) {
		t.Fatal("a refused BeginRebuild must leave the guard set")
	}

	fetched := false
	e.h.Fetch = func(context.Context, string, transport.AuthMethod) error { fetched = true; return nil }
	_, retryable, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))
	var te *TransientError
	if !errors.As(err, &te) || !retryable {
		t.Fatalf("expected retryable *TransientError, got retryable=%v err=%v", retryable, err)
	}
	if fetched {
		t.Error("nothing may run for a refused rebuild")
	}
	if !wslock.RebuildActive(e.slug) {
		t.Error("the refused rebuild must not clear the other run's guard")
	}
	assertLockFree(t, e.slug)
	if len(e.trunk.WorktreeAddCalls) != 0 {
		t.Error("no worktree may be created for a refused rebuild")
	}
}

// ===========================================================================
// TS-01-34: the guard is held from before stale cleanup until after removal
// ===========================================================================

func TestRebuildPhases_TS_01_34_GuardCoversCleanupToRemoval(t *testing.T) {
	conflict := func(e *wtEnv) {
		e.wt.CherryPickFunc = func(context.Context, string) error { return &CherryPickConflictError{} }
		prev := e.wt.RunFunc
		e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "diff" {
				return "conflict.txt", nil
			}
			return prev(ctx, args...)
		}
	}
	scenarios := []struct {
		name  string
		setup func(e *wtEnv)
	}{
		{"success", func(*wtEnv) {}},
		{"patch conflict", conflict},
		{"error", func(e *wtEnv) {
			e.wt.CherryPickFunc = func(context.Context, string) error { return errors.New("boom") }
		}},
		{"update-ref failure", func(e *wtEnv) { e.trunk.UpdateRefErr = errors.New("update-ref failed") }},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			e := newWtEnv(t, onePatch())
			e.trackState()
			sc.setup(e)

			_, _, _ = e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))

			si, stale := e.find(hasPrefix("trunk:WorktreePrune"))
			ai, _ := e.find(hasPrefix("trunk:WorktreeAdd"))
			if si < 0 || ai < 0 || si > ai {
				t.Fatalf("stale cleanup prune must precede WorktreeAdd; events=%v", e.events.snapshot())
			}
			if !strings.Contains(stale, "active=true") {
				t.Errorf("guard not set at stale cleanup: %s", stale)
			}
			// Every trunk event, up to and including the prune that follows
			// the removal, runs under the guard.
			ri, _ := e.find(hasPrefix("trunk:WorktreeRemove"))
			if ri < 0 {
				t.Fatalf("WorktreeRemove not called; events=%v", e.events.snapshot())
			}
			evs := e.events.snapshot()
			if ri+1 >= len(evs) || !strings.HasPrefix(evs[ri+1], "trunk:WorktreePrune") {
				t.Fatalf("prune must follow removal; events=%v", evs)
			}
			for i := 0; i <= ri+1; i++ {
				if strings.HasPrefix(evs[i], "trunk:") && !strings.Contains(evs[i], "active=true") {
					t.Errorf("guard not set during %s", evs[i])
				}
			}
			if wslock.RebuildActive(e.slug) {
				t.Error("guard still set after return")
			}
		})
	}
}

// ===========================================================================
// TS-01-55: stale directories under rebuild/ are removed and pruned first
// ===========================================================================

func TestRebuildPhases_TS_01_55_StaleDirectoriesRemovedBeforeWorktreeAdd(t *testing.T) {
	e := newWtEnv(t, onePatch())
	e.trackState()
	d1 := filepath.Join(e.rebuildDir(), "old-job-1")
	d2 := filepath.Join(e.rebuildDir(), "old-job-2")
	for _, d := range []string{d1, d2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "leftover"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The directories must already be gone when the prune runs.
	e.trunk.OnWorktreePrune = func(context.Context) error {
		if len(e.trunk.WorktreeAddCalls) == 0 {
			for _, d := range []string{d1, d2} {
				if _, err := os.Stat(d); err == nil {
					t.Errorf("stale directory %s still exists at the prune", d)
				}
			}
		}
		return nil
	}

	if _, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, "")); err != nil {
		t.Fatalf("HandleRebuildJob: %v", err)
	}

	for _, d := range []string{d1, d2} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("stale directory %s still exists", d)
		}
		if !e.logs.has(slog.LevelInfo, "stale", e.slug, d) {
			t.Errorf("no info log naming slug and %s; records=%+v", d, e.logs.records)
		}
	}
	pi, prune := e.find(hasPrefix("trunk:WorktreePrune"))
	ai, _ := e.find(hasPrefix("trunk:WorktreeAdd"))
	if pi < 0 || ai < 0 || pi > ai {
		t.Errorf("WorktreePrune must precede WorktreeAdd; events=%v", e.events.snapshot())
	}
	if !strings.Contains(prune, "active=true") {
		t.Errorf("guard not set during stale cleanup: %s", prune)
	}
	e.assertWorktreeGone()
	if entries, _ := os.ReadDir(e.rebuildDir()); len(entries) != 0 {
		t.Errorf("rebuild dir not empty: %v", entries)
	}
}
