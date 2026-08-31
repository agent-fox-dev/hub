package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
	_ "modernc.org/sqlite"
)

// ===========================================================================
// Database helpers
// ===========================================================================

// openTestDB opens an in-memory SQLite database with WAL mode and busy_timeout.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("failed to set WAL mode: %v", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		t.Fatalf("failed to set busy_timeout: %v", err)
	}
	return db
}

// newTestQueue creates an in-memory SQLite database, initialises the
// jobqueue schema, and returns a new Queue and the underlying *sql.DB.
func newTestQueue(t *testing.T) (*jobqueue.Queue, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey() returned error: %v", err)
	}
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress() returned error: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("jobqueue.New() returned error: %v", err)
	}
	return q, db
}

// nopLogger returns a *slog.Logger that discards all output.
func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// createWorkspacesTable creates the workspaces table for tests, matching the
// production schema in workspace/schema.go.
func createWorkspacesTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS workspaces (
			slug              TEXT PRIMARY KEY,
			git_url           TEXT NOT NULL,
			branch            TEXT,
			owner_id          TEXT NOT NULL,
			org_id            TEXT,
			status            TEXT NOT NULL DEFAULT 'active',
			display_name      TEXT NOT NULL DEFAULT '',
			description       TEXT NOT NULL DEFAULT '',
			clone_status      TEXT NOT NULL DEFAULT 'pending' CHECK(clone_status IN ('pending','cloning','ready','failed','archived')),
			head_sha          TEXT,
			clone_error       TEXT,
			created_at        TEXT NOT NULL,
			updated_at        TEXT NOT NULL,
			sync_mode         TEXT NOT NULL DEFAULT 'pull_only',
			sync_status       TEXT NOT NULL DEFAULT 'idle',
			upstream_head_sha TEXT,
			last_sync_at      TEXT,
			sync_error        TEXT,
			workspace_mode    TEXT NOT NULL DEFAULT 'standard',
			upstream_url      TEXT,
			integration_branch TEXT
		)`)
	if err != nil {
		t.Fatalf("failed to create workspaces table: %v", err)
	}
}

// createPatchesTable creates the patches table for tests.
func createPatchesTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS patches (
			id              TEXT PRIMARY KEY,
			workspace_slug  TEXT NOT NULL,
			branch_name     TEXT NOT NULL,
			position        INTEGER NOT NULL,
			status          TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','conflict','disabled','merged_upstream')),
			conflict_files  TEXT,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL,
			FOREIGN KEY (workspace_slug) REFERENCES workspaces(slug)
		)`)
	if err != nil {
		t.Fatalf("failed to create patches table: %v", err)
	}
}

// seedWorkspace inserts a workspace row for tests.
func seedWorkspace(t *testing.T, db *sql.DB, slug, ownerID, status, cloneStatus, mode, integrationBranch string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, integration_branch, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		slug, "https://github.com/example/repo", ownerID, status, cloneStatus, mode, integrationBranch, now, now,
	)
	if err != nil {
		t.Fatalf("seedWorkspace(%q) returned error: %v", slug, err)
	}
}

// seedPatch inserts a patch row for tests.
func seedPatch(t *testing.T, db *sql.DB, id, workspaceSlug, branchName string, position int, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, workspaceSlug, branchName, position, status, now, now,
	)
	if err != nil {
		t.Fatalf("seedPatch(%q) returned error: %v", id, err)
	}
}

// seedRebuildJob inserts a rebuild job row directly into the jobs table.
func seedRebuildJob(t *testing.T, db *sql.DB, id, status, workspaceSlug, strategy, submittedBy string) {
	t.Helper()
	payload := RebuildPayload{
		WorkspaceSlug: workspaceSlug,
		Strategy:      strategy,
		SubmittedBy:   submittedBy,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("seedRebuildJob: marshal payload: %v", err)
	}
	now := apikit.NowUTC()
	key := workspaceSlug
	groupKey := workspaceSlug + ":integration"

	_, err = db.Exec(
		`INSERT INTO jobs (id, type, key, group_key, nonce, status, payload, result, error, retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES (?, 'rebuild', ?, ?, ?, ?, ?, NULL, NULL, 0, ?, ?, ?, ?)`,
		id, key, groupKey, id, status, string(payloadJSON), now, submittedBy, now, now,
	)
	if err != nil {
		t.Fatalf("seedRebuildJob(%q) failed: %v", id, err)
	}
}

