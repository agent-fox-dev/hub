package workspace

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// TestAccess_Scopes: the workspace scopes hub registers, and what a PAT may do
// with each: workspaces:write implies workspaces:read, no other scope does;
// keys and admin tokens pass both.
func TestAccess_Scopes(t *testing.T) {
	var names []string
	for _, p := range WorkspacePermissions() {
		names = append(names, p.Resource+":"+p.Action)
	}
	if strings.Join(names, " ") != "workspaces:read workspaces:write patches:read patches:write" {
		t.Errorf("permissions %v", names)
	}
	for _, c := range []struct {
		auth        *apikit.AuthInfo
		read, write bool
	}{
		{testutil.PAT(owner, "workspaces:read"), true, false},
		{testutil.PAT(owner, "workspaces:write"), true, true},
		{testutil.PAT(owner, "patches:write", "users:read"), false, false},
		{testutil.PAT(owner), false, false},
		{testutil.User(owner), true, true},
		{testutil.Admin(), true, true},
	} {
		if hasReadAccess(c.auth) != c.read || hasWriteAccess(c.auth) != c.write {
			t.Errorf("%v: read %v write %v; want %v %v", c.auth.Permissions, hasReadAccess(c.auth), hasWriteAccess(c.auth), c.read, c.write)
		}
	}
}

// TestAccess_Matrix: every workspace route answers each kind of caller the
// same way: admin tokens and the owner (by key or write PAT) are let in, a
// read PAT or a PAT without the scope is refused by the route, a member of the
// org may read and is refused writes with 403, a stranger gets 404 as if the
// workspace did not exist, no credential is 401.
func TestAccess_Matrix(t *testing.T) {
	v := newEnv(t)
	stubSync(t, upstreamSHA, "up_to_date", nil, nil)
	stubPush(t, func(context.Context, transport.AuthMethod) error { return nil })
	testutil.SeedOrg(t, v.db, "org-acme", "acme", "")
	testutil.SeedOrgMember(t, v.db, "org-acme", owner)
	testutil.SeedOrgMember(t, v.db, "org-acme", member)
	testutil.BindToken(t, v.db, "tok-other", "elsewhere")

	type route struct {
		name, method string
		path         func(slug string) string
		body         string
		archived     bool
		trunk        bool
	}
	routes := []route{
		{"get", http.MethodGet, func(s string) string { return "/api/v1/workspaces/" + s }, "", false, false},
		{"patch", http.MethodPatch, func(s string) string { return "/api/v1/workspaces/" + s }, `{"description":"x"}`, false, false},
		{"archive", http.MethodPost, func(s string) string { return "/api/v1/workspaces/" + s + "/archive" }, "", false, false},
		{"reactivate", http.MethodPost, func(s string) string { return "/api/v1/workspaces/" + s + "/reactivate" }, "", true, false},
		{"sync", http.MethodPost, func(s string) string { return "/api/v1/workspaces/" + s + "/sync" }, "", false, false},
		{"reclone", http.MethodPost, func(s string) string { return "/api/v1/workspaces/" + s + "/reclone" }, "", false, false},
		{"delete", http.MethodDelete, func(s string) string { return "/api/v1/workspaces/" + s }, "", true, false},
		{"push", http.MethodPost, func(s string) string { return "/api/v1/workspaces/" + s + "/branches/main/push" }, "", false, true},
		{"patches", http.MethodGet, func(s string) string { return "/api/v1/workspaces/" + s + "/patches" }, "", false, false},
	}
	callers := []struct {
		name string
		auth *apikit.AuthInfo
		want []int // per route
	}{
		{"unauthenticated", nil, []int{401, 401, 401, 401, 401, 401, 401, 401, 401}},
		{"admin", testutil.Admin(), []int{200, 200, 200, 200, 200, 200, 204, 200, 200}},
		{"owner key", testutil.User(owner), []int{200, 200, 200, 200, 200, 200, 204, 200, 200}},
		{"owner write PAT", testutil.PAT(owner, "workspaces:write", "patches:read"), []int{200, 200, 200, 200, 200, 200, 204, 200, 200}},
		{"owner read PAT", testutil.PAT(owner, "workspaces:read", "patches:read"), []int{200, 404, 404, 404, 403, 403, 404, 403, 200}},
		{"other scope PAT", testutil.PAT(owner, "users:read"), []int{404, 404, 404, 404, 403, 403, 404, 403, 403}},
		{"bound elsewhere", testutil.BoundPAT(owner, "tok-other", "workspaces:write", "patches:read"), []int{404, 404, 404, 404, 404, 404, 404, 404, 404}},
		{"member key", testutil.User(member), []int{200, 403, 403, 403, 403, 403, 403, 403, 200}},
		{"stranger key", testutil.User(stranger), []int{404, 404, 404, 404, 404, 404, 404, 404, 404}},
	}
	for ci, c := range callers {
		for ri, r := range routes {
			slug := fmt.Sprintf("m-%d-%d", ci, ri)
			ws := &Workspace{Slug: slug, OwnerID: owner, OrgID: strPtr("org-acme"), CloneStatus: "ready"}
			if r.archived {
				ws.Status, ws.CloneStatus = "archived", "archived"
			}
			if r.trunk {
				v.seedPushWorkspace(t, ws)
			} else {
				v.seed(t, ws)
			}
			rec := v.do(t, r.method, r.path(slug), r.body, c.auth)
			if rec.Code != c.want[ri] {
				t.Errorf("%s %s: %d; want %d: %s", c.name, r.name, rec.Code, c.want[ri], rec.Body)
			}
			if rec.Code == http.StatusNotFound && r.name != "push" {
				if msg := testutil.Error(t, rec).Message; msg != "workspace not found" {
					t.Errorf("%s %s: 404 %q; want the anti-enumeration message", c.name, r.name, msg)
				}
			}
			if rec.Code == http.StatusForbidden && c.name == "member key" {
				if e := testutil.Error(t, rec); e.ErrorType != wsaccess.ErrorTypeForbidden || e.Message != "org members have read-only access to this workspace" {
					t.Errorf("%s %s: %+v", c.name, r.name, e)
				}
			}
		}
	}
	// A member's read sees the owner's view with access member.
	v.seed(t, &Workspace{Slug: "view", OwnerID: owner, OrgID: strPtr("org-acme")})
	got := testutil.Object(t, v.do(t, http.MethodGet, "/api/v1/workspaces/view", "", testutil.User(member)))
	if got["access"] != "member" || got["owner_id"] != owner || got["git_url"] != "https://github.com/org/view" {
		t.Errorf("member's view %v", got)
	}
}

