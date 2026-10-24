package boundtoken

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// stubAPIKit stands in for apikit's server.Echo(): it records every request
// and answers with status and body, or with reply when set.
type stubAPIKit struct {
	mu     sync.Mutex
	calls  int
	req    *http.Request
	body   []byte
	status int
	answer string
	reply  func(w http.ResponseWriter, r *http.Request, body []byte)
}

func newStub(status int, answer string) *stubAPIKit {
	return &stubAPIKit{status: status, answer: answer}
}

func (s *stubAPIKit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls++
	s.req, s.body = r, body
	reply := s.reply
	s.mu.Unlock()
	if reply != nil {
		reply(w, r, body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(s.status)
	_, _ = io.WriteString(w, s.answer)
}

const mintPath = "/api/v1/workspaces/s/tokens"

// okAnswer is a well-formed apikit 201 for token id T1.
const okAnswer = `{"token_id":"T1","name":"svc","token":"ak_pat_T1_supersecret","permissions":["workspaces:read"],"expires_at":null,"created_at":"2026-10-21T10:00:00Z"}`

// TS-34-1: RegisterRoutes mounts POST and GET /workspaces/:slug/tokens on the
// API group; without credentials both answer 401 and apikit is not called.
// Requirement: 34-REQ-1.1
func TestRegisterRoutes_TS_34_1(t *testing.T) {
	stub := newStub(http.StatusCreated, okAnswer)
	env := newTestEnv(t, Config{APIKit: stub})
	seedWorkspace(t, env.db, "s", "u1", "ready")
	found := map[string]bool{}
	for _, r := range env.e.Routes() {
		if r.Path == "/api/v1/workspaces/:slug/tokens" {
			found[r.Method] = true
		}
	}
	if !found[http.MethodPost] || !found[http.MethodGet] {
		t.Errorf("routes %v; want POST and GET /api/v1/workspaces/:slug/tokens", found)
	}
	for _, m := range []string{http.MethodPost, http.MethodGet} {
		if rec := env.do(m, mintPath, nil, `{}`, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without credentials: %d; want 401", m, rec.Code)
		}
	}
	if stub.calls != 0 {
		t.Errorf("apikit calls = %d; want 0", stub.calls)
	}
}

// TS-34-2: Minting with no credential answers 401 without contacting apikit.
// Requirement: 34-REQ-1.2
func TestMint_NoCredential_TS_34_2(t *testing.T) {
	stub := newStub(http.StatusCreated, okAnswer)
	env := newTestEnv(t, Config{APIKit: stub})
	seedWorkspace(t, env.db, "s", "u1", "ready")
	rec := env.do(http.MethodPost, mintPath, nil, `{"name":"n"}`, nil)
	if rec.Code != http.StatusUnauthorized || parseError(t, rec).Error.Message != "authentication required" || stub.calls != 0 {
		t.Errorf("(%d, %s), apikit calls %d; want 401 authentication required and no call", rec.Code, rec.Body, stub.calls)
	}
}

// TS-34-3: A PAT bound to any workspace, the path's own included, is refused
// with token_bound and reported once per request. Requirement: 34-REQ-1.3
func TestMint_BoundPAT_TS_34_3(t *testing.T) {
	stub := newStub(http.StatusCreated, okAnswer)
	env := newTestEnv(t, Config{APIKit: stub})
	seedWorkspace(t, env.db, "S", "u1", "ready")
	seedWorkspace(t, env.db, "X", "u1", "ready")
	bind(t, env.db, "T", "S")
	reports := recordRefusals(t)

	for i, slug := range []string{"S", "X"} {
		rec := env.do(http.MethodPost, "/api/v1/workspaces/"+slug+"/tokens", pat("u1", "T", "tokens:manage"), `{"name":"n"}`, nil)
		b := parseError(t, rec)
		if rec.Code != http.StatusForbidden || b.Error.ErrorType != "token_bound" || b.Error.Message != "this token is bound to workspace S" {
			t.Errorf("%s: (%d, %s); want 403 token_bound: this token is bound to workspace S", slug, rec.Code, rec.Body)
		}
		if len(*reports) != i+1 || (*reports)[i].Status != http.StatusForbidden || (*reports)[i].Surface != "api" || (*reports)[i].RequestedSlug != slug {
			t.Errorf("%s: reports %+v; want one more with status 403, surface api", slug, *reports)
		}
	}
	var pats int
	_ = env.db.QueryRow(`SELECT COUNT(*) FROM pats`).Scan(&pats)
	if stub.calls != 0 || pats != 0 {
		t.Errorf("apikit calls %d, pats rows %d; want 0 and 0", stub.calls, pats)
	}
}

// TS-34-4: A binding lookup error for a PAT caller answers 500.
// Requirement: 34-REQ-1.4
func TestMint_LookupError_TS_34_4(t *testing.T) {
	stub := newStub(http.StatusCreated, okAnswer)
	env := newTestEnv(t, Config{APIKit: stub})
	seedWorkspace(t, env.db, "s", "u1", "ready")
	if _, err := env.db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	rec := env.do(http.MethodPost, mintPath, pat("u1", "T", "tokens:manage"), `{"name":"n"}`, nil)
	if rec.Code != http.StatusInternalServerError || parseError(t, rec).Error.Message != "internal server error" || stub.calls != 0 {
		t.Errorf("(%d, %s), apikit calls %d; want 500 internal server error and no call", rec.Code, rec.Body, stub.calls)
	}
}

// TS-34-5: An admin token cannot mint a bound token. Requirement: 34-REQ-1.5
func TestMint_Admin_TS_34_5(t *testing.T) {
	stub := newStub(http.StatusCreated, okAnswer)
	env := newTestEnv(t, Config{APIKit: stub})
	seedWorkspace(t, env.db, "s", "u1", "ready")
	rec := env.do(http.MethodPost, mintPath, admin(), `{"name":"n"}`, nil)
	const want = "admin tokens cannot mint bound tokens; a real user is required as the token's owner"
	if rec.Code != http.StatusForbidden || parseError(t, rec).Error.Message != want || stub.calls != 0 {
		t.Errorf("(%d, %s), apikit calls %d; want 403 %q and no call", rec.Code, rec.Body, stub.calls, want)
	}
}

// TS-34-6: A missing workspace and a workspace of another owner answer 404.
// (Every caller AuthorizeWorkspace allows is the owner today, so the explicit
// owner check is exercised through the non-owner cases; erratum 34.)
// Requirement: 34-REQ-1.6
func TestMint_NotFound_TS_34_6(t *testing.T) {
	stub := newStub(http.StatusCreated, okAnswer)
	env := newTestEnv(t, Config{APIKit: stub})
	seedWorkspace(t, env.db, "a", "u1", "ready")
	seedWorkspace(t, env.db, "b", "u2", "ready")
	for _, c := range []struct {
		slug string
		auth *apikit.AuthInfo
	}{
		{"missing", apiKey("u1")}, {"b", apiKey("u1")}, {"a", apiKey("u2")}, {"b", pat("u1", "T", "tokens:manage")},
	} {
		rec := env.do(http.MethodPost, "/api/v1/workspaces/"+c.slug+"/tokens", c.auth, `{"name":"n"}`, nil)
		if rec.Code != http.StatusNotFound || parseError(t, rec).Error.Message != "workspace not found" {
			t.Errorf("%s as %s: (%d, %s); want 404 workspace not found", c.slug, c.auth.UserID, rec.Code, rec.Body)
		}
	}
	if stub.calls != 0 {
		t.Errorf("apikit calls = %d; want 0", stub.calls)
	}
}

// TS-34-7: The owner of an archived workspace is refused with 400.
// Requirement: 34-REQ-1.7
func TestMint_Archived_TS_34_7(t *testing.T) {
	stub := newStub(http.StatusCreated, okAnswer)
	env := newTestEnv(t, Config{APIKit: stub})
	seedWorkspace(t, env.db, "s", "u1", "archived")
	rec := env.do(http.MethodPost, mintPath, apiKey("u1"), `{"name":"n"}`, nil)
	if rec.Code != http.StatusBadRequest || parseError(t, rec).Error.Message != "workspace is not active" || stub.calls != 0 {
		t.Errorf("(%d, %s), apikit calls %d; want 400 workspace is not active and no call", rec.Code, rec.Body, stub.calls)
	}
}

// TS-34-8 (property): apikit is invoked exactly when all six checks pass, the
// first failing check answers, and hub does no tokens:manage check.
// Requirement: 34-REQ-1.8
func TestMint_PropertyCheckOrder_TS_34_8(t *testing.T) {
	type caller struct {
		name       string
		auth       *apikit.AuthInfo
		bound      bool
		lookupFail bool
	}
	callers := []caller{
		{"none", nil, false, false},
		{"bound pat", pat("u1", "TB", "tokens:manage"), true, false},
		{"bound pat, lookup fails", pat("u1", "TB", "tokens:manage"), true, true},
		{"admin", admin(), false, false},
		{"api_key", apiKey("u1"), false, false},
		{"pat without tokens:manage", pat("u1", "TU", "workspaces:read"), false, false},
	}
	workspaces := map[string]struct{ owner, status string }{
		"missing": {}, "other": {"u2", "ready"}, "archived": {"u1", "archived"}, "ready": {"u1", "ready"},
	}
	for _, c := range callers {
		for wsName, w := range workspaces {
			stub := newStub(http.StatusCreated, okAnswer)
			env := newTestEnv(t, Config{APIKit: stub})
			if w.owner != "" {
				seedWorkspace(t, env.db, "s", w.owner, w.status)
			}
			if c.bound {
				bind(t, env.db, "TB", "elsewhere")
			}
			if c.lookupFail {
				if _, err := env.db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
					t.Fatal(err)
				}
			}
			want := 0 // delegated
			switch {
			case c.auth == nil:
				want = http.StatusUnauthorized
			case c.lookupFail:
				want = http.StatusInternalServerError
			case c.bound:
				want = http.StatusForbidden
			case c.name == "admin":
				want = http.StatusForbidden
			case wsName == "missing" || wsName == "other":
				want = http.StatusNotFound
			case wsName == "archived":
				want = http.StatusBadRequest
			}
			rec := env.do(http.MethodPost, mintPath, c.auth, `{"name":"n","permissions":["workspaces:read"]}`, nil)
			if (stub.calls == 1) != (want == 0) || stub.calls > 1 {
				t.Errorf("%s on %s: apikit calls %d; want delegation only when every check passes", c.name, wsName, stub.calls)
			}
			if want != 0 && rec.Code != want {
				t.Errorf("%s on %s: status %d; want %d", c.name, wsName, rec.Code, want)
			}
		}
	}
}

// ctxMarker is the context key of TS-34-9's marker.
type ctxMarker struct{}

// TS-34-9: The inner request carries the caller's body bytes, Authorization
// header verbatim, Content-Type and Content-Length, and the caller's context.
// Requirement: 34-REQ-2.1
func TestMint_InnerRequest_TS_34_9(t *testing.T) {
	stub := newStub(http.StatusCreated, okAnswer)
	db := openTestDB(t)
	seedWorkspace(t, db, "s", "u1", "ready")
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware(), func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), ctxMarker{}, "marker")))
			return next(c)
		}
	})
	RegisterRoutes(api, Config{DB: db, APIKit: stub})
	env := &testEnv{e: e, db: db}

	sent := "{ \"name\" : \"svc\",\n  \"permissions\": [\"workspaces:read\"], \"unknown\": 1 }  "
	header := http.Header{"Authorization": {"Bearer ak_key1_verbatim-SECRET"}}
	env.do(http.MethodPost, mintPath, apiKey("u1"), sent, header)
	if stub.calls != 1 {
		t.Fatalf("apikit calls = %d; want 1", stub.calls)
	}
	r := stub.req
	if r.Method != http.MethodPost || r.URL.Path != "/api/v1/user/tokens" {
		t.Errorf("inner request %s %s; want POST /api/v1/user/tokens", r.Method, r.URL.Path)
	}
	if string(stub.body) != sent {
		t.Errorf("inner body %q; want the caller's bytes %q", stub.body, sent)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer ak_key1_verbatim-SECRET" {
		t.Errorf("inner Authorization %q; want the caller's verbatim", got)
	}
	if r.Header.Get("Content-Type") != "application/json" || r.ContentLength != int64(len(sent)) || r.Header.Get("Content-Length") != strconv.Itoa(len(sent)) {
		t.Errorf("inner Content-Type %q, Content-Length %d/%q; want application/json and %d", r.Header.Get("Content-Type"), r.ContentLength, r.Header.Get("Content-Length"), len(sent))
	}
	if r.Context().Value(ctxMarker{}) != "marker" {
		t.Error("the inner request does not carry the caller's context")
	}
}

// TS-34-10 (property): The inner path is the route template minus
// /workspaces/:slug/tokens plus /user/tokens, for any mount prefix.
// Requirement: 34-REQ-2.2
func TestMint_PropertyInnerPath_TS_34_10(t *testing.T) {
	for _, prefix := range []string{"", "/api/v1", "/x", "/a/b/c"} {
		stub := newStub(http.StatusCreated, okAnswer)
		db := openTestDB(t)
		seedWorkspace(t, db, "s", "u1", "ready")
		e := echo.New()
		g := e.Group(prefix)
		g.Use(testAuthMiddleware())
		RegisterRoutes(g, Config{DB: db, APIKit: stub})
		(&testEnv{e: e, db: db}).do(http.MethodPost, prefix+"/workspaces/s/tokens", apiKey("u1"), `{"name":"n"}`, nil)
		if stub.calls != 1 || stub.req.URL.Path != prefix+"/user/tokens" {
			t.Errorf("prefix %q: apikit calls %d, path %v; want one call to %s/user/tokens", prefix, stub.calls, stub.req, prefix)
		}
	}
}
