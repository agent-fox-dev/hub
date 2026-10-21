package gitserver

import (
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// Spec 37 — authorizeGitAccess decides through wsaccess.AuthorizeWorkspaceAt.

// Route templates of the three git routes, as c.Path() reports them.
const (
	infoRefsRoute    = "/git/:org/:slug.git/info/refs"
	uploadPackRoute  = "/git/:org/:slug.git/git-upload-pack"
	receivePackRoute = "/git/:org/:slug.git/git-receive-pack"
)

// readOnlyBody is the pkt-line body of a member's refused push.
var readOnlyBody = string(encodePktLine("ERR org members have read-only access to this workspace\n"))

// notFoundBody is the pkt-line body of the git server's 404.
var notFoundBody = string(encodePktLine("ERR repository not found\n"))

// newMemberGitEnv is a git test env with org myorg (org-1) holding user-a
// and user-b, workspace myws in it owned by user-a with a repository, and
// user-2 a stranger.
func newMemberGitEnv(t *testing.T) *gitTestEnv {
	t.Helper()
	env := newGitTestEnv(t)
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedOrgMember(t, "org-1", "user-b")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")
	return env
}

// authzContext is an echo context for method target matched to route, with
// info as its credential (none when nil).
func authzContext(method, target, route string, info *apikit.AuthInfo) (echo.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(method, target, nil), rec)
	c.SetPath(route)
	if info != nil {
		apikit.SetAuthInfo(c, info)
	}
	return c, rec
}

// myws is the resolver's view of the workspace of newMemberGitEnv.
var myws = &workspaceInfo{Slug: "myws", OwnerID: "user-a", OrgID: "org-1", CloneStatus: "ready"}

// TS-37-1: the owner, a member and an admin token (no user id, no
// membership) are granted; a stranger gets 404. Requirement: 37-REQ-1.1
func TestAuthorizeGitAccess_Decides_TS_37_1(t *testing.T) {
	env := newMemberGitEnv(t)
	for name, info := range map[string]*apikit.AuthInfo{
		"owner":  {CredentialType: "api_key", UserID: "user-a"},
		"member": {CredentialType: "api_key", UserID: "user-b"},
		"admin":  {CredentialType: "admin_token"},
	} {
		c, rec := authzContext(http.MethodPost, "/git/myorg/myws.git/git-upload-pack", uploadPackRoute, info)
		if err := authorizeGitAccess(c, env.db, myws); err != nil || c.Response().Committed || rec.Body.Len() != 0 {
			t.Errorf("%s: err %v, committed %v, body %q; want granted with nothing written", name, err, c.Response().Committed, rec.Body)
		}
	}
	c, rec := authzContext(http.MethodPost, "/git/myorg/myws.git/git-upload-pack", uploadPackRoute, &apikit.AuthInfo{CredentialType: "api_key", UserID: "user-2"})
	if err := authorizeGitAccess(c, env.db, myws); !errors.Is(err, errAccessDenied) || rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
		t.Errorf("stranger: err %v, (%d, %q); want errAccessDenied and 404 %q", err, rec.Code, rec.Body, notFoundBody)
	}
}

// TS-37-2: with no AuthInfo, authorizeGitAccess returns 401 before any
// query (a nil database would panic on one) and writes nothing.
// Requirement: 37-REQ-1.2
func TestAuthorizeGitAccess_NoAuth401_TS_37_2(t *testing.T) {
	c, rec := authzContext(http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-upload-pack", infoRefsRoute, nil)
	err := authorizeGitAccess(c, nil, myws)
	var he *echo.HTTPError
	if !errors.As(err, &he) || he.Code != http.StatusUnauthorized {
		t.Errorf("err %v; want *echo.HTTPError 401", err)
	}
	if rec.Body.Len() != 0 || c.Response().Committed {
		t.Errorf("wrote %q; want nothing", rec.Body)
	}
}

// TS-37-3: no non-test source in internal/gitserver compares an owner id
// with the caller, and authz.go decides through AuthorizeWorkspaceAt with
// no DeniesWorkspace call and no admin_token check. Requirement: 37-REQ-1.3
func TestGitserver_NoOwnerComparison_TS_37_3(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	names := func(e ast.Expr) string {
		var out []string
		ast.Inspect(e, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				out = append(out, x.Name)
			case *ast.BasicLit:
				out = append(out, x.Value)
			}
			return true
		})
		return strings.ToLower(strings.Join(out, " "))
	}
	callsAuthorize := false
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BinaryExpr:
				if x.Op != token.EQL && x.Op != token.NEQ {
					return true
				}
				l, r := names(x.X), names(x.Y)
				owner := func(s string) bool { return strings.Contains(s, "ownerid") || strings.Contains(s, "owner_id") }
				if (owner(l) && strings.Contains(r, "userid")) || (owner(r) && strings.Contains(l, "userid")) {
					t.Errorf("%s compares an owner id with the caller", fset.Position(x.Pos()))
				}

			case *ast.BasicLit:
				if name == "authz.go" && x.Value == `"admin_token"` {
					t.Errorf("%s: authz.go checks admin_token itself", fset.Position(x.Pos()))
				}
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && id.Name == "wsaccess" && name == "authz.go" {
					switch x.Sel.Name {
					case "DeniesWorkspace":
						t.Errorf("%s: authz.go calls wsaccess.DeniesWorkspace", fset.Position(x.Pos()))
					case "AuthorizeWorkspaceAt":
						callsAuthorize = true
					}
				}
			}
			return true
		})
	}
	if !callsAuthorize {
		t.Error("authz.go does not call wsaccess.AuthorizeWorkspaceAt")
	}
}

