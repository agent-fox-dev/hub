package workspace

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// sqliteMasterRowid returns the rowid of the named object in sqlite_master.
func sqliteMasterRowid(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	var rowid int64
	if err := db.QueryRow(`SELECT rowid FROM sqlite_master WHERE name = ?`, name).Scan(&rowid); err != nil {
		t.Fatalf("sqlite_master entry %s: %v", name, err)
	}
	return rowid
}

// TS-33-3: MountWorkspaceHandlers creates the binding table before the first
// request is served. Requirement: 33-REQ-1.3
func TestMountWorkspaceHandlers_CreatesBindingTable_TS_33_3(t *testing.T) {
	server, database := openRealServer(t)
	var before int
	if err := database.SqlDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'token_workspace_bindings'`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("binding table before mount: %d (%v); want absent", before, err)
	}
	h := mountRealHub(t, server, database)

	if got, want := sqliteMasterRowid(t, database.SqlDB, "token_workspace_bindings"), sqliteMasterRowid(t, database.SqlDB, "workspaces"); got <= want {
		t.Errorf("token_workspace_bindings rowid %d; want after workspaces (%d)", got, want)
	}

	// The first request of a bound token is served, not a missing-table 500.
	h.seed(t, "s", "u1", "active")
	token, id := h.mintPAT(t, "workspaces:read")
	bindToken(t, database.SqlDB, id, "s")
	if rec := h.do(http.MethodGet, "/api/v1/workspaces/s", token, ""); rec.Code != http.StatusOK {
		t.Errorf("first bound request: status %d; want 200: %s", rec.Code, rec.Body)
	}
}

// TS-33-6: Archiving or deleting a workspace leaves every binding row that
// names its slug unchanged. Requirement: 33-REQ-1.6
func TestArchiveDelete_KeepBindings_TS_33_6(t *testing.T) {
	env := newTestEnv(t)
	if err := wsaccess.InitBindingSchema(env.db); err != nil {
		t.Fatal(err)
	}
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = t.TempDir()
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env.seedWorkspace(t, &Workspace{Slug: "s", GitURL: "https://github.com/org/repo", OwnerID: "user-1", Status: "active"})
	bindToken(t, env.db, "t1", "s")
	bindToken(t, env.db, "t2", "s")
	bindToken(t, env.db, "t3", "other")

	snapshot := func() []string {
		rows, err := env.db.Query(`SELECT token_id || '|' || slug || '|' || created_at FROM token_workspace_bindings ORDER BY token_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	before := snapshot()
	if len(before) != 3 {
		t.Fatalf("rows before = %d; want 3", len(before))
	}

	if rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/s/archive", "", userAuth("user-1")); rec.Code != http.StatusOK {
		t.Fatalf("archive: status %d: %s", rec.Code, rec.Body)
	}
	if after := snapshot(); !slices.Equal(after, before) {
		t.Errorf("bindings after archive = %v; want %v", after, before)
	}
	if rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/s", "", userAuth("user-1")); rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("delete: status %d: %s", rec.Code, rec.Body)
	}
	if after := snapshot(); !slices.Equal(after, before) {
		t.Errorf("bindings after delete = %v; want %v", after, before)
	}
}

// TS-33-14: A bound token on slug S gets exactly the response an unbound PAT
// with the same scopes gets. Requirement: 33-REQ-3.2
func TestBoundToken_OwnSlugEqualsUnbound_TS_33_14(t *testing.T) {
	h := newRealHub(t)
	h.seed(t, "s", "u1", "active")

	for _, scopes := range [][]string{{"workspaces:read", "patches:read"}, {"users:read"}} {
		bound, boundID := h.mintPAT(t, scopes...)
		bindToken(t, h.db.SqlDB, boundID, "s")
		unbound, _ := h.mintPAT(t, scopes...)
		for _, path := range []string{"/api/v1/workspaces/s", "/api/v1/workspaces/s/patches"} {
			b := h.do(http.MethodGet, path, bound, "")
			u := h.do(http.MethodGet, path, unbound, "")
			if b.Code != u.Code || b.Body.String() != u.Body.String() {
				t.Errorf("%v %s: bound (%d, %s); want the unbound PAT's (%d, %s)", scopes, path, b.Code, b.Body, u.Code, u.Body)
			}
			if len(scopes) == 1 && b.Code == http.StatusOK {
				t.Errorf("%v %s: status 200; want the handler's own refusal without the read scope", scopes, path)
			}
		}
	}
}