// ===========================================================================
// Mock GitRunner for unit tests
// ===========================================================================

// cherryPickCall records a CherryPick invocation.
type cherryPickCall struct {
	CommitSHA string
}

// mergeNoFFCall records a MergeNoFF invocation.
type mergeNoFFCall struct {
	Branch string
}

// runCall records a Run invocation.
type runCall struct {
	Args []string
}

// mergeTreeCall records a MergeTree invocation.
type mergeTreeCall struct {
	Base string
	Head string
}

// mockGitRunner is a recording mock for the GitRunner interface.
type mockGitRunner struct {
	mu sync.Mutex

	RunCalls        []runCall
	RunFunc         func(ctx context.Context, args ...string) (string, error)
	CherryPickCalls []cherryPickCall
	CherryPickFunc  func(ctx context.Context, commitSHA string) error
	MergeNoFFCalls  []mergeNoFFCall
	MergeNoFFFunc   func(ctx context.Context, branch string) error
	MergeTreeCalls  []mergeTreeCall
	MergeTreeFunc   func(ctx context.Context, base, head string) (string, error)
	IsAncestorFunc  func(ctx context.Context, ancestor, descendant string) (bool, error)
	CherryFunc      func(ctx context.Context, upstream, head string) ([]string, []string, error)
	HardResetFunc   func(ctx context.Context, ref string) error
}

func newMockGitRunner() *mockGitRunner {
	return &mockGitRunner{
		RunFunc: func(_ context.Context, args ...string) (string, error) {
			return "", nil
		},
		CherryPickFunc: func(_ context.Context, _ string) error {
			return nil
		},
		MergeNoFFFunc: func(_ context.Context, _ string) error {
			return nil
		},
		MergeTreeFunc: func(_ context.Context, _, _ string) (string, error) {
			return "aaaa", nil
		},
		IsAncestorFunc: func(_ context.Context, _, _ string) (bool, error) {
			return false, nil
		},
		CherryFunc: func(_ context.Context, _, _ string) ([]string, []string, error) {
			return nil, nil, nil
		},
		HardResetFunc: func(_ context.Context, _ string) error {
			return nil
		},
	}
}

func (m *mockGitRunner) Run(ctx context.Context, args ...string) (string, error) {
	m.mu.Lock()
	m.RunCalls = append(m.RunCalls, runCall{Args: args})
	m.mu.Unlock()
	return m.RunFunc(ctx, args...)
}

func (m *mockGitRunner) CherryPick(ctx context.Context, commitSHA string) error {
	m.mu.Lock()
	m.CherryPickCalls = append(m.CherryPickCalls, cherryPickCall{CommitSHA: commitSHA})
	m.mu.Unlock()
	return m.CherryPickFunc(ctx, commitSHA)
}

func (m *mockGitRunner) MergeNoFF(ctx context.Context, branch string) error {
	m.mu.Lock()
	m.MergeNoFFCalls = append(m.MergeNoFFCalls, mergeNoFFCall{Branch: branch})
	m.mu.Unlock()
	return m.MergeNoFFFunc(ctx, branch)
}

func (m *mockGitRunner) MergeTree(ctx context.Context, base, head string) (string, error) {
	m.mu.Lock()
	m.MergeTreeCalls = append(m.MergeTreeCalls, mergeTreeCall{Base: base, Head: head})
	m.mu.Unlock()
	return m.MergeTreeFunc(ctx, base, head)
}

func (m *mockGitRunner) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	return m.IsAncestorFunc(ctx, ancestor, descendant)
}

func (m *mockGitRunner) Cherry(ctx context.Context, upstream, head string) ([]string, []string, error) {
	return m.CherryFunc(ctx, upstream, head)
}

func (m *mockGitRunner) HardReset(ctx context.Context, ref string) error {
	return m.HardResetFunc(ctx, ref)
}

