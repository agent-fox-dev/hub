package merge

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// Test Environment for merge REST API tests
// ===========================================================================

// mergeTestEnv holds a test HTTP server, database, and job queue for
// merge endpoint integration tests.
type mergeTestEnv struct {
	echo  *echo.Echo
	db    *sql.DB
	queue *jobqueue.Queue
}

// newMergeTestEnv creates an echo server with merge routes mounted for
// testing. Uses an in-memory SQLite database initialised with the workspace
// and jobqueue schemas. A BranchChecker stub is injected that checks the
// branchRegistry map for branch existence.
func newMergeTestEnv(t *testing.T, branchRegistry map[string]bool) *mergeTestEnv {
	t.Helper()

	db := openTestDB(t)

	// Create a minimal workspaces table for merge handler workspace lookups.
	createWorkspacesTable(t, db)

	// Initialise the jobqueue schema.
	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey() returned error: %v", err)
	}

	logger := nopLogger()
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("jobqueue.New() returned error: %v", err)
	}

	// Register the merge handler so merge-type jobs can be enqueued.
	// Note: this will fail until RegisterHandler is implemented (task group 8).
	_ = RegisterHandler(q)

	e := echo.New()
	api := e.Group("/api/v1")

	// Apply test auth middleware.
	api.Use(mergeTestAuthMiddleware())

	cfg := MergeAPIConfig{
		DB:    db,
		Queue: q,
		BranchExists: func(slug, branch string) (bool, error) {
			key := slug + ":" + branch
			exists, ok := branchRegistry[key]
			if !ok {
				return false, nil
			}
			return exists, nil
		},
	}
	RegisterMergeRoutes(api, cfg)

	return &mergeTestEnv{
		echo:  e,
		db:    db,
		queue: q,
	}
}

// createWorkspacesTable creates the workspaces table DDL in the test database.
// This is a minimal copy of the workspace schema used by merge tests; it
// avoids importing the unexported workspace.initSchema function.
func createWorkspacesTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS workspaces (
			slug         TEXT PRIMARY KEY,
			git_url      TEXT NOT NULL,
			branch       TEXT,
			owner_id     TEXT NOT NULL,
			org_id       TEXT,
			status       TEXT NOT NULL DEFAULT 'active',
			display_name TEXT NOT NULL DEFAULT '',
			description  TEXT NOT NULL DEFAULT '',
			clone_status TEXT NOT NULL DEFAULT 'pending' CHECK(clone_status IN ('pending','cloning','ready','failed','archived')),
			head_sha     TEXT,
			clone_error  TEXT,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		)`)
	if err != nil {
		t.Fatalf("failed to create workspaces table: %v", err)
	}
}

// seedTestWorkspace inserts a workspace row directly into the database.
func seedTestWorkspace(t *testing.T, db *sql.DB, slug, ownerID, status, cloneStatus string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		slug, "https://github.com/example/repo", ownerID, status, cloneStatus, now, now,
	)
	if err != nil {
		t.Fatalf("seedTestWorkspace(%q) returned error: %v", slug, err)
	}
}

// mergeTestAuthMiddleware returns middleware that reads apikit.AuthInfo from
// the X-Test-Auth JSON header and injects it via apikit.SetAuthInfo.
func mergeTestAuthMiddleware() echo.MiddlewareFunc {
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

// doMergeRequest performs an HTTP request against the merge test server.
func (env *mergeTestEnv) doRequest(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
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

// nopLogger returns a *slog.Logger that discards all output.
func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mergeAuth helpers for test readability.

func mergeUserAuth(userID string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "api_key",
		UserID:         userID,
	}
}

func mergePATAuth(userID string, permissions ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         userID,
		Permissions:    permissions,
	}
}

// mergeJobResponse is the JSON response body expected when a merge job is
// created or retrieved. Fields mirror the test specification TS-12-28.
type mergeJobResponse struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	WorkspaceSlug string `json:"workspace_slug"`
	TargetBranch  string `json:"target_branch"`
	SourceRef     string `json:"source_ref"`
	SubmittedBy   string `json:"submitted_by"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// mergeErrorEnvelope represents the JSON error response envelope.
type mergeErrorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// parseMergeErrorEnvelope parses the response body as a JSON error envelope.
func parseMergeErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) mergeErrorEnvelope {
	t.Helper()
	var resp mergeErrorEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v (body: %s)", err, rec.Body.String())
	}
	return resp
}

