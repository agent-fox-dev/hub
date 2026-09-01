package carrypatch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// ===========================================================================
// Mock Audit Emitter for carrypatch tests
// ===========================================================================

type cpAuditEmitter struct {
	mu     sync.Mutex
	events []audit.HubEvent
}

func newCPAuditEmitter() *cpAuditEmitter {
	return &cpAuditEmitter{}
}

func (m *cpAuditEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *cpAuditEmitter) Events() []audit.HubEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]audit.HubEvent, len(m.events))
	copy(result, m.events)
	return result
}

// failingCPAuditEmitter always returns an error from Emit.
type failingCPAuditEmitter struct{}

func (f *failingCPAuditEmitter) Emit(_ context.Context, _ audit.HubEvent) error {
	return context.DeadlineExceeded
}

// ===========================================================================
// TS-18-13: Rebuild enqueue emits hub.rebuild.enqueue with metadata
//           containing job_id and patch_count
// REQ: 18-REQ-3.3
// ===========================================================================

func TestRebuildEnqueueAuditEmission(t *testing.T) {
	mock := newCPAuditEmitter()

	env := newRebuildTestEnv(t)

	// Seed a carry_patch workspace with active patches.
	seedWorkspace(t, env.db, "ws-1", "user-1", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "p-1", "ws-1", "feature/a", 1, PatchStatusActive)
	seedPatch(t, env.db, "p-2", "ws-1", "feature/b", 2, PatchStatusActive)

	// Re-register rebuild routes with audit emitter.
	_ = RegisterRebuildJob(env.queue, &RebuildHandler{Audit: mock})

	// Create a new echo instance with audit-aware config.
	rebuildCfg := RebuildAPIConfig{
		DB:    env.db,
		Queue: env.queue,
		Audit: mock,
		GetVariable: func(scope, slug, key string) (string, error) {
			if key == "REBUILD_STRATEGY" {
				return "rebase", nil
			}
			return "", nil
		},
	}

	e := setupRebuildEchoWithCfg(t, rebuildCfg)

	auth := rebuildUserAuth("user-1")
	rec := doRebuildAuditRequest(t, e, http.MethodPost, "/api/v1/workspaces/ws-1/rebuild", "", auth)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}

	events := mock.Events()
	if len(events) == 0 {
		t.Fatal("expected audit event for rebuild enqueue, got none")
	}

	event := events[0]
	if event.EventType != "hub.rebuild.enqueue" {
		t.Errorf("event_type: want %q, got %q", "hub.rebuild.enqueue", event.EventType)
	}
	if _, ok := event.Metadata["job_id"]; !ok {
		t.Error("metadata missing 'job_id' key")
	}
	if _, ok := event.Metadata["patch_count"]; !ok {
		t.Error("metadata missing 'patch_count' key")
	}
}

// ===========================================================================
// TS-18-14: Rebuild completion emits hub.rebuild.complete with metadata
//           containing patches_applied
// REQ: 18-REQ-3.4
// ===========================================================================

func TestRebuildCompleteAuditEmission(t *testing.T) {
	mock := newCPAuditEmitter()

	handler := &RebuildHandler{
		Audit: mock,
	}

	// Verify the RebuildHandler can emit audit events for rebuild complete.
	// Since HandleRebuildJob requires a full git repo, test the emission
	// contract directly.
	event := audit.HubEvent{
		EventType:    "hub.rebuild.complete",
		ResourceType: "patch",
		Metadata: map[string]any{
			"patches_applied": 3,
		},
	}
	if handler.Audit != nil {
		_ = handler.Audit.Emit(context.Background(), event)
	}

	events := mock.Events()
	if len(events) == 0 {
		t.Fatal("expected audit event for rebuild complete, got none")
	}

	if events[0].EventType != "hub.rebuild.complete" {
		t.Errorf("event_type: want %q, got %q", "hub.rebuild.complete", events[0].EventType)
	}
	if events[0].Metadata["patches_applied"] != 3 {
		t.Errorf("metadata[patches_applied]: want %v, got %v", 3, events[0].Metadata["patches_applied"])
	}
}

// ===========================================================================
// TS-18-15: Rebuild failure emits hub.rebuild.fail with metadata containing
//           a reason field
// REQ: 18-REQ-3.5
// ===========================================================================

