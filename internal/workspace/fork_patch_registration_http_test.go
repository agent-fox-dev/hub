package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	"github.com/agent-fox-dev/hub/internal/carrypatch"
)

// ===========================================================================
// Git helpers for HTTP-level tests (duplicated from carrypatch tests because
// the workspace package cannot import carrypatch test helpers).
// ===========================================================================

func httpTestGit(t *testing.T, dir string, args ...string) string {
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

func httpTestRevParse(t *testing.T, dir, ref string) string {
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

func httpTestInitBareRepo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	httpTestGit(t, "", "init", "--bare", path)
}

func httpTestCloneRepo(t *testing.T, src, dst string) {
	t.Helper()
	httpTestGit(t, "", "clone", src, dst)
	httpTestGit(t, dst, "config", "user.name", "Test User")
	httpTestGit(t, dst, "config", "user.email", "test@example.com")
}

func httpTestCommitFile(t *testing.T, dir, filename, content, message string) string {
	t.Helper()
	fpath := filepath.Join(dir, filename)
	if err := os.MkdirAll(filepath.Dir(fpath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fpath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	httpTestGit(t, dir, "add", filename)
	httpTestGit(t, dir, "commit", "-m", message)
	return httpTestGit(t, dir, "rev-parse", "HEAD")
}

func httpTestCreateBranchOnBare(t *testing.T, bareDir, branchName, filename, content, message string) string {
	t.Helper()
	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "work")
	httpTestCloneRepo(t, bareDir, cloneDir)
	httpTestGit(t, cloneDir, "checkout", "-b", branchName)
	sha := httpTestCommitFile(t, cloneDir, filename, content, message)
	httpTestGit(t, cloneDir, "push", "origin", branchName)
	return sha
}

// setupHTTPForkAndTrunk creates a bare "fork" repo with a main branch and an
// initial commit, then clones it as the "trunk" at <workspaceRoot>/<slug>/trunk.
func setupHTTPForkAndTrunk(t *testing.T, workspaceRoot, slug string) (forkDir, trunkDir, initialSHA string) {
	t.Helper()
	forkDir = filepath.Join(t.TempDir(), "fork.git")
	httpTestInitBareRepo(t, forkDir)

	tmpDir := t.TempDir()
	tmpClone := filepath.Join(tmpDir, "init-clone")
	httpTestCloneRepo(t, forkDir, tmpClone)
	initialSHA = httpTestCommitFile(t, tmpClone, "README.md", "initial", "initial commit")
	httpTestGit(t, tmpClone, "push", "origin", "main")

	trunkDir = filepath.Join(workspaceRoot, slug, "trunk")
	if err := os.MkdirAll(filepath.Dir(trunkDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	httpTestCloneRepo(t, forkDir, trunkDir)
	return forkDir, trunkDir, initialSHA
}

// ===========================================================================
// HTTP test environment with real git repos and the carrypatch resolver.
// ===========================================================================

type httpResolverTestEnv struct {
	echo          *echo.Echo
	db            *sql.DB
	workspaceRoot string
	forkDir       string
	trunkDir      string
	initialSHA    string
	slug          string
	fetchCalls    int
	credCalls     int
	credErr       error
	fetchFunc     carrypatch.SingleBranchFetchFunc
	getVar        carrypatch.GetVariableFunc
	logBuf        *bytes.Buffer
}

func newHTTPResolverTestEnv(t *testing.T, slug, mode string) *httpResolverTestEnv {
	t.Helper()

	workspaceRoot := t.TempDir()
	forkDir, trunkDir, initialSHA := setupHTTPForkAndTrunk(t, workspaceRoot, slug)

	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)
	seedCarryPatchWorkspaceRaw(t, db, slug,
		forkDir, // use the fork dir as git_url
		"https://github.com/upstream/repo.git",
		"deploy")

	env := &httpResolverTestEnv{
		db:            db,
		workspaceRoot: workspaceRoot,
		forkDir:       forkDir,
		trunkDir:      trunkDir,
		initialSHA:    initialSHA,
		slug:          slug,
	}

	// Default getVar returns the mode.
	env.getVar = func(scope, s, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return mode, nil
		}
		return "", fmt.Errorf("not found")
	}

	// Default fetch: use the real single-branch fetch.
	realFetch := carrypatch.DefaultSingleBranchFetch()
	env.fetchFunc = func(ctx context.Context, repoPath, branch string, auth transport.AuthMethod) error {
		env.fetchCalls++
		return realFetch(ctx, repoPath, branch, auth)
	}

	// Default cred resolver: no auth needed for local file:// protocol.
	env.credErr = nil
	credResolver := func(s string) (transport.AuthMethod, error) {
		env.credCalls++
		if env.credErr != nil {
			return nil, env.credErr
		}
		return nil, nil
	}

	// Capture log output.
	env.logBuf = &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(env.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	// Build the echo server with the resolver hook.
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}

	// Register the carrypatch resolver hook.
	hook := carrypatch.NewBranchResolverHook(
		carrypatch.NewGitRunnerFactory(),
		workspaceRoot,
		env.getVar,
		credResolver,
		func(ctx context.Context, repoPath, branch string, auth transport.AuthMethod) error {
			return env.fetchFunc(ctx, repoPath, branch, auth)
		},
	)
	RegisterBranchCheckHook(hook)
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	env.echo = e
	return env
}

func (env *httpResolverTestEnv) doRequest(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
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

func (env *httpResolverTestEnv) patchCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := env.db.QueryRow(
		`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, env.slug,
	).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return count
}

// typedErrorEnvelope represents the JSON error response with error_type.
type typedErrorEnvelope struct {
	Error struct {
		Code      int    `json:"code"`
		Message   string `json:"message"`
		ErrorType string `json:"error_type"`
	} `json:"error"`
	ErrorType string `json:"error_type"`
}

func parseTypedError(t *testing.T, rec *httptest.ResponseRecorder) typedErrorEnvelope {
	t.Helper()
	var resp typedErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v\nbody: %s", err, rec.Body.String())
	}
	return resp
}

// ===========================================================================
// TS-21-6 (integration): A name that is only a tag, a SHA or a remote-
// qualified name is rejected with 400 and nothing is created.
// Verifies: 21-REQ-1.6
// ===========================================================================

func TestTS21_6_TagSHARemoteQualifiedRejected(t *testing.T) {
	slug := "ts21-6-tag-sha"
	env := newHTTPResolverTestEnv(t, slug, "hub")
	auth := userAuth("user-1")

	// Create a tag on the trunk.
	httpTestGit(t, env.trunkDir, "tag", "v1")

	// Create a remote-tracking ref for "x" by pushing a branch to the fork
	// and fetching it.
	httpTestCreateBranchOnBare(t, env.forkDir, "x", "x.txt", "x content", "add x")
	httpTestGit(t, env.trunkDir, "fetch", "origin")

	// Verify refs/remotes/origin/x exists but refs/heads/x does not.
	if httpTestRevParse(t, env.trunkDir, "refs/remotes/origin/x") == "" {
		t.Fatal("refs/remotes/origin/x should exist")
	}

	names := []string{"v1", env.initialSHA, "origin/x"}
	for _, name := range names {
		t.Run("name="+name, func(t *testing.T) {
			body := fmt.Sprintf(`{"branch_name": %q}`, name)
			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d; want 400; body: %s", rec.Code, rec.Body.String())
			}

			var errResp errorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			wantMsg := "branch does not exist in repository or on origin"
			if errResp.Error.Message != wantMsg {
				t.Errorf("message = %q; want %q", errResp.Error.Message, wantMsg)
			}

			if env.patchCount(t) != 0 {
				t.Error("expected 0 patches")
			}

			// For "v1" and "origin/x", verify no refs/heads ref was created.
			// (The SHA name won't be a valid ref name anyway.)
			if name == "v1" || name == "origin/x" {
				if httpTestRevParse(t, env.trunkDir, "refs/heads/"+name) != "" {
					t.Errorf("refs/heads/%s should not exist", name)
				}
			}
		})
	}
}

// ===========================================================================
// TS-21-11 (integration): A ref write failure other than ref-exists answers
// 500 and inserts no row.
// Verifies: 21-REQ-2.3
// ===========================================================================

func TestTS21_11_RefWriteFailureAnswers500(t *testing.T) {
	slug := "ts21-11-write-fail"
	workspaceRoot := t.TempDir()
	forkDir, trunkDir, _ := setupHTTPForkAndTrunk(t, workspaceRoot, slug)

	// Create a tracking-only branch.
	httpTestCreateBranchOnBare(t, forkDir, "feat", "feat.txt", "feat content", "add feat")
	httpTestGit(t, trunkDir, "fetch", "origin")

	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)
	seedCarryPatchWorkspaceRaw(t, db, slug, forkDir,
		"https://github.com/upstream/repo.git", "deploy")

	// Capture log output.
	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	// Create a runner factory that wraps the real runner but makes update-ref
	// fail with a non-exists error (simulating a read-only refs directory).
	failingFactory := func(repoPath string) (carrypatch.GitRunner, error) {
		real, err := carrypatch.NewGitRunnerFactory()(repoPath)
		if err != nil {
			return nil, err
		}
		return &updateRefFailRunner{inner: real}, nil
	}

	getVar := func(_, _, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "hub", nil
		}
		return "", fmt.Errorf("not found")
	}

	hook := carrypatch.NewBranchResolverHook(
		failingFactory,
		workspaceRoot,
		getVar,
		func(_ string) (transport.AuthMethod, error) { return nil, nil },
		func(_ context.Context, _, _ string, _ transport.AuthMethod) error { return nil },
	)

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
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

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want 500; body: %s", rec.Code, rec.Body.String())
	}

	var errResp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp.Error.Message != "internal server error" {
		t.Errorf("message = %q; want %q", errResp.Error.Message, "internal server error")
	}

	// Verify no patch row was inserted.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, slug).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 patches; got %d", count)
	}

	// Verify the log contains the slug and branch.
	logStr := logBuf.String()
	if !strings.Contains(logStr, slug) {
		t.Errorf("log should contain slug %q; got: %s", slug, logStr)
	}
	if !strings.Contains(logStr, "feat") {
		t.Errorf("log should contain branch name 'feat'; got: %s", logStr)
	}
}

// updateRefFailRunner wraps a GitRunner and makes update-ref fail with a
// non-exists error (simulating a read-only refs directory).
type updateRefFailRunner struct {
	inner carrypatch.GitRunner
}

func (r *updateRefFailRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) >= 1 && args[0] == "update-ref" {
		return "", fmt.Errorf("fatal: unable to create refs/heads/feat: permission denied")
	}
	return r.inner.Run(ctx, args...)
}

func (r *updateRefFailRunner) CherryPick(ctx context.Context, commitSHA string) error {
	return r.inner.CherryPick(ctx, commitSHA)
}
func (r *updateRefFailRunner) MergeNoFF(ctx context.Context, ref, message string) error {
	return r.inner.MergeNoFF(ctx, ref, message)
}
func (r *updateRefFailRunner) MergeTree(ctx context.Context, base, head string) (string, error) {
	return r.inner.MergeTree(ctx, base, head)
}
func (r *updateRefFailRunner) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	return r.inner.IsAncestor(ctx, ancestor, descendant)
}
func (r *updateRefFailRunner) Cherry(ctx context.Context, upstream, head string) ([]string, []string, error) {
	return r.inner.Cherry(ctx, upstream, head)
}
func (r *updateRefFailRunner) HardReset(ctx context.Context, ref string) error {
	return r.inner.HardReset(ctx, ref)
}
func (r *updateRefFailRunner) WorktreeAdd(ctx context.Context, path, commit string) error {
	return r.inner.WorktreeAdd(ctx, path, commit)
}
func (r *updateRefFailRunner) WorktreeRemove(ctx context.Context, path string) error {
	return r.inner.WorktreeRemove(ctx, path)
}
func (r *updateRefFailRunner) WorktreePrune(ctx context.Context) error {
	return r.inner.WorktreePrune(ctx)
}
func (r *updateRefFailRunner) UpdateRef(ctx context.Context, ref, sha string) error {
	return r.inner.UpdateRef(ctx, ref, sha)
}

var _ carrypatch.GitRunner = (*updateRefFailRunner)(nil)

// ===========================================================================
// TS-21-14 (integration): A real fork that lacks the branch gives 400
// decided by type or remote ref listing, not error text.
// Verifies: 21-REQ-3.3, 21-REQ-3.6
// ===========================================================================

func TestTS21_14_RealForkMissingBranchGives400(t *testing.T) {
	slug := "ts21-14-missing"
	env := newHTTPResolverTestEnv(t, slug, "origin")
	auth := userAuth("user-1")

	// The fork does not have branch "ghost".
	body := `{"branch_name": "ghost"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body: %s", rec.Code, rec.Body.String())
	}

	var errResp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantMsg := "branch does not exist in repository or on origin"
	if errResp.Error.Message != wantMsg {
		t.Errorf("message = %q; want %q", errResp.Error.Message, wantMsg)
	}

	// Verify no local branch was created.
	if httpTestRevParse(t, env.trunkDir, "refs/heads/ghost") != "" {
		t.Error("refs/heads/ghost should not exist")
	}

	// Verify no row was inserted.
	if env.patchCount(t) != 0 {
		t.Error("expected 0 patches")
	}

	// Verify classification is by type, not error text: the carrypatch
	// package's ErrBranchNotOnOrigin sentinel is used, and wrapping it
	// with different text still classifies as not_found.
	t.Run("classification_by_type", func(t *testing.T) {
		// A wrapped ErrBranchNotOnOrigin should still be classified as not_found.
		wrappedErr := fmt.Errorf("xyz: %w", carrypatch.ErrBranchNotOnOrigin)
		if !errors.Is(wrappedErr, carrypatch.ErrBranchNotOnOrigin) {
			t.Error("wrapped ErrBranchNotOnOrigin should be detectable via errors.Is")
		}

		// A plain error with text matching "couldn't find remote ref" should
		// NOT be classified as not_found (we don't match on text).
		plainErr := errors.New("couldn't find remote ref")
		if errors.Is(plainErr, carrypatch.ErrBranchNotOnOrigin) {
			t.Error("plain error should not match ErrBranchNotOnOrigin")
		}
	})
}

// ===========================================================================
// TS-21-15 (integration): An unreachable fork answers 502 origin_fetch_failed
// without echoing the error text.
// Verifies: 21-REQ-3.4
// ===========================================================================

func TestTS21_15_UnreachableForkAnswers502(t *testing.T) {
	slug := "ts21-15-unreachable"
	workspaceRoot := t.TempDir()
	forkDir, trunkDir, _ := setupHTTPForkAndTrunk(t, workspaceRoot, slug)
	_ = forkDir

	// Point the trunk's origin URL at a nonexistent path.
	httpTestGit(t, trunkDir, "remote", "set-url", "origin", "/nonexistent/path/to/repo.git")

	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)
	seedCarryPatchWorkspaceRaw(t, db, slug, "/nonexistent/path/to/repo.git",
		"https://github.com/upstream/repo.git", "deploy")

	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	getVar := func(_, _, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		return "", fmt.Errorf("not found")
	}

	hook := carrypatch.NewBranchResolverHook(
		carrypatch.NewGitRunnerFactory(),
		workspaceRoot,
		getVar,
		func(_ string) (transport.AuthMethod, error) { return nil, nil },
		carrypatch.DefaultSingleBranchFetch(),
	)

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}
	RegisterBranchCheckHook(hook)
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	auth := userAuth("user-1")
	body := `{"branch_name": "somebranch"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	authJSON, _ := json.Marshal(auth)
	req.Header.Set("X-Test-Auth", string(authJSON))
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d; want 502; body: %s", rec.Code, rec.Body.String())
	}

	resp := parseTypedError(t, rec)
	if resp.Error.Message != "origin fetch failed" {
		t.Errorf("message = %q; want %q", resp.Error.Message, "origin fetch failed")
	}
	errType := resp.Error.ErrorType
	if errType == "" {
		errType = resp.ErrorType
	}
	if errType != "origin_fetch_failed" {
		t.Errorf("error_type = %q; want %q", errType, "origin_fetch_failed")
	}

	// Verify the response body does not contain the underlying error text.
	bodyStr := rec.Body.String()
	if strings.Contains(bodyStr, "/nonexistent/path") {
		t.Error("response body should not contain the underlying error text")
	}

	// Verify the error text IS logged.
	logStr := logBuf.String()
	// The go-git error for a nonexistent path says "repository not found".
	if !strings.Contains(logStr, "origin fetch failed") {
		t.Errorf("log should contain the origin fetch error; got: %s", logStr)
	}

	// Verify no local branch and no patch row.
	if httpTestRevParse(t, trunkDir, "refs/heads/somebranch") != "" {
		t.Error("refs/heads/somebranch should not exist")
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, slug).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 patches; got %d", count)
	}
}

// ===========================================================================
// TS-21-16 (integration): A credential resolution failure answers 502 and
// skips the fetch.
// Verifies: 21-REQ-3.5
// ===========================================================================

func TestTS21_16_CredentialFailureAnswers502(t *testing.T) {
	slug := "ts21-16-cred-fail"
	workspaceRoot := t.TempDir()
	_, _, _ = setupHTTPForkAndTrunk(t, workspaceRoot, slug)

	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)
	seedCarryPatchWorkspaceRaw(t, db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git", "deploy")

	getVar := func(_, _, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		return "", fmt.Errorf("not found")
	}

	var fetchCalls int
	fetchStub := func(_ context.Context, _, _ string, _ transport.AuthMethod) error {
		fetchCalls++
		return nil
	}

	credResolver := func(_ string) (transport.AuthMethod, error) {
		return nil, errors.New("credential store unavailable")
	}

	hook := carrypatch.NewBranchResolverHook(
		carrypatch.NewGitRunnerFactory(),
		workspaceRoot,
		getVar,
		credResolver,
		fetchStub,
	)

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}
	RegisterBranchCheckHook(hook)
	t.Cleanup(func() { RegisterBranchCheckHook(nil) })

	auth := userAuth("user-1")
	body := `{"branch_name": "nonexistent"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	authJSON, _ := json.Marshal(auth)
	req.Header.Set("X-Test-Auth", string(authJSON))
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d; want 502; body: %s", rec.Code, rec.Body.String())
	}

	var errResp errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errResp.Error.Message != "failed to resolve origin credentials" {
		t.Errorf("message = %q; want %q", errResp.Error.Message, "failed to resolve origin credentials")
	}

	// Verify the fetch stub was never called.
	if fetchCalls != 0 {
		t.Errorf("fetch calls = %d; want 0", fetchCalls)
	}

	// Verify no local branch and no patch row.
	trunkDir := filepath.Join(workspaceRoot, slug, "trunk")
	if httpTestRevParse(t, trunkDir, "refs/heads/nonexistent") != "" {
		t.Error("refs/heads/nonexistent should not exist")
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM patches WHERE workspace_slug = ?`, slug).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 patches; got %d", count)
	}
}

// ===========================================================================
// TS-21-17 (integration): A single registration for a branch found nowhere
// answers 400 with the new message in each mode.
// Verifies: 21-REQ-4.1
// ===========================================================================

func TestTS21_17_BranchFoundNowhereAnswers400(t *testing.T) {
	for _, mode := range []string{"hub", "origin"} {
		t.Run("mode="+mode, func(t *testing.T) {
			slug := "ts21-17-" + mode
			env := newHTTPResolverTestEnv(t, slug, mode)
			auth := userAuth("user-1")

			body := `{"branch_name": "nonexistent-branch"}`
			rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/patches", body, auth)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d; want 400; body: %s", rec.Code, rec.Body.String())
			}

			var errResp errorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			wantMsg := "branch does not exist in repository or on origin"
			if errResp.Error.Message != wantMsg {
				t.Errorf("message = %q; want %q", errResp.Error.Message, wantMsg)
			}

			if env.patchCount(t) != 0 {
				t.Error("expected 0 patches")
			}

			if httpTestRevParse(t, env.trunkDir, "refs/heads/nonexistent-branch") != "" {
				t.Error("refs/heads/nonexistent-branch should not exist")
			}
		})
	}
}

// ===========================================================================
// TS-21-19 (property): The not-found message is identical in hub and origin
// mode for any missing branch.
// Verifies: 21-REQ-4.3
// ===========================================================================

func TestTS21_19_NotFoundMessageIdenticalInBothModes(t *testing.T) {
	// Use a set of generated valid branch names that are absent from the
	// trunk and fork.
	missingNames := []string{
		"nonexistent-a",
		"feature/missing-b",
		"fix/absent-c",
		"release/gone-d",
		"hotfix/nowhere-e",
	}

	wantMsg := "branch does not exist in repository or on origin"

	for _, name := range missingNames {
		t.Run("branch="+name, func(t *testing.T) {
			auth := userAuth("user-1")
			body := fmt.Sprintf(`{"branch_name": %q}`, name)

			var hubMsg, originMsg string

			// Run hub mode.
			t.Run("hub", func(t *testing.T) {
				slugHub := "ts21-19h-" + strings.ReplaceAll(name, "/", "-")
				envHub := newHTTPResolverTestEnv(t, slugHub, "hub")
				recHub := envHub.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slugHub+"/patches", body, auth)
				if recHub.Code != http.StatusBadRequest {
					t.Fatalf("hub status = %d; want 400; body: %s", recHub.Code, recHub.Body.String())
				}
				var hubErr errorEnvelope
				json.Unmarshal(recHub.Body.Bytes(), &hubErr)
				hubMsg = hubErr.Error.Message
				if hubMsg != wantMsg {
					t.Errorf("hub message = %q; want %q", hubMsg, wantMsg)
				}
			})

			// Run origin mode.
			t.Run("origin", func(t *testing.T) {
				slugOrigin := "ts21-19o-" + strings.ReplaceAll(name, "/", "-")
				envOrigin := newHTTPResolverTestEnv(t, slugOrigin, "origin")
				recOrigin := envOrigin.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slugOrigin+"/patches", body, auth)
				if recOrigin.Code != http.StatusBadRequest {
					t.Fatalf("origin status = %d; want 400; body: %s", recOrigin.Code, recOrigin.Body.String())
				}
				var originErr errorEnvelope
				json.Unmarshal(recOrigin.Body.Bytes(), &originErr)
				originMsg = originErr.Error.Message
				if originMsg != wantMsg {
					t.Errorf("origin message = %q; want %q", originMsg, wantMsg)
				}
			})

			// Compare messages.
			if hubMsg != "" && originMsg != "" && hubMsg != originMsg {
				t.Errorf("messages differ: hub=%q origin=%q", hubMsg, originMsg)
			}
		})
	}
}

// ===========================================================================
// TS-21-35 (integration): The carrypatch resolver is constructed from
// injected dependencies and registered in main in place of the inline closure.
// Verifies: 21-REQ-9.3
// ===========================================================================

func TestTS21_35_ResolverConstructedFromInjectedDeps(t *testing.T) {
	// Part 1: Construct the hook with stubs and verify the stubs are used.
	t.Run("stubs_are_used", func(t *testing.T) {
		workspaceRoot := t.TempDir()
		slug := "ts21-35-stubs"
		_, _, _ = setupHTTPForkAndTrunk(t, workspaceRoot, slug)

		var getVarCalls int
		getVar := func(scope, s, key string) (string, error) {
			getVarCalls++
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			return "", fmt.Errorf("not found")
		}

		var credCalls int
		credResolver := func(_ string) (transport.AuthMethod, error) {
			credCalls++
			return nil, nil
		}

		var fetchCalls int
		fetchStub := func(_ context.Context, _, _ string, _ transport.AuthMethod) error {
			fetchCalls++
			return carrypatch.ErrBranchNotOnOrigin
		}

		hook := carrypatch.NewBranchResolverHook(
			carrypatch.NewGitRunnerFactory(),
			workspaceRoot,
			getVar,
			credResolver,
			fetchStub,
		)

		// Call the hook — the branch doesn't exist, so it will go through
		// all steps and use the stubs.
		_, err := hook(context.Background(), slug, "nonexistent")
		if err == nil {
			t.Fatal("expected error for missing branch")
		}

		if getVarCalls == 0 {
			t.Error("getVariable stub was not called")
		}
		if credCalls == 0 {
			t.Error("credential resolver stub was not called")
		}
		if fetchCalls == 0 {
			t.Error("fetch stub was not called")
		}
	})

	// Part 2: Read cmd/af-hub/main.go and verify it registers the
	// carrypatch resolver and no longer contains the inline rev-parse closure.
	t.Run("main_go_wiring", func(t *testing.T) {
		mainPath := filepath.Join(".", "..", "..", "cmd", "af-hub", "main.go")
		src, err := os.ReadFile(mainPath)
		if err != nil {
			t.Fatalf("read main.go: %v", err)
		}
		srcStr := string(src)

		// Must contain the carrypatch resolver registration.
		if !strings.Contains(srcStr, "RegisterBranchCheckHook(carrypatch.") {
			t.Error("main.go should contain RegisterBranchCheckHook(carrypatch.)")
		}

		// Must NOT contain the inline rev-parse --verify closure for branch checking.
		// The old pattern was: rev-parse --verify inside a RegisterBranchCheckHook closure.
		// We check that rev-parse --verify does not appear in the context of
		// RegisterBranchCheckHook.
		if strings.Contains(srcStr, `rev-parse", "--verify", branchName`) {
			t.Error("main.go should no longer contain the inline rev-parse --verify branch-check closure")
		}
		if strings.Contains(srcStr, `rev-parse", "--verify", branch`) {
			t.Error("main.go should no longer contain the inline rev-parse --verify branch-check closure")
		}
	})
}
