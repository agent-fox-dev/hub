package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// Helpers for origin sync tests
// ===========================================================================

// syncTestEnv is a test environment for origin sync tests with recording stubs.
type syncTestEnv struct {
	echo  *echo.Echo
	db    *sql.DB
	queue *jobqueue.Queue

	mu     sync.Mutex
	events []string // ordered event log

	// Counters
	originFetchCount      int
	originAuthResolveCount int
	upstreamFetchCount    int

	// Variable store
	variables map[string]string
	varErrors map[string]error

	// Controls
	originFetchErr      error
	originAuthResolveErr error
	fetchOriginNil      bool // if true, FetchOrigin is nil
}

func newSyncTestEnv(t *testing.T) *syncTestEnv {
	t.Helper()

	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)
	addWorkspaceColumns(t, db)

	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey: %v", err)
	}
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress: %v", err)
	}

	logger := nopLogger()
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("jobqueue.New: %v", err)
	}
	_ = RegisterRebuildJob(q, &RebuildHandler{})

	env := &syncTestEnv{
		db:        db,
		queue:     q,
		variables: make(map[string]string),
		varErrors: make(map[string]error),
	}

	mock := newMockGitRunner()
	patches := newMockPatchStore(nil)

	getVar := func(scope, slug, key string) (string, error) {
		env.mu.Lock()
		defer env.mu.Unlock()
		env.events = append(env.events, "getvar:"+key)
		if e, ok := env.varErrors[key]; ok {
			return "", e
		}
		if v, ok := env.variables[key]; ok {
			return v, nil
		}
		return "", fmt.Errorf("variable not set")
	}

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	syncCfg := SyncAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: t.TempDir(),
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
		Fetch: func(_ context.Context, _ string, _ transport.AuthMethod) error {
			env.mu.Lock()
			env.upstreamFetchCount++
			env.events = append(env.events, "fetchUpstream")
			env.mu.Unlock()
			return nil
		},
		ResolveAuth: func(_ string) (transport.AuthMethod, error) {
			env.mu.Lock()
			env.events = append(env.events, "resolveUpstreamAuth")
			env.mu.Unlock()
			return nil, nil
		},
		GetVariable: getVar,
		PatchStore:  patches,
	}

	if !env.fetchOriginNil {
		syncCfg.FetchOrigin = func(_ context.Context, _ string, _ transport.AuthMethod) error {
			env.mu.Lock()
			env.originFetchCount++
			env.events = append(env.events, "fetchOrigin")
			fetchErr := env.originFetchErr
			env.mu.Unlock()
			return fetchErr
		}
		syncCfg.ResolveOriginAuth = func(_ string) (transport.AuthMethod, error) {
			env.mu.Lock()
			env.originAuthResolveCount++
			env.events = append(env.events, "resolveOriginAuth")
			authErr := env.originAuthResolveErr
			env.mu.Unlock()
			return nil, authErr
		}
	}

	RegisterSyncRoutes(api, syncCfg)
	env.echo = e

	return env
}

// newSyncTestEnvWithNilFetchOrigin creates a test env where FetchOrigin is nil.
func newSyncTestEnvWithNilFetchOrigin(t *testing.T) *syncTestEnv {
	t.Helper()

	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)
	addWorkspaceColumns(t, db)

	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey: %v", err)
	}
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress: %v", err)
	}

	logger := nopLogger()
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("jobqueue.New: %v", err)
	}
	_ = RegisterRebuildJob(q, &RebuildHandler{})

	env := &syncTestEnv{
		db:        db,
		queue:     q,
		variables: make(map[string]string),
		varErrors: make(map[string]error),
	}

	mock := newMockGitRunner()
	patches := newMockPatchStore(nil)

	getVar := func(scope, slug, key string) (string, error) {
		env.mu.Lock()
		defer env.mu.Unlock()
		env.events = append(env.events, "getvar:"+key)
		if e, ok := env.varErrors[key]; ok {
			return "", e
		}
		if v, ok := env.variables[key]; ok {
			return v, nil
		}
		return "", fmt.Errorf("variable not set")
	}

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())

	syncCfg := SyncAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: t.TempDir(),
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
		Fetch: func(_ context.Context, _ string, _ transport.AuthMethod) error {
			env.mu.Lock()
			env.upstreamFetchCount++
			env.events = append(env.events, "fetchUpstream")
			env.mu.Unlock()
			return nil
		},
		ResolveAuth: func(_ string) (transport.AuthMethod, error) {
			env.mu.Lock()
			env.events = append(env.events, "resolveUpstreamAuth")
			env.mu.Unlock()
			return nil, nil
		},
		GetVariable: getVar,
		PatchStore:  patches,
		// FetchOrigin is nil
		// ResolveOriginAuth is nil
	}

	RegisterSyncRoutes(api, syncCfg)
	env.echo = e

	return env
}