// parseMergeJobResponse parses the response body as a merge job response.
func parseMergeJobResponse(t *testing.T, rec *httptest.ResponseRecorder) mergeJobResponse {
	t.Helper()
	var resp mergeJobResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode merge job response: %v (body: %s)", err, rec.Body.String())
	}
	return resp
}

// ===========================================================================
// TS-12-28: POST /api/v1/workspaces/:slug/merges with valid target_branch
// and source_ref enqueues a merge job and returns HTTP 202 with the queued
// job record.
//
// Requirement: 12-REQ-9.1
// ===========================================================================

func TestSubmitMerge_ValidRequest_Returns202(t *testing.T) {
	branchRegistry := map[string]bool{
		"ws1:main":      true,
		"ws1:feature/a": true,
	}
	env := newMergeTestEnv(t, branchRegistry)

	// Seed an active workspace with clone_status=ready.
	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	body := `{"target_branch":"main","source_ref":"feature/a"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges", body, mergeUserAuth("alice"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /merges status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	resp := parseMergeJobResponse(t, rec)

	// Verify the response contains the expected fields.
	if resp.ID == "" {
		t.Error("expected non-empty job ID in response")
	}
	if resp.Status != "queued" {
		t.Errorf("expected status='queued', got %q", resp.Status)
	}
	if resp.WorkspaceSlug != "ws1" {
		t.Errorf("expected workspace_slug='ws1', got %q", resp.WorkspaceSlug)
	}
	if resp.TargetBranch != "main" {
		t.Errorf("expected target_branch='main', got %q", resp.TargetBranch)
	}
	if resp.SourceRef != "feature/a" {
		t.Errorf("expected source_ref='feature/a', got %q", resp.SourceRef)
	}
	if resp.SubmittedBy == "" {
		t.Error("expected non-empty submitted_by in response")
	}
}

// ===========================================================================
// TS-12-29: Submitting a duplicate merge job for the same source_ref and
// target_branch in the same workspace returns HTTP 409.
//
// Requirement: 12-REQ-9.2
// ===========================================================================

func TestSubmitMerge_Duplicate_Returns409(t *testing.T) {
	branchRegistry := map[string]bool{
		"ws1:main":      true,
		"ws1:feature/a": true,
	}
	env := newMergeTestEnv(t, branchRegistry)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	body := `{"target_branch":"main","source_ref":"feature/a"}`
	auth := mergeUserAuth("alice")

	// First submission should succeed.
	rec1 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges", body, auth)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first POST /merges status = %d; want %d; body = %s",
			rec1.Code, http.StatusAccepted, rec1.Body.String())
	}

	// Second submission with the same source_ref and target_branch should return 409.
	rec2 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges", body, auth)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("duplicate POST /merges status = %d; want %d; body = %s",
			rec2.Code, http.StatusConflict, rec2.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec2)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for duplicate merge submission")
	}
}

// ===========================================================================
// TS-12-30: POST /api/v1/workspaces/:slug/merges with missing target_branch
// or source_ref returns HTTP 400 indicating which field is missing.
//
// Requirement: 12-REQ-9.3
// ===========================================================================

func TestSubmitMerge_MissingSourceRef_Returns400(t *testing.T) {
	branchRegistry := map[string]bool{
		"ws1:main": true,
	}
	env := newMergeTestEnv(t, branchRegistry)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	// Request with target_branch but missing source_ref.
	body := `{"target_branch":"main"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges", body, mergeUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /merges (missing source_ref) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if !strings.Contains(strings.ToLower(resp.Error.Message), "source_ref") {
		t.Errorf("expected error message to mention 'source_ref', got %q", resp.Error.Message)
	}
}

func TestSubmitMerge_MissingTargetBranch_Returns400(t *testing.T) {
	branchRegistry := map[string]bool{
		"ws1:feature/a": true,
	}
	env := newMergeTestEnv(t, branchRegistry)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	// Request with source_ref but missing target_branch.
	body := `{"source_ref":"feature/a"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges", body, mergeUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /merges (missing target_branch) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if !strings.Contains(strings.ToLower(resp.Error.Message), "target_branch") {
		t.Errorf("expected error message to mention 'target_branch', got %q", resp.Error.Message)
	}
}

// ===========================================================================
// TS-12-31: POST /api/v1/workspaces/:slug/merges for a non-existent
// workspace returns HTTP 404.
//
// Requirement: 12-REQ-9.4
// ===========================================================================

func TestSubmitMerge_WorkspaceNotFound_Returns404(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	// No workspace seeded — "nonexistent" does not exist.
	body := `{"target_branch":"main","source_ref":"feature/a"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/nonexistent/merges", body, mergeUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /merges (nonexistent workspace) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for workspace not found")
	}
}

