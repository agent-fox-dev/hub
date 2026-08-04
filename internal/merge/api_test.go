package merge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

// ===========================================================================
// Helpers for task group 5 tests
// ===========================================================================

// mergeJobFullResponse is the JSON response body expected when retrieving
// a merge job, including all projection fields from 12-REQ-14.
type mergeJobFullResponse struct {
	ID            string   `json:"id"`
	WorkspaceSlug string   `json:"workspace_slug"`
	TargetBranch  string   `json:"target_branch"`
	SourceRef     string   `json:"source_ref"`
	Status        string   `json:"status"`
	BaseSHA       *string  `json:"base_sha"`
	MergedSHA     *string  `json:"merged_sha"`
	ConflictFiles []string `json:"conflict_files"`
	CheckOutput   *string  `json:"check_output"`
	Error         *string  `json:"error"`
	RetryCount    int      `json:"retry_count"`
	SubmittedBy   string   `json:"submitted_by"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

// batchRebaseTestResponse is the JSON response body for POST /rebase.
type batchRebaseTestResponse struct {
	Results []RebaseResult `json:"results"`
}

// seedMergeJob inserts a merge job row directly into the jobs table for
// test preconditions. This bypasses the job queue's Enqueue function so
// tests don't depend on merge handler registration being implemented.
func seedMergeJob(t *testing.T, db *sql.DB, id, status, workspaceSlug, targetBranch, sourceRef, submittedBy string, result, jobError *string) {
	t.Helper()
	payload := fmt.Sprintf(
		`{"workspace_slug":%q,"target_branch":%q,"source_ref":%q,"submitted_by":%q}`,
		workspaceSlug, targetBranch, sourceRef, submittedBy,
	)
	nowStr := apikit.NowUTC()
	key := workspaceSlug + ":" + targetBranch + ":" + sourceRef
	groupKey := workspaceSlug + ":" + targetBranch

	var resultVal, errorVal any
	if result != nil {
		resultVal = *result
	}
	if jobError != nil {
		errorVal = *jobError
	}

	_, err := db.Exec(
		`INSERT INTO jobs (id, type, key, group_key, nonce, status, payload, result, error, retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES (?, 'merge', ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		id, key, groupKey, id, status, payload, resultVal, errorVal, nowStr, submittedBy, nowStr, nowStr,
	)
	if err != nil {
		t.Fatalf("seedMergeJob(%q) failed: %v", id, err)
	}
}

// parseMergeJobFullResponse parses the response body as a full merge job response.
func parseMergeJobFullResponse(t *testing.T, rec *httptest.ResponseRecorder) mergeJobFullResponse {
	t.Helper()
	var resp mergeJobFullResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode full merge job response: %v (body: %s)", err, rec.Body.String())
	}
	return resp
}

// parseMergeJobListResponse parses the response body as an array of full
// merge job responses.
func parseMergeJobListResponse(t *testing.T, rec *httptest.ResponseRecorder) []mergeJobFullResponse {
	t.Helper()
	var resp []mergeJobFullResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode merge job list response: %v (body: %s)", err, rec.Body.String())
	}
	return resp
}

// newMergeTestEnvWithRebase creates a merge test env with a BatchRebaseFunc
// injected into the config. This is used by POST /rebase endpoint tests.
func newMergeTestEnvWithRebase(t *testing.T, branchRegistry map[string]bool, rebaseFunc BatchRebaseFunc) *mergeTestEnv {
	t.Helper()

	db := openTestDB(t)
	createWorkspacesTable(t, db)

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

	_ = RegisterHandler(q)

	e := echo.New()
	api := e.Group("/api/v1")
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
		BatchRebase: rebaseFunc,
	}
	RegisterMergeRoutes(api, cfg)

	return &mergeTestEnv{
		echo:  e,
		db:    db,
		queue: q,
	}
}

// ===========================================================================
// TS-12-36: GET /api/v1/workspaces/:slug/merges returns HTTP 200 with a
// JSON array of merge job records scoped to the workspace.
//
// Requirement: 12-REQ-10.1
// ===========================================================================

