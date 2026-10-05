package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/carrypatch"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// ===========================================================================
// Shared test environment for batch / skip / concurrency / race / audit tests
// ===========================================================================

// batchEnv holds a test HTTP server with real git repos, the carrypatch
// resolver, audit capture, and counting stubs.
type batchEnv struct {
	echo          *echo.Echo
	db            *sql.DB
	workspaceRoot string
	forkDir       string
	trunkDir      string
	initialSHA    string
	slug          string
	emitter       *mockAuditEmitter
	logBuf        *bytes.Buffer

	// Mutable mode — tests can change this between requests.
	mu          sync.Mutex
	mode        string
	fetchCalls  int
	credCalls   int
	getVarCalls int

	// flipModeAfterRead, when non-empty, switches mode to this value as soon
	// as PATCH_BRANCH_SOURCE has been read once. It simulates an operator
	// changing the variable while a request is in flight.
	flipModeAfterRead string

	// wrapRunner, when set, wraps every GitRunner the resolver opens. It lets
	// a test inject git failures (for example a failing update-ref).
	wrapRunner func(carrypatch.GitRunner) carrypatch.GitRunner
}

func newBatchEnv(t *testing.T, slug, mode string) *batchEnv {
	t.Helper()

	workspaceRoot := t.TempDir()
	forkDir, trunkDir, initialSHA := setupHTTPForkAndTrunk(t, workspaceRoot, slug)

	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)
	seedCarryPatchWorkspaceRaw(t, db, slug,
		forkDir,
		"https://github.com/upstream/repo.git",
		"deploy")

	env := &batchEnv{
		db:            db,
		workspaceRoot: workspaceRoot,
		forkDir:       forkDir,
		trunkDir:      trunkDir,
		initialSHA:    initialSHA,
		slug:          slug,
		mode:          mode,
	}

	// Capture log output.
	env.logBuf = &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(env.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	// Audit emitter.
	emitter := newMockAuditEmitter()
	env.emitter = emitter

	// Counting stubs.
	realFetch := carrypatch.DefaultSingleBranchFetch()
	fetchFunc := func(ctx context.Context, repoPath, branch string, auth transport.AuthMethod) error {
		env.mu.Lock()
		env.fetchCalls++
		env.mu.Unlock()
		return realFetch(ctx, repoPath, branch, auth)
	}

	credResolver := func(s string) (transport.AuthMethod, error) {
		env.mu.Lock()
		env.credCalls++
		env.mu.Unlock()
		return nil, nil
	}

	getVar := func(scope, s, key string) (string, error) {
		env.mu.Lock()
		env.getVarCalls++
		m := env.mode
		if key == "PATCH_BRANCH_SOURCE" && env.flipModeAfterRead != "" {
			env.mode = env.flipModeAfterRead
		}
		env.mu.Unlock()
		if key == "PATCH_BRANCH_SOURCE" {
			return m, nil
		}
		return "", fmt.Errorf("not found")
	}

	// Build the echo server with audit.
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())

	cfg := HandlerConfig{
		DB:    db,
		Audit: emitter,
	}
	if err := RegisterRoutesWithConfig(api, cfg); err != nil {
		t.Fatalf("RegisterRoutesWithConfig: %v", err)
	}

	// Runner factory: the real one, optionally wrapped by the test.
	realRunners := carrypatch.NewGitRunnerFactory()
	runnerFactory := func(repoPath string) (carrypatch.GitRunner, error) {
		r, err := realRunners(repoPath)
		if err != nil {
			return nil, err
		}
		env.mu.Lock()
		wrap := env.wrapRunner
		env.mu.Unlock()
		if wrap != nil {
			return wrap(r), nil
		}
		return r, nil
	}

	// Register the carrypatch resolver hook.
	hook := carrypatch.NewBranchResolverHook(
		runnerFactory,
		workspaceRoot,
		getVar,
		credResolver,
		fetchFunc,
	)
	RegisterBranchCheckHook(hook)
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	env.echo = e
	return env
}

