package gitserver

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"
)

// TS-06-9: Git server initialization registers git:read and git:write as
// named apikit.Permission scopes.
// Requirement: 06-REQ-3.1
func TestGitPermissions_ReadScope(t *testing.T) {
	perms := GitPermissions()
	expected := apikit.Permission{Resource: "git", Action: "read"}
	found := false
	for _, p := range perms {
		if p == expected {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("git:read permission not found in GitPermissions(); got %v", perms)
	}
}

// TS-06-9 (continued): Verify git:write is registered.
// Requirement: 06-REQ-3.1
func TestGitPermissions_WriteScope(t *testing.T) {
	perms := GitPermissions()
	expected := apikit.Permission{Resource: "git", Action: "write"}
	found := false
	for _, p := range perms {
		if p == expected {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("git:write permission not found in GitPermissions(); got %v", perms)
	}
}

// TS-06-10: A PAT with only git:write scope is permitted to perform both
// fetch and push operations (git:write implies git:read).
// Requirement: 06-REQ-3.2
func TestGitPermissions_WriteImpliesRead(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	// A PAT with only git:write should be permitted for fetch (upload-pack).
	fetchRec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_pat_writeonly"))

	if fetchRec.Code != http.StatusOK {
		t.Errorf("GET info/refs?service=git-upload-pack with git:write PAT: status = %d; want %d",
			fetchRec.Code, http.StatusOK)
	}

	// A PAT with only git:write should also be permitted for push (receive-pack).
	pushRec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-receive-pack", "",
		withBasicAuth("x-token-auth", "af_pat_writeonly"))

	if pushRec.Code != http.StatusOK {
		t.Errorf("GET info/refs?service=git-receive-pack with git:write PAT: status = %d; want %d",
			pushRec.Code, http.StatusOK)
	}
}

// TS-06-11: A PAT with git:read scope but no workspaces:read scope can still
// perform git fetch operations (scopes are independent).
// Requirement: 06-REQ-3.3
func TestGitPermissions_IndependentFromWorkspaceScopes(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	// A PAT with only git:read (no workspaces:read) should succeed for fetch.
	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_pat_gitonly"))

	if rec.Code != http.StatusOK {
		t.Errorf("GET info/refs with git:read-only PAT (no workspaces:read): status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}

// TestGitPermissions_ReadOnlyPAT_PushRejected verifies that a PAT with only
// git:read scope is rejected when attempting a push (receive-pack).
// Requirement: 06-REQ-3.E1
func TestGitPermissions_ReadOnlyPAT_PushRejected(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	// A PAT with only git:read should be rejected for push.
	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-receive-pack", "",
		withBasicAuth("x-token-auth", "af_pat_readonly"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET info/refs?service=git-receive-pack with git:read PAT: status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
}

// TestGitPermissions_NeitherScope_Rejected verifies that a PAT with neither
// git:read nor git:write scope is rejected for any git operation.
// Requirement: 06-REQ-3.E2
func TestGitPermissions_NeitherScope_Rejected(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	// A PAT with neither git:read nor git:write should be rejected.
	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_pat_noscopes"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET info/refs with no git scopes: status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
}
