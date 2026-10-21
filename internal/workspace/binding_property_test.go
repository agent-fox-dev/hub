package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/sandboxes"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// sweepSlugs are the workspaces of a sweep environment: s and x owned by
// user-1, a archived and owned by user-1, o owned by user-2, and ghost, which
// does not exist.
var sweepSlugs = []string{"s", "x", "a", "o", "ghost"}

// newSweepEnv is a workspace API on a single-connection database with the
// sweep workspaces. guarded installs the binding guard ahead of the routes;
// bound binds token tokB to s.
func newSweepEnv(t *testing.T, guarded, bound bool) *testEnv {
	t.Helper()
	db := openTestDB(t)
	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatal(err)
	}
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	if guarded {
		api.Use(wsaccess.BindingGuard(db))
	}
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatal(err)
	}
	env := &testEnv{echo: e, db: db}
	seedDefaultPersonalOrgs(t, db)
	for _, w := range []struct{ slug, owner, status string }{
		{"s", "user-1", "active"}, {"x", "user-1", "active"}, {"a", "user-1", "archived"}, {"o", "user-2", "active"},
	} {
		env.seedWorkspace(t, &Workspace{Slug: w.slug, GitURL: "https://github.com/org/" + w.slug, OwnerID: w.owner, Status: w.status})
	}
	if bound {
		bindToken(t, db, "tokB", "s")
	}
	return env
}

// sweepRoute is one registered route of a sweep environment.
type sweepRoute struct{ method, template string }

func (r sweepRoute) path(slug string) string {
	return strings.NewReplacer(":slug", slug, ":id", "p1").Replace(r.template)
}

func (r sweepRoute) mutates() bool { return r.method != http.MethodGet }

// sweepRoutes lists the API routes registered on env, in a stable order.
func sweepRoutes(env *testEnv) []sweepRoute {
	var out []sweepRoute
	for _, r := range env.echo.Routes() {
		switch r.Method {
		case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		default:
			continue
		}
		if strings.HasPrefix(r.Path, "/api/v1/") && !strings.HasSuffix(r.Path, "*") {
			out = append(out, sweepRoute{r.Method, r.Path})
		}
	}
	slices.SortFunc(out, func(a, b sweepRoute) int { return strings.Compare(a.method+a.template, b.method+b.template) })
	return out
}

// generated matches the values a handler generates per call (timestamps and
// UUIDs). Two runs of the same request on two databases differ in them and
// in nothing else, so they are masked before bodies are compared.
var generated = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func masked(rec *httptest.ResponseRecorder) string {
	return generated.ReplaceAllString(rec.Body.String(), "<generated>")
}

// sweep sends one request of r for slug; mutating requests carry {}.
func sweep(t *testing.T, env *testEnv, r sweepRoute, slug string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	body := ""
	if r.method != http.MethodGet && r.method != http.MethodDelete {
		body = "{}"
	}
	return env.doRequest(t, r.method, r.path(slug), body, auth)
}

var sweepScopes = []string{"workspaces:read", "workspaces:write", "workspaces:delete", "patches:read", "patches:write"}

func scopeSubset(mask int) []string {
	var out []string
	for i, s := range sweepScopes {
		if mask&(1<<i) != 0 {
			out = append(out, s)
		}
	}
	return out
}

