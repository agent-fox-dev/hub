package wsaccess_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/workspace"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// TS-33-17: AuthorizeWorkspace denies a bound PAT for an owned workspace other
// than S and allows S, on a single-connection database.
// Requirements: 33-REQ-3.5, 33-REQ-2.4
func TestAuthorizeWorkspace_Binding_TS_33_17(t *testing.T) {
	db := openBindingDB(t)
	for _, slug := range []string{"S", "X"} {
		if _, err := db.Exec(`INSERT INTO workspaces (slug, owner_id) VALUES (?, 'u1')`, slug); err != nil {
			t.Fatal(err)
		}
	}
	bind(t, db, "tokB", "S")
	bound := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokB", Permissions: []string{"workspaces:read"}}
	unbound := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokU", Permissions: []string{"workspaces:read"}}

	access, code, msg := wsaccess.AuthorizeWorkspace(db, bound, "X", wsaccess.NeedPrivate)
	if access != nil || code != http.StatusNotFound || msg != "workspace not found" {
		t.Errorf("AuthorizeWorkspace(bound, X) = (%v, %d, %q); want (nil, 404, workspace not found)", access, code, msg)
	}

	got, code, msg := wsaccess.AuthorizeWorkspace(db, bound, "S", wsaccess.NeedPrivate)
	if got == nil || code != 0 || msg != "" {
		t.Fatalf("AuthorizeWorkspace(bound, S) = (%v, %d, %q); want access", got, code, msg)
	}
	want, _, _ := wsaccess.AuthorizeWorkspace(db, unbound, "S", wsaccess.NeedPrivate)
	if want == nil || *got != *want {
		t.Errorf("AuthorizeWorkspace(bound, S) = %+v; want the unbound PAT's %+v", got, want)
	}
	// The unbound PAT of the same user still reaches X.
	if a, code, _ := wsaccess.AuthorizeWorkspace(db, unbound, "X", wsaccess.NeedPrivate); a == nil || code != 0 {
		t.Errorf("AuthorizeWorkspace(unbound, X) = (%v, %d); want access", a, code)
	}

	// The existence check still answers first for an unknown slug.
	if a, code, msg := wsaccess.AuthorizeWorkspace(db, bound, "ghost", wsaccess.NeedPrivate); a != nil || code != http.StatusNotFound || msg != "workspace not found" {
		t.Errorf("AuthorizeWorkspace(bound, ghost) = (%v, %d, %q); want (nil, 404, workspace not found)", a, code, msg)
	}
}

// openAccessDB is openBindingDB, optionally with apikit's org_members table.
func openAccessDB(t *testing.T, members bool) *sql.DB {
	t.Helper()
	db := openBindingDB(t)
	if members {
		if _, err := db.Exec(`CREATE TABLE org_members (
			org_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (org_id, user_id)
		)`); err != nil {
			t.Fatalf("creating org_members: %v", err)
		}
	}
	return db
}

// addWorkspace inserts a workspace; org is nil for a NULL org_id.
func addWorkspace(t *testing.T, db *sql.DB, slug, owner string, org any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspaces (slug, owner_id, org_id) VALUES (?, ?, ?)`, slug, owner, org); err != nil {
		t.Fatalf("adding workspace %s: %v", slug, err)
	}
}

// addMember inserts the org_members row (org, user).
func addMember(t *testing.T, db *sql.DB, org, user string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO org_members (org_id, user_id) VALUES (?, ?)`, org, user); err != nil {
		t.Fatalf("adding member %s of %s: %v", user, org, err)
	}
}

func apiKeyOf(user string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "api_key", UserID: user}
}

func adminToken() *apikit.AuthInfo { return &apikit.AuthInfo{CredentialType: "admin_token"} }

