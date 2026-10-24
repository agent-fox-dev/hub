package carrypatch

import (
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

type cpRoute struct{ method, path, body, scope string }

func (r cpRoute) on(slug string) string { return strings.Replace(r.path, "%s", slug, 1) }

// carryPatchRoutes are the carry-patch routes of this spec with the scope
// each needs.
var carryPatchRoutes = []cpRoute{
	{http.MethodGet, "/api/v1/workspaces/%s/rebuilds", "", "rebuilds:read"},
	{http.MethodGet, "/api/v1/workspaces/%s/rebuilds/job-1", "", "rebuilds:read"},
	{http.MethodGet, "/api/v1/workspaces/%s/rebuild-preview", "", "rebuilds:read"},
	{http.MethodGet, "/api/v1/workspaces/%s/patch-status", "", "workspaces:read"},
	{http.MethodGet, "/api/v1/workspaces/%s/rerere", "", "workspaces:read"},
	{http.MethodPost, "/api/v1/workspaces/%s/rebuild", `{}`, "rebuilds:write"},
	{http.MethodDelete, "/api/v1/workspaces/%s/rebuilds/job-1", "", "rebuilds:write"},
	{http.MethodPost, "/api/v1/workspaces/%s/rebuilds/job-1/requeue", "", "rebuilds:write"},
	{http.MethodPost, "/api/v1/workspaces/%s/rebuilds/job-1/rollback", "", "rebuilds:write"},
	{http.MethodDelete, "/api/v1/workspaces/%s/rerere/h1", "", "workspaces:write"},
	{http.MethodPost, "/api/v1/workspaces/%s/sync", "", "workspaces:write"},
}

// TS-35-52 (carry-patch): A member's rebuild on an archived workspace is 403;
// the owner's is today's 400.
// Requirement: 35-REQ-10.1
func TestMemberRebuildOnArchived_TS_35_52(t *testing.T) {
	env := newMemberCarryPatchEnv(t)
	if _, err := env.db.Exec(`UPDATE workspaces SET status = 'archived' WHERE slug = 'cp'`); err != nil {
		t.Fatal(err)
	}
	forbiddenBody(t, "member rebuild", env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cp/rebuild", `{}`, rebuildUserAuth(cpMember)))
	if rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cp/rebuild", `{}`, rebuildUserAuth(cpOwner)); rec.Code != http.StatusBadRequest {
		t.Errorf("owner rebuild: %d %s; want today's 400", rec.Code, rec.Body)
	}
}

// TS-35-53 (carry-patch): A member's PAT without a route's scope gets today's
// 403 without error_type, the same for an existing and an unknown slug.
// Requirement: 35-REQ-10.2
func TestMemberCarryPatchScopeAnswers_TS_35_53(t *testing.T) {
	env := newMemberCarryPatchEnv(t)
	for _, r := range carryPatchRoutes {
		pat := rebuildPATAuth(cpMember, "tokens:read")
		w := env.doRequest(t, r.method, r.on("cp"), r.body, pat)
		ghost := env.doRequest(t, r.method, r.on("ghost"), r.body, pat)
		if w.Code != http.StatusForbidden || w.Body.String() != ghost.Body.String() || strings.Contains(w.Body.String(), "error_type") {
			t.Errorf("%s %s: cp %d %s, ghost %d %s; want the same 403 without error_type", r.method, r.path, w.Code, w.Body, ghost.Code, ghost.Body)
		}
	}
}

// TS-35-54 (carry-patch): A stranger gets the unknown slug's 404 on every
// carry-patch route.
// Requirement: 35-REQ-10.3
func TestStrangerCarryPatchAnswers_TS_35_54(t *testing.T) {
	env := newMemberCarryPatchEnv(t)
	for _, auth := range []*apikit.AuthInfo{rebuildUserAuth(cpStranger), rebuildPATAuth(cpStranger, carryPatchScopes...)} {
		for _, r := range carryPatchRoutes {
			w := env.doRequest(t, r.method, r.on("cp"), r.body, auth)
			ghost := env.doRequest(t, r.method, r.on("ghost"), r.body, auth)
			if w.Code != http.StatusNotFound || w.Body.String() != ghost.Body.String() || !strings.Contains(w.Body.String(), `"workspace not found"`) {
				t.Errorf("%s %s %s: cp %d %s, ghost %d %s; want the same 404", auth.CredentialType, r.method, r.path, w.Code, w.Body, ghost.Code, ghost.Body)
			}
		}
	}
}
