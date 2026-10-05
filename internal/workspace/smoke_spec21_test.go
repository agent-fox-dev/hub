package workspace

import (
	"bytes"
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
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/carrypatch"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// ===========================================================================
// Smoke test environment: real handler, real carrypatch resolver, real git
// runner, real wslock, real SQLite database, real audit sink.
// ===========================================================================

// smokeEnv holds a fully-wired test HTTP server with real git repos and the
// carrypatch resolver built exactly as cmd/af-hub/main.go builds it.
type smokeEnv struct {
	echo          *echo.Echo
	db            *sql.DB
	workspaceRoot string
	forkDir       string
	trunkDir      string
	initialSHA    string
	slug          string
	emitter       *mockAuditEmitter
	logBuf        *bytes.Buffer
}

// newSmokeEnv creates a fully-wired test environment with:
//   - a real bare fork repo and a clone as trunk
//   - a real carrypatch resolver (NewBranchResolverHook) with real git runner,
//     real wslock, real DefaultSingleBranchFetch, and real credential resolver
//   - a real SQLite database with workspace and patches tables
//   - a real audit emitter (mockAuditEmitter captures events)
//   - no stubs for fetch or locks
func newSmokeEnv(t *testing.T, slug, mode string) *smokeEnv {
	t.Helper()

	workspaceRoot := t.TempDir()
	forkDir, trunkDir, initialSHA := setupHTTPForkAndTrunk(t, workspaceRoot, slug)

	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)
	seedCarryPatchWorkspaceRaw(t, db, slug,
		forkDir, // use the fork dir as git_url (file:// protocol)
		"https://github.com/upstream/repo.git",
		"deploy")

	// Capture log output.
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	// Audit emitter.
	emitter := newMockAuditEmitter()

	// Variable getter: returns the mode for PATCH_BRANCH_SOURCE.
	getVar := func(scope, s, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return mode, nil
		}
		return "", fmt.Errorf("not found")
	}

	// Real credential resolver, built as main.go builds it: ResolveCloneAuth
	// over the secrets store. The store holds no credentials for the test
	// workspace, so it answers (nil, nil), which is what the local file://
	// fork needs.
	store := secrets.NewStore(db)
	credResolver := func(s string) (transport.AuthMethod, error) {
		return ResolveCloneAuth(store, s)
	}

	// Real single-branch fetch.
	realFetch := carrypatch.DefaultSingleBranchFetch()

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

	// Register the carrypatch resolver hook — built exactly as main.go does.
	hook := carrypatch.NewBranchResolverHook(
		carrypatch.NewGitRunnerFactory(),
		workspaceRoot,
		getVar,
		credResolver,
		realFetch,
	)
	RegisterBranchCheckHook(hook)
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	return &smokeEnv{
		echo:          e,
		db:            db,
		workspaceRoot: workspaceRoot,
		forkDir:       forkDir,
		trunkDir:      trunkDir,
		initialSHA:    initialSHA,
		slug:          slug,
		emitter:       emitter,
		logBuf:        logBuf,
	}
}

func (env *smokeEnv) doReq(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
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

func (env *smokeEnv) countPatches(t *testing.T) int {
	t.Helper()
	var count int
	if err := env.db.QueryRow(
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, env.slug,
	).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return count
}

func (env *smokeEnv) lastAuditEvent(t *testing.T, eventType string) *audit.HubEvent {
	t.Helper()
	events := env.emitter.Events()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventType == eventType {
			return &events[i]
		}
	}
	return nil
}

func (env *smokeEnv) clearAudit() {
	env.emitter.mu.Lock()
	env.emitter.events = nil
	env.emitter.mu.Unlock()
}