// TS-35-1: Access and Need expose the specified constants, and
// workspace.AuthorizeWorkspace answers as wsaccess.AuthorizeWorkspace.
// Requirements: 35-REQ-1.1, 35-REQ-2.1
func TestAccessAndNeedTypes_TS_35_1(t *testing.T) {
	if wsaccess.AccessNone != "" || wsaccess.AccessMember != "member" || wsaccess.AccessOwner != "owner" || wsaccess.AccessAdmin != "admin" {
		t.Errorf("access constants = %q, %q, %q, %q; want \"\", member, owner, admin",
			wsaccess.AccessNone, wsaccess.AccessMember, wsaccess.AccessOwner, wsaccess.AccessAdmin)
	}
	needs := map[wsaccess.Need]bool{wsaccess.NeedRead: true, wsaccess.NeedWrite: true, wsaccess.NeedPrivate: true}
	if len(needs) != 3 {
		t.Errorf("NeedRead, NeedWrite, NeedPrivate are not three distinct values: %v", needs)
	}

	db := openAccessDB(t, true)
	addWorkspace(t, db, "w", "u1", "O")
	addMember(t, db, "O", "u2")
	for _, auth := range []*apikit.AuthInfo{nil, adminToken(), apiKeyOf("u1"), apiKeyOf("u2"), apiKeyOf("u3")} {
		for _, need := range []wsaccess.Need{wsaccess.NeedRead, wsaccess.NeedWrite, wsaccess.NeedPrivate} {
			for _, slug := range []string{"w", "ghost"} {
				wa, wc, wm := wsaccess.AuthorizeWorkspace(db, auth, slug, need)
				ga, gc, gm := workspace.AuthorizeWorkspace(db, auth, slug, need)
				if wc != gc || wm != gm || (wa == nil) != (ga == nil) || (wa != nil && *wa != *ga) {
					t.Errorf("workspace.AuthorizeWorkspace(%+v, %s, %s) = (%+v, %d, %q); want (%+v, %d, %q)",
						auth, slug, need, ga, gc, gm, wa, wc, wm)
				}
			}
		}
	}
	if a, code, msg := wsaccess.AuthorizeWorkspaceAt(db, apiKeyOf("u2"), "w", wsaccess.NeedRead, wsaccess.RefusalSite{}); a == nil || code != 0 || msg != "" || a.Access != wsaccess.AccessMember {
		t.Errorf("AuthorizeWorkspaceAt(member, read) = (%+v, %d, %q); want member access", a, code, msg)
	}
}

// TS-35-2: An admin token is AccessAdmin without reading org_members.
// Requirement: 35-REQ-1.2
func TestAuthorizeWorkspace_AdminWithoutMembers_TS_35_2(t *testing.T) {
	db := openAccessDB(t, false)
	addWorkspace(t, db, "w", "u1", "O")
	ws, code, msg := wsaccess.AuthorizeWorkspace(db, adminToken(), "w", wsaccess.NeedWrite)
	if ws == nil || code != 0 || msg != "" {
		t.Fatalf("AuthorizeWorkspace(admin) = (%v, %d, %q); want access", ws, code, msg)
	}
	if ws.Access != wsaccess.AccessAdmin {
		t.Errorf("Access = %q; want admin", ws.Access)
	}
}

// TS-35-3: The owner is AccessOwner without reading org_members.
// Requirement: 35-REQ-1.3
func TestAuthorizeWorkspace_OwnerWithoutMembers_TS_35_3(t *testing.T) {
	db := openAccessDB(t, false)
	addWorkspace(t, db, "w", "u1", "O")
	ws, code, msg := wsaccess.AuthorizeWorkspace(db, apiKeyOf("u1"), "w", wsaccess.NeedWrite)
	if ws == nil || code != 0 || msg != "" {
		t.Fatalf("AuthorizeWorkspace(owner) = (%v, %d, %q); want access", ws, code, msg)
	}
	if ws.Access != wsaccess.AccessOwner {
		t.Errorf("Access = %q; want owner", ws.Access)
	}
}

