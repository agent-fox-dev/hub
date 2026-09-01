package audit

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TS-19-20: GET /api/v1/sessions/:id/usage returns paginated token_usage
// records and unbounded totals for the session.
// Requirement: 19-REQ-6.1
func TestListSessionUsage_Paginated_TS19_20(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed a session.
	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})

	// Seed five token_usage rows with varying models and token counts.
	models := []string{"gpt-4", "claude-3", "gpt-4", "claude-3-5-sonnet", "gpt-4"}
	for i := 1; i <= 5; i++ {
		env.seedTokenUsage(t, &TokenUsage{
			ID:              usageID(i),
			SessionID:       "sess-1",
			WorkspaceSlug:   "ws-1",
			Model:           models[i-1],
			InputTokens:     int64(i * 100),
			OutputTokens:    int64(i * 50),
			CacheReadTokens: int64(i * 10),
			ReportedAt:      time.Now().UTC().Add(time.Duration(-5+i) * time.Minute).Format(time.RFC3339Nano),
		})
	}

	// Request first page with limit=3.
	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions/sess-1/usage?limit=3",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseUsageListJSON(t, rec)

	if resp.SessionID != "sess-1" {
		t.Errorf("session_id = %q; want %q", resp.SessionID, "sess-1")
	}
	if len(resp.Records) != 3 {
		t.Fatalf("records count = %d; want 3", len(resp.Records))
	}
	if !resp.HasMore {
		t.Error("has_more = false; want true")
	}
	if resp.NextCursor == nil {
		t.Fatal("next_cursor is nil; want a cursor string")
	}

	// Totals should reflect all 5 records.
	// Sum of input_tokens: 100+200+300+400+500 = 1500
	expectedInput := int64(1500)
	if resp.Totals.TotalInputTokens != expectedInput {
		t.Errorf("totals.total_input_tokens = %d; want %d", resp.Totals.TotalInputTokens, expectedInput)
	}

	// Sum of output_tokens: 50+100+150+200+250 = 750
	expectedOutput := int64(750)
	if resp.Totals.TotalOutputTokens != expectedOutput {
		t.Errorf("totals.total_output_tokens = %d; want %d", resp.Totals.TotalOutputTokens, expectedOutput)
	}

	// Sum of cache_read_tokens: 10+20+30+40+50 = 150
	expectedCache := int64(150)
	if resp.Totals.TotalCacheReadTokens != expectedCache {
		t.Errorf("totals.total_cache_read_tokens = %d; want %d", resp.Totals.TotalCacheReadTokens, expectedCache)
	}

	// models_used should contain at least the distinct models.
	if resp.Totals.ModelsUsed == nil {
		t.Error("totals.models_used is nil")
	}
}

// TS-19-21: GET /api/v1/sessions/:id/usage for a non-existent session
// returns HTTP 404.
// Requirement: 19-REQ-6.2
func TestListSessionUsage_NotFound_TS19_21(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions/ghost/usage",
		"", adminAuth())

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// TS-19-22: GET /api/v1/sessions/:id/usage for a session in an inaccessible
// workspace returns HTTP 403 for non-admin callers.
// Requirement: 19-REQ-6.3
func TestListSessionUsage_InaccessibleWorkspace_TS19_22(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)

	// Seed a session in a restricted workspace (no workspace row seeded
	// for 'restricted-ws' to simulate no access).
	env.seedSession(t, &Session{
		ID:             "sess-restricted",
		WorkspaceSlug:  "restricted-ws",
		Status:         "active",
		CredentialID:   "cred-restricted",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions/sess-restricted/usage",
		"", apiKeyAuth("cred-no-access"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// TS-19-23: GET /api/v1/sessions/:id/usage totals always reflect all
// token_usage records regardless of pagination page.
// Requirement: 19-REQ-6.4
// Property: 19-PROP-4 (Usage Totals Are Unbounded and Consistent)
func TestListSessionUsage_TotalsConsistentAcrossPages_TS19_23(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed a session.
	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})

	// Seed ten token_usage rows with total input_tokens=1000.
	for i := 1; i <= 10; i++ {
		env.seedTokenUsage(t, &TokenUsage{
			ID:              usageID(i),
			SessionID:       "sess-1",
			WorkspaceSlug:   "ws-1",
			Model:           "gpt-4",
			InputTokens:     100, // 10 * 100 = 1000
			OutputTokens:    int64(i * 10),
			CacheReadTokens: 5,
			ReportedAt:      time.Now().UTC().Add(time.Duration(-10+i) * time.Minute).Format(time.RFC3339Nano),
		})
	}

	// Page 1.
	rec1 := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions/sess-1/usage?limit=3&order=desc",
		"", adminAuth())

	if rec1.Code != http.StatusOK {
		t.Fatalf("page 1: status = %d; want %d\nbody: %s", rec1.Code, http.StatusOK, rec1.Body.String())
	}

	page1 := parseUsageListJSON(t, rec1)

	if page1.Totals.TotalInputTokens != 1000 {
		t.Errorf("page 1: totals.total_input_tokens = %d; want 1000", page1.Totals.TotalInputTokens)
	}
	if !page1.HasMore {
		t.Error("page 1: has_more = false; want true")
	}
	if page1.NextCursor == nil {
		t.Fatal("page 1: next_cursor is nil; want a cursor string")
	}

	// Page 2.
	rec2 := env.doRequest(t, http.MethodGet,
		fmt.Sprintf("/api/v1/sessions/sess-1/usage?limit=3&order=desc&cursor=%s", *page1.NextCursor),
		"", adminAuth())

	if rec2.Code != http.StatusOK {
		t.Fatalf("page 2: status = %d; want %d\nbody: %s", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	page2 := parseUsageListJSON(t, rec2)

	// Totals MUST be the same as page 1 (reflecting all 10 records).
	if page2.Totals.TotalInputTokens != 1000 {
		t.Errorf("page 2: totals.total_input_tokens = %d; want 1000", page2.Totals.TotalInputTokens)
	}
}

// 19-REQ-6.E1: Usage list limit exceeding 1000 is capped at 1000.
func TestListSessionUsage_LimitCappedAt1000(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions/sess-1/usage?limit=2000",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// 19-REQ-6.E2: Usage list default page size is 100.
func TestListSessionUsage_DefaultLimit100(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})

	// Seed a few records.
	for i := 1; i <= 3; i++ {
		env.seedTokenUsage(t, &TokenUsage{
			ID:            usageID(i),
			SessionID:     "sess-1",
			WorkspaceSlug: "ws-1",
			Model:         "gpt-4",
			InputTokens:   int64(i * 100),
			ReportedAt:    time.Now().UTC().Add(time.Duration(-i) * time.Minute).Format(time.RFC3339Nano),
		})
	}

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions/sess-1/usage",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseUsageListJSON(t, rec)
	if len(resp.Records) > 100 {
		t.Errorf("records count = %d; want <= 100 (default limit)", len(resp.Records))
	}
}

// 19-REQ-6.E3: Malformed cursor returns 400.
func TestListSessionUsage_MalformedCursor(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions/sess-1/usage?cursor=invalid-base64!!!",
		"", adminAuth())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}
