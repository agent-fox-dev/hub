package workspace

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TS-01-32: Verify that GET /api/v1/workspaces with admin token returns all
// workspaces across all users, excluding archived by default, ordered by
// created_at descending.
// Requirement: 01-REQ-6.1
func TestWorkspaceList_AdminAllUsers(t *testing.T) {
	env := newTestEnv(t)

	// Seed workspaces for two different users.
	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/alice-repo",
		OwnerID: "alice-id",
		Status:  "active",
	})
	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-archived",
		GitURL:  "https://github.com/org/alice-repo2",
		OwnerID: "alice-id",
		Status:  "archived",
	})
	env.seedWorkspace(t, &Workspace{
		Slug:    "bob-ws",
		GitURL:  "https://github.com/org/bob-repo",
		OwnerID: "bob-id",
		Status:  "active",
	})

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	workspaces := parseWorkspaceListJSON(t, rec)
	slugs := make(map[string]bool)
	for _, ws := range workspaces {
		slugs[ws.Slug] = true
	}

	if !slugs["alice-ws"] {
		t.Error("missing alice-ws in admin listing")
	}
	if !slugs["bob-ws"] {
		t.Error("missing bob-ws in admin listing")
	}
	if slugs["alice-archived"] {
		t.Error("alice-archived (archived) should NOT appear in default listing")
	}

	// Verify ordering by created_at descending.
	for i := 1; i < len(workspaces); i++ {
		prev, errP := time.Parse(time.RFC3339, workspaces[i-1].CreatedAt)
		curr, errC := time.Parse(time.RFC3339, workspaces[i].CreatedAt)
		if errP != nil || errC != nil {
			t.Errorf("invalid RFC 3339 timestamp at index %d", i)
			continue
		}
		if prev.Before(curr) {
			t.Errorf("workspaces not ordered by created_at desc: index %d (%s) < index %d (%s)",
				i-1, workspaces[i-1].CreatedAt, i, workspaces[i].CreatedAt)
		}
	}
}

// TS-01-33: Verify that GET /api/v1/workspaces with user API key or PAT
// returns only the authenticated user's workspaces, excluding archived by
// default, ordered by created_at descending.
// Requirement: 01-REQ-6.2
func TestWorkspaceList_UserOwnOnly(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/alice-repo",
		OwnerID: "alice-id",
		Status:  "active",
	})
	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-archived",
		GitURL:  "https://github.com/org/alice-repo2",
		OwnerID: "alice-id",
		Status:  "archived",
	})
	env.seedWorkspace(t, &Workspace{
		Slug:    "bob-ws",
		GitURL:  "https://github.com/org/bob-repo",
		OwnerID: "bob-id",
		Status:  "active",
	})

	t.Run("user API key", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", userAuth("alice-id"))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
		}

		workspaces := parseWorkspaceListJSON(t, rec)
		slugs := make(map[string]bool)
		for _, ws := range workspaces {
			slugs[ws.Slug] = true
		}

		if !slugs["alice-ws"] {
			t.Error("missing alice-ws")
		}
		if slugs["alice-archived"] {
			t.Error("alice-archived should NOT appear (archived, default excluded)")
		}
		if slugs["bob-ws"] {
			t.Error("bob-ws should NOT appear (different owner)")
		}

		// Verify ordering by created_at descending.
		for i := 1; i < len(workspaces); i++ {
			prev, errP := time.Parse(time.RFC3339, workspaces[i-1].CreatedAt)
			curr, errC := time.Parse(time.RFC3339, workspaces[i].CreatedAt)
			if errP != nil || errC != nil {
				continue
			}
			if prev.Before(curr) {
				t.Errorf("not ordered by created_at desc at index %d", i)
			}
		}
	})

	t.Run("PAT with workspaces:read", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "",
			patAuth("alice-id", "workspaces:read"))

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
		}

		workspaces := parseWorkspaceListJSON(t, rec)
		for _, ws := range workspaces {
			if ws.OwnerID != "alice-id" {
				t.Errorf("returned workspace %q has owner_id %q; want %q",
					ws.Slug, ws.OwnerID, "alice-id")
			}
			if ws.Status == "archived" {
				t.Errorf("returned workspace %q has status %q; archived should be excluded",
					ws.Slug, ws.Status)
			}
		}
	})
}

// TS-01-34: Verify that GET /api/v1/workspaces?include_archived=true includes
// archived workspaces in the result set.
// Requirement: 01-REQ-6.3
func TestWorkspaceList_IncludeArchived(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/alice-repo",
		OwnerID: "alice-id",
		Status:  "active",
	})
	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-archived",
		GitURL:  "https://github.com/org/alice-repo2",
		OwnerID: "alice-id",
		Status:  "archived",
	})

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces?include_archived=true", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	workspaces := parseWorkspaceListJSON(t, rec)
	slugs := make(map[string]bool)
	for _, ws := range workspaces {
		slugs[ws.Slug] = true
	}

	if !slugs["alice-ws"] {
		t.Error("missing active workspace alice-ws")
	}
	if !slugs["alice-archived"] {
		t.Error("missing archived workspace alice-archived")
	}
}

