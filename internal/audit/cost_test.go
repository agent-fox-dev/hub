package audit

import (
	"math"
	"net/http"
	"testing"
	"time"
)

// TS-19-24: GET /api/v1/workspaces/:slug/cost returns aggregated token usage
// grouped by the specified dimension with correct session counts.
// Requirement: 19-REQ-7.1
func TestWorkspaceCost_GroupByDay_TS19_24(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed two sessions for workspace 'ws-1'.
	for i := 1; i <= 2; i++ {
		env.seedSession(t, &Session{
			ID:             sessionID(i),
			WorkspaceSlug:  "ws-1",
			Status:         "completed",
			CredentialID:   "cred-1",
			CredentialType: "api_key",
		})
	}

	// Seed token_usage rows reported within the query period (2026-09-01).
	reported := "2026-09-01T12:00:00Z"
	env.seedTokenUsage(t, &TokenUsage{
		ID:              "u-1",
		SessionID:       sessionID(1),
		WorkspaceSlug:   "ws-1",
		Model:           "gpt-4",
		InputTokens:     200,
		OutputTokens:    100,
		CacheReadTokens: 10,
		ReportedAt:      reported,
	})
	env.seedTokenUsage(t, &TokenUsage{
		ID:              "u-2",
		SessionID:       sessionID(2),
		WorkspaceSlug:   "ws-1",
		Model:           "claude-3",
		InputTokens:     300,
		OutputTokens:    150,
		CacheReadTokens: 20,
		ReportedAt:      reported,
	})

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/cost?group_by=day&since=2026-09-01T00:00:00Z&until=2026-09-02T00:00:00Z",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseCostJSON(t, rec)

	if resp.Workspace != "ws-1" {
		t.Errorf("workspace = %q; want %q", resp.Workspace, "ws-1")
	}
	if resp.Period.Since != "2026-09-01T00:00:00Z" {
		t.Errorf("period.since = %q; want %q", resp.Period.Since, "2026-09-01T00:00:00Z")
	}
	if resp.Period.Until != "2026-09-02T00:00:00Z" {
		t.Errorf("period.until = %q; want %q", resp.Period.Until, "2026-09-02T00:00:00Z")
	}

	// totals should include both sessions.
	if resp.Totals.Sessions != 2 {
		t.Errorf("totals.sessions = %d; want 2", resp.Totals.Sessions)
	}
	if resp.Totals.InputTokens != 500 {
		t.Errorf("totals.input_tokens = %d; want 500", resp.Totals.InputTokens)
	}
	if resp.Totals.OutputTokens != 250 {
		t.Errorf("totals.output_tokens = %d; want 250", resp.Totals.OutputTokens)
	}
	if resp.Totals.CacheReadTokens != 30 {
		t.Errorf("totals.cache_read_tokens = %d; want 30", resp.Totals.CacheReadTokens)
	}

	// Breakdown should have day-level entries with a 'date' field.
	for _, entry := range resp.Breakdown {
		if entry.Date == "" {
			t.Error("breakdown entry missing 'date' field for group_by=day")
		}
	}
}

// TS-19-25: GET /api/v1/workspaces/:slug/cost with since >= until returns
// HTTP 400 with error 'since must be before until'.
// Requirement: 19-REQ-7.2
// Property: 19-PROP-9
func TestWorkspaceCost_SinceAfterUntil_TS19_25(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/cost?since=2026-09-02T00:00:00Z&until=2026-09-01T00:00:00Z",
		"", adminAuth())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	errResp := parseErrorJSON(t, rec)
	if errResp.Error.Message != "since must be before until" {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, "since must be before until")
	}
}

// TS-19-25 variant: since == until also returns 400.
func TestWorkspaceCost_SinceEqualsUntil(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/cost?since=2026-09-01T00:00:00Z&until=2026-09-01T00:00:00Z",
		"", adminAuth())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	errResp := parseErrorJSON(t, rec)
	if errResp.Error.Message != "since must be before until" {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, "since must be before until")
	}
}