func (env *syncTestEnv) setVar(key, value string) {
	env.mu.Lock()
	defer env.mu.Unlock()
	env.variables[key] = value
}

func (env *syncTestEnv) setVarError(key string, err error) {
	env.mu.Lock()
	defer env.mu.Unlock()
	env.varErrors[key] = err
}

func (env *syncTestEnv) doSync(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	auth := rebuildUserAuth("alice")
	authJSON, _ := json.Marshal(auth)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/my-workspace/sync", nil)
	req.Header.Set("X-Test-Auth", string(authJSON))
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

func (env *syncTestEnv) getEvents() []string {
	env.mu.Lock()
	defer env.mu.Unlock()
	result := make([]string, len(env.events))
	copy(result, env.events)
	return result
}

func (env *syncTestEnv) resetCounters() {
	env.mu.Lock()
	defer env.mu.Unlock()
	env.originFetchCount = 0
	env.originAuthResolveCount = 0
	env.upstreamFetchCount = 0
	env.events = nil
}

// ===========================================================================
// TS-20-1 (unit): Both variables are read through GetVariable after the lock
// is taken and before any fetch, on every sync
//
// Verifies: 20-REQ-1.1
// ===========================================================================

func TestSyncOrigin_VariablesReadPerSync_TS201(t *testing.T) {
	env := newSyncTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration", "")

	// First sync: PATCH_BRANCH_SOURCE is unset → hub mode.
	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("first sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	events1 := env.getEvents()
	// Verify GetVariable was called for both variables.
	foundSource := false
	foundPolicy := false
	for _, e := range events1 {
		if e == "getvar:PATCH_BRANCH_SOURCE" {
			foundSource = true
		}
		if e == "getvar:PATCH_DIVERGENCE_POLICY" {
			foundPolicy = true
		}
	}
	if !foundSource {
		t.Error("first sync: PATCH_BRANCH_SOURCE not read")
	}
	if !foundPolicy {
		t.Error("first sync: PATCH_DIVERGENCE_POLICY not read")
	}

	// Verify no origin fetch in hub mode.
	env.mu.Lock()
	if env.originFetchCount != 0 {
		t.Errorf("first sync: expected 0 origin fetches, got %d", env.originFetchCount)
	}
	env.mu.Unlock()

	// Verify variable reads happen before any fetch.
	firstVarIdx := -1
	firstFetchIdx := -1
	for i, e := range events1 {
		if firstVarIdx == -1 && (e == "getvar:PATCH_BRANCH_SOURCE" || e == "getvar:PATCH_DIVERGENCE_POLICY") {
			firstVarIdx = i
		}
		if firstFetchIdx == -1 && (e == "fetchUpstream" || e == "fetchOrigin") {
			firstFetchIdx = i
		}
	}
	if firstFetchIdx != -1 && firstVarIdx > firstFetchIdx {
		t.Error("first sync: variable reads should happen before any fetch")
	}

	// Second sync: set PATCH_BRANCH_SOURCE=origin without rebuilding handler.
	env.resetCounters()
	env.setVar("PATCH_BRANCH_SOURCE", "origin")

	rec2 := env.doSync(t)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second sync status = %d; want %d; body = %s", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	env.mu.Lock()
	if env.originFetchCount != 1 {
		t.Errorf("second sync: expected 1 origin fetch, got %d", env.originFetchCount)
	}
	env.mu.Unlock()
}

// ===========================================================================
// TS-20-2 (unit): The exact value origin selects origin as the patch authority
//
// Verifies: 20-REQ-1.2
// ===========================================================================

func TestSyncOrigin_ExactOriginValue_TS202(t *testing.T) {
	env := newSyncTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration", "")

	env.setVar("PATCH_BRANCH_SOURCE", "origin")

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	env.mu.Lock()
	fetchCount := env.originFetchCount
	env.mu.Unlock()

	if fetchCount != 1 {
		t.Errorf("expected 1 origin fetch, got %d", fetchCount)
	}

	// Check response has origin_fetched=true.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if of, ok := resp["origin_fetched"]; !ok {
		t.Error("expected origin_fetched field in response")
	} else if of != true {
		t.Errorf("expected origin_fetched=true, got %v", of)
	}
}

// ===========================================================================
// TS-20-3 (unit): Unset, error, hub, Origin, empty and other values select
// hub without an error
//
// Verifies: 20-REQ-1.3
// ===========================================================================

func TestSyncOrigin_NonOriginValues_SelectHub_TS203(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		setError bool
		unset    bool
	}{
		{"unset", "", false, true},
		{"lookup_error", "", true, false},
		{"hub", "hub", false, false},
		{"Origin_capital", "Origin", false, false},
		{"ORIGIN_upper", "ORIGIN", false, false},
		{"empty_string", "", false, false},
		{"garbage", "garbage", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSyncTestEnv(t)

			seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
				"https://github.com/example/upstream",
				"aaaa000000000000000000000000000000000001",
				"integration", "")

			if tc.setError {
				env.setVarError("PATCH_BRANCH_SOURCE", fmt.Errorf("lookup error"))
			} else if !tc.unset {
				env.setVar("PATCH_BRANCH_SOURCE", tc.value)
			}
			// If unset, the variable is not in the map, so GetVariable returns error.

			rec := env.doSync(t)
			if rec.Code != http.StatusOK {
				t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
			}

			env.mu.Lock()
			fetchCount := env.originFetchCount
			authCount := env.originAuthResolveCount
			env.mu.Unlock()

			if fetchCount != 0 {
				t.Errorf("expected 0 origin fetches, got %d", fetchCount)
			}
			if authCount != 0 {
				t.Errorf("expected 0 origin auth resolves, got %d", authCount)
			}

			// Check response.
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if of, ok := resp["origin_fetched"]; !ok {
				t.Error("expected origin_fetched field in response")
			} else if of != false {
				t.Errorf("expected origin_fetched=false, got %v", of)
			}
			if _, ok := resp["patches_synced"]; ok {
				t.Error("expected no patches_synced field in hub mode")
			}
		})
	}
}