// TS-01-35: Verify that GET /api/v1/workspaces returns an empty array when no
// workspaces match the query criteria.
// Requirement: 01-REQ-6.4
func TestWorkspaceList_Empty(t *testing.T) {
	env := newTestEnv(t)

	// Alice exists but owns no workspaces.
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	workspaces := parseWorkspaceListJSON(t, rec)
	if len(workspaces) != 0 {
		t.Errorf("got %d workspaces; want 0", len(workspaces))
	}
}

// TS-01-36: Verify that GET /api/v1/workspaces/:slug returns the workspace
// record for an authorized credential.
// Requirement: 01-REQ-7.1
func TestWorkspaceGet_Success(t *testing.T) {
	env := newTestEnv(t)

	branch := "main"
	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-ws",
		GitURL:  "https://github.com/org/alice-repo",
		Branch:  &branch,
		OwnerID: "alice-id",
		Status:  "active",
	})

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/alice-ws", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Slug != "alice-ws" {
		t.Errorf("slug = %q; want %q", ws.Slug, "alice-ws")
	}
	if ws.GitURL != "https://github.com/org/alice-repo" {
		t.Errorf("git_url = %q; want %q", ws.GitURL, "https://github.com/org/alice-repo")
	}
	if ws.Branch == nil || *ws.Branch != "main" {
		t.Errorf("branch = %v; want %q", ws.Branch, "main")
	}
	if ws.OwnerID != "alice-id" {
		t.Errorf("owner_id = %q; want %q", ws.OwnerID, "alice-id")
	}
	if ws.Status != "active" {
		t.Errorf("status = %q; want %q", ws.Status, "active")
	}
	if _, err := time.Parse(time.RFC3339, ws.CreatedAt); err != nil {
		t.Errorf("created_at %q is not valid RFC 3339: %v", ws.CreatedAt, err)
	}
	if _, err := time.Parse(time.RFC3339, ws.UpdatedAt); err != nil {
		t.Errorf("updated_at %q is not valid RFC 3339: %v", ws.UpdatedAt, err)
	}
}

// TS-01-37: Verify that GET /api/v1/workspaces/:slug returns HTTP 404 when no
// workspace exists with the given slug.
// Requirement: 01-REQ-7.2
func TestWorkspaceGet_NotFound(t *testing.T) {
	env := newTestEnv(t)

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/nonexistent-ws", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusNotFound)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-01-38: Verify that GET /api/v1/workspaces/:slug returns HTTP 404 (not 403)
// when the workspace exists but the credential doesn't own it.
// Requirement: 01-REQ-7.3
func TestWorkspaceGet_OtherUserWorkspace_Returns404(t *testing.T) {
	env := newTestEnv(t)

	// Bob's workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "bob-ws",
		GitURL:  "https://github.com/org/bob-repo",
		OwnerID: "bob-id",
		Status:  "active",
	})

	// Alice tries to access Bob's workspace.
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/bob-ws", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want %d (must be 404, not 403, to prevent slug enumeration)",
			rec.Code, http.StatusNotFound)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusNotFound)
	}
	if rec.Code == http.StatusForbidden {
		t.Error("returned 403 instead of 404; this leaks slug existence information")
	}
}

// TS-01-E11: Verify that GET /api/v1/workspaces returns all matching workspaces
// without pagination; no pagination headers or cursors in the response.
// Requirement: 01-REQ-6.E1
func TestWorkspaceList_NoPagination(t *testing.T) {
	env := newTestEnv(t)

	// Seed 25 active workspaces for alice.
	for i := 0; i < 25; i++ {
		env.seedWorkspace(t, &Workspace{
			Slug:    fmt.Sprintf("alice-ws-%02d", i),
			GitURL:  fmt.Sprintf("https://github.com/org/repo-%02d", i),
			OwnerID: "alice-id",
			Status:  "active",
		})
	}

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "",
		userAuth("alice-id"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	workspaces := parseWorkspaceListJSON(t, rec)
	if len(workspaces) != 25 {
		t.Errorf("got %d workspaces; want 25", len(workspaces))
	}

	// Verify no pagination headers are present.
	if link := rec.Header().Get("Link"); link != "" {
		t.Errorf("Link header = %q; want empty (no pagination)", link)
	}
	if cursor := rec.Header().Get("X-Next-Cursor"); cursor != "" {
		t.Errorf("X-Next-Cursor header = %q; want empty (no pagination)", cursor)
	}

	// Verify the response is a plain JSON array, not a paginated envelope.
	// parseWorkspaceListJSON already validates it's an array; if it were
	// an object like {"data":[...],"cursor":"..."} the parse would fail.
}