// ===========================================================================
// Mock PatchStore for unit tests
// ===========================================================================

// mockPatchStore is a recording mock for the PatchStore interface.
type mockPatchStore struct {
	mu             sync.Mutex
	Patches        []Patch
	UpdatedPatches map[string]Patch // id -> updated patch
	DeletedPatches []string
	Compacted      bool
}

func newMockPatchStore(patches []Patch) *mockPatchStore {
	return &mockPatchStore{
		Patches:        patches,
		UpdatedPatches: make(map[string]Patch),
	}
}

func (m *mockPatchStore) ListPatches(_ context.Context, _ string) ([]Patch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Patch, len(m.Patches))
	copy(result, m.Patches)
	return result, nil
}

func (m *mockPatchStore) UpdatePatchStatus(_ context.Context, patchID, status string, conflictFiles []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.UpdatedPatches[patchID] = Patch{
		ID:            patchID,
		Status:        status,
		ConflictFiles: conflictFiles,
	}
	return nil
}

func (m *mockPatchStore) DeletePatch(_ context.Context, patchID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DeletedPatches = append(m.DeletedPatches, patchID)
	return nil
}

func (m *mockPatchStore) CompactPositions(_ context.Context, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Compacted = true
	return nil
}

// ===========================================================================
// HTTP Test Environment
// ===========================================================================

// rebuildTestEnv holds a test HTTP server, database, and job queue for
// rebuild endpoint tests.
type rebuildTestEnv struct {
	echo  *echo.Echo
	db    *sql.DB
	queue *jobqueue.Queue
}

// newRebuildTestEnv creates an echo server with rebuild routes mounted for
// testing. Uses an in-memory SQLite database with workspace and patches tables.
func newRebuildTestEnv(t *testing.T) *rebuildTestEnv {
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
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress() returned error: %v", err)
	}

	logger := nopLogger()
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("jobqueue.New() returned error: %v", err)
	}

	// Register the rebuild handler so rebuild-type jobs can be enqueued.
	_ = RegisterRebuildJob(q, &RebuildHandler{})

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	cfg := RebuildAPIConfig{
		DB:    db,
		Queue: q,
		GetVariable: func(scope, slug, key string) (string, error) {
			// Default: return 'rebase' for REBUILD_STRATEGY.
			if key == "REBUILD_STRATEGY" {
				return "rebase", nil
			}
			return "", nil
		},
	}
	RegisterRebuildRoutes(api, cfg)

	return &rebuildTestEnv{
		echo:  e,
		db:    db,
		queue: q,
	}
}

// newRebuildTestEnvWithStrategy creates a test env that returns the specified
// strategy from GetVariable.
func newRebuildTestEnvWithStrategy(t *testing.T, strategy string) *rebuildTestEnv {
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
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress() returned error: %v", err)
	}

	logger := nopLogger()
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("jobqueue.New() returned error: %v", err)
	}

	_ = RegisterRebuildJob(q, &RebuildHandler{})

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	cfg := RebuildAPIConfig{
		DB:    db,
		Queue: q,
		GetVariable: func(scope, slug, key string) (string, error) {
			if key == "REBUILD_STRATEGY" {
				return strategy, nil
			}
			return "", nil
		},
	}
	RegisterRebuildRoutes(api, cfg)

	return &rebuildTestEnv{
		echo:  e,
		db:    db,
		queue: q,
	}
}

// rebuildTestAuthMiddleware returns middleware that reads apikit.AuthInfo from
// the X-Test-Auth JSON header and injects it via apikit.SetAuthInfo.
func rebuildTestAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("X-Test-Auth")
			if authHeader != "" {
				var info apikit.AuthInfo
				if err := json.Unmarshal([]byte(authHeader), &info); err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Test-Auth header")
				}
				apikit.SetAuthInfo(c, &info)
			}
			return next(c)
		}
	}
}

