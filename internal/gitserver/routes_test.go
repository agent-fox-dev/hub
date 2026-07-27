package gitserver

import (
	"net/http"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// TS-06-1: MountHandlers registers all four git smart HTTP routes on the Echo
// instance at startup.
// Requirement: 06-REQ-1.1
func TestMountGitHandlers_RegistersRoutes(t *testing.T) {
	env := newGitTestEnv(t)

	routes := env.echo.Routes()

	// Collect registered route patterns keyed by method+path.
	type routeKey struct {
		method string
		path   string
	}
	registered := make(map[routeKey]bool)
	for _, r := range routes {
		registered[routeKey{r.Method, r.Path}] = true
	}

	expectedRoutes := []routeKey{
		{http.MethodGet, "/git/:org/:slug.git/info/refs"},
		{http.MethodPost, "/git/:org/:slug.git/git-upload-pack"},
		{http.MethodPost, "/git/:org/:slug.git/git-receive-pack"},
	}

	for _, exp := range expectedRoutes {
		if !registered[exp] {
			t.Errorf("expected route %s %s not registered; registered routes: %v",
				exp.method, exp.path, routeList(routes))
		}
	}
}

// TestMountGitHandlers_NoMatchWithoutDotGitSuffix verifies that requests
// without the .git suffix in the path do not match any git route.
// Requirement: 06-REQ-1.E2
func TestMountGitHandlers_NoMatchWithoutDotGitSuffix(t *testing.T) {
	env := newGitTestEnv(t)

	// A request without .git suffix should fall through to Echo's default 404.
	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_pat_test123"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /git/myorg/myws/info/refs (no .git) status = %d; want %d",
			rec.Code, http.StatusNotFound)
	}
}

// TS-06-2: GET info/refs with service=git-upload-pack returns HTTP 200 with
// the correct Content-Type and a pkt-line ref advertisement body.
// Requirement: 06-REQ-1.2
func TestInfoRefs_UploadPack_ContentType(t *testing.T) {
	env := newGitTestEnv(t)

	// Seed the org, workspace, and a local git repository.
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_pat_test123"))

	if rec.Code != http.StatusOK {
		t.Errorf("GET info/refs?service=git-upload-pack status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	expected := "application/x-git-upload-pack-advertisement"
	if ct != expected {
		t.Errorf("Content-Type = %q; want %q", ct, expected)
	}

	// The pkt-line response must start with the service announcement line.
	body := rec.Body.String()
	if !strings.Contains(body, "# service=git-upload-pack") {
		t.Errorf("response body does not contain service announcement; got %q", truncate(body, 200))
	}

	// The body must contain a flush packet (0000).
	if !strings.Contains(body, "0000") {
		t.Error("response body does not contain flush packet (0000)")
	}
}

// TS-06-3: GET info/refs with service=git-receive-pack returns HTTP 200 with
// the correct Content-Type and a pkt-line ref advertisement body.
// Requirement: 06-REQ-1.3
func TestInfoRefs_ReceivePack_ContentType(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodGet,
		"/git/myorg/myws.git/info/refs?service=git-receive-pack", "",
		withBasicAuth("x-token-auth", "af_pat_test123"))

	if rec.Code != http.StatusOK {
		t.Errorf("GET info/refs?service=git-receive-pack status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	expected := "application/x-git-receive-pack-advertisement"
	if ct != expected {
		t.Errorf("Content-Type = %q; want %q", ct, expected)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "# service=git-receive-pack") {
		t.Errorf("response body does not contain service announcement; got %q", truncate(body, 200))
	}

	if !strings.Contains(body, "0000") {
		t.Error("response body does not contain flush packet (0000)")
	}
}

// TS-06-4: POST to git-upload-pack returns HTTP 200 with Content-Type
// application/x-git-upload-pack-result and a streaming pack response.
// Requirement: 06-REQ-1.4
func TestUploadPack_ContentType(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	// Build a minimal upload-pack request body.
	// In a real scenario this would contain want/have lines and a flush packet.
	// For the stub test, we just verify the endpoint exists and returns the
	// correct content type. The full protocol test requires a real git repo.
	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-upload-pack", "",
		withBasicAuth("x-token-auth", "af_pat_test123"))

	if rec.Code != http.StatusOK {
		t.Errorf("POST git-upload-pack status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	expected := "application/x-git-upload-pack-result"
	if ct != expected {
		t.Errorf("Content-Type = %q; want %q", ct, expected)
	}
}

// TS-06-5: POST to git-receive-pack returns HTTP 200 with Content-Type
// application/x-git-receive-pack-result and a streaming pack response.
// Requirement: 06-REQ-1.5
func TestReceivePack_ContentType(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	rec := env.doRequest(t, http.MethodPost,
		"/git/myorg/myws.git/git-receive-pack", "",
		withBasicAuth("x-token-auth", "af_pat_test123"))

	if rec.Code != http.StatusOK {
		t.Errorf("POST git-receive-pack status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	ct := rec.Header().Get("Content-Type")
	expected := "application/x-git-receive-pack-result"
	if ct != expected {
		t.Errorf("Content-Type = %q; want %q", ct, expected)
	}
}

// TestInfoRefs_InvalidService verifies that an invalid or missing service
// query parameter returns HTTP 403 with a pkt-line error body.
// Requirement: 06-REQ-1.E1
func TestInfoRefs_InvalidService(t *testing.T) {
	env := newGitTestEnv(t)

	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.initWorkspaceRepo(t, "myws")

	tests := []struct {
		name    string
		service string
	}{
		{"missing service param", ""},
		{"unknown service", "?service=git-foo-bar"},
		{"empty service value", "?service="},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url := "/git/myorg/myws.git/info/refs" + tc.service
			rec := env.doRequest(t, http.MethodGet, url, "",
				withBasicAuth("x-token-auth", "af_pat_test123"))

			if rec.Code != http.StatusForbidden {
				t.Errorf("GET info/refs (%s) status = %d; want %d",
					tc.name, rec.Code, http.StatusForbidden)
			}
		})
	}
}

// routeList formats echo routes into a human-readable list for error messages.
func routeList(routes []*echo.Route) []string {
	var result []string
	for _, r := range routes {
		result = append(result, r.Method+" "+r.Path)
	}
	return result
}

// truncate shortens a string for error messages.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
