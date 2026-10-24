package audit

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// The run and the per-run routes most tests ingest into and read from.
const (
	runID    = "20260704_143022_a1b2c3"
	runPath  = "/api/v1/workspaces/w/runs/" + runID
	auditDB  = "audit.duckdb"
	sessions = "/api/v1/sessions"
)

// openAuditDB opens a DuckDB database in a temporary directory with the
// audit schema applied.
func openAuditDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), auditDB))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

// workspacesDDL is the part of the workspaces table the access rule and
// testutil.SeedWorkspace use. The workspace package cannot be imported here
// because it imports audit.
const workspacesDDL = `CREATE TABLE IF NOT EXISTS workspaces (
	slug TEXT PRIMARY KEY, git_url TEXT NOT NULL, branch TEXT, owner_id TEXT NOT NULL, org_id TEXT,
	status TEXT NOT NULL DEFAULT 'active', clone_status TEXT NOT NULL DEFAULT 'ready', head_sha TEXT,
	created_at TEXT NOT NULL, updated_at TEXT NOT NULL, workspace_mode TEXT NOT NULL DEFAULT 'standard',
	upstream_url TEXT, integration_branch TEXT, settings TEXT NOT NULL DEFAULT '{}')`

// openHubDB opens SQLite with apikit's schema, the workspaces table and the
// token bindings.
func openHubDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.OpenDB(t)
	testutil.Exec(t, db, workspacesDDL)
	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatalf("InitBindingSchema: %v", err)
	}
	return db
}

// env is the audit API mounted on Echo behind the test auth middleware, with
// its DuckDB store, the hub's SQLite (nil in a bare env), metrics and a
// running SSE manager.
type env struct {
	e       *echo.Echo
	duck    *sql.DB
	sqlite  *sql.DB
	store   Store
	metrics *Metrics
	mgr     *SSEManager
	emitter Emitter
}

// newEnv mounts the session, ingestion and query routes on SQLite with the
// standard tenancy: u1 owns w (active) and w-arch (archived) in org O, of
// which u2 is a member; u2 owns w-own outside the org; u3 owns w-x; the PAT
// tokB of u1 is bound to w.
func newEnv(t *testing.T) *env {
	t.Helper()
	env := mount(t, openHubDB(t))
	db := env.sqlite
	for _, u := range []string{"u1", "u2", "u3"} {
		testutil.SeedUser(t, db, u)
	}
	testutil.SeedOrg(t, db, "O", "o", "u1")
	testutil.SeedOrgMember(t, db, "O", "u1")
	testutil.SeedOrgMember(t, db, "O", "u2")
	seedWS(t, db, "w", "u1", "O", "active")
	seedWS(t, db, "w-arch", "u1", "O", "archived")
	seedWS(t, db, "w-own", "u2", "", "active")
	seedWS(t, db, "w-x", "u3", "", "active")
	testutil.BindToken(t, db, "tokB", "w")
	return env
}

// newBareEnv mounts the routes without SQLite, so the workspace checks are
// skipped.
func newBareEnv(t *testing.T) *env {
	t.Helper()
	return mount(t, nil)
}

func mount(t *testing.T, sqlite *sql.DB) *env {
	t.Helper()
	duck := openAuditDB(t)
	store := NewStore(duck)
	m := NewMetrics()
	SetMetrics(m)
	t.Cleanup(func() { SetMetrics(nil) })
	mgr := NewSSEManager(3)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go mgr.Run(done)
	e, api := testutil.API()
	emitter := NewEmitter(store, mgr)
	RegisterSessionRoutes(api, store, sqlite, m)
	RegisterRoutes(api, store, emitter, sqlite)
	RegisterAuditQueryRoutes(api, store, mgr, sqlite)
	return &env{e: e, duck: duck, sqlite: sqlite, store: store, metrics: m, mgr: mgr, emitter: emitter}
}

// seedWS inserts a workspace with an explicit status (testutil defaults it
// to a clone state, not to active).
func seedWS(t *testing.T, db *sql.DB, slug, owner, org, status string) {
	t.Helper()
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: slug, OwnerID: owner, OrgID: org, Status: status})
}

// Credentials of the standard tenancy.
var (
	admin    = testutil.Admin()
	owner    = testutil.PAT("u1", "sessions:read", "sessions:write", "audit:read", "audit:write")
	member   = testutil.PAT("u2", "sessions:read", "sessions:write", "audit:read", "audit:write")
	stranger = testutil.PAT("u3", "sessions:read", "sessions:write", "audit:read", "audit:write")
	bound    = testutil.BoundPAT("u1", "tokB", "sessions:read", "sessions:write", "audit:read", "audit:write")
	key      = testutil.User("u1")
)

// do serves one request on the env.
func (env *env) do(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Request(t, env.e, method, path, body, auth)
}

