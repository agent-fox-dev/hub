package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// Spec 35 fixtures: org "org-o" (slug "acme") has members owner-1 and
// member-2; stranger-3 belongs to no org. Workspace "w" is owner-1's
// carry-patch workspace in org-o, with two patches.
const (
	specOwner    = "owner-1"
	specMember   = "member-2"
	specStranger = "stranger-3"
	specOrg      = "org-o"
)

// allWorkspaceScopes are the PAT scopes every workspace and patch route needs.
var allWorkspaceScopes = []string{"workspaces:read", "workspaces:write", "workspaces:write", "workspaces:write", "patches:read", "patches:write"}

func strp(s string) *string { return &s }

// newMemberEnv is newTestEnv with the spec 35 org, its members and workspace w.
func newMemberEnv(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	env.seedOrg(t, specOrg, "Org O", "acme")
	env.seedOrgMember(t, specOrg, specOwner)
	env.seedOrgMember(t, specOrg, specMember)
	env.seedWorkspace(t, &Workspace{
		Slug: "w", GitURL: "https://github.com/fork/repo.git", OwnerID: specOwner, OrgID: strp(specOrg),
		Status: "active", CloneStatus: "ready", WorkspaceMode: "carry_patch",
		UpstreamURL: strp("https://github.com/upstream/repo.git"), IntegrationBranch: strp("deploy"),
	})
	seedPatchRaw(t, env.db, "p1", "w", "feature/a", 1)
	seedPatchRaw(t, env.db, "p2", "w", "feature/b", 2)
	return env
}

// decodeObject decodes a JSON object response body.
func decodeObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body, err)
	}
	return body
}

// assertForbidden checks a member's 403 forbidden refusal.
func assertForbidden(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Errorf("%s: status %d; want 403. body: %s", name, rec.Code, rec.Body)
		return
	}
	body := rec.Body.Bytes()
	var raw struct {
		Error struct {
			Message   string `json:"message"`
			ErrorType string `json:"error_type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.Error.ErrorType != wsaccess.ErrorTypeForbidden ||
		raw.Error.Message != "org members have read-only access to this workspace" {
		t.Errorf("%s: body %s; want error_type forbidden and the read-only message", name, body)
	}
}

// dumpTable renders every row of table in a stable order.
func dumpTable(t *testing.T, db *sql.DB, table, order string) string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY ` + order)
	if err != nil {
		t.Fatalf("reading %s: %v", table, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var b strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%v\n", vals)
	}
	return b.String()
}

// TS-35-28: Every workspace response carries access and org_slug: create,
// get, list, update, archive, reactivate, sync with carry-patch extras and
// reclone.
// Requirement: 35-REQ-5.1
func TestWorkspaceResponses_CarryAccessAndOrgSlug_TS_35_28(t *testing.T) {
	env := newMemberEnv(t)
	root := t.TempDir()
	oldRoot, oldPush, oldHead := defaultWorkspaceRoot, archiveOpenAndPushFn, archiveHeadFn
	defaultWorkspaceRoot = root
	archiveOpenAndPushFn = func(_, _ string, _ transport.AuthMethod) error { return nil }
	archiveHeadFn = func(string) (string, error) { return "abcdef1234567890abcdef1234567890abcdef12", nil }
	t.Cleanup(func() { defaultWorkspaceRoot, archiveOpenAndPushFn, archiveHeadFn = oldRoot, oldPush, oldHead })
	registerSyncHook(t, func(_ echo.Context, _, _ string) (map[string]any, bool, error) {
		return map[string]any{"patches_merged": []string{}, "rebuild_triggered": false}, true, nil
	})
	owner := userAuth(specOwner)
	hasKeys := func(name string, obj map[string]any) {
		t.Helper()
		_, a := obj["access"]
		_, o := obj["org_slug"]
		if !a || !o {
			t.Errorf("%s: object %v lacks access or org_slug", name, obj)
		}
	}
	step := func(name, method, path, body string, want int) map[string]any {
		t.Helper()
		rec := env.doRequest(t, method, path, body, owner)
		if rec.Code != want {
			t.Fatalf("%s: status %d; want %d. body: %s", name, rec.Code, want, rec.Body)
		}
		obj := decodeObject(t, rec)
		hasKeys(name, obj)
		return obj
	}

	step("create", http.MethodPost, "/api/v1/workspaces", `{"slug":"made","git_url":"https://github.com/example/repo.git","org_id":"org-o"}`, http.StatusCreated)
	step("get", http.MethodGet, "/api/v1/workspaces/made", "", http.StatusOK)
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", owner)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d", rec.Code)
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) == 0 {
		t.Fatalf("list: %v, %s", err, rec.Body)
	}
	for _, obj := range list {
		hasKeys("list", obj)
	}
	step("update", http.MethodPatch, "/api/v1/workspaces/made", `{"display_name":"Made"}`, http.StatusOK)
	step("archive", http.MethodPost, "/api/v1/workspaces/made/archive", "", http.StatusOK)
	step("reactivate", http.MethodPost, "/api/v1/workspaces/made/reactivate", "", http.StatusOK)
	extras := step("sync", http.MethodPost, "/api/v1/workspaces/w/sync", "", http.StatusOK)
	if _, ok := extras["rebuild_triggered"]; !ok {
		t.Errorf("sync: carry-patch extras missing from %v", extras)
	}
	step("reclone", http.MethodPost, "/api/v1/workspaces/w/reclone", "", http.StatusOK)
}

