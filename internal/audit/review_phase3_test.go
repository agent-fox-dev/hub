package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Regression tests for the audit findings of the 2026-09 codebase review:
// session metadata round-trip, hub event timestamps in the unified query,
// client timestamp validation, session_id filtering, retention guards and
// metrics wiring.

func TestSessionMetadata_RoundTripThroughDuckDB(t *testing.T) {
	db := openTestAuditDB(t)
	store := NewStore(db)
	ctx := context.Background()

	sess := &Session{
		ID: "sess-meta", WorkspaceSlug: "ws-meta", RunID: "20260901_120000_abcdef",
		Status: "active", StartedAt: "2026-09-01T12:00:00Z",
		Metadata: map[string]any{"task": "review", "attempt": float64(2)},
	}
	if _, _, err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := store.GetSession(ctx, "sess-meta")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	meta, ok := got.Metadata.(map[string]any)
	if !ok || meta["task"] != "review" {
		t.Fatalf("GetSession metadata = %#v, want map with task=review", got.Metadata)
	}

	list, err := store.ListSessions(ctx, SessionListParams{WorkspaceSlug: "ws-meta"}, nil)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list.Sessions) != 1 {
		t.Fatalf("ListSessions returned %d sessions, want 1", len(list.Sessions))
	}
	meta, ok = list.Sessions[0].Metadata.(map[string]any)
	if !ok || meta["task"] != "review" {
		t.Fatalf("ListSessions metadata = %#v, want map with task=review", list.Sessions[0].Metadata)
	}

	// A session created without metadata reads back as nil, not an error.
	if _, _, err := store.CreateSession(ctx, &Session{
		ID: "sess-nometa", WorkspaceSlug: "ws-meta", Status: "active", StartedAt: "2026-09-01T12:00:00Z",
	}); err != nil {
		t.Fatalf("CreateSession (no metadata): %v", err)
	}
	got, err = store.GetSession(ctx, "sess-nometa")
	if err != nil {
		t.Fatalf("GetSession (no metadata): %v", err)
	}
	if got.Metadata != nil {
		t.Errorf("metadata = %#v, want nil", got.Metadata)
	}
}

func TestDecodeMetadata_AcceptsTextAndDecodedShapes(t *testing.T) {
	if v := decodeMetadata(nil); v != nil {
		t.Errorf("nil -> %#v", v)
	}
	if v := decodeMetadata("null"); v != nil {
		t.Errorf("\"null\" -> %#v", v)
	}
	if v := decodeMetadata(`{"a":1}`).(map[string]any); v["a"] != float64(1) {
		t.Errorf("json text -> %#v", v)
	}
	if v := decodeMetadata([]byte(`[1,2]`)).([]any); len(v) != 2 {
		t.Errorf("json bytes -> %#v", v)
	}
	if v := decodeMetadata(map[string]any{"k": "v"}).(map[string]any); v["k"] != "v" {
		t.Errorf("decoded map -> %#v", v)
	}
}

