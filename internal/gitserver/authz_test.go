package gitserver

import (
	"net/http"
	"testing"
)

// TS-06-16: An admin token is permitted to perform both fetch and push on
// any workspace regardless of ownership.
// Requirement: 06-REQ-5.1
func TestAuthz_AdminToken_FetchAndPush_AnyWorkspace(t *testing.T) {
	env := newGitTestEnv(t)

	// Workspace owned by user-1, NOT the admin.
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "otherws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "otherws")

	// Admin token should be permitted for fetch (upload-pack).
	fetchRec := env.doRequest(t, http.MethodGet,
		"/git/myorg/otherws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("admin", "af_admin_supertoken"))

	if fetchRec.Code != http.StatusOK {
		t.Errorf("admin fetch: status = %d; want %d", fetchRec.Code, http.StatusOK)
	}

	// Admin token should be permitted for push (receive-pack).
	pushRec := env.doRequest(t, http.MethodGet,
		"/git/myorg/otherws.git/info/refs?service=git-receive-pack", "",
		withBasicAuth("admin", "af_admin_supertoken"))

	if pushRec.Code != http.StatusOK {
		t.Errorf("admin push: status = %d; want %d", pushRec.Code, http.StatusOK)
	}
}

// TS-06-17: The workspace owner's API key is permitted to perform fetch/clone.
// Requirement: 06-REQ-5.2
func TestAuthz_OwnerAPIKey_Fetch(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("user", "af_key_userA"))

	if rec.Code != http.StatusOK {
		t.Errorf("owner API key fetch: status = %d; want %d", rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	expected := "application/x-git-upload-pack-advertisement"
	if ct != expected {
		t.Errorf("Content-Type = %q; want %q", ct, expected)
	}
}

// TS-06-18: The workspace owner's API key is permitted to perform push.
// Requirement: 06-REQ-5.3
func TestAuthz_OwnerAPIKey_Push(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-receive-pack", "",
		withBasicAuth("user", "af_key_userA"))

	if rec.Code != http.StatusOK {
		t.Errorf("owner API key push: status = %d; want %d", rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	expected := "application/x-git-receive-pack-advertisement"
	if ct != expected {
		t.Errorf("Content-Type = %q; want %q", ct, expected)
	}
}

// TS-06-19: A PAT with git:read scope belonging to the workspace owner is
// permitted to perform fetch/clone operations.
// Requirement: 06-REQ-5.4
func TestAuthz_PATGitRead_Fetch(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x", "af_pat_readpat"))

	if rec.Code != http.StatusOK {
		t.Errorf("PAT git:read fetch: status = %d; want %d", rec.Code, http.StatusOK)
	}
}

// TS-06-20: A PAT with git:write scope belonging to the workspace owner is
// permitted to perform push operations.
// Requirement: 06-REQ-5.5
func TestAuthz_PATGitWrite_Push(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-receive-pack", "",
		withBasicAuth("x", "af_pat_writepat"))

	if rec.Code != http.StatusOK {
		t.Errorf("PAT git:write push: status = %d; want %d", rec.Code, http.StatusOK)
	}
}

// TS-06-21: A push request from a PAT with only git:read scope is rejected
// with HTTP 403.
// Requirement: 06-REQ-5.6
func TestAuthz_PATGitReadOnly_PushRejected(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-receive-pack", "",
		withBasicAuth("x", "af_pat_readonly"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("PAT git:read push: status = %d; want %d", rec.Code, http.StatusForbidden)
	}
}

// TestAuthz_PATGitWrite_AlsoPermitsFetch verifies that git:write implies
// git:read access (correctness property 06-PROP-4).
// Requirement: 06-REQ-5.4, 06-REQ-5.5
func TestAuthz_PATGitWrite_AlsoPermitsFetch(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	// A PAT with git:write (no explicit git:read) should also be able to fetch.
	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x", "af_pat_writepat"))

	if rec.Code != http.StatusOK {
		t.Errorf("PAT git:write fetch: status = %d; want %d (write implies read)",
			rec.Code, http.StatusOK)
	}
}

// TestAuthz_PATWrongOwner_Returns404 verifies that a PAT with correct scopes
// but belonging to a non-owner, non-admin user returns 404 (anti-enumeration).
// Requirement: 06-REQ-5.E1
func TestAuthz_PATWrongOwner_Returns404(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedOrgMember(t, "org-1", "user-b")
	// Workspace owned by user-a.
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	// user-b's PAT with git:read should get 404, not 403 (anti-enumeration).
	readRec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x", "af_pat_userB_read"))

	if readRec.Code != http.StatusNotFound {
		t.Errorf("non-owner PAT git:read fetch: status = %d; want %d (anti-enumeration)",
			readRec.Code, http.StatusNotFound)
	}

	// The 404 must come from the authorization middleware, not Echo's router.
	assertPktLineErrorBody(t, readRec.Body.String(), "non-owner PAT fetch response")

	// user-b's PAT with git:write should also get 404.
	writeRec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-receive-pack", "",
		withBasicAuth("x", "af_pat_userB_write"))

	if writeRec.Code != http.StatusNotFound {
		t.Errorf("non-owner PAT git:write push: status = %d; want %d (anti-enumeration)",
			writeRec.Code, http.StatusNotFound)
	}

	assertPktLineErrorBody(t, writeRec.Body.String(), "non-owner PAT push response")
}

// TestAuthz_APIKeyWrongOwner_Returns404 verifies that a valid API key
// belonging to a non-owner, non-admin user returns 404 (anti-enumeration).
// Requirement: 06-REQ-5.E2
func TestAuthz_APIKeyWrongOwner_Returns404(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedOrgMember(t, "org-1", "user-b")
	// Workspace owned by user-a.
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	// user-b's API key should get 404, not 403.
	fetchRec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("user", "af_key_userB"))

	if fetchRec.Code != http.StatusNotFound {
		t.Errorf("non-owner API key fetch: status = %d; want %d (anti-enumeration)",
			fetchRec.Code, http.StatusNotFound)
	}

	// The 404 must come from the authorization middleware, not Echo's router.
	assertPktLineErrorBody(t, fetchRec.Body.String(), "non-owner API key fetch response")

	pushRec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-receive-pack", "",
		withBasicAuth("user", "af_key_userB"))

	if pushRec.Code != http.StatusNotFound {
		t.Errorf("non-owner API key push: status = %d; want %d (anti-enumeration)",
			pushRec.Code, http.StatusNotFound)
	}

	assertPktLineErrorBody(t, pushRec.Body.String(), "non-owner API key push response")
}