// smokeGit runs a git command in the given directory.
func smokeGit(t *testing.T, dir string, args ...string) string {
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

// smokeRevParse runs git rev-parse --verify in the given dir.
func smokeRevParse(t *testing.T, dir, ref string) string {
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

// smokeCreateBranchOnBare creates a branch on a bare repo and returns the SHA.
func smokeCreateBranchOnBare(t *testing.T, bareDir, branchName, filename, content, message string) string {
	t.Helper()
	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "work")
	smokeGit(t, "", "clone", bareDir, cloneDir)
	smokeGit(t, cloneDir, "config", "user.name", "Test User")
	smokeGit(t, cloneDir, "config", "user.email", "test@example.com")
	smokeGit(t, cloneDir, "checkout", "-b", branchName)
	fpath := filepath.Join(cloneDir, filename)
	if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fpath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	smokeGit(t, cloneDir, "add", filename)
	smokeGit(t, cloneDir, "commit", "-m", message)
	sha := smokeGit(t, cloneDir, "rev-parse", "HEAD")
	smokeGit(t, cloneDir, "push", "origin", branchName)
	return sha
}

// ===========================================================================
// TS-21-43 (smoke): An operator registers a tracking-only branch in hub mode
// and gets origin_tracking.
//
// Verifies: 21-PATH-1, 21-REQ-1.2, 21-REQ-8.1
//
// Real components (must not be mocked): registration HTTP handler, carrypatch
// resolver, git runner, workspace lock, database, audit sink
// ===========================================================================

func TestTS21_43_TrackingOnlyBranchHubModeOriginTracking(t *testing.T) {
	slug := "ts21-43-smoke"
	env := newSmokeEnv(t, slug, "") // PATCH_BRANCH_SOURCE unset → hub mode

	auth := userAuth("user-1")

	// Create a branch on the fork and fetch it into the trunk as a tracking ref.
	trackingSHA := smokeCreateBranchOnBare(t, env.forkDir, "patch-branch", "patch.txt", "patch content", "add patch")
	smokeGit(t, env.trunkDir, "fetch", "origin")

	// Verify: tracking ref exists, local does not.
	if smokeRevParse(t, env.trunkDir, "refs/remotes/origin/patch-branch") == "" {
		t.Fatal("refs/remotes/origin/patch-branch should exist")
	}
	if smokeRevParse(t, env.trunkDir, "refs/heads/patch-branch") != "" {
		t.Fatal("refs/heads/patch-branch should NOT exist before registration")
	}

	// Register the branch.
	body := `{"branch_name": "patch-branch"}`
	rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	// Assert: 201 Created.
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body: %s", rec.Code, rec.Body.String())
	}

	// Assert: refs/heads/patch-branch exists at the tracking tip.
	localSHA := smokeRevParse(t, env.trunkDir, "refs/heads/patch-branch")
	if localSHA == "" {
		t.Fatal("refs/heads/patch-branch should exist after registration")
	}
	if localSHA != trackingSHA {
		t.Errorf("refs/heads/patch-branch = %s; want %s (tracking tip)", localSHA, trackingSHA)
	}

	// Assert: the row exists.
	if env.countPatches(t) != 1 {
		t.Errorf("patch count = %d; want 1", env.countPatches(t))
	}

	// Assert: hub.patch.create has branch_resolution = origin_tracking.
	ev := env.lastAuditEvent(t, "hub.patch.create")
	if ev == nil {
		t.Fatal("expected hub.patch.create event")
	}
	if ev.Metadata["branch_resolution"] != "origin_tracking" {
		t.Errorf("branch_resolution = %v; want origin_tracking", ev.Metadata["branch_resolution"])
	}
	if ev.Metadata["branch_name"] != "patch-branch" {
		t.Errorf("branch_name = %v; want patch-branch", ev.Metadata["branch_name"])
	}

	// Assert: workspace lock is free afterwards.
	unlock, ok := wslock.TryLock(slug)
	if !ok {
		t.Error("workspace lock should be free after registration")
	} else {
		unlock()
	}
}

// ===========================================================================
// TS-21-44 (smoke): An operator registers a branch pushed to the fork after
// the clone in origin mode.
//
// Verifies: 21-PATH-2, 21-REQ-1.3, 21-REQ-3.1
//
// Real components (must not be mocked): registration HTTP handler, carrypatch
// resolver, go-git fetch, workspace.ResolveCloneAuth, workspace lock,
// database, audit sink
// ===========================================================================

func TestTS21_44_ForkOnlyBranchOriginModeOriginFetch(t *testing.T) {
	slug := "ts21-44-smoke"
	env := newSmokeEnv(t, slug, "origin")

	auth := userAuth("user-1")

	// Push a branch to the fork AFTER the trunk was cloned.
	// This means neither refs/heads/<name> nor refs/remotes/origin/<name>
	// exist in the trunk.
	forkSHA := smokeCreateBranchOnBare(t, env.forkDir, "late-branch", "late.txt", "late content", "add late")

	// Verify: neither ref exists in the trunk.
	if smokeRevParse(t, env.trunkDir, "refs/heads/late-branch") != "" {
		t.Fatal("refs/heads/late-branch should NOT exist before registration")
	}
	if smokeRevParse(t, env.trunkDir, "refs/remotes/origin/late-branch") != "" {
		t.Fatal("refs/remotes/origin/late-branch should NOT exist before registration")
	}

	// Register the branch.
	body := `{"branch_name": "late-branch"}`
	rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	// Assert: 201 Created.
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body: %s", rec.Code, rec.Body.String())
	}

	// Assert: refs/remotes/origin/late-branch and refs/heads/late-branch exist
	// at the fork tip.
	trackingSHA := smokeRevParse(t, env.trunkDir, "refs/remotes/origin/late-branch")
	if trackingSHA == "" {
		t.Fatal("refs/remotes/origin/late-branch should exist after fetch")
	}
	if trackingSHA != forkSHA {
		t.Errorf("tracking ref = %s; want %s (fork tip)", trackingSHA, forkSHA)
	}

	localSHA := smokeRevParse(t, env.trunkDir, "refs/heads/late-branch")
	if localSHA == "" {
		t.Fatal("refs/heads/late-branch should exist after registration")
	}
	if localSHA != forkSHA {
		t.Errorf("local ref = %s; want %s (fork tip)", localSHA, forkSHA)
	}

	// Assert: the row exists.
	if env.countPatches(t) != 1 {
		t.Errorf("patch count = %d; want 1", env.countPatches(t))
	}

	// Assert: hub.patch.create has branch_resolution = origin_fetch.
	ev := env.lastAuditEvent(t, "hub.patch.create")
	if ev == nil {
		t.Fatal("expected hub.patch.create event")
	}
	if ev.Metadata["branch_resolution"] != "origin_fetch" {
		t.Errorf("branch_resolution = %v; want origin_fetch", ev.Metadata["branch_resolution"])
	}
}