// ===========================================================================
// TS-12-32: POST /api/v1/workspaces/:slug/merges for a workspace that is
// not active returns HTTP 400 indicating workspace is not active.
//
// Requirement: 12-REQ-9.5
// ===========================================================================

func TestSubmitMerge_WorkspaceNotActive_Returns400(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	// Seed an archived workspace.
	seedTestWorkspace(t, env.db, "ws1", "alice", "archived", "archived")

	body := `{"target_branch":"main","source_ref":"feature/a"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges", body, mergeUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /merges (archived workspace) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if !strings.Contains(strings.ToLower(resp.Error.Message), "not active") {
		t.Errorf("expected error message to mention 'not active', got %q", resp.Error.Message)
	}
}

// ===========================================================================
// TS-12-33: POST /api/v1/workspaces/:slug/merges when the workspace clone
// is not ready returns HTTP 400 indicating clone is not ready.
//
// Requirement: 12-REQ-9.6
// ===========================================================================

func TestSubmitMerge_CloneNotReady_Returns400(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	// Seed an active workspace with clone_status='cloning' (not ready).
	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "cloning")

	body := `{"target_branch":"main","source_ref":"feature/a"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges", body, mergeUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /merges (clone not ready) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if !strings.Contains(strings.ToLower(resp.Error.Message), "clone") {
		t.Errorf("expected error message to mention 'clone', got %q", resp.Error.Message)
	}
}

// ===========================================================================
// TS-12-34: POST /api/v1/workspaces/:slug/merges when source or target
// branch does not exist in the workspace repository returns HTTP 400
// identifying the missing branch.
//
// Requirement: 12-REQ-9.7
// ===========================================================================

func TestSubmitMerge_BranchNotFound_Returns400(t *testing.T) {
	branchRegistry := map[string]bool{
		"ws1:main": true,
		// feature/nonexistent is NOT registered.
	}
	env := newMergeTestEnv(t, branchRegistry)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	body := `{"target_branch":"main","source_ref":"feature/nonexistent"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges", body, mergeUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /merges (branch not found) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if !strings.Contains(resp.Error.Message, "feature/nonexistent") {
		t.Errorf("expected error message to mention 'feature/nonexistent', got %q", resp.Error.Message)
	}
}

func TestSubmitMerge_TargetBranchNotFound_Returns400(t *testing.T) {
	branchRegistry := map[string]bool{
		"ws1:feature/a": true,
		// "main" is NOT registered — target branch does not exist.
	}
	env := newMergeTestEnv(t, branchRegistry)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	body := `{"target_branch":"main","source_ref":"feature/a"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges", body, mergeUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /merges (target branch not found) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if !strings.Contains(resp.Error.Message, "main") {
		t.Errorf("expected error message to mention 'main', got %q", resp.Error.Message)
	}
}

// ===========================================================================
// TS-12-35: POST /api/v1/workspaces/:slug/merges called by a PAT without
// merges:write scope returns HTTP 403.
//
// Requirement: 12-REQ-9.8
// ===========================================================================

func TestSubmitMerge_PATWithoutScope_Returns403(t *testing.T) {
	branchRegistry := map[string]bool{
		"ws1:main":      true,
		"ws1:feature/a": true,
	}
	env := newMergeTestEnv(t, branchRegistry)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	body := `{"target_branch":"main","source_ref":"feature/a"}`

	// PAT with only merges:read scope (no merges:write).
	auth := mergePATAuth("alice", "merges:read")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges", body, auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /merges (PAT without write scope) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for missing permission scope")
	}
}

// ===========================================================================
// TS-12-9.E1: If the request body is malformed JSON, the merge REST API
// returns HTTP 400.
//
// Requirement: 12-REQ-9.E1
// ===========================================================================

func TestSubmitMerge_MalformedJSON_Returns400(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	body := `{invalid json`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges", body, mergeUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /merges (malformed JSON) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
