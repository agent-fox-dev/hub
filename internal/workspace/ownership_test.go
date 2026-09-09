package workspace

import (
	"net/http"
	"testing"
)

// Per-workspace endpoints outside the CRUD set (sync, reclone, patches) must
// apply the same ownership rule as CRUD: a non-owner gets 404, an admin token
// passes. These endpoints used to skip the check entirely.
func TestOwnership_SyncRecloneAndPatchesRequireOwner(t *testing.T) {
	env := newTestEnv(t)
	env.seedWorkspace(t, &Workspace{
		Slug: "owned-ws", GitURL: "https://github.com/org/repo", OwnerID: "alice",
		Status: "active", CloneStatus: "ready", WorkspaceMode: "carry_patch",
		UpstreamURL: strPtr("https://github.com/up/repo"), IntegrationBranch: strPtr("deploy"),
	})

	bob := userAuth("bob")
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/workspaces/owned-ws/sync", ""},
		{http.MethodPost, "/api/v1/workspaces/owned-ws/reclone", ""},
		{http.MethodGet, "/api/v1/workspaces/owned-ws/patches", ""},
		{http.MethodPost, "/api/v1/workspaces/owned-ws/patches", `{"branch_name":"feature/x","skip_branch_check":true}`},
		{http.MethodPatch, "/api/v1/workspaces/owned-ws/patches/p1", `{"status":"disabled"}`},
		{http.MethodDelete, "/api/v1/workspaces/owned-ws/patches/p1", ""},
		{http.MethodPost, "/api/v1/workspaces/owned-ws/patches/p1/restore", ""},
		{http.MethodPost, "/api/v1/workspaces/owned-ws/patches/reorder", `{"patch_ids":[]}`},
	}
	for _, tc := range cases {
		rec := env.doRequest(t, tc.method, tc.path, tc.body, bob)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as non-owner: status = %d; want 404; body: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}

	// The owner (and an admin) can list patches.
	if rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/owned-ws/patches", "", userAuth("alice")); rec.Code != http.StatusOK {
		t.Errorf("GET patches as owner: status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}
	if rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/owned-ws/patches", "", adminAuth()); rec.Code != http.StatusOK {
		t.Errorf("GET patches as admin: status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// Branch names that git would parse as options are rejected at the API.
func TestPatchAdd_RejectsOptionLikeBranchName(t *testing.T) {
	env := newTestEnv(t)
	env.seedWorkspace(t, &Workspace{
		Slug: "cp-ws", GitURL: "https://github.com/org/repo", OwnerID: "alice",
		Status: "active", CloneStatus: "ready", WorkspaceMode: "carry_patch",
		UpstreamURL: strPtr("https://github.com/up/repo"), IntegrationBranch: strPtr("deploy"),
	})
	for _, body := range []string{
		`{"branch_name":"--detach","skip_branch_check":true}`,
		`[{"branch_name":"ok/one","skip_branch_check":true},{"branch_name":"-x","skip_branch_check":true}]`,
	} {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cp-ws/patches", body, userAuth("alice"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST patches %s: status = %d; want 400; body: %s", body, rec.Code, rec.Body.String())
		}
	}
}

