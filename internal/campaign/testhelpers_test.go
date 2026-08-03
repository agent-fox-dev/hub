package campaign

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	echo *echo.Echo
	db   *sql.DB
}

// newHandlerTestEnv creates an echo server with campaign routes mounted for testing.
func newHandlerTestEnv(t *testing.T) *handlerTestEnv {
	t.Helper()
	db := openTestDB(t)

	e := echo.New()
	api := e.Group("/api/v1")

	// Apply test auth middleware.
	api.Use(testAuthMiddleware())

	// Register campaign routes.
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatalf("RegisterRoutes() returned error: %v", err)
	}

	return &handlerTestEnv{echo: e, db: db}
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