// TS-35-29: access describes the caller, not the PAT's scopes, and the create
// response says owner.
// Requirement: 35-REQ-5.2
func TestWorkspaceAccess_DescribesCaller_TS_35_29(t *testing.T) {
	env := newMemberEnv(t)
	for _, tc := range []struct {
		name string
		auth *apikit.AuthInfo
		want string
	}{
		{"owner PAT with workspaces:read only", patAuth(specOwner, "workspaces:read"), "owner"},
		{"admin token", adminAuth(), "admin"},
		{"member", userAuth(specMember), "member"},
	} {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/w", "", tc.auth)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d; want 200. body: %s", tc.name, rec.Code, rec.Body)
			continue
		}
		if got := decodeObject(t, rec)["access"]; got != tc.want {
			t.Errorf("%s: access %v; want %q", tc.name, got, tc.want)
		}
	}
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", `{"slug":"made","git_url":"https://github.com/example/repo.git","org_id":"org-o"}`, userAuth(specOwner))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d. body: %s", rec.Code, rec.Body)
	}
	if got := decodeObject(t, rec)["access"]; got != "owner" {
		t.Errorf("create: access %v; want owner", got)
	}
}

// TS-35-30: org_slug is the org's slug, or null when org_id is NULL or names
// no org row.
// Requirement: 35-REQ-5.3
func TestWorkspaceOrgSlug_TS_35_30(t *testing.T) {
	env := newMemberEnv(t)
	env.seedWorkspace(t, &Workspace{Slug: "w2", GitURL: "https://github.com/a/b", OwnerID: specOwner, Status: "active"})
	env.seedWorkspace(t, &Workspace{Slug: "w3", GitURL: "https://github.com/a/c", OwnerID: specOwner, OrgID: strp("no-such-org"), Status: "active"})
	for slug, want := range map[string]any{"w": "acme", "w2": nil, "w3": nil} {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug, "", userAuth(specOwner))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d", slug, rec.Code)
			continue
		}
		obj := decodeObject(t, rec)
		got, present := obj["org_slug"]
		if !present || got != want {
			t.Errorf("GET %s: org_slug %v (present %v); want %v", slug, got, present, want)
		}
	}
}

