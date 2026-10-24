package wsaccess

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// An admin token and the owner have every need; a member of the workspace's
// org reads, is forbidden to write and does not see a private route; a
// stranger, a credential without a user and a nil credential get the
// unknown workspace's 404 (401 for nil), as does an unknown slug.
func TestAuthorizeWorkspace_AccessAndNeeds(t *testing.T) {
	db := newDB(t)
	seedTeam(t, db)
	refusals := captureRefusals(t)
	cases := []struct {
		name   string
		auth   *apikit.AuthInfo
		slug   string
		need   Need
		access Access
		code   int
		msg    string
	}{
		{"admin private", testutil.Admin(), wsSlug, NeedPrivate, AccessAdmin, 0, ""},
		{"owner private", testutil.PAT(owner, "workspaces:read"), wsSlug, NeedPrivate, AccessOwner, 0, ""},
		{"member read", testutil.User(member), wsSlug, NeedRead, AccessMember, 0, ""},
		{"member write", testutil.User(member), wsSlug, NeedWrite, "", http.StatusForbidden, "org members have read-only access to this workspace"},
		{"member private", testutil.User(member), wsSlug, NeedPrivate, "", http.StatusNotFound, "workspace not found"},
		{"stranger read", testutil.User(stranger), wsSlug, NeedRead, "", http.StatusNotFound, "workspace not found"},
		{"no user", &apikit.AuthInfo{CredentialType: "api_key"}, wsSlug, NeedRead, "", http.StatusNotFound, "workspace not found"},
		{"unknown slug", testutil.Admin(), "ghost", NeedRead, "", http.StatusNotFound, "workspace not found"},
		{"nil", nil, wsSlug, NeedRead, "", http.StatusUnauthorized, "authentication required"},
	}
	for _, c := range cases {
		ws, code, msg := AuthorizeWorkspace(db, c.auth, c.slug, c.need)
		if code != c.code || msg != c.msg || (ws == nil) != (c.code != 0) {
			t.Errorf("%s: (%v, %d, %q), want (%d, %q)", c.name, ws, code, msg, c.code, c.msg)
			continue
		}
		if ws != nil && (ws.Access != c.access || ws.Slug != wsSlug || ws.OwnerID != owner || ws.OrgID != orgID || ws.Status != "ready") {
			t.Errorf("%s: record %+v, want access %q and the workspace's row", c.name, ws, c.access)
		}
	}
	if len(*refusals) != 0 {
		t.Errorf("%d refusals reported, want none: access refusals are not binding refusals", len(*refusals))
	}
	// Membership is read on every call: a removed member is a stranger.
	testutil.Exec(t, db, `DELETE FROM org_members WHERE user_id = ?`, member)
	if _, code, _ := AuthorizeWorkspace(db, testutil.User(member), wsSlug, NeedRead); code != http.StatusNotFound {
		t.Errorf("removed member: %d, want 404", code)
	}
	// A workspace without an org has no members.
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "solo", OwnerID: stranger})
	if _, code, _ := AuthorizeWorkspace(db, testutil.User(owner), "solo", NeedRead); code != http.StatusNotFound {
		t.Errorf("non-owner of a workspace without org: %d, want 404", code)
	}
}

// A PAT bound to another workspace gets the unknown workspace's 404 whatever
// its access, and the refusal is reported once with the site: the API's 404
// by AuthorizeWorkspace, the caller's site by AuthorizeWorkspaceAt. The PAT
// reaches its own workspace, and a stranger's refusal is not reported.
func TestAuthorizeWorkspace_BoundTokenSeesOnlyItsWorkspace(t *testing.T) {
	db := newDB(t)
	seedTeam(t, db)
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "other", OwnerID: owner})
	testutil.BindToken(t, db, "tok", "other")
	refusals := captureRefusals(t)
	bound := testutil.BoundPAT(owner, "tok", "workspaces:write")
	if ws, code, _ := AuthorizeWorkspace(db, bound, "other", NeedPrivate); code != 0 || ws.Access != AccessOwner {
		t.Errorf("own workspace: %v %d", ws, code)
	}
	if ws, code, msg := AuthorizeWorkspace(db, bound, wsSlug, NeedRead); ws != nil || code != http.StatusNotFound || msg != "workspace not found" {
		t.Errorf("other workspace: %v %d %q", ws, code, msg)
	}
	site := RefusalSite{Method: "GET", Route: "/git/:slug", Status: http.StatusForbidden, Surface: SurfaceGit}
	if _, code, _ := AuthorizeWorkspaceAt(db, bound, wsSlug, NeedRead, site); code != http.StatusNotFound {
		t.Errorf("other workspace at a site: %d", code)
	}
	AuthorizeWorkspace(db, testutil.User(stranger), wsSlug, NeedRead)
	want := []BoundRefusal{
		{TokenID: "tok", UserID: owner, BoundSlug: "other", RequestedSlug: wsSlug, Status: http.StatusNotFound, Surface: SurfaceAPI},
		{TokenID: "tok", UserID: owner, BoundSlug: "other", RequestedSlug: wsSlug, Method: "GET", Route: "/git/:slug", Status: http.StatusForbidden, Surface: SurfaceGit},
	}
	if len(*refusals) != 2 || (*refusals)[0] != want[0] || (*refusals)[1] != want[1] {
		t.Errorf("refusals %+v\nwant %+v", *refusals, want)
	}
}

// A database error is 500 and never a grant, whichever lookup fails.
func TestAuthorizeWorkspace_DatabaseErrorIs500(t *testing.T) {
	for _, c := range []struct {
		drop string
		auth *apikit.AuthInfo
	}{
		{"workspaces", testutil.User(owner)},
		{"org_members", testutil.User(member)},
		{"token_workspace_bindings", testutil.BoundPAT(owner, "tok", "workspaces:read")},
	} {
		db := newDB(t)
		seedTeam(t, db)
		testutil.Exec(t, db, `DROP TABLE `+c.drop)
		if ws, code, msg := AuthorizeWorkspace(db, c.auth, wsSlug, NeedRead); ws != nil || code != http.StatusInternalServerError || msg != "internal server error" {
			t.Errorf("without %s: (%v, %d, %q)", c.drop, ws, code, msg)
		}
	}
}

// WriteRefusal writes the apikit envelope, with error_type forbidden on a
// 403 only.
func TestWriteRefusal_ErrorTypeOnForbiddenOnly(t *testing.T) {
	for _, c := range []struct {
		status    int
		msg, kind string
	}{
		{http.StatusForbidden, "org members have read-only access to this workspace", ErrorTypeForbidden},
		{http.StatusNotFound, "workspace not found", ""},
		{http.StatusInternalServerError, "internal server error", ""},
	} {
		rec := httptest.NewRecorder()
		c0 := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		if err := WriteRefusal(c0, c.status, c.msg); err != nil {
			t.Fatal(err)
		}
		wantRefusal(t, rec, c.status, c.msg, c.kind)
	}
}

func TestCredentialHelpers(t *testing.T) {
	pat := testutil.PAT("u", "a:read", "b:write")
	if !IsAdmin(testutil.Admin()) || IsAdmin(pat) || !IsPAT(pat) || IsPAT(testutil.User("u")) {
		t.Error("IsAdmin or IsPAT misreads a credential type")
	}
	if !HasScope(pat, "b:write") || !HasScope(pat, "x", "a:read") || HasScope(pat, "a:write") || HasScope(pat) {
		t.Error("HasScope misreads the scopes")
	}
}
