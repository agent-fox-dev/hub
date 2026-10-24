package sandboxes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/carrypatch"
	"github.com/agent-fox-dev/hub/internal/forge"
	"github.com/agent-fox-dev/hub/internal/merge"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// TS-33-41: GET /sandbox-profiles answers a bound token exactly as an unbound
// PAT, behind the binding guard. Requirement: 33-REQ-6.8
func TestSandboxProfiles_BoundTokenAsUnbound_TS_33_41(t *testing.T) {
	db := openLeaseDB(t)
	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO token_workspace_bindings (token_id, slug, created_at) VALUES ('tokB', 's', ?)`, apikit.NowUTC()); err != nil {
		t.Fatal(err)
	}
	svc, err := New(requireValid(t, defaultLeaseConfig()), newFakeProvider())
	if err != nil {
		t.Fatal(err)
	}
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	api.Use(wsaccess.BindingGuard(db))
	RegisterRoutes(api, svc)

	get := func(auth *apikit.AuthInfo) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox-profiles", nil)
		raw, _ := json.Marshal(auth)
		req.Header.Set("X-Test-Auth", string(raw))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	for _, scopes := range [][]string{{"sandboxes:read"}, {}} {
		b := get(&apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokB", Permissions: scopes})
		u := get(&apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokU", Permissions: scopes})
		if b.Code != u.Code || b.Body.String() != u.Body.String() {
			t.Errorf("%v: bound (%d, %s); want the unbound PAT's (%d, %s)", scopes, b.Code, b.Body, u.Code, u.Body)
		}
	}
}

// TS-33-21: After all hub packages register their routes as cmd/af-hub does,
// exactly the ten specified routes are on the bound-token allowlist.
// Requirement: 33-REQ-4.2
func TestBoundTokenAllowlist_ExactlyTheTenRoutes_TS_33_21(t *testing.T) {
	db := openLeaseDB(t)
	// A mount point of its own, so that no other test's marks count.
	const mount = "/allowlist-check"
	e := echo.New()
	api := e.Group(mount)

	if err := workspace.RegisterRoutes(api, db); err != nil {
		t.Fatal(err)
	}
	store := audit.NewStore(nil)
	audit.RegisterSessionRoutes(api, store, db, nil)
	audit.RegisterRoutes(api, store, audit.NewEmitter(store, nil), db)
	audit.RegisterAuditQueryRoutes(api, store, audit.NewSSEManager(1), db)
	if err := secrets.RegisterRoutesWithAudit(api, secrets.SecretsRouteConfig{DB: db}); err != nil {
		t.Fatal(err)
	}
	merge.RegisterMergeRoutes(api, merge.MergeAPIConfig{DB: db})
	RegisterRoutes(api, nil)
	RegisterLeaseRoutes(api, nil)
	forge.RegisterRoutes(api, forge.Config{DB: db})
	carrypatch.RegisterRebuildRoutes(api, carrypatch.RebuildAPIConfig{DB: db})
	carrypatch.RegisterRerereRoutes(api, carrypatch.RerereAPIConfig{DB: db})
	carrypatch.RegisterRebuildRollbackRoutes(api, carrypatch.RebuildRollbackAPIConfig{DB: db})
	carrypatch.RegisterRebuildPreviewRoutes(api, carrypatch.RebuildPreviewAPIConfig{DB: db})
	carrypatch.RegisterPatchStatusRoutes(api, carrypatch.PatchStatusAPIConfig{DB: db})

	var allowed []string
	for _, r := range e.Routes() {
		if wsaccess.IsAllowed(r.Method, r.Path) {
			allowed = append(allowed, r.Method+" "+r.Path[len(mount):])
		}
	}
	sort.Strings(allowed)
	want := []string{
		"GET /audit",
		"GET /events",
		"GET /sandbox-profiles",
		"GET /sessions",
		"GET /sessions/:id",
		"GET /sessions/:id/usage",
		"GET /workspaces",
		"POST /sessions",
		"POST /sessions/:id/complete",
		"POST /sessions/:id/usage",
	}
	if len(allowed) != len(want) {
		t.Fatalf("allowlisted routes = %v; want exactly %v", allowed, want)
	}
	for i := range want {
		if allowed[i] != want[i] {
			t.Errorf("allowlisted routes = %v; want exactly %v", allowed, want)
			break
		}
	}
}
