package audit

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// newBoundQueryEnv registers the audit query routes with a SQLite handle in
// which u1 owns s and x and token tokB is bound to s, and events of both
// workspaces in DuckDB. It returns the environment and its SSE manager.
func newBoundQueryEnv(t *testing.T) (*auditTestEnv, *SSEManager) {
	t.Helper()
	duckDB := openTestAuditDB(t)
	store := NewStore(duckDB)
	sqliteDB := openTestSQLiteDB(t)
	for _, slug := range []string{"s", "x"} {
		if _, err := sqliteDB.Exec(`INSERT INTO workspaces (slug, owner_id) VALUES (?, 'u1')`, slug); err != nil {
			t.Fatal(err)
		}
	}
	bindToken(t, sqliteDB, "tokB", "s")

	mgr := NewSSEManager(10)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go mgr.Run(done)

	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	RegisterAuditQueryRoutes(api, store, mgr, sqliteDB)

	insertTestHubEvent(t, duckDB, "hub-s", "hub.workspace.updated", "u1", "pat", "workspace", "s", "update", "s", "info", "2026-09-01T10:00:00Z")
	insertTestHubEvent(t, duckDB, "hub-x", "hub.workspace.updated", "u1", "pat", "workspace", "x", "update", "x", "info", "2026-09-01T10:01:00Z")
	insertTestAgentEvent(t, duckDB, "agent-s", testRunID, "s", "session.start", "info", "", "", "", "2026-09-01T10:02:00Z")
	insertTestAgentEvent(t, duckDB, "agent-x", testRunID, "x", "session.start", "info", "", "", "", "2026-09-01T10:03:00Z")

	return &auditTestEnv{echo: e, db: duckDB, sqliteDB: sqliteDB, store: store}, mgr
}

// TS-33-39: GET /audit and GET /events return only S's events for a bound
// token. Requirement: 33-REQ-6.6
func TestAuditQuery_BoundTokenSeesOnlyS_TS_33_39(t *testing.T) {
	env, mgr := newBoundQueryEnv(t)
	bound := boundPatAuth("u1", "tokB", "audit:read")

	rec := env.doJSON(t, http.MethodGet, "/api/v1/audit", "", bound)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /audit: status %d; want 200: %s", rec.Code, rec.Body)
	}
	var resp auditQueryResponse
	parseJSON(t, rec, &resp)
	ids := map[string]bool{}
	for _, e := range resp.Events {
		ids[e["id"].(string)] = true
		if e["workspace"] != "s" {
			t.Errorf("GET /audit returned event %v of workspace %v; want only s", e["id"], e["workspace"])
		}
	}
	if !ids["hub-s"] || !ids["agent-s"] {
		t.Errorf("GET /audit events %v; want both of s's events", ids)
	}
	// The unbound PAT of the same user sees both workspaces.
	rec = env.doJSON(t, http.MethodGet, "/api/v1/audit", "", boundPatAuth("u1", "tokU", "audit:read"))
	parseJSON(t, rec, &resp)
	if len(resp.Events) != 4 {
		t.Errorf("unbound PAT sees %d events; want 4", len(resp.Events))
	}

	// GET /events delivers only s's events: x's is broadcast first and must
	// be filtered out, so the first audit_event frame is s's.
	ts := httptest.NewServer(env.echo)
	defer ts.Close()
	stream, cancel := sseTestRequest(t, ts, "", bound)
	defer cancel()
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("GET /events: status %d; want 200", stream.StatusCode)
	}
	deadline := time.Now().Add(3 * time.Second)
	for mgr.ConnCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	mgr.Broadcast(HubEvent{ID: "live-x", EventType: "hub.workspace.updated", Workspace: "x", Timestamp: "2026-09-01T11:00:00Z"})
	mgr.Broadcast(HubEvent{ID: "live-s", EventType: "hub.workspace.updated", Workspace: "s", Timestamp: "2026-09-01T11:00:01Z"})

	scanner := bufio.NewScanner(stream.Body)
	for {
		frame := readSSEFrame(t, scanner)
		if frame.event == "" {
			t.Fatal("stream ended before an audit_event frame")
		}
		if frame.event != "audit_event" {
			continue
		}
		var ev HubEvent
		if err := json.Unmarshal([]byte(frame.data), &ev); err != nil {
			t.Fatalf("audit_event data %q: %v", frame.data, err)
		}
		if ev.ID != "live-s" || ev.Workspace != "s" {
			t.Errorf("first audit_event = %s of workspace %q; want live-s of s", ev.ID, ev.Workspace)
		}
		break
	}
}

// TS-33-40: GET /audit?workspace=<other> is 404 workspace not found for a
// bound token. Requirement: 33-REQ-6.7
func TestAuditQuery_BoundTokenOtherWorkspace_TS_33_40(t *testing.T) {
	env, _ := newBoundQueryEnv(t)
	bound := boundPatAuth("u1", "tokB", "audit:read")

	rec := env.doJSON(t, http.MethodGet, "/api/v1/audit?workspace=x", "", bound)
	if rec.Code != http.StatusNotFound || errOf(t, rec).Error.Message != "workspace not found" {
		t.Errorf("GET /audit?workspace=x: (%d, %s); want 404 workspace not found", rec.Code, rec.Body)
	}
	if rec := env.doJSON(t, http.MethodGet, "/api/v1/audit?workspace=s", "", bound); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hub-s") {
		t.Errorf("GET /audit?workspace=s: (%d, %s); want 200 with s's events", rec.Code, rec.Body)
	}
	if rec := env.doJSON(t, http.MethodGet, "/api/v1/audit?workspace=x", "", boundPatAuth("u1", "tokU", "audit:read")); rec.Code != http.StatusOK {
		t.Errorf("unbound PAT, workspace=x: status %d; want 200", rec.Code)
	}
}