func TestListMerges_Returns200WithArray(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	// Seed two merge jobs for workspace ws1.
	seedMergeJob(t, env.db, "job-list-1", "queued", "ws1", "main", "feature/a", "alice", nil, nil)
	seedMergeJob(t, env.db, "job-list-2", "completed", "ws1", "main", "feature/b", "bob", nil, nil)

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws1/merges", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /merges status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	items := parseMergeJobListResponse(t, rec)
	if len(items) != 2 {
		t.Fatalf("expected 2 merge jobs, got %d", len(items))
	}

	// Verify each item has the required fields and is scoped to ws1.
	requiredFields := []string{"id", "workspace_slug", "target_branch", "source_ref", "status", "submitted_by", "created_at", "updated_at"}
	for _, item := range items {
		if item.WorkspaceSlug != "ws1" {
			t.Errorf("expected workspace_slug='ws1', got %q", item.WorkspaceSlug)
		}
		if item.ID == "" {
			t.Error("expected non-empty job ID")
		}
		if item.Status == "" {
			t.Error("expected non-empty status")
		}
		// Verify the status is a valid job status.
		validStatuses := map[string]bool{
			"queued": true, "running": true, "completed": true,
			"failed": true, "dead_letter": true, "cancelled": true,
		}
		if !validStatuses[item.Status] {
			t.Errorf("unexpected status %q", item.Status)
		}
	}
	_ = requiredFields // Used conceptually above; suppress unused warning.
}

// ===========================================================================
// 12-REQ-10.E1: GET /api/v1/workspaces/:slug/merges returns empty array
// when no merge jobs exist for the workspace.
// ===========================================================================

func TestListMerges_Empty_Returns200WithEmptyArray(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	// No merge jobs seeded.
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws1/merges", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /merges (no jobs) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	items := parseMergeJobListResponse(t, rec)
	if items == nil {
		t.Fatal("expected non-nil array (possibly empty), got nil")
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 merge jobs, got %d", len(items))
	}
}

// ===========================================================================
// TS-12-37: GET /api/v1/workspaces/:slug/merges/:id returns HTTP 200 with
// the merge job record for a valid job ID, or HTTP 404 if the job does not
// exist.
//
// Requirement: 12-REQ-10.2
// ===========================================================================

func TestGetMerge_ValidID_Returns200(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedMergeJob(t, env.db, "job-uuid-1", "queued", "ws1", "main", "feature/a", "alice", nil, nil)

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws1/merges/job-uuid-1", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /merges/:id status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseMergeJobFullResponse(t, rec)
	if resp.ID != "job-uuid-1" {
		t.Errorf("expected id='job-uuid-1', got %q", resp.ID)
	}
	if resp.WorkspaceSlug != "ws1" {
		t.Errorf("expected workspace_slug='ws1', got %q", resp.WorkspaceSlug)
	}
}

func TestGetMerge_NonexistentID_Returns404(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws1/merges/nonexistent-id", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /merges/:id (nonexistent) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// ===========================================================================
// 12-REQ-10.E2: GET /merges/:id for a job that belongs to a different
// workspace returns HTTP 404 to prevent cross-workspace information
// disclosure.
// ===========================================================================

func TestGetMerge_DifferentWorkspace_Returns404(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedTestWorkspace(t, env.db, "ws2", "bob", "active", "ready")

	// Job belongs to ws2, but we query via ws1.
	seedMergeJob(t, env.db, "job-ws2-only", "queued", "ws2", "main", "feature/a", "bob", nil, nil)

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws1/merges/job-ws2-only", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /merges/:id (cross-workspace) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// ===========================================================================
// TS-12-38: GET /api/v1/workspaces/:slug/merges called by a PAT without
// merges:read scope returns HTTP 403.
//
// Requirement: 12-REQ-10.3
// ===========================================================================

func TestListMerges_PATWithoutReadScope_Returns403(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	// PAT with no merge scopes.
	auth := mergePATAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws1/merges", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /merges (PAT no scopes) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for missing permission scope")
	}
}

func TestGetMerge_PATWithoutReadScope_Returns403(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedMergeJob(t, env.db, "job-perm-1", "queued", "ws1", "main", "feature/a", "alice", nil, nil)

	// PAT with no merge scopes.
	auth := mergePATAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws1/merges/job-perm-1", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /merges/:id (PAT no scopes) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// ===========================================================================
// TS-12-39: DELETE /api/v1/workspaces/:slug/merges/:id for a queued job
// returns HTTP 200 and transitions the job to cancelled status.
//
// Requirement: 12-REQ-11.1
// ===========================================================================

func TestCancelMerge_QueuedJob_Returns200(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedMergeJob(t, env.db, "job-cancel-1", "queued", "ws1", "main", "feature/a", "alice", nil, nil)

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/merges/job-cancel-1", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /merges/:id (queued) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify the job transitioned to cancelled status in the database.
	var status string
	err := env.db.QueryRow("SELECT status FROM jobs WHERE id = ?", "job-cancel-1").Scan(&status)
	if err != nil {
		t.Fatalf("query job status failed: %v", err)
	}
	if status != "cancelled" {
		t.Errorf("expected job status='cancelled', got %q", status)
	}
}

// ===========================================================================
// TS-12-40: DELETE /api/v1/workspaces/:slug/merges/:id for a job that is
// already running, completed, failed, or cancelled returns HTTP 409.
//
// Requirement: 12-REQ-11.2
// ===========================================================================

func TestCancelMerge_RunningJob_Returns409(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedMergeJob(t, env.db, "job-running-1", "running", "ws1", "main", "feature/a", "alice", nil, nil)

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/merges/job-running-1", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE /merges/:id (running) status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for non-cancellable job")
	}
}

