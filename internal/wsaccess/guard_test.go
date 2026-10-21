package wsaccess_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// testAuthMiddleware injects the apikit.AuthInfo carried as JSON in the
// X-Test-Auth header, standing in for apikit's auth middleware.
func testAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if raw := c.Request().Header.Get("X-Test-Auth"); raw != "" {
				var info apikit.AuthInfo
				if err := json.Unmarshal([]byte(raw), &info); err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Test-Auth header")
				}
				apikit.SetAuthInfo(c, &info)
			}
			return next(c)
		}
	}
}

// newGuardedAPI returns an Echo instance whose /api/v1 group runs the test
// auth middleware and then the binding guard, before any route exists.
func newGuardedAPI(db *sql.DB) (*echo.Echo, *echo.Group) {
	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	api.Use(wsaccess.BindingGuard(db))
	return e, api
}

// serve sends one request with the given credential and Authorization header.
func serve(e *echo.Echo, method, path string, auth *apikit.AuthInfo, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if auth != nil {
		raw, _ := json.Marshal(auth)
		req.Header.Set("X-Test-Auth", string(raw))
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

type errorBody struct {
	Error struct {
		Code      int    `json:"code"`
		Message   string `json:"message"`
		ErrorType string `json:"error_type"`
	} `json:"error"`
}

func parseError(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var b errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("response is not an error envelope: %v: %s", err, rec.Body)
	}
	return b
}

// probe is a handler that records whether it ran and the AuthInfo it saw.
type probe struct {
	calls int
	auth  *apikit.AuthInfo
}

func (p *probe) handle(c echo.Context) error {
	p.calls++
	p.auth = apikit.GetAuthInfo(c)
	return c.String(http.StatusOK, "probe")
}

// boundPAT is a PAT AuthInfo with a token id, as apikit's middleware sets it.
func boundPAT(tokenID string, scopes ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", Role: "user", TokenID: tokenID, Permissions: scopes}
}

// patHeader is the Bearer header of a PAT with the given id, in the prefix
// apikit was built with.
func patHeader(scheme, tokenID string) string {
	return scheme + " " + apikit.TokenPrefix + "_pat_" + tokenID + "_secret"
}

// TS-33-9: The guard derives a missing token id from the Bearer header and
// writes the enriched AuthInfo back. Requirement: 33-REQ-2.2
func TestBindingGuard_DerivesTokenID_TS_33_9(t *testing.T) {
	db := openBindingDB(t)
	bind(t, db, "abc123", "S")
	e, api := newGuardedAPI(db)
	p := &probe{}
	api.GET("/workspaces/:slug", p.handle)

	orig := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", Role: "user", Permissions: []string{"workspaces:read"}}
	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		p.calls, p.auth = 0, nil
		rec := serve(e, http.MethodGet, "/api/v1/workspaces/S", orig, patHeader(scheme, "abc123"))
		if rec.Code != http.StatusOK || p.calls != 1 {
			t.Fatalf("%s: status %d, probe calls %d; want 200 and 1: %s", scheme, rec.Code, p.calls, rec.Body)
		}
		if p.auth.TokenID != "abc123" {
			t.Errorf("%s: TokenID in the handler = %q; want abc123", scheme, p.auth.TokenID)
		}
		if p.auth.CredentialType != orig.CredentialType || p.auth.UserID != orig.UserID || p.auth.Role != orig.Role ||
			p.auth.KeyID != orig.KeyID || !slices.Equal(p.auth.Permissions, orig.Permissions) {
			t.Errorf("%s: AuthInfo in the handler = %+v; want %+v with only TokenID set", scheme, p.auth, orig)
		}
	}

	p.calls = 0
	rec := serve(e, http.MethodGet, "/api/v1/workspaces/X", orig, patHeader("Bearer", "abc123"))
	if rec.Code != http.StatusNotFound || p.calls != 0 {
		t.Errorf("slug X: status %d, probe calls %d; want 404 before the handler, so the parsed id found the binding", rec.Code, p.calls)
	}
}

// TS-33-10: A PAT with no derivable token id is treated as unbound without a
// binding query. Requirement: 33-REQ-2.3
func TestBindingGuard_NoTokenIDIsUnbound_TS_33_10(t *testing.T) {
	db := openBindingDB(t)
	db.Close() // any query would now fail and produce a 500
	e, api := newGuardedAPI(db)
	p := &probe{}
	api.GET("/widgets", p.handle)

	pat := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1"}
	prefix := apikit.TokenPrefix
	headers := []string{
		"",
		"Basic xxx",
		"Bearer " + prefix + "_pat_",
		"Bearer " + prefix + "_pat__secret",
		"Bearer " + prefix + "_apikey_xyz",
		"Bearer " + prefix + "_pat_noseparator",
		"Bearer",
	}
	for _, h := range headers {
		p.calls = 0
		rec := serve(e, http.MethodGet, "/api/v1/widgets", pat, h)
		if rec.Code != http.StatusOK || p.calls != 1 {
			t.Errorf("Authorization %q: status %d, probe calls %d; want the probe's 200 without a query: %s", h, rec.Code, p.calls, rec.Body)
		}
		if p.auth != nil && p.auth.TokenID != "" {
			t.Errorf("Authorization %q: TokenID = %q; want empty", h, p.auth.TokenID)
		}
	}
}