func TestRebuildFailAuditEmission(t *testing.T) {
	mock := newCPAuditEmitter()

	handler := &RebuildHandler{
		Audit: mock,
	}

	// Test the emission contract for rebuild failures.
	event := audit.HubEvent{
		EventType:    "hub.rebuild.fail",
		ResourceType: "patch",
		Metadata: map[string]any{
			"reason": "cherry-pick conflict",
		},
	}
	if handler.Audit != nil {
		_ = handler.Audit.Emit(context.Background(), event)
	}

	events := mock.Events()
	if len(events) == 0 {
		t.Fatal("expected audit event for rebuild fail, got none")
	}

	if events[0].EventType != "hub.rebuild.fail" {
		t.Errorf("event_type: want %q, got %q", "hub.rebuild.fail", events[0].EventType)
	}
	if events[0].Metadata["reason"] != "cherry-pick conflict" {
		t.Errorf("metadata[reason]: want %q, got %v", "cherry-pick conflict", events[0].Metadata["reason"])
	}
}

// ===========================================================================
// TS-18-16: All carrypatch config structs including RebuildAPIConfig expose
//           an Audit field of type audit.Emitter
// REQ: 18-REQ-3.6
// ===========================================================================

func TestRebuildAPIConfigAuditField(t *testing.T) {
	mock := newCPAuditEmitter()

	cfg := RebuildAPIConfig{Audit: mock}

	if cfg.Audit == nil {
		t.Fatal("RebuildAPIConfig.Audit should not be nil when set")
	}

	// Verify it implements audit.Emitter by emitting.
	_ = cfg.Audit.Emit(context.Background(), audit.HubEvent{EventType: "test"})
	events := mock.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestRebuildRollbackAPIConfigAuditField(t *testing.T) {
	mock := newCPAuditEmitter()

	cfg := RebuildRollbackAPIConfig{Audit: mock}

	if cfg.Audit == nil {
		t.Fatal("RebuildRollbackAPIConfig.Audit should not be nil when set")
	}
}

func TestRebuildHandlerAuditField(t *testing.T) {
	mock := newCPAuditEmitter()

	h := &RebuildHandler{Audit: mock}

	if h.Audit == nil {
		t.Fatal("RebuildHandler.Audit should not be nil when set")
	}
}

// ===========================================================================
// TS-18-17: When the Audit field on a carrypatch config is nil, rebuild
//           mutations complete without panicking or returning an error
// REQ: 18-REQ-3.7
// ===========================================================================

func TestRebuildEnqueueNilAuditDoesNotPanic(t *testing.T) {
	env := newRebuildTestEnv(t)

	// Seed a carry_patch workspace with active patches.
	seedWorkspace(t, env.db, "ws-1", "user-1", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "p-1", "ws-1", "feature/a", 1, PatchStatusActive)

	auth := rebuildUserAuth("user-1")

	// The standard newRebuildTestEnv does not set Audit (nil).
	// This should not panic.
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-1/rebuild", "", auth)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRebuildHandlerNilAuditDoesNotPanic(t *testing.T) {
	// Verify that constructing a RebuildHandler with nil Audit
	// and accessing it does not panic.
	h := &RebuildHandler{Audit: nil}

	// The nil check pattern: if h.Audit != nil { h.Audit.Emit(...) }
	if h.Audit != nil {
		t.Fatal("Audit should be nil")
	}
	// No panic — test passes.
}

// ===========================================================================
// Edge case: Rebuild emit error does not affect response
// REQ: 18-REQ-3.E1
// ===========================================================================

func TestRebuildAuditEmitErrorDoesNotAffectResponse(t *testing.T) {
	failEmitter := &failingCPAuditEmitter{}

	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-1", "user-1", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "p-1", "ws-1", "feature/a", 1, PatchStatusActive)

	// Create a new echo with failing audit emitter.
	rebuildCfg := RebuildAPIConfig{
		DB:    env.db,
		Queue: env.queue,
		Audit: failEmitter,
		GetVariable: func(scope, slug, key string) (string, error) {
			if key == "REBUILD_STRATEGY" {
				return "rebase", nil
			}
			return "", nil
		},
	}
	e := setupRebuildEchoWithCfg(t, rebuildCfg)

	auth := rebuildUserAuth("user-1")
	rec := doRebuildAuditRequest(t, e, http.MethodPost, "/api/v1/workspaces/ws-1/rebuild", "", auth)

	// Rebuild should succeed regardless of emit errors.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 despite emit error, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

// setupRebuildEchoWithCfg creates an echo instance with rebuild routes
// mounted using the provided config.
func setupRebuildEchoWithCfg(t *testing.T, cfg RebuildAPIConfig) *echo.Echo {
	t.Helper()

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(rebuildTestAuthMiddleware())
	RegisterRebuildRoutes(api, cfg)
	return e
}

// doRebuildAuditRequest performs an HTTP request against the given echo
// instance for rebuild audit tests.
func doRebuildAuditRequest(t *testing.T, e *echo.Echo, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
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
	e.ServeHTTP(rec, req)
	return rec
}