// TS-35-4: A user with an org_members row for the workspace's org is a
// member, whatever the org's status or kind.
// Requirement: 35-REQ-1.4
func TestAuthorizeWorkspace_Member_TS_35_4(t *testing.T) {
	db := openAccessDB(t, true)
	if _, err := db.Exec(`CREATE TABLE orgs (id TEXT PRIMARY KEY, slug TEXT, owner_id TEXT, status TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO orgs VALUES ('O', 'o', NULL, 'active'), ('P', 'personal-u9', 'u9', 'suspended')`); err != nil {
		t.Fatal(err)
	}
	addWorkspace(t, db, "w", "u1", "O")
	addWorkspace(t, db, "p", "u9", "P")
	addMember(t, db, "O", "u2")
	addMember(t, db, "P", "u3")
	for _, tc := range []struct{ user, slug string }{{"u2", "w"}, {"u3", "p"}} {
		ws, code, msg := wsaccess.AuthorizeWorkspace(db, apiKeyOf(tc.user), tc.slug, wsaccess.NeedRead)
		if ws == nil || code != 0 || msg != "" {
			t.Errorf("AuthorizeWorkspace(%s, %s) = (%v, %d, %q); want access", tc.user, tc.slug, ws, code, msg)
			continue
		}
		if ws.Access != wsaccess.AccessMember {
			t.Errorf("AuthorizeWorkspace(%s, %s).Access = %q; want member", tc.user, tc.slug, ws.Access)
		}
	}
}

// TS-35-5: A NULL or empty org_id, or an empty UserID, gives AccessNone and
// 404 without an org_members read (there is no org_members table here).
// Requirements: 35-REQ-1.5, 35-REQ-2.3
func TestAuthorizeWorkspace_NoMembershipRead_TS_35_5(t *testing.T) {
	db := openAccessDB(t, false)
	addWorkspace(t, db, "w1", "u1", nil)
	addWorkspace(t, db, "w2", "u1", "")
	addWorkspace(t, db, "w3", "u1", "O")
	adminRoleKey := &apikit.AuthInfo{CredentialType: "api_key", Role: "admin"}
	emptyPAT := &apikit.AuthInfo{CredentialType: "pat", Permissions: []string{"workspaces:read"}}
	for _, tc := range []struct {
		auth *apikit.AuthInfo
		slug string
	}{
		{apiKeyOf("u2"), "w1"}, {apiKeyOf("u2"), "w2"}, {adminRoleKey, "w3"}, {emptyPAT, "w3"},
	} {
		ws, code, msg := wsaccess.AuthorizeWorkspace(db, tc.auth, tc.slug, wsaccess.NeedRead)
		if ws != nil || code != http.StatusNotFound || msg != "workspace not found" {
			t.Errorf("AuthorizeWorkspace(%+v, %s) = (%v, %d, %q); want (nil, 404, workspace not found)", tc.auth, tc.slug, ws, code, msg)
		}
	}
}

// TS-35-6: Deleting the membership row takes effect on the next call.
// Requirement: 35-REQ-1.6
func TestAuthorizeWorkspace_MembershipRemoved_TS_35_6(t *testing.T) {
	db := openAccessDB(t, true)
	addWorkspace(t, db, "w", "u1", "O")
	addMember(t, db, "O", "u2")
	if ws, code, _ := wsaccess.AuthorizeWorkspace(db, apiKeyOf("u2"), "w", wsaccess.NeedRead); ws == nil || code != 0 || ws.Access != wsaccess.AccessMember {
		t.Fatalf("AuthorizeWorkspace(member) = (%+v, %d); want member access", ws, code)
	}
	if _, err := db.Exec(`DELETE FROM org_members WHERE org_id = 'O' AND user_id = 'u2'`); err != nil {
		t.Fatal(err)
	}
	if ws, code, msg := wsaccess.AuthorizeWorkspace(db, apiKeyOf("u2"), "w", wsaccess.NeedRead); ws != nil || code != http.StatusNotFound || msg != "workspace not found" {
		t.Errorf("AuthorizeWorkspace after removal = (%v, %d, %q); want (nil, 404, workspace not found)", ws, code, msg)
	}
}

