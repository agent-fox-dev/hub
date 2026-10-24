package wsaccess

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// Credentials that are not PATs, requests without one and unbound PATs pass
// into the handler, the first two without a query; a PAT without a token id
// gets it from its Bearer header, written back to the AuthInfo, and a PAT
// without a derivable id is unbound.
func TestBindingGuard_PassesUnboundCredentials(t *testing.T) {
	db := newDB(t)
	testutil.BindToken(t, db, "bound", "w")
	e, api := guardedAPI(db)
	p := &probe{}
	api.GET("/me", p.handle)
	for _, c := range []struct {
		name          string
		auth          *apikit.AuthInfo
		authorization string
		wantTokenID   string
	}{
		{"no credential", nil, "", ""},
		{"api key", testutil.User("u"), patHeader("bound"), ""},
		{"admin", testutil.Admin(), "", ""},
		{"unbound PAT", testutil.BoundPAT("u", "free"), patHeader("free"), "free"},
		{"PAT id from the header", testutil.PAT("u"), patHeader("free"), "free"},
		{"PAT id from a doubled scheme", testutil.PAT("u"), "bearer Bearer " + apikit.TokenPrefix + "_pat_free_s", "free"},
		{"PAT without an id", testutil.PAT("u"), "Bearer something-else", ""},
	} {
		p.calls = 0
		rec := serve(e, http.MethodGet, "/api/v1/me", c.auth, c.authorization)
		if rec.Code != http.StatusOK || p.calls != 1 {
			t.Errorf("%s: %d %s, handler calls %d; want it to pass", c.name, rec.Code, rec.Body, p.calls)
		}
		if c.auth != nil && p.auth.TokenID != c.wantTokenID {
			t.Errorf("%s: handler saw token id %q, want %q", c.name, p.auth.TokenID, c.wantTokenID)
		}
	}
	testutil.Exec(t, db, `DROP TABLE token_workspace_bindings`)
	if rec := serve(e, http.MethodGet, "/api/v1/me", testutil.User("u"), ""); rec.Code != http.StatusOK {
		t.Errorf("api key without the binding table: %d; want no query", rec.Code)
	}
	wantRefusal(t, serve(e, http.MethodGet, "/api/v1/me", testutil.BoundPAT("u", "bound"), ""), http.StatusInternalServerError, "internal server error", "")
}

// A bound token reaches its own workspace's routes and the routes marked with
// AllowBoundTokens; another workspace is 404 before the handler, and any
// other route, matched or not, is 403 token_bound. Each refusal is reported
// once with the route.
func TestBindingGuard_ConfinesBoundToken(t *testing.T) {
	resetAllowlist(t)
	db := newDB(t)
	testutil.BindToken(t, db, "tok", "mine")
	e, api := guardedAPI(db)
	p := &probe{}
	api.GET("/workspaces/:slug/patches", p.handle)
	api.GET("/user/tokens", p.handle)
	api.POST("/workspaces", p.handle)
	allowed := api.GET("/user/me", p.handle)
	api.GET("/later/workspaces/:slug", p.handle) // registered after the guard, still covered
	AllowBoundTokens(allowed)
	if !IsAllowed(http.MethodGet, "/api/v1/user/me") || IsAllowed(http.MethodPost, "/api/v1/user/me") {
		t.Error("IsAllowed does not reflect AllowBoundTokens")
	}
	refusals := captureRefusals(t)
	bound := testutil.BoundPAT("u", "tok", "workspaces:read")
	for _, path := range []string{"/api/v1/workspaces/mine/patches", "/api/v1/user/me", "/api/v1/later/workspaces/mine"} {
		if rec := serve(e, http.MethodGet, path, bound, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d %s; want the handler", path, rec.Code, rec.Body)
		}
	}
	p.calls = 0
	cases := []struct {
		method, path, route string
		status              int
		message, kind       string
	}{
		{http.MethodGet, "/api/v1/workspaces/other/patches", "/api/v1/workspaces/:slug/patches", http.StatusNotFound, "workspace not found", ""},
		{http.MethodGet, "/api/v1/later/workspaces/other", "/api/v1/later/workspaces/:slug", http.StatusNotFound, "workspace not found", ""},
		{http.MethodGet, "/api/v1/user/tokens", "/api/v1/user/tokens", http.StatusForbidden, "this token is bound to workspace mine", ErrorTypeTokenBound},
		{http.MethodPost, "/api/v1/workspaces", "/api/v1/workspaces", http.StatusForbidden, "this token is bound to workspace mine", ErrorTypeTokenBound},
		{http.MethodGet, "/api/v1/nowhere", "/api/v1/*", http.StatusForbidden, "this token is bound to workspace mine", ErrorTypeTokenBound},
	}
	for i, c := range cases {
		wantRefusal(t, serve(e, c.method, c.path, bound, ""), c.status, c.message, c.kind)
		want := BoundRefusal{TokenID: "tok", UserID: "u", BoundSlug: "mine", Method: c.method, Route: c.route, Status: c.status, Surface: SurfaceAPI}
		if c.status == http.StatusNotFound {
			want.RequestedSlug = "other"
		}
		if len(*refusals) != i+1 || (*refusals)[i] != want {
			t.Errorf("%s %s: refusals %+v, want %+v as refusal %d", c.method, c.path, *refusals, want, i+1)
		}
	}
	if p.calls != 0 {
		t.Errorf("the handler ran %d times behind a refusal", p.calls)
	}
}