// ===========================================================================
// TS-21-45 (smoke): A batch with a missing second branch is rejected without
// rows and the first branch resolves as local afterwards.
//
// Verifies: 21-PATH-3, 21-REQ-4.2, 21-REQ-6.3
//
// Real components (must not be mocked): registration HTTP handler, carrypatch
// resolver, git runner, database, audit sink
// ===========================================================================

func TestTS21_45_BatchMissingSecondBranchRejectedFirstResolvesLocal(t *testing.T) {
	slug := "ts21-45-smoke"
	env := newSmokeEnv(t, slug, "") // hub mode

	auth := userAuth("user-1")

	// Element 0: tracking-only branch.
	smokeCreateBranchOnBare(t, env.forkDir, "batch-ok", "ok.txt", "ok content", "add ok")
	smokeGit(t, env.trunkDir, "fetch", "origin")

	// Element 1: exists nowhere.

	// Post the batch.
	body := `[
		{"branch_name":"batch-ok"},
		{"branch_name":"batch-missing"}
	]`
	rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	// Assert: 400 with patch[1] prefix.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("batch status = %d; want 400; body: %s", rec.Code, rec.Body.String())
	}
	var errResp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantMsg := "patch[1]: branch does not exist in repository or on origin"
	if errResp.Error.Message != wantMsg {
		t.Errorf("message = %q; want %q", errResp.Error.Message, wantMsg)
	}

	// Assert: no rows inserted.
	if env.countPatches(t) != 0 {
		t.Errorf("patch count = %d; want 0", env.countPatches(t))
	}

	// Assert: refs/heads/batch-ok exists (created by resolution of element 0).
	if smokeRevParse(t, env.trunkDir, "refs/heads/batch-ok") == "" {
		t.Error("refs/heads/batch-ok should exist after resolution of element 0")
	}

	// Re-submit element 0 alone → should resolve as local.
	env.clearAudit()
	body2 := `{"branch_name":"batch-ok"}`
	rec2 := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body2, auth)

	if rec2.Code != http.StatusCreated {
		t.Fatalf("re-submit status = %d; want 201; body: %s", rec2.Code, rec2.Body.String())
	}

	// Assert: branch_resolution = local (no ref written this time).
	ev := env.lastAuditEvent(t, "hub.patch.create")
	if ev == nil {
		t.Fatal("expected hub.patch.create event on re-submit")
	}
	if ev.Metadata["branch_resolution"] != "local" {
		t.Errorf("branch_resolution = %v; want local", ev.Metadata["branch_resolution"])
	}
}

// ===========================================================================
// TS-21-46 (smoke): An unreachable fork in origin mode answers 502
// origin_fetch_failed.
//
// Verifies: 21-PATH-4, 21-REQ-3.4
//
// Real components (must not be mocked): registration HTTP handler, carrypatch
// resolver, go-git fetch, workspace lock, database
// ===========================================================================

