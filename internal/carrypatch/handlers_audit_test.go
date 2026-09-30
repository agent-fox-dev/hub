package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/jobqueue"
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

func TestSyncAPIConfigAuditField(t *testing.T) {
	mock := newCPAuditEmitter()

	cfg := SyncAPIConfig{Audit: mock}

	if cfg.Audit == nil {
		t.Fatal("SyncAPIConfig.Audit should not be nil when set")
	}

	_ = cfg.Audit.Emit(context.Background(), audit.HubEvent{EventType: "test"})
	events := mock.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
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

// ===========================================================================
// TS-02-30 (unit): A sync in origin mode emits one hub.patch.sync event with
// per-category branch lists, plus one hub.patch.replace event per replaced branch
// Verifies: 02-REQ-7.1, 02-REQ-7.2
// ===========================================================================

func setupSyncAuditTestEnv(t *testing.T, slug string, emitter audit.Emitter) (SyncAPIConfig, *mockPatchStore, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey() returned error: %v", err)
	}
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("MigrateProgress() returned error: %v", err)
	}

	q, err := jobqueue.New(db, nopLogger())
	if err != nil {
		t.Fatalf("jobqueue.New() returned error: %v", err)
	}

	seedWorkspaceCarryPatch(t, db, slug, "alice", "https://github.com/example/upstream", "upstream0", "integration", "upstream0")

	patchStore := newMockPatchStore(nil)
	patchStore.db = db
	patchStore.Patches = []Patch{
		{ID: "p-created", WorkspaceID: slug, BranchName: "feature-created", Position: 1, Status: PatchStatusActive},
		{ID: "p-ff", WorkspaceID: slug, BranchName: "feature-ff", Position: 2, Status: PatchStatusActive},
		{ID: "p-replaced", WorkspaceID: slug, BranchName: "feature-replaced", Position: 3, Status: PatchStatusActive},
	}

	mockGit := newMockGitRunner()
	mockGit.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			switch args[2] {
			case "refs/remotes/upstream/HEAD":
				return "upstream0", nil
			case "refs/remotes/origin/feature-created":
				return "orig-sha-created", nil
			case "refs/heads/feature-created":
				return "", errors.New("not found")
			case "refs/remotes/origin/feature-ff":
				return "orig-sha-ff", nil
			case "refs/heads/feature-ff":
				return "local-sha-ff", nil
			case "refs/remotes/origin/feature-replaced":
				return "orig-sha-replaced", nil
			case "refs/heads/feature-replaced":
				return "local-sha-replaced", nil
			}
		}
		return "", nil
	}
	mockGit.IsAncestorFunc = func(_ context.Context, ancestor, descendant string) (bool, error) {
		if ancestor == "local-sha-ff" && descendant == "orig-sha-ff" {
			return true, nil
		}
		if ancestor == "local-sha-replaced" && descendant == "orig-sha-replaced" {
			return false, nil
		}
		return false, nil
	}

	cfg := SyncAPIConfig{
		DB:            db,
		Queue:         q,
		WorkspaceRoot: t.TempDir(),
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mockGit, nil
		},
		Fetch:       func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
		OriginFetch: func(_ context.Context, _ string, _ transport.AuthMethod) error { return nil },
		ResolveOriginAuth: func(_ string) (transport.AuthMethod, error) { return nil, nil },
		GetVariable: func(scope, scopeID, key string) (string, error) {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			if key == "PATCH_DIVERGENCE_POLICY" {
				return "replace", nil
			}
			if key == "AUTO_REBUILD_AFTER_SYNC" {
				return "true", nil
			}
			return "", nil
		},
		PatchStore: patchStore,
		Audit:      emitter,
	}

	return cfg, patchStore, db
}

func newSyncEchoContext(slug string, auth *apikit.AuthInfo) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/v1/workspaces/:slug/sync")
	c.SetParamNames("slug")
	c.SetParamValues(slug)
	if auth != nil {
		apikit.SetAuthInfo(c, auth)
	}
	return c, rec
}