// TS-33-12: A binding lookup database error makes API routes answer 500 and
// the handler does not run. Requirement: 33-REQ-2.5
func TestBindingGuard_LookupErrorFailsClosed_TS_33_12(t *testing.T) {
	db := openBindingDB(t)
	if _, err := db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := wsaccess.LookupBinding(db, "t"); err == nil {
		t.Fatal("LookupBinding on a dropped table = nil error; want an error")
	}

	e, api := newGuardedAPI(db)
	p := &probe{}
	api.GET("/widgets", p.handle)
	api.GET("/workspaces/:slug", p.handle)
	for _, path := range []string{"/api/v1/widgets", "/api/v1/workspaces/S"} {
		rec := serve(e, http.MethodGet, path, boundPAT("t", "workspaces:read"), "")
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status %d; want 500: %s", path, rec.Code, rec.Body)
			continue
		}
		if msg := parseError(t, rec).Error.Message; msg != "internal server error" {
			t.Errorf("%s: message %q; want internal server error", path, msg)
		}
	}
	if p.calls != 0 {
		t.Errorf("probe calls = %d; want 0, the token must not be treated as unbound", p.calls)
	}
}

// TS-33-13: A bound token on a workspace-scoped route with another slug gets
// 404 before the handler runs. Requirement: 33-REQ-3.1
func TestBindingGuard_OtherSlugIs404_TS_33_13(t *testing.T) {
	db := openBindingDB(t)
	bind(t, db, "tokB", "S")
	e, api := newGuardedAPI(db)

	calls := 0
	scoped := func(c echo.Context) error {
		calls++
		return apikit.WriteAPIError(c, http.StatusForbidden, "insufficient scope")
	}
	api.GET("/workspaces/:slug", scoped)
	api.Group("/workspaces/:slug/runs/:run_id").GET("/events", scoped)

	for _, path := range []string{"/api/v1/workspaces/X", "/api/v1/workspaces/X/runs/r1/events"} {
		rec := serve(e, http.MethodGet, path, boundPAT("tokB"), "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d; want 404: %s", path, rec.Code, rec.Body)
			continue
		}
		if msg := parseError(t, rec).Error.Message; msg != "workspace not found" {
			t.Errorf("%s: message %q; want workspace not found", path, msg)
		}
	}
	if calls != 0 {
		t.Errorf("handler calls = %d; want 0, no scope check may run", calls)
	}
}