// authorizeGitAccess keeps its signature (37-REQ-1.4).
var _ func(echo.Context, *sql.DB, *workspaceInfo) error = authorizeGitAccess

// TS-37-4: a refusal written by authorizeGitAccess is the one response the
// client receives through the resolver. Requirement: 37-REQ-1.4
func TestResolver_RefusalIsTheOnlyResponse_TS_37_4(t *testing.T) {
	env := newMemberGitEnv(t)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-upload-pack"},
		{http.MethodPost, "/git/myorg/myws.git/git-upload-pack"},
	} {
		rec := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", "af_key_user2"))
		if rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
			t.Errorf("%s %s: (%d, %q); want exactly 404 %q", r.method, r.path, rec.Code, rec.Body, notFoundBody)
		}
	}
}

// TS-37-5: receive-pack requests need write: a member is refused with the
// read-only 403 and the owner is served. Requirement: 37-REQ-2.1
func TestReceivePack_NeedWrite_TS_37_5(t *testing.T) {
	env := newMemberGitEnv(t)
	// A repeated service parameter: the first value decides, as in
	// handleInfoRefs.
	if m := env.doRequest(t, http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-upload-pack&service=git-receive-pack", "", withBasicAuth("x", "af_key_userB")); m.Code != http.StatusOK {
		t.Errorf("member, upload-pack first: (%d, %q); want 200", m.Code, m.Body)
	}
	if m := env.doRequest(t, http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-receive-pack&service=git-upload-pack", "", withBasicAuth("x", "af_key_userB")); m.Code != http.StatusForbidden || m.Body.String() != readOnlyBody {
		t.Errorf("member, receive-pack first: (%d, %q); want 403 %q", m.Code, m.Body, readOnlyBody)
	}
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-receive-pack"},
		{http.MethodPost, "/git/myorg/myws.git/git-receive-pack"},
	} {
		m := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", "af_key_userB"))
		if m.Code != http.StatusForbidden || m.Body.String() != readOnlyBody {
			t.Errorf("member %s %s: (%d, %q); want 403 %q", r.method, r.path, m.Code, m.Body, readOnlyBody)
		}
		if o := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", "af_key_userA")); o.Code != http.StatusOK {
			t.Errorf("owner %s %s: (%d, %q); want 200", r.method, r.path, o.Code, o.Body)
		}
	}
}

// TS-37-16: a wsaccess 404 is written with the git server's own text, for
// a stranger and for a workspace row that is missing.
// Requirement: 37-REQ-5.1
func TestAuthorizeGitAccess_404Text_TS_37_16(t *testing.T) {
	env := newMemberGitEnv(t)
	for name, ws := range map[string]*workspaceInfo{
		"stranger":    myws,
		"missing row": {Slug: "nope", OwnerID: "user-2", OrgID: "org-1", CloneStatus: "ready"},
	} {
		c, rec := authzContext(http.MethodGet, "/git/myorg/x.git/info/refs?service=git-upload-pack", infoRefsRoute, &apikit.AuthInfo{CredentialType: "api_key", UserID: "user-2"})
		err := authorizeGitAccess(c, env.db, ws)
		if !errors.Is(err, errAccessDenied) || rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody ||
			rec.Header().Get("Content-Type") != "text/plain" || strings.Contains(rec.Body.String(), "workspace not found") {
			t.Errorf("%s: err %v, (%d, %q, %q); want errAccessDenied and text/plain 404 %q", name, err, rec.Code, rec.Header().Get("Content-Type"), rec.Body, notFoundBody)
		}
	}
}

