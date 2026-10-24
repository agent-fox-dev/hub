package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// The helpers of this file serve the tests of the patch routes, which used to
// run inside the hub's workspace package. The hooks that package held as
// package state (the branch resolver, the recovery service, the audit
// emitter) are test variables here, read when a request is served, so a test
// changes them the way it used to and the next request sees the change.

// RecoveryHook is the name the patch route tests use for the module's
// Recoverer.
type RecoveryHook = Recoverer

var (
	branchCheckHook     BranchResolverFunc
	recoveryHook        Recoverer
	defaultAuditEmitter Emitter
)

// RegisterBranchCheckHook sets the resolver the test env mounts.
func RegisterBranchCheckHook(fn BranchResolverFunc) { branchCheckHook = fn }

// RegisterRecoveryHook sets the recovery service the test env mounts.
func RegisterRecoveryHook(h Recoverer) { recoveryHook = h }

// stubRecoveryHook is a Recoverer with canned answers.
type stubRecoveryHook struct {
	readSHA   string
	readFound bool
	readErr   error

	removeErr error

	resetResult ResetResult
	resetErr    error
}

func (s *stubRecoveryHook) ReadReplacedSHA(_ context.Context, _, _ string) (string, bool, error) {
	return s.readSHA, s.readFound, s.readErr
}

func (s *stubRecoveryHook) RemoveBackup(_ context.Context, _, _ string) error {
	return s.removeErr
}

func (s *stubRecoveryHook) RunReset(_ context.Context, _ string, _ ResetPatchInfo, _ *apikit.AuthInfo) (ResetResult, error) {
	return s.resetResult, s.resetErr
}

// mockAuditEmitter records emitted events for assertions.
type mockAuditEmitter struct {
	mu     sync.Mutex
	events []Event
}

func newMockAuditEmitter() *mockAuditEmitter { return &mockAuditEmitter{} }

func (m *mockAuditEmitter) Emit(_ context.Context, event Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockAuditEmitter) Events() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Event, len(m.events))
	copy(result, m.events)
	return result
}

// testEnv is a database with the workspace schema; each request is served by
// a server mounted with the module's routes over the hooks set at that moment.
type testEnv struct {
	db      *sql.DB
	emitter *mockAuditEmitter // when set, the routes' audit emitter
}

// newTestEnv returns an env with no audit emitter of its own: the routes emit
// through defaultAuditEmitter.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return &testEnv{db: workspaceTestDB(t)}
}

// auditTestEnv is a testEnv whose routes emit into a recording emitter.
type auditTestEnv struct {
	*testEnv
	emitter *mockAuditEmitter
}

func newAuditTestEnv(t *testing.T) *auditTestEnv {
	t.Helper()
	emitter := newMockAuditEmitter()
	return &auditTestEnv{testEnv: &testEnv{db: workspaceTestDB(t), emitter: emitter}, emitter: emitter}
}

// server mounts the module's routes over the current hooks.
func (env *testEnv) server() *echo.Echo {
	cfg := APIConfig{
		DB:            env.db,
		PatchStore:    NewSQLPatchStore(env.db),
		ResolveBranch: branchCheckHook,
		Recovery:      recoveryHook,
		Audit:         defaultAuditEmitter,
	}
	if env.emitter != nil {
		cfg.Audit = env.emitter
	}
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())
	RegisterRoutes(api, cfg)
	return e
}

// doRequest performs an HTTP request with optional auth.
func (env *testEnv) doRequest(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
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
	env.server().ServeHTTP(rec, req)
	return rec
}

// workspaceTestDB is an in-memory database with the workspaces and patches
// tables as the hub creates them, and the apikit orgs tables the access rule
// reads.
func workspaceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)
	for _, stmt := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS patches_branch ON patches(workspace_slug, branch_name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS patches_position ON patches(workspace_slug, position)`,
		`CREATE TABLE IF NOT EXISTS orgs (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			url TEXT,
			owner_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS org_members (
			org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (org_id, user_id)
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("workspace test schema: %v", err)
		}
	}
	return db
}

// ensureCarryPatchColumns adds the carry-patch columns to a workspaces table
// that predates them. Errors are ignored: the column may already exist.
func ensureCarryPatchColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ddl := range []string{
		`ALTER TABLE workspaces ADD COLUMN workspace_mode TEXT NOT NULL DEFAULT 'standard'`,
		`ALTER TABLE workspaces ADD COLUMN upstream_url TEXT`,
		`ALTER TABLE workspaces ADD COLUMN integration_branch TEXT`,
	} {
		_, _ = db.Exec(ddl)
	}
}

// seedCarryPatchWorkspaceRaw inserts a carry_patch workspace owned by user-1
// with a pending clone (status 'pending').
func seedCarryPatchWorkspaceRaw(t *testing.T, db *sql.DB, slug, gitURL, upstreamURL, integrationBranch string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description,
		 created_at, updated_at, workspace_mode, upstream_url, integration_branch)
		 VALUES (?, ?, 'user-1', 'pending', '', '', ?, ?, 'carry_patch', ?, ?)`,
		slug, gitURL, now, now, upstreamURL, integrationBranch,
	)
	if err != nil {
		t.Fatalf("seed carry_patch workspace %q: %v", slug, err)
	}
}

// seedOwnedStandardWorkspace inserts a ready standard-mode workspace with
// the given owner.
func (env *testEnv) seedOwnedStandardWorkspace(t *testing.T, slug, gitURL, ownerID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := env.db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, created_at, updated_at, workspace_mode)
		 VALUES (?, ?, ?, 'ready', ?, ?, 'standard')`,
		slug, gitURL, ownerID, now, now,
	)
	if err != nil {
		t.Fatalf("seed standard workspace %q: %v", slug, err)
	}
}

// userAuth is the apikit.AuthInfo of a user API key.
func userAuth(userID string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "api_key", UserID: userID}
}

// adminAuth is the apikit.AuthInfo of the admin token.
func adminAuth() *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "admin_token"}
}

// patAuth is the apikit.AuthInfo of a PAT with the given permission scopes.
func patAuth(userID string, permissions ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "pat", UserID: userID, Permissions: permissions}
}