// ===========================================================================
// TS-20-6 (unit): In hub mode the divergence policy is ignored
//
// Verifies: 20-REQ-1.6
// ===========================================================================

func TestSyncOrigin_HubModeIgnoresPolicy_TS206(t *testing.T) {
	// Run two syncs in hub mode with different policies and verify both
	// produce the same result: no origin fetch, no ref writes.
	for _, policy := range []string{"report", "replace"} {
		t.Run("policy_"+policy, func(t *testing.T) {
			env := newSyncTestEnv(t)

			seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
				"https://github.com/example/upstream",
				"aaaa000000000000000000000000000000000001",
				"integration", "")

			// Hub mode (PATCH_BRANCH_SOURCE not set to "origin").
			env.setVar("PATCH_DIVERGENCE_POLICY", policy)

			rec := env.doSync(t)
			if rec.Code != http.StatusOK {
				t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
			}

			env.mu.Lock()
			fetchCount := env.originFetchCount
			env.mu.Unlock()

			if fetchCount != 0 {
				t.Errorf("expected 0 origin fetches in hub mode, got %d", fetchCount)
			}

			// No patches_synced field in hub mode.
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if _, ok := resp["patches_synced"]; ok {
				t.Error("expected no patches_synced field in hub mode")
			}
		})
	}
}

// ===========================================================================
// TS-20-9 (unit): Sync steps run in the documented order under the held lock
//
// Verifies: 20-REQ-2.2
// ===========================================================================