// TS-37-17: a wsaccess 403 is written as a pkt-line with its message.
// Requirement: 37-REQ-5.2
func TestAuthorizeGitAccess_403Message_TS_37_17(t *testing.T) {
	env := newMemberGitEnv(t)
	c, rec := authzContext(http.MethodPost, "/git/myorg/myws.git/git-receive-pack", receivePackRoute, &apikit.AuthInfo{CredentialType: "api_key", UserID: "user-b"})
	err := authorizeGitAccess(c, env.db, myws)
	if !errors.Is(err, errAccessDenied) || rec.Code != http.StatusForbidden || rec.Body.String() != readOnlyBody || rec.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("err %v, (%d, %q, %q); want errAccessDenied and text/plain 403 %q", err, rec.Code, rec.Header().Get("Content-Type"), rec.Body, readOnlyBody)
	}
}

// TS-37-18: a failed membership read during the decision is a pkt-line 500
// and nothing is served. Requirement: 37-REQ-5.3
func TestResolver_MembershipError500_TS_37_18(t *testing.T) {
	env := newMemberGitEnv(t)
	if _, err := env.db.Exec(`DROP TABLE org_members`); err != nil {
		t.Fatal(err)
	}
	rec := env.doRequest(t, http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-upload-pack", "", withBasicAuth("x", "af_key_user2"))
	want := string(encodePktLine("ERR internal server error\n"))
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != want || rec.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("(%d, %q); want text/plain 500 %q and no advertisement", rec.Code, rec.Body, want)
	}
}

// TS-37-31: a workspace row deleted after the resolver read it is refused
// with the 404. Requirement: 37-REQ-8.4
func TestAuthorizeGitAccess_RowVanished_TS_37_31(t *testing.T) {
	env := newMemberGitEnv(t)
	env.seedWorkspace(t, "gone", "https://github.com/org/gone", "user-a", "org-1", "active")
	gone := &workspaceInfo{Slug: "gone", OwnerID: "user-a", OrgID: "org-1", CloneStatus: "ready"}
	if _, err := env.db.Exec(`DELETE FROM workspaces WHERE slug = 'gone'`); err != nil {
		t.Fatal(err)
	}
	c, rec := authzContext(http.MethodGet, "/git/myorg/gone.git/info/refs?service=git-upload-pack", infoRefsRoute, &apikit.AuthInfo{CredentialType: "api_key", UserID: "user-a"})
	if err := authorizeGitAccess(c, env.db, gone); !errors.Is(err, errAccessDenied) || rec.Code != http.StatusNotFound || rec.Body.String() != notFoundBody {
		t.Errorf("err %v, (%d, %q); want errAccessDenied and 404 %q", err, rec.Code, rec.Body, notFoundBody)
	}
}

// TS-37-34: the stranger fixtures no longer make their non-owner a member:
// newBindingGitEnv has no org_members row for user-b, and the three
// stranger tests seed none for their non-owner. Requirement: 37-REQ-10.1
func TestStrangerFixtures_NoMemberRow_TS_37_34(t *testing.T) {
	env := newBindingGitEnv(t)
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM org_members WHERE user_id = 'user-b'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("newBindingGitEnv: %d org_members rows for user-b (%v); want 0", n, err)
	}
	fset := token.NewFileSet()
	for file, tests := range map[string]map[string]string{
		"authz_test.go":    {"TestAuthz_PATWrongOwner_Returns404": "user-b", "TestAuthz_APIKeyWrongOwner_Returns404": "user-b"},
		"resolver_test.go": {"TestResolver_NonOwnerNonAdmin_Returns404": "user-2"},
	} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			user, ok := tests[fn.Name.Name]
			if !ok {
				continue
			}
			delete(tests, fn.Name.Name)
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "seedOrgMember" {
					for _, a := range call.Args {
						if lit, ok := a.(*ast.BasicLit); ok && lit.Value == `"`+user+`"` {
							t.Errorf("%s seeds %s as a member", fn.Name.Name, user)
						}
					}
				}
				return true
			})
		}
		for missing := range tests {
			t.Errorf("%s not found in %s", missing, file)
		}
	}
}