// TS-35-7: Access is the first matching rule in the order admin, owner,
// member, none, over every combination of credential type, UserID, owner,
// org_id and membership rows; AccessNone is 404.
// Requirement: 35-REQ-1.7
func TestAuthorizeWorkspace_AccessOrder_TS_35_7(t *testing.T) {
	db := openAccessDB(t, true)
	orgs := map[string]any{"wn": nil, "we": "", "wo": "O", "wp": "P", "wq": "Q"}
	owners := map[string]string{"wn": "u1", "we": "u1", "wo": "u1", "wp": "u2", "wq": "u3"}
	for slug, org := range orgs {
		addWorkspace(t, db, slug, owners[slug], org)
	}
	members := map[[2]string]bool{{"O", "u2"}: true, {"O", "u1"}: true, {"P", "u3"}: true, {"P", "u2"}: true, {"", "u4"}: true}
	for m := range members {
		addMember(t, db, m[0], m[1])
	}
	model := func(auth *apikit.AuthInfo, slug string) wsaccess.Access {
		org, _ := orgs[slug].(string)
		switch {
		case auth.CredentialType == "admin_token":
			return wsaccess.AccessAdmin
		case auth.UserID != "" && auth.UserID == owners[slug]:
			return wsaccess.AccessOwner
		case auth.UserID != "" && org != "" && members[[2]string{org, auth.UserID}]:
			return wsaccess.AccessMember
		}
		return wsaccess.AccessNone
	}
	for _, typ := range []string{"admin_token", "api_key", "pat"} {
		for _, user := range []string{"", "u1", "u2", "u3", "u4", "u5"} {
			auth := &apikit.AuthInfo{CredentialType: typ, UserID: user}
			for slug := range orgs {
				ws, code, msg := wsaccess.AuthorizeWorkspace(db, auth, slug, wsaccess.NeedRead)
				got := wsaccess.AccessNone
				if ws != nil {
					got = ws.Access
				}
				want := model(auth, slug)
				if got != want {
					t.Errorf("%s %q on %s: access %q; want %q", typ, user, slug, got, want)
				}
				if want == wsaccess.AccessNone && (ws != nil || code != http.StatusNotFound || msg != "workspace not found") {
					t.Errorf("%s %q on %s: (%v, %d, %q); want (nil, 404, workspace not found)", typ, user, slug, ws, code, msg)
				}
			}
		}
	}
}

// TS-35-8: A nil auth is refused with 401 without a database query.
// Requirement: 35-REQ-2.2
func TestAuthorizeWorkspace_NilAuthNoQuery_TS_35_8(t *testing.T) {
	db := openAccessDB(t, true)
	db.Close()
	ws, code, msg := wsaccess.AuthorizeWorkspace(db, nil, "w", wsaccess.NeedRead)
	if ws != nil || code != http.StatusUnauthorized || msg != "authentication required" {
		t.Errorf("AuthorizeWorkspace(nil) = (%v, %d, %q); want (nil, 401, authentication required)", ws, code, msg)
	}
}

