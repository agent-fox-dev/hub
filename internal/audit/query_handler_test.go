package audit

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// ===========================================================================
// Test infrastructure for unified audit query handler (GET /api/v1/audit)
// Requirements: 18-REQ-6
// ===========================================================================

// auditQueryResponse is the JSON response body for GET /api/v1/audit.
type auditQueryResponse struct {
	Events     []map[string]any `json:"events"`
	NextCursor *string          `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

const auditQueryPath = "/api/v1/audit"

// initUnifiedQuerySchema creates hub_audit_events and agent_audit_events
// tables with all columns required by the unified query handler, including
// timestamp on hub events and archetype on agent events.
func initUnifiedQuerySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	// DROP + CREATE instead of CREATE IF NOT EXISTS because openTestAuditDB →
	// InitSchema already creates these tables without the extra columns
	// (severity, timestamp on hub; archetype on agent). The IF NOT EXISTS
	// variant would be a no-op, leaving the wrong schema in place.
	ddl := []string{
		`DROP TABLE IF EXISTS hub_audit_events`,
		`DROP TABLE IF EXISTS agent_audit_events`,
		`CREATE TABLE hub_audit_events (
			id            VARCHAR PRIMARY KEY,
			event_type    VARCHAR NOT NULL,
			actor_id      VARCHAR NOT NULL DEFAULT '',
			actor_type    VARCHAR NOT NULL DEFAULT '',
			resource_type VARCHAR NOT NULL DEFAULT '',
			resource_id   VARCHAR NOT NULL DEFAULT '',
			action        VARCHAR NOT NULL DEFAULT '',
			workspace     VARCHAR NOT NULL DEFAULT '',
			severity      VARCHAR NOT NULL DEFAULT 'info',
			timestamp     VARCHAR NOT NULL DEFAULT '',
			metadata      VARCHAR NOT NULL DEFAULT '{}',
			ingested_at   VARCHAR NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE agent_audit_events (
			id          VARCHAR PRIMARY KEY,
			run_id      VARCHAR NOT NULL,
			workspace   VARCHAR NOT NULL DEFAULT '',
			event_type  VARCHAR NOT NULL,
			severity    VARCHAR NOT NULL DEFAULT 'info',
			node_id     VARCHAR NOT NULL DEFAULT '',
			session_id  VARCHAR NOT NULL DEFAULT '',
			archetype   VARCHAR NOT NULL DEFAULT '',
			timestamp   VARCHAR NOT NULL DEFAULT '',
			payload     VARCHAR NOT NULL DEFAULT '{}',
			ingested_at VARCHAR NOT NULL DEFAULT ''
		)`,
	}
	for _, stmt := range ddl {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("initUnifiedQuerySchema: %v\nSQL: %s", err, stmt)
		}
	}
}

// newUnifiedQueryTestEnv creates a test environment with audit query routes
// registered and both hub_audit_events and agent_audit_events tables.
func newUnifiedQueryTestEnv(t *testing.T) *auditTestEnv {
	t.Helper()
	duckDB := openTestAuditDB(t)
	initUnifiedQuerySchema(t, duckDB)
	store := NewStore(duckDB)

	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	RegisterAuditQueryRoutes(api, store, &mockSSEManager{})

	return &auditTestEnv{
		echo:  e,
		db:    duckDB,
		store: store,
	}
}

// insertTestHubEvent inserts a hub audit event into hub_audit_events.
func insertTestHubEvent(t *testing.T, db *sql.DB, id, eventType, actorID, actorType, resourceType, resourceID, action, workspace, severity, timestamp string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO hub_audit_events
			(id, event_type, actor_id, actor_type, resource_type, resource_id, action, workspace, severity, timestamp, metadata, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?)`,
		id, eventType, actorID, actorType, resourceType, resourceID, action, workspace, severity, timestamp, now,
	)
	if err != nil {
		t.Fatalf("insertTestHubEvent(%q): %v", id, err)
	}
}

