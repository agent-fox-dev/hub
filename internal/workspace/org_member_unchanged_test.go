package workspace

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// wsRoute is a workspace or patch route; %s is where the slug goes.
type wsRoute struct{ method, path, body string }

func (r wsRoute) on(slug string) string { return strings.Replace(r.path, "%s", slug, 1) }

// workspaceRoutes are every route of this spec served by the workspace package.
var workspaceRoutes = []wsRoute{
	{http.MethodGet, "/api/v1/workspaces/%s", ""},
	{http.MethodPatch, "/api/v1/workspaces/%s", `{"display_name":"x"}`},
	{http.MethodPost, "/api/v1/workspaces/%s/archive", ""},
	{http.MethodPost, "/api/v1/workspaces/%s/reactivate", ""},
	{http.MethodDelete, "/api/v1/workspaces/%s", ""},
	{http.MethodPost, "/api/v1/workspaces/%s/sync", ""},
	{http.MethodPost, "/api/v1/workspaces/%s/reclone", ""},
	{http.MethodGet, "/api/v1/workspaces/%s/patches", ""},
	{http.MethodGet, "/api/v1/workspaces/%s/patches/p1", ""},
	{http.MethodPost, "/api/v1/workspaces/%s/patches", `{"branch_name":"feature/c","skip_branch_check":true}`},
	{http.MethodPatch, "/api/v1/workspaces/%s/patches/p1", `{"description":"x"}`},
	{http.MethodDelete, "/api/v1/workspaces/%s/patches/p1", ""},
	{http.MethodPost, "/api/v1/workspaces/%s/patches/reorder", `{"patch_ids":["p2","p1"]}`},
	{http.MethodPost, "/api/v1/workspaces/%s/patches/p1/restore", ""},
	{http.MethodPost, "/api/v1/workspaces/%s/patches/p1/reset-to-origin", ""},
}