// TS-35-9: An unknown slug and a stranger get 404 and trigger no bound-refusal
// report, even when the stranger is a bound PAT.
// Requirement: 35-REQ-2.3
func TestAuthorizeWorkspace_StrangerNoReport_TS_35_9(t *testing.T) {
	db := openAccessDB(t, true)
	addWorkspace(t, db, "w", "u9", "O")
	addWorkspace(t, db, "w2", "u1", "O")
	bind(t, db, "tokW", "w")
	var calls atomic.Int32
	wsaccess.SetRefusalObserver(func(wsaccess.BoundRefusal) { calls.Add(1) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })
	stranger := &apikit.AuthInfo{CredentialType: "pat", UserID: "u9", TokenID: "tokW", Permissions: []string{"workspaces:read"}}

	for _, slug := range []string{"ghost", "w2"} {
		ws, code, msg := wsaccess.AuthorizeWorkspace(db, stranger, slug, wsaccess.NeedRead)
		if ws != nil || code != http.StatusNotFound || msg != "workspace not found" {
			t.Errorf("AuthorizeWorkspace(%s) = (%v, %d, %q); want (nil, 404, workspace not found)", slug, ws, code, msg)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("observer calls = %d; want 0", n)
	}
}

// recordReports registers a refusal observer for the test and returns the
// reports it receives.
func recordReports(t *testing.T) *[]wsaccess.BoundRefusal {
	t.Helper()
	var got []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { got = append(got, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })
	return &got
}

var allNeeds = []wsaccess.Need{wsaccess.NeedRead, wsaccess.NeedWrite, wsaccess.NeedPrivate}

// TS-35-10: A PAT bound to another workspace is refused with one report
// carrying the site, whatever its access and need.
// Requirement: 35-REQ-2.4
func TestAuthorizeWorkspace_BoundRefusalSite_TS_35_10(t *testing.T) {
	db := openAccessDB(t, true)
	addWorkspace(t, db, "A", "u1", "OA")
	addWorkspace(t, db, "B", "u1", "OB")
	addMember(t, db, "OB", "u2")
	bind(t, db, "tok1", "A")
	bind(t, db, "tok2", "A")
	got := recordReports(t)
	site := wsaccess.RefusalSite{Method: "GET", Route: "/workspaces/:slug", Status: http.StatusNotFound, Surface: wsaccess.SurfaceAPI}

	for _, user := range []string{"u1", "u2"} {
		tok := map[string]string{"u1": "tok1", "u2": "tok2"}[user]
		pat := &apikit.AuthInfo{CredentialType: "pat", UserID: user, TokenID: tok, Permissions: []string{"workspaces:read"}}
		for _, need := range allNeeds {
			for _, at := range []bool{false, true} {
				*got = nil
				var ws *wsaccess.WorkspaceAccess
				var code int
				var msg string
				want := wsaccess.BoundRefusal{TokenID: tok, UserID: user, BoundSlug: "A", RequestedSlug: "B", Status: http.StatusNotFound, Surface: wsaccess.SurfaceAPI}
				if at {
					ws, code, msg = wsaccess.AuthorizeWorkspaceAt(db, pat, "B", need, site)
					want.Method, want.Route = "GET", "/workspaces/:slug"
				} else {
					ws, code, msg = wsaccess.AuthorizeWorkspace(db, pat, "B", need)
				}
				if ws != nil || code != http.StatusNotFound || msg != "workspace not found" {
					t.Errorf("%s %s at=%v: (%v, %d, %q); want (nil, 404, workspace not found)", user, need, at, ws, code, msg)
				}
				if len(*got) != 1 || (*got)[0] != want {
					t.Errorf("%s %s at=%v: reports %+v; want exactly %+v", user, need, at, *got, want)
				}
			}
		}
	}
}

// TS-35-11: A member needing write gets 403 and needing private gets 404,
// after the binding check.
// Requirement: 35-REQ-2.5
func TestAuthorizeWorkspace_MemberNeeds_TS_35_11(t *testing.T) {
	db := openAccessDB(t, true)
	addWorkspace(t, db, "w", "u1", "O")
	addWorkspace(t, db, "other", "u2", "P")
	addMember(t, db, "O", "u2")
	bind(t, db, "tokO", "other")
	got := recordReports(t)
	member := apiKeyOf("u2")

	if ws, code, msg := wsaccess.AuthorizeWorkspace(db, member, "w", wsaccess.NeedWrite); ws != nil || code != http.StatusForbidden || msg != "org members have read-only access to this workspace" {
		t.Errorf("member NeedWrite = (%v, %d, %q); want (nil, 403, org members have read-only access to this workspace)", ws, code, msg)
	}
	if ws, code, msg := wsaccess.AuthorizeWorkspace(db, member, "w", wsaccess.NeedPrivate); ws != nil || code != http.StatusNotFound || msg != "workspace not found" {
		t.Errorf("member NeedPrivate = (%v, %d, %q); want (nil, 404, workspace not found)", ws, code, msg)
	}
	if ws, code, _ := wsaccess.AuthorizeWorkspace(db, member, "w", wsaccess.NeedRead); ws == nil || code != 0 || ws.Access != wsaccess.AccessMember {
		t.Errorf("member NeedRead = (%+v, %d); want member access", ws, code)
	}
	if len(*got) != 0 {
		t.Errorf("unbound member caused reports %+v; want none", *got)
	}

	bound := &apikit.AuthInfo{CredentialType: "pat", UserID: "u2", TokenID: "tokO", Permissions: []string{"workspaces:write"}}
	if ws, code, msg := wsaccess.AuthorizeWorkspace(db, bound, "w", wsaccess.NeedWrite); ws != nil || code != http.StatusNotFound || msg != "workspace not found" {
		t.Errorf("bound member NeedWrite = (%v, %d, %q); want (nil, 404, workspace not found)", ws, code, msg)
	}
	if len(*got) != 1 {
		t.Errorf("bound member caused %d reports; want 1", len(*got))
	}
}

// TS-35-12: A satisfied need returns the full WorkspaceAccess, with OrgID
// empty for a NULL column.
// Requirement: 35-REQ-2.6
func TestAuthorizeWorkspace_FullRecord_TS_35_12(t *testing.T) {
	db := openAccessDB(t, true)
	if _, err := db.Exec(`INSERT INTO workspaces (slug, owner_id, org_id, status, clone_status) VALUES
		('w1', 'u1', 'O', 'archived', 'failed'), ('w2', 'u1', NULL, 'active', 'ready')`); err != nil {
		t.Fatal(err)
	}
	for slug, want := range map[string]wsaccess.WorkspaceAccess{
		"w1": {Slug: "w1", OwnerID: "u1", OrgID: "O", Status: "archived", CloneStatus: "failed", Access: wsaccess.AccessOwner},
		"w2": {Slug: "w2", OwnerID: "u1", OrgID: "", Status: "active", CloneStatus: "ready", Access: wsaccess.AccessOwner},
	} {
		ws, code, msg := wsaccess.AuthorizeWorkspace(db, apiKeyOf("u1"), slug, wsaccess.NeedWrite)
		if ws == nil || code != 0 || msg != "" {
			t.Errorf("AuthorizeWorkspace(%s) = (%v, %d, %q); want access", slug, ws, code, msg)
			continue
		}
		if *ws != want {
			t.Errorf("AuthorizeWorkspace(%s) = %+v; want %+v", slug, *ws, want)
		}
	}
}

// TS-35-13: A database error reading the workspace, the membership or the
// binding is 500 and never a grant.
// Requirement: 35-REQ-2.7
func TestAuthorizeWorkspace_DatabaseErrors_TS_35_13(t *testing.T) {
	check := func(name string, db *sql.DB, auth *apikit.AuthInfo) {
		t.Helper()
		if ws, code, msg := wsaccess.AuthorizeWorkspace(db, auth, "w", wsaccess.NeedRead); ws != nil || code != http.StatusInternalServerError || msg != "internal server error" {
			t.Errorf("%s: (%v, %d, %q); want (nil, 500, internal server error)", name, ws, code, msg)
		}
	}

	db := openAccessDB(t, true)
	if _, err := db.Exec(`DROP TABLE workspaces`); err != nil {
		t.Fatal(err)
	}
	check("no workspaces table", db, apiKeyOf("u1"))

	db = openAccessDB(t, false)
	addWorkspace(t, db, "w", "u1", "O")
	check("no org_members table", db, apiKeyOf("u2"))

	db = openAccessDB(t, true)
	addWorkspace(t, db, "w", "u1", "O")
	if _, err := db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	check("no binding table", db, &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tok", Permissions: []string{"workspaces:read"}})
}

// TS-35-14: WriteRefusal writes the apikit envelope, with error_type
// forbidden only on 403.
// Requirement: 35-REQ-2.8
func TestWriteRefusal_TS_35_14(t *testing.T) {
	if wsaccess.ErrorTypeForbidden != "forbidden" {
		t.Errorf("ErrorTypeForbidden = %q; want forbidden", wsaccess.ErrorTypeForbidden)
	}
	for _, tc := range []struct {
		status        int
		msg, wantType string
	}{
		{http.StatusForbidden, "org members have read-only access to this workspace", "forbidden"},
		{http.StatusNotFound, "workspace not found", ""},
		{http.StatusInternalServerError, "internal server error", ""},
	} {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		if err := wsaccess.WriteRefusal(c, tc.status, tc.msg); err != nil {
			t.Fatalf("WriteRefusal(%d) = %v", tc.status, err)
		}
		if rec.Code != tc.status {
			t.Errorf("WriteRefusal(%d) status = %d", tc.status, rec.Code)
		}
		var body struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("WriteRefusal(%d) body %q: %v", tc.status, rec.Body, err)
		}
		if body.Error["message"] != tc.msg || body.Error["code"] != float64(tc.status) {
			t.Errorf("WriteRefusal(%d) body = %s; want code %d and message %q", tc.status, rec.Body, tc.status, tc.msg)
		}
		et, has := body.Error["error_type"]
		if tc.wantType != "" && et != tc.wantType {
			t.Errorf("WriteRefusal(%d) error_type = %v; want %q", tc.status, et, tc.wantType)
		}
		if tc.wantType == "" && has {
			t.Errorf("WriteRefusal(%d) has error_type %v; want none", tc.status, et)
		}
	}
}
