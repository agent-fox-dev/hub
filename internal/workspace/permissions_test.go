package workspace

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"
)

// TS-01-13: Verify that MountHandlers registers the workspaces:read permission
// scope (Permission{Resource: "workspaces", Action: "read"}) with apikit's
// permission registry.
// Requirement: 01-REQ-3.1
func TestWorkspacePermissions_ReadScope(t *testing.T) {
	perms := WorkspacePermissions()
	expected := apikit.Permission{Resource: "workspaces", Action: "read"}
	found := false
	for _, p := range perms {
		if p == expected {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("workspaces:read permission not found in WorkspacePermissions(); got %v", perms)
	}
}

// TS-01-14: Verify that MountHandlers registers the workspaces:create
// permission scope (Permission{Resource: "workspaces", Action: "create"})
// with apikit's permission registry.
// Requirement: 01-REQ-3.2
func TestWorkspacePermissions_CreateScope(t *testing.T) {
	perms := WorkspacePermissions()
	expected := apikit.Permission{Resource: "workspaces", Action: "create"}
	found := false
	for _, p := range perms {
		if p == expected {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("workspaces:create permission not found in WorkspacePermissions(); got %v", perms)
	}
}

// TS-01-15: Verify that a PAT with workspaces:read scope grants read-only
// access to list and view the PAT owner's workspaces.
// Requirement: 01-REQ-3.3
func TestWorkspacePermissions_PATReadAccess(t *testing.T) {
	env := newTestEnv(t)

	// Seed workspaces for alice.
	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws-1",
		GitURL:  "https://github.com/org/repo1",
		OwnerID: "alice-id",
		Status:  "active",
	})
	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws-2",
		GitURL:  "https://github.com/org/repo2",
		OwnerID: "alice-id",
		Status:  "active",
	})

	auth := patAuth("alice-id", "workspaces:read")

	// GET /api/v1/workspaces should return 200 with alice's workspaces.
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/workspaces status = %d; want %d", rec.Code, http.StatusOK)
	}
	workspaces := parseWorkspaceListJSON(t, rec)
	for _, ws := range workspaces {
		if ws.OwnerID != "alice-id" {
			t.Errorf("returned workspace %q has owner_id %q; want %q",
				ws.Slug, ws.OwnerID, "alice-id")
		}
	}

	// GET /api/v1/workspaces/alice-ws-1 should return 200.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces/alice-ws-1", "", auth)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/workspaces/alice-ws-1 status = %d; want %d",
			rec.Code, http.StatusOK)
	}
}

// TS-01-16: Verify that a PAT with workspaces:create scope can create
// workspaces and read the owner's own workspaces.
// Requirement: 01-REQ-3.4
func TestWorkspacePermissions_PATCreateAccess(t *testing.T) {
	env := newTestEnv(t)

	auth := patAuth("alice-id", "workspaces:create")

	// POST /api/v1/workspaces should return 201.
	body := `{"slug":"new-ws","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Errorf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusCreated)
	}

	// GET /api/v1/workspaces should return 200 with alice's workspaces.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusOK)
	}
	workspaces := parseWorkspaceListJSON(t, rec)
	found := false
	for _, ws := range workspaces {
		if ws.Slug == "new-ws" {
			found = true
			break
		}
	}
	if !found {
		t.Error("created workspace 'new-ws' not found in GET /api/v1/workspaces response")
	}
}