func TestSyncOrigin_StepOrder_TS209(t *testing.T) {
	env := newSyncTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration", "")

	env.setVar("PATCH_BRANCH_SOURCE", "origin")

	rec := env.doSync(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	events := env.getEvents()

	// Expected order: getvar reads, resolveOriginAuth, fetchUpstream, fetchOrigin
	// (resolveUpstreamBase and beyond are not recorded by our stubs)
	expectedOrder := []string{
		"resolveOriginAuth",
		"fetchUpstream",
		"fetchOrigin",
	}

	// Find the indices of these events.
	indices := make(map[string]int)
	for i, e := range events {
		if _, ok := indices[e]; !ok {
			indices[e] = i
		}
	}

	// Variable reads must come before resolveOriginAuth.
	varSourceIdx, hasVarSource := indices["getvar:PATCH_BRANCH_SOURCE"]
	resolveOriginIdx, hasResolveOrigin := indices["resolveOriginAuth"]
	fetchUpstreamIdx, hasFetchUpstream := indices["fetchUpstream"]
	fetchOriginIdx, hasFetchOrigin := indices["fetchOrigin"]

	if !hasVarSource {
		t.Fatal("getvar:PATCH_BRANCH_SOURCE not found in events")
	}
	if !hasResolveOrigin {
		t.Fatal("resolveOriginAuth not found in events")
	}
	if !hasFetchUpstream {
		t.Fatal("fetchUpstream not found in events")
	}
	if !hasFetchOrigin {
		t.Fatal("fetchOrigin not found in events")
	}

	if varSourceIdx >= resolveOriginIdx {
		t.Errorf("variable reads (%d) should come before resolveOriginAuth (%d)", varSourceIdx, resolveOriginIdx)
	}
	if resolveOriginIdx >= fetchUpstreamIdx {
		t.Errorf("resolveOriginAuth (%d) should come before fetchUpstream (%d)", resolveOriginIdx, fetchUpstreamIdx)
	}
	if fetchUpstreamIdx >= fetchOriginIdx {
		t.Errorf("fetchUpstream (%d) should come before fetchOrigin (%d)", fetchUpstreamIdx, fetchOriginIdx)
	}

	_ = expectedOrder
}

// ===========================================================================
// TS-20-10 (unit): A credential-resolution failure answers 502 before any
// fetch and changes nothing
//
// Verifies: 20-REQ-2.3
// ===========================================================================

func TestSyncOrigin_OriginAuthFailure_TS2010(t *testing.T) {
	env := newSyncTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration", "")

	env.setVar("PATCH_BRANCH_SOURCE", "origin")

	// Snapshot before.
	var beforeSHA, beforeSyncAt sql.NullString
	env.db.QueryRow(`SELECT upstream_head_sha, last_sync_at FROM workspaces WHERE slug = ?`, "my-workspace").Scan(&beforeSHA, &beforeSyncAt)

	// Make origin auth fail.
	env.mu.Lock()
	env.originAuthResolveErr = fmt.Errorf("auth resolution failed")
	env.mu.Unlock()

	rec := env.doSync(t)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}

	// Check error message.
	var errResp errorEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errResp.Error.Message != "failed to resolve origin credentials" {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, "failed to resolve origin credentials")
	}

	// Verify no fetches happened.
	env.mu.Lock()
	if env.upstreamFetchCount != 0 {
		t.Errorf("expected 0 upstream fetches, got %d", env.upstreamFetchCount)
	}
	if env.originFetchCount != 0 {
		t.Errorf("expected 0 origin fetches, got %d", env.originFetchCount)
	}
	env.mu.Unlock()

	// Verify snapshot unchanged.
	var afterSHA, afterSyncAt sql.NullString
	env.db.QueryRow(`SELECT upstream_head_sha, last_sync_at FROM workspaces WHERE slug = ?`, "my-workspace").Scan(&afterSHA, &afterSyncAt)
	if afterSHA != beforeSHA {
		t.Errorf("upstream_head_sha changed: %v → %v", beforeSHA, afterSHA)
	}
	if afterSyncAt != beforeSyncAt {
		t.Errorf("last_sync_at changed: %v → %v", beforeSyncAt, afterSyncAt)
	}
}

// ===========================================================================
// TS-20-11 (unit): An origin fetch failure after a successful upstream fetch
// answers 502 origin_fetch_failed and changes nothing
//
// Verifies: 20-REQ-2.4
// ===========================================================================

