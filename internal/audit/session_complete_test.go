package audit

import (
	"net/http"
	"testing"
)

// TS-19-4: POST /api/v1/sessions/:id/complete on an active session by the
// session owner sets terminal status, records ended_at, and returns 200.
func TestCompleteSession_ActiveByOwner_TS19_4(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed an active session.
	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-1/complete",
		`{"status":"completed","input_tokens":100,"output_tokens":50,"duration_ms":3000}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	s := parseSessionJSON(t, rec)

	if s.ID != "sess-1" {
		t.Errorf("id = %q; want %q", s.ID, "sess-1")
	}
	if s.Status != "completed" {
		t.Errorf("status = %q; want %q", s.Status, "completed")
	}
	if s.EndedAt == nil || *s.EndedAt == "" {
		t.Error("ended_at is nil or empty; want an RFC3339 timestamp")
	}

	// Verify database was updated.
	dbStatus := env.getSessionStatus(t, "sess-1")
	if dbStatus != "completed" {
		t.Errorf("DB status = %q; want %q", dbStatus, "completed")
	}
}

// TS-19-5: POST /api/v1/sessions/:id/complete on an already-terminal session
// returns HTTP 200 with the existing record unchanged.
func TestCompleteSession_AlreadyTerminal_TS19_5(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-done",
		WorkspaceSlug:  "ws-1",
		Status:         "completed",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-done/complete",
		`{"status":"failed"}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	s := parseSessionJSON(t, rec)

	// Status should remain 'completed', not change to 'failed'.
	if s.Status != "completed" {
		t.Errorf("status = %q; want %q (unchanged)", s.Status, "completed")
	}

	// Verify DB is unchanged.
	dbStatus := env.getSessionStatus(t, "sess-done")
	if dbStatus != "completed" {
		t.Errorf("DB status = %q; want %q", dbStatus, "completed")
	}
}

// TS-19-6: POST /api/v1/sessions/:id/complete by a non-owner non-admin
// credential returns HTTP 403 Forbidden.
func TestCompleteSession_NonOwner_TS19_6(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-1/complete",
		`{"status":"completed"}`,
		apiKeyAuth("cred-other"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// TS-19-7: POST /api/v1/sessions/:id/complete for a non-existent session
// returns HTTP 404.
func TestCompleteSession_NotFound_TS19_7(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/nonexistent/complete",
		`{"status":"completed"}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// 19-REQ-2.E1: POST /api/v1/sessions/:id/complete with status='terminated'
// returns HTTP 400 because 'terminated' is not a valid client-settable status.
func TestCompleteSession_TerminatedStatus_Rejected(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-1/complete",
		`{"status":"terminated"}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// 19-REQ-2.E2: Admin can close session owned by another credential.
func TestCompleteSession_AdminOverride(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-1/complete",
		`{"status":"completed"}`,
		adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	s := parseSessionJSON(t, rec)
	if s.Status != "completed" {
		t.Errorf("status = %q; want %q", s.Status, "completed")
	}
}

// 19-REQ-2.E3: Complete on a terminated session returns 200 with existing
// record, treating all terminal states uniformly.
func TestCompleteSession_TerminatedSession_Returns200(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-terminated",
		WorkspaceSlug:  "ws-1",
		Status:         "terminated",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-terminated/complete",
		`{"status":"completed"}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	s := parseSessionJSON(t, rec)
	if s.Status != "terminated" {
		t.Errorf("status = %q; want %q (unchanged)", s.Status, "terminated")
	}
}

// 19-REQ-2.1: Complete with status 'failed' on an active session.
func TestCompleteSession_FailedStatus(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-fail",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-fail/complete",
		`{"status":"failed","error_message":"something went wrong"}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	s := parseSessionJSON(t, rec)
	if s.Status != "failed" {
		t.Errorf("status = %q; want %q", s.Status, "failed")
	}
}

// 19-REQ-2.1: Complete with status 'timeout' on an active session.
func TestCompleteSession_TimeoutStatus(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-timeout",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-owner",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodPost, "/api/v1/sessions/sess-timeout/complete",
		`{"status":"timeout"}`,
		apiKeyAuth("cred-owner"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	s := parseSessionJSON(t, rec)
	if s.Status != "timeout" {
		t.Errorf("status = %q; want %q", s.Status, "timeout")
	}
}