func TestCancelMerge_CompletedJob_Returns409(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedMergeJob(t, env.db, "job-done-1", "completed", "ws1", "main", "feature/a", "alice", nil, nil)

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/merges/job-done-1", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE /merges/:id (completed) status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestCancelMerge_CancelledJob_Returns409(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedMergeJob(t, env.db, "job-already-cancelled", "cancelled", "ws1", "main", "feature/a", "alice", nil, nil)

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/merges/job-already-cancelled", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("DELETE /merges/:id (cancelled) status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

// ===========================================================================
// TS-12-41: DELETE /api/v1/workspaces/:slug/merges/:id called by a PAT
// without merges:write scope returns HTTP 403.
//
// Requirement: 12-REQ-11.3
// ===========================================================================

func TestCancelMerge_PATWithoutWriteScope_Returns403(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedMergeJob(t, env.db, "job-perm-cancel", "queued", "ws1", "main", "feature/a", "alice", nil, nil)

	// PAT with merges:read only — no merges:write.
	auth := mergePATAuth("alice", "merges:read")
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/merges/job-perm-cancel", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("DELETE /merges/:id (PAT read-only) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for missing permission scope")
	}
}

// ===========================================================================
// 12-REQ-11.E1: DELETE /merges/:id for a non-existent or different-workspace
// job returns HTTP 404.
// ===========================================================================

func TestCancelMerge_NonexistentJob_Returns404(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/merges/nonexistent-id", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE /merges/:id (nonexistent) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestCancelMerge_DifferentWorkspaceJob_Returns404(t *testing.T) {
	env := newMergeTestEnv(t, nil)

	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedTestWorkspace(t, env.db, "ws2", "bob", "active", "ready")

	// Job belongs to ws2.
	seedMergeJob(t, env.db, "job-ws2-cancel", "queued", "ws2", "main", "feature/a", "bob", nil, nil)

	// Attempt to cancel via ws1 path.
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/ws1/merges/job-ws2-cancel", "", mergeUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE /merges/:id (cross-workspace) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// ===========================================================================
// TS-12-42: POST /api/v1/workspaces/:slug/rebase with valid target_ref and
// non-empty branches list returns HTTP 200 with per-branch results
// synchronously.
//
// Requirement: 12-REQ-12.1
// ===========================================================================

func TestBatchRebase_ValidRequest_Returns200(t *testing.T) {
	rebaseFunc := func(_ context.Context, slug, targetRef string, branches []string) ([]RebaseResult, error) {
		results := make([]RebaseResult, len(branches))
		for i, b := range branches {
			results[i] = RebaseResult{
				Branch:  b,
				Status:  "ok",
				NewHead: "aaaa000000000000000000000000000000000001",
			}
		}
		return results, nil
	}

	env := newMergeTestEnvWithRebase(t, nil, rebaseFunc)
	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	body := `{"target_ref":"main","branches":["feature/a","feature/b"]}`
	auth := mergePATAuth("alice", "merges:write")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/rebase", body, auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /rebase status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp batchRebaseTestResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode rebase response: %v", err)
	}

	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}

	for _, r := range resp.Results {
		if r.Branch != "feature/a" && r.Branch != "feature/b" {
			t.Errorf("unexpected branch %q in results", r.Branch)
		}
		if r.Status != "ok" && r.Status != "conflict" {
			t.Errorf("unexpected status %q for branch %q", r.Status, r.Branch)
		}
	}
}

// ===========================================================================
// TS-12-43: POST /api/v1/workspaces/:slug/rebase with an empty branches
// list returns HTTP 400 without performing any git operations.
//
// Requirement: 12-REQ-12.2
// ===========================================================================