// doRequest performs an HTTP request against the rebuild test server.
func (env *rebuildTestEnv) doRequest(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if auth != nil {
		authJSON, err := json.Marshal(auth)
		if err != nil {
			t.Fatalf("failed to marshal auth info: %v", err)
		}
		req.Header.Set("X-Test-Auth", string(authJSON))
	}
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

// ===========================================================================
// Auth helpers
// ===========================================================================

func rebuildUserAuth(userID string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "api_key",
		UserID:         userID,
	}
}

func rebuildPATAuth(userID string, permissions ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         userID,
		Permissions:    permissions,
	}
}

// ===========================================================================
// Response parsing helpers
// ===========================================================================

// rebuildJobResponse is the JSON response body for a rebuild job.
type rebuildJobResponse struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Key      string          `json:"key"`
	GroupKey string          `json:"group_key"`
	Status   string          `json:"status"`
	Payload  json.RawMessage `json:"payload"`
}

// errorEnvelope represents the JSON error response envelope.
type errorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func parseRebuildJobResponse(t *testing.T, rec *httptest.ResponseRecorder) rebuildJobResponse {
	t.Helper()
	var resp rebuildJobResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode rebuild job response: %v (body: %s)", err, rec.Body.String())
	}
	return resp
}

func parseErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var resp errorEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v (body: %s)", err, rec.Body.String())
	}
	return resp
}

// ===========================================================================
// Git helpers for integration tests
// ===========================================================================

// runGitCmd executes a git command in the specified directory and returns
// trimmed stdout. It fails the test on non-zero exit.
func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s) failed: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// configGitUserCmd sets user.name and user.email in the given repo directory.
func configGitUserCmd(t *testing.T, dir string) {
	t.Helper()
	runGitCmd(t, dir, "config", "user.name", "Test User")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
}

// writeFileHelper creates or overwrites a file with the given content.
func writeFileHelper(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ===========================================================================
// Extended test environment for group 2 tests
// ===========================================================================

// fullTestEnv holds a test HTTP server with all carrypatch routes
// (rebuild, rerere, sync, patch-status) mounted.
type fullTestEnv struct {
	echo          *echo.Echo
	db            *sql.DB
	queue         *jobqueue.Queue
	workspaceRoot string
	gitRunner     *mockGitRunner
	patchStore    *mockPatchStore
	getVariable   GetVariableFunc
}

// newFullTestEnv creates an echo server with all carry-patch routes mounted.
func newFullTestEnv(t *testing.T) *fullTestEnv {
	t.Helper()

	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)
	addWorkspaceColumns(t, db)

	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey() returned error: %v", err)
	}
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress() returned error: %v", err)
	}

	logger := nopLogger()
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("jobqueue.New() returned error: %v", err)
	}
	_ = RegisterRebuildJob(q, &RebuildHandler{})

	workspaceRoot := t.TempDir()
	mock := newMockGitRunner()
	patches := newMockPatchStore(nil)

	getVar := func(scope, slug, key string) (string, error) {
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	rebuildCfg := RebuildAPIConfig{
		DB:          db,
		Queue:       q,
		GetVariable: getVar,
	}
	RegisterRebuildRoutes(api, rebuildCfg)

	rerereCfg := RerereAPIConfig{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
	}
	RegisterRerereRoutes(api, rerereCfg)

	syncCfg := SyncAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: workspaceRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
		Fetch:       func(_ context.Context, _ string) error { return nil },
		ResolveAuth: func(_ string) error { return nil },
		GetVariable: getVar,
		PatchStore:  patches,
	}
	RegisterSyncRoutes(api, syncCfg)

	rebuildPreviewCfg := RebuildPreviewAPIConfig{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
		PatchStore: patches,
	}
	RegisterRebuildPreviewRoutes(api, rebuildPreviewCfg)

	patchStatusCfg := PatchStatusAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: workspaceRoot,
		PatchStore:    patches,
	}
	RegisterPatchStatusRoutes(api, patchStatusCfg)

	rollbackCfg := RebuildRollbackAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: workspaceRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
	}
	RegisterRebuildRollbackRoutes(api, rollbackCfg)

	return &fullTestEnv{
		echo:          e,
		db:            db,
		queue:         q,
		workspaceRoot: workspaceRoot,
		gitRunner:     mock,
		patchStore:    patches,
		getVariable:   getVar,
	}
}