// TestAccess_Lookup: lookupWorkspaceForAuth applies the binding rule, reports
// a bound refusal once with the request's method and route, answers 500 when
// the bindings cannot be read, and 404 when the row goes between the decision
// and the load.
func TestAccess_Lookup(t *testing.T) {
	v := newEnv(t)
	var reports []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { reports = append(reports, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })
	v.seed(t, &Workspace{Slug: "s", OwnerID: owner})
	v.seed(t, &Workspace{Slug: "x", OwnerID: owner})
	testutil.BindToken(t, v.db, "tokB", "s")
	bound := testutil.BoundPAT(owner, "tokB", "workspaces:read")

	c, rec := contextOf(http.MethodGet, "/api/v1/workspaces/x", "/api/v1/workspaces/:slug")
	if ws, access := lookupWorkspaceForAuth(c, v.db, "x", bound, wsaccess.NeedRead); ws != nil || access != wsaccess.AccessNone || rec.Code != http.StatusNotFound {
		t.Fatalf("another workspace: %v %q %d", ws, access, rec.Code)
	}
	want := wsaccess.BoundRefusal{TokenID: "tokB", UserID: owner, BoundSlug: "s", RequestedSlug: "x", Method: "GET",
		Route: "/api/v1/workspaces/:slug", Status: 404, Surface: wsaccess.SurfaceAPI}
	if len(reports) != 1 || reports[0] != want {
		t.Errorf("reports %+v; want %+v", reports, want)
	}
	c, _ = contextOf(http.MethodGet, "/api/v1/workspaces/s", "/api/v1/workspaces/:slug")
	if ws, access := lookupWorkspaceForAuth(c, v.db, "s", bound, wsaccess.NeedRead); ws == nil || ws.Slug != "s" || access != wsaccess.AccessOwner || len(reports) != 1 {
		t.Errorf("its own workspace: %v %q, %d reports", ws, access, len(reports))
	}
	if a, code, msg := AuthorizeWorkspace(v.db, testutil.User(member), "s", wsaccess.NeedRead); a != nil || code != http.StatusNotFound || msg != "workspace not found" {
		t.Errorf("AuthorizeWorkspace for a stranger = %v %d %q", a, code, msg)
	}

	// The row goes between the decision and the load.
	swap(t, &afterAccessDecision, func(slug string) { testutil.Exec(t, v.db, `DELETE FROM workspaces WHERE slug = ?`, slug) })
	c, rec = contextOf(http.MethodGet, "/api/v1/workspaces/x", "/api/v1/workspaces/:slug")
	if ws, _ := lookupWorkspaceForAuth(c, v.db, "x", testutil.User(owner), wsaccess.NeedRead); ws != nil || rec.Code != http.StatusNotFound {
		t.Errorf("vanished row: %v %d", ws, rec.Code)
	}
	swap(t, &afterAccessDecision, nil)

	testutil.Exec(t, v.db, `DROP TABLE token_workspace_bindings`)
	c, rec = contextOf(http.MethodGet, "/api/v1/workspaces/s", "/api/v1/workspaces/:slug")
	if ws, _ := lookupWorkspaceForAuth(c, v.db, "s", bound, wsaccess.NeedRead); ws != nil || rec.Code != http.StatusInternalServerError || testutil.Error(t, rec).Message != "internal server error" {
		t.Errorf("unreadable bindings: %v %d %s", ws, rec.Code, rec.Body)
	}
	if a, code, _ := AuthorizeWorkspace(v.db, bound, "s", wsaccess.NeedPrivate); a != nil || code != http.StatusInternalServerError {
		t.Errorf("AuthorizeWorkspace with unreadable bindings = %v %d", a, code)
	}
	testutil.Exec(t, v.db, `DROP TABLE workspaces`)
	c, rec = contextOf(http.MethodGet, "/api/v1/workspaces/s", "/api/v1/workspaces/:slug")
	if ws, _ := lookupWorkspaceForAuth(c, v.db, "s", testutil.Admin(), wsaccess.NeedRead); ws != nil || rec.Code != http.StatusInternalServerError {
		t.Errorf("unreadable workspaces: %v %d", ws, rec.Code)
	}
}

