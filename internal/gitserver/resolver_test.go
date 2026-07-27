package gitserver

import (
	"net/http"
	"strings"
	"testing"
)

// TS-06-12: Repository resolver extracts org and slug from the URL, verifies
// org_id, verifies clone_status=ready, opens the repository via PlainOpen,
// and returns the storer to the transport.
// Requirement: 06-REQ-4.1
func TestResolver_HappyPath_ReadyWorkspace_ReturnsOK(t *testing.T) {
	env := newGitTestEnv(t)

	// Seed org and workspace with matching org_id and clone_status='ready'.
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	// clone_status defaults to 'ready' from schema.

	// Create a real git repository at the expected filesystem path.
	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusOK {
		t.Errorf("GET info/refs for ready workspace: status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	// The response should have the correct Content-Type for upload-pack advertisement.
	ct := rec.Header().Get("Content-Type")
	expected := "application/x-git-upload-pack-advertisement"
	if ct != expected {
		t.Errorf("Content-Type = %q; want %q", ct, expected)
	}
}

// TS-06-12 (continued): Verify that org and slug are correctly extracted
// from the URL path and used for the database lookup.
// Requirement: 06-REQ-4.1
func TestResolver_CorrectOrgAndSlugExtraction(t *testing.T) {
	env := newGitTestEnv(t)

	// Seed two orgs with workspaces — verify the correct one is resolved.
	env.seedOrg(t, "org-1", "Org One", "orgone")
	env.seedOrg(t, "org-2", "Org Two", "orgtwo")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedOrgMember(t, "org-2", "user-2")
	env.seedWorkspace(t, "ws-alpha", "https://github.com/org/alpha", "user-1", "org-1", "active")
	env.seedWorkspace(t, "ws-beta", "https://github.com/org/beta", "user-2", "org-2", "active")

	env.initWorkspaceRepo(t, "ws-alpha")

	// Request ws-alpha under orgone — should resolve to the correct workspace.
	rec := env.doRequest(t, http.MethodGet,
		"/git/orgone/ws-alpha.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusOK {
		t.Errorf("GET info/refs for ws-alpha under orgone: status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}

// TS-06-13: Repository resolver returns transport.ErrRepositoryNotFound and
// HTTP 404 when no workspace with the given slug exists.
// Requirement: 06-REQ-4.2
func TestResolver_WorkspaceNotFound_Returns404(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	// No workspace seeded — 'nonexistent' does not exist.

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/nonexistent.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET info/refs for nonexistent workspace: status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}

	// Per the spec, the body should contain a pkt-line error.
	body := rec.Body.String()
	if !strings.Contains(body, "ERR") && !strings.Contains(body, "not found") {
		t.Errorf("response body should contain a pkt-line error; got %q", truncate(body, 200))
	}
}

// TS-06-14: Repository resolver returns transport.ErrRepositoryNotFound and
// HTTP 404 when the org slug does not match the workspace's org_id.
// Requirement: 06-REQ-4.3
func TestResolver_OrgMismatch_Returns404(t *testing.T) {
	env := newGitTestEnv(t)

	// Workspace 'myws' belongs to org-1 (slug 'myorg'), but we request
	// it under org-2 (slug 'wrongorg').
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrg(t, "org-2", "Wrong Org", "wrongorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedOrgMember(t, "org-2", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/wrongorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET info/refs with org mismatch: status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}

	// The response must be a git pkt-line error, not Echo's default JSON 404.
	body := rec.Body.String()
	assertPktLineErrorBody(t, body, "org mismatch response")

	// Anti-enumeration: the error should NOT reveal whether the workspace
	// exists under a different org.
	if strings.Contains(body, "org mismatch") || strings.Contains(body, "wrong org") {
		t.Errorf("response body should not leak org mismatch details; got %q",
			truncate(body, 200))
	}
}

// TS-06-15: Repository resolver returns transport.ErrRepositoryNotFound and
// HTTP 404 when the workspace clone_status is not 'ready'.
// Requirement: 06-REQ-4.4
func TestResolver_CloneStatusNotReady_Returns404(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	// Override clone_status to 'cloning' (not ready).
	env.setCloneStatus(t, "myws", "cloning")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET info/refs with clone_status='cloning': status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}

	// The response should indicate the workspace is not servable.
	body := rec.Body.String()
	if !strings.Contains(body, "ERR") && !strings.Contains(body, "not") {
		t.Errorf("response body should contain a pkt-line error for non-ready clone; got %q",
			truncate(body, 200))
	}
}

// TestResolver_CloneStatusFailed_Returns404 verifies that a workspace with
// clone_status='failed' is also rejected with HTTP 404.
// Requirement: 06-REQ-4.4
func TestResolver_CloneStatusFailed_Returns404(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.setCloneStatus(t, "myws", "failed")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET info/refs with clone_status='failed': status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}

	assertPktLineErrorBody(t, rec.Body.String(), "clone_status=failed response")
}

// TestResolver_CloneStatusArchived_Returns404 verifies that a workspace with
// clone_status='archived' is rejected with HTTP 404.
// Requirement: 06-REQ-4.4
func TestResolver_CloneStatusArchived_Returns404(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.setCloneStatus(t, "myws", "archived")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET info/refs with clone_status='archived': status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}

	assertPktLineErrorBody(t, rec.Body.String(), "clone_status=archived response")
}

// TestResolver_PlainOpenFails_Returns500 verifies that when the repository
// filesystem path is missing or corrupted, the resolver returns HTTP 500.
// Requirement: 06-REQ-4.E1
func TestResolver_PlainOpenFails_Returns500(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	// Deliberately do NOT create a git repo at the expected path.
	// The resolver should call PlainOpen and get an error.

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("GET info/refs with missing repo dir: status = %d; want %d",
			rec.Code, http.StatusInternalServerError)
	}
}

// TestResolver_NonOwnerNonAdmin_Returns404 verifies that a valid credential
// belonging to a user who is not the workspace owner and not an admin
// receives HTTP 404 to prevent workspace enumeration.
// Requirement: 06-REQ-4.E2
func TestResolver_NonOwnerNonAdmin_Returns404(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedOrgMember(t, "org-1", "user-2")
	// Workspace owned by user-1, not user-2.
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	// user-2's API key should get 404, not 403.
	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_key_user2"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET info/refs as non-owner: status = %d; want %d (anti-enumeration)",
			rec.Code, http.StatusNotFound)
	}

	// Must NOT return 403 — that would reveal the workspace exists.
	if rec.Code == http.StatusForbidden {
		t.Error("non-owner access should return 404, not 403 (anti-enumeration)")
	}

	// The 404 must be a git pkt-line error, not Echo's default JSON.
	assertPktLineErrorBody(t, rec.Body.String(), "non-owner response")
}

