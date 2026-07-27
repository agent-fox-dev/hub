package gitserver

import (
	"database/sql"
	"encoding/base64"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database for test isolation.
// It limits the pool to a single connection because SQLite :memory: databases
// are per-connection — without this, concurrent goroutines would get separate
// in-memory databases each missing the schema.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	// Create the workspaces table schema.
	workspaceSQL := `CREATE TABLE IF NOT EXISTS workspaces (
		slug         TEXT PRIMARY KEY,
		git_url      TEXT NOT NULL,
		branch       TEXT,
		owner_id     TEXT NOT NULL,
		org_id       TEXT,
		status       TEXT NOT NULL DEFAULT 'active',
		display_name TEXT NOT NULL DEFAULT '',
		description  TEXT NOT NULL DEFAULT '',
		created_at   TEXT NOT NULL,
		updated_at   TEXT NOT NULL
	)`
	if _, err := db.Exec(workspaceSQL); err != nil {
		t.Fatalf("failed to create workspaces table: %v", err)
	}

	// Create the orgs and org_members tables for org-related lookups.
	orgSchemaSQL := []string{
		`CREATE TABLE IF NOT EXISTS orgs (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			url TEXT,
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
	}
	for _, stmt := range orgSchemaSQL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create org schema: %v", err)
		}
	}

	return db
}

// gitTestEnv holds a test HTTP server with git routes mounted.
type gitTestEnv struct {
	echo *echo.Echo
	db   *sql.DB
}

// newGitTestEnv creates an echo server with git routes mounted for testing.
func newGitTestEnv(t *testing.T) *gitTestEnv {
	t.Helper()
	db := openTestDB(t)
	e := echo.New()

	if err := MountGitHandlers(e, db); err != nil {
		t.Fatalf("MountGitHandlers() returned error: %v", err)
	}

	return &gitTestEnv{echo: e, db: db}
}

// doRequest performs an HTTP request against the test server.
func (env *gitTestEnv) doRequest(t *testing.T, method, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

// basicAuthHeader returns the value for an Authorization: Basic header
// using the given username and password.
func basicAuthHeader(username, password string) string {
	cred := username + ":" + password
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(cred))
}

// seedOrg inserts an organization into the orgs table for test setup.
func (env *gitTestEnv) seedOrg(t *testing.T, orgID, name, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := env.db.Exec(
		`INSERT INTO orgs (id, name, slug, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
		orgID, name, slug, now, now,
	)
	if err != nil {
		t.Fatalf("seedOrg(%q) returned error: %v", orgID, err)
	}
}

// seedOrgMember adds a user as a member of an organization.
func (env *gitTestEnv) seedOrgMember(t *testing.T, orgID, userID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := env.db.Exec(
		`INSERT INTO org_members (org_id, user_id, created_at) VALUES (?, ?, ?)`,
		orgID, userID, now,
	)
	if err != nil {
		t.Fatalf("seedOrgMember(%q, %q) returned error: %v", orgID, userID, err)
	}
}

// seedWorkspace inserts a workspace directly into the database for test setup.
func (env *gitTestEnv) seedWorkspace(t *testing.T, slug, gitURL, ownerID, orgID, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := env.db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		slug, gitURL, ownerID, orgID, status, now, now,
	)
	if err != nil {
		t.Fatalf("seedWorkspace(%q) returned error: %v", slug, err)
	}
}

// noHeaders returns an empty header map (no Authorization).
func noHeaders() map[string]string {
	return map[string]string{}
}

// withBasicAuth returns a header map with an Authorization: Basic header.
func withBasicAuth(username, password string) map[string]string {
	return map[string]string{
		"Authorization": basicAuthHeader(username, password),
	}
}