// TS-33-11: Every enforcing layer completes on a single-connection database
// without deadlock. Requirement: 33-REQ-2.4
func TestBinding_SingleConnectionNoDeadlock_TS_33_11(t *testing.T) {
	db := openTestDB(t) // SetMaxOpenConns(1)
	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"s", "x"} {
		if err := insertWorkspace(db, &Workspace{Slug: slug, GitURL: "https://github.com/org/" + slug, OwnerID: "user-1", Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	bindToken(t, db, "tokB", "s")

	duck, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { duck.Close() })
	if err := audit.InitSchema(duck); err != nil {
		t.Fatal(err)
	}
	store := audit.NewStore(duck)
	mgr := audit.NewSSEManager(1)

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	api.Use(wsaccess.BindingGuard(db))
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatal(err)
	}
	audit.RegisterSessionRoutes(api, store, db, nil)
	audit.RegisterAuditQueryRoutes(api, store, mgr, db)
	env := &testEnv{echo: e, db: db}
	bound := &apikit.AuthInfo{CredentialType: "pat", UserID: "user-1", TokenID: "tokB",
		Permissions: []string{"workspaces:read", "sessions:read", "audit:read"}}

	within := func(name string, want int, call func() int) {
		t.Helper()
		done := make(chan int, 1)
		go func() { done <- call() }()
		select {
		case got := <-done:
			if got != want {
				t.Errorf("%s: %d; want %d", name, got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not return within 5 seconds", name)
		}
	}
	for _, c := range []struct {
		path string
		want int
	}{
		{"/api/v1/workspaces", http.StatusOK},
		{"/api/v1/workspaces/s", http.StatusOK},
		{"/api/v1/workspaces/x", http.StatusNotFound},
		{"/api/v1/sessions", http.StatusOK},
		{"/api/v1/audit", http.StatusOK},
	} {
		within("GET "+c.path, c.want, func() int { return env.doRequest(t, http.MethodGet, c.path, "", bound).Code })
	}
	within("AuthorizeWorkspace(s)", 0, func() int { _, code, _ := wsaccess.AuthorizeWorkspace(db, bound, "s"); return code })
	within("AuthorizeWorkspace(x)", http.StatusNotFound, func() int { _, code, _ := wsaccess.AuthorizeWorkspace(db, bound, "x"); return code })
}

// TS-33-23: A bound token with full scopes is refused on POST /workspaces,
// user and org secrets and vars, and GET /sandboxes, and nothing is written.
// Requirement: 33-REQ-4.4
func TestBoundToken_RefusedOnHubRoutes_TS_33_23(t *testing.T) {
	h := newRealHub(t)
	if err := secrets.InitSchema(h.db.SqlDB); err != nil {
		t.Fatal(err)
	}
	if err := secrets.RegisterRoutes(h.server.APIGroup(), h.db.SqlDB); err != nil {
		t.Fatal(err)
	}
	sandboxes.RegisterLeaseRoutes(h.server.APIGroup(), nil)
	token, id := h.mintPAT(t, "workspaces:create", "workspaces:read", "secrets:manage", "vars:manage", "sandboxes:read")
	bindToken(t, h.db.SqlDB, id, "s")

	counts := func() [3]int {
		var c [3]int
		for i, table := range []string{"workspaces", "secrets", "variables"} {
			if err := h.db.SqlDB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&c[i]); err != nil {
				t.Fatal(err)
			}
		}
		return c
	}
	before := counts()
	for _, r := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/workspaces", `{"slug":"new","git_url":"https://github.com/org/new"}`},
		{http.MethodPost, "/api/v1/user/secrets", `{"secrets":[{"key":"K","value":"v"}]}`},
		{http.MethodGet, "/api/v1/user/vars", ""},
		{http.MethodPost, "/api/v1/orgs/o1/secrets", `{"secrets":[{"key":"K","value":"v"}]}`},
		{http.MethodPatch, "/api/v1/orgs/o1/vars/K", `{"value":"v"}`},
		{http.MethodGet, "/api/v1/sandboxes", ""},
	} {
		rec := h.do(r.method, r.path, token, r.body)
		raw := rec.Body.String()
		if rec.Code != http.StatusForbidden || parseErrorEnvelope(t, rec).Error.ErrorType != "token_bound" {
			t.Errorf("%s %s: (%d, %s); want 403 token_bound", r.method, r.path, rec.Code, raw)
		}
	}
	if after := counts(); after != before {
		t.Errorf("rows (workspaces, secrets, variables) %v after; want %v", after, before)
	}

	// PUT /orgs/:slug/vars/:key matches no route (the route is PATCH): it
	// reaches the API group's catch-all route, behind auth and the guard.
	rec := h.do(http.MethodPut, "/api/v1/orgs/o1/vars/K", token, `{"value":"v"}`)
	raw := rec.Body.String()
	if rec.Code != http.StatusForbidden || parseErrorEnvelope(t, rec).Error.ErrorType != "token_bound" {
		t.Errorf("PUT /api/v1/orgs/o1/vars/K: (%d, %s); want 403 token_bound", rec.Code, raw)
	}
	if after := counts(); after != before {
		t.Errorf("rows (workspaces, secrets, variables) %v after PUT; want %v", after, before)
	}
}