// TS-33-16: Routes and subgroups registered after the guard, including the
// runs subgroup, are guarded without further change. Requirement: 33-REQ-3.4
func TestBindingGuard_CoversLaterRoutes_TS_33_16(t *testing.T) {
	h := newRealHub(t)
	h.seed(t, "s", "u1", "active")
	h.seed(t, "x", "u1", "active")

	duck, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatalf("audit.OpenDB: %v", err)
	}
	t.Cleanup(func() { duck.Close() })
	if err := audit.InitSchema(duck); err != nil {
		t.Fatalf("audit.InitSchema: %v", err)
	}
	store := audit.NewStore(duck)
	audit.RegisterRoutes(h.server.APIGroup(), store, audit.NewEmitter(store), h.db.SqlDB)
	newRouteCalls := 0
	h.server.APIGroup().GET("/workspaces/:slug/new", func(c echo.Context) error {
		newRouteCalls++
		return c.NoContent(http.StatusNoContent)
	})

	token, id := h.mintPAT(t, "audit:write", "workspaces:read")
	bindToken(t, h.db.SqlDB, id, "s")
	body := `{"event_type":"session.start","payload":{}}`

	for _, r := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/workspaces/x/runs/20260901_120000_abcdef/events", body},
		{http.MethodGet, "/api/v1/workspaces/x/new", ""},
	} {
		rec := h.do(r.method, r.path, token, r.body)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"workspace not found"`) {
			t.Errorf("%s %s: (%d, %s); want 404 workspace not found", r.method, r.path, rec.Code, rec.Body)
		}
	}
	if newRouteCalls != 0 {
		t.Errorf("later route handler calls = %d; want 0", newRouteCalls)
	}

	if rec := h.do(http.MethodPost, "/api/v1/workspaces/s/runs/20260901_120000_abcdef/events", token, body); rec.Code != http.StatusCreated {
		t.Errorf("POST runs events on S: status %d; want the audit handler's 201: %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodGet, "/api/v1/workspaces/s/new", token, ""); rec.Code != http.StatusNoContent || newRouteCalls != 1 {
		t.Errorf("later route on S: status %d, calls %d; want the handler's 204", rec.Code, newRouteCalls)
	}
}

// boundAuthCtx returns an echo context with a recorder for calling
// lookupWorkspaceForAuth directly, with no guard in front.
func boundAuthCtx() (echo.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	return echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec), rec
}

// TS-33-18: lookupWorkspaceForAuth applies the binding rule and writes 404 for
// another owned workspace. Requirement: 33-REQ-3.6
//
// Like every denial of lookupWorkspaceForAuth, the refusal is a nil workspace
// with the response written; the returned error is the write's result, which
// callers ignore (they test the workspace for nil).
func TestLookupWorkspaceForAuth_Binding_TS_33_18(t *testing.T) {
	db := openTestDB(t)
	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"s", "x"} {
		if err := insertWorkspace(db, &Workspace{Slug: slug, GitURL: "https://github.com/org/r", OwnerID: "u1", Status: "active"}); err != nil {
			t.Fatal(err)
		}
	}
	bindToken(t, db, "tokB", "s")
	bound := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokB", Permissions: []string{"workspaces:read"}}

	c, rec := boundAuthCtx()
	ws, _ := lookupWorkspaceForAuth(c, db, "x", bound)
	if ws != nil || rec.Code != http.StatusNotFound {
		t.Fatalf("lookupWorkspaceForAuth(x) = %v, status %d; want nil and 404", ws, rec.Code)
	}
	if env := parseErrorEnvelope(t, rec); env.Error.Message != "workspace not found" {
		t.Errorf("message %q; want workspace not found", env.Error.Message)
	}

	c, _ = boundAuthCtx()
	ws, err := lookupWorkspaceForAuth(c, db, "s", bound)
	if ws == nil || ws.Slug != "s" || err != nil {
		t.Errorf("lookupWorkspaceForAuth(s) = (%v, %v); want workspace s", ws, err)
	}
}

// TS-33-19: A binding lookup failure inside AuthorizeWorkspace and
// lookupWorkspaceForAuth is a 500. Requirements: 33-REQ-3.7
func TestBindingLookupFailure_Is500_TS_33_19(t *testing.T) {
	db := openTestDB(t)
	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := insertWorkspace(db, &Workspace{Slug: "s", GitURL: "https://github.com/org/r", OwnerID: "u1", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	pat := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokB", Permissions: []string{"workspaces:read"}}

	if a, code, msg := AuthorizeWorkspace(db, pat, "s"); a != nil || code != http.StatusInternalServerError || msg != "internal server error" {
		t.Errorf("AuthorizeWorkspace = (%v, %d, %q); want (nil, 500, internal server error)", a, code, msg)
	}
	c, rec := boundAuthCtx()
	if ws, _ := lookupWorkspaceForAuth(c, db, "s", pat); ws != nil || rec.Code != http.StatusInternalServerError {
		t.Errorf("lookupWorkspaceForAuth = %v, status %d; want nil and 500", ws, rec.Code)
	} else if env := parseErrorEnvelope(t, rec); env.Error.Message != "internal server error" {
		t.Errorf("message %q; want internal server error", env.Error.Message)
	}
}

// TS-33-27: MountWorkspaceHandlers records apikit's own routes and omits
// health routes. Requirement: 33-REQ-5.1
func TestMountWorkspaceHandlers_RecordsAPIKitRoutes_TS_33_27(t *testing.T) {
	server, database := openRealServer(t)
	existing := map[string]bool{}
	for _, r := range server.Echo().Routes() {
		existing[r.Method+" "+r.Path] = true
	}
	mountRealHub(t, server, database)

	recorded := map[string]bool{}
	for _, r := range recordedAPIKitRoutes {
		key := r.Method + " " + r.Path
		recorded[key] = true
		if existing[key] {
			t.Errorf("recorded %s, which existed before MountHandlers", key)
		}
		switch r.Path {
		case "/healthz", "/readyz", "/version":
			t.Errorf("recorded the health route %s", key)
		}
	}
	for _, want := range []string{"GET /api/v1/user", "POST /api/v1/user/tokens", "DELETE /api/v1/user/keys/:key_id", "GET /api/v1/orgs/:id"} {
		if !recorded[want] {
			t.Errorf("recorded apikit routes lack %s", want)
		}
	}
	for _, hub := range []string{"GET /api/v1/workspaces", "POST /api/v1/workspaces", "GET /api/v1/workspaces/:slug"} {
		if recorded[hub] {
			t.Errorf("recorded the hub route %s", hub)
		}
	}
}

// TS-33-28: A bound PAT is refused on apikit's routes before authentication
// with a message that does not name the workspace. Requirement: 33-REQ-5.2
func TestAPIKitRoutes_RefuseBoundPAT_TS_33_28(t *testing.T) {
	h := newRealHub(t)
	token, id := h.mintPAT(t, "users:read", "keys:manage", "orgs:read")
	bindToken(t, h.db.SqlDB, id, "secret-ws")

	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/user"},
		{http.MethodDelete, "/api/v1/user/keys/k1"},
		{http.MethodGet, "/api/v1/orgs/o1"},
	} {
		rec := h.do(r.method, r.path, token, "")
		raw := rec.Body.String()
		env := parseErrorEnvelope(t, rec)
		if rec.Code != http.StatusForbidden || env.Error.ErrorType != "token_bound" || env.Error.Message != "this token is bound to a workspace" {
			t.Errorf("%s %s: (%d, %s); want 403 token_bound: this token is bound to a workspace", r.method, r.path, rec.Code, raw)
		}
		if strings.Contains(raw, "secret-ws") {
			t.Errorf("%s %s: body %s names the workspace", r.method, r.path, raw)
		}
	}
}

// TS-33-29: A bound PAT holding tokens:manage cannot mint a new PAT.
// Requirement: 33-REQ-5.3
func TestAPIKitRoutes_BoundPATCannotMint_TS_33_29(t *testing.T) {
	h := newRealHub(t)
	token, id := h.mintPAT(t, "tokens:manage")
	bindToken(t, h.db.SqlDB, id, "s")

	countPATs := func() int {
		var n int
		if err := h.db.SqlDB.QueryRow(`SELECT COUNT(*) FROM pats`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := countPATs()
	rec := h.do(http.MethodPost, "/api/v1/user/tokens", token, `{"name":"escape","permissions":["tokens:manage"]}`)
	raw := rec.Body.String()
	if rec.Code != http.StatusForbidden || parseErrorEnvelope(t, rec).Error.ErrorType != "token_bound" {
		t.Errorf("POST /api/v1/user/tokens: (%d, %s); want 403 token_bound", rec.Code, raw)
	}
	if after := countPATs(); after != before {
		t.Errorf("pats rows %d after; want %d", after, before)
	}

	// apikit strips "Bearer " twice, so a doubled scheme authenticates the
	// same PAT; it must be refused just the same.
	rec = h.do(http.MethodPost, "/api/v1/user/tokens", "Bearer "+token, `{"name":"escape","permissions":["tokens:manage"]}`)
	raw = rec.Body.String()
	if rec.Code != http.StatusForbidden || parseErrorEnvelope(t, rec).Error.ErrorType != "token_bound" {
		t.Errorf("POST /api/v1/user/tokens with a doubled Bearer: (%d, %s); want 403 token_bound", rec.Code, raw)
	}
	if after := countPATs(); after != before {
		t.Errorf("pats rows %d after the doubled Bearer; want %d", after, before)
	}

	// The same PAT unbound would have minted one: the refusal is the binding's.
	unbound, _ := h.mintPAT(t, "tokens:manage")
	if rec := h.do(http.MethodPost, "/api/v1/user/tokens", unbound, `{"name":"ok","permissions":["tokens:manage"]}`); rec.Code != http.StatusCreated {
		t.Errorf("unbound tokens:manage PAT: status %d; want 201: %s", rec.Code, rec.Body)
	}
}

// TS-33-34: GET /workspaces returns at most S for a bound token.
// Requirement: 33-REQ-6.1
func TestListWorkspaces_BoundToken_TS_33_34(t *testing.T) {
	h := newRealHub(t)
	h.seed(t, "s", "u1", "active")
	h.seed(t, "x", "u1", "active")
	h.seed(t, "a", "u1", "archived")

	slugs := func(rec *httptest.ResponseRecorder) []string {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d; want 200: %s", rec.Code, rec.Body)
		}
		var list []workspaceJSON
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, w := range list {
			out = append(out, w.Slug)
		}
		return out
	}

	toS, idS := h.mintPAT(t, "workspaces:read")
	bindToken(t, h.db.SqlDB, idS, "s")
	toA, idA := h.mintPAT(t, "workspaces:read")
	bindToken(t, h.db.SqlDB, idA, "a")

	cases := []struct {
		token, query string
		want         []string
	}{
		{toS, "", []string{"s"}},
		{toS, "?include_archived=true", []string{"s"}},
		{toA, "", nil},
		{toA, "?include_archived=true", []string{"a"}},
	}
	for _, tc := range cases {
		if got := slugs(h.do(http.MethodGet, "/api/v1/workspaces"+tc.query, tc.token, "")); !slices.Equal(got, tc.want) {
			t.Errorf("GET /workspaces%s: %v; want %v", tc.query, got, tc.want)
		}
	}
	// The API key of the same user still lists all of its active workspaces.
	if got := slugs(h.do(http.MethodGet, "/api/v1/workspaces", h.apiKey, "")); len(got) != 2 {
		t.Errorf("API key: %v; want s and x", got)
	}

	// Without workspaces:read the bound token gets the usual scope refusal,
	// the same an unbound PAT with the same scopes gets.
	noScope, noScopeID := h.mintPAT(t, "users:read")
	bindToken(t, h.db.SqlDB, noScopeID, "s")
	unbound, _ := h.mintPAT(t, "users:read")
	b := h.do(http.MethodGet, "/api/v1/workspaces", noScope, "")
	u := h.do(http.MethodGet, "/api/v1/workspaces", unbound, "")
	if b.Code == http.StatusOK || b.Code != u.Code || b.Body.String() != u.Body.String() {
		t.Errorf("without workspaces:read: bound (%d, %s); want the unbound PAT's refusal (%d, %s)", b.Code, b.Body, u.Code, u.Body)
	}
}
