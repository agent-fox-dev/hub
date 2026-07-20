package workspace

import (
	"net/http"
	"testing"
)

// TS-01-20: Verify that a PAT with workspaces:read grants read-only access
// to the PAT owner's workspaces only.
// Requirement: 01-REQ-4.4
func TestWorkspaceScoping_PATReadOnlyOwn(t *testing.T) {
	env := newTestEnv(t)

	// Alice's workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-id",
		Status:  "active",
	})
	// Bob's workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "bob-ws",
		GitURL:  "https://github.com/org/bob-repo",
		OwnerID: "bob-id",
		Status:  "active",
	})

	auth := patAuth("alice-id", "workspaces:read")

	t.Run("list returns only alice workspaces", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/workspaces status = %d; want %d",
				rec.Code, http.StatusOK)
		}
		workspaces := parseWorkspaceListJSON(t, rec)
		for _, ws := range workspaces {
			if ws.OwnerID != "alice-id" {
				t.Errorf("returned workspace %q has owner_id %q; want %q",
					ws.Slug, ws.OwnerID, "alice-id")
			}
		}
	})

	t.Run("get bob workspace returns 404", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/bob-ws", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/v1/workspaces/bob-ws status = %d; want %d",
				rec.Code, http.StatusNotFound)
		}
	})
}

// TS-01-21: Verify that a PAT with workspaces:create can create workspaces
// on behalf of the owner and read the owner's own workspaces.
// Requirement: 01-REQ-4.5
func TestWorkspaceScoping_PATCreateOwnership(t *testing.T) {
	env := newTestEnv(t)

	auth := patAuth("alice-id", "workspaces:create")

	// Create workspace via PAT.
	body := `{"slug":"created-via-pat","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusCreated)
	}
	ws := parseWorkspaceJSON(t, rec)
	if ws.OwnerID != "alice-id" {
		t.Errorf("created workspace owner_id = %q; want %q",
			ws.OwnerID, "alice-id")
	}

	// Verify the created workspace appears in list.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusOK)
	}
	workspaces := parseWorkspaceListJSON(t, rec)
	found := false
	for _, w := range workspaces {
		if w.Slug == "created-via-pat" {
			found = true
			break
		}
	}
	if !found {
		t.Error("created workspace 'created-via-pat' not found in list")
	}
}

// TS-01-22: Verify that when a workspace exists but the requesting credential
// cannot access it, the API returns 404 (not 403) to prevent slug enumeration.
// Requirement: 01-REQ-4.6
func TestWorkspaceScoping_SlugEnumeration(t *testing.T) {
	env := newTestEnv(t)

	// Bob owns a workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "bob-secret-ws",
		GitURL:  "https://github.com/org/secret-repo",
		OwnerID: "bob-id",
		Status:  "active",
	})

	// Alice tries to access Bob's workspace via user API key.
	auth := userAuth("alice-id")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/bob-secret-ws", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/workspaces/bob-secret-ws status = %d; want %d (must be 404, not 403)",
			rec.Code, http.StatusNotFound)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}