func TestUnifiedQuery_HubEventsCarryTimestampAndOrderWithAgentEvents(t *testing.T) {
	env := newUnifiedQueryTestEnv(t)
	emitter := NewEmitter(env.store)

	// Legacy row written before hub_audit_events.timestamp was populated:
	// falls back to ingested_at instead of breaking the query.
	insertTestHubEvent(t, env.db, "hub-legacy", "hub.user.create", "admin", "admin_token",
		"user", "u1", "create", "", "info", "")
	if _, err := env.db.Exec(`UPDATE hub_audit_events SET ingested_at = ? WHERE id = 'hub-legacy'`,
		"2026-09-01T10:00:00Z"); err != nil {
		t.Fatalf("backdate legacy row: %v", err)
	}

	before := time.Now().UTC().Add(-time.Second)
	if err := emitter.Emit(context.Background(), HubEvent{
		EventType: "hub.workspace.create", ActorID: "u1", ActorType: "api_key",
		ResourceType: "workspace", ResourceID: "ws-1", Action: "create", Workspace: "ws-1",
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	// Agent event one hour in the future sorts first; one from 2026-09-01
	// sorts after the fresh hub event but before the legacy row.
	insertTestAgentEvent(t, env.db, "agent-future", "20260901_120000_abcdef", "ws-1",
		"session.start", "info", "n1", "s1", "coder", after.Add(time.Hour).Format(time.RFC3339Nano))
	insertTestAgentEvent(t, env.db, "agent-old", "20260901_120000_abcdef", "ws-1",
		"session.end", "info", "n1", "s1", "coder", "2026-09-01T12:00:00Z")

	rec := env.doRequest(t, http.MethodGet, auditQueryPath, "", adminAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp auditQueryResponse
	parseJSON(t, rec, &resp)
	if len(resp.Events) != 4 {
		t.Fatalf("got %d events, want 4: %s", len(resp.Events), rec.Body.String())
	}
	field := func(i int, key string) string {
		v, _ := resp.Events[i][key].(string)
		return v
	}
	wantOrder := []string{"agent-future", "", "agent-old", "hub-legacy"}
	for i, want := range wantOrder {
		if want == "" {
			continue
		}
		if got := field(i, "id"); got != want {
			t.Errorf("events[%d].id = %q, want %q", i, got, want)
		}
	}
	if field(1, "source") != "hub" || field(1, "event_type") != "hub.workspace.create" {
		t.Fatalf("events[1] = %+v, want the emitted hub event", resp.Events[1])
	}
	ts, err := time.Parse(time.RFC3339Nano, field(1, "timestamp"))
	if err != nil {
		t.Fatalf("hub timestamp %q is not RFC 3339: %v", field(1, "timestamp"), err)
	}
	if ts.Before(before) || ts.After(after) {
		t.Errorf("hub timestamp %v not within [%v, %v]", ts, before, after)
	}
	for i := range resp.Events {
		if _, err := time.Parse(time.RFC3339Nano, field(i, "timestamp")); err != nil {
			t.Errorf("event %s timestamp %q is not RFC 3339", field(i, "id"), field(i, "timestamp"))
		}
	}

	// since/until and cursor pagination operate on the normalized time.
	rec = env.doRequest(t, http.MethodGet, auditQueryPath+"?since=2026-09-01T11:00:00Z&until=2026-09-01T13:00:00Z", "", adminAuth())
	parseJSON(t, rec, &resp)
	if len(resp.Events) != 1 || field(0, "id") != "agent-old" {
		t.Errorf("since/until window returned %+v, want only agent-old", resp.Events)
	}
	rec = env.doRequest(t, http.MethodGet, auditQueryPath+"?limit=2", "", adminAuth())
	parseJSON(t, rec, &resp)
	if !resp.HasMore || resp.NextCursor == nil {
		t.Fatalf("page 1 has_more=%v cursor=%v", resp.HasMore, resp.NextCursor)
	}
	rec = env.doRequest(t, http.MethodGet, auditQueryPath+"?limit=2&cursor="+*resp.NextCursor, "", adminAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("page 2 status = %d: %s", rec.Code, rec.Body.String())
	}
	parseJSON(t, rec, &resp)
	if len(resp.Events) != 2 || field(0, "id") != "agent-old" || field(1, "id") != "hub-legacy" {
		t.Errorf("page 2 = %+v, want agent-old then hub-legacy", resp.Events)
	}
}

func TestPostEvent_InvalidTimestampRejectedWith400(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodPost, eventsPath,
		`{"event_type":"session.start","timestamp":"yesterday"}`, apiKeyAuth())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("single event status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// Offsets are accepted and normalized to UTC.
	rec = env.doJSON(t, http.MethodPost, eventsPath,
		`{"event_type":"session.start","timestamp":"2026-09-01T14:00:00+02:00"}`, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		t.Fatalf("offset event status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var stored string
	if err := env.db.QueryRow(`SELECT CAST(timestamp AS VARCHAR) FROM agent_audit_events`).Scan(&stored); err != nil {
		t.Fatalf("read stored timestamp: %v", err)
	}
	if stored != "2026-09-01 12:00:00+00" {
		t.Errorf("stored timestamp = %q, want 2026-09-01 12:00:00+00", stored)
	}

	batch := `[{"event_type":"session.start","timestamp":"bad"},{"event_type":"session.end"}]`
	rec = env.doJSON(t, http.MethodPost, eventsBatchPath, batch, apiKeyAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("batch status = %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Accepted int              `json:"accepted"`
		Errors   []BatchItemError `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 1 || len(result.Errors) != 1 || result.Errors[0].Index != 0 {
		t.Errorf("batch result = %+v, want 1 accepted and error at index 0", result)
	}

	rec = env.doJSON(t, http.MethodPost, outcomesPath,
		`{"session_id":"s1","status":"completed","timestamp":"nope"}`, apiKeyAuth())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("session outcome status = %d, want 400", rec.Code)
	}
}

func TestQueryAuditEvents_SessionIDFilter(t *testing.T) {
	db := openTestAuditDB(t)
	store := NewStore(db).(*duckDBStore)
	ctx := context.Background()
	runID := "20260901_120000_abcdef"
	for _, sid := range []string{"s1", "s2", "s1"} {
		if _, _, err := store.InsertAuditEvent(ctx, PostEventRequest{EventType: "session.start", SessionID: sid}, runID, "ws1"); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if _, _, err := store.InsertSessionOutcome(ctx, PostSessionOutcomeRequest{SessionID: sid, Status: "completed"}, runID, "ws1"); err != nil {
			t.Fatalf("insert outcome: %v", err)
		}
	}
	events, _, _, err := store.QueryAuditEvents(ctx, runID, "ws1", QueryParams{SessionID: "s1"})
	if err != nil {
		t.Fatalf("QueryAuditEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("events for s1 = %d, want 2", len(events))
	}
	outcomes, _, _, err := store.QuerySessionOutcomes(ctx, runID, "ws1", QueryParams{SessionID: "s2"})
	if err != nil {
		t.Fatalf("QuerySessionOutcomes: %v", err)
	}
	if len(outcomes) != 1 {
		t.Errorf("outcomes for s2 = %d, want 1", len(outcomes))
	}
}

func TestRetentionStep1_AgesToolAndOutcomeTables(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	old := time.Now().UTC().Add(-100 * 24 * time.Hour).Format(time.RFC3339Nano)
	fresh := time.Now().UTC().Format(time.RFC3339Nano)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO session_outcomes (id, run_id, workspace, session_id, status, timestamp) VALUES ('o-old','r','ws','s','completed',?), ('o-new','r','ws','s','completed',?)`, old, fresh)
	mustExec(`INSERT INTO tool_calls (id, run_id, workspace, tool_name, timestamp) VALUES ('c-old','r','ws','bash',?), ('c-new','r','ws','bash',?)`, old, fresh)
	mustExec(`INSERT INTO tool_errors (id, run_id, workspace, tool_name, error_msg, timestamp) VALUES ('e-old','r','ws','bash','x',?), ('e-new','r','ws','bash','x',?)`, old, fresh)

	deleted, err := RetentionStep1_DeleteAgedAgentRecords(context.Background(), db, 90)
	if err != nil {
		t.Fatalf("step 1: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted = %d, want 3", deleted)
	}
	for _, table := range []string{"session_outcomes", "tool_calls", "tool_errors"} {
		if n := queryTableCount(t, db, table); n != 1 {
			t.Errorf("%s rows = %d, want 1", table, n)
		}
	}
}

func TestRetentionStep8_KeepsWorkspacelessRows(t *testing.T) {
	db := openTestAuditDBWithAllTables(t)
	sqliteDB := openTestSQLiteDB(t)
	old := time.Now().UTC().Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := sqliteDB.Exec(`INSERT INTO workspaces (slug, owner_id) VALUES ('live-ws', 'o')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hub_audit_events (id, event_type, workspace, timestamp, ingested_at)
		VALUES ('global', 'hub.user.create', '', ?, ?), ('orphan', 'hub.workspace.delete', 'gone-ws', ?, ?)`, old, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := RetentionStep8_DeleteOrphanedWorkspaceData(context.Background(), db, sqliteDB, 30); err != nil {
		t.Fatalf("step 8: %v", err)
	}
	if n := queryTableCountWhere(t, db, "hub_audit_events", "workspace = ''"); n != 1 {
		t.Errorf("global rows = %d, want 1 (must not be treated as orphans)", n)
	}
	if n := queryTableCountWhere(t, db, "hub_audit_events", "workspace = 'gone-ws'"); n != 0 {
		t.Errorf("orphan rows = %d, want 0", n)
	}

	// Same guarantee when SQLite lists no workspaces at all.
	if _, err := sqliteDB.Exec(`DELETE FROM workspaces`); err != nil {
		t.Fatal(err)
	}
	if _, err := RetentionStep8_DeleteOrphanedWorkspaceData(context.Background(), db, sqliteDB, 30); err != nil {
		t.Fatalf("step 8 (empty sqlite): %v", err)
	}
	if n := queryTableCountWhere(t, db, "hub_audit_events", "workspace = ''"); n != 1 {
		t.Errorf("global rows after empty-sqlite run = %d, want 1", n)
	}
}

func TestLoadRetentionConfig_RejectsNonPositiveValues(t *testing.T) {
	t.Setenv("AF_AUDIT_ORPHAN_RETENTION_DAYS", "0")
	t.Setenv("AF_AUDIT_MAX_AGE_DAYS", "-5")
	t.Setenv("AF_AUDIT_MAX_RUNS", "abc")
	cfg := LoadRetentionConfigFromEnv()
	def := DefaultRetentionConfig()
	if cfg.OrphanRetentionDays != def.OrphanRetentionDays || cfg.MaxAgeDays != def.MaxAgeDays || cfg.MaxRuns != def.MaxRuns {
		t.Errorf("cfg = %+v, want defaults %+v", cfg, def)
	}
}

func TestMetrics_SSEGaugeAndAuditCounterAreWired(t *testing.T) {
	m := NewMetrics()
	SetMetrics(m)
	t.Cleanup(func() { SetMetrics(nil) })

	mgr := NewSSEManager(5)
	conn, err := mgr.Register(sseFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if v := readGauge(t, m.SSEConnections); v != 1 {
		t.Errorf("sse gauge after register = %v, want 1", v)
	}
	mgr.Unregister(conn.id)
	if v := readGauge(t, m.SSEConnections); v != 0 {
		t.Errorf("sse gauge after unregister = %v, want 0", v)
	}

	db := openTestAuditDB(t)
	if err := NewEmitter(NewStore(db)).Emit(context.Background(), HubEvent{EventType: "hub.workspace.create"}); err != nil {
		t.Fatal(err)
	}
	if v := getCounterValue(t, m.AuditEventsTotal, prometheus.Labels{"source": "hub", "event_type": "hub.workspace.create"}); v != 1 {
		t.Errorf("audit counter = %v, want 1", v)
	}
}

func readGauge(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var pb dto.Metric
	if err := g.Write(&pb); err != nil {
		t.Fatal(err)
	}
	return pb.GetGauge().GetValue()
}