// TestResolver_ReceivePack_HappyPath verifies the resolver works for
// git-receive-pack (push) requests with a ready workspace.
// Requirement: 06-REQ-4.1
func TestResolver_ReceivePack_HappyPath(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")

	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-receive-pack", "",
		withBasicAuth("x-token-auth", "af_key_user1"))

	if rec.Code != http.StatusOK {
		t.Errorf("GET info/refs?service=git-receive-pack for ready workspace: status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	expected := "application/x-git-receive-pack-advertisement"
	if ct != expected {
		t.Errorf("Content-Type = %q; want %q", ct, expected)
	}
}

// TestResolver_NotReady_AllEndpoints_Return404 verifies that ALL git endpoints
// reject requests for non-ready workspaces (correctness property 06-PROP-1).
// Requirement: 06-REQ-4.4
func TestResolver_NotReady_AllEndpoints_Return404(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.setCloneStatus(t, "myws", "cloning")

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-upload-pack"},
		{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-receive-pack"},
		{http.MethodPost, "/git/myorg/myws.git/git-upload-pack"},
		{http.MethodPost, "/git/myorg/myws.git/git-receive-pack"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			rec := env.doRequest(t, ep.method, ep.path, "",
				withBasicAuth("x-token-auth", "af_key_user1"))

			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s with clone_status='cloning': status = %d; want %d",
					ep.method, ep.path, rec.Code, http.StatusNotFound)
			}

			// Must be a pkt-line error, not Echo's default JSON 404.
			assertPktLineErrorBody(t, rec.Body.String(),
				ep.method+" "+ep.path+" non-ready response")
		})
	}
}