// insertTestAgentEvent inserts an agent audit event into agent_audit_events.
func insertTestAgentEvent(t *testing.T, db *sql.DB, id, runID, workspace, eventType, severity, nodeID, sessionID, archetype, timestamp string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO agent_audit_events
			(id, run_id, workspace, event_type, severity, node_id, session_id, archetype, timestamp, payload, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '{}', ?)`,
		id, runID, workspace, eventType, severity, nodeID, sessionID, archetype, timestamp, now,
	)
	if err != nil {
		t.Fatalf("insertTestAgentEvent(%q): %v", id, err)
	}
}

// ===========================================================================
// 3.1 — GET /api/v1/audit basic query and filtering
// Requirements: 18-REQ-6.1, 18-REQ-6.2, 18-REQ-6.5, 18-REQ-6.6
// Test Spec: TS-18-28, TS-18-29, TS-18-33
// ===========================================================================

// TS-18-28: GET /api/v1/audit with audit:read permission returns HTTP 200
// with events from both hub_audit_events and agent_audit_events ordered by
// timestamp descending.
func TestQueryAudit_UnifiedBothSources_TS18_28(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// Seed 2 hub events at T1 and T3, 1 agent event at T2.
	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestAgentEvent(t, env.db,
		"agent-1", "run-1", "ws-1", "session.start", "info",
		"node-1", "sess-1", "coder", "2026-09-01T13:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.workspace.update", "user-1", "api_key",
		"workspace", "ws-1", "update", "ws-1", "info",
		"2026-09-01T14:00:00Z")

	rec := env.doRequest(t, http.MethodGet, auditQueryPath, "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 3 {
		t.Fatalf("events count = %d, want 3", len(resp.Events))
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
	if resp.NextCursor != nil {
		t.Errorf("next_cursor = %v, want nil", *resp.NextCursor)
	}

	// Verify descending timestamp order (most recent first).
	for i := 0; i < len(resp.Events)-1; i++ {
		ts1, _ := resp.Events[i]["timestamp"].(string)
		ts2, _ := resp.Events[i+1]["timestamp"].(string)
		if ts1 < ts2 {
			t.Errorf("events not in descending order: events[%d].timestamp=%s < events[%d].timestamp=%s",
				i, ts1, i+1, ts2)
		}
	}
}

// TS-18-29: GET /api/v1/audit with source=hub and workspace=ws-1 returns only
// matching hub events for workspace ws-1.
func TestQueryAudit_FilterSourceAndWorkspace_TS18_29(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// Hub events for ws-1 and ws-2.
	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.workspace.create", "user-2", "api_key",
		"workspace", "ws-2", "create", "ws-2", "info",
		"2026-09-01T13:00:00Z")
	// Agent event for ws-1.
	insertTestAgentEvent(t, env.db,
		"agent-1", "run-1", "ws-1", "session.start", "info",
		"node-1", "sess-1", "coder", "2026-09-01T14:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?source=hub&workspace=ws-1", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	for i, ev := range resp.Events {
		src, _ := ev["source"].(string)
		if src != "hub" {
			t.Errorf("events[%d].source = %q, want %q", i, src, "hub")
		}
		ws, _ := ev["workspace"].(string)
		if ws != "ws-1" {
			t.Errorf("events[%d].workspace = %q, want %q", i, ws, "ws-1")
		}
	}
}

// TS-18-29: GET /api/v1/audit with source=agent returns only agent events.
func TestQueryAudit_FilterBySourceAgent(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestAgentEvent(t, env.db,
		"agent-1", "run-1", "ws-1", "session.start", "info",
		"node-1", "sess-1", "coder", "2026-09-01T13:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?source=agent", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 1 {
		t.Fatalf("events count = %d, want 1", len(resp.Events))
	}
	src, _ := resp.Events[0]["source"].(string)
	if src != "agent" {
		t.Errorf("events[0].source = %q, want %q", src, "agent")
	}
}

// 18-REQ-6.2: GET /api/v1/audit with actor_id filter returns only matching events.
func TestQueryAudit_FilterByActorID(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.workspace.update", "user-2", "pat",
		"workspace", "ws-1", "update", "ws-1", "info",
		"2026-09-01T13:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?actor_id=user-1", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 1 {
		t.Fatalf("events count = %d, want 1", len(resp.Events))
	}
	actorID, _ := resp.Events[0]["actor_id"].(string)
	if actorID != "user-1" {
		t.Errorf("actor_id = %q, want %q", actorID, "user-1")
	}
}

// 18-REQ-6.2: GET /api/v1/audit with actor_type filter returns only matching events.
func TestQueryAudit_FilterByActorType(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.workspace.update", "admin-1", "admin_token",
		"workspace", "ws-1", "update", "ws-1", "info",
		"2026-09-01T13:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?actor_type=admin_token", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 1 {
		t.Fatalf("events count = %d, want 1", len(resp.Events))
	}
	actorType, _ := resp.Events[0]["actor_type"].(string)
	if actorType != "admin_token" {
		t.Errorf("actor_type = %q, want %q", actorType, "admin_token")
	}
}

// 18-REQ-6.2: GET /api/v1/audit with resource_type filter returns only matching events.
func TestQueryAudit_FilterByResourceType(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.secret.create", "user-1", "api_key",
		"secret", "s-1", "create", "ws-1", "info",
		"2026-09-01T13:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?resource_type=secret", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 1 {
		t.Fatalf("events count = %d, want 1", len(resp.Events))
	}
	rt, _ := resp.Events[0]["resource_type"].(string)
	if rt != "secret" {
		t.Errorf("resource_type = %q, want %q", rt, "secret")
	}
}

// 18-REQ-6.2: GET /api/v1/audit with action filter returns only matching events.
func TestQueryAudit_FilterByAction(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.workspace.delete", "user-1", "api_key",
		"workspace", "ws-1", "delete", "ws-1", "info",
		"2026-09-01T13:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?action=delete", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 1 {
		t.Fatalf("events count = %d, want 1", len(resp.Events))
	}
	action, _ := resp.Events[0]["action"].(string)
	if action != "delete" {
		t.Errorf("action = %q, want %q", action, "delete")
	}
}

// 18-REQ-6.2: GET /api/v1/audit with event_type exact filter returns only matching events.
func TestQueryAudit_FilterByEventType(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.workspace.update", "user-1", "api_key",
		"workspace", "ws-1", "update", "ws-1", "info",
		"2026-09-01T13:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?event_type=hub.workspace.create", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 1 {
		t.Fatalf("events count = %d, want 1", len(resp.Events))
	}
	et, _ := resp.Events[0]["event_type"].(string)
	if et != "hub.workspace.create" {
		t.Errorf("event_type = %q, want %q", et, "hub.workspace.create")
	}
}

// TS-18-33: GET /api/v1/audit with event_type_prefix=hub.workspace returns
// only events whose event_type starts with "hub.workspace", excluding
// events like hub.merge.enqueue.
func TestQueryAudit_EventTypePrefix_TS18_33(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.workspace.sync", "user-1", "api_key",
		"workspace", "ws-1", "sync", "ws-1", "info",
		"2026-09-01T13:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-3", "hub.merge.enqueue", "user-1", "api_key",
		"merge", "m-1", "enqueue", "ws-1", "info",
		"2026-09-01T14:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?event_type_prefix=hub.workspace", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 2 {
		t.Fatalf("events count = %d, want 2 (hub.workspace.create, hub.workspace.sync)", len(resp.Events))
	}

	for i, ev := range resp.Events {
		et, _ := ev["event_type"].(string)
		if !strings.HasPrefix(et, "hub.workspace") {
			t.Errorf("events[%d].event_type = %q, want prefix %q", i, et, "hub.workspace")
		}
		if et == "hub.merge.enqueue" {
			t.Errorf("events[%d] should not include hub.merge.enqueue", i)
		}
	}
}

// ===========================================================================
// 3.2 — GET /api/v1/audit pagination and time range filtering
// Requirements: 18-REQ-6.2, 18-REQ-6.3, 18-REQ-6.E5, 18-REQ-6.E6
// Test Spec: TS-18-30, TS-18-31, TS-18-32
// ===========================================================================

// TS-18-30: GET /api/v1/audit with 150 hub events and limit=100 returns 100
// events, has_more=true, and a decodable next_cursor with {ts, id} fields.
func TestQueryAudit_CursorPagination_TS18_30(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// Seed 150 hub events with distinct timestamps.
	for i := 0; i < 150; i++ {
		ts := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).
			Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		insertTestHubEvent(t, env.db,
			fmt.Sprintf("hub-%03d", i),
			"hub.workspace.create", "user-1", "api_key",
			"workspace", "ws-1", "create", "ws-1", "info", ts)
	}

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?limit=100", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 100 {
		t.Fatalf("events count = %d, want 100", len(resp.Events))
	}
	if !resp.HasMore {
		t.Error("has_more = false, want true")
	}
	if resp.NextCursor == nil {
		t.Fatal("next_cursor is nil, want non-nil")
	}

	// Decode the cursor and verify it has ts and id fields.
	decoded, err := base64.URLEncoding.DecodeString(*resp.NextCursor)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(*resp.NextCursor)
		if err != nil {
			t.Fatalf("failed to base64-decode next_cursor: %v (value: %q)", err, *resp.NextCursor)
		}
	}

	var cursorObj struct {
		TS string `json:"ts"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal(decoded, &cursorObj); err != nil {
		t.Fatalf("failed to JSON-parse cursor: %v (decoded: %q)", err, string(decoded))
	}
	if cursorObj.TS == "" {
		t.Error("cursor.ts is empty, want a valid timestamp")
	}
	if cursorObj.ID == "" {
		t.Error("cursor.id is empty, want a non-empty identifier")
	}
}

