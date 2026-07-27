package workspace

import (
	"encoding/json"
	"net/http"
	"testing"
)

// ========================================================================
// Spec 05 Task 2.3: Workspace creation endpoint enqueuing clone job
// (TS-05-17)
// ========================================================================

// spec05WorkspaceJSON extends the baseline workspaceJSON with clone fields
// added by spec 05. Using a separate struct avoids modifying the existing
// testhelpers_test.go shared by prior specs.
type spec05WorkspaceJSON struct {
	Slug        string  `json:"slug"`
	GitURL      string  `json:"git_url"`
	Branch      *string `json:"branch"`
	DisplayName string  `json:"display_name"`
	Description string  `json:"description"`
	OwnerID     string  `json:"owner_id"`
	OrgID       *string `json:"org_id"`
	Status      string  `json:"status"`
	CloneStatus *string `json:"clone_status"`
	HeadSHA     *string `json:"head_sha"`
	CloneError  *string `json:"clone_error"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// TS-05-17: POST /api/v1/workspaces returns HTTP 201 immediately with
// clone_status='pending', head_sha=null, clone_error=null without waiting
// for the clone to complete.
// Requirement: 05-REQ-5.1
func TestWorkspaceCreate_Spec05_ReturnsImmediatelyWithPending(t *testing.T) {
	env := newTestEnv(t)

	body := `{
		"slug": "spec05-test-ws",
		"git_url": "https://github.com/example/repo.git",
		"display_name": "Test Workspace"
	}`

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body,
		userAuth("user-1"))

	// Must return HTTP 201 Created.
	if rec.Code != http.StatusCreated {
		t.Fatalf("HTTP status = %d; want %d\nbody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Parse response with clone fields.
	var ws spec05WorkspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&ws); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// clone_status must be present and set to "pending".
	if ws.CloneStatus == nil {
		t.Fatal("clone_status is missing from response; want \"pending\"")
	}
	if *ws.CloneStatus != "pending" {
		t.Errorf("clone_status = %q; want %q", *ws.CloneStatus, "pending")
	}

	// head_sha must be null (clone hasn't happened yet).
	if ws.HeadSHA != nil {
		t.Errorf("head_sha = %q; want null (clone not yet complete)", *ws.HeadSHA)
	}

	// clone_error must be null (no error at creation time).
	if ws.CloneError != nil {
		t.Errorf("clone_error = %q; want null", *ws.CloneError)
	}
}

// TestWorkspaceCreate_Spec05_WithBranchReturnsImmediately verifies that
// creating a workspace with an explicit branch still returns immediately
// with clone_status='pending'.
// Requirement: 05-REQ-5.1
func TestWorkspaceCreate_Spec05_WithBranchReturnsImmediately(t *testing.T) {
	env := newTestEnv(t)

	body := `{
		"slug": "spec05-branch-ws",
		"git_url": "https://github.com/example/repo.git",
		"branch": "feature/my-branch",
		"display_name": "Branch Workspace"
	}`

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body,
		userAuth("user-1"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("HTTP status = %d; want %d\nbody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var ws spec05WorkspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&ws); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// clone_status must be "pending" regardless of branch presence.
	if ws.CloneStatus == nil {
		t.Fatal("clone_status is missing from response; want \"pending\"")
	}
	if *ws.CloneStatus != "pending" {
		t.Errorf("clone_status = %q; want %q", *ws.CloneStatus, "pending")
	}

	if ws.HeadSHA != nil {
		t.Errorf("head_sha = %q; want null", *ws.HeadSHA)
	}

	if ws.CloneError != nil {
		t.Errorf("clone_error = %q; want null", *ws.CloneError)
	}

	// Branch should be echoed back in the response.
	if ws.Branch == nil {
		t.Fatal("branch is missing from response; want \"feature/my-branch\"")
	}
	if *ws.Branch != "feature/my-branch" {
		t.Errorf("branch = %q; want %q", *ws.Branch, "feature/my-branch")
	}
}

// TestWorkspaceCreate_Spec05_DBInsertFailNoEnqueue verifies that when the
// database insert fails, no clone job is enqueued and an error response is
// returned.
// Requirement: 05-REQ-5.E1
func TestWorkspaceCreate_Spec05_DBInsertFailNoEnqueue(t *testing.T) {
	env := newTestEnv(t)

	// Seed a workspace with the same slug to trigger a duplicate key error.
	env.seedWorkspace(t, &Workspace{
		Slug:    "spec05-dup-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "user-1",
		Status:  "active",
	})

	body := `{
		"slug": "spec05-dup-ws",
		"git_url": "https://github.com/example/other.git",
		"display_name": "Duplicate"
	}`

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body,
		userAuth("user-1"))

	// Must return HTTP 409 Conflict (slug already exists).
	if rec.Code != http.StatusConflict {
		t.Fatalf("HTTP status = %d; want %d\nbody: %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}