func (env *batchEnv) doReq(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	var req *http.Request
	if bodyReader != nil {
		req = httptest.NewRequest(method, path, bodyReader)
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if auth != nil {
		authJSON, err := json.Marshal(auth)
		if err != nil {
			t.Fatalf("marshal auth: %v", err)
		}
		req.Header.Set("X-Test-Auth", string(authJSON))
	}
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

func (env *batchEnv) countPatches(t *testing.T) int {
	t.Helper()
	var count int
	if err := env.db.QueryRow(
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, env.slug,
	).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return count
}

func (env *batchEnv) resetCounters() {
	env.mu.Lock()
	env.fetchCalls = 0
	env.credCalls = 0
	env.getVarCalls = 0
	env.mu.Unlock()
}

func (env *batchEnv) setMode(mode string) {
	env.mu.Lock()
	env.mode = mode
	env.mu.Unlock()
}

// flipModeAfterFirstRead makes the variable getter switch to mode right after
// the first PATCH_BRANCH_SOURCE read, simulating a mid-request change.
func (env *batchEnv) flipModeAfterFirstRead(mode string) {
	env.mu.Lock()
	env.flipModeAfterRead = mode
	env.mu.Unlock()
}

func (env *batchEnv) clearAudit() {
	env.emitter.mu.Lock()
	env.emitter.events = nil
	env.emitter.mu.Unlock()
}

func (env *batchEnv) lastAuditEvent(t *testing.T, eventType string) *audit.HubEvent {
	t.Helper()
	events := env.emitter.Events()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventType == eventType {
			return &events[i]
		}
	}
	return nil
}

// batchGit runs a git command in the given directory.
func batchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_EDITOR=true",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s) failed: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// batchRevParse runs git rev-parse --verify in the given dir.
func batchRevParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", ref)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// batchCreateBranchOnBare creates a branch on a bare repo.
func batchCreateBranchOnBare(t *testing.T, bareDir, branchName, filename, content, message string) string {
	t.Helper()
	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "work")
	batchGit(t, "", "clone", bareDir, cloneDir)
	batchGit(t, cloneDir, "config", "user.name", "Test User")
	batchGit(t, cloneDir, "config", "user.email", "test@example.com")
	batchGit(t, cloneDir, "checkout", "-b", branchName)
	fpath := filepath.Join(cloneDir, filename)
	if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fpath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	batchGit(t, cloneDir, "add", filename)
	batchGit(t, cloneDir, "commit", "-m", message)
	sha := batchGit(t, cloneDir, "rev-parse", "HEAD")
	batchGit(t, cloneDir, "push", "origin", branchName)
	return sha
}

// ===========================================================================
// TS-21-5 (integration): PATCH_BRANCH_SOURCE changed between two requests
// takes effect on the second and is read once per request.
// Verifies: 21-REQ-1.5
// ===========================================================================

func TestTS21_5_PatchBranchSourceChangeBetweenRequests(t *testing.T) {
	slug := "ts21-5-var-change"
	env := newBatchEnv(t, slug, "hub")
	auth := userAuth("user-1")

	// Create a branch that exists only on the fork (not fetched into trunk).
	batchCreateBranchOnBare(t, env.forkDir, "fork-only", "fork.txt", "fork content", "add fork")

	// First request: hub mode → branch not found → 400, zero fetches.
	env.resetCounters()
	body := `{"branch_name": "fork-only"}`
	rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("first request: status = %d; want 400; body: %s", rec.Code, rec.Body.String())
	}
	env.mu.Lock()
	fc1 := env.fetchCalls
	gv1 := env.getVarCalls
	env.mu.Unlock()
	if fc1 != 0 {
		t.Errorf("first request: fetchCalls = %d; want 0", fc1)
	}

	// Change mode to origin.
	env.setMode("origin")
	env.resetCounters()
	env.clearAudit()

	// Second request: origin mode → fetch → 201.
	rec2 := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec2.Code != http.StatusCreated {
		t.Fatalf("second request: status = %d; want 201; body: %s", rec2.Code, rec2.Body.String())
	}
	env.mu.Lock()
	fc2 := env.fetchCalls
	gv2 := env.getVarCalls
	env.mu.Unlock()
	if fc2 != 1 {
		t.Errorf("second request: fetchCalls = %d; want 1", fc2)
	}

	// Verify audit shows origin_fetch.
	ev := env.lastAuditEvent(t, "hub.patch.create")
	if ev == nil {
		t.Fatal("expected hub.patch.create event")
	}
	if ev.Metadata["branch_resolution"] != "origin_fetch" {
		t.Errorf("branch_resolution = %v; want origin_fetch", ev.Metadata["branch_resolution"])
	}

	// Each request should read the variable exactly once.
	if gv1 != 1 {
		t.Errorf("first request: getVarCalls = %d; want 1", gv1)
	}
	if gv2 != 1 {
		t.Errorf("second request: getVarCalls = %d; want 1", gv2)
	}
}

// ===========================================================================
// TS-21-5 (integration, batch): PATCH_BRANCH_SOURCE is read once for a whole
// batch request, so a change mid-batch cannot mix modes within one request.
// Verifies: 21-REQ-1.5
// ===========================================================================

