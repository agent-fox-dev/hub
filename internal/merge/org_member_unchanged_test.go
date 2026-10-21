package merge

import (
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

type mRoute struct{ method, path, body string }

func (r mRoute) on(slug string) string { return strings.Replace(r.path, "%s", slug, 1) }

var mergeRoutes = []mRoute{
	{http.MethodGet, "/api/v1/workspaces/%s/merges", ""},
	{http.MethodGet, "/api/v1/workspaces/%s/merges/m-1", ""},
	{http.MethodPost, "/api/v1/workspaces/%s/merges", `{"target_branch":"main","source_ref":"feature"}`},
	{http.MethodDelete, "/api/v1/workspaces/%s/merges/m-1", ""},
	{http.MethodPost, "/api/v1/workspaces/%s/merges/m-1/requeue", ""},
	{http.MethodPost, "/api/v1/workspaces/%s/rebase", `{"target_ref":"main","branches":["feature"]}`},
}

// TS-35-52 (merge): A member's merge on an archived workspace is 403; the
// owner's is today's 400.
// Requirement: 35-REQ-10.1
func TestMemberMergeOnArchived_TS_35_52(t *testing.T) {
	env, _ := newMemberMergeEnv(t)
	if _, err := env.db.Exec(`UPDATE workspaces SET status = 'archived' WHERE slug = 'mw'`); err != nil {
		t.Fatal(err)
	}
	body := `{"target_branch":"main","source_ref":"feature"}`
	if rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/mw/merges", body, mergeUserAuth(mMember)); !isForbidden(rec) {
		t.Errorf("member merge: %d %s; want 403 forbidden", rec.Code, rec.Body)
	}
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/mw/merges", body, mergeUserAuth(mOwner))
	if rec.Code != http.StatusBadRequest || parseMergeErrorEnvelope(t, rec).Error.Message != "workspace is not active" {
		t.Errorf("owner merge: %d %s; want today's 400", rec.Code, rec.Body)
	}
}

// TS-35-53 (merge): A member's PAT without the merge scopes gets today's 403
// without error_type, the same for an existing and an unknown slug.
// Requirement: 35-REQ-10.2
func TestMemberMergeScopeAnswers_TS_35_53(t *testing.T) {
	env, _ := newMemberMergeEnv(t)
	for _, r := range mergeRoutes {
		pat := mergePATAuth(mMember, "tokens:read")
		w := env.doRequest(t, r.method, r.on("mw"), r.body, pat)
		ghost := env.doRequest(t, r.method, r.on("ghost"), r.body, pat)
		if w.Code != http.StatusForbidden || w.Body.String() != ghost.Body.String() || strings.Contains(w.Body.String(), "error_type") {
			t.Errorf("%s %s: mw %d %s, ghost %d %s; want the same 403 without error_type", r.method, r.path, w.Code, w.Body, ghost.Code, ghost.Body)
		}
	}
}

// TS-35-54 (merge): A stranger gets the unknown slug's 404 on every merge
// route.
// Requirement: 35-REQ-10.3
func TestStrangerMergeAnswers_TS_35_54(t *testing.T) {
	env, _ := newMemberMergeEnv(t)
	for _, auth := range []*apikit.AuthInfo{mergeUserAuth(mStranger), mergePATAuth(mStranger, mergeScopes...)} {
		for _, r := range mergeRoutes {
			w := env.doRequest(t, r.method, r.on("mw"), r.body, auth)
			ghost := env.doRequest(t, r.method, r.on("ghost"), r.body, auth)
			if w.Code != http.StatusNotFound || w.Body.String() != ghost.Body.String() || !strings.Contains(w.Body.String(), `"workspace not found"`) {
				t.Errorf("%s %s %s: mw %d %s, ghost %d %s; want the same 404", auth.CredentialType, r.method, r.path, w.Code, w.Body, ghost.Code, ghost.Body)
			}
		}
	}
}

// TS-35-52 (merge, clone not ready): a member's merge on a workspace whose
// clone is not ready is 403; the owner's is today's 400.
// Requirement: 35-REQ-10.1
func TestMemberMergeCloneNotReady_TS_35_52(t *testing.T) {
	env, _ := newMemberMergeEnv(t)
	if _, err := env.db.Exec(`UPDATE workspaces SET clone_status = 'pending' WHERE slug = 'mw'`); err != nil {
		t.Fatal(err)
	}
	body := `{"target_branch":"main","source_ref":"feature"}`
	if rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/mw/merges", body, mergeUserAuth(mMember)); !isForbidden(rec) {
		t.Errorf("member merge: %d %s; want 403 forbidden", rec.Code, rec.Body)
	}
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/mw/merges", body, mergeUserAuth(mOwner))
	if rec.Code != http.StatusBadRequest || parseMergeErrorEnvelope(t, rec).Error.Message != "workspace clone is not ready" {
		t.Errorf("owner merge: %d %s; want today's 400", rec.Code, rec.Body)
	}
}