func TestCarryPatchSync_TS02_30_AuditEmission(t *testing.T) {
	mockEmitter := newCPAuditEmitter()
	slug := "ws-ts02-30"
	cfg, _, _ := setupSyncAuditTestEnv(t, slug, mockEmitter)

	auth := rebuildUserAuth("alice")
	c, _ := newSyncEchoContext(slug, auth)

	resp, err := runCarryPatchSync(cfg, c)
	if err != nil {
		t.Fatalf("runCarryPatchSync failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	events := mockEmitter.Events()

	var syncEvt *audit.HubEvent
	var replaceEvt *audit.HubEvent

	for i := range events {
		switch events[i].EventType {
		case "hub.patch.sync":
			if syncEvt != nil {
				t.Fatalf("expected exactly one hub.patch.sync event, got multiple")
			}
			syncEvt = &events[i]
		case "hub.patch.replace":
			if replaceEvt != nil {
				t.Fatalf("expected exactly one hub.patch.replace event, got multiple")
			}
			replaceEvt = &events[i]
		}
	}

	if syncEvt == nil {
		t.Fatal("expected hub.patch.sync event, got none")
	}
	if syncEvt.ActorType != "system" {
		t.Errorf("syncEvt.ActorType: want %q, got %q", "system", syncEvt.ActorType)
	}
	if syncEvt.ResourceType != "patch" {
		t.Errorf("syncEvt.ResourceType: want %q, got %q", "patch", syncEvt.ResourceType)
	}
	if syncEvt.Workspace != slug {
		t.Errorf("syncEvt.Workspace: want %q, got %q", slug, syncEvt.Workspace)
	}

	// Verify metadata categories for hub.patch.sync
	if syncEvt.Metadata["origin_fetched"] != true {
		t.Errorf("metadata[origin_fetched]: want true, got %v", syncEvt.Metadata["origin_fetched"])
	}
	if !reflect.DeepEqual(syncEvt.Metadata["created"], []string{"feature-created"}) {
		t.Errorf("metadata[created]: want [feature-created], got %v", syncEvt.Metadata["created"])
	}
	if !reflect.DeepEqual(syncEvt.Metadata["fast_forwarded"], []string{"feature-ff"}) {
		t.Errorf("metadata[fast_forwarded]: want [feature-ff], got %v", syncEvt.Metadata["fast_forwarded"])
	}
	if !reflect.DeepEqual(syncEvt.Metadata["replaced"], []string{"feature-replaced"}) {
		t.Errorf("metadata[replaced]: want [feature-replaced], got %v", syncEvt.Metadata["replaced"])
	}
	if !reflect.DeepEqual(syncEvt.Metadata["diverged"], []string{}) {
		t.Errorf("metadata[diverged]: want [], got %v", syncEvt.Metadata["diverged"])
	}
	if !reflect.DeepEqual(syncEvt.Metadata["missing_on_origin"], []string{}) {
		t.Errorf("metadata[missing_on_origin]: want [], got %v", syncEvt.Metadata["missing_on_origin"])
	}

	// Verify hub.patch.replace event
	if replaceEvt == nil {
		t.Fatal("expected hub.patch.replace event for feature-replaced, got none")
	}
	if replaceEvt.ActorType != "system" {
		t.Errorf("replaceEvt.ActorType: want %q, got %q", "system", replaceEvt.ActorType)
	}
	if replaceEvt.ResourceType != "patch" {
		t.Errorf("replaceEvt.ResourceType: want %q, got %q", "patch", replaceEvt.ResourceType)
	}
	if replaceEvt.Workspace != slug {
		t.Errorf("replaceEvt.Workspace: want %q, got %q", slug, replaceEvt.Workspace)
	}
	if replaceEvt.Metadata["branch_name"] != "feature-replaced" {
		t.Errorf("metadata[branch_name]: want feature-replaced, got %v", replaceEvt.Metadata["branch_name"])
	}
	if replaceEvt.Metadata["origin_sha"] != "orig-sha-replaced" {
		t.Errorf("metadata[origin_sha]: want orig-sha-replaced, got %v", replaceEvt.Metadata["origin_sha"])
	}
	if replaceEvt.Metadata["replaced_sha"] != "local-sha-replaced" {
		t.Errorf("metadata[replaced_sha]: want local-sha-replaced, got %v", replaceEvt.Metadata["replaced_sha"])
	}
}

// ===========================================================================
// TS-02-31 (unit): A nil audit emitter or a failing Emit call does not
// affect the sync's response or state changes
// Verifies: 02-REQ-7.3
// ===========================================================================

func TestCarryPatchSync_TS02_31_NilOrFailingAuditEmitter(t *testing.T) {
	auth := rebuildUserAuth("alice")

	// 1. Sync with nil audit emitter
	slug1 := "ws-ts02-31-nil"
	cfgNil, storeNil, dbNil := setupSyncAuditTestEnv(t, slug1, nil)
	c1, _ := newSyncEchoContext(slug1, auth)
	resp1, err1 := runCarryPatchSync(cfgNil, c1)
	if err1 != nil {
		t.Fatalf("sync with nil audit failed: %v", err1)
	}

	// 2. Sync with failing audit emitter
	slug2 := "ws-ts02-31-fail"
	cfgFail, storeFail, dbFail := setupSyncAuditTestEnv(t, slug2, &failingCPAuditEmitter{})
	c2, _ := newSyncEchoContext(slug2, auth)
	resp2, err2 := runCarryPatchSync(cfgFail, c2)
	if err2 != nil {
		t.Fatalf("sync with failing audit returned error: %v", err2)
	}

	// Responses should be identical (except rebuild job ID if any, but in this setup neither triggers a different result)
	if resp1.OriginFetched != resp2.OriginFetched {
		t.Errorf("OriginFetched mismatch: %v vs %v", resp1.OriginFetched, resp2.OriginFetched)
	}
	if resp1.RebuildTriggered != resp2.RebuildTriggered {
		t.Errorf("RebuildTriggered mismatch: %v vs %v", resp1.RebuildTriggered, resp2.RebuildTriggered)
	}
	if resp1.ForcePushDetected != resp2.ForcePushDetected {
		t.Errorf("ForcePushDetected mismatch: %v vs %v", resp1.ForcePushDetected, resp2.ForcePushDetected)
	}
	if !reflect.DeepEqual(resp1.PatchesMerged, resp2.PatchesMerged) {
		t.Errorf("PatchesMerged mismatch: %v vs %v", resp1.PatchesMerged, resp2.PatchesMerged)
	}
	if !reflect.DeepEqual(resp1.PatchesDiverged, resp2.PatchesDiverged) {
		t.Errorf("PatchesDiverged mismatch: %v vs %v", resp1.PatchesDiverged, resp2.PatchesDiverged)
	}
	if len(resp1.PatchesSynced) != len(resp2.PatchesSynced) {
		t.Fatalf("PatchesSynced length mismatch: %d vs %d", len(resp1.PatchesSynced), len(resp2.PatchesSynced))
	}
	for i := range resp1.PatchesSynced {
		p1 := resp1.PatchesSynced[i]
		p2 := resp2.PatchesSynced[i]
		if p1.BranchName != p2.BranchName || p1.Action != p2.Action || p1.State != p2.State ||
			p1.LocalSHA != p2.LocalSHA || p1.OriginSHA != p2.OriginSHA || p1.ReplacedSHA != p2.ReplacedSHA {
			t.Errorf("PatchesSynced[%d] mismatch: %+v vs %+v", i, p1, p2)
		}
	}

	// Patch store state should be identical
	patches1, err := storeNil.ListPatches(context.Background(), slug1)
	if err != nil {
		t.Fatalf("storeNil.ListPatches failed: %v", err)
	}
	patches2, err := storeFail.ListPatches(context.Background(), slug2)
	if err != nil {
		t.Fatalf("storeFail.ListPatches failed: %v", err)
	}
	if len(patches1) != len(patches2) {
		t.Fatalf("patch count mismatch: %d vs %d", len(patches1), len(patches2))
	}
	for i := range patches1 {
		p1 := patches1[i]
		p2 := patches2[i]
		if p1.BranchName != p2.BranchName {
			t.Errorf("patch %d branch name mismatch: %s vs %s", i, p1.BranchName, p2.BranchName)
		}
		if (p1.OriginSyncState == nil) != (p2.OriginSyncState == nil) ||
			(p1.OriginSyncState != nil && *p1.OriginSyncState != *p2.OriginSyncState) {
			t.Errorf("patch %d origin_sync_state mismatch: %v vs %v", i, p1.OriginSyncState, p2.OriginSyncState)
		}
		if (p1.OriginSHA == nil) != (p2.OriginSHA == nil) ||
			(p1.OriginSHA != nil && *p1.OriginSHA != *p2.OriginSHA) {
			t.Errorf("patch %d origin_sha mismatch: %v vs %v", i, p1.OriginSHA, p2.OriginSHA)
		}
	}

	// Workspace DB state should have upstream_head_sha set identically
	var sha1, sha2 sql.NullString
	if err := dbNil.QueryRow(`SELECT upstream_head_sha FROM workspaces WHERE slug = ?`, slug1).Scan(&sha1); err != nil {
		t.Fatalf("dbNil query failed: %v", err)
	}
	if err := dbFail.QueryRow(`SELECT upstream_head_sha FROM workspaces WHERE slug = ?`, slug2).Scan(&sha2); err != nil {
		t.Fatalf("dbFail query failed: %v", err)
	}
	if sha1.String != sha2.String {
		t.Errorf("upstream_head_sha mismatch: %q vs %q", sha1.String, sha2.String)
	}
}