func TestTS21_5_PatchBranchSourceReadOncePerBatchRequest(t *testing.T) {
	slug := "ts21-5-batch-once"
	env := newBatchEnv(t, slug, "origin")
	auth := userAuth("user-1")

	// Two branches that exist only on the fork: each needs origin mode.
	batchCreateBranchOnBare(t, env.forkDir, "batch-fork-a", "a.txt", "a content", "add a")
	batchCreateBranchOnBare(t, env.forkDir, "batch-fork-b", "b.txt", "b content", "add b")

	// The variable flips to hub right after the first read. If the resolver
	// re-read it for the second element, that element would resolve in hub
	// mode and the batch would answer 400.
	env.flipModeAfterFirstRead("hub")
	env.resetCounters()

	body := `[{"branch_name":"batch-fork-a"},{"branch_name":"batch-fork-b"}]`
	rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body: %s", rec.Code, rec.Body.String())
	}

	env.mu.Lock()
	gv := env.getVarCalls
	fc := env.fetchCalls
	env.mu.Unlock()
	if gv != 1 {
		t.Errorf("getVarCalls = %d; want 1 (PATCH_BRANCH_SOURCE read once per request)", gv)
	}
	if fc != 2 {
		t.Errorf("fetchCalls = %d; want 2 (both elements resolved in origin mode)", fc)
	}
	if env.countPatches(t) != 2 {
		t.Errorf("patch count = %d; want 2", env.countPatches(t))
	}
	for _, b := range []string{"batch-fork-a", "batch-fork-b"} {
		if batchRevParse(t, env.trunkDir, "refs/heads/"+b) == "" {
			t.Errorf("refs/heads/%s should exist", b)
		}
	}

	// The memo is per request: the next request reads the variable afresh and
	// now sees hub mode.
	env.flipModeAfterFirstRead("")
	batchCreateBranchOnBare(t, env.forkDir, "batch-fork-c", "c.txt", "c content", "add c")
	env.resetCounters()
	rec2 := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches",
		`{"branch_name":"batch-fork-c"}`, auth)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("next request: status = %d; want 400 (hub mode); body: %s", rec2.Code, rec2.Body.String())
	}
	env.mu.Lock()
	gv2 := env.getVarCalls
	env.mu.Unlock()
	if gv2 != 1 {
		t.Errorf("next request: getVarCalls = %d; want 1", gv2)
	}
}

// ===========================================================================
// TS-21-10 (integration): Losing the compare-and-swap race to a hub push
// reports local and leaves the pushed ref intact.
// Verifies: 21-REQ-2.2
// ===========================================================================

