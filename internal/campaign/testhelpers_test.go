package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database for test isolation.
// It calls InitSchema to create the campaigns and campaign_specs tables.
// SQLite in-memory databases are per-connection, so the pool is capped
// at one connection to ensure all operations share the same database.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	// Enable foreign key enforcement for SQLite.
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("failed to enable foreign keys: %v", err)
	}

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	return db
}

// columnInfo holds metadata for a single table column from PRAGMA table_info.
type columnInfo struct {
	Name    string
	Type    string
	NotNull int
	PK      int
}

// queryTableInfo returns column metadata for the given table using PRAGMA table_info.
func queryTableInfo(t *testing.T, db *sql.DB, tableName string) []columnInfo {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info('%s')", tableName))
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s) failed: %v", tableName, err)
	}
	defer rows.Close()

	var cols []columnInfo
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dfltValue *string
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan column info: %v", err)
		}
		cols = append(cols, columnInfo{Name: name, Type: typ, NotNull: notnull, PK: pk})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}
	return cols
}

// findColumn searches for a column by name in the column list.
func findColumn(cols []columnInfo, name string) (columnInfo, bool) {
	for _, c := range cols {
		if c.Name == name {
			return c, true
		}
	}
	return columnInfo{}, false
}

// seedCampaign inserts a campaign row directly into the database for test setup.
func seedCampaign(t *testing.T, db *sql.DB, id, workspaceSlug, name, integrationBranch, status, dagJSON, createdBy string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO campaigns (id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, workspaceSlug, name, integrationBranch, status, dagJSON, createdBy, now, now,
	)
	if err != nil {
		t.Fatalf("seedCampaign(%q, %q) returned error: %v", id, name, err)
	}
}

// seedCampaignSpec inserts a campaign_specs row directly into the database for test setup.
func seedCampaignSpec(t *testing.T, db *sql.DB, campaignID, specID, status, branchName, branchSHA string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO campaign_specs (campaign_id, spec_id, status, branch_name, branch_sha, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		campaignID, specID, status, branchName, branchSHA, now,
	)
	if err != nil {
		t.Fatalf("seedCampaignSpec(%q, %q) returned error: %v", campaignID, specID, err)
	}
}

// handlerTestEnv holds a test HTTP server and database for integration tests.
type handlerTestEnv struct {
	echo    *echo.Echo
	db      *sql.DB
	handler *Handler
}

// newHandlerTestEnv creates an echo server with campaign routes mounted for testing.
// The handler is accessible via env.handler for injecting test dependencies
// (e.g., gitOps, workspaceRoot).
func newHandlerTestEnv(t *testing.T) *handlerTestEnv {
	t.Helper()
	db := openTestDB(t)

	e := echo.New()
	api := e.Group("/api/v1")

	// Apply test auth middleware.
	api.Use(testAuthMiddleware())

	// Create handler directly so tests can access it.
	h := NewHandler(db)

	// Register all campaign routes.
	campaigns := api.Group("/workspaces/:slug/campaigns")
	campaigns.POST("", h.createCampaign)
	campaigns.GET("", h.listCampaigns)
	campaigns.GET("/:id", h.getCampaign)
	campaigns.DELETE("/:id", h.cancelCampaign)
	campaigns.POST("/:id/specs/:spec_id/resolve", h.resolveSpec)

	return &handlerTestEnv{echo: e, db: db, handler: h}
}

// testAuthMiddleware returns middleware that reads apikit.AuthInfo from the
// X-Test-Auth JSON header and injects it via apikit.SetAuthInfo.
func testAuthMiddleware() echo.MiddlewareFunc {
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

// doRequest performs an HTTP request against the test server with optional auth.
func (env *handlerTestEnv) doRequest(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
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

// adminAuth returns an apikit.AuthInfo representing an admin token.
func adminAuth() *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "admin_token",
	}
}

// userAuth returns an apikit.AuthInfo representing a user API key.
func userAuth(userID string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "api_key",
		UserID:         userID,
	}
}

// patAuth returns an apikit.AuthInfo representing a PAT with the given permission scopes.
func patAuth(userID string, permissions ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         userID,
		Permissions:    permissions,
	}
}

// parseRawJSON parses the response body as a generic map for field-presence checks.
func parseRawJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	return resp
}

// mockTasksReader implements TasksReader for tests.
type mockTasksReader struct {
	tasks map[string]*TasksJSON
	errs  map[string]error
}

func newMockTasksReader() *mockTasksReader {
	return &mockTasksReader{
		tasks: make(map[string]*TasksJSON),
		errs:  make(map[string]error),
	}
}

func (m *mockTasksReader) ReadTasksJSON(_ context.Context, specID string) (*TasksJSON, error) {
	if err, ok := m.errs[specID]; ok {
		return nil, err
	}
	if tj, ok := m.tasks[specID]; ok {
		return tj, nil
	}
	return nil, fmt.Errorf("no tasks.json for spec %s", specID)
}