// TS-35-31: A member sees the same keys and values as the owner, apart from
// access, with hub_url present, and no credential value.
// Requirement: 35-REQ-5.4
func TestWorkspaceObject_MemberSeesOwnersView_TS_35_31(t *testing.T) {
	env := newMemberEnv(t)
	oldURL := defaultExternalURL
	defaultExternalURL = "https://hub.example.com"
	t.Cleanup(func() { defaultExternalURL = oldURL })
	oldValidate := validateCredentialsFn
	validateCredentialsFn = func(context.Context, string, transport.AuthMethod) error { return nil }
	t.Cleanup(func() { validateCredentialsFn = oldValidate })

	const secret = "ghp_spec35secretvalue"
	for i, body := range []string{
		`{"slug":"var-one","git_url":"https://github.com/acme/v1","org_id":"org-o","git_pat":"` + secret + `"}`,
		`{"slug":"var-two","git_url":"https://github.com/acme/v2","org_id":"org-o","git_username":"bot","git_password":"` + secret + `"}`,
		`{"slug":"var-three","git_url":"https://github.com/acme/v3","org_id":"org-o","workspace_mode":"carry_patch","upstream_url":"https://github.com/up/v3"}`,
		`{"slug":"var-four","git_url":"https://github.com/acme/v4","org_id":"org-o","sync_mode":"disabled","description":"d"}`,
	} {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, userAuth(specOwner))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create variant %d: status %d. body: %s", i, rec.Code, rec.Body)
		}
	}
	for _, slug := range []string{"w", "var-one", "var-two", "var-three", "var-four"} {
		o := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug, "", userAuth(specOwner))
		m := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug, "", userAuth(specMember))
		if o.Code != http.StatusOK || m.Code != http.StatusOK {
			t.Errorf("%s: owner %d, member %d; want 200, 200", slug, o.Code, m.Code)
			continue
		}
		if strings.Contains(m.Body.String(), secret) || strings.Contains(o.Body.String(), secret) {
			t.Errorf("%s: a response contains the credential value", slug)
		}
		ob, mb := decodeObject(t, o), decodeObject(t, m)
		if ob["hub_url"] == nil || mb["hub_url"] == nil {
			t.Errorf("%s: hub_url owner %v, member %v; want both set", slug, ob["hub_url"], mb["hub_url"])
		}
		if ob["access"] != "owner" || mb["access"] != "member" {
			t.Errorf("%s: access owner %v, member %v", slug, ob["access"], mb["access"])
		}
		delete(ob, "access")
		delete(mb, "access")
		ok, mk := slices.Sorted(mapsKeys(ob)), slices.Sorted(mapsKeys(mb))
		if !slices.Equal(ok, mk) {
			t.Errorf("%s: keys owner %v, member %v", slug, ok, mk)
		}
		for k, v := range ob {
			if fmt.Sprint(mb[k]) != fmt.Sprint(v) {
				t.Errorf("%s: %s owner %v, member %v", slug, k, v, mb[k])
			}
		}
	}
}

func mapsKeys(m map[string]any) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// TS-35-32: lookupWorkspaceForAuth returns AuthorizeWorkspaceAt's decision,
// and 404 when the row vanishes between the decision and the full load.
// Requirements: 35-REQ-6.1, 35-REQ-6.4
func TestLookupWorkspaceForAuth_Decision_TS_35_32(t *testing.T) {
	env := newMemberEnv(t)
	call := func(user string) (*Workspace, wsaccess.Access, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/w", nil), rec)
		c.SetPath("/api/v1/workspaces/:slug")
		ws, access := lookupWorkspaceForAuth(c, env.db, "w", userAuth(user), wsaccess.NeedWrite)
		return ws, access, rec
	}

	if ws, _, rec := call(specMember); ws != nil {
		t.Errorf("member NeedWrite granted")
	} else {
		assertForbidden(t, "member", rec)
	}
	if ws, _, rec := call(specStranger); ws != nil || rec.Code != http.StatusNotFound || parseErrorEnvelope(t, rec).Error.Message != "workspace not found" {
		t.Errorf("stranger: %v, %d %s; want nil, 404 workspace not found", ws, rec.Code, rec.Body)
	}
	if ws, access, rec := call(specOwner); ws == nil || ws.Slug != "w" || access != wsaccess.AccessOwner || rec.Body.Len() != 0 {
		t.Errorf("owner: %v, %q, %s; want workspace w, owner, nothing written", ws, access, rec.Body)
	}

	afterAccessDecision = func(slug string) { env.deleteWorkspaceBySlug(t, slug) }
	t.Cleanup(func() { afterAccessDecision = nil })
	if ws, _, rec := call(specOwner); ws != nil || rec.Code != http.StatusNotFound || parseErrorEnvelope(t, rec).Error.Message != "workspace not found" {
		t.Errorf("vanished row: %v, %d %s; want nil, 404 workspace not found", ws, rec.Code, rec.Body)
	}
}