func TestTS21_10_CASRaceReportsLocalLeavesRef(t *testing.T) {
	slug := "ts21-10-cas-race"
	workspaceRoot := t.TempDir()
	forkDir, trunkDir, _ := setupHTTPForkAndTrunk(t, workspaceRoot, slug)

	// Create a tracking-only branch "feat".
	trackingSHA := batchCreateBranchOnBare(t, forkDir, "feat", "feat.txt", "feat content", "add feat")
	batchGit(t, trunkDir, "fetch", "origin")

	// Create a different commit in the trunk to simulate a hub push.
	// We need a commit that exists in the trunk's object store.
	// Push a second commit to the fork on the feat branch, then fetch it.
	tmpDir := t.TempDir()
	tmpClone := filepath.Join(tmpDir, "other")
	batchGit(t, "", "clone", forkDir, tmpClone)
	batchGit(t, tmpClone, "config", "user.name", "Test User")
	batchGit(t, tmpClone, "config", "user.email", "test@example.com")
	batchGit(t, tmpClone, "checkout", "-b", "feat2", "origin/feat")
	fpath := filepath.Join(tmpClone, "other.txt")
	if err := os.WriteFile(fpath, []byte("other"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	batchGit(t, tmpClone, "add", "other.txt")
	batchGit(t, tmpClone, "commit", "-m", "other commit")
	otherSHA := batchGit(t, tmpClone, "rev-parse", "HEAD")
	batchGit(t, tmpClone, "push", "origin", "feat2")
	// Fetch feat2 into the trunk so the object exists.
	batchGit(t, trunkDir, "fetch", "origin", "feat2")

	// Verify tracking ref exists but local does not.
	if batchRevParse(t, trunkDir, "refs/heads/feat") != "" {
		t.Fatal("refs/heads/feat should not exist before test")
	}
	if batchRevParse(t, trunkDir, "refs/remotes/origin/feat") != trackingSHA {
		t.Fatal("tracking ref should point to trackingSHA")
	}

	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)
	seedCarryPatchWorkspaceRaw(t, db, slug, forkDir,
		"https://github.com/upstream/repo.git", "deploy")

	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	emitter := newMockAuditEmitter()

	// Create a runner factory that intercepts update-ref to simulate a race:
	// just before the resolver's update-ref, create refs/heads/feat at otherSHA.
	raceFactory := func(repoPath string) (carrypatch.GitRunner, error) {
		real, err := carrypatch.NewGitRunnerFactory()(repoPath)
		if err != nil {
			return nil, err
		}
		return &raceRunner{inner: real, trunkDir: trunkDir, otherSHA: otherSHA, t: t}, nil
	}

	getVar := func(_, _, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "hub", nil
		}
		return "", fmt.Errorf("not found")
	}

	hook := carrypatch.NewBranchResolverHook(
		raceFactory,
		workspaceRoot,
		getVar,
		func(_ string) (transport.AuthMethod, error) { return nil, nil },
		func(_ context.Context, _, _ string, _ transport.AuthMethod) error { return nil },
	)

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	cfgH := HandlerConfig{DB: db, Audit: emitter}
	if err := RegisterRoutesWithConfig(api, cfgH); err != nil {
		t.Fatalf("RegisterRoutesWithConfig: %v", err)
	}
	RegisterBranchCheckHook(hook)
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	auth := userAuth("user-1")
	body := `{"branch_name": "feat"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	authJSON, _ := json.Marshal(auth)
	req.Header.Set("X-Test-Auth", string(authJSON))
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body: %s", rec.Code, rec.Body.String())
	}

	// Verify audit shows local.
	events := emitter.Events()
	var createEv *audit.HubEvent
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventType == "hub.patch.create" {
			createEv = &events[i]
			break
		}
	}
	if createEv == nil {
		t.Fatal("expected hub.patch.create event")
	}
	if createEv.Metadata["branch_resolution"] != "local" {
		t.Errorf("branch_resolution = %v; want local", createEv.Metadata["branch_resolution"])
	}

	// Verify refs/heads/feat points at the pushed commit (otherSHA), not the tracking tip.
	finalSHA := batchRevParse(t, trunkDir, "refs/heads/feat")
	if finalSHA != otherSHA {
		t.Errorf("refs/heads/feat = %s; want %s (otherSHA)", finalSHA, otherSHA)
	}
}

// raceRunner intercepts update-ref to simulate a concurrent hub push.
type raceRunner struct {
	inner    carrypatch.GitRunner
	trunkDir string
	otherSHA string
	t        *testing.T
	raced    bool
}

func (r *raceRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) >= 1 && args[0] == "update-ref" && !r.raced {
		r.raced = true
		// Simulate a hub push creating refs/heads/feat at otherSHA.
		batchGit(r.t, r.trunkDir, "update-ref", "refs/heads/feat", r.otherSHA)
	}
	return r.inner.Run(ctx, args...)
}

func (r *raceRunner) CherryPick(ctx context.Context, commitSHA string) error {
	return r.inner.CherryPick(ctx, commitSHA)
}
func (r *raceRunner) MergeNoFF(ctx context.Context, ref, message string) error {
	return r.inner.MergeNoFF(ctx, ref, message)
}
func (r *raceRunner) MergeTree(ctx context.Context, base, head string) (string, error) {
	return r.inner.MergeTree(ctx, base, head)
}
func (r *raceRunner) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	return r.inner.IsAncestor(ctx, ancestor, descendant)
}
func (r *raceRunner) Cherry(ctx context.Context, upstream, head string) ([]string, []string, error) {
	return r.inner.Cherry(ctx, upstream, head)
}
func (r *raceRunner) HardReset(ctx context.Context, ref string) error {
	return r.inner.HardReset(ctx, ref)
}
func (r *raceRunner) WorktreeAdd(ctx context.Context, path, commit string) error {
	return r.inner.WorktreeAdd(ctx, path, commit)
}
func (r *raceRunner) WorktreeRemove(ctx context.Context, path string) error {
	return r.inner.WorktreeRemove(ctx, path)
}
func (r *raceRunner) WorktreePrune(ctx context.Context) error {
	return r.inner.WorktreePrune(ctx)
}
func (r *raceRunner) UpdateRef(ctx context.Context, ref, sha string) error {
	return r.inner.UpdateRef(ctx, ref, sha)
}

var _ carrypatch.GitRunner = (*raceRunner)(nil)

// ===========================================================================
// TS-21-20 (integration): A skipped single registration performs no lookup,
// fetch, credential resolution, ref write or lock in both modes.
// Verifies: 21-REQ-5.1
// ===========================================================================

func TestTS21_20_SkippedSingleNoLookupFetchCredRefLock(t *testing.T) {
	for _, mode := range []string{"hub", "origin"} {
		t.Run("mode="+mode, func(t *testing.T) {
			slug := "ts21-20-" + mode
			env := newBatchEnv(t, slug, mode)
			auth := userAuth("user-1")

			// Branch does not exist anywhere.
			env.resetCounters()
			body := `{"branch_name": "nonexistent-skip", "skip_branch_check": true}`
			rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d; want 201; body: %s", rec.Code, rec.Body.String())
			}

			env.mu.Lock()
			fc := env.fetchCalls
			cc := env.credCalls
			env.mu.Unlock()

			if fc != 0 {
				t.Errorf("fetchCalls = %d; want 0", fc)
			}
			if cc != 0 {
				t.Errorf("credCalls = %d; want 0", cc)
			}

			// No refs/heads ref should exist.
			if batchRevParse(t, env.trunkDir, "refs/heads/nonexistent-skip") != "" {
				t.Error("refs/heads/nonexistent-skip should not exist")
			}
		})
	}
}

// ===========================================================================
// TS-21-21 (integration): A mixed batch decides each element separately.
// Verifies: 21-REQ-5.2
// ===========================================================================

func TestTS21_21_MixedBatchDecidesSeparately(t *testing.T) {
	slug := "ts21-21-mixed"
	env := newBatchEnv(t, slug, "origin")
	auth := userAuth("user-1")

	// Element 0: skipped, absent → no local ref.
	// Element 1: tracking-only → local ref created.
	batchCreateBranchOnBare(t, env.forkDir, "track-only", "track.txt", "track content", "add track")
	batchGit(t, env.trunkDir, "fetch", "origin", "track-only")

	// Element 2: skipped → no local ref.
	// Element 3: fork-only → fetch + local ref.
	batchCreateBranchOnBare(t, env.forkDir, "fork-only-21", "fork21.txt", "fork21 content", "add fork21")

	env.resetCounters()

	body := `[
		{"branch_name":"absent-skip-0", "skip_branch_check": true},
		{"branch_name":"track-only"},
		{"branch_name":"absent-skip-2", "skip_branch_check": true},
		{"branch_name":"fork-only-21"}
	]`
	rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body: %s", rec.Code, rec.Body.String())
	}

	// Verify 4 rows inserted.
	if env.countPatches(t) != 4 {
		t.Errorf("patch count = %d; want 4", env.countPatches(t))
	}

	// Only elements 1 and 3 get local branches.
	if batchRevParse(t, env.trunkDir, "refs/heads/track-only") == "" {
		t.Error("refs/heads/track-only should exist")
	}
	if batchRevParse(t, env.trunkDir, "refs/heads/fork-only-21") == "" {
		t.Error("refs/heads/fork-only-21 should exist")
	}

	// Elements 0 and 2 get no local ref.
	if batchRevParse(t, env.trunkDir, "refs/heads/absent-skip-0") != "" {
		t.Error("refs/heads/absent-skip-0 should not exist")
	}
	if batchRevParse(t, env.trunkDir, "refs/heads/absent-skip-2") != "" {
		t.Error("refs/heads/absent-skip-2 should not exist")
	}

	// Fetch stub called once (for element 3 only).
	env.mu.Lock()
	fc := env.fetchCalls
	env.mu.Unlock()
	if fc != 1 {
		t.Errorf("fetchCalls = %d; want 1", fc)
	}
}

// ===========================================================================
// TS-21-23 (integration): Earlier local branches and fetched tracking refs
// survive a later failure and resolve as local afterwards.
// Verifies: 21-REQ-6.3
// ===========================================================================

func TestTS21_23_EarlierBranchesSurviveLaterFailure(t *testing.T) {
	slug := "ts21-23-survive"
	env := newBatchEnv(t, slug, "origin")
	auth := userAuth("user-1")

	// Element 0: tracking-only.
	batchCreateBranchOnBare(t, env.forkDir, "surv-a", "a.txt", "a content", "add a")
	batchGit(t, env.trunkDir, "fetch", "origin", "surv-a")

	// Element 1: fork-only (will be fetched).
	batchCreateBranchOnBare(t, env.forkDir, "surv-b", "b.txt", "b content", "add b")

	// Element 2: exists nowhere → 400.

	body := `[
		{"branch_name":"surv-a"},
		{"branch_name":"surv-b"},
		{"branch_name":"surv-missing"}
	]`
	rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body: %s", rec.Code, rec.Body.String())
	}

	// Verify error message has patch[2] prefix.
	var errResp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantMsg := "patch[2]: branch does not exist in repository or on origin"
	if errResp.Error.Message != wantMsg {
		t.Errorf("message = %q; want %q", errResp.Error.Message, wantMsg)
	}

	// No rows inserted.
	if env.countPatches(t) != 0 {
		t.Errorf("patch count = %d; want 0", env.countPatches(t))
	}

	// Earlier local branches remain.
	if batchRevParse(t, env.trunkDir, "refs/heads/surv-a") == "" {
		t.Error("refs/heads/surv-a should still exist")
	}
	if batchRevParse(t, env.trunkDir, "refs/heads/surv-b") == "" {
		t.Error("refs/heads/surv-b should still exist")
	}
	// Tracking ref for element 1 should exist.
	if batchRevParse(t, env.trunkDir, "refs/remotes/origin/surv-b") == "" {
		t.Error("refs/remotes/origin/surv-b should exist after fetch")
	}

	// Now register element 0 alone → should resolve as local.
	env.clearAudit()
	body2 := `{"branch_name":"surv-a"}`
	rec2 := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body2, auth)

	if rec2.Code != http.StatusCreated {
		t.Fatalf("second request: status = %d; want 201; body: %s", rec2.Code, rec2.Body.String())
	}

	ev := env.lastAuditEvent(t, "hub.patch.create")
	if ev == nil {
		t.Fatal("expected hub.patch.create event")
	}
	if ev.Metadata["branch_resolution"] != "local" {
		t.Errorf("branch_resolution = %v; want local", ev.Metadata["branch_resolution"])
	}
}

// ===========================================================================
// TS-21-24 (integration): Resolution precedes duplicate and already-registered
// checks, preserving error precedence.
// Verifies: 21-REQ-6.4, 21-REQ-6.5
// ===========================================================================

func TestTS21_24_ResolutionPrecedesDuplicateChecks(t *testing.T) {
	t.Run("duplicate_batch", func(t *testing.T) {
		slug := "ts21-24-dup"
		env := newBatchEnv(t, slug, "hub")
		auth := userAuth("user-1")

		// Create a tracking-only branch.
		batchCreateBranchOnBare(t, env.forkDir, "dup-branch", "dup.txt", "dup content", "add dup")
		batchGit(t, env.trunkDir, "fetch", "origin", "dup-branch")

		// Batch with the same tracking-only element repeated twice.
		body := `[
			{"branch_name":"dup-branch"},
			{"branch_name":"dup-branch"}
		]`
		rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d; want 409; body: %s", rec.Code, rec.Body.String())
		}

		// The local branch should have been created by resolution.
		if batchRevParse(t, env.trunkDir, "refs/heads/dup-branch") == "" {
			t.Error("refs/heads/dup-branch should exist after resolution")
		}
	})

	t.Run("already_registered_plus_missing", func(t *testing.T) {
		slug := "ts21-24-reg"
		env := newBatchEnv(t, slug, "hub")
		auth := userAuth("user-1")

		// Create a local branch and register it.
		batchCreateBranchOnBare(t, env.forkDir, "registered-br", "reg.txt", "reg content", "add reg")
		batchGit(t, env.trunkDir, "fetch", "origin", "registered-br")
		batchGit(t, env.trunkDir, "branch", "registered-br", "origin/registered-br")

		regBody := `{"branch_name":"registered-br"}`
		regRec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", regBody, auth)
		if regRec.Code != http.StatusCreated {
			t.Fatalf("seed registration: status = %d; want 201; body: %s", regRec.Code, regRec.Body.String())
		}

		// Batch: [already-registered, missing] → should answer 400 (missing branch
		// is resolved first, before the already-registered check).
		body := `[
			{"branch_name":"registered-br"},
			{"branch_name":"missing-24"}
		]`
		rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		// The resolution loop runs first. "registered-br" resolves as local (ok),
		// "missing-24" fails → 400.
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d; want 400; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("single_missing_if_not_exists", func(t *testing.T) {
		slug := "ts21-24-ifne"
		env := newBatchEnv(t, slug, "hub")
		auth := userAuth("user-1")

		// Register a branch first.
		batchCreateBranchOnBare(t, env.forkDir, "existing-ifne", "ifne.txt", "ifne content", "add ifne")
		batchGit(t, env.trunkDir, "fetch", "origin", "existing-ifne")
		batchGit(t, env.trunkDir, "branch", "existing-ifne", "origin/existing-ifne")

		regBody := `{"branch_name":"existing-ifne"}`
		regRec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", regBody, auth)
		if regRec.Code != http.StatusCreated {
			t.Fatalf("seed: status = %d; body: %s", regRec.Code, regRec.Body.String())
		}

		// Single request with a missing branch and if_not_exists → 400 (resolution
		// fails before if_not_exists is checked).
		body := `{"branch_name":"missing-ifne", "if_not_exists": true}`
		rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d; want 400; body: %s", rec.Code, rec.Body.String())
		}
	})
}

// ===========================================================================
// TS-21-27 (integration): A busy workspace lock answers 409 workspace_busy
// on the create and fetch paths without waiting.
// Verifies: 21-REQ-7.3
// ===========================================================================

func TestTS21_27_BusyLockAnswers409(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
	}{
		{"tracking_hub", "hub"},
		{"fork_origin", "origin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slug := "ts21-27-" + tc.name
			env := newBatchEnv(t, slug, tc.mode)
			auth := userAuth("user-1")

			// Create a tracking-only branch (for hub mode) or fork-only (for origin mode).
			if tc.mode == "hub" {
				batchCreateBranchOnBare(t, env.forkDir, "locked-br", "locked.txt", "locked content", "add locked")
				batchGit(t, env.trunkDir, "fetch", "origin", "locked-br")
			} else {
				batchCreateBranchOnBare(t, env.forkDir, "locked-br", "locked.txt", "locked content", "add locked")
			}

			// Hold the workspace lock.
			unlock, ok := wslock.TryLock(slug)
			if !ok {
				t.Fatal("could not acquire lock for test setup")
			}
			defer unlock()

			body := `{"branch_name": "locked-br"}`
			rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d; want 409; body: %s", rec.Code, rec.Body.String())
			}

			resp := parseTypedError(t, rec)
			errType := resp.Error.ErrorType
			if errType == "" {
				errType = resp.ErrorType
			}
			if errType != "workspace_busy" {
				t.Errorf("error_type = %q; want workspace_busy", errType)
			}
			wantMsg := "another operation is running on this workspace; retry later"
			if resp.Error.Message != wantMsg {
				t.Errorf("message = %q; want %q", resp.Error.Message, wantMsg)
			}

			// No row inserted.
			if env.countPatches(t) != 0 {
				t.Errorf("patch count = %d; want 0", env.countPatches(t))
			}

			// No local branch created.
			if batchRevParse(t, env.trunkDir, "refs/heads/locked-br") != "" {
				t.Error("refs/heads/locked-br should not exist")
			}
		})
	}
}

// ===========================================================================
// TS-21-28 (integration): The workspace lock is released before the insert
// and after every resolution outcome.
// Verifies: 21-REQ-7.4
// ===========================================================================

func TestTS21_28_LockReleasedAfterResolution(t *testing.T) {
	scenarios := []struct {
		name   string
		mode   string
		setup  func(t *testing.T, env *batchEnv) string // returns branch name
		expect int                                      // expected status
	}{
		{
			name: "tracking_create_success",
			mode: "hub",
			setup: func(t *testing.T, env *batchEnv) string {
				batchCreateBranchOnBare(t, env.forkDir, "lock28-track", "t.txt", "t", "t")
				batchGit(t, env.trunkDir, "fetch", "origin", "lock28-track")
				return "lock28-track"
			},
			expect: http.StatusCreated,
		},
		{
			name: "fetch_success",
			mode: "origin",
			setup: func(t *testing.T, env *batchEnv) string {
				batchCreateBranchOnBare(t, env.forkDir, "lock28-fetch", "f.txt", "f", "f")
				return "lock28-fetch"
			},
			expect: http.StatusCreated,
		},
		{
			name: "not_found",
			mode: "hub",
			setup: func(t *testing.T, env *batchEnv) string {
				return "lock28-missing"
			},
			expect: http.StatusBadRequest,
		},
		{
			// The fetch itself fails: the branch exists on the fork but the
			// trunk's origin no longer points at it.
			name: "fetch_failure",
			mode: "origin",
			setup: func(t *testing.T, env *batchEnv) string {
				batchCreateBranchOnBare(t, env.forkDir, "lock28-fetchfail", "ff.txt", "ff", "ff")
				batchGit(t, env.trunkDir, "remote", "set-url", "origin", "/nonexistent/path/to/repo.git")
				return "lock28-fetchfail"
			},
			expect: http.StatusBadGateway,
		},
		{
			// Creating the local ref fails after the lock was taken.
			name: "write_failure",
			mode: "hub",
			setup: func(t *testing.T, env *batchEnv) string {
				batchCreateBranchOnBare(t, env.forkDir, "lock28-write", "w.txt", "w", "w")
				batchGit(t, env.trunkDir, "fetch", "origin", "lock28-write")
				env.mu.Lock()
				env.wrapRunner = func(inner carrypatch.GitRunner) carrypatch.GitRunner {
					return &updateRefFailRunner{inner: inner}
				}
				env.mu.Unlock()
				return "lock28-write"
			},
			expect: http.StatusInternalServerError,
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			slug := "ts21-28-" + strings.ReplaceAll(sc.name, "_", "-")
			env := newBatchEnv(t, slug, sc.mode)
			auth := userAuth("user-1")

			branch := sc.setup(t, env)

			// Probe the lock the moment the resolver hook returns. The handler
			// inserts the row (when it inserts at all) after the hook returns,
			// on the same goroutine, so a lock that is free here is not held
			// at insert time.
			inner := branchCheckHook
			if inner == nil {
				t.Fatal("expected the resolver hook to be registered")
			}
			var probes []bool
			RegisterBranchCheckHook(func(ctx context.Context, s, b string) (string, error) {
				method, err := inner(ctx, s, b)
				unlock, free := wslock.TryLock(s)
				if free {
					unlock()
				}
				probes = append(probes, free)
				return method, err
			})

			body := fmt.Sprintf(`{"branch_name": %q}`, branch)
			rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

			if rec.Code != sc.expect {
				t.Fatalf("status = %d; want %d; body: %s", rec.Code, sc.expect, rec.Body.String())
			}

			if len(probes) != 1 {
				t.Fatalf("lock probed %d times; want 1 (once per hook call)", len(probes))
			}
			if !probes[0] {
				t.Error("workspace lock still held when the resolver hook returned (before insert)")
			}

			// The failure classes leave no row; the success classes leave one.
			wantRows := 0
			if sc.expect == http.StatusCreated {
				wantRows = 1
			}
			if got := env.countPatches(t); got != wantRows {
				t.Errorf("patch count = %d; want %d", got, wantRows)
			}

			// After the response, TryLock should succeed (lock released).
			unlock, ok := wslock.TryLock(slug)
			if !ok {
				t.Error("TryLock should succeed after response (lock not released)")
			} else {
				unlock()
			}
		})
	}
}

// ===========================================================================
// TS-21-29 (integration): Local-path and skipped registrations succeed while
// the lock is held elsewhere.
// Verifies: 21-REQ-7.5
// ===========================================================================

func TestTS21_29_LocalAndSkippedSucceedWhileLocked(t *testing.T) {
	slug := "ts21-29-locked"
	env := newBatchEnv(t, slug, "hub")
	auth := userAuth("user-1")

	// Create a local branch.
	batchCreateBranchOnBare(t, env.forkDir, "local-br", "local.txt", "local content", "add local")
	batchGit(t, env.trunkDir, "fetch", "origin", "local-br")
	batchGit(t, env.trunkDir, "branch", "local-br", "origin/local-br")

	// Hold the workspace lock.
	unlock, ok := wslock.TryLock(slug)
	if !ok {
		t.Fatal("could not acquire lock for test setup")
	}
	defer unlock()

	// Local branch registration should succeed.
	body1 := `{"branch_name": "local-br"}`
	rec1 := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body1, auth)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("local: status = %d; want 201; body: %s", rec1.Code, rec1.Body.String())
	}

	// Skipped registration should succeed.
	body2 := `{"branch_name": "missing-skip-29", "skip_branch_check": true}`
	rec2 := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body2, auth)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("skipped: status = %d; want 201; body: %s", rec2.Code, rec2.Body.String())
	}
}

// ===========================================================================
// TS-21-30 (integration): The hub.patch.create event carries branch_resolution
// for each path including skipped.
// Verifies: 21-REQ-8.1, 21-REQ-8.2
// ===========================================================================

func TestTS21_30_AuditBranchResolutionForEachPath(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		setup      func(t *testing.T, env *batchEnv) string // returns branch name
		skip       bool
		wantMethod string
	}{
		{
			name: "local",
			mode: "hub",
			setup: func(t *testing.T, env *batchEnv) string {
				batchCreateBranchOnBare(t, env.forkDir, "audit-local", "al.txt", "al", "al")
				batchGit(t, env.trunkDir, "fetch", "origin", "audit-local")
				batchGit(t, env.trunkDir, "branch", "audit-local", "origin/audit-local")
				return "audit-local"
			},
			wantMethod: "local",
		},
		{
			name: "origin_tracking",
			mode: "hub",
			setup: func(t *testing.T, env *batchEnv) string {
				batchCreateBranchOnBare(t, env.forkDir, "audit-track", "at.txt", "at", "at")
				batchGit(t, env.trunkDir, "fetch", "origin", "audit-track")
				return "audit-track"
			},
			wantMethod: "origin_tracking",
		},
		{
			name: "origin_fetch",
			mode: "origin",
			setup: func(t *testing.T, env *batchEnv) string {
				batchCreateBranchOnBare(t, env.forkDir, "audit-fetch", "af.txt", "af", "af")
				return "audit-fetch"
			},
			wantMethod: "origin_fetch",
		},
		{
			name: "skipped",
			mode: "hub",
			setup: func(t *testing.T, env *batchEnv) string {
				return "audit-skipped"
			},
			skip:       true,
			wantMethod: "skipped",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slug := "ts21-30-" + tc.name
			env := newBatchEnv(t, slug, tc.mode)
			auth := userAuth("user-1")

			branch := tc.setup(t, env)
			env.clearAudit()

			var body string
			if tc.skip {
				body = fmt.Sprintf(`{"branch_name": %q, "skip_branch_check": true}`, branch)
			} else {
				body = fmt.Sprintf(`{"branch_name": %q}`, branch)
			}

			rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d; want 201; body: %s", rec.Code, rec.Body.String())
			}

			ev := env.lastAuditEvent(t, "hub.patch.create")
			if ev == nil {
				t.Fatal("expected hub.patch.create event")
			}

			if ev.Metadata["branch_resolution"] != tc.wantMethod {
				t.Errorf("branch_resolution = %v; want %s", ev.Metadata["branch_resolution"], tc.wantMethod)
			}

			// branch_name and position should be present.
			if _, ok := ev.Metadata["branch_name"]; !ok {
				t.Error("metadata missing 'branch_name' key")
			}
			if _, ok := ev.Metadata["position"]; !ok {
				t.Error("metadata missing 'position' key")
			}
		})
	}
}
