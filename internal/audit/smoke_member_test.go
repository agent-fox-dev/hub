package audit_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// memberHub is hub's API wired as cmd/af-hub wires it, on a real apikit
// server, SQLite and DuckDB, with org acme holding u1 and u2, u1 owning
// team-w in acme, u2 owning own-w outside it, and u3 a stranger.
type memberHub struct {
	srv     *httptest.Server
	duck    *sql.DB
	emitter audit.Emitter
	mgr     *audit.SSEManager
	keys    map[string]string
}

// smokeRunID is the run whose audit data the owner ingests into team-w.
const smokeRunID = "20260704_143022_c0ffee"

// startMemberHub starts a memberHub.
func startMemberHub(t *testing.T) *memberHub {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	config := "[server]\nport = 0\n\n[database]\npath = \"" + filepath.Join(dir, "hub.db") + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := apikit.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	database, err := apikit.OpenDatabase(cfg.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	duck, err := audit.OpenDB(filepath.Join(dir, "audit.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { duck.Close() })
	if err := audit.InitSchema(duck); err != nil {
		t.Fatal(err)
	}
	store := audit.NewStore(duck)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := audit.NewSSEManager(10)
	go mgr.Run(ctx.Done())
	emitter := audit.NewEmitterWithBroadcast(store, mgr)

	// The wiring of cmd/af-hub: workspace handlers first, then audit.
	server := apikit.NewServer(cfg, func() error { return nil })
	if err := workspace.MountWorkspaceHandlers(server, database, audit.Permissions()...); err != nil {
		t.Fatal(err)
	}
	audit.RegisterSessionRoutes(server.APIGroup(), store, database.SqlDB, nil)
	audit.RegisterRoutes(server.APIGroup(), store, emitter, database.SqlDB)
	audit.RegisterAuditQueryRoutes(server.APIGroup(), store, mgr, database.SqlDB)

	now := apikit.NowUTC()
	h := &memberHub{duck: duck, emitter: emitter, mgr: mgr, keys: map[string]string{}}
	for _, u := range []string{"u1", "u2", "u3"} {
		if _, err := database.SqlDB.Exec(`INSERT INTO users (id, username, email, full_name, role, status, provider, provider_id, created_at, updated_at)
			VALUES (?, ?, ?, '', 'user', 'active', 'github', ?, ?, ?)`, u, u, u+"@example.com", "p-"+u, now, now); err != nil {
			t.Fatal(err)
		}
		key, err := apikit.GenerateAPIKey(database.SqlDB, u, 0, echo.New().Logger)
		if err != nil {
			t.Fatal(err)
		}
		h.keys[u] = key.FullKey
	}
	for _, stmt := range []string{
		`INSERT INTO orgs (id, name, slug, status, created_at, updated_at) VALUES ('org-acme', 'Acme', 'acme', 'active', '` + now + `', '` + now + `')`,
		`INSERT INTO org_members (org_id, user_id, created_at) VALUES ('org-acme', 'u1', '` + now + `'), ('org-acme', 'u2', '` + now + `')`,
		`INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, clone_status, created_at, updated_at)
			VALUES ('team-w', 'https://github.com/acme/w', 'u1', 'org-acme', 'active', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, created_at, updated_at)
			VALUES ('own-w', 'https://github.com/u2/w', 'u2', 'active', 'ready', '` + now + `', '` + now + `')`,
	} {
		if _, err := database.SqlDB.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	h.srv = httptest.NewServer(server.Echo())
	t.Cleanup(h.srv.Close)
	return h
}

// call sends a request with user's API key and returns status and body.
func (h *memberHub) call(t *testing.T, method, path, user, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.keys[user])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// must sends a request that must answer want.
func (h *memberHub) must(t *testing.T, want int, method, path, user, body string) string {
	t.Helper()
	code, got := h.call(t, method, path, user, body)
	if code != want {
		t.Fatalf("%s %s as %s: (%d, %s); want %d", method, path, user, code, got, want)
	}
	return got
}

// ownerOpensSession has u1 ingest an event of smokeRunID into team-w, open
// session sess-team there and report usage on it, all through the routes.
func (h *memberHub) ownerOpensSession(t *testing.T) {
	t.Helper()
	h.must(t, http.StatusCreated, http.MethodPost, "/api/v1/workspaces/team-w/runs/"+smokeRunID+"/events", "u1",
		`{"event_type":"session.start","payload":{"note":"owner-run"}}`)
	h.must(t, http.StatusCreated, http.MethodPost, "/api/v1/sessions", "u1", `{"id":"sess-team","workspace_slug":"team-w"}`)
	h.must(t, http.StatusCreated, http.MethodPost, "/api/v1/sessions/sess-team/usage", "u1", `{"model":"m","input_tokens":42,"output_tokens":5}`)
}

// TS-36-47 (smoke, 36-PATH-1): a member reads a teammate's run audit,
// sessions and cost through the running hub. Real: apikit server and auth,
// the binding guard, audit handlers and routes, wsaccess, SQLite with
// org_members, the DuckDB audit store.
func TestSmoke_MemberReadsRunSessionsAndCost_TS_36_47(t *testing.T) {
	h := startMemberHub(t)
	h.ownerOpensSession(t)

	body := h.must(t, http.StatusOK, http.MethodGet, "/api/v1/workspaces/team-w/runs/"+smokeRunID+"/events", "u2", "")
	var events struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal([]byte(body), &events); err != nil || len(events.Events) != 1 || events.Events[0]["event_type"] != "session.start" {
		t.Errorf("run events: %s; want the owner's session.start", body)
	}
	body = h.must(t, http.StatusOK, http.MethodGet, "/api/v1/sessions?workspace_slug=team-w", "u2", "")
	var list audit.SessionListResponse
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Sessions) != 1 || list.Sessions[0].ID != "sess-team" {
		t.Errorf("session list: %s; want sess-team", body)
	}
	body = h.must(t, http.StatusOK, http.MethodGet, "/api/v1/workspaces/team-w/cost?since=2000-01-01T00:00:00Z&until=2100-01-01T00:00:00Z", "u2", "")
	var cost audit.CostResponse
	if err := json.Unmarshal([]byte(body), &cost); err != nil || cost.Workspace != "team-w" || cost.Totals.InputTokens != 42 {
		t.Errorf("cost: %s; want team-w's 42 input tokens", body)
	}
	// A stranger is still refused on each.
	for path, want := range map[string]int{
		"/api/v1/workspaces/team-w/runs/" + smokeRunID + "/events": http.StatusNotFound,
		"/api/v1/sessions?workspace_slug=team-w":                   http.StatusForbidden,
		"/api/v1/workspaces/team-w/cost":                           http.StatusForbidden,
	} {
		if code, body := h.call(t, http.MethodGet, path, "u3", ""); code != want {
			t.Errorf("stranger %s: (%d, %s); want %d", path, code, body, want)
		}
	}
}

// TS-36-48 (smoke, 36-PATH-2): a member sees the workspace in the unified
// query and on the live stream. Real: audit handlers, the SSE manager, the
// event emitter, wsaccess.ReadableSlugs, SQLite.
func TestSmoke_MemberSeesQueryAndStream_TS_36_48(t *testing.T) {
	h := startMemberHub(t)
	ctx := context.Background()
	for _, ev := range []audit.HubEvent{
		{EventType: "hub.workspace.updated", ActorID: "u1", ActorType: "api_key", ResourceType: "workspace", ResourceID: "team-w", Action: "update", Workspace: "team-w"},
		{EventType: "hub.workspace.updated", ActorID: "u2", ActorType: "api_key", ResourceType: "workspace", ResourceID: "own-w", Action: "update", Workspace: "own-w"},
		{EventType: "hub.user.updated", ActorID: "u1", ActorType: "api_key", ResourceType: "user", ResourceID: "u1", Action: "update"},
	} {
		if err := h.emitter.Emit(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	body := h.must(t, http.StatusOK, http.MethodGet, "/api/v1/audit", "u2", "")
	var resp struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	seen := map[any]bool{}
	for _, e := range resp.Events {
		seen[e["workspace"]] = true
	}
	if len(seen) != 2 || !seen["team-w"] || !seen["own-w"] {
		t.Errorf("GET /audit workspaces %v; want team-w and own-w only, no event without a workspace", seen)
	}

	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(sctx, http.MethodGet, h.srv.URL+"/api/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+h.keys["u2"])
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("GET /events: %d", stream.StatusCode)
	}
	for deadline := time.Now().Add(3 * time.Second); h.mgr.ConnCount() != 1 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if err := h.emitter.Emit(ctx, audit.HubEvent{EventType: "hub.workspace.synced", ActorID: "u1", ActorType: "api_key",
		ResourceType: "workspace", ResourceID: "team-w", Action: "sync", Workspace: "team-w"}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stream.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || !strings.Contains(line, `"event_type":"hub.workspace.synced"`) {
			continue
		}
		if !strings.Contains(line, `"workspace":"team-w"`) {
			t.Errorf("streamed %s; want team-w's event", line)
		}
		return
	}
	t.Fatal("the stream ended before team-w's event arrived")
}

// TS-36-49 (smoke, 36-PATH-3): a member's attempts to open or complete
// sessions are refused and change nothing. Real: audit handlers,
// wsaccess.AuthorizeWorkspace, SQLite, the DuckDB audit store.
func TestSmoke_MemberWritesRefused_TS_36_49(t *testing.T) {
	h := startMemberHub(t)
	h.ownerOpensSession(t)
	count := func() int {
		var n int
		if err := h.duck.QueryRow(`SELECT COUNT(*) FROM agent_sessions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()

	code, body := h.call(t, http.MethodPost, "/api/v1/sessions", "u2", `{"workspace_slug":"team-w"}`)
	if code != http.StatusForbidden || !strings.Contains(body, `"error_type":"forbidden"`) {
		t.Errorf("POST /sessions as member: (%d, %s); want 403 forbidden", code, body)
	}
	code, body = h.call(t, http.MethodPost, "/api/v1/sessions/sess-team/complete", "u2", `{"status":"completed"}`)
	if code != http.StatusForbidden || !strings.Contains(body, `"message":"not session owner"`) {
		t.Errorf("complete as member: (%d, %s); want 403 not session owner", code, body)
	}
	if after := count(); after != before {
		t.Errorf("session rows %d -> %d; want unchanged", before, after)
	}
	var status string
	if err := h.duck.QueryRow(`SELECT status FROM agent_sessions WHERE id = 'sess-team'`).Scan(&status); err != nil || status != "active" {
		t.Errorf("sess-team status %q (%v); want active", status, err)
	}
}
