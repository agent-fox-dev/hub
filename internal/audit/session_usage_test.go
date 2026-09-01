package audit

import (
	"net/http"
	"testing"
)

// TS-19-8: POST /api/v1/sessions/:id/usage on an active session by the
// session owner creates a token_usage record and returns HTTP 201.
func TestReportUsage_ActiveByOwner_TS19_8(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-1/usage",
		`{"model":"claude-3-5-sonnet","input_tokens":200,"output_tokens":80,"cache_read_tokens":10}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	u := parseUsageJSON(t, rec)

	if u.SessionID != "sess-1" {
		t.Errorf("session_id = %q; want %q", u.SessionID, "sess-1")
	}
	if u.WorkspaceSlug != "ws-1" {
		t.Errorf("workspace_slug = %q; want %q", u.WorkspaceSlug, "ws-1")
	}
	if u.Model != "claude-3-5-sonnet" {
		t.Errorf("model = %q; want %q", u.Model, "claude-3-5-sonnet")
	}
	if u.InputTokens != 200 {
		t.Errorf("input_tokens = %d; want %d", u.InputTokens, 200)
	}
	if u.OutputTokens != 80 {
		t.Errorf("output_tokens = %d; want %d", u.OutputTokens, 80)
	}
	if u.CacheReadTokens != 10 {
		t.Errorf("cache_read_tokens = %d; want %d", u.CacheReadTokens, 10)
	}
	if u.ID == "" {
		t.Error("id is empty; want a generated UUID")
	}
	if u.ReportedAt == "" {
		t.Error("reported_at is empty; want an RFC3339 timestamp")
	}
}

// TS-19-9: POST /api/v1/sessions/:id/usage for a non-existent session
// returns HTTP 404.
func TestReportUsage_SessionNotFound_TS19_9(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/ghost/usage",
		`{"model":"gpt-4"}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// TS-19-10: POST /api/v1/sessions/:id/usage by a non-owner non-admin
// credential returns HTTP 403 Forbidden.
func TestReportUsage_NonOwner_TS19_10(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-1/usage",
		`{"model":"gpt-4","input_tokens":50}`,
		apiKeyAuth("cred-other"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// TS-19-11: POST /api/v1/sessions/:id/usage on a non-active session returns
// HTTP 409 with error body 'session is not active'.
func TestReportUsage_SessionNotActive_TS19_11(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-done",
		WorkspaceSlug:  "ws-1",
		Status:         "completed",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-done/usage",
		`{"model":"gpt-4","input_tokens":10}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}

	errResp := parseErrorJSON(t, rec)
	if errResp.Error.Message != "session is not active" {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, "session is not active")
	}
}

// TS-19-12: POST /api/v1/sessions/:id/usage with a missing model field
// returns HTTP 400.
func TestReportUsage_MissingModel_TS19_12(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-1/usage",
		`{"input_tokens":50}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// 19-REQ-3.6: Negative token counts return HTTP 422.
func TestReportUsage_NegativeTokens(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-1/usage",
		`{"model":"gpt-4","input_tokens":-1,"output_tokens":10,"cache_read_tokens":0}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}

	errResp := parseErrorJSON(t, rec)
	if errResp.Error.Message != "token counts must be non-negative" {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, "token counts must be non-negative")
	}
}

// 19-REQ-3.E1: Multiple usage calls on the same session create separate
// token_usage records.
func TestReportUsage_MultipleCallsCreateSeparateRecords(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	// First usage report.
	rec1 := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-1/usage",
		`{"model":"gpt-4","input_tokens":100,"output_tokens":50,"cache_read_tokens":0}`,
		apiKeyAuth("cred-owner"))

	if rec1.Code != http.StatusCreated {
		t.Fatalf("first usage: status = %d; want %d", rec1.Code, http.StatusCreated)
	}

	u1 := parseUsageJSON(t, rec1)

	// Second usage report.
	rec2 := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-1/usage",
		`{"model":"claude-3-5-sonnet","input_tokens":200,"output_tokens":80,"cache_read_tokens":10}`,
		apiKeyAuth("cred-owner"))

	if rec2.Code != http.StatusCreated {
		t.Fatalf("second usage: status = %d; want %d", rec2.Code, http.StatusCreated)
	}

	u2 := parseUsageJSON(t, rec2)

	// Each call should produce a distinct ID.
	if u1.ID == u2.ID {
		t.Errorf("both usage records have same id %q; want distinct IDs", u1.ID)
	}
}

// 19-REQ-3.E4: Admin can report usage on session owned by another credential.
func TestReportUsage_AdminOverride(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-1/usage",
		`{"model":"gpt-4","input_tokens":50,"output_tokens":10,"cache_read_tokens":0}`,
		adminAuth())

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// 19-REQ-3.4: Usage on terminated session returns 409.
func TestReportUsage_TerminatedSession(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-terminated",
		WorkspaceSlug:  "ws-1",
		Status:         "terminated",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-terminated/usage",
		`{"model":"gpt-4","input_tokens":10}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}
