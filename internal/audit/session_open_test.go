package audit

import (
	"net/http"
	"testing"
)

// TS-19-1: POST /api/v1/sessions with a valid workspace_slug creates a new
// active session and returns HTTP 201 with the session record including
// credential info extracted from AuthInfo.
func TestCreateSession_ValidWorkspace_TS19_1(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions",
		`{"workspace_slug":"my-workspace","run_id":"run-1","archetype":"coder","model":"claude-3-5-sonnet"}`,
		apiKeyAuth("cred-abc"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	s := parseSessionJSON(t, rec)

	if s.WorkspaceSlug != "my-workspace" {
		t.Errorf("workspace_slug = %q; want %q", s.WorkspaceSlug, "my-workspace")
	}
	if s.CredentialID != "cred-abc" {
		t.Errorf("credential_id = %q; want %q", s.CredentialID, "cred-abc")
	}
	if s.CredentialType != "api_key" {
		t.Errorf("credential_type = %q; want %q", s.CredentialType, "api_key")
	}
	if s.Status != "active" {
		t.Errorf("status = %q; want %q", s.Status, "active")
	}
	if s.ID == "" {
		t.Error("id is empty; want a generated UUID")
	}
	if s.StartedAt == "" {
		t.Error("started_at is empty; want an RFC3339 timestamp")
	}

	// Verify database row was created.
	count := env.countSessions(t, s.ID)
	if count != 1 {
		t.Errorf("agent_sessions row count for id=%q: %d; want 1", s.ID, count)
	}
}

// TS-19-2: POST /api/v1/sessions with a duplicate id returns HTTP 200 with
// the existing session record without creating a duplicate row.
func TestCreateSession_DuplicateID_TS19_2(t *testing.T) {
	env := newAuditTestEnv(t)

	// Pre-seed an existing session.
	env.seedSession(t, &Session{
		ID:             "session-dup",
		WorkspaceSlug:  "my-workspace",
		Status:         "active",
		CredentialID:   "cred-abc",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions",
		`{"id":"session-dup","workspace_slug":"my-workspace"}`,
		apiKeyAuth("cred-abc"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	s := parseSessionJSON(t, rec)

	if s.ID != "session-dup" {
		t.Errorf("id = %q; want %q", s.ID, "session-dup")
	}
	if s.Status != "active" {
		t.Errorf("status = %q; want %q", s.Status, "active")
	}

	// Verify no duplicate was created.
	count := env.countSessions(t, "session-dup")
	if count != 1 {
		t.Errorf("row count = %d; want 1 (no duplicate)", count)
	}
}

// TS-19-3: POST /api/v1/sessions with a missing workspace_slug returns
// HTTP 400 with the standard error envelope.
func TestCreateSession_MissingWorkspaceSlug_TS19_3(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions",
		`{}`,
		apiKeyAuth("cred-abc"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	errResp := parseErrorJSON(t, rec)

	if errResp.Error.Message != "workspace_slug is required" {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, "workspace_slug is required")
	}
	if errResp.Error.Code != http.StatusBadRequest {
		t.Errorf("error code = %d; want %d", errResp.Error.Code, http.StatusBadRequest)
	}
}

// 19-REQ-1.E2: POST /api/v1/sessions with all optional fields omitted
// creates the session with run_id, node_id, archetype, model set to empty
// string and metadata set to null.
func TestCreateSession_OptionalFieldsOmitted(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions",
		`{"workspace_slug":"my-workspace"}`,
		apiKeyAuth("cred-abc"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	s := parseSessionJSON(t, rec)

	if s.WorkspaceSlug != "my-workspace" {
		t.Errorf("workspace_slug = %q; want %q", s.WorkspaceSlug, "my-workspace")
	}
	if s.Status != "active" {
		t.Errorf("status = %q; want %q", s.Status, "active")
	}
	if s.ID == "" {
		t.Error("id is empty; want a generated UUID")
	}
}

// 19-REQ-1.3: POST /api/v1/sessions with an empty workspace_slug string
// returns HTTP 400.
func TestCreateSession_EmptyWorkspaceSlug(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions",
		`{"workspace_slug":""}`,
		apiKeyAuth("cred-abc"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	errResp := parseErrorJSON(t, rec)
	if errResp.Error.Message != "workspace_slug is required" {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, "workspace_slug is required")
	}
}
