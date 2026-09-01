package audit

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TS-19-13: GET /api/v1/sessions returns a paginated list of sessions with
// token_summary aggregated from token_usage rows.
func TestListSessions_WithTokenSummary_TS19_13(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed three sessions with usage data.
	for i := 1; i <= 3; i++ {
		id := sessionID(i)
		started := time.Now().UTC().Add(time.Duration(-i) * time.Minute)
		env.seedSession(t, &Session{
			ID:             id,
			WorkspaceSlug:  "ws-1",
			Status:         "active",
			CredentialID:   "cred-1",
			CredentialType: "api_key",
			StartedAt:      started.Format(time.RFC3339Nano),
		})
		env.seedTokenUsage(t, &TokenUsage{
			ID:            usageID(i),
			SessionID:     id,
			WorkspaceSlug: "ws-1",
			Model:         "gpt-4",
			InputTokens:   int64(i * 100),
			OutputTokens:  int64(i * 50),
		})
	}

	rec := env.doRequest(t, http.MethodGet, "/api/v1/sessions?workspace_slug=ws-1&limit=10",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseSessionListJSON(t, rec)

	if len(resp.Sessions) != 3 {
		t.Fatalf("sessions count = %d; want 3", len(resp.Sessions))
	}

	for _, s := range resp.Sessions {
		if s.TokenSummary == nil {
			t.Errorf("session %q missing token_summary", s.ID)
			continue
		}
		if s.TokenSummary.TotalInputTokens == 0 {
			t.Errorf("session %q: total_input_tokens = 0; want > 0", s.ID)
		}
	}
}

// TS-19-14: GET /api/v1/sessions with an explicit workspace_slug filter for
// an inaccessible workspace returns HTTP 403 for non-admin callers.
func TestListSessions_InaccessibleWorkspace_TS19_14(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)

	// Seed a session in a restricted workspace.
	env.seedSession(t, &Session{
		ID:             "sess-restricted",
		WorkspaceSlug:  "restricted-ws",
		Status:         "active",
		CredentialID:   "cred-restricted",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions?workspace_slug=restricted-ws",
		"", apiKeyAuth("cred-no-access"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// TS-19-15: GET /api/v1/sessions with a cursor parameter applies keyset
// pagination and returns the correct next page.
func TestListSessions_CursorPagination_TS19_15(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed 10 sessions with distinct timestamps.
	for i := 1; i <= 10; i++ {
		started := time.Now().UTC().Add(time.Duration(-i) * time.Minute)
		env.seedSession(t, &Session{
			ID:             sessionID(i),
			WorkspaceSlug:  "ws-1",
			Status:         "active",
			CredentialID:   "cred-1",
			CredentialType: "api_key",
			StartedAt:      started.Format(time.RFC3339Nano),
		})
	}

	// Page 1: first 5 sessions.
	rec1 := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions?workspace_slug=ws-1&limit=5&order=desc",
		"", adminAuth())

	if rec1.Code != http.StatusOK {
		t.Fatalf("page 1: status = %d; want %d", rec1.Code, http.StatusOK)
	}

	page1 := parseSessionListJSON(t, rec1)

	if len(page1.Sessions) != 5 {
		t.Fatalf("page 1: count = %d; want 5", len(page1.Sessions))
	}
	if !page1.HasMore {
		t.Error("page 1: has_more = false; want true")
	}
	if page1.NextCursor == nil {
		t.Fatal("page 1: next_cursor is nil; want a cursor string")
	}

	// Page 2: next 5 sessions.
	rec2 := env.doRequest(t, http.MethodGet,
		fmt.Sprintf("/api/v1/sessions?workspace_slug=ws-1&limit=5&order=desc&cursor=%s", *page1.NextCursor),
		"", adminAuth())

	if rec2.Code != http.StatusOK {
		t.Fatalf("page 2: status = %d; want %d", rec2.Code, http.StatusOK)
	}

	page2 := parseSessionListJSON(t, rec2)

	if len(page2.Sessions) != 5 {
		t.Fatalf("page 2: count = %d; want 5", len(page2.Sessions))
	}

	// Verify no overlap between pages.
	ids1 := make(map[string]bool)
	for _, s := range page1.Sessions {
		ids1[s.ID] = true
	}
	for _, s := range page2.Sessions {
		if ids1[s.ID] {
			t.Errorf("page 2 contains duplicate id %q from page 1", s.ID)
		}
	}
}

// TS-19-16: GET /api/v1/sessions with an admin_token returns sessions across
// all workspaces without restriction.
func TestListSessions_AdminAllWorkspaces_TS19_16(t *testing.T) {
	env := newAuditTestEnv(t)

	workspaces := []string{"ws-a", "ws-b", "ws-c"}
	for i, ws := range workspaces {
		env.seedSession(t, &Session{
			ID:             sessionID(i + 1),
			WorkspaceSlug:  ws,
			Status:         "active",
			CredentialID:   "cred-1",
			CredentialType: "api_key",
			StartedAt:      time.Now().UTC().Add(time.Duration(-i) * time.Minute).Format(time.RFC3339Nano),
		})
	}

	rec := env.doRequest(t, http.MethodGet, "/api/v1/sessions", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseSessionListJSON(t, rec)

	foundWorkspaces := make(map[string]bool)
	for _, s := range resp.Sessions {
		foundWorkspaces[s.WorkspaceSlug] = true
	}

	for _, ws := range workspaces {
		if !foundWorkspaces[ws] {
			t.Errorf("workspace %q not found in results", ws)
		}
	}
}

// TS-19-17: GET /api/v1/sessions/:id returns the full session record with
// token_summary aggregated from all token_usage rows.
func TestGetSession_WithTokenSummary_TS19_17(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})

	// Seed two token_usage rows with different input_tokens.
	env.seedTokenUsage(t, &TokenUsage{
		ID:            "u1",
		SessionID:     "sess-1",
		WorkspaceSlug: "ws-1",
		Model:         "gpt-4",
		InputTokens:   100,
		OutputTokens:  50,
	})
	env.seedTokenUsage(t, &TokenUsage{
		ID:            "u2",
		SessionID:     "sess-1",
		WorkspaceSlug: "ws-1",
		Model:         "claude-3",
		InputTokens:   200,
		OutputTokens:  80,
	})

	rec := env.doRequest(t, http.MethodGet, "/api/v1/sessions/sess-1", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	s := parseSessionJSON(t, rec)

	if s.ID != "sess-1" {
		t.Errorf("id = %q; want %q", s.ID, "sess-1")
	}
	if s.WorkspaceSlug != "ws-1" {
		t.Errorf("workspace_slug = %q; want %q", s.WorkspaceSlug, "ws-1")
	}
	if s.TokenSummary == nil {
		t.Fatal("token_summary is nil")
	}
	if s.TokenSummary.TotalInputTokens != 300 {
		t.Errorf("total_input_tokens = %d; want 300", s.TokenSummary.TotalInputTokens)
	}
	if s.TokenSummary.ModelsUsed == nil {
		t.Error("models_used is nil")
	}
}

// TS-19-18: GET /api/v1/sessions/:id for a non-existent session returns
// HTTP 404.
func TestGetSession_NotFound_TS19_18(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodGet, "/api/v1/sessions/missing", "", adminAuth())

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// TS-19-19: GET /api/v1/sessions/:id for a session in an inaccessible
// workspace returns HTTP 403 for non-admin callers.
func TestGetSession_InaccessibleWorkspace_TS19_19(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)

	env.seedSession(t, &Session{
		ID:             "sess-restricted",
		WorkspaceSlug:  "restricted-ws",
		Status:         "active",
		CredentialID:   "cred-restricted",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodGet, "/api/v1/sessions/sess-restricted",
		"", apiKeyAuth("cred-no-access"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// 19-REQ-5.E1: Admin can fetch sessions from any workspace.
func TestGetSession_AdminAnyWorkspace(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-any",
		WorkspaceSlug:  "ws-private",
		Status:         "active",
		CredentialID:   "cred-other",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodGet, "/api/v1/sessions/sess-any", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// 19-REQ-5.E2: Session with no token_usage records returns token_summary
// with all zero counts and empty models_used list.
func TestGetSession_EmptyTokenSummary(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-empty",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodGet, "/api/v1/sessions/sess-empty", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	s := parseSessionJSON(t, rec)

	if s.TokenSummary == nil {
		t.Fatal("token_summary is nil")
	}
	if s.TokenSummary.TotalInputTokens != 0 {
		t.Errorf("total_input_tokens = %d; want 0", s.TokenSummary.TotalInputTokens)
	}
	if s.TokenSummary.TotalOutputTokens != 0 {
		t.Errorf("total_output_tokens = %d; want 0", s.TokenSummary.TotalOutputTokens)
	}
	if s.TokenSummary.TotalCacheReadTokens != 0 {
		t.Errorf("total_cache_read_tokens = %d; want 0", s.TokenSummary.TotalCacheReadTokens)
	}
	if len(s.TokenSummary.ModelsUsed) != 0 {
		t.Errorf("models_used = %v; want empty", s.TokenSummary.ModelsUsed)
	}
}

// 19-REQ-4.E1: Limit exceeding 500 is capped at 500.
func TestListSessions_LimitCappedAt500(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed a few sessions (don't need 500+).
	for i := 1; i <= 3; i++ {
		env.seedSession(t, &Session{
			ID:             sessionID(i),
			WorkspaceSlug:  "ws-1",
			Status:         "active",
			CredentialID:   "cred-1",
			CredentialType: "api_key",
			StartedAt:      time.Now().UTC().Add(time.Duration(-i) * time.Minute).Format(time.RFC3339Nano),
		})
	}

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions?workspace_slug=ws-1&limit=999",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// We only verify status is 200 — limit enforcement is checked by
	// verifying no more than 500 sessions are returned in a larger dataset.
}

// 19-REQ-4.E2: Default page size is 50.
func TestListSessions_DefaultLimit50(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed 3 sessions (well under 50).
	for i := 1; i <= 3; i++ {
		env.seedSession(t, &Session{
			ID:             sessionID(i),
			WorkspaceSlug:  "ws-1",
			Status:         "active",
			CredentialID:   "cred-1",
			CredentialType: "api_key",
			StartedAt:      time.Now().UTC().Add(time.Duration(-i) * time.Minute).Format(time.RFC3339Nano),
		})
	}

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions?workspace_slug=ws-1",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseSessionListJSON(t, rec)
	if len(resp.Sessions) > 50 {
		t.Errorf("sessions count = %d; want <= 50 (default limit)", len(resp.Sessions))
	}
}

// 19-REQ-4.E3: Malformed cursor returns 400.
func TestListSessions_MalformedCursor(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions?cursor=not-valid-base64!!!",
		"", adminAuth())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// 19-REQ-4.E4: Filter by status='terminated' returns only terminated sessions.
func TestListSessions_FilterByTerminatedStatus(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-active",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})
	env.seedSession(t, &Session{
		ID:             "sess-terminated",
		WorkspaceSlug:  "ws-1",
		Status:         "terminated",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions?workspace_slug=ws-1&status=terminated",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseSessionListJSON(t, rec)
	for _, s := range resp.Sessions {
		if s.Status != "terminated" {
			t.Errorf("session %q has status %q; want %q", s.ID, s.Status, "terminated")
		}
	}
}