// TS-35-33: A member reads the workspace, the patch list and a patch, with an
// API key and with a PAT holding the read scopes.
// Requirement: 35-REQ-6.2
func TestMemberReads_TS_35_33(t *testing.T) {
	env := newMemberEnv(t)
	for _, auth := range []*apikit.AuthInfo{userAuth(specMember), patAuth(specMember, "workspaces:read", "patches:read")} {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/w", "", auth)
		if rec.Code != http.StatusOK || decodeObject(t, rec)["access"] != "member" {
			t.Errorf("%s GET workspace: %d %s; want 200 with access member", auth.CredentialType, rec.Code, rec.Body)
		}
		for _, path := range []string{"/api/v1/workspaces/w/patches", "/api/v1/workspaces/w/patches/p1"} {
			if rec := env.doRequest(t, http.MethodGet, path, "", auth); rec.Code != http.StatusOK {
				t.Errorf("%s GET %s: %d %s; want 200", auth.CredentialType, path, rec.Code, rec.Body)
			}
		}
	}
}

// TS-35-34: A member's workspace, sync, reclone and patch writes are refused
// with 403 forbidden and change no row and no job queue entry.
// Requirement: 35-REQ-6.3
func TestMemberWritesRefused_TS_35_34(t *testing.T) {
	env := newMemberEnv(t)
	q := &JobQueue{jobs: make(chan CloneJob, 10)}
	oldQueue := defaultQueue
	defaultQueue = q
	t.Cleanup(func() { defaultQueue = oldQueue })
	before := dumpTable(t, env.db, "workspaces", "slug") + dumpTable(t, env.db, "patches", "id")

	writes := []struct{ method, path, body string }{
		{http.MethodPatch, "/api/v1/workspaces/w", `{"display_name":"x"}`},
		{http.MethodPost, "/api/v1/workspaces/w/archive", ""},
		{http.MethodPost, "/api/v1/workspaces/w/reactivate", ""},
		{http.MethodDelete, "/api/v1/workspaces/w", ""},
		{http.MethodPost, "/api/v1/workspaces/w/sync", ""},
		{http.MethodPost, "/api/v1/workspaces/w/reclone", ""},
		{http.MethodPost, "/api/v1/workspaces/w/patches", `{"branch_name":"feature/c","skip_branch_check":true}`},
		{http.MethodPatch, "/api/v1/workspaces/w/patches/p1", `{"description":"x"}`},
		{http.MethodDelete, "/api/v1/workspaces/w/patches/p1", ""},
		{http.MethodPost, "/api/v1/workspaces/w/patches/reorder", `{"patch_ids":["p2","p1"]}`},
		{http.MethodPost, "/api/v1/workspaces/w/patches/p1/restore", ""},
		{http.MethodPost, "/api/v1/workspaces/w/patches/p1/reset-to-origin", ""},
	}
	for _, auth := range []*apikit.AuthInfo{userAuth(specMember), patAuth(specMember, allWorkspaceScopes...)} {
		for _, w := range writes {
			rec := env.doRequest(t, w.method, w.path, w.body, auth)
			assertForbidden(t, auth.CredentialType+" "+w.method+" "+w.path, rec)
		}
	}
	if after := dumpTable(t, env.db, "workspaces", "slug") + dumpTable(t, env.db, "patches", "id"); after != before {
		t.Errorf("rows changed:\nbefore %s\nafter %s", before, after)
	}
	if n := len(q.jobs); n != 0 {
		t.Errorf("clone queue has %d jobs; want 0", n)
	}
}

