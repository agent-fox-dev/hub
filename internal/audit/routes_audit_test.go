package audit

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// ===========================================================================
// TS-18-48: GET /api/v1/audit is registered on the Echo router with
//           audit:read permission middleware and the unified query handler
// REQ: 18-REQ-10.1
// ===========================================================================

func TestAuditQueryRouteRegistered(t *testing.T) {
	env := newAuditQueryTestEnv(t)

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodGet, "/api/v1/audit", "", auth)

	// The route must be registered and return 200 (not 404).
	if rec.Code == http.StatusNotFound {
		t.Fatal("GET /api/v1/audit returned 404 — route is not registered")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAuditQueryRouteRequiresAuth(t *testing.T) {
	env := newAuditQueryTestEnv(t)

	// No auth header — should return 401.
	rec := env.doRequest(t, http.MethodGet, "/api/v1/audit", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAuditQueryRouteRequiresAuditReadScope(t *testing.T) {
	env := newAuditQueryTestEnv(t)

	// PAT without audit:read scope — should return 403.
	auth := patAuth("user-1", "other:scope")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/audit", "", auth)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 without audit:read scope, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ===========================================================================
// TS-18-49: GET /api/v1/workspaces/:slug/runs/:run_id/transcript is
//           registered on the Echo router with audit:read permission
//           middleware and the transcript handler
// REQ: 18-REQ-10.2
// ===========================================================================

func TestTranscriptRouteRegistered(t *testing.T) {
	env := newAuditQueryTestEnv(t)

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/runs/run-uuid/transcript?node_id=node-uuid",
		"", auth)

	// The route must be registered and return 200 (not 404).
	if rec.Code == http.StatusNotFound {
		t.Fatal("GET /api/v1/workspaces/:slug/runs/:run_id/transcript returned 404 — route is not registered")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTranscriptRouteRequiresAuth(t *testing.T) {
	env := newAuditQueryTestEnv(t)

	// No auth header — should return 401.
	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/runs/run-uuid/transcript?node_id=node-uuid",
		"", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTranscriptRouteRequiresAuditReadScope(t *testing.T) {
	env := newAuditQueryTestEnv(t)

	// PAT without audit:read scope — should return 403.
	auth := patAuth("user-1", "other:scope")
	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/runs/run-uuid/transcript?node_id=node-uuid",
		"", auth)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 without audit:read scope, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ===========================================================================
// TS-18-50: GET /api/v1/events is registered on the Echo router with
//           audit:read permission middleware and the SSE handler
// REQ: 18-REQ-10.3
// ===========================================================================

func TestSSERouteRegistered(t *testing.T) {
	env := newAuditQueryTestEnv(t)

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodGet, "/api/v1/events", "", auth)

	// The route must be registered and return 200 (not 404).
	if rec.Code == http.StatusNotFound {
		t.Fatal("GET /api/v1/events returned 404 — route is not registered")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	contentType := rec.Header().Get("Content-Type")
	if contentType == "" || !containsSSEContentType(contentType) {
		t.Errorf("expected Content-Type containing 'text/event-stream', got %q", contentType)
	}
}

func TestSSERouteRequiresAuth(t *testing.T) {
	env := newAuditQueryTestEnv(t)

	// No auth header — should return 401.
	rec := env.doRequest(t, http.MethodGet, "/api/v1/events", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSSERouteRequiresAuditReadScope(t *testing.T) {
	env := newAuditQueryTestEnv(t)

	// PAT without audit:read scope — should return 403.
	auth := patAuth("user-1", "other:scope")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/events", "", auth)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 without audit:read scope, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ===========================================================================
// Edge case: nil Store or SSE manager panics at startup
// REQ: 18-REQ-10.E1
// ===========================================================================

func TestAuditRouteRegistrationPanicsOnNilStore(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when Store is nil, but no panic occurred")
		}
	}()

	env := newAuditQueryTestEnvRaw(t)
	RegisterAuditQueryRoutes(env.apiGroup, nil, &mockSSEManager{})
}

func TestAuditRouteRegistrationPanicsOnNilSSEManager(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when SSE manager is nil, but no panic occurred")
		}
	}()

	env := newAuditQueryTestEnvRaw(t)
	RegisterAuditQueryRoutes(env.apiGroup, env.store, nil)
}

// ===========================================================================
// Test helpers
// ===========================================================================

// auditQueryTestEnv holds a test echo server with audit query routes.
type auditQueryTestEnv struct {
	*auditTestEnv
	apiGroup *echo.Group
}

// mockSSEManager satisfies SSEBroadcaster for route registration tests.
type mockSSEManager struct{}

// newAuditQueryTestEnv creates a test environment with audit query routes
// registered.
func newAuditQueryTestEnv(t *testing.T) *auditQueryTestEnv {
	t.Helper()

	duckDB := openTestAuditDB(t)
	store := NewStore(duckDB)

	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())

	// Register the audit query, transcript, and SSE routes.
	RegisterAuditQueryRoutes(api, store, &mockSSEManager{})

	return &auditQueryTestEnv{
		auditTestEnv: &auditTestEnv{
			echo:  e,
			db:    duckDB,
			store: store,
		},
		apiGroup: api,
	}
}

// newAuditQueryTestEnvRaw creates a bare test environment without
// registering routes — for testing panic behavior at registration time.
func newAuditQueryTestEnvRaw(t *testing.T) *auditQueryTestEnv {
	t.Helper()

	duckDB := openTestAuditDB(t)
	store := NewStore(duckDB)

	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())

	return &auditQueryTestEnv{
		auditTestEnv: &auditTestEnv{
			echo:  e,
			db:    duckDB,
			store: store,
		},
		apiGroup: api,
	}
}

// containsSSEContentType checks if a Content-Type header contains
// the text/event-stream media type.
func containsSSEContentType(contentType string) bool {
	return contentType == "text/event-stream" ||
		len(contentType) > 18 && contentType[:18] == "text/event-stream;"
}
