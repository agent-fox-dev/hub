package audit_test

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// Through a real apikit server with the hub's wiring, a personal access
// token minted with the audit and session scopes and bound to a workspace
// writes and reads that workspace's audit data and sessions, sees every
// other workspace as missing, and is confined to its own by the listings.
func TestHub_BoundTokenConfinedToItsWorkspace(t *testing.T) {
	server, database := testutil.Server(t)
	db := database.SqlDB
	duck, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { duck.Close() })
	if err := audit.InitSchema(duck); err != nil {
		t.Fatal(err)
	}
	store := audit.NewStore(duck)
	mgr := audit.NewSSEManager(10)
	if err := workspace.MountWorkspaceHandlers(server, database, audit.Permissions()...); err != nil {
		t.Fatal(err)
	}
	audit.RegisterSessionRoutes(server.APIGroup(), store, db, nil)
	audit.RegisterRoutes(server.APIGroup(), store, audit.NewEmitter(store, mgr), db)
	audit.RegisterAuditQueryRoutes(server.APIGroup(), store, mgr, db)
	e := server.Echo()

	u1 := testutil.SeedUser(t, db, "u1")
	for _, slug := range []string{"s", "x"} {
		testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: slug, OwnerID: u1, Status: "active"})
	}
	apiKey := testutil.APIKey(t, db, u1)
	pat, tokenID := testutil.MintPAT(t, e, apiKey, "workspaces:read", "sessions:write", "audit:write")
	testutil.BindToken(t, db, tokenID, "s")

	for _, c := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"ingest into s", http.MethodPost, "/api/v1/workspaces/s/runs/20260704_143022_c0ffee/events", `{"event_type":"session.start"}`, 201},
		{"ingest into x", http.MethodPost, "/api/v1/workspaces/x/runs/20260704_143022_c0ffee/events", `{"event_type":"session.start"}`, 404},
		{"open in s", http.MethodPost, "/api/v1/sessions", `{"workspace_slug":"s"}`, 201},
		{"open in x", http.MethodPost, "/api/v1/sessions", `{"workspace_slug":"x"}`, 404},
		{"read x", http.MethodGet, "/api/v1/workspaces/x/runs/20260704_143022_c0ffee/events", "", 404},
		{"no credential", http.MethodGet, "/api/v1/audit", "", 401},
	} {
		token := pat
		if c.name == "no credential" {
			token = ""
		}
		if rec := testutil.Bearer(e, c.method, c.path, c.body, token); rec.Code != c.status {
			t.Errorf("%s: (%d, %s); want %d", c.name, rec.Code, rec.Body, c.status)
		}
	}
	rec := testutil.Bearer(e, http.MethodGet, "/api/v1/audit", "", pat)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /audit: (%d, %s)", rec.Code, rec.Body)
	}
	var audited struct {
		Events []map[string]any `json:"events"`
	}
	testutil.Decode(t, rec, &audited)
	if len(audited.Events) == 0 {
		t.Fatal("GET /audit returned no events of s")
	}
	for _, ev := range audited.Events {
		if ev["workspace"] != "s" {
			t.Errorf("GET /audit returned an event of %v", ev["workspace"])
		}
	}
	rec = testutil.Bearer(e, http.MethodGet, "/api/v1/sessions", "", pat)
	var listed audit.SessionListResponse
	testutil.Decode(t, rec, &listed)
	if rec.Code != http.StatusOK || len(listed.Sessions) != 1 || listed.Sessions[0].WorkspaceSlug != "s" {
		t.Errorf("GET /sessions: (%d, %s); want the one session of s", rec.Code, rec.Body)
	}
	if rec := testutil.Bearer(e, http.MethodGet, "/api/v1/workspaces/x/cost", "", apiKey); rec.Code != http.StatusOK {
		t.Errorf("the owner's API key on x: (%d, %s); want 200", rec.Code, rec.Body)
	}
}