// TS-33-15 (property): For any slug other than S, any workspace-scoped route
// and any scope set, a bound token gets 404 workspace not found with the body
// a non-owner receives, and the handler is never entered.
// Requirement: 33-REQ-3.3
func TestBindingGuard_PropertyOtherSlugs_TS_33_15(t *testing.T) {
	db := openBindingDB(t)
	for _, ws := range []struct{ slug, owner, status string }{
		{"s", "u1", "active"}, {"x", "u1", "active"}, {"a", "u1", "archived"}, {"o", "u2", "active"},
	} {
		if _, err := db.Exec(`INSERT INTO workspaces (slug, owner_id, status) VALUES (?, ?, ?)`, ws.slug, ws.owner, ws.status); err != nil {
			t.Fatal(err)
		}
	}
	bind(t, db, "tokB", "s")
	e, api := newGuardedAPI(db)

	// A handler that answers as hub's workspace handlers do: the shared
	// ownership rule first, then a scope check, then success.
	calls := 0
	handler := func(c echo.Context) error {
		calls++
		auth := apikit.GetAuthInfo(c)
		if _, code, msg := wsaccess.AuthorizeWorkspace(db, auth, c.Param("slug")); code != 0 {
			return apikit.WriteAPIError(c, code, msg)
		}
		if !slices.Contains(auth.Permissions, "workspaces:read") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "insufficient scope")
		}
		return c.String(http.StatusOK, "ok")
	}
	type route struct{ method, template, path string }
	routes := []route{
		{http.MethodGet, "/workspaces/:slug", "/workspaces/%s"},
		{http.MethodPatch, "/workspaces/:slug", "/workspaces/%s"},
		{http.MethodDelete, "/workspaces/:slug/patches/:id", "/workspaces/%s/patches/p1"},
		{http.MethodPost, "/workspaces/:slug/sync", "/workspaces/%s/sync"},
		{http.MethodGet, "/workspaces/:slug/cost", "/workspaces/%s/cost"},
	}
	for _, r := range routes {
		api.Add(r.method, r.template, handler)
	}
	api.Group("/workspaces/:slug/runs/:run_id").POST("/events", handler)
	routes = append(routes, route{http.MethodPost, "", "/workspaces/%s/runs/r1/events"})

	allScopes := []string{"workspaces:read", "workspaces:write", "audit:write"}
	checked := 0
	for _, slug := range []string{"x", "a", "o", "ghost"} {
		for _, r := range routes {
			for mask := 0; mask < 1<<len(allScopes); mask++ {
				var scopes []string
				for i, s := range allScopes {
					if mask&(1<<i) != 0 {
						scopes = append(scopes, s)
					}
				}
				path := "/api/v1" + fmt.Sprintf(r.path, slug)
				nonOwner := serve(e, r.method, path, &apikit.AuthInfo{CredentialType: "pat", UserID: "u3", Permissions: scopes}, "")

				calls = 0
				rec := serve(e, r.method, path, boundPAT("tokB", scopes...), "")
				if rec.Code != http.StatusNotFound || calls != 0 {
					t.Errorf("%s %s %v: status %d, handler calls %d; want 404 before the handler", r.method, path, scopes, rec.Code, calls)
					continue
				}
				if parseError(t, rec).Error.Message != "workspace not found" || rec.Body.String() != nonOwner.Body.String() {
					t.Errorf("%s %s %v: body %s; want the non-owner's %s", r.method, path, scopes, rec.Body, nonOwner.Body)
				}
				checked++
			}
		}
	}
	if checked != 4*len(routes)*8 {
		t.Errorf("checked %d cases; want %d", checked, 4*len(routes)*8)
	}
}

// TS-33-20: A bound token on a non-workspace, non-allowlisted route gets 403
// token_bound naming S. Requirement: 33-REQ-4.1
func TestBindingGuard_DefaultDeny_TS_33_20(t *testing.T) {
	wsaccess.ResetAllowlistForTest()
	db := openBindingDB(t)
	bind(t, db, "tokB", "S")
	e, api := newGuardedAPI(db)
	p := &probe{}
	api.GET("/widgets", p.handle)

	rec := serve(e, http.MethodGet, "/api/v1/widgets", boundPAT("tokB", "workspaces:read", "secrets:read", "tokens:manage"), "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d; want 403: %s", rec.Code, rec.Body)
	}
	b := parseError(t, rec)
	if b.Error.ErrorType != "token_bound" || wsaccess.ErrorTypeTokenBound != "token_bound" {
		t.Errorf("error_type %q; want token_bound", b.Error.ErrorType)
	}
	if b.Error.Message != "this token is bound to workspace S" {
		t.Errorf("message %q; want %q", b.Error.Message, "this token is bound to workspace S")
	}
	if p.calls != 0 {
		t.Errorf("probe calls = %d; want 0", p.calls)
	}
}

// TS-33-22: A bound token on an allowlisted route reaches the handler.
// Requirement: 33-REQ-4.3
func TestBindingGuard_AllowlistedRoute_TS_33_22(t *testing.T) {
	wsaccess.ResetAllowlistForTest()
	t.Cleanup(wsaccess.ResetAllowlistForTest)
	db := openBindingDB(t)
	bind(t, db, "tokB", "S")
	e, api := newGuardedAPI(db)
	p := &probe{}
	wsaccess.AllowBoundTokens(api.GET("/listing", p.handle))

	if !wsaccess.IsAllowed(http.MethodGet, "/api/v1/listing") {
		t.Error("IsAllowed(GET /api/v1/listing) = false after AllowBoundTokens")
	}
	if wsaccess.IsAllowed(http.MethodPost, "/api/v1/listing") {
		t.Error("IsAllowed(POST /api/v1/listing) = true; only the marked method is allowed")
	}
	rec := serve(e, http.MethodGet, "/api/v1/listing", boundPAT("tokB"), "")
	if rec.Code != http.StatusOK || rec.Body.String() != "probe" || p.calls != 1 {
		t.Errorf("status %d, body %q, probe calls %d; want the handler's 200", rec.Code, rec.Body, p.calls)
	}
}