// seedCampaignSpecFull inserts a campaign_specs row with all fields including
// conflict_details and blocked_by_merge for test setup.
func seedCampaignSpecFull(t *testing.T, db *sql.DB, campaignID, specID, status, branchName, branchSHA, conflictDetails, blockedByMerge string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO campaign_specs (campaign_id, spec_id, status, branch_name, branch_sha, conflict_details, blocked_by_merge, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		campaignID, specID, status, branchName, branchSHA, conflictDetails, blockedByMerge, now,
	)
	if err != nil {
		t.Fatalf("seedCampaignSpecFull(%q, %q) returned error: %v", campaignID, specID, err)
	}
}

// mockGitOps implements GitOps for tests, tracking calls and optionally
// failing on a specific create call.
type mockGitOps struct {
	createCalls     []string              // branch names passed to CreateBranch
	deleteCalls     []string              // branch names passed to DeleteBranch
	failOnNth       int                   // fail on the Nth CreateBranch call (1-indexed); 0 = never fail
	callCount       int                   // internal counter for CreateBranch calls
	defaultSHA      string                // SHA returned for successful CreateBranch/ResolveRef
	rebaseCalls     []string              // branch names passed to Rebase, in call order
	rebaseConflicts map[string][]string   // branch → conflict file paths; non-nil entry = conflict
	rebaseErrors    map[string]error       // branch → error to return from Rebase
	rebaseSHA       string                // SHA returned for clean rebases (defaults to defaultSHA)
}

func newMockGitOps() *mockGitOps {
	return &mockGitOps{
		defaultSHA: "abcdef1234567890abcdef1234567890abcdef12",
	}
}

func (m *mockGitOps) CreateBranch(_ context.Context, _, branchName, _ string) (string, error) {
	m.callCount++
	m.createCalls = append(m.createCalls, branchName)
	if m.failOnNth > 0 && m.callCount == m.failOnNth {
		return "", fmt.Errorf("mock git error: failed to create branch %s", branchName)
	}
	return m.defaultSHA, nil
}

func (m *mockGitOps) DeleteBranch(_ context.Context, _, branchName string) error {
	m.deleteCalls = append(m.deleteCalls, branchName)
	return nil
}

func (m *mockGitOps) BranchExists(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}

func (m *mockGitOps) ResolveRef(_ context.Context, _, _ string) (string, error) {
	return m.defaultSHA, nil
}

func (m *mockGitOps) Rebase(_ context.Context, _, branchName, _ string) (string, []string, error) {
	m.rebaseCalls = append(m.rebaseCalls, branchName)
	if m.rebaseErrors != nil {
		if err, ok := m.rebaseErrors[branchName]; ok {
			return "", nil, err
		}
	}
	if m.rebaseConflicts != nil {
		if conflicts, ok := m.rebaseConflicts[branchName]; ok && len(conflicts) > 0 {
			return "", conflicts, nil
		}
	}
	sha := m.rebaseSHA
	if sha == "" {
		sha = m.defaultSHA
	}
	return sha, nil, nil
}

// setupWorkspaceDir creates a temporary workspace directory structure with
// spec directories containing tasks.json files. Returns the workspace root path.
//
// specTasks maps spec directory names (e.g., "07_secrets_variables") to their
// tasks.json content as raw JSON strings. Pass nil to omit tasks.json for a spec.
func setupWorkspaceDir(t *testing.T, slug string, specTasks map[string]*string) string {
	t.Helper()
	root := t.TempDir()

	specsDir := fmt.Sprintf("%s/%s/trunk/.agent-fox/specs", root, slug)

	for specDir, content := range specTasks {
		dir := fmt.Sprintf("%s/%s", specsDir, specDir)
		if err := mkdirAll(dir); err != nil {
			t.Fatalf("failed to create spec dir %s: %v", dir, err)
		}
		if content != nil {
			path := fmt.Sprintf("%s/tasks.json", dir)
			if err := writeFile(path, *content); err != nil {
				t.Fatalf("failed to write tasks.json for %s: %v", specDir, err)
			}
		}
	}

	return root
}

// mkdirAll creates a directory and all parents. Test helper wrapping os.MkdirAll.
func mkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}

// writeFile writes content to a file. Test helper wrapping os.WriteFile.
func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// strPtr returns a pointer to s. Useful for optional string arguments in test helpers.
func strPtr(s string) *string {
	return &s
}

// parseJSONArray parses the response body as a JSON array for list endpoint tests.
func parseJSONArray(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var resp []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode JSON array response: %v", err)
	}
	return resp
}

// readAuth returns an apikit.AuthInfo for a PAT with campaigns:read scope.
func readAuth(userID string) *apikit.AuthInfo {
	return patAuth(userID, "campaigns:read")
}

// readWriteAuth returns an apikit.AuthInfo for a PAT with both read and write scopes.
func readWriteAuth(userID string) *apikit.AuthInfo {
	return patAuth(userID, "campaigns:read", "campaigns:write")
}