// TS-35-52 (workspace): A member's PATCH of an archived workspace is 403, the
// owner's is today's 400, and an invalid body is still the 400 that precedes
// the access step.
// Requirement: 35-REQ-10.1
func TestMemberWriteOnArchived_TS_35_52(t *testing.T) {
	env := newMemberEnv(t)
	if _, err := env.db.Exec(`UPDATE workspaces SET status = 'archived' WHERE slug = 'w'`); err != nil {
		t.Fatal(err)
	}
	assertForbidden(t, "member PATCH", env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/w", `{"display_name":"x"}`, userAuth(specMember)))
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/w", `{"display_name":"x"}`, userAuth(specOwner))
	if rec.Code != http.StatusBadRequest || parseErrorEnvelope(t, rec).Error.Message != "workspace is archived and must be reactivated before updating" {
		t.Errorf("owner PATCH: %d %s; want today's 400", rec.Code, rec.Body)
	}
	rec = env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/w", `{"slug":"x"}`, userAuth(specMember))
	if rec.Code != http.StatusBadRequest || parseErrorEnvelope(t, rec).Error.Message != "slug is immutable and cannot be updated" {
		t.Errorf("member invalid PATCH: %d %s; want the body-validation 400", rec.Code, rec.Body)
	}
}

// TS-35-53 (workspace): A member's PAT without a route's scope gets today's
// scope answer, the same for an existing and an unknown slug: 404 on the
// workspace CRUD routes, 403 without error_type elsewhere.
// Requirement: 35-REQ-10.2
func TestMemberScopeAnswers_TS_35_53(t *testing.T) {
	env := newMemberEnv(t)
	for _, tc := range []struct {
		r     wsRoute
		scope string
		want  int
	}{
		{workspaceRoutes[0], "tokens:read", http.StatusNotFound},
		{workspaceRoutes[1], "workspaces:read", http.StatusNotFound},
		{workspaceRoutes[2], "workspaces:read", http.StatusNotFound},
		{workspaceRoutes[3], "workspaces:read", http.StatusNotFound},
		{workspaceRoutes[4], "workspaces:read", http.StatusNotFound},
		{workspaceRoutes[5], "workspaces:read", http.StatusForbidden},
		{workspaceRoutes[6], "workspaces:read", http.StatusForbidden},
		{workspaceRoutes[7], "workspaces:read", http.StatusForbidden},
		{workspaceRoutes[8], "workspaces:read", http.StatusForbidden},
		{workspaceRoutes[9], "patches:read", http.StatusForbidden},
		{workspaceRoutes[10], "patches:read", http.StatusForbidden},
		{workspaceRoutes[11], "patches:read", http.StatusForbidden},
		{workspaceRoutes[12], "patches:read", http.StatusForbidden},
		{workspaceRoutes[13], "patches:read", http.StatusForbidden},
		{workspaceRoutes[14], "patches:read", http.StatusForbidden},
	} {
		pat := patAuth(specMember, tc.scope)
		w := env.doRequest(t, tc.r.method, tc.r.on("w"), tc.r.body, pat)
		ghost := env.doRequest(t, tc.r.method, tc.r.on("ghost"), tc.r.body, pat)
		if w.Code != tc.want || w.Body.String() != ghost.Body.String() || strings.Contains(w.Body.String(), "error_type") {
			t.Errorf("%s %s with %s: w %d %s, ghost %d %s; want %d, equal, no error_type", tc.r.method, tc.r.path, tc.scope, w.Code, w.Body, ghost.Code, ghost.Body, tc.want)
		}
	}
}

// TS-35-54 (workspace): A stranger gets 404 workspace not found on every
// workspace and patch route, the body it gets for an unknown slug.
// Requirement: 35-REQ-10.3
func TestStrangerAnswers_TS_35_54(t *testing.T) {
	env := newMemberEnv(t)
	for _, auth := range []*apikit.AuthInfo{userAuth(specStranger), patAuth(specStranger, allWorkspaceScopes...)} {
		for _, r := range workspaceRoutes {
			w := env.doRequest(t, r.method, r.on("w"), r.body, auth)
			ghost := env.doRequest(t, r.method, r.on("ghost"), r.body, auth)
			if w.Code != http.StatusNotFound || w.Body.String() != ghost.Body.String() || parseErrorEnvelope(t, w).Error.Message != "workspace not found" {
				t.Errorf("%s %s %s: w %d %s, ghost %d %s; want the same 404", auth.CredentialType, r.method, r.path, w.Code, w.Body, ghost.Code, ghost.Body)
			}
		}
	}
}

// TS-35-55 (workspace): The owner and an admin token of a personal-org
// workspace read and change it as before; their objects differ only in
// access. The existing ownership suites run unchanged in make test.
// Requirement: 35-REQ-10.4
func TestOwnerAndAdminUnchanged_TS_35_55(t *testing.T) {
	env := newTestEnv(t)
	env.seedWorkspace(t, &Workspace{Slug: "mine", GitURL: "https://github.com/a/mine", OwnerID: "alice-id",
		OrgID: strp("personal-org-alice-id"), Status: "ready"})
	owner := decodeObject(t, env.doRequest(t, http.MethodGet, "/api/v1/workspaces/mine", "", userAuth("alice-id")))
	admin := decodeObject(t, env.doRequest(t, http.MethodGet, "/api/v1/workspaces/mine", "", adminAuth()))
	if owner["access"] != "owner" || admin["access"] != "admin" || owner["org_slug"] != "personal-alice-id" {
		t.Errorf("access owner %v, admin %v, org_slug %v", owner["access"], admin["access"], owner["org_slug"])
	}
	delete(owner, "access")
	delete(admin, "access")
	for k, v := range owner {
		if !reflect.DeepEqual(admin[k], v) {
			t.Errorf("%s: owner %v, admin %v", k, v, admin[k])
		}
	}
	for _, auth := range []*apikit.AuthInfo{userAuth("alice-id"), adminAuth()} {
		if rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/mine", `{"display_name":"Mine"}`, auth); rec.Code != http.StatusOK {
			t.Errorf("%s PATCH: %d %s; want 200", auth.CredentialType, rec.Code, rec.Body)
		}
	}
	if rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/mine", "", userAuth("u2-id")); rec.Code != http.StatusNotFound {
		t.Errorf("another user: %d; want 404", rec.Code)
	}
}