// TestAuthz_AdminToken_OverridesOwnership verifies that admin tokens bypass
// ownership checks entirely (correctness property 06-PROP-3 exception).
// Requirement: 06-REQ-5.1
func TestAuthz_AdminToken_OverridesOwnership(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	// Workspace owned by user-a. Admin is not the owner.
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	// Admin should be able to fetch even though not the owner.
	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("admin", "af_admin_supertoken"))

	if rec.Code != http.StatusOK {
		t.Errorf("admin fetch on non-owned workspace: status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}

// TestAuthz_PostUploadPack_OwnerPermitted verifies that the POST git-upload-pack
// endpoint also enforces the authorization matrix (not just info/refs).
// Requirement: 06-REQ-5.2
func TestAuthz_PostUploadPack_OwnerPermitted(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-upload-pack", "",
		withBasicAuth("user", "af_key_userA"))

	if rec.Code != http.StatusOK {
		t.Errorf("POST git-upload-pack with owner API key: status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}

// TestAuthz_PostReceivePack_OwnerPermitted verifies that the POST
// git-receive-pack endpoint enforces the authorization matrix.
// Requirement: 06-REQ-5.3
func TestAuthz_PostReceivePack_OwnerPermitted(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-a")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-a", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-receive-pack", "",
		withBasicAuth("user", "af_key_userA"))

	if rec.Code != http.StatusOK {
		t.Errorf("POST git-receive-pack with owner API key: status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}