// TS-33-51 (property): For any request by an unbound PAT (with and without a
// token id), an API key, an admin token or no credential, a hub with the
// binding guard answers exactly as one without it. Requirement: 33-REQ-9.1
func TestBinding_PropertyUnboundUnchanged_TS_33_51(t *testing.T) {
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = t.TempDir()
	defer func() { defaultWorkspaceRoot = oldRoot }()

	creds := []struct {
		name string
		auth *apikit.AuthInfo
	}{
		{"pat without token id", patAuth("user-1", sweepScopes...)},
		{"unbound pat", &apikit.AuthInfo{CredentialType: "pat", UserID: "user-1", TokenID: "tokU", Permissions: sweepScopes}},
		{"api_key", userAuth("user-1")},
		{"admin_token", adminAuth()},
		{"none", nil},
	}
	routes := sweepRoutes(newSweepEnv(t, false, false))
	if len(routes) < 15 {
		t.Fatalf("swept %d routes; want every workspace route", len(routes))
	}
	cases := 0
	for _, r := range routes {
		for _, slug := range sweepSlugs {
			for _, c := range creds {
				guarded, plain := newSweepEnv(t, true, false), newSweepEnv(t, false, false)
				g := sweep(t, guarded, r, slug, c.auth)
				p := sweep(t, plain, r, slug, c.auth)
				if g.Code != p.Code || masked(g) != masked(p) {
					t.Errorf("%s %s as %s: guarded (%d, %s); want unguarded (%d, %s)", r.method, r.path(slug), c.name, g.Code, g.Body, p.Code, p.Body)
				}
				cases++
			}
		}
	}
	t.Logf("compared %d requests", cases)
}

// TS-33-52: API key and admin token requests cause no binding query on any
// layer: with the binding table dropped after mount every answer equals the
// one before. Requirement: 33-REQ-9.2
func TestBinding_NoQueryForKeysAndAdmin_TS_33_52(t *testing.T) {
	h := newRealHub(t)
	if err := secrets.InitSchema(h.db.SqlDB); err != nil {
		t.Fatal(err)
	}
	if err := secrets.RegisterRoutes(h.server.APIGroup(), h.db.SqlDB); err != nil {
		t.Fatal(err)
	}
	h.seed(t, "s", "u1", "active")
	adminToken := apikit.TokenPrefix + "_admin_" + strings.Repeat("ab", 32)
	sum := sha256.Sum256([]byte(adminToken))
	if _, err := h.db.SqlDB.Exec(`INSERT OR REPLACE INTO admin_config (key, value) VALUES ('admin_token_hash', ?)`, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}

	requests := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/workspaces"},           // hub, allowlisted
		{http.MethodGet, "/api/v1/workspaces/s"},         // hub, workspace-scoped
		{http.MethodGet, "/api/v1/workspaces/s/patches"}, // hub, workspace-scoped
		{http.MethodGet, "/api/v1/user/secrets"},         // hub, not allowlisted
		{http.MethodGet, "/api/v1/user"},                 // apikit
		{http.MethodGet, "/api/v1/orgs"},                 // apikit
		{http.MethodGet, "/api/v1/no/such/path"},         // unmatched
	}
	creds := map[string]string{"api_key": h.apiKey, "admin_token": adminToken}
	before := map[string]*httptest.ResponseRecorder{}
	for name, cred := range creds {
		for _, r := range requests {
			before[name+r.method+r.path] = h.do(r.method, r.path, cred, "")
		}
	}
	if _, err := h.db.SqlDB.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	for name, cred := range creds {
		for _, r := range requests {
			got := h.do(r.method, r.path, cred, "")
			want := before[name+r.method+r.path]
			if got.Code == http.StatusInternalServerError || got.Code != want.Code || masked(got) != masked(want) {
				t.Errorf("%s %s %s: (%d, %s) after the drop; want (%d, %s) as before", name, r.method, r.path, got.Code, got.Body, want.Code, want.Body)
			}
		}
	}
}

