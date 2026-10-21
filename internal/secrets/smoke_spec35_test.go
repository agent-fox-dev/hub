package secrets_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/boundtoken"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// TS-35-59 (smoke): On the real hub (apikit server and schema, workspace,
// secrets and bound-token handlers), a member of the workspace's org gets the
// stranger's 404 on the workspace's secrets and bound tokens, and learns no
// secret name and no token.
// Path: 35-PATH-3. Requirements: 35-REQ-9.2, 35-REQ-9.6
func TestSmoke_MemberGetsStrangers404_TS_35_59(t *testing.T) {
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
	server := apikit.NewServer(cfg, func() error { return nil })
	if err := workspace.MountWorkspaceHandlers(server, database, secrets.Permissions()...); err != nil {
		t.Fatal(err)
	}
	db := database.SqlDB
	if err := secrets.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := secrets.RegisterRoutes(server.APIGroup(), db); err != nil {
		t.Fatal(err)
	}
	boundtoken.RegisterRoutes(server.APIGroup(), boundtoken.Config{DB: db, APIKit: server.Echo()})

	now := apikit.NowUTC()
	keys := map[string]string{}
	for _, u := range []string{"u1", "u2", "u3"} {
		if _, err := db.Exec(`INSERT INTO users (id, username, email, full_name, role, status, provider, provider_id, created_at, updated_at)
			VALUES (?, ?, ?, '', 'user', 'active', 'github', ?, ?, ?)`, u, u, u+"@example.com", "p-"+u, now, now); err != nil {
			t.Fatal(err)
		}
		key, err := apikit.GenerateAPIKey(db, u, 0, echo.New().Logger)
		if err != nil {
			t.Fatal(err)
		}
		keys[u] = key.FullKey
	}
	for _, stmt := range []string{
		`INSERT INTO orgs (id, name, slug, status, created_at, updated_at) VALUES ('org-acme', 'Acme', 'acme', 'active', '` + now + `', '` + now + `')`,
		`INSERT INTO org_members (org_id, user_id, created_at) VALUES ('org-acme', 'u1', '` + now + `'), ('org-acme', 'u2', '` + now + `')`,
		`INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, display_name, description, clone_status, created_at, updated_at)
			VALUES ('team-w', 'https://github.com/acme/w', 'u1', 'org-acme', 'active', 'team-w', '', 'ready', '` + now + `', '` + now + `')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	do := func(method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		server.Echo().ServeHTTP(rec, req)
		return rec
	}
	// The owner stores a secret and mints a bound token through the real routes.
	if rec := do(http.MethodPost, "/api/v1/workspaces/team-w/secrets", keys["u1"], `{"entries":[{"key":"db_password","value":"hunter2"}]}`); rec.Code != http.StatusCreated {
		t.Fatalf("owner secret: %d %s", rec.Code, rec.Body)
	}
	minted := do(http.MethodPost, "/api/v1/workspaces/team-w/tokens", keys["u1"], `{"name":"svc","permissions":["workspaces:read"]}`)
	if minted.Code != http.StatusCreated {
		t.Fatalf("owner mint: %d %s", minted.Code, minted.Body)
	}
	tokenID := strings.SplitN(strings.SplitN(minted.Body.String(), `"token_id":"`, 2)[1], `"`, 2)[0]

	for _, path := range []string{"/api/v1/workspaces/team-w/secrets", "/api/v1/workspaces/team-w/tokens"} {
		member := do(http.MethodGet, path, keys["u2"], "")
		stranger := do(http.MethodGet, path, keys["u3"], "")
		if member.Code != http.StatusNotFound || !strings.Contains(member.Body.String(), `"message":"workspace not found"`) {
			t.Errorf("member GET %s: %d %s; want 404 workspace not found", path, member.Code, member.Body)
		}
		if member.Body.String() != stranger.Body.String() || member.Code != stranger.Code {
			t.Errorf("GET %s: member %d %s, stranger %d %s; want the same answer", path, member.Code, member.Body, stranger.Code, stranger.Body)
		}
		if strings.Contains(member.Body.String(), "db_password") || strings.Contains(member.Body.String(), tokenID) {
			t.Errorf("member GET %s: the body reveals a secret name or a token", path)
		}
	}
	// The owner still sees both.
	if rec := do(http.MethodGet, "/api/v1/workspaces/team-w/tokens", keys["u1"], ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tokenID) {
		t.Errorf("owner tokens: %d %s; want the minted token", rec.Code, rec.Body)
	}
}
