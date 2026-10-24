package audit_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// startSmokeHub runs hub's API wired as cmd/af-hub wires it (workspace
// handlers first, then the audit routes) on a real apikit server, SQLite and
// DuckDB, with user u1 owning s and x and one hub event in each. It returns the
// running server, the SQLite handle and an API key of u1.
func startSmokeHub(t *testing.T) (*httptest.Server, *apikit.DB, string) {
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
	emitter := audit.NewEmitter(store, nil)

	// The wiring of cmd/af-hub: workspace handlers first, then audit.
	server := apikit.NewServer(cfg, func() error { return nil })
	if err := workspace.MountWorkspaceHandlers(server, database, audit.Permissions()...); err != nil {
		t.Fatal(err)
	}
	audit.RegisterSessionRoutes(server.APIGroup(), store, database.SqlDB, nil)
	audit.RegisterRoutes(server.APIGroup(), store, emitter, database.SqlDB)
	audit.RegisterAuditQueryRoutes(server.APIGroup(), store, audit.NewSSEManager(10), database.SqlDB)

	now := apikit.NowUTC()
	if _, err := database.SqlDB.Exec(`INSERT INTO users (id, username, email, full_name, role, status, provider, provider_id, created_at, updated_at)
		VALUES ('u1', 'u1', 'u1@example.com', '', 'user', 'active', 'github', 'p1', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"s", "x"} {
		if _, err := database.SqlDB.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, status, created_at, updated_at)
			VALUES (?, ?, 'u1', 'ready', ?, ?)`, slug, "https://github.com/org/"+slug, now, now); err != nil {
			t.Fatal(err)
		}
		if err := emitter.Emit(context.Background(), audit.HubEvent{EventType: "hub.workspace.updated", ActorID: "u1", ActorType: "api_key",
			ResourceType: "workspace", ResourceID: slug, Action: "update", Workspace: slug}); err != nil {
			t.Fatal(err)
		}
	}
	key, err := apikit.GenerateAPIKey(database.SqlDB, "u1", 0, echo.New().Logger)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.Echo())
	t.Cleanup(srv.Close)
	return srv, database, key.FullKey
}

// TS-33-59 (smoke, 33-PATH-4): A bound token discovers its workspace, reports
// a session and reads its audit stream through the running hub. Real: apikit
// server and auth middleware, binding guard, workspace and audit handlers,
// SQLite, the DuckDB audit store.
func TestSmoke_BoundTokenSessionsAndAudit_TS_33_59(t *testing.T) {
	srv, database, apiKey := startSmokeHub(t)
	call := func(method, path, credential, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+credential)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	code, body := call(http.MethodPost, "/api/v1/user/tokens", apiKey, `{"name":"svc","permissions":["workspaces:read","sessions:write","audit:read"]}`)
	var pat struct {
		Token   string `json:"token"`
		TokenID string `json:"token_id"`
	}
	if err := json.Unmarshal([]byte(body), &pat); code != http.StatusCreated || err != nil {
		t.Fatalf("minting the PAT: (%d, %s)", code, body)
	}
	if _, err := database.SqlDB.Exec(`INSERT INTO token_workspace_bindings (token_id, slug, created_at) VALUES (?, 's', ?)`, pat.TokenID, apikit.NowUTC()); err != nil {
		t.Fatal(err)
	}

	code, body = call(http.MethodGet, "/api/v1/workspaces", pat.Token, "")
	var list []struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal([]byte(body), &list); code != http.StatusOK || err != nil || len(list) != 1 || list[0].Slug != "s" {
		t.Errorf("GET /workspaces: (%d, %s); want only s", code, body)
	}

	code, body = call(http.MethodPost, "/api/v1/sessions", pat.Token, `{"workspace_slug":"s"}`)
	if code != http.StatusCreated {
		t.Errorf("POST /sessions in s: (%d, %s); want 201", code, body)
	}
	if code, body := call(http.MethodPost, "/api/v1/sessions", pat.Token, `{"workspace_slug":"x"}`); code != http.StatusNotFound {
		t.Errorf("POST /sessions in x: (%d, %s); want 404", code, body)
	}

	code, body = call(http.MethodGet, "/api/v1/audit", pat.Token, "")
	var events struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal([]byte(body), &events); code != http.StatusOK || err != nil || len(events.Events) == 0 {
		t.Fatalf("GET /audit: (%d, %s); want 200 with s's events", code, body)
	}
	for _, e := range events.Events {
		if e["workspace"] != "s" {
			t.Errorf("GET /audit returned an event of workspace %v; want only s", e["workspace"])
		}
	}
}