// TestHub_Mount: MountWorkspaceHandlers creates the schema and the binding
// table, registers the workspace scopes so that apikit mints PATs with them,
// records apikit's own routes without the health routes, and guards every
// route registered later.
func TestHub_Mount(t *testing.T) {
	h := newHub(t)
	if !columns(t, h.db, "token_workspace_bindings")["token_id"] || !columns(t, h.db, "workspaces")["settings"] {
		t.Error("the schema was not created")
	}
	recorded := map[string]bool{}
	for _, r := range recordedAPIKitRoutes {
		recorded[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{"GET /api/v1/user", "POST /api/v1/user/tokens", "GET /api/v1/orgs/:id"} {
		if !recorded[want] {
			t.Errorf("recorded apikit routes lack %s", want)
		}
	}
	for _, not := range []string{"GET /healthz", "GET /readyz", "GET /version", "GET /api/v1/workspaces", "POST /api/v1/workspaces"} {
		if recorded[not] {
			t.Errorf("recorded %s", not)
		}
	}
	// The registry accepts the workspace scopes and refuses an unknown one.
	testutil.MintPAT(t, h.server.Echo(), h.apiKey, "workspaces:read", "workspaces:write", "patches:read", "patches:write")
	if rec := h.do(http.MethodPost, "/api/v1/user/tokens", h.apiKey, `{"name":"t","permissions":["workspaces:fly"]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("minting with an unknown scope: %d %s", rec.Code, rec.Body)
	}

	// Later routes are covered by the binding guard.
	calls := 0
	h.server.APIGroup().GET("/workspaces/:slug/later", func(c echo.Context) error { calls++; return c.NoContent(http.StatusNoContent) })
	insertWorkspace(h.db, &Workspace{Slug: "s", GitURL: "https://h/s", OwnerID: "u1", Status: "active"})
	insertWorkspace(h.db, &Workspace{Slug: "x", GitURL: "https://h/x", OwnerID: "u1", Status: "active"})
	bound := h.boundPAT(t, "s", "workspaces:read")
	if rec := h.do(http.MethodGet, "/api/v1/workspaces/x/later", bound, ""); rec.Code != http.StatusNotFound || calls != 0 {
		t.Errorf("later route on another workspace: %d, %d calls", rec.Code, calls)
	}
	if rec := h.do(http.MethodGet, "/api/v1/workspaces/s/later", bound, ""); rec.Code != http.StatusNoContent || calls != 1 {
		t.Errorf("later route on its own workspace: %d, %d calls", rec.Code, calls)
	}
}

// TestHub_BoundToken: through the real stack a bound token gets exactly the
// answers an unbound PAT with the same scopes gets on its own workspace, 404
// on another, is refused on apikit's own routes without the workspace being
// named and cannot mint a token, and lists at most its workspace.
func TestHub_BoundToken(t *testing.T) {
	h := newHub(t)
	for _, slug := range []string{"s", "x"} {
		insertWorkspace(h.db, &Workspace{Slug: slug, GitURL: "https://h/" + slug, OwnerID: "u1", Status: "active"})
	}
	bound := h.boundPAT(t, "s", "workspaces:read", "patches:read", "tokens:manage", "users:read")
	unbound, _ := testutil.MintPAT(t, h.server.Echo(), h.apiKey, "workspaces:read", "patches:read", "tokens:manage", "users:read")
	for _, path := range []string{"/api/v1/workspaces/s", "/api/v1/workspaces/s/patches"} {
		b, u := h.do(http.MethodGet, path, bound, ""), h.do(http.MethodGet, path, unbound, "")
		if b.Code != http.StatusOK || b.Code != u.Code || b.Body.String() != u.Body.String() {
			t.Errorf("%s: bound (%d, %s); unbound (%d, %s)", path, b.Code, b.Body, u.Code, u.Body)
		}
	}
	for _, path := range []string{"/api/v1/workspaces/x", "/api/v1/workspaces/x/patches"} {
		if rec := h.do(http.MethodGet, path, bound, ""); rec.Code != http.StatusNotFound || testutil.Error(t, rec).Message != "workspace not found" {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := h.do(http.MethodGet, "/api/v1/workspaces", bound, ""); rec.Code != http.StatusOK || len(testutil.Objects(t, rec)) != 1 {
		t.Errorf("listing: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodGet, "/api/v1/workspaces", h.apiKey, ""); len(testutil.Objects(t, rec)) != 2 {
		t.Errorf("the key's listing: %s", rec.Body)
	}
	before := testutil.Count(t, h.db, `SELECT COUNT(*) FROM pats`)
	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/user", ""},
		{http.MethodPost, "/api/v1/user/tokens", `{"name":"escape","permissions":["tokens:manage"]}`},
	} {
		rec := h.do(r.method, r.path, bound, r.body)
		e := testutil.Error(t, rec)
		if rec.Code != http.StatusForbidden || e.ErrorType != wsaccess.ErrorTypeTokenBound || e.Message != "this token is bound to a workspace" || strings.Contains(rec.Body.String(), `"s"`) {
			t.Errorf("%s %s: %d %s", r.method, r.path, rec.Code, rec.Body)
		}
	}
	if after := testutil.Count(t, h.db, `SELECT COUNT(*) FROM pats`); after != before {
		t.Error("the bound token minted a token")
	}
	if rec := h.do(http.MethodPost, "/api/v1/user/tokens", unbound, `{"name":"ok","permissions":["tokens:manage"]}`); rec.Code != http.StatusCreated {
		t.Errorf("the unbound twin minting: %d %s", rec.Code, rec.Body)
	}
	// A bound token is refused on a hub route of another kind as well, with
	// the workspace named.
	if rec := h.do(http.MethodGet, "/api/v1/user/secrets", bound, ""); rec.Code != http.StatusForbidden || testutil.Error(t, rec).Message != "this token is bound to workspace s" {
		t.Errorf("GET /user/secrets: %d %s", rec.Code, rec.Body)
	}
}

// TestHub_Members: through the real stack a member of the org lists and reads
// a teammate's workspace and its patches, and loses that when removed.
func TestHub_Members(t *testing.T) {
	h := newHub(t)
	testutil.SeedUser(t, h.db, "u2")
	testutil.SeedOrg(t, h.db, "org-acme", "acme", "")
	testutil.SeedOrgMember(t, h.db, "org-acme", "u1")
	testutil.SeedOrgMember(t, h.db, "org-acme", "u2")
	insertWorkspace(h.db, &Workspace{Slug: "team", GitURL: "https://h/w", OwnerID: "u1", OrgID: strPtr("org-acme"), Status: "active", CloneStatus: "ready",
		WorkspaceMode: "carry_patch", UpstreamURL: strPtr("https://h/up"), IntegrationBranch: strPtr("deploy")})
	testutil.Exec(t, h.db, `INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at) VALUES ('p1', 'team', 'feature/a', 1, 'active', '', '')`)
	memberKey := testutil.APIKey(t, h.db, "u2")

	rec := h.do(http.MethodGet, "/api/v1/workspaces", memberKey, "")
	if list := testutil.Objects(t, rec); rec.Code != http.StatusOK || len(list) != 1 || list[0]["slug"] != "team" || list[0]["access"] != "member" || list[0]["org_slug"] != "acme" {
		t.Errorf("listing: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(http.MethodGet, "/api/v1/workspaces/team/patches", memberKey, "")
	if patches := testutil.Objects(t, rec); rec.Code != http.StatusOK || len(patches) != 1 || patches[0]["id"] != "p1" {
		t.Errorf("patches: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodPost, "/api/v1/workspaces/team/patches", memberKey, `{"branch_name":"feature/b"}`); rec.Code != http.StatusForbidden {
		t.Errorf("a member's patch add: %d %s", rec.Code, rec.Body)
	}
	testutil.Exec(t, h.db, `DELETE FROM org_members WHERE user_id = 'u2'`)
	if rec := h.do(http.MethodGet, "/api/v1/workspaces/team", memberKey, ""); rec.Code != http.StatusNotFound {
		t.Errorf("after removal: %d %s", rec.Code, rec.Body)
	}
}
