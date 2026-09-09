package merge

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"
)

// Merge endpoints apply the workspace ownership rule: non-owners get 404,
// admin tokens pass.
func TestMergeEndpoints_RequireWorkspaceOwner(t *testing.T) {
	env := newMergeTestEnv(t, map[string]bool{"main": true, "feature/a": true})
	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	seedMergeJob(t, env.db, "job-1", "queued", "ws1", "main", "feature/a", "alice", nil, nil)

	bob := mergeUserAuth("bob")
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/workspaces/ws1/merges", `{"target_branch":"main","source_ref":"feature/a"}`},
		{http.MethodGet, "/api/v1/workspaces/ws1/merges", ""},
		{http.MethodGet, "/api/v1/workspaces/ws1/merges/job-1", ""},
		{http.MethodDelete, "/api/v1/workspaces/ws1/merges/job-1", ""},
		{http.MethodPost, "/api/v1/workspaces/ws1/merges/job-1/requeue", ""},
		{http.MethodPost, "/api/v1/workspaces/ws1/rebase", `{"target_ref":"main","branches":["feature/a"]}`},
	}
	for _, tc := range cases {
		rec := env.doRequest(t, tc.method, tc.path, tc.body, bob)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as non-owner: status = %d; want 404; body: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	if rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws1/merges", "", &apikit.AuthInfo{CredentialType: "admin_token"}); rec.Code != http.StatusOK {
		t.Errorf("GET merges as admin: status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// Ref names that git would parse as options are rejected before any git
// command runs.
func TestMergeEndpoints_RejectOptionLikeRefs(t *testing.T) {
	env := newMergeTestEnv(t, map[string]bool{"main": true, "--detach": true, "--exec=touch pwned": true})
	seedTestWorkspace(t, env.db, "ws1", "alice", "active", "ready")
	alice := mergeUserAuth("alice")

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/merges",
		`{"target_branch":"main","source_ref":"--detach"}`, alice)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST merges with option-like source_ref: status = %d; want 400; body: %s", rec.Code, rec.Body.String())
	}
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws1/rebase",
		`{"target_ref":"--exec=touch pwned","branches":["main"]}`, alice)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST rebase with option-like target_ref: status = %d; want 400; body: %s", rec.Code, rec.Body.String())
	}
}
