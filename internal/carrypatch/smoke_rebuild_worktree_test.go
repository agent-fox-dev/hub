package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/jobqueue"
	"github.com/agent-fox-dev/hub/internal/workspace"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// ===========================================================================
// Spec 01, task 11: end-to-end smoke tests for the worktree rebuild.
//
// Every test runs against real components: the rebuild and rerere and sync
// HTTP handlers, the durable job queue with running workers, the rebuild
// executor, the git CLI runner, wslock, a SQLite database and the audit
// emitter. Only the upstream network fetch is replaced (the rebuild's
// fetch is a no-op; the sync endpoint's fetch moves refs/remotes/upstream).
// ===========================================================================

const smokeOwner = "operator-1"

// smokeBlock lets a test hold a rebuild inside its first worktree cherry-pick.
type smokeBlock struct {
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *smokeBlock) open() { b.once.Do(func() { close(b.release) }) }

func (b *smokeBlock) waitBlocked(t *testing.T) {
	t.Helper()
	select {
	case <-b.blocked:
	case <-time.After(30 * time.Second):
		t.Fatal("the rebuild never reached its first patch step")
	}
}

type smokeHub struct {
	t     *testing.T
	e     *realEnv
	db    *sql.DB
	q     *jobqueue.Queue
	audit *cpAuditEmitter
	cpAPI *echo.Echo // carry-patch routes: rebuild, rerere, sync
	wsAPI *echo.Echo // workspace routes: archive, reclone

	mu    sync.Mutex
	wrap  func(path string, hr *hookRunner) GitRunner
	fetch func(ctx context.Context, repoPath string, auth transport.AuthMethod) error
}

// newSmokeHub builds the wiring around a real repository. The queue is
// created but not started.
func newSmokeHub(t *testing.T, grace time.Duration) *smokeHub {
	t.Helper()
	root := t.TempDir()
	e := newRealEnvAt(t, root, "ws-smoke")
	s := &smokeHub{t: t, e: e, audit: newCPAuditEmitter()}

	s.db = openTestDB(t)
	createWorkspacesTable(t, s.db)
	createPatchesTable(t, s.db)
	// The production SQLPatchStore reads these patch columns.
	addUpstreamPRURLColumn(t, s.db)
	addDeletedAtColumn(t, s.db)
	addWorkspaceColumns(t, s.db)
	if err := jobqueue.InitSchema(s.db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(s.db); err != nil {
		t.Fatalf("MigrateGroupKey: %v", err)
	}
	if err := jobqueue.MigrateProgress(s.db); err != nil {
		t.Fatalf("MigrateProgress: %v", err)
	}
	q, err := jobqueue.New(s.db, nopLogger(),
		jobqueue.WithWorkers(2),
		jobqueue.WithPollInterval(20*time.Millisecond),
		jobqueue.WithGracePeriod(grace))
	if err != nil {
		t.Fatalf("jobqueue.New: %v", err)
	}
	s.q = q

	seedWorkspaceCarryPatch(t, s.db, e.slug, smokeOwner, "https://example.invalid/upstream",
		e.baseSHA, "integration", "")
	seedPatch(t, s.db, "p1", e.slug, "feature/patch-a", 1, PatchStatusActive)
	seedPatch(t, s.db, "p2", e.slug, "feature/patch-b", 2, PatchStatusActive)

	getVar := func(_, _, _ string) (string, error) { return "", nil }

	e.h.DB = s.db
	e.h.Queue = q
	e.h.Audit = s.audit
	e.h.GetVariable = getVar
	e.h.PatchStore = NewSQLPatchStore(s.db)
	factory := NewGitRunnerFactory()
	e.h.NewGitRunner = func(path string) (GitRunner, error) {
		inner, err := factory(path)
		if err != nil {
			return nil, err
		}
		hr := &hookRunner{GitRunner: inner}
		if path == e.trunk {
			return hr, nil
		}
		s.mu.Lock()
		wrap := s.wrap
		s.mu.Unlock()
		if wrap != nil {
			return wrap(path, hr), nil
		}
		return hr, nil
	}
	if err := RegisterRebuildJob(q, e.h); err != nil {
		t.Fatalf("RegisterRebuildJob: %v", err)
	}

	s.cpAPI = echo.New()
	api := s.cpAPI.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())
	RegisterRebuildRoutes(api, RebuildAPIConfig{DB: s.db, Queue: q, GetVariable: getVar, Audit: s.audit})
	RegisterRerereRoutes(api, RerereAPIConfig{DB: s.db, WorkspaceRoot: root, NewGitRunner: factory})
	RegisterSyncRoutes(api, SyncAPIConfig{
		DB: s.db, Queue: q, WorkspaceRoot: root, NewGitRunner: factory,
		Fetch: func(ctx context.Context, repoPath string, auth transport.AuthMethod) error {
			s.mu.Lock()
			f := s.fetch
			s.mu.Unlock()
			if f == nil {
				return nil
			}
			return f(ctx, repoPath, auth)
		},
		ResolveAuth: func(string) (transport.AuthMethod, error) { return nil, nil },
		GetVariable: getVar,
		PatchStore:  NewSQLPatchStore(s.db),
	})

	s.wsAPI = echo.New()
	wsGroup := s.wsAPI.Group("/api/v1")
	wsGroup.Use(rebuildTestAuthMiddleware())
	if err := workspace.RegisterRoutes(wsGroup, s.db); err != nil {
		t.Fatalf("workspace.RegisterRoutes: %v", err)
	}
	return s
}

