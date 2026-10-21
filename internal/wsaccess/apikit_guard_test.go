package wsaccess_test

import (
	"database/sql"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// apikitStandIn builds an Echo instance with a route standing in for one of
// apikit's (GET /api/v1/user) and a hub route (GET /api/v1/workspaces). When
// guarded, the apikit route guard is installed on the root instance with only
// the stand-in route recorded. The returned probe sees what reaches apikit.
func apikitStandIn(db *sql.DB, guarded bool) (*echo.Echo, *probe) {
	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	p := &probe{}
	user := e.GET("/api/v1/user", p.handle)
	e.GET("/api/v1/workspaces", p.handle)
	if guarded {
		e.Use(wsaccess.APIKitRouteGuard(db, []*echo.Route{user}))
	}
	return e, p
}

// TS-33-30: Requests on apikit routes with no PAT id or an unbound id pass
// through unchanged. Requirement: 33-REQ-5.4
func TestAPIKitRouteGuard_PassThrough_TS_33_30(t *testing.T) {
	closed := openBindingDB(t)
	closed.Close()
	live := openBindingDB(t)
	bind(t, live, "tokB", "S")
	prefix := apikit.TokenPrefix

	cases := []struct {
		name, header string
		db           *sql.DB
	}{
		{"no Authorization", "", closed},
		{"Bearer API key", "Bearer " + prefix + "_key1_secret", closed},
		{"Bearer admin token", "Bearer " + prefix + "_admin_00ff", closed},
		{"unbound PAT", "Bearer " + prefix + "_pat_tokU_secret", live},
	}
	for _, tc := range cases {
		guarded, gp := apikitStandIn(tc.db, true)
		plain, _ := apikitStandIn(tc.db, false)
		got := serve(guarded, http.MethodGet, "/api/v1/user", nil, tc.header)
		want := serve(plain, http.MethodGet, "/api/v1/user", nil, tc.header)
		if got.Code != want.Code || got.Body.String() != want.Body.String() || gp.calls != 1 {
			t.Errorf("%s: guarded (%d, %s, %d calls); want unguarded (%d, %s) and one call", tc.name, got.Code, got.Body, gp.calls, want.Code, want.Body)
		}
	}
}

// TS-33-31: The apikit route guard never sets AuthInfo, grants access or
// verifies a secret. Requirement: 33-REQ-5.5
func TestAPIKitRouteGuard_DenyOnly_TS_33_31(t *testing.T) {
	db := openBindingDB(t)
	bind(t, db, "tokB", "S")
	e, p := apikitStandIn(db, true)

	right := serve(e, http.MethodGet, "/api/v1/user", nil, "Bearer "+apikit.TokenPrefix+"_pat_tokB_therealsecret")
	wrong := serve(e, http.MethodGet, "/api/v1/user", nil, "Bearer "+apikit.TokenPrefix+"_pat_tokB_wrong")
	if right.Code != http.StatusForbidden || wrong.Code != http.StatusForbidden || right.Body.String() != wrong.Body.String() {
		t.Errorf("bound id: (%d, %s) and (%d, %s); want the same 403 whatever the secret", right.Code, right.Body, wrong.Code, wrong.Body)
	}
	if p.calls != 0 {
		t.Errorf("probe calls = %d; want 0 for a bound id", p.calls)
	}

	rec := serve(e, http.MethodGet, "/api/v1/user", nil, "Bearer "+apikit.TokenPrefix+"_pat_tokU_wrong")
	if rec.Code != http.StatusOK || p.calls != 1 {
		t.Fatalf("unbound id: status %d, probe calls %d; want it passed on", rec.Code, p.calls)
	}
	if p.auth != nil {
		t.Errorf("AuthInfo after the guard = %+v; want nil, the guard never sets it", p.auth)
	}

	// The guard's source imports no hashing or token-verification package.
	f, err := parser.ParseFile(token.NewFileSet(), "apikit_guard.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing apikit_guard.go: %v", err)
	}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if strings.HasPrefix(path, "crypto") || strings.Contains(path, "x/crypto") || strings.Contains(path, "hash") {
			t.Errorf("apikit_guard.go imports %q; the guard must never hash or verify a secret", path)
		}
	}
}