// TS-33-53 (property): A binding never widens a scope: whatever a bound token
// is allowed, an unbound PAT with the same user and scopes is allowed with
// the same answer; on its own workspace the answers are equal.
// Requirement: 33-REQ-9.3
func TestBinding_PropertyNeverWidens_TS_33_53(t *testing.T) {
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = t.TempDir()
	defer func() { defaultWorkspaceRoot = oldRoot }()

	pat := func(tokenID string, scopes []string) *apikit.AuthInfo {
		return &apikit.AuthInfo{CredentialType: "pat", UserID: "user-1", TokenID: tokenID, Permissions: scopes}
	}
	// A 2xx for the bound token is a 2xx with the same status for the unbound
	// PAT (a listing may be narrower for the bound token, never wider). On
	// its own workspace's routes the answers are equal.
	check := func(r sweepRoute, slug string, scopes []string, b, u *httptest.ResponseRecorder) {
		t.Helper()
		if b.Code >= 200 && b.Code < 300 && u.Code != b.Code {
			t.Errorf("%s %s %v: bound %d (%s) but unbound %d (%s)", r.method, r.path(slug), scopes, b.Code, b.Body, u.Code, u.Body)
		}
		if slug == "s" && strings.Contains(r.template, ":slug") && (b.Code != u.Code || masked(b) != masked(u)) {
			t.Errorf("%s %s %v on its own workspace: bound (%d, %s); want the unbound PAT's (%d, %s)", r.method, r.path(slug), scopes, b.Code, b.Body, u.Code, u.Body)
		}
	}

	shared := newSweepEnv(t, true, true)
	routes := sweepRoutes(shared)
	allowedSomewhere := 0
	for _, r := range routes {
		for _, slug := range sweepSlugs {
			if !r.mutates() {
				for mask := 0; mask < 1<<len(sweepScopes); mask++ {
					scopes := scopeSubset(mask)
					b := sweep(t, shared, r, slug, pat("tokB", scopes))
					u := sweep(t, shared, r, slug, pat("tokU", scopes))
					check(r, slug, scopes, b, u)
					if b.Code < 300 {
						allowedSomewhere++
					}
				}
				continue
			}
			for _, mask := range []int{0, 1, 2, 4, 8, 16, 1<<len(sweepScopes) - 1} {
				scopes := scopeSubset(mask)
				b := sweep(t, newSweepEnv(t, true, true), r, slug, pat("tokB", scopes))
				u := sweep(t, newSweepEnv(t, true, true), r, slug, pat("tokU", scopes))
				check(r, slug, scopes, b, u)
				if b.Code < 300 {
					allowedSomewhere++
				}
			}
		}
	}
	if allowedSomewhere == 0 {
		t.Error("the bound token was allowed nothing; the sweep proves nothing")
	}
}