// must serves one request that must answer status and returns the recorder.
func (env *env) must(t *testing.T, status int, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	rec := env.do(t, method, path, body, auth)
	if rec.Code != status {
		t.Fatalf("%s %s: (%d, %s); want %d", method, path, rec.Code, rec.Body, status)
	}
	return rec
}

// expect fails unless rec answered status and, when msg is not empty, an
// error envelope with that message.
func expect(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("(%d, %s); want %d", rec.Code, rec.Body, status)
	}
	if msg != "" {
		if got := testutil.Error(t, rec).Message; got != msg {
			t.Errorf("message %q; want %q", got, msg)
		}
	}
}

// decode decodes the JSON body of rec into a value of type T.
func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	testutil.Decode(t, rec, &v)
	return v
}

// count counts the rows of table matching where (all rows when empty).
func (env *env) count(t *testing.T, table, where string, args ...any) int {
	t.Helper()
	q := "SELECT COUNT(*) FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	return testutil.Count(t, env.duck, q, args...)
}

// now is the current UTC time in RFC 3339 form with nanoseconds.
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// daysAgo is the RFC 3339 time n days before now.
func daysAgo(n int) string {
	return time.Now().UTC().Add(-time.Duration(n) * 24 * time.Hour).Format(time.RFC3339Nano)
}