// start launches the queue workers; the queue is stopped at test end.
func (s *smokeHub) start() {
	s.t.Helper()
	if err := s.q.Start(); err != nil {
		s.t.Fatalf("queue Start: %v", err)
	}
	s.t.Cleanup(func() { _ = s.q.Stop() })
}

func (s *smokeHub) setWrap(w func(path string, hr *hookRunner) GitRunner) {
	s.mu.Lock()
	s.wrap = w
	s.mu.Unlock()
}

// blockFirstCherryPick holds the first worktree cherry-pick of the run until
// the returned block is opened. The block is opened at test end as well.
func (s *smokeHub) blockFirstCherryPick() *smokeBlock {
	b := &smokeBlock{blocked: make(chan struct{}), release: make(chan struct{})}
	s.t.Cleanup(b.open)
	var once sync.Once
	s.setWrap(func(_ string, hr *hookRunner) GitRunner {
		hr.onCherryPick = func(context.Context, string) error {
			once.Do(func() {
				close(b.blocked)
				<-b.release
			})
			return nil
		}
		return hr
	})
	return b
}

func (s *smokeHub) do(api *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	s.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	authJSON, err := json.Marshal(rebuildUserAuth(smokeOwner))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("X-Test-Auth", string(authJSON))
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	return rec
}

func (s *smokeHub) wsPath(suffix string) string {
	return "/api/v1/workspaces/" + s.e.slug + suffix
}

