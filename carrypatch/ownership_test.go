package carrypatch

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"
)

// Carry-patch endpoints apply the workspace ownership rule: non-owners get
// 404, admin tokens pass.
func TestCarryPatchEndpoints_RequireWorkspaceOwner(t *testing.T) {
	env := newRebuildTestEnv(t)
	seedWorkspace(t, env.db, "ws1", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-1", "ws1", "feature/a", 1, PatchStatusActive)

	bob := rebuildUserAuth("bob")
	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/workspaces/ws1/rebuild"},
		{http.MethodGet, "/api/v1/workspaces/ws1/rebuilds"},
		{http.MethodGet, "/api/v1/workspaces/ws1/rebuilds/some-id"},
		{http.MethodDelete, "/api/v1/workspaces/ws1/rebuilds/some-id"},
		{http.MethodPost, "/api/v1/workspaces/ws1/rebuilds/some-id/requeue"},
	}
	for _, tc := range cases {
		rec := env.doRequest(t, tc.method, tc.path, "", bob)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as non-owner: status = %d; want 404; body: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws1/rebuilds", "", &adminAuthInfo)
	if rec.Code != http.StatusOK {
		t.Errorf("GET rebuilds as admin: status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}
}

var adminAuthInfo = apikit.AuthInfo{CredentialType: "admin_token"}