// TS-33-24: A bound token requesting an unmatched path under the mount point
// is refused. Requirement: 33-REQ-4.5
func TestBindingGuard_UnmatchedPath_TS_33_24(t *testing.T) {
	db := openBindingDB(t)
	bind(t, db, "tokB", "S")
	e, api := newGuardedAPI(db)
	api.GET("/widgets", (&probe{}).handle)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := serve(e, method, "/api/v1/no/such/path", boundPAT("tokB"), "")
		if rec.Code != http.StatusForbidden || parseError(t, rec).Error.ErrorType != "token_bound" {
			t.Errorf("%s /api/v1/no/such/path: status %d, body %s; want 403 token_bound", method, rec.Code, rec.Body)
		}
	}
	// An unbound credential still gets the router's usual 404.
	if rec := serve(e, http.MethodGet, "/api/v1/no/such/path", boundPAT("tokU"), ""); rec.Code != http.StatusNotFound {
		t.Errorf("unbound: status %d; want 404", rec.Code)
	}
}

// TS-33-25 (property): For any route registered after the guard that is
// neither workspace-scoped nor allowlisted, a bound token is refused with 403
// token_bound and the handler is never called. Requirement: 33-REQ-4.6
func TestBindingGuard_PropertyLaterRoutes_TS_33_25(t *testing.T) {
	wsaccess.ResetAllowlistForTest()
	db := openBindingDB(t)
	bind(t, db, "tokB", "S")
	e, api := newGuardedAPI(db)

	calls := 0
	h := func(c echo.Context) error { calls++; return c.NoContent(http.StatusNoContent) }
	type route struct{ method, path string }
	var routes []route
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	templates := map[string]string{
		"/things":                  "/things",
		"/things/:id":              "/things/7",
		"/orgs/:slug/vars":         "/orgs/o1/vars",
		"/workspaces":              "/workspaces",
		"/workspaces/x/:slug":      "/workspaces/x/S",
		"/users/:slug/workspaces":  "/users/S/workspaces",
		"/workspacesx/:slug":       "/workspacesx/S",
		"/sessions/:id/workspaces": "/sessions/1/workspaces",
	}
	for tmpl, path := range templates {
		for _, m := range methods {
			api.Add(m, tmpl, h)
			routes = append(routes, route{m, path})
		}
	}
	later := api.Group("/later")
	for _, m := range methods {
		later.Add(m, "/:slug", h)
		routes = append(routes, route{m, "/later/S"})
	}

	for _, r := range routes {
		calls = 0
		rec := serve(e, r.method, "/api/v1"+r.path, boundPAT("tokB", "workspaces:read", "workspaces:write"), "")
		if rec.Code != http.StatusForbidden || parseError(t, rec).Error.ErrorType != "token_bound" || calls != 0 {
			t.Errorf("%s %s: status %d, handler calls %d, body %s; want 403 token_bound before the handler", r.method, r.path, rec.Code, calls, rec.Body)
		}
	}
}

// TS-33-26: Requests without credentials or with api_key, admin_token or
// unbound PAT pass the guard identically, without a binding query for
// non-PATs. Requirement: 33-REQ-4.7
func TestBindingGuard_UnboundPassThrough_TS_33_26(t *testing.T) {
	wsaccess.ResetAllowlistForTest()
	handler := func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		return c.JSON(http.StatusOK, map[string]any{"type": auth.CredentialType, "user": auth.UserID, "token": auth.TokenID})
	}
	plain := echo.New()
	plain.HTTPErrorHandler = apikit.HTTPErrorHandler
	plainAPI := plain.Group("/api/v1")
	plainAPI.Use(testAuthMiddleware())
	plainAPI.GET("/widgets", handler)
	plainAPI.GET("/workspaces/:slug", handler)

	closed := openBindingDB(t)
	closed.Close()
	live := openBindingDB(t)
	bind(t, live, "tokB", "S")

	cases := []struct {
		name string
		db   *sql.DB
		auth *apikit.AuthInfo
	}{
		{"no credentials", closed, nil},
		{"api_key", closed, &apikit.AuthInfo{CredentialType: "api_key", UserID: "u1", KeyID: "k1"}},
		{"admin_token", closed, &apikit.AuthInfo{CredentialType: "admin_token", Role: "admin"}},
		{"unbound pat", live, &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokU"}},
	}
	for _, tc := range cases {
		guarded, api := newGuardedAPI(tc.db)
		api.GET("/widgets", handler)
		api.GET("/workspaces/:slug", handler)
		for _, path := range []string{"/api/v1/widgets", "/api/v1/workspaces/X", "/api/v1/no/such"} {
			got := serve(guarded, http.MethodGet, path, tc.auth, "")
			want := serve(plain, http.MethodGet, path, tc.auth, "")
			if got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Errorf("%s %s: guarded (%d, %s); want unguarded (%d, %s)", tc.name, path, got.Code, got.Body, want.Code, want.Body)
			}
		}
	}
}