// seedSession inserts a session row. Status defaults to active, StartedAt to
// now, CredentialID to u1 and CredentialType to pat.
func (env *env) seedSession(t *testing.T, s Session) Session {
	t.Helper()
	if s.Status == "" {
		s.Status = "active"
	}
	if s.StartedAt == "" {
		s.StartedAt = now()
	}
	if s.CredentialID == "" {
		s.CredentialID = "u1"
	}
	if s.CredentialType == "" {
		s.CredentialType = "pat"
	}
	testutil.Exec(t, env.duck, `INSERT INTO agent_sessions (id, run_id, workspace_slug, node_id, archetype, status,
		started_at, model, credential_id, credential_type, error_message, ended_at, duration_ms,
		cache_creation_input_tokens, metadata, ingested_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.RunID, s.WorkspaceSlug, s.NodeID, s.Archetype, s.Status, s.StartedAt, s.Model, s.CredentialID,
		s.CredentialType, nilStr(s.ErrorMessage), s.EndedAt, s.DurationMs, s.CacheCreationInputTokens,
		metadataToJSON(s.Metadata), now())
	return s
}

// seedUsage inserts a token_usage row; ReportedAt defaults to now.
func (env *env) seedUsage(t *testing.T, u TokenUsage) {
	t.Helper()
	if u.ReportedAt == "" {
		u.ReportedAt = now()
	}
	testutil.Exec(t, env.duck, `INSERT INTO token_usage (id, session_id, workspace_slug, model, input_tokens,
		output_tokens, cache_read_tokens, reported_at, ingested_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.SessionID, u.WorkspaceSlug, u.Model, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.ReportedAt, now())
}

// agentEvent is an agent_audit_events row to seed.
type agentEvent struct {
	id, run, ws, eventType, severity, nodeID, sessionID, ts string
}

// seedAgentEvent inserts an agent audit event; run defaults to runID, ws to
// w, eventType to session.start, severity to info and ts to now.
func (env *env) seedAgentEvent(t *testing.T, ev agentEvent) {
	t.Helper()
	ev.run = or(ev.run, runID)
	ev.ws = or(ev.ws, "w")
	ev.eventType = or(ev.eventType, "session.start")
	ev.severity = or(ev.severity, "info")
	ev.ts = or(ev.ts, now())
	testutil.Exec(t, env.duck, `INSERT INTO agent_audit_events (id, run_id, workspace, event_type, severity, node_id,
		session_id, timestamp, ingested_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.id, ev.run, ev.ws, ev.eventType, ev.severity, ev.nodeID, ev.sessionID, ev.ts, ev.ts)
}

// seedHubEvent inserts a hub audit event row of workspace ws at ts (now when
// empty) and returns its id.
func (env *env) seedHubEvent(t *testing.T, row HubEventRow) string {
	t.Helper()
	row.EventType = or(row.EventType, "hub.workspace.updated")
	row.Timestamp = or(row.Timestamp, now())
	row.IngestedAt = or(row.IngestedAt, row.Timestamp)
	row.Metadata = or(row.Metadata, "{}")
	if err := env.store.InsertHubEvent(context.Background(), row); err != nil {
		t.Fatalf("InsertHubEvent(%s): %v", row.ID, err)
	}
	return row.ID
}

// seedRow inserts a row of a per-run table (session_outcomes, tool_calls,
// tool_errors or agent_traces) of run runID in w with the given extra
// columns.
func (env *env) seedRow(t *testing.T, table, id, ts string, cols map[string]any) {
	t.Helper()
	ts = or(ts, now())
	names := []string{"id", "run_id", "workspace", "timestamp", "ingested_at"}
	args := []any{id, runID, "w", ts, ts}
	for k, v := range cols {
		names = append(names, k)
		args = append(args, v)
	}
	testutil.Exec(t, env.duck, fmt.Sprintf("INSERT INTO %s (%s) VALUES (?%s)", table,
		strings.Join(names, ", "), strings.Repeat(", ?", len(names)-1)), args...)
}

// seedTrace inserts an agent trace of run runID in w.
func (env *env) seedTrace(t *testing.T, id, nodeID, eventType, ts string, seq int, data string) {
	t.Helper()
	env.seedRow(t, "agent_traces", id, ts, map[string]any{
		"node_id": nodeID, "event_type": eventType, "sequence": seq, "data": or(data, "{}")})
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// uuidN is the n-th of a series of valid UUIDs.
func uuidN(n int) string { return fmt.Sprintf("550e8400-e29b-41d4-a716-%012d", n) }

// ids lists the "id" of each object in list.
func ids(list []map[string]any) []string {
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, fmt.Sprint(m["id"]))
	}
	return out
}

// page is the shape of the paginated list answers of the per-run reads and
// the unified query, decoded with the list under its key.
type page struct {
	Items      []map[string]any
	NextCursor *string
	HasMore    bool
}

// listPage decodes a paginated answer whose list is under key.
func listPage(t *testing.T, rec *httptest.ResponseRecorder, key string) page {
	t.Helper()
	m := testutil.Object(t, rec)
	var p page
	raw, _ := json.Marshal(m[key])
	if err := json.Unmarshal(raw, &p.Items); err != nil {
		t.Fatalf("decoding %s: %v: %s", key, err, rec.Body)
	}
	if c, ok := m["next_cursor"].(string); ok {
		p.NextCursor = &c
	}
	p.HasMore, _ = m["has_more"].(bool)
	return p
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// SSE stream helpers
// ---------------------------------------------------------------------------

// sseFrame is one parsed SSE frame.
type sseFrame struct{ event, data string }

// stream is an open GET /events connection.
type stream struct {
	resp    *http.Response
	scanner *bufio.Scanner
}

// serve serves env over HTTP until the test ends.
func (env *env) serve(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(env.e)
	t.Cleanup(ts.Close)
	return ts
}

// openStream sends GET /events?query to ts as auth and returns the response
// without checking it. The request is cancelled when the test ends.
func openStream(t *testing.T, ts *httptest.Server, query string, auth *apikit.AuthInfo) *stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events"+query, nil)
	if auth != nil {
		raw, _ := json.Marshal(auth)
		req.Header.Set(testutil.AuthHeader, string(raw))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET /events: %v", err)
	}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return &stream{resp: resp, scanner: bufio.NewScanner(resp.Body)}
}

// connect opens a stream that must answer 200 and waits until the manager
// holds want connections.
func (env *env) connect(t *testing.T, ts *httptest.Server, query string, auth *apikit.AuthInfo, want int) *stream {
	t.Helper()
	s := openStream(t, ts, query, auth)
	if s.resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events%s: status %d; want 200", query, s.resp.StatusCode)
	}
	waitFor(t, fmt.Sprintf("%d connections", want), func() bool { return env.mgr.ConnCount() == want })
	return s
}

// next reads the next frame of the stream; a frame without an event marks
// the end of the stream.
func (s *stream) next() sseFrame {
	var f sseFrame
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			f.event = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			f.data = v
		}
	}
	return f
}

// receivedUntil reads audit_event frames until the event with id sentinel
// arrives and returns the ids received before it.
func (s *stream) receivedUntil(t *testing.T, sentinel string) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for {
		f := s.next()
		if f.event == "" {
			t.Fatalf("stream ended before %s; received %v", sentinel, got)
		}
		if f.event != "audit_event" {
			continue
		}
		var ev HubEvent
		if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
			t.Fatalf("audit_event data %q: %v", f.data, err)
		}
		if ev.ID == sentinel {
			return got
		}
		got[ev.ID] = true
	}
}

// refusedStream sends GET /events?query as auth through the recorder, with a
// deadline so that a stream opened by mistake ends.
func (env *env) refusedStream(t *testing.T, query string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, _ := json.Marshal(auth)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events"+query, nil).WithContext(ctx)
	req.Header.Set(testutil.AuthHeader, string(raw))
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	return rec
}

// liveEvent is a hub event of workspace ws with id.
func liveEvent(id, ws string) HubEvent {
	return HubEvent{ID: id, EventType: "hub.workspace.updated", Workspace: ws, Timestamp: "2026-09-01T11:00:00Z"}
}

// failingStore is a Store whose every method fails.
type failingStore struct{ Store }

func (failingStore) InsertHubEvent(context.Context, HubEventRow) error {
	return fmt.Errorf("disk full")
}