// 18-PROP-2: Using cursor from first page returns next page without
// duplicates or gaps.
func TestQueryAudit_CursorRoundTrip(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// Seed 25 events: mix of hub and agent.
	for i := 0; i < 15; i++ {
		ts := time.Date(2026, 9, 1, 10, i, 0, 0, time.UTC).Format(time.RFC3339)
		insertTestHubEvent(t, env.db,
			fmt.Sprintf("hub-%03d", i),
			"hub.workspace.create", "user-1", "api_key",
			"workspace", "ws-1", "create", "ws-1", "info", ts)
	}
	for i := 0; i < 10; i++ {
		ts := time.Date(2026, 9, 1, 10, 15+i, 0, 0, time.UTC).Format(time.RFC3339)
		insertTestAgentEvent(t, env.db,
			fmt.Sprintf("agent-%03d", i),
			"run-1", "ws-1", "session.start", "info",
			"node-1", "sess-1", "coder", ts)
	}

	// Page 1: limit=10.
	rec1 := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?limit=10", "", adminAuth())
	if rec1.Code != http.StatusOK {
		t.Fatalf("page 1: status = %d, want 200", rec1.Code)
	}
	var page1 auditQueryResponse
	parseJSON(t, rec1, &page1)

	if len(page1.Events) != 10 {
		t.Fatalf("page 1: events count = %d, want 10", len(page1.Events))
	}
	if !page1.HasMore {
		t.Error("page 1: has_more = false, want true")
	}
	if page1.NextCursor == nil {
		t.Fatal("page 1: next_cursor is nil")
	}

	// Page 2 using cursor.
	rec2 := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?limit=10&cursor="+*page1.NextCursor, "", adminAuth())
	if rec2.Code != http.StatusOK {
		t.Fatalf("page 2: status = %d, want 200", rec2.Code)
	}
	var page2 auditQueryResponse
	parseJSON(t, rec2, &page2)

	if len(page2.Events) != 10 {
		t.Fatalf("page 2: events count = %d, want 10", len(page2.Events))
	}
	if !page2.HasMore {
		t.Error("page 2: has_more = false, want true")
	}

	// Page 3.
	rec3 := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?limit=10&cursor="+*page2.NextCursor, "", adminAuth())
	if rec3.Code != http.StatusOK {
		t.Fatalf("page 3: status = %d, want 200", rec3.Code)
	}
	var page3 auditQueryResponse
	parseJSON(t, rec3, &page3)

	if len(page3.Events) != 5 {
		t.Errorf("page 3: events count = %d, want 5", len(page3.Events))
	}
	if page3.HasMore {
		t.Error("page 3: has_more = true, want false")
	}

	// Verify no duplicates across all pages.
	seen := make(map[string]bool)
	for _, page := range []auditQueryResponse{page1, page2, page3} {
		for _, ev := range page.Events {
			id := fmt.Sprintf("%v", ev["id"])
			if seen[id] {
				t.Errorf("duplicate event id %q across pages", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 25 {
		t.Errorf("total unique events = %d, want 25", len(seen))
	}
}

// 18-REQ-6.2: GET /api/v1/audit with since and until returns events in the
// specified time range (since inclusive, until exclusive).
func TestQueryAudit_SinceUntilTimeRange(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// Events at 12:00, 13:00, 14:00, 15:00.
	timestamps := []string{
		"2026-09-01T12:00:00Z",
		"2026-09-01T13:00:00Z",
		"2026-09-01T14:00:00Z",
		"2026-09-01T15:00:00Z",
	}
	for i, ts := range timestamps {
		insertTestHubEvent(t, env.db,
			fmt.Sprintf("hub-%d", i),
			"hub.workspace.create", "user-1", "api_key",
			"workspace", "ws-1", "create", "ws-1", "info", ts)
	}

	// since=13:00 (inclusive), until=15:00 (exclusive) → 13:00 and 14:00 events.
	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?since=2026-09-01T13:00:00Z&until=2026-09-01T15:00:00Z",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 2 {
		t.Fatalf("events count = %d, want 2 (13:00 and 14:00)", len(resp.Events))
	}

	// Verify all returned timestamps are within [since, until).
	for i, ev := range resp.Events {
		ts, _ := ev["timestamp"].(string)
		parsedTime, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			t.Errorf("events[%d]: failed to parse timestamp %q: %v", i, ts, err)
			continue
		}
		since := time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC)
		until := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
		if parsedTime.Before(since) || !parsedTime.Before(until) {
			t.Errorf("events[%d].timestamp = %s, want within [%s, %s)",
				i, ts, since.Format(time.RFC3339), until.Format(time.RFC3339))
		}
	}
}

// 18-REQ-6.3, 18-REQ-6.E6: GET /api/v1/audit with no limit returns at most
// 100 events (default page size).
func TestQueryAudit_DefaultLimit100(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// Seed 150 events.
	for i := 0; i < 150; i++ {
		ts := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).
			Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		insertTestHubEvent(t, env.db,
			fmt.Sprintf("hub-%03d", i),
			"hub.workspace.create", "user-1", "api_key",
			"workspace", "ws-1", "create", "ws-1", "info", ts)
	}

	rec := env.doRequest(t, http.MethodGet, auditQueryPath, "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 100 {
		t.Errorf("events count = %d, want 100 (default page size)", len(resp.Events))
	}
	if !resp.HasMore {
		t.Error("has_more = false, want true when more than 100 events exist")
	}
}