// TS-35-35: A PAT bound to another workspace gets 404 and one refusal report
// with method and route, even for a member: through lookupWorkspaceForAuth
// and through the binding guard of the real server.
// Requirements: 35-REQ-6.5, 35-REQ-10.5
func TestBoundMemberRefused_TS_35_35(t *testing.T) {
	var reports []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { reports = append(reports, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })

	// lookupWorkspaceForAuth, without the guard.
	env := newMemberEnv(t)
	env.seedWorkspace(t, &Workspace{Slug: "a", GitURL: "https://github.com/a/a", OwnerID: specMember, Status: "active"})
	bindToken(t, env.db, "tokA", "a")
	bound := &apikit.AuthInfo{CredentialType: "pat", UserID: specMember, TokenID: "tokA", Permissions: []string{"workspaces:read"}}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/w", nil), rec)
	c.SetPath("/api/v1/workspaces/:slug")
	if ws, _ := lookupWorkspaceForAuth(c, env.db, "w", bound, wsaccess.NeedRead); ws != nil || rec.Code != http.StatusNotFound ||
		parseErrorEnvelope(t, rec).Error.Message != "workspace not found" {
		t.Errorf("lookupWorkspaceForAuth: %v, %d %s; want nil, 404 workspace not found", ws, rec.Code, rec.Body)
	}
	want := wsaccess.BoundRefusal{TokenID: "tokA", UserID: specMember, BoundSlug: "a", RequestedSlug: "w", Method: "GET",
		Route: "/api/v1/workspaces/:slug", Status: http.StatusNotFound, Surface: wsaccess.SurfaceAPI}
	if len(reports) != 1 || reports[0] != want {
		t.Errorf("lookupWorkspaceForAuth reports %+v; want exactly %+v", reports, want)
	}

	// The real server's binding guard.
	reports = nil
	h := newRealHub(t)
	db := h.db.SqlDB
	now := apikit.NowUTC()
	for _, stmt := range []string{
		`INSERT INTO users (id, username, email, full_name, role, status, provider, provider_id, created_at, updated_at) VALUES ('u2', 'u2', 'u2@example.com', '', 'user', 'active', 'github', 'p2', '` + now + `', '` + now + `')`,
		`INSERT INTO orgs (id, name, slug, status, created_at, updated_at) VALUES ('ob', 'OB', 'ob', 'active', '` + now + `', '` + now + `')`,
		`INSERT INTO org_members (org_id, user_id, created_at) VALUES ('ob', 'u2', '` + now + `')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := insertWorkspace(db, &Workspace{Slug: "b", GitURL: "https://github.com/a/b", OwnerID: "u1", OrgID: strp("ob"), Status: "active"}); err != nil {
		t.Fatal(err)
	}
	h.seed(t, "a2", "u2", "active")
	key, err := apikit.GenerateAPIKey(db, "u2", 0, echo.New().Logger)
	if err != nil {
		t.Fatal(err)
	}
	if rec := h.do(http.MethodGet, "/api/v1/workspaces/b", key.FullKey, ""); rec.Code != http.StatusOK {
		t.Fatalf("member API key GET b: %d %s; want 200", rec.Code, rec.Body)
	}
	mint := h.do(http.MethodPost, "/api/v1/user/tokens", key.FullKey, `{"name":"t","permissions":["workspaces:read"]}`)
	var created struct {
		Token   string `json:"token"`
		TokenID string `json:"token_id"`
	}
	if err := json.Unmarshal(mint.Body.Bytes(), &created); err != nil || created.Token == "" {
		t.Fatalf("minting u2's PAT: %d %s", mint.Code, mint.Body)
	}
	bindToken(t, db, created.TokenID, "a2")
	rec = h.do(http.MethodGet, "/api/v1/workspaces/b", created.Token, "")
	if rec.Code != http.StatusNotFound || parseErrorEnvelope(t, rec).Error.Message != "workspace not found" {
		t.Errorf("bound member PAT GET b: %d %s; want 404 workspace not found", rec.Code, rec.Body)
	}
	want = wsaccess.BoundRefusal{TokenID: created.TokenID, UserID: "u2", BoundSlug: "a2", RequestedSlug: "b", Method: "GET",
		Route: "/api/v1/workspaces/:slug", Status: http.StatusNotFound, Surface: wsaccess.SurfaceAPI}
	if len(reports) != 1 || reports[0] != want {
		t.Errorf("guard reports %+v; want exactly %+v", reports, want)
	}
}
