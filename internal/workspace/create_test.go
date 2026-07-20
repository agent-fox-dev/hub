package workspace

import (
	"net/http"
	"testing"
	"time"
)

// TS-01-25: Verify that POST /api/v1/workspaces creates a workspace with
// status active, sets owner_id to the creating user, and returns HTTP 201
// with the workspace JSON object.
// Requirement: 01-REQ-5.1
func TestWorkspaceCreate_Success(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("alice-id")
	body := `{"slug":"new-workspace","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Slug != "new-workspace" {
		t.Errorf("slug = %q; want %q", ws.Slug, "new-workspace")
	}
	if ws.GitURL != "https://github.com/org/repo" {
		t.Errorf("git_url = %q; want %q", ws.GitURL, "https://github.com/org/repo")
	}
	if ws.Status != "active" {
		t.Errorf("status = %q; want %q", ws.Status, "active")
	}
	if ws.OwnerID != "alice-id" {
		t.Errorf("owner_id = %q; want %q", ws.OwnerID, "alice-id")
	}
	if _, err := time.Parse(time.RFC3339, ws.CreatedAt); err != nil {
		t.Errorf("created_at %q is not valid RFC 3339: %v", ws.CreatedAt, err)
	}
	if _, err := time.Parse(time.RFC3339, ws.UpdatedAt); err != nil {
		t.Errorf("updated_at %q is not valid RFC 3339: %v", ws.UpdatedAt, err)
	}
}

// TS-01-26: Verify that creating a workspace with a duplicate slug returns
// HTTP 409.
// Requirement: 01-REQ-5.2
func TestWorkspaceCreate_DuplicateSlug(t *testing.T) {
	env := newTestEnv(t)

	// Seed existing workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "existing-ws",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "user-1",
		Status:  "active",
	})

	auth := userAuth("user-1")
	body := `{"slug":"existing-ws","git_url":"https://github.com/org/repo2"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusConflict)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusConflict {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusConflict)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-01-27: Verify that providing an org_id when the creating user is not a
// member of that organization returns HTTP 403.
// Requirement: 01-REQ-5.3
func TestWorkspaceCreate_NonMemberOrg(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("alice-id")
	body := `{"slug":"org-ws","git_url":"https://github.com/org/repo","org_id":"other-org-uuid"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusForbidden {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusForbidden)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-01-28: Verify that providing an org_id that references a non-existent
// organization returns HTTP 400.
// Requirement: 01-REQ-5.4
func TestWorkspaceCreate_NonexistentOrg(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("alice-id")
	body := `{"slug":"org-ws","git_url":"https://github.com/org/repo","org_id":"nonexistent-org-uuid"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusBadRequest)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusBadRequest)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-01-29: Verify that a missing or malformed request body returns HTTP 400.
// Requirement: 01-REQ-5.5
func TestWorkspaceCreate_MalformedBody(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("alice-id")

	t.Run("malformed JSON", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", "not valid json", auth)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/v1/workspaces status = %d; want %d",
				rec.Code, http.StatusBadRequest)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusBadRequest {
			t.Errorf("error.code = %d; want %d",
				resp.Error.Code, http.StatusBadRequest)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", "", auth)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/v1/workspaces status = %d; want %d",
				rec.Code, http.StatusBadRequest)
		}
		resp := parseErrorEnvelope(t, rec)
		if resp.Error.Code != http.StatusBadRequest {
			t.Errorf("error.code = %d; want %d",
				resp.Error.Code, http.StatusBadRequest)
		}
	})
}

// TS-01-30: Verify that when a valid branch is provided in the create request,
// it is stored and returned in the workspace object.
// Requirement: 01-REQ-5.6
func TestWorkspaceCreate_WithBranch(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("alice-id")
	body := `{"slug":"branched-ws","git_url":"https://github.com/org/repo","branch":"feature/my-feature"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Branch == nil || *ws.Branch != "feature/my-feature" {
		t.Errorf("branch = %v; want %q", ws.Branch, "feature/my-feature")
	}

	// Verify the DB row has the branch value.
	var branch *string
	err := env.db.QueryRow("SELECT branch FROM workspaces WHERE slug = ?", "branched-ws").Scan(&branch)
	if err != nil {
		t.Fatalf("querying branch from DB: %v", err)
	}
	if branch == nil || *branch != "feature/my-feature" {
		t.Errorf("DB branch = %v; want %q", branch, "feature/my-feature")
	}
}

// TS-01-31: Verify that when branch is omitted from the create request, the
// workspace stores NULL for branch and returns branch: null.
// Requirement: 01-REQ-5.7
func TestWorkspaceCreate_WithoutBranch(t *testing.T) {
	env := newTestEnv(t)

	auth := userAuth("alice-id")
	body := `{"slug":"no-branch-ws","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusCreated)
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Branch != nil {
		t.Errorf("branch = %v; want nil", ws.Branch)
	}

	// Verify the DB row has NULL branch.
	var branch *string
	err := env.db.QueryRow("SELECT branch FROM workspaces WHERE slug = ?", "no-branch-ws").Scan(&branch)
	if err != nil {
		t.Fatalf("querying branch from DB: %v", err)
	}
	if branch != nil {
		t.Errorf("DB branch = %v; want nil", branch)
	}
}

// TS-01-E8: Verify that an admin token attempting to call POST
// /api/v1/workspaces is rejected with HTTP 403.
// Requirement: 01-REQ-5.E1
func TestWorkspaceCreate_AdminToken(t *testing.T) {
	env := newTestEnv(t)

	body := `{"slug":"admin-created-ws","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, adminAuth())

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusForbidden {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusForbidden)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-01-E9: Verify that a PAT with workspaces:read (but not
// workspaces:create) attempting to create a workspace is rejected with
// HTTP 403.
// Requirement: 01-REQ-5.E2
func TestWorkspaceCreate_PATReadOnly(t *testing.T) {
	env := newTestEnv(t)

	auth := patAuth("alice-id", "workspaces:read")
	body := `{"slug":"read-pat-ws","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusForbidden {
		t.Errorf("error.code = %d; want %d", resp.Error.Code, http.StatusForbidden)
	}
	if resp.Error.Message == "" {
		t.Error("error.message is empty; want non-empty descriptive message")
	}
}

// TS-01-E10: Verify that a transient database insert failure after validation
// passes returns HTTP 500 with no partial workspace record retained.
// Requirement: 01-REQ-5.E3
func TestWorkspaceCreate_DBInsertFailure(t *testing.T) {
	env := newTestEnv(t)

	// Break the workspaces table to simulate an insert failure by dropping
	// the table and recreating it with a schema that rejects valid inserts.
	if _, err := env.db.Exec("DROP TABLE workspaces"); err != nil {
		t.Fatalf("failed to drop workspaces table: %v", err)
	}
	if _, err := env.db.Exec("CREATE TABLE workspaces (slug TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("failed to recreate broken workspaces table: %v", err)
	}

	auth := userAuth("alice-id")
	body := `{"slug":"db-fail-ws","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("POST /api/v1/workspaces status = %d; want %d",
			rec.Code, http.StatusInternalServerError)
	}
	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Code != http.StatusInternalServerError {
		t.Errorf("error.code = %d; want %d",
			resp.Error.Code, http.StatusInternalServerError)
	}

	// Verify no partial row exists.
	var count int
	if err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", "db-fail-ws").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("found %d rows with slug 'db-fail-ws'; want 0", count)
	}
}