// newFullTestEnvWithGetVariable creates a full test env with custom GetVariable.
func newFullTestEnvWithGetVariable(t *testing.T, getVar GetVariableFunc) *fullTestEnv {
	t.Helper()

	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)
	addWorkspaceColumns(t, db)

	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey() returned error: %v", err)
	}
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress() returned error: %v", err)
	}

	logger := nopLogger()
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("jobqueue.New() returned error: %v", err)
	}
	_ = RegisterRebuildJob(q, &RebuildHandler{})

	workspaceRoot := t.TempDir()
	mock := newMockGitRunner()
	patches := newMockPatchStore(nil)

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	rebuildCfg := RebuildAPIConfig{
		DB:          db,
		Queue:       q,
		GetVariable: getVar,
	}
	RegisterRebuildRoutes(api, rebuildCfg)

	syncCfg := SyncAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: workspaceRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
		Fetch:       func(_ context.Context, _ string) error { return nil },
		ResolveAuth: func(_ string) error { return nil },
		GetVariable: getVar,
		PatchStore:  patches,
	}
	RegisterSyncRoutes(api, syncCfg)

	rebuildPreviewCfg := RebuildPreviewAPIConfig{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
		PatchStore: patches,
	}
	RegisterRebuildPreviewRoutes(api, rebuildPreviewCfg)

	patchStatusCfg := PatchStatusAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: workspaceRoot,
		PatchStore:    patches,
	}
	RegisterPatchStatusRoutes(api, patchStatusCfg)

	return &fullTestEnv{
		echo:          e,
		db:            db,
		queue:         q,
		workspaceRoot: workspaceRoot,
		gitRunner:     mock,
		patchStore:    patches,
		getVariable:   getVar,
	}
}

// doRequest performs an HTTP request against the full test server.
func (env *fullTestEnv) doRequest(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if auth != nil {
		authJSON, err := json.Marshal(auth)
		if err != nil {
			t.Fatalf("failed to marshal auth info: %v", err)
		}
		req.Header.Set("X-Test-Auth", string(authJSON))
	}
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

// addWorkspaceColumns adds carry-patch-related columns that may not be present
// in the minimal workspaces table from createWorkspacesTable.
func addWorkspaceColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	// upstream_url, upstream_head_sha, last_sync_at might not be in the
	// minimal workspaces table. Add them safely with IF NOT EXISTS semantics.
	columns := []struct {
		name     string
		typeDef  string
	}{
		{"upstream_url", "TEXT NOT NULL DEFAULT ''"},
		{"upstream_head_sha", "TEXT"},
		{"last_sync_at", "TEXT"},
	}
	for _, col := range columns {
		// Ignore errors — column may already exist.
		_, _ = db.Exec(fmt.Sprintf("ALTER TABLE workspaces ADD COLUMN %s %s", col.name, col.typeDef))
	}
}