// 18-REQ-6.E5: GET /api/v1/audit with limit exceeding 1000 clamps to 1000.
func TestQueryAudit_LimitClampedTo1000(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// Seed 5 events (fewer than the cap, to verify clamping doesn't error).
	for i := 0; i < 5; i++ {
		ts := time.Date(2026, 9, 1, 12, i, 0, 0, time.UTC).Format(time.RFC3339)
		insertTestHubEvent(t, env.db,
			fmt.Sprintf("hub-%d", i),
			"hub.workspace.create", "user-1", "api_key",
			"workspace", "ws-1", "create", "ws-1", "info", ts)
	}

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?limit=2000", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	// With only 5 events, all should be returned despite limit=2000.
	if len(resp.Events) != 5 {
		t.Errorf("events count = %d, want 5", len(resp.Events))
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
}

// 18-REQ-6.2: GET /api/v1/audit with workspace filter returns only events for
// that workspace.
func TestQueryAudit_WorkspaceFilter(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestAgentEvent(t, env.db,
		"agent-1", "run-1", "ws-1", "session.start", "info",
		"node-1", "sess-1", "coder", "2026-09-01T13:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.workspace.create", "user-2", "api_key",
		"workspace", "ws-2", "create", "ws-2", "info",
		"2026-09-01T14:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?workspace=ws-1", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 2 {
		t.Fatalf("events count = %d, want 2 (hub-1 + agent-1 for ws-1)", len(resp.Events))
	}
	for i, ev := range resp.Events {
		ws, _ := ev["workspace"].(string)
		if ws != "ws-1" {
			t.Errorf("events[%d].workspace = %q, want %q", i, ws, "ws-1")
		}
	}
}

// 18-REQ-6.2: GET /api/v1/audit with run_id filter returns only agent events
// with that run_id.
func TestQueryAudit_RunIDFilter(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// Hub event (no run_id).
	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	// Agent events with different run_ids.
	insertTestAgentEvent(t, env.db,
		"agent-1", "run-1", "ws-1", "session.start", "info",
		"node-1", "sess-1", "coder", "2026-09-01T13:00:00Z")
	insertTestAgentEvent(t, env.db,
		"agent-2", "run-2", "ws-1", "session.start", "info",
		"node-2", "sess-2", "reviewer", "2026-09-01T14:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?run_id=run-1", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 1 {
		t.Fatalf("events count = %d, want 1", len(resp.Events))
	}
	runID, _ := resp.Events[0]["run_id"].(string)
	if runID != "run-1" {
		t.Errorf("run_id = %q, want %q", runID, "run-1")
	}
}

// 18-REQ-6.2: GET /api/v1/audit with severity filter returns only events
// matching the specified severity.
func TestQueryAudit_SeverityFilter(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.merge.fail", "user-1", "api_key",
		"merge", "m-1", "fail", "ws-1", "error",
		"2026-09-01T13:00:00Z")
	insertTestAgentEvent(t, env.db,
		"agent-1", "run-1", "ws-1", "tool.error", "error",
		"node-1", "sess-1", "coder", "2026-09-01T14:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?severity=error", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 2 {
		t.Fatalf("events count = %d, want 2 (hub-2 + agent-1 with severity=error)", len(resp.Events))
	}
	for i, ev := range resp.Events {
		sev, _ := ev["severity"].(string)
		if sev != "error" {
			t.Errorf("events[%d].severity = %q, want %q", i, sev, "error")
		}
	}
}

// ===========================================================================
// 3.3 — GET /api/v1/audit response schema and error handling
// Requirements: 18-REQ-6.4, 18-REQ-6.5, 18-REQ-6.E1–E8
// Test Spec: TS-18-31, TS-18-32
// ===========================================================================

// TS-18-31: Hub events in the unified query response have non-null hub-specific
// fields and null agent-specific fields; agent events have the inverse.
func TestQueryAudit_HubEventFields_TS18_31(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// One hub event and one agent event.
	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestAgentEvent(t, env.db,
		"agent-1", "run-1", "ws-1", "session.start", "info",
		"node-1", "sess-1", "coder", "2026-09-01T13:00:00Z")

	rec := env.doRequest(t, http.MethodGet, auditQueryPath, "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 2 {
		t.Fatalf("events count = %d, want 2", len(resp.Events))
	}

	// Find hub and agent events by source field.
	var hubEvent, agentEvent map[string]any
	for _, ev := range resp.Events {
		src, _ := ev["source"].(string)
		switch src {
		case "hub":
			hubEvent = ev
		case "agent":
			agentEvent = ev
		}
	}

	if hubEvent == nil {
		t.Fatal("no event with source=hub found")
	}
	if agentEvent == nil {
		t.Fatal("no event with source=agent found")
	}

	// Hub event: hub-specific fields must be non-null.
	hubNonNull := []string{"actor_id", "actor_type", "resource_type", "resource_id", "action"}
	for _, field := range hubNonNull {
		if hubEvent[field] == nil {
			t.Errorf("hub event: %s is null, want non-null", field)
		}
	}
	// Hub event: agent-specific fields must be null.
	hubNull := []string{"run_id", "node_id", "session_id", "archetype"}
	for _, field := range hubNull {
		if hubEvent[field] != nil {
			t.Errorf("hub event: %s = %v, want null", field, hubEvent[field])
		}
	}

	// Agent event: agent-specific fields must be non-null.
	agentNonNull := []string{"run_id", "node_id", "session_id"}
	for _, field := range agentNonNull {
		if agentEvent[field] == nil {
			t.Errorf("agent event: %s is null, want non-null", field)
		}
	}
	// Agent event: hub-specific fields must be null.
	agentNull := []string{"actor_id", "actor_type", "resource_type", "resource_id", "action"}
	for _, field := range agentNull {
		if agentEvent[field] != nil {
			t.Errorf("agent event: %s = %v, want null", field, agentEvent[field])
		}
	}
}

// TS-18-32: Every event has the correct source field — hub for hub_audit_events
// rows and agent for agent_audit_events rows.
func TestQueryAudit_SourceFieldAccuracy_TS18_32(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// Seed events in both tables.
	insertTestHubEvent(t, env.db,
		"hub-1", "hub.workspace.create", "user-1", "api_key",
		"workspace", "ws-1", "create", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.workspace.update", "user-1", "api_key",
		"workspace", "ws-1", "update", "ws-1", "info",
		"2026-09-01T13:00:00Z")
	insertTestAgentEvent(t, env.db,
		"agent-1", "run-1", "ws-1", "session.start", "info",
		"node-1", "sess-1", "coder", "2026-09-01T14:00:00Z")
	insertTestAgentEvent(t, env.db,
		"agent-2", "run-2", "ws-1", "tool.use", "info",
		"node-2", "sess-2", "reviewer", "2026-09-01T15:00:00Z")

	rec := env.doRequest(t, http.MethodGet, auditQueryPath, "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 4 {
		t.Fatalf("events count = %d, want 4", len(resp.Events))
	}

	hubCount, agentCount := 0, 0
	for _, ev := range resp.Events {
		src, _ := ev["source"].(string)
		switch src {
		case "hub":
			hubCount++
		case "agent":
			agentCount++
		default:
			t.Errorf("unexpected source = %q, want hub or agent", src)
		}
	}

	if hubCount != 2 {
		t.Errorf("hub event count = %d, want 2", hubCount)
	}
	if agentCount != 2 {
		t.Errorf("agent event count = %d, want 2", agentCount)
	}
}

// 18-REQ-6.E1: Invalid cursor parameter returns HTTP 400 with message
// "invalid cursor".
func TestQueryAudit_InvalidCursor400(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?cursor=not_valid_base64!!!", "", adminAuth())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)

	if errResp.Error.Message != "invalid cursor" {
		t.Errorf("error message = %q, want %q", errResp.Error.Message, "invalid cursor")
	}
}

// 18-REQ-6.E2: DuckDB query failure returns HTTP 500 with a generic error.
func TestQueryAudit_DBFailure500(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// Close the database to simulate a query failure.
	env.db.Close()

	rec := env.doRequest(t, http.MethodGet, auditQueryPath, "", adminAuth())

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// 18-REQ-6.E3: Unauthenticated request returns HTTP 401.
func TestQueryAudit_Unauthenticated401(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	rec := env.doRequest(t, http.MethodGet, auditQueryPath, "", nil)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

// 18-REQ-6.E4: Caller with insufficient scope (PAT without audit:read)
// returns HTTP 403.
func TestQueryAudit_InsufficientScope403(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// PAT with wrong permission scope.
	auth := patAuth("user-1", "sessions:read")
	rec := env.doRequest(t, http.MethodGet, auditQueryPath, "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// 18-REQ-6.E4: Caller with audit:read scope is authorized and gets 200.
func TestQueryAudit_AuditReadScopeAllowed(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	// PAT with audit:read permission should be allowed.
	auth := patAuth("user-1", "audit:read")
	rec := env.doRequest(t, http.MethodGet, auditQueryPath, "", auth)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 for PAT with audit:read, got %d: %s",
			rec.Code, rec.Body.String())
	}
}

// 18-REQ-6.E7: since later than until returns HTTP 400 with message
// "since must be before until".
func TestQueryAudit_SinceAfterUntil400(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?since=2026-09-02T00:00:00Z&until=2026-09-01T00:00:00Z",
		"", adminAuth())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)

	if errResp.Error.Message != "since must be before until" {
		t.Errorf("error message = %q, want %q",
			errResp.Error.Message, "since must be before until")
	}
}

// 18-REQ-6.E8: since parameter with invalid RFC 3339 returns HTTP 400.
func TestQueryAudit_InvalidSinceTimestamp400(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?since=not-a-timestamp", "", adminAuth())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)

	want := "invalid query parameter: since/until must be RFC 3339"
	if errResp.Error.Message != want {
		t.Errorf("error message = %q, want %q", errResp.Error.Message, want)
	}
}

// 18-REQ-6.E8: until parameter with invalid RFC 3339 returns HTTP 400.
func TestQueryAudit_InvalidUntilTimestamp400(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		auditQueryPath+"?until=bad-date", "", adminAuth())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)

	want := "invalid query parameter: since/until must be RFC 3339"
	if errResp.Error.Message != want {
		t.Errorf("error message = %q, want %q", errResp.Error.Message, want)
	}
}

// 18-REQ-6.E6: No filters, no cursor, empty tables returns HTTP 200 with
// empty events array, has_more=false, next_cursor=null.
func TestQueryAudit_NoFiltersEmptyResult(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)

	rec := env.doRequest(t, http.MethodGet, auditQueryPath, "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp auditQueryResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 0 {
		t.Errorf("events count = %d, want 0", len(resp.Events))
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
	if resp.NextCursor != nil {
		t.Errorf("next_cursor = %v, want nil", *resp.NextCursor)
	}
}