// submitRebuild enqueues a rebuild through the rebuild API and returns the job id.
func (s *smokeHub) submitRebuild(body string) string {
	s.t.Helper()
	rec := s.do(s.cpAPI, http.MethodPost, s.wsPath("/rebuild"), body)
	if rec.Code != http.StatusAccepted {
		s.t.Fatalf("POST /rebuild = %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	var resp RebuildJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		s.t.Fatalf("decode rebuild response: %v", err)
	}
	return resp.ID
}

// waitStatus polls the job until it reaches one of the statuses.
func (s *smokeHub) waitStatus(jobID string, want ...string) *jobqueue.Job {
	s.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		j, err := s.q.GetByID(jobID)
		if err != nil {
			s.t.Fatalf("GetByID(%s): %v", jobID, err)
		}
		for _, w := range want {
			if j.Status == w {
				return j
			}
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("job %s is %q (error %q), want one of %v", jobID, j.Status, j.Error, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *smokeHub) resultOf(j *jobqueue.Job) RebuildResult {
	s.t.Helper()
	var r RebuildResult
	if err := json.Unmarshal(j.Result, &r); err != nil {
		s.t.Fatalf("decode job result %q: %v", string(j.Result), err)
	}
	return r
}

func (s *smokeHub) events(eventType string) []audit.HubEvent {
	var out []audit.HubEvent
	for _, ev := range s.audit.Events() {
		if ev.EventType == eventType {
			out = append(out, ev)
		}
	}
	return out
}

type smokeFollowup struct{ ID, Status, SubmittedBy, GroupKey string }

func (s *smokeHub) followUps() []smokeFollowup {
	s.t.Helper()
	rows, err := s.db.Query(`SELECT id, status, submitted_by, group_key FROM jobs
		WHERE type = 'rebuild' AND submitted_by = ?`, followupSubmitter)
	if err != nil {
		s.t.Fatalf("query follow-ups: %v", err)
	}
	defer rows.Close()
	var out []smokeFollowup
	for rows.Next() {
		var f smokeFollowup
		if err := rows.Scan(&f.ID, &f.Status, &f.SubmittedBy, &f.GroupKey); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

// assertNoWorktrees checks that no rebuild directory or registration is left.
func (s *smokeHub) assertNoWorktrees(label string) {
	s.t.Helper()
	if entries, err := os.ReadDir(filepath.Join(s.e.root, s.e.slug, "rebuild")); err == nil && len(entries) > 0 {
		s.t.Errorf("%s: rebuild directory still holds %d entries", label, len(entries))
	}
	if n := s.e.worktreeCount(); n != 1 {
		s.t.Errorf("%s: git worktree list shows %d entries, want only the trunk:\n%s", label, n,
			runGitCmd(s.t, s.e.trunk, "worktree", "list", "--porcelain"))
	}
	if wslock.RebuildActive(s.e.slug) {
		s.t.Errorf("%s: the rebuild-active guard is still set", label)
	}
	assertLockFree(s.t, s.e.slug)
}

// advanceBranch adds an empty-diff commit on top of a local branch, as a push
// to that branch would.
func (s *smokeHub) advanceBranch(branch string) string {
	s.t.Helper()
	tree := runGitCmd(s.t, s.e.trunk, "rev-parse", "refs/heads/"+branch+"^{tree}")
	sha := runGitCmd(s.t, s.e.trunk, "commit-tree", tree, "-p", "refs/heads/"+branch, "-m", "pushed fix")
	runGitCmd(s.t, s.e.trunk, "update-ref", "refs/heads/"+branch, sha)
	return sha
}

// ===========================================================================
// TS-01-73 (smoke): a rebuild succeeds through the queue without touching the
// trunk and reports source_sha. Verifies: 01-PATH-1
// ===========================================================================

func TestSmoke_TS_01_73_RebuildSucceedsWithoutTouchingTrunk(t *testing.T) {
	s := newSmokeHub(t, 30*time.Second)
	s.start()
	e := s.e
	tipA := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/feature/patch-a")
	tipB := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/feature/patch-b")
	before := e.trunkSnapshot()
	if !strings.HasSuffix(before[0], "refs/heads/main") {
		t.Fatalf("setup: trunk is on %q, want main (not the integration branch)", before[0])
	}

	jobID := s.submitRebuild("")
	job := s.waitStatus(jobID, jobqueue.StatusCompleted, jobqueue.StatusDeadLetter, jobqueue.StatusFailed)
	if job.Status != jobqueue.StatusCompleted {
		t.Fatalf("rebuild job %s: %s", job.Status, job.Error)
	}

	// The integration branch holds both patches.
	for _, f := range []string{"patch-a.txt", "patch-b.txt"} {
		if _, err := gitTry(e.trunk, "cat-file", "-e", "refs/heads/integration:"+f); err != nil {
			t.Errorf("integration lacks %s: %v", f, err)
		}
	}
	res := s.resultOf(job)
	if got := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/integration"); got != res.IntegrationHeadSHA {
		t.Errorf("integration = %s, result says %s", got, res.IntegrationHeadSHA)
	}
	if len(res.PatchResults) != 2 {
		t.Fatalf("patch results = %+v", res.PatchResults)
	}
	for i, want := range []string{tipA, tipB} {
		if pr := res.PatchResults[i]; pr.Status != "success" || pr.SourceSHA != want {
			t.Errorf("patch[%d] = %s/%s source_sha=%s, want success at %s", i, pr.BranchName, pr.Status, pr.SourceSHA, want)
		}
	}

	// The API response carries source_sha.
	rec := s.do(s.cpAPI, http.MethodGet, s.wsPath("/rebuilds/"+jobID), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rebuilds/:id = %d: %s", rec.Code, rec.Body.String())
	}
	var rb struct {
		PatchResults []map[string]any `json:"patch_results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rb); err != nil {
		t.Fatal(err)
	}
	if len(rb.PatchResults) != 2 || rb.PatchResults[0]["source_sha"] != tipA || rb.PatchResults[1]["source_sha"] != tipB {
		t.Errorf("API patch_results = %v, want source_sha %s and %s", rb.PatchResults, tipA, tipB)
	}

	// hub.rebuild.complete was emitted; no failure, no follow-up.
	if n := len(s.events("hub.rebuild.complete")); n != 1 {
		t.Errorf("hub.rebuild.complete emitted %d times, want 1", n)
	}
	if n := len(s.events("hub.rebuild.fail")); n != 0 {
		t.Errorf("hub.rebuild.fail emitted %d times, want 0", n)
	}
	if f := s.followUps(); len(f) != 0 {
		t.Errorf("unexpected follow-ups: %+v", f)
	}

	// The trunk is untouched; the worktree and the guard are gone.
	if after := e.trunkSnapshot(); after != before {
		t.Errorf("trunk changed by the rebuild:\nbefore=%q\nafter =%q", before, after)
	}
	s.assertNoWorktrees("after a successful rebuild")
}

// ===========================================================================
// TS-01-74 (smoke): a push to a patch branch during application yields a
// queued follow-up and a followup audit event. Verifies: 01-PATH-2, 01-REQ-7.5
// ===========================================================================

func TestSmoke_TS_01_74_PushDuringApplicationQueuesFollowup(t *testing.T) {
	s := newSmokeHub(t, 30*time.Second)
	e := s.e
	hub := newHubServer(t, e)
	client := filepath.Join(t.TempDir(), "client")
	runGitCmd(t, "", "clone", hub.url, client)
	configGitUserCmd(t, client)

	s.start()
	blk := s.blockFirstCherryPick()
	tipBefore := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/feature/patch-a")

	jobID := s.submitRebuild("")
	blk.waitBlocked(t)
	if !wslock.RebuildActive(e.slug) {
		t.Fatal("the guard is not set while the rebuild applies patches")
	}
	assertLockFree(t, e.slug)

	// A developer pushes to the checked-out branch and to the patch branch.
	writeFileHelper(t, filepath.Join(client, "pushed-main.txt"), "pushed to main")
	runGitCmd(t, client, "add", ".")
	runGitCmd(t, client, "commit", "-m", "push to main")
	pushedMain := runGitCmd(t, client, "rev-parse", "HEAD")
	runGitCmd(t, client, "checkout", "-b", "feature/patch-a", "origin/feature/patch-a")
	writeFileHelper(t, filepath.Join(client, "patch-a-more.txt"), "more patch a")
	runGitCmd(t, client, "add", ".")
	runGitCmd(t, client, "commit", "-m", "push to patch-a")
	pushedPatch := runGitCmd(t, client, "rev-parse", "HEAD")
	runGitCmd(t, client, "push", "origin", "main", "feature/patch-a")

	// Stop the workers from claiming further jobs (they finish the running
	// one), so the follow-up stays observable in its queued state.
	s.q.Shutdown()
	blk.open()
	if err := s.q.Wait(); err != nil {
		t.Fatalf("queue Wait: %v", err)
	}
	job := s.waitStatus(jobID, jobqueue.StatusCompleted, jobqueue.StatusDeadLetter, jobqueue.StatusFailed)
	if job.Status != jobqueue.StatusCompleted {
		t.Fatalf("rebuild job %s: %s", job.Status, job.Error)
	}

	// The run applied the snapshot, not the pushed commit.
	res := s.resultOf(job)
	if len(res.PatchResults) < 1 || res.PatchResults[0].SourceSHA != tipBefore {
		t.Errorf("patch results = %+v, want source_sha %s", res.PatchResults, tipBefore)
	}
	if _, err := gitTry(e.trunk, "cat-file", "-e", "refs/heads/integration:patch-a-more.txt"); err == nil {
		t.Error("integration holds the commit pushed during the run; it must hold the snapshot only")
	}
	if got := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/feature/patch-a"); got != pushedPatch {
		t.Errorf("patch branch = %s, want the pushed %s", got, pushedPatch)
	}

	// The push reset the trunk working tree (the lock was free).
	if got := runGitCmd(t, e.trunk, "rev-parse", "HEAD"); got != pushedMain {
		t.Errorf("trunk HEAD = %s, want the pushed commit %s", got, pushedMain)
	}
	if st := runGitCmd(t, e.trunk, "status", "--porcelain"); st != "" {
		t.Errorf("trunk working tree not clean: %q", st)
	}

	// One queued follow-up, and the audit event.
	fu := s.followUps()
	if len(fu) != 1 {
		t.Fatalf("follow-ups = %+v, want exactly one", fu)
	}
	if fu[0].Status != jobqueue.StatusQueued || fu[0].SubmittedBy != "system:stale-snapshot" {
		t.Errorf("follow-up = %+v, want queued by system:stale-snapshot", fu[0])
	}
	if orig, err := s.q.GetByID(jobID); err == nil && fu[0].GroupKey != orig.GroupKey {
		t.Errorf("follow-up group key %q differs from the original %q", fu[0].GroupKey, orig.GroupKey)
	}
	evs := s.events(audit.EventRebuildFollowup)
	if len(evs) != 1 {
		t.Fatalf("hub.rebuild.followup emitted %d times, want 1", len(evs))
	}
	stale, _ := evs[0].Metadata["stale_patches"].([]string)
	if len(stale) != 1 || stale[0] != "feature/patch-a" {
		t.Errorf("stale_patches = %v, want [feature/patch-a]", evs[0].Metadata["stale_patches"])
	}
	if evs[0].Metadata["follow_up_job_id"] != fu[0].ID {
		t.Errorf("follow_up_job_id = %v, want %s", evs[0].Metadata["follow_up_job_id"], fu[0].ID)
	}
	s.assertNoWorktrees("after the run")
}

// ===========================================================================
// TS-01-75 (smoke): a cancelled rebuild cleans up, returns a retryable error
// and is retried. Verifies: 01-PATH-3
// ===========================================================================

func TestSmoke_TS_01_75_CancelledRebuildCleansUpAndIsRetried(t *testing.T) {
	s := newSmokeHub(t, 300*time.Millisecond)
	s.start()
	e := s.e
	blocked := make(chan struct{})
	var once sync.Once
	s.setWrap(func(_ string, hr *hookRunner) GitRunner {
		// Wait for the job context to die, then let the real runner see it.
		hr.onCherryPick = func(ctx context.Context, _ string) error {
			once.Do(func() { close(blocked) })
			<-ctx.Done()
			return nil
		}
		return hr
	})
	before := e.trunkSnapshot()
	integrationBefore := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/integration")

	jobID := s.submitRebuild("")
	select {
	case <-blocked:
	case <-time.After(30 * time.Second):
		t.Fatal("the rebuild never reached its first patch step")
	}
	if got := e.worktreeCount(); got != 2 {
		t.Fatalf("setup: %d worktrees registered while applying, want trunk + rebuild", got)
	}

	// Hub shutdown: the grace period expires and the job context is cancelled.
	s.q.Shutdown()
	if err := s.q.Wait(); err != nil {
		t.Fatalf("queue Wait: %v", err)
	}

	// A TransientError was returned: the queue scheduled a retry.
	job := s.waitStatus(jobID, jobqueue.StatusFailed, jobqueue.StatusDeadLetter, jobqueue.StatusCompleted)
	if job.Status != jobqueue.StatusFailed {
		t.Fatalf("job status = %s (error %q), want failed with a retry scheduled", job.Status, job.Error)
	}
	if job.RetryCount != 1 {
		t.Errorf("retry_count = %d, want 1", job.RetryCount)
	}
	if !job.AvailableAt.After(time.Now().Add(-time.Second)) {
		t.Errorf("available_at = %v, want a future retry time", job.AvailableAt)
	}
	if job.Error == "" {
		t.Error("the failed job records no error")
	}

	// Worktree, registration and guard are gone; the trunk is unchanged.
	s.assertNoWorktrees("after cancellation")
	if after := e.trunkSnapshot(); after != before {
		t.Errorf("trunk changed by the cancelled rebuild:\nbefore=%q\nafter =%q", before, after)
	}
	if got := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/integration"); got != integrationBefore {
		t.Errorf("integration moved from %s to %s", integrationBefore, got)
	}
	if n := len(s.events("hub.rebuild.complete")); n != 0 {
		t.Errorf("hub.rebuild.complete emitted %d times for a cancelled run", n)
	}
	if n := len(s.events("hub.rebuild.fail")); n != 1 {
		t.Errorf("hub.rebuild.fail emitted %d times, want 1", n)
	}
}

// ===========================================================================
// TS-01-76 (smoke): archive and reclone are refused with 409 while sync
// proceeds during a rebuild, and the moved upstream triggers a follow-up.
// Verifies: 01-PATH-4, 01-REQ-9.2
// ===========================================================================

func TestSmoke_TS_01_76_ArchiveRecloneRefusedWhileSyncProceeds(t *testing.T) {
	s := newSmokeHub(t, 30*time.Second)
	e := s.e
	s.start()
	blk := s.blockFirstCherryPick()

	var movedTo string
	s.mu.Lock()
	s.fetch = func(context.Context, string, transport.AuthMethod) error {
		old := runGitCmd(t, e.trunk, "rev-parse", "refs/remotes/upstream/HEAD")
		tree := runGitCmd(t, e.trunk, "rev-parse", "refs/remotes/upstream/HEAD^{tree}")
		movedTo = runGitCmd(t, e.trunk, "commit-tree", tree, "-p", old, "-m", "upstream moves")
		runGitCmd(t, e.trunk, "update-ref", "refs/remotes/upstream/HEAD", movedTo)
		return nil
	}
	s.mu.Unlock()

	jobID := s.submitRebuild("")
	blk.waitBlocked(t)
	baseUsed := e.baseSHA

	// POST /sync proceeds: it takes the free lock, fetches and records the
	// new upstream head. Its own rebuild enqueue is deduplicated against the
	// running rebuild.
	rec := s.do(s.cpAPI, http.MethodPost, s.wsPath("/sync"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync during a rebuild = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var syncResp CarryPatchSyncResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &syncResp); err != nil {
		t.Fatal(err)
	}
	if syncResp.RebuildTriggered {
		t.Error("sync enqueued a second rebuild although one is running")
	}
	var stored string
	if err := s.db.QueryRow(`SELECT upstream_head_sha FROM workspaces WHERE slug = ?`, e.slug).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if movedTo == "" || stored != movedTo || stored == baseUsed {
		t.Errorf("upstream_head_sha = %q, want the moved upstream %q", stored, movedTo)
	}

	// Archive and reclone answer 409 workspace_busy while the lock is free.
	assertLockFree(t, e.slug)
	for _, tc := range []struct{ name, path string }{
		{"archive", s.wsPath("/archive")},
		{"reclone", s.wsPath("/reclone")},
	} {
		rec := s.do(s.wsAPI, http.MethodPost, tc.path, "")
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s during a rebuild = %d, want 409; body: %s", tc.name, rec.Code, rec.Body.String())
		}
		var body struct {
			Error struct {
				ErrorType string `json:"error_type"`
			} `json:"error"`
			ErrorType string `json:"error_type"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.ErrorType != "workspace_busy" && body.ErrorType != "workspace_busy" {
			t.Errorf("%s error_type = %+v, want workspace_busy; body: %s", tc.name, body, rec.Body.String())
		}
		if _, err := os.Stat(filepath.Join(e.root, e.slug, "trunk", ".git")); err != nil {
			t.Fatalf("%s removed the trunk: %v", tc.name, err)
		}
		assertLockFree(t, e.slug)
	}
	if !wslock.RebuildActive(e.slug) {
		t.Error("the guard was cleared by a refused archive or reclone")
	}

	// Finish the run; keep the follow-up observable in its queued state.
	s.q.Shutdown()
	blk.open()
	if err := s.q.Wait(); err != nil {
		t.Fatalf("queue Wait: %v", err)
	}
	job := s.waitStatus(jobID, jobqueue.StatusCompleted, jobqueue.StatusDeadLetter, jobqueue.StatusFailed)
	if job.Status != jobqueue.StatusCompleted {
		t.Fatalf("rebuild job %s: %s", job.Status, job.Error)
	}
	fu := s.followUps()
	if len(fu) != 1 || fu[0].Status != jobqueue.StatusQueued {
		t.Fatalf("follow-ups = %+v, want one queued (upstream moved)", fu)
	}
	evs := s.events(audit.EventRebuildFollowup)
	if len(evs) != 1 || evs[0].Metadata["upstream_stale"] != true {
		t.Errorf("followup events = %+v, want one with upstream_stale=true", evs)
	}
	s.assertNoWorktrees("after the run")
}

// ===========================================================================
// TS-01-77 (smoke): crash leftovers are removed at startup and before the next
// rebuild. Verifies: 01-PATH-5
// ===========================================================================

func TestSmoke_TS_01_77_CrashLeftoversRemovedAtStartupAndBeforeRebuild(t *testing.T) {
	s := newSmokeHub(t, 30*time.Second)
	e := s.e
	rebuildDir := filepath.Join(e.root, e.slug, "rebuild")

	// A crash left a registered worktree directory, and another workspace
	// that has a rebuild directory but no trunk.
	orphan := filepath.Join(rebuildDir, "crashed-job")
	runGitCmd(t, e.trunk, "worktree", "add", "--detach", orphan, e.baseSHA)
	if e.worktreeCount() != 2 {
		t.Fatal("setup: the orphan worktree is not registered")
	}
	noTrunk := filepath.Join(e.root, "ws-no-trunk", "rebuild", "x")
	if err := os.MkdirAll(noTrunk, 0o755); err != nil {
		t.Fatal(err)
	}

	// Startup cleanup removes the directory and prunes the registration.
	CleanupStaleRebuildWorktrees(context.Background(), e.root, nopLogger())
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("startup cleanup left %s (err=%v)", orphan, err)
	}
	if _, err := os.Stat(noTrunk); !os.IsNotExist(err) {
		t.Errorf("startup cleanup left the trunk-less workspace's directory (err=%v)", err)
	}
	if n := e.worktreeCount(); n != 1 {
		t.Fatalf("after startup cleanup: %d worktrees registered, want only the trunk", n)
	}

	// More leftovers appear: a plain directory and another registered orphan.
	plain := filepath.Join(rebuildDir, "plain-stale")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileHelper(t, filepath.Join(plain, "leftover.txt"), "stale")
	orphan2 := filepath.Join(rebuildDir, "crashed-job-2")
	runGitCmd(t, e.trunk, "worktree", "add", "--detach", orphan2, e.baseSHA)

	// The next rebuild, through the queue, removes them before creating its
	// own worktree and succeeds.
	s.start()
	jobID := s.submitRebuild("")
	job := s.waitStatus(jobID, jobqueue.StatusCompleted, jobqueue.StatusDeadLetter, jobqueue.StatusFailed)
	if job.Status != jobqueue.StatusCompleted {
		t.Fatalf("rebuild job %s: %s", job.Status, job.Error)
	}
	for _, p := range []string{plain, orphan2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("the rebuild left %s (err=%v)", p, err)
		}
	}
	s.assertNoWorktrees("after the rebuild")
}

// The startup cleanup must be live in the hub binary: main.go calls it before
// the queue starts, so no rebuild can be using the directories it removes.
// Also confirms the production consumers of the other contract results.
func TestSmoke_TS_01_77_ProductionWiring(t *testing.T) {
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}

	main := read("cmd/af-hub/main.go")
	cleanup := strings.Index(main, "carrypatch.CleanupStaleRebuildWorktrees(")
	start := strings.Index(main, "mergeQueue.Start()")
	if cleanup < 0 || start < 0 || cleanup > start {
		t.Errorf("main.go must call CleanupStaleRebuildWorktrees before mergeQueue.Start() (cleanup at %d, start at %d)", cleanup, start)
	}
	if !strings.Contains(main, "carrypatch.RegisterRebuildJob(mergeQueue, rebuildHandler)") {
		t.Error("main.go does not register the rebuild job")
	}
	if !strings.Contains(main, "rebuildHandler.Audit = auditEmitter") {
		t.Error("main.go does not give the rebuild handler an audit emitter")
	}

	exec := read("internal/carrypatch/rebuild_executor.go")
	for _, want := range []string{"wslock.BeginRebuild(", "ExcludeJobID:", "SourceSHA", "UpdateRef(", "WorktreeAdd(", "WorktreeRemove("} {
		if !strings.Contains(exec, want) {
			t.Errorf("rebuild_executor.go does not use %s", want)
		}
	}
	for _, rel := range []string{"internal/workspace/handlers.go", "internal/workspace/reclone_handler.go"} {
		if !strings.Contains(read(rel), "wslock.RebuildActive(") {
			t.Errorf("%s does not consult wslock.RebuildActive", rel)
		}
	}
	// Sync, rollback and rerere forget are not rejected by the guard.
	for _, rel := range []string{
		"internal/carrypatch/sync_handlers.go", "internal/carrypatch/api.go",
		"internal/carrypatch/rerere_handlers.go", "internal/merge/api.go",
	} {
		if strings.Contains(read(rel), "RebuildActive(") {
			t.Errorf("%s consults the rebuild guard; only archive and reclone may", rel)
		}
	}
}

// ===========================================================================
// TS-01-78 (smoke): a recorded rerere resolution replays in the next rebuild
// and the endpoints list and forget it. Verifies: 01-PATH-6
// ===========================================================================

func TestSmoke_TS_01_78_RerereResolutionReplaysAndEndpointsListForget(t *testing.T) {
	s := newSmokeHub(t, 30*time.Second)
	e := s.e
	if _, err := s.db.Exec(`UPDATE patches SET branch_name = 'feature/conflict' WHERE id = 'p2'`); err != nil {
		t.Fatal(err)
	}
	s.start()

	// The first rebuild: the operator resolves the conflict in the rebuild's
	// worktree and rerere records the resolution in the shared rr-cache.
	s.setWrap(func(path string, hr *hookRunner) GitRunner {
		return &conflictHook{GitRunner: hr, path: path, after: e.resolveAndRecord}
	})
	first := s.waitStatus(s.submitRebuild(""), jobqueue.StatusCompleted, jobqueue.StatusDeadLetter, jobqueue.StatusFailed)
	if first.Status != jobqueue.StatusCompleted {
		t.Fatalf("recording rebuild %s: %s", first.Status, first.Error)
	}
	recorded := resolvedEntries(e.trunk, rrCacheIDs(t, e.trunk))
	if len(recorded) != 1 {
		t.Fatalf("want exactly one recorded resolution in the shared rr-cache, got %v", recorded)
	}
	id := recorded[0]
	s.assertNoWorktrees("after the recording rebuild")

	// The second rebuild gets no help and must replay it.
	s.setWrap(nil)
	second := s.waitStatus(s.submitRebuild(""), jobqueue.StatusCompleted, jobqueue.StatusDeadLetter, jobqueue.StatusFailed)
	if second.Status != jobqueue.StatusCompleted {
		t.Fatalf("second rebuild %s: %s", second.Status, second.Error)
	}
	res := s.resultOf(second)
	if len(res.PatchResults) != 2 || res.PatchResults[1].Status != "success" {
		t.Fatalf("second rebuild reports a conflict: %+v", res.PatchResults)
	}
	if got := runGitCmd(t, e.trunk, "show", "integration:base.txt"); got != "resolved base.txt" {
		t.Errorf("integration base.txt = %q, want the replayed resolution", got)
	}
	s.assertNoWorktrees("after the replaying rebuild")

	// GET lists the entry; DELETE forgets it.
	list := func() []RerereResolution {
		rec := s.do(s.cpAPI, http.MethodGet, s.wsPath("/rerere"), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /rerere = %d: %s", rec.Code, rec.Body.String())
		}
		var resp RerereListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Resolutions
	}
	found := false
	for _, r := range list() {
		if r.ID == id {
			found = true
			if !r.Resolved {
				t.Errorf("entry %s is listed as unresolved", id)
			}
		}
	}
	if !found {
		t.Fatalf("GET /rerere does not list %s: %+v", id, list())
	}
	rec := s.do(s.cpAPI, http.MethodDelete, s.wsPath("/rerere/"+id), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /rerere/%s = %d, want 204: %s", id, rec.Code, rec.Body.String())
	}
	for _, r := range list() {
		if r.ID == id {
			t.Errorf("GET /rerere still lists %s after DELETE", id)
		}
	}
}

// ===========================================================================
// TS-01-79 (smoke): a fail-fast conflict leaves the trunk untouched after a
// legacy _rebuild_temp migration. Verifies: 01-PATH-7, 01-REQ-2.7
// ===========================================================================

func TestSmoke_TS_01_79_FailFastConflictAfterLegacyMigration(t *testing.T) {
	s := newSmokeHub(t, 30*time.Second)
	e := s.e
	if _, err := s.db.Exec(`UPDATE patches SET branch_name = 'feature/conflict' WHERE id = 'p2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE workspaces SET branch = 'main' WHERE slug = ?`, e.slug); err != nil {
		t.Fatal(err)
	}

	// A pre-upgrade run left the trunk on _rebuild_temp, mid cherry-pick.
	runGitCmd(t, e.trunk, "checkout", "-b", "_rebuild_temp")
	if _, err := gitTry(e.trunk, "cherry-pick", "feature/conflict"); err == nil {
		t.Fatal("setup: the cherry-pick was expected to conflict")
	}
	if _, err := gitTry(e.trunk, "rev-parse", "--verify", "-q", "CHERRY_PICK_HEAD"); err != nil {
		t.Fatal("setup: no cherry-pick in progress")
	}
	integrationBefore := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/integration")
	s.start()

	// The worktree runner is built after phase one, i.e. after the migration:
	// record the trunk there. During the run a developer pushes a fix to the
	// conflicting patch branch; the queue is told to claim nothing further so
	// the follow-up stays observable.
	var afterMigration [4]string
	var once sync.Once
	s.setWrap(func(_ string, hr *hookRunner) GitRunner {
		afterMigration = e.trunkSnapshot()
		hr.onCherryPick = func(context.Context, string) error {
			once.Do(func() {
				s.q.Shutdown()
				s.advanceBranch("feature/conflict")
			})
			return nil
		}
		return hr
	})

	jobID := s.submitRebuild(`{"fail_mode":"fail_fast"}`)
	job := s.waitStatus(jobID, jobqueue.StatusDeadLetter, jobqueue.StatusFailed, jobqueue.StatusCompleted)
	if job.Status != jobqueue.StatusDeadLetter || !strings.Contains(strings.ToLower(job.Error), "conflict") {
		t.Fatalf("job = %s (error %q), want a fail-fast conflict failure", job.Status, job.Error)
	}

	// The migration moved the trunk to the workspace branch and removed the
	// legacy branch and the half-applied pick.
	if afterMigration[0] != "refs/heads/main" {
		t.Errorf("after migration the trunk is on %q, want refs/heads/main", afterMigration[0])
	}
	if out, err := gitTry(e.trunk, "show-ref", "refs/heads/_rebuild_temp"); err == nil {
		t.Errorf("_rebuild_temp still exists: %s", out)
	}
	if _, err := gitTry(e.trunk, "rev-parse", "--verify", "-q", "CHERRY_PICK_HEAD"); err == nil {
		t.Error("the half-applied cherry-pick was not aborted")
	}

	// The run stopped with the integration branch unchanged, and the trunk is
	// exactly as the migration left it.
	if got := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/integration"); got != integrationBefore {
		t.Errorf("integration moved from %s to %s on a fail-fast conflict", integrationBefore, got)
	}
	if after := e.trunkSnapshot(); after != afterMigration {
		t.Errorf("trunk changed by the rebuild after the migration:\nbefore=%q\nafter =%q", afterMigration, after)
	}
	s.assertNoWorktrees("after the conflict")

	// The stale-input check ran on the conflict exit: the pushed fix produced
	// a follow-up.
	fu := s.followUps()
	if len(fu) != 1 || fu[0].Status != jobqueue.StatusQueued {
		t.Fatalf("follow-ups = %+v, want one queued", fu)
	}
	evs := s.events(audit.EventRebuildFollowup)
	if len(evs) != 1 {
		t.Fatalf("hub.rebuild.followup emitted %d times, want 1", len(evs))
	}
	stale, _ := evs[0].Metadata["stale_patches"].([]string)
	if len(stale) != 1 || stale[0] != "feature/conflict" {
		t.Errorf("stale_patches = %v, want [feature/conflict]", evs[0].Metadata["stale_patches"])
	}
	if n := len(s.events("hub.rebuild.fail")); n != 1 {
		t.Errorf("hub.rebuild.fail emitted %d times, want 1", n)
	}
}