// seedWorkspaceCarryPatch inserts a carry_patch workspace with all fields set.
func seedWorkspaceCarryPatch(t *testing.T, db *sql.DB, slug, ownerID, upstreamURL, upstreamHeadSHA, integrationBranch, _ string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, integration_branch, upstream_url, upstream_head_sha, last_sync_at, created_at, updated_at)
		 VALUES (?, ?, ?, 'active', 'ready', 'carry_patch', ?, ?, ?, ?, ?, ?)`,
		slug, "https://github.com/example/repo", ownerID, integrationBranch, upstreamURL, upstreamHeadSHA, now, now, now,
	)
	if err != nil {
		t.Fatalf("seedWorkspaceCarryPatch(%q) returned error: %v", slug, err)
	}
}

// seedRebuildJobWithResult inserts a rebuild job with status, result and created_at set.
func seedRebuildJobWithResult(t *testing.T, db *sql.DB, id, status, workspaceSlug, strategy string, createdAt time.Time, result json.RawMessage) {
	t.Helper()
	payload := RebuildPayload{
		WorkspaceSlug: workspaceSlug,
		Strategy:      strategy,
		SubmittedBy:   "operator",
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("seedRebuildJobWithResult: marshal payload: %v", err)
	}
	now := apikit.FormatUTC(createdAt)
	key := workspaceSlug
	groupKey := workspaceSlug + ":integration"

	var resultStr *string
	if result != nil {
		s := string(result)
		resultStr = &s
	}

	_, err = db.Exec(
		`INSERT INTO jobs (id, type, key, group_key, nonce, status, payload, result, error, retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES (?, 'rebuild', ?, ?, ?, ?, ?, ?, NULL, 0, ?, 'operator', ?, ?)`,
		id, key, groupKey, id, status, string(payloadJSON), resultStr, now, now, now,
	)
	if err != nil {
		t.Fatalf("seedRebuildJobWithResult(%q) failed: %v", id, err)
	}
}

// setupRRCacheDir creates a mock rr-cache directory structure for rerere tests.
func setupRRCacheDir(t *testing.T, workspaceRoot, slug string, entries []rrCacheEntry) string {
	t.Helper()
	gitDir := filepath.Join(workspaceRoot, slug, "trunk", ".git")
	rrCacheDir := filepath.Join(gitDir, "rr-cache")
	if err := os.MkdirAll(rrCacheDir, 0o755); err != nil {
		t.Fatalf("failed to create rr-cache dir: %v", err)
	}

	for _, entry := range entries {
		subdir := filepath.Join(rrCacheDir, entry.hash)
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatalf("failed to create rr-cache subdir: %v", err)
		}
		if entry.preimage != "" {
			writeFileHelper(t, filepath.Join(subdir, "preimage"), entry.preimage)
		}
		if entry.postimage != "" {
			writeFileHelper(t, filepath.Join(subdir, "postimage"), entry.postimage)
		}
	}
	return rrCacheDir
}

// rrCacheEntry describes a mock rr-cache subdirectory.
type rrCacheEntry struct {
	hash      string // directory name (hash)
	preimage  string // content for preimage file (empty = no file)
	postimage string // content for postimage file (empty = no file)
}

// setupCarryPatchRepo creates a git repository at <workspaceRoot>/<slug>/trunk/
// configured for carry-patch testing with:
//   - main: base commit + upstream change
//   - feature/patch-a: adds patch-a.txt (clean apply)
//   - feature/patch-b: adds patch-b.txt (clean apply)
//   - feature/conflict: modifies base.txt (conflicts with main)
//
// Returns the trunk directory path and the upstream HEAD SHA.
func setupCarryPatchRepo(t *testing.T, workspaceRoot, slug string) (trunkDir string, upstreamSHA string) {
	t.Helper()
	trunkDir = filepath.Join(workspaceRoot, slug, "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}

	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)

	// Base commit on main.
	writeFileHelper(t, filepath.Join(trunkDir, "base.txt"), "base content")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "base commit")

	// Create feature/patch-a: adds a new file (clean apply).
	runGitCmd(t, trunkDir, "checkout", "-b", "feature/patch-a")
	writeFileHelper(t, filepath.Join(trunkDir, "patch-a.txt"), "patch a content")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "patch a change")

	// Create feature/patch-b: adds another new file (clean apply).
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "checkout", "-b", "feature/patch-b")
	writeFileHelper(t, filepath.Join(trunkDir, "patch-b.txt"), "patch b content")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "patch b change")

	// Create feature/conflict: modifies base.txt differently (will conflict).
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "checkout", "-b", "feature/conflict")
	writeFileHelper(t, filepath.Join(trunkDir, "base.txt"), "conflict modification from feature")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "conflicting change")

	// Add a divergent change to main (creates conflict with feature/conflict).
	runGitCmd(t, trunkDir, "checkout", "main")
	writeFileHelper(t, filepath.Join(trunkDir, "base.txt"), "main upstream modification")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "upstream divergent change")

	upstreamSHA = runGitCmd(t, trunkDir, "rev-parse", "HEAD")

	// Create an integration branch at the current main HEAD.
	runGitCmd(t, trunkDir, "branch", "integration")

	return trunkDir, upstreamSHA
}