// TS-33-54: A bound token still reaches its archived workspace and gets the
// usual archived responses. Requirement: 33-REQ-9.4
func TestBoundToken_ArchivedWorkspace_TS_33_54(t *testing.T) {
	h := newRealHub(t)
	h.seed(t, "a", "u1", "archived")
	scopes := []string{"workspaces:read", "workspaces:write", "workspaces:sync"}
	bound, id := h.mintPAT(t, scopes...)
	bindToken(t, h.db.SqlDB, id, "a")
	unbound, _ := h.mintPAT(t, scopes...)

	for _, r := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/v1/workspaces/a", `{"display_name":"renamed"}`},
		{http.MethodPost, "/api/v1/workspaces/a/sync", ""},
		{http.MethodPost, "/api/v1/workspaces/a/archive", ""},
	} {
		b := h.do(r.method, r.path, bound, r.body)
		u := h.do(r.method, r.path, unbound, r.body)
		if b.Code != u.Code || b.Body.String() != u.Body.String() {
			t.Errorf("%s %s: bound (%d, %s); want the unbound PAT's (%d, %s)", r.method, r.path, b.Code, b.Body, u.Code, u.Body)
		}
		if b.Code != http.StatusBadRequest && b.Code != http.StatusConflict {
			t.Errorf("%s %s: status %d; want the archived workspace's 400 or 409", r.method, r.path, b.Code)
		}
	}
	if rec := h.do(http.MethodGet, "/api/v1/workspaces/a", bound, ""); rec.Code != http.StatusOK {
		t.Errorf("GET /workspaces/a: status %d; want the handler's 200, not the guard's 404: %s", rec.Code, rec.Body)
	}
}

// TS-33-55: A token whose bound workspace was deleted fails closed on the API
// (the git server half is in internal/gitserver). Requirement: 33-REQ-9.5
func TestBoundToken_DeletedWorkspaceFailsClosed_TS_33_55(t *testing.T) {
	h := newRealHub(t)
	if err := secrets.InitSchema(h.db.SqlDB); err != nil {
		t.Fatal(err)
	}
	if err := secrets.RegisterRoutes(h.server.APIGroup(), h.db.SqlDB); err != nil {
		t.Fatal(err)
	}
	h.seed(t, "s", "u1", "active")
	h.seed(t, "x", "u1", "active")
	token, id := h.mintPAT(t, "workspaces:read", "workspaces:write", "patches:read", "secrets:manage")
	bindToken(t, h.db.SqlDB, id, "s")
	noPatches, noPatchesID := h.mintPAT(t, "workspaces:read")
	bindToken(t, h.db.SqlDB, noPatchesID, "s")
	unboundNoPatches, _ := h.mintPAT(t, "workspaces:read")
	// The workspace row vanishes by a path other than deleteWorkspace, which
	// since spec 34 also revokes and unbinds the tokens: the binding rows
	// stay until the sweep removes them, and the tokens must reach nothing.
	if _, err := h.db.SqlDB.Exec(`DELETE FROM workspaces WHERE slug = 's'`); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := h.db.SqlDB.QueryRow(`SELECT COUNT(*) FROM token_workspace_bindings WHERE slug = 's'`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("binding rows of s after the workspace vanished = %d (%v); want 2", rows, err)
	}

	for _, path := range []string{"/api/v1/workspaces/s", "/api/v1/workspaces/x", "/api/v1/workspaces/s/patches"} {
		rec := h.do(http.MethodGet, path, token, "")
		if rec.Code != http.StatusNotFound || parseErrorEnvelope(t, rec).Error.Message != "workspace not found" {
			t.Errorf("GET %s: status %d; want 404 workspace not found", path, rec.Code)
		}
	}
	// On its own (deleted) slug the guard lets the request through and the
	// route's own checks decide, as for an unbound PAT: a handler that checks
	// its scope first answers the scope refusal (erratum 33, entry 7).
	b := h.do(http.MethodGet, "/api/v1/workspaces/s/patches", noPatches, "")
	u := h.do(http.MethodGet, "/api/v1/workspaces/s/patches", unboundNoPatches, "")
	if b.Code != u.Code || b.Body.String() != u.Body.String() {
		t.Errorf("GET /workspaces/s/patches without patches:read: bound (%d, %s); want the unbound PAT's (%d, %s)", b.Code, b.Body, u.Code, u.Body)
	}

	rec := h.do(http.MethodGet, "/api/v1/user/secrets", token, "")
	if rec.Code != http.StatusForbidden || parseErrorEnvelope(t, rec).Error.ErrorType != "token_bound" {
		t.Errorf("GET /user/secrets: status %d; want 403 token_bound", rec.Code)
	}
	rec = h.do(http.MethodGet, "/api/v1/workspaces", token, "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("GET /workspaces: (%d, %s); want an empty list", rec.Code, rec.Body)
	}
}