// TS-19-26: GET /api/v1/workspaces/:slug/cost for an inaccessible workspace
// returns HTTP 403 for non-admin callers.
// Requirement: 19-REQ-7.3
func TestWorkspaceCost_InaccessibleWorkspace_TS19_26(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/restricted-ws/cost",
		"", apiKeyAuth("cred-no-access"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// TS-19-27: GET /api/v1/workspaces/:slug/cost with no since/until parameters
// defaults to the last 24 hours.
// Requirement: 19-REQ-7.4
func TestWorkspaceCost_DefaultTimeRange_TS19_27(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed a session and usage within the last 24 hours.
	env.seedSession(t, &Session{
		ID:             "sess-recent",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})
	env.seedTokenUsage(t, &TokenUsage{
		ID:            "u-recent",
		SessionID:     "sess-recent",
		WorkspaceSlug: "ws-1",
		Model:         "gpt-4",
		InputTokens:   100,
		ReportedAt:    time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339Nano),
	})

	now := time.Now().UTC()

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/cost",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseCostJSON(t, rec)

	// Parse period timestamps.
	since, err := time.Parse(time.RFC3339, resp.Period.Since)
	if err != nil {
		t.Fatalf("failed to parse period.since %q: %v", resp.Period.Since, err)
	}
	until, err := time.Parse(time.RFC3339, resp.Period.Until)
	if err != nil {
		t.Fatalf("failed to parse period.until %q: %v", resp.Period.Until, err)
	}

	// until should be approximately now (within 5 seconds).
	if math.Abs(until.Sub(now).Seconds()) > 5 {
		t.Errorf("period.until = %v; want approximately %v", until, now)
	}

	// since should be approximately 24 hours ago (within 5 seconds).
	expectedSince := now.Add(-24 * time.Hour)
	if math.Abs(since.Sub(expectedSince).Seconds()) > 5 {
		t.Errorf("period.since = %v; want approximately %v", since, expectedSince)
	}
}

// 19-REQ-7.E1: group_by=day returns breakdown entries with 'date' field.
func TestWorkspaceCost_GroupByDay_DateField(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})
	env.seedTokenUsage(t, &TokenUsage{
		ID:            "u-1",
		SessionID:     "sess-1",
		WorkspaceSlug: "ws-1",
		Model:         "gpt-4",
		InputTokens:   100,
		ReportedAt:    "2026-09-01T12:00:00Z",
	})

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/cost?group_by=day&since=2026-09-01T00:00:00Z&until=2026-09-02T00:00:00Z",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseCostJSON(t, rec)
	if len(resp.Breakdown) == 0 {
		t.Fatal("breakdown is empty; want at least one entry")
	}
	for _, entry := range resp.Breakdown {
		if entry.Date == "" {
			t.Error("breakdown entry missing 'date' field")
		}
	}
}

// 19-REQ-7.E2: group_by=session returns breakdown entries with 'session_id'.
func TestWorkspaceCost_GroupBySession(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})
	env.seedTokenUsage(t, &TokenUsage{
		ID:            "u-1",
		SessionID:     "sess-1",
		WorkspaceSlug: "ws-1",
		Model:         "gpt-4",
		InputTokens:   100,
		ReportedAt:    "2026-09-01T12:00:00Z",
	})

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/cost?group_by=session&since=2026-09-01T00:00:00Z&until=2026-09-02T00:00:00Z",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseCostJSON(t, rec)
	for _, entry := range resp.Breakdown {
		if entry.SessionID == "" {
			t.Error("breakdown entry missing 'session_id' field for group_by=session")
		}
	}
}

// 19-REQ-7.E3: group_by=model returns breakdown entries with 'model'.
func TestWorkspaceCost_GroupByModel(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})
	env.seedTokenUsage(t, &TokenUsage{
		ID:            "u-1",
		SessionID:     "sess-1",
		WorkspaceSlug: "ws-1",
		Model:         "gpt-4",
		InputTokens:   100,
		ReportedAt:    "2026-09-01T12:00:00Z",
	})

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/cost?group_by=model&since=2026-09-01T00:00:00Z&until=2026-09-02T00:00:00Z",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseCostJSON(t, rec)
	for _, entry := range resp.Breakdown {
		if entry.Model == "" {
			t.Error("breakdown entry missing 'model' field for group_by=model")
		}
	}
}

// 19-REQ-7.E4: No token_usage records in period returns zero totals and
// empty breakdown.
func TestWorkspaceCost_EmptyPeriod(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-empty/cost?since=2026-09-01T00:00:00Z&until=2026-09-02T00:00:00Z&group_by=day",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseCostJSON(t, rec)

	if resp.Totals.InputTokens != 0 {
		t.Errorf("totals.input_tokens = %d; want 0", resp.Totals.InputTokens)
	}
	if resp.Totals.OutputTokens != 0 {
		t.Errorf("totals.output_tokens = %d; want 0", resp.Totals.OutputTokens)
	}
	if resp.Totals.CacheReadTokens != 0 {
		t.Errorf("totals.cache_read_tokens = %d; want 0", resp.Totals.CacheReadTokens)
	}
	if resp.Totals.Sessions != 0 {
		t.Errorf("totals.sessions = %d; want 0", resp.Totals.Sessions)
	}
	if len(resp.Breakdown) != 0 {
		t.Errorf("breakdown length = %d; want 0", len(resp.Breakdown))
	}
}

// 19-REQ-7.E5: Admin can access cost data for any workspace.
func TestWorkspaceCost_AdminAccess(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/any-ws/cost?since=2026-09-01T00:00:00Z&until=2026-09-02T00:00:00Z&group_by=day",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// 19-REQ-7.E6: Invalid group_by value returns 400.
func TestWorkspaceCost_InvalidGroupBy(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/cost?group_by=invalid&since=2026-09-01T00:00:00Z&until=2026-09-02T00:00:00Z",
		"", adminAuth())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