func TestTS21_46_UnreachableForkOriginModeAnswers502(t *testing.T) {
	slug := "ts21-46-smoke"

	// Create the environment in origin mode.
	env := newSmokeEnv(t, slug, "origin")

	// Point the trunk's origin URL at a nonexistent path to simulate
	// an unreachable fork.
	smokeGit(t, env.trunkDir, "remote", "set-url", "origin", "/nonexistent/path/to/repo.git")

	auth := userAuth("user-1")

	// Register a branch that doesn't exist locally or in tracking refs.
	body := `{"branch_name": "unreachable-branch"}`
	rec := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	// Assert: 502.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d; want 502; body: %s", rec.Code, rec.Body.String())
	}

	// Assert: message and error_type.
	resp := parseTypedError(t, rec)
	if resp.Error.Message != "origin fetch failed" {
		t.Errorf("message = %q; want %q", resp.Error.Message, "origin fetch failed")
	}
	errType := resp.Error.ErrorType
	if errType == "" {
		errType = resp.ErrorType
	}
	if errType != "origin_fetch_failed" {
		t.Errorf("error_type = %q; want origin_fetch_failed", errType)
	}

	// Assert: the response body does not contain the underlying error text.
	bodyStr := rec.Body.String()
	if strings.Contains(bodyStr, "/nonexistent/path") {
		t.Error("response body should not contain the underlying error text")
	}

	// Assert: the underlying error text IS logged.
	logStr := env.logBuf.String()
	if !strings.Contains(logStr, "origin fetch failed") {
		t.Errorf("log should contain the origin fetch error; got: %s", logStr)
	}

	// Assert: no row and no local branch.
	if env.countPatches(t) != 0 {
		t.Errorf("patch count = %d; want 0", env.countPatches(t))
	}
	if smokeRevParse(t, env.trunkDir, "refs/heads/unreachable-branch") != "" {
		t.Error("refs/heads/unreachable-branch should not exist")
	}

	// Assert: workspace lock is released.
	unlock, ok := wslock.TryLock(slug)
	if !ok {
		t.Error("workspace lock should be free after failed registration")
	} else {
		unlock()
	}
}

// ===========================================================================
// TS-21-47 (smoke): Registration during a sync lock answers 409 for a
// tracking-only branch while a skipped registration is accepted.
//
// Verifies: 21-PATH-5, 21-REQ-7.3, 21-REQ-7.5
//
// Real components (must not be mocked): registration HTTP handler, carrypatch
// resolver, workspace lock, git runner, database, audit sink
// ===========================================================================

func TestTS21_47_SyncLock409SkippedAccepted(t *testing.T) {
	slug := "ts21-47-smoke"
	env := newSmokeEnv(t, slug, "") // hub mode

	auth := userAuth("user-1")

	// Create a tracking-only branch.
	smokeCreateBranchOnBare(t, env.forkDir, "locked-branch", "locked.txt", "locked content", "add locked")
	smokeGit(t, env.trunkDir, "fetch", "origin")

	// Verify tracking ref exists, local does not.
	if smokeRevParse(t, env.trunkDir, "refs/remotes/origin/locked-branch") == "" {
		t.Fatal("refs/remotes/origin/locked-branch should exist")
	}
	if smokeRevParse(t, env.trunkDir, "refs/heads/locked-branch") != "" {
		t.Fatal("refs/heads/locked-branch should NOT exist before test")
	}

	// Simulate a sync holding the workspace lock.
	unlock, ok := wslock.TryLock(slug)
	if !ok {
		t.Fatal("could not acquire lock for test setup")
	}

	// First: register the tracking-only branch → should get 409.
	body1 := `{"branch_name": "locked-branch"}`
	rec1 := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body1, auth)

	if rec1.Code != http.StatusConflict {
		t.Fatalf("locked registration: status = %d; want 409; body: %s", rec1.Code, rec1.Body.String())
	}

	resp := parseTypedError(t, rec1)
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

	// Assert: no row inserted for the locked registration.
	if env.countPatches(t) != 0 {
		t.Errorf("patch count after locked = %d; want 0", env.countPatches(t))
	}

	// Second: register another branch with skip_branch_check → should succeed.
	env.clearAudit()
	body2 := `{"branch_name": "skip-branch", "skip_branch_check": true}`
	rec2 := env.doReq(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body2, auth)

	if rec2.Code != http.StatusCreated {
		t.Fatalf("skipped registration: status = %d; want 201; body: %s", rec2.Code, rec2.Body.String())
	}

	// Assert: hub.patch.create for the skipped registration has branch_resolution = skipped.
	ev := env.lastAuditEvent(t, "hub.patch.create")
	if ev == nil {
		t.Fatal("expected hub.patch.create event for skipped registration")
	}
	if ev.Metadata["branch_resolution"] != "skipped" {
		t.Errorf("branch_resolution = %v; want skipped", ev.Metadata["branch_resolution"])
	}

	// Assert: no refs/heads ref was created for the skipped branch.
	if smokeRevParse(t, env.trunkDir, "refs/heads/skip-branch") != "" {
		t.Error("refs/heads/skip-branch should not exist (skipped)")
	}

	// Release the lock.
	unlock()

	// Assert: the lock is now free.
	unlock2, ok2 := wslock.TryLock(slug)
	if !ok2 {
		t.Error("workspace lock should be free after release")
	} else {
		unlock2()
	}
}