func TestBatchRebase_EmptyBranches_Returns400(t *testing.T) {
	rebaseCalled := false
	rebaseFunc := func(_ context.Context, _, _ string, _ []string) ([]RebaseResult, error) {
		rebaseCalled = true
		return nil, nil
	}

	env := newMergeTestEnvWithRebase(t, nil, rebaseFunc)
	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	body := `{"target_ref":"main","branches":[]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/rebase", body, mergeUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /rebase (empty branches) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if !strings.Contains(strings.ToLower(resp.Error.Message), "branches") {
		t.Errorf("expected error message to mention 'branches', got %q", resp.Error.Message)
	}

	if rebaseCalled {
		t.Error("rebase function should not have been called for empty branches list")
	}
}

// ===========================================================================
// TS-12-44: POST /api/v1/workspaces/:slug/rebase called by a PAT without
// merges:write scope returns HTTP 403.
//
// Requirement: 12-REQ-12.3
// ===========================================================================

func TestBatchRebase_PATWithoutWriteScope_Returns403(t *testing.T) {
	env := newMergeTestEnvWithRebase(t, nil, nil)
	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	body := `{"target_ref":"main","branches":["feature/a"]}`
	// PAT with merges:read only — no merges:write.
	auth := mergePATAuth("alice", "merges:read")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/rebase", body, auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /rebase (PAT read-only) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for missing permission scope")
	}
}

// ===========================================================================
// TS-12-45: POST /api/v1/workspaces/:slug/rebase with missing target_ref
// returns HTTP 400.
//
// Requirement: 12-REQ-12.4
// ===========================================================================

func TestBatchRebase_MissingTargetRef_Returns400(t *testing.T) {
	env := newMergeTestEnvWithRebase(t, nil, nil)
	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")

	// Body has branches but no target_ref.
	body := `{"branches":["feature/a"]}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/rebase", body, mergeUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /rebase (missing target_ref) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseMergeErrorEnvelope(t, rec)
	if !strings.Contains(strings.ToLower(resp.Error.Message), "target_ref") {
		t.Errorf("expected error message to mention 'target_ref', got %q", resp.Error.Message)
	}
}

// ===========================================================================
// TS-12-50: Merge REST API response serialization projects job queue records
// into merge response objects with all required fields, using null for
// missing optional fields.
//
// Requirement: 12-REQ-14.1
// ===========================================================================