// The route guard refuses a request on one of apikit's routes whose Bearer
// header names a bound PAT, with 403 token_bound that names no workspace and
// a report without a user; every other request, and every other route, goes
// on without a query. A failed lookup is 500.
func TestAPIKitRouteGuard_RefusesBoundTokensOnRecordedRoutes(t *testing.T) {
	db := newDB(t)
	testutil.BindToken(t, db, "tok", "w")
	refusals := captureRefusals(t)
	e := echo.New()
	p := &probe{}
	recorded := e.GET("/api/v1/user/tokens", p.handle)
	e.GET("/api/v1/health", p.handle)
	e.Use(APIKitRouteGuard(db, []*echo.Route{recorded}))
	for _, c := range []struct {
		name, path, authorization string
	}{
		{"unbound PAT", "/api/v1/user/tokens", patHeader("free")},
		{"no PAT id", "/api/v1/user/tokens", "Bearer " + apikit.TokenPrefix + "_abc"},
		{"no header", "/api/v1/user/tokens", ""},
		{"other route", "/api/v1/health", patHeader("tok")},
	} {
		if rec := serve(e, http.MethodGet, c.path, nil, c.authorization); rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s; want the handler", c.name, rec.Code, rec.Body)
		}
	}
	wantRefusal(t, serve(e, http.MethodGet, "/api/v1/user/tokens", nil, patHeader("tok")), http.StatusForbidden, "this token is bound to a workspace", ErrorTypeTokenBound)
	want := BoundRefusal{TokenID: "tok", BoundSlug: "w", Method: http.MethodGet, Route: "/api/v1/user/tokens", Status: http.StatusForbidden, Surface: SurfaceAPI}
	if len(*refusals) != 1 || (*refusals)[0] != want {
		t.Errorf("refusals %+v, want %+v", *refusals, want)
	}
	testutil.Exec(t, db, `DROP TABLE token_workspace_bindings`)
	wantRefusal(t, serve(e, http.MethodGet, "/api/v1/user/tokens", nil, patHeader("tok")), http.StatusInternalServerError, "internal server error", "")
	if rec := serve(e, http.MethodGet, "/api/v1/health", nil, patHeader("tok")); rec.Code != http.StatusOK {
		t.Errorf("other route without the table: %d; want no query", rec.Code)
	}
}

// patTokenID reads the id of a PAT out of every Bearer header apikit accepts
// and nothing out of any other header.
func TestPatTokenID(t *testing.T) {
	pat := apikit.TokenPrefix + "_pat_abc_secret"
	for header, want := range map[string]string{
		"Bearer " + pat:        "abc",
		"bearer   " + pat:      "abc",
		"Bearer Bearer " + pat: "abc",
		"BEARER bearer " + pat: "abc",
		pat:                    "",
		"Basic " + pat:         "",
		"Bearer " + apikit.TokenPrefix + "_abc_secret":  "",
		"Bearer " + apikit.TokenPrefix + "_pat__secret": "",
		"Bearer " + apikit.TokenPrefix + "_pat_abc":     "",
		"Bearer xx_pat_abc_secret":                      "",
		"":                                              "",
	} {
		if got := patTokenID(header); got != want {
			t.Errorf("patTokenID(%q) = %q, want %q", header, got, want)
		}
	}
	if !isWorkspaceScoped("/api/v1/workspaces/:slug/patches") || isWorkspaceScoped("/api/v1/workspaces") || isWorkspaceScoped("/api/v1/workspaces/:id") {
		t.Error("isWorkspaceScoped misreads a route template")
	}
}