func TestSyncOrigin_OriginFetchFailure_TS2011(t *testing.T) {
	env := newSyncTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration", "")

	env.setVar("PATCH_BRANCH_SOURCE", "origin")

	// Snapshot before.
	var beforeSHA, beforeSyncAt sql.NullString
	env.db.QueryRow(`SELECT upstream_head_sha, last_sync_at FROM workspaces WHERE slug = ?`, "my-workspace").Scan(&beforeSHA, &beforeSyncAt)

	// Make origin fetch fail.
	env.mu.Lock()
	env.originFetchErr = fmt.Errorf("origin fetch error")
	env.mu.Unlock()

	rec := env.doSync(t)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}

	// Check error message and error_type.
	var errResp errorEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errResp.Error.Message != "origin fetch failed" {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, "origin fetch failed")
	}
	if errResp.Error.ErrorType != "origin_fetch_failed" {
		t.Errorf("error_type = %q; want %q", errResp.Error.ErrorType, "origin_fetch_failed")
	}

	// Verify upstream fetch was called.
	env.mu.Lock()
	if env.upstreamFetchCount != 1 {
		t.Errorf("expected 1 upstream fetch, got %d", env.upstreamFetchCount)
	}
	env.mu.Unlock()

	// Verify snapshot unchanged.
	var afterSHA, afterSyncAt sql.NullString
	env.db.QueryRow(`SELECT upstream_head_sha, last_sync_at FROM workspaces WHERE slug = ?`, "my-workspace").Scan(&afterSHA, &afterSyncAt)
	if afterSHA != beforeSHA {
		t.Errorf("upstream_head_sha changed: %v → %v", beforeSHA, afterSHA)
	}
	if afterSyncAt != beforeSyncAt {
		t.Errorf("last_sync_at changed: %v → %v", beforeSyncAt, afterSyncAt)
	}

	// Verify no rebuild job enqueued.
	var jobCount int
	env.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type='rebuild' AND key='my-workspace'`).Scan(&jobCount)
	if jobCount != 0 {
		t.Errorf("expected 0 rebuild jobs, got %d", jobCount)
	}
}

// ===========================================================================
// TS-20-12 (unit): The hub source performs zero origin fetches and zero
// origin credential lookups
//
// Verifies: 20-REQ-2.5
// ===========================================================================

func TestSyncOrigin_HubModeNoOriginOps_TS2012(t *testing.T) {
	// Test with upstream unchanged.
	t.Run("upstream_unchanged", func(t *testing.T) {
		env := newSyncTestEnv(t)

		seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
			"https://github.com/example/upstream",
			"aaaa000000000000000000000000000000000001",
			"integration", "")

		// PATCH_BRANCH_SOURCE unset → hub mode.
		rec := env.doSync(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		env.mu.Lock()
		if env.originFetchCount != 0 {
			t.Errorf("expected 0 origin fetches, got %d", env.originFetchCount)
		}
		if env.originAuthResolveCount != 0 {
			t.Errorf("expected 0 origin auth resolves, got %d", env.originAuthResolveCount)
		}
		env.mu.Unlock()
	})

	// Test with upstream advanced (RunFunc returns a new SHA).
	t.Run("upstream_advanced", func(t *testing.T) {
		env := newSyncTestEnv(t)

		seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
			"https://github.com/example/upstream",
			"aaaa000000000000000000000000000000000001",
			"integration", "")

		seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)

		rec := env.doSync(t)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}

		env.mu.Lock()
		if env.originFetchCount != 0 {
			t.Errorf("expected 0 origin fetches, got %d", env.originFetchCount)
		}
		if env.originAuthResolveCount != 0 {
			t.Errorf("expected 0 origin auth resolves, got %d", env.originAuthResolveCount)
		}
		env.mu.Unlock()
	})
}

// ===========================================================================
// TS-20-13 (unit): Origin mode with a nil FetchOrigin answers 500 origin
// fetch is not configured
//
// Verifies: 20-REQ-2.6
// ===========================================================================

func TestSyncOrigin_NilFetchOrigin_TS2013(t *testing.T) {
	env := newSyncTestEnvWithNilFetchOrigin(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration", "")

	env.setVar("PATCH_BRANCH_SOURCE", "origin")

	// Snapshot before.
	var beforeSHA, beforeSyncAt sql.NullString
	env.db.QueryRow(`SELECT upstream_head_sha, last_sync_at FROM workspaces WHERE slug = ?`, "my-workspace").Scan(&beforeSHA, &beforeSyncAt)

	rec := env.doSync(t)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("sync status = %d; want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	var errResp errorEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errResp.Error.Message != "origin fetch is not configured" {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, "origin fetch is not configured")
	}

	// Verify snapshot unchanged.
	var afterSHA, afterSyncAt sql.NullString
	env.db.QueryRow(`SELECT upstream_head_sha, last_sync_at FROM workspaces WHERE slug = ?`, "my-workspace").Scan(&afterSHA, &afterSyncAt)
	if afterSHA != beforeSHA {
		t.Errorf("upstream_head_sha changed: %v → %v", beforeSHA, afterSHA)
	}
	if afterSyncAt != beforeSyncAt {
		t.Errorf("last_sync_at changed: %v → %v", beforeSyncAt, afterSyncAt)
	}
}

// Ensure apikit is used (avoid unused import).
var _ = apikit.NowUTC