// TS-33-32: Requests that match no recorded apikit route cause no binding
// query at the root guard. Requirement: 33-REQ-5.6
func TestAPIKitRouteGuard_UnrecordedRoutesNoQuery_TS_33_32(t *testing.T) {
	db := openBindingDB(t)
	bind(t, db, "tokB", "S")
	db.Close()
	e, _ := apikitStandIn(db, true)

	header := "Bearer " + apikit.TokenPrefix + "_pat_tokB_secret"
	if rec := serve(e, http.MethodGet, "/api/v1/workspaces", nil, header); rec.Code != http.StatusOK {
		t.Errorf("hub route: status %d; want the handler's 200 without a query: %s", rec.Code, rec.Body)
	}
	if rec := serve(e, http.MethodGet, "/api/v1/no/such/path", nil, header); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path: status %d; want the router's 404 without a query: %s", rec.Code, rec.Body)
	}
	// Another method on a recorded path is not the recorded route either.
	if rec := serve(e, http.MethodPost, "/api/v1/user", nil, header); rec.Code == http.StatusInternalServerError {
		t.Errorf("POST /api/v1/user: status 500; want no query for an unrecorded method")
	}
}

// TS-33-33: A database error in the apikit route guard fails closed with 500.
// Requirement: 33-REQ-5.7
func TestAPIKitRouteGuard_LookupErrorFailsClosed_TS_33_33(t *testing.T) {
	db := openBindingDB(t)
	if _, err := db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	e, p := apikitStandIn(db, true)

	rec := serve(e, http.MethodGet, "/api/v1/user", nil, "Bearer "+apikit.TokenPrefix+"_pat_tokB_secret")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d; want 500: %s", rec.Code, rec.Body)
	}
	if msg := parseError(t, rec).Error.Message; msg != "internal server error" {
		t.Errorf("message %q; want internal server error", msg)
	}
	if p.calls != 0 {
		t.Errorf("probe calls = %d; want 0, the request must not reach apikit", p.calls)
	}
}

// The refusal of TS-33-31 carries token_bound and does not name the
// workspace (33-REQ-5.2).
func TestAPIKitRouteGuard_RefusalBody_TS_33_31(t *testing.T) {
	db := openBindingDB(t)
	bind(t, db, "tokB", "secret-ws")
	e, _ := apikitStandIn(db, true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/user", nil)
	req.Header.Set("Authorization", "bearer "+apikit.TokenPrefix+"_pat_tokB_x")
	e.ServeHTTP(rec, req)
	b := parseError(t, rec)
	if rec.Code != http.StatusForbidden || b.Error.ErrorType != "token_bound" || b.Error.Message != "this token is bound to a workspace" {
		t.Errorf("(%d, %s); want 403 token_bound: this token is bound to a workspace", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "secret-ws") {
		t.Errorf("body %s names the workspace", rec.Body)
	}
}

// TS-33-31 / 33-REQ-5.2: apikit strips "Bearer " from the header and then
// once more from the token, so "Bearer Bearer <PAT>" authenticates; the guard
// must see the PAT id through the second scheme as well.
func TestAPIKitRouteGuard_DoubledScheme_TS_33_31(t *testing.T) {
	db := openBindingDB(t)
	bind(t, db, "tokB", "S")
	e, p := apikitStandIn(db, true)
	pat := apikit.TokenPrefix + "_pat_tokB_secret"

	for _, header := range []string{"Bearer Bearer " + pat, "Bearer bearer " + pat, "Bearer  " + pat, "bearer Bearer " + pat} {
		rec := serve(e, http.MethodGet, "/api/v1/user", nil, header)
		if rec.Code != http.StatusForbidden || parseError(t, rec).Error.ErrorType != "token_bound" {
			t.Errorf("Authorization %q: (%d, %s); want 403 token_bound", header, rec.Code, rec.Body)
		}
	}
	if p.calls != 0 {
		t.Errorf("probe calls = %d; want 0", p.calls)
	}
}
