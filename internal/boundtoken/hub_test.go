package boundtoken

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/carrypatch"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/cphost"
	"github.com/agent-fox-dev/hub/internal/gitserver"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// realHub is hub's API wired as cmd/af-hub wires it, on a real apikit server,
// SQLite database and DuckDB audit store: workspace handlers and both binding
// guards, the carry-patch module's patch routes, the bound-token routes
// (minting through server.Echo()), the audit session, ingestion and query
// routes, the secrets routes and the git server.
type realHub struct {
	server  *apikit.Server
	db      *sql.DB
	duck    *sql.DB
	store   audit.Store
	emitter audit.Emitter
	apiKey  string // an API key of user u1
}

func newRealHub(t *testing.T) *realHub {
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

	server := apikit.NewServer(cfg, func() error { return nil })
	perms := append(audit.Permissions(), secrets.Permissions()...)
	perms = append(perms, gitserver.GitPermissions()...)
	perms = append(perms, carrypatch.CarryPatchPermissions()...)
	if err := workspace.MountWorkspaceHandlers(server, database, perms...); err != nil {
		t.Fatal(err)
	}
	// The patch routes are the carry-patch module's; they are mounted over
	// the hub's locks and access rule as cmd/af-hub mounts them, since the
	// binding guard's answers are tested through them.
	cphost.Bind()
	carrypatch.RegisterPatchRoutes(server.APIGroup(), carrypatch.APIConfig{
		DB: database.SqlDB, PatchStore: carrypatch.NewSQLPatchStore(database.SqlDB), Audit: cphost.Emitter(emitter),
	})
	RegisterRoutes(server.APIGroup(), Config{DB: database.SqlDB, APIKit: server.Echo(), Audit: emitter})
	workspace.SetAuditDependencies(store, emitter, nil)
	t.Cleanup(func() { workspace.SetAuditDependencies(nil, nil, nil) })
	audit.RegisterSessionRoutes(server.APIGroup(), store, database.SqlDB, nil)
	audit.RegisterRoutes(server.APIGroup(), store, emitter, database.SqlDB)
	audit.RegisterAuditQueryRoutes(server.APIGroup(), store, audit.NewSSEManager(10), database.SqlDB)
	if err := secrets.InitSchema(database.SqlDB); err != nil {
		t.Fatal(err)
	}
	if err := secrets.RegisterRoutes(server.APIGroup(), database.SqlDB); err != nil {
		t.Fatal(err)
	}
	if err := gitserver.MountGitHandlers(server.Echo(), database.SqlDB, t.TempDir()); err != nil {
		t.Fatal(err)
	}

	now := apikit.NowUTC()
	if _, err := database.SqlDB.Exec(`INSERT INTO users (id, username, email, full_name, role, status, provider, provider_id, created_at, updated_at)
		VALUES ('u1', 'u1', 'u1@example.com', '', 'user', 'active', 'github', 'p1', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	key, err := apikit.GenerateAPIKey(database.SqlDB, "u1", 0, echo.New().Logger)
	if err != nil {
		t.Fatal(err)
	}
	return &realHub{server: server, db: database.SqlDB, duck: duck, store: store, emitter: emitter, apiKey: key.FullKey}
}

// do sends one request with credential as the Bearer token.
func (h *realHub) do(method, path, credential, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	rec := httptest.NewRecorder()
	h.server.Echo().ServeHTTP(rec, req)
	return rec
}

// seed inserts a workspace of u1.
func (h *realHub) seed(t *testing.T, slug, status string) {
	t.Helper()
	now := apikit.NowUTC()
	if _, err := h.db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, created_at, updated_at)
		VALUES (?, ?, 'u1', ?, 'ready', ?, ?)`, slug, "https://github.com/org/"+slug, status, now, now); err != nil {
		t.Fatal(err)
	}
}

// mintBound mints a PAT through apikit and binds it to slug directly.
func (h *realHub) mintBound(t *testing.T, slug string, scopes ...string) (token, tokenID string) {
	t.Helper()
	perms, _ := json.Marshal(scopes)
	rec := h.do(http.MethodPost, "/api/v1/user/tokens", h.apiKey, `{"name":"svc","permissions":`+string(perms)+`}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("minting a PAT: (%d, %s)", rec.Code, rec.Body)
	}
	var created struct {
		Token   string `json:"token"`
		TokenID string `json:"token_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if _, err := h.db.Exec(`INSERT INTO token_workspace_bindings (token_id, slug, created_at) VALUES (?, ?, ?)`, created.TokenID, slug, apikit.NowUTC()); err != nil {
		t.Fatal(err)
	}
	return created.Token, created.TokenID
}

// hubEvents returns the hub audit events of eventType stored in DuckDB.
func (h *realHub) hubEvents(t *testing.T, eventType string) []map[string]any {
	t.Helper()
	rows, err := h.duck.QueryContext(context.Background(),
		`SELECT resource_id, workspace, actor_id, actor_type, CAST(metadata AS VARCHAR) FROM hub_audit_events WHERE event_type = ? ORDER BY timestamp, id`, eventType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var resource, ws, actorID, actorType, meta sql.NullString
		if err := rows.Scan(&resource, &ws, &actorID, &actorType, &meta); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(meta.String), &m)
		out = append(out, map[string]any{"resource_id": resource.String, "workspace": ws.String,
			"actor_id": actorID.String, "actor_type": actorType.String, "metadata": m})
	}
	return out
}