func TestProjectMergeJobResponse_QueuedJob(t *testing.T) {
	now := time.Now().UTC()
	nowStr := apikit.FormatUTC(now)

	job := &jobqueue.Job{
		ID:         "uuid-1",
		Type:       "merge",
		Key:        "ws1:main:feature/a",
		GroupKey:   "ws1:main",
		Status:     "queued",
		Payload:    json.RawMessage(`{"workspace_slug":"ws1","target_branch":"main","source_ref":"feature/a","submitted_by":"alice"}`),
		Result:     nil,
		Error:      "",
		RetryCount: 0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	resp := ProjectMergeJobResponse(job)

	if resp.ID != "uuid-1" {
		t.Errorf("expected id='uuid-1', got %q", resp.ID)
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
	if resp.Status != "queued" {
		t.Errorf("expected status='queued', got %q", resp.Status)
	}
	if resp.BaseSHA != nil {
		t.Errorf("expected base_sha=nil for queued job, got %v", resp.BaseSHA)
	}
	if resp.MergedSHA != nil {
		t.Errorf("expected merged_sha=nil for queued job, got %v", resp.MergedSHA)
	}
	if resp.ConflictFiles == nil {
		t.Error("expected conflict_files to be non-nil empty slice, got nil")
	}
	if len(resp.ConflictFiles) != 0 {
		t.Errorf("expected conflict_files to be empty, got %v", resp.ConflictFiles)
	}
	if resp.CheckOutput != nil {
		t.Errorf("expected check_output=nil for queued job, got %v", resp.CheckOutput)
	}
	if resp.Error != nil {
		t.Errorf("expected error=nil for queued job, got %v", resp.Error)
	}
	if resp.RetryCount != 0 {
		t.Errorf("expected retry_count=0, got %d", resp.RetryCount)
	}
	if resp.SubmittedBy != "alice" {
		t.Errorf("expected submitted_by='alice', got %q", resp.SubmittedBy)
	}
	if resp.CreatedAt != nowStr {
		t.Errorf("expected created_at=%q, got %q", nowStr, resp.CreatedAt)
	}
	if resp.UpdatedAt != nowStr {
		t.Errorf("expected updated_at=%q, got %q", nowStr, resp.UpdatedAt)
	}
}

// ===========================================================================
// TS-12-51: When a merge job is in completed status, the response includes
// non-null base_sha and merged_sha as 40-char hex strings.
//
// Requirement: 12-REQ-14.2
// ===========================================================================

func TestProjectMergeJobResponse_CompletedJob(t *testing.T) {
	now := time.Now().UTC()
	baseSHA := "aaaa000000000000000000000000000000000001"
	mergedSHA := "bbbb000000000000000000000000000000000002"

	resultJSON := fmt.Sprintf(`{"base_sha":%q,"merged_sha":%q}`, baseSHA, mergedSHA)

	job := &jobqueue.Job{
		ID:         "uuid-completed",
		Type:       "merge",
		Key:        "ws1:main:feature/a",
		GroupKey:   "ws1:main",
		Status:     "completed",
		Payload:    json.RawMessage(`{"workspace_slug":"ws1","target_branch":"main","source_ref":"feature/a","submitted_by":"alice"}`),
		Result:     json.RawMessage(resultJSON),
		Error:      "",
		RetryCount: 0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	resp := ProjectMergeJobResponse(job)

	if resp.BaseSHA == nil {
		t.Fatal("expected non-nil base_sha for completed job")
	}
	if *resp.BaseSHA != baseSHA {
		t.Errorf("expected base_sha=%q, got %q", baseSHA, *resp.BaseSHA)
	}
	if len(*resp.BaseSHA) != 40 {
		t.Errorf("expected base_sha length=40, got %d", len(*resp.BaseSHA))
	}

	if resp.MergedSHA == nil {
		t.Fatal("expected non-nil merged_sha for completed job")
	}
	if *resp.MergedSHA != mergedSHA {
		t.Errorf("expected merged_sha=%q, got %q", mergedSHA, *resp.MergedSHA)
	}
	if len(*resp.MergedSHA) != 40 {
		t.Errorf("expected merged_sha length=40, got %d", len(*resp.MergedSHA))
	}
}

// ===========================================================================
// TS-12-52: When a merge job failed with WouldConflict, the response
// includes a non-empty conflict_files array.
//
// Requirement: 12-REQ-14.3
// ===========================================================================

func TestProjectMergeJobResponse_WouldConflict(t *testing.T) {
	now := time.Now().UTC()

	job := &jobqueue.Job{
		ID:         "uuid-conflict",
		Type:       "merge",
		Key:        "ws1:main:feature/a",
		GroupKey:   "ws1:main",
		Status:     "failed",
		Payload:    json.RawMessage(`{"workspace_slug":"ws1","target_branch":"main","source_ref":"feature/a","submitted_by":"alice"}`),
		Result:     nil,
		Error:      `{"reason":"WouldConflict","conflict_files":["src/a.go","src/b.go"]}`,
		RetryCount: 0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	resp := ProjectMergeJobResponse(job)

	if len(resp.ConflictFiles) == 0 {
		t.Fatal("expected non-empty conflict_files for WouldConflict job")
	}
	expectedFiles := []string{"src/a.go", "src/b.go"}
	if len(resp.ConflictFiles) != len(expectedFiles) {
		t.Fatalf("expected %d conflict files, got %d", len(expectedFiles), len(resp.ConflictFiles))
	}
	for i, f := range expectedFiles {
		if resp.ConflictFiles[i] != f {
			t.Errorf("conflict_files[%d] = %q; want %q", i, resp.ConflictFiles[i], f)
		}
	}
}

// ===========================================================================
// 12-REQ-14.E1: If the job result JSON is malformed or missing expected
// fields, the projection returns null for missing fields rather than
// returning an error response.
// ===========================================================================

func TestProjectMergeJobResponse_MalformedResult(t *testing.T) {
	now := time.Now().UTC()

	job := &jobqueue.Job{
		ID:         "uuid-malformed",
		Type:       "merge",
		Key:        "ws1:main:feature/a",
		GroupKey:   "ws1:main",
		Status:     "completed",
		Payload:    json.RawMessage(`{"workspace_slug":"ws1","target_branch":"main","source_ref":"feature/a","submitted_by":"alice"}`),
		Result:     json.RawMessage(`{invalid json`),
		Error:      "",
		RetryCount: 0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	// Should not panic; returns null for unparseable result fields.
	resp := ProjectMergeJobResponse(job)

	if resp.BaseSHA != nil {
		t.Errorf("expected base_sha=nil for malformed result, got %v", resp.BaseSHA)
	}
	if resp.MergedSHA != nil {
		t.Errorf("expected merged_sha=nil for malformed result, got %v", resp.MergedSHA)
	}
	// Payload fields should still be extracted correctly.
	if resp.WorkspaceSlug != "ws1" {
		t.Errorf("expected workspace_slug='ws1' even with malformed result, got %q", resp.WorkspaceSlug)
	}
}
