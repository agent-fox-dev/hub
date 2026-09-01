package audit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TS-19-52: Pagination cursors for GET /api/v1/sessions encode (started_at,
// id) tuples as URL-safe base64 without padding.
func TestCursorFormat_Sessions_TS19_52(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed multiple sessions.
	for i := 1; i <= 5; i++ {
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

	rec := env.doRequest(t, http.MethodGet, "/api/v1/sessions?limit=2", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseSessionListJSON(t, rec)

	if resp.NextCursor == nil {
		t.Fatal("next_cursor is nil; want a cursor string")
	}

	cursor := *resp.NextCursor

	// Cursor must be URL-safe base64 without padding.
	if strings.ContainsAny(cursor, "=+/") {
		t.Errorf("cursor %q contains padding or non-URL-safe chars (=, +, /)", cursor)
	}

	// Cursor must decode to a valid (started_at, id) tuple.
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("base64 decode cursor: %v", err)
	}

	// Parse the decoded cursor to verify it contains a timestamp and ID.
	var cursorData struct {
		TS string `json:"ts"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal(decoded, &cursorData); err != nil {
		t.Fatalf("unmarshal cursor JSON: %v (decoded: %q)", err, string(decoded))
	}

	if cursorData.TS == "" {
		t.Error("cursor.ts is empty; want a valid RFC3339 timestamp")
	}
	if cursorData.ID == "" {
		t.Error("cursor.id is empty; want a non-empty string")
	}

	// Verify timestamp parses as RFC3339.
	if _, err := time.Parse(time.RFC3339Nano, cursorData.TS); err != nil {
		t.Errorf("cursor.ts %q is not valid RFC3339: %v", cursorData.TS, err)
	}
}

// TS-19-53: GET /api/v1/sessions with a cursor and desc order filters results
// using (started_at, id) < (cursor_started_at, cursor_id) without duplicates.
func TestCursorPagination_DescOrder_TS19_53(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed 6 sessions with distinct timestamps.
	for i := 1; i <= 6; i++ {
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

	// Page 1 with desc order.
	rec1 := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions?limit=3&order=desc",
		"", adminAuth())

	if rec1.Code != http.StatusOK {
		t.Fatalf("page 1: status = %d; want %d", rec1.Code, http.StatusOK)
	}

	page1 := parseSessionListJSON(t, rec1)

	if len(page1.Sessions) != 3 {
		t.Fatalf("page 1: count = %d; want 3", len(page1.Sessions))
	}
	if page1.NextCursor == nil {
		t.Fatal("page 1: next_cursor is nil; want a cursor")
	}

	// Page 2 using cursor from page 1.
	rec2 := env.doRequest(t, http.MethodGet,
		fmt.Sprintf("/api/v1/sessions?limit=3&order=desc&cursor=%s", *page1.NextCursor),
		"", adminAuth())

	if rec2.Code != http.StatusOK {
		t.Fatalf("page 2: status = %d; want %d", rec2.Code, http.StatusOK)
	}

	page2 := parseSessionListJSON(t, rec2)

	if len(page2.Sessions) != 3 {
		t.Fatalf("page 2: count = %d; want 3", len(page2.Sessions))
	}

	// Verify no duplicates across pages.
	ids := make(map[string]bool)
	for _, s := range page1.Sessions {
		ids[s.ID] = true
	}
	for _, s := range page2.Sessions {
		if ids[s.ID] {
			t.Errorf("duplicate id %q found across pages", s.ID)
		}
		ids[s.ID] = true
	}

	// Total unique IDs should be 6.
	if len(ids) != 6 {
		t.Errorf("total unique ids = %d; want 6", len(ids))
	}

	// Page 1 sessions should have newer started_at than page 2.
	minPage1Time := page1.Sessions[len(page1.Sessions)-1].StartedAt
	maxPage2Time := page2.Sessions[0].StartedAt
	t1, _ := time.Parse(time.RFC3339Nano, minPage1Time)
	t2, _ := time.Parse(time.RFC3339Nano, maxPage2Time)
	if !t1.After(t2) {
		t.Errorf("page 1 min started_at (%s) should be after page 2 max started_at (%s)",
			minPage1Time, maxPage2Time)
	}
}

// TS-19-54: GET /api/v1/sessions with a cursor and asc order filters results
// using (started_at, id) > (cursor_started_at, cursor_id) without duplicates.
func TestCursorPagination_AscOrder_TS19_54(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed 6 sessions with distinct timestamps.
	for i := 1; i <= 6; i++ {
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

	// Page 1 with asc order.
	rec1 := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions?limit=3&order=asc",
		"", adminAuth())

	if rec1.Code != http.StatusOK {
		t.Fatalf("page 1: status = %d; want %d", rec1.Code, http.StatusOK)
	}

	page1 := parseSessionListJSON(t, rec1)

	if len(page1.Sessions) != 3 {
		t.Fatalf("page 1: count = %d; want 3", len(page1.Sessions))
	}
	if page1.NextCursor == nil {
		t.Fatal("page 1: next_cursor is nil; want a cursor")
	}

	// Page 2 using cursor from page 1.
	rec2 := env.doRequest(t, http.MethodGet,
		fmt.Sprintf("/api/v1/sessions?limit=3&order=asc&cursor=%s", *page1.NextCursor),
		"", adminAuth())

	if rec2.Code != http.StatusOK {
		t.Fatalf("page 2: status = %d; want %d", rec2.Code, http.StatusOK)
	}

	page2 := parseSessionListJSON(t, rec2)

	if len(page2.Sessions) != 3 {
		t.Fatalf("page 2: count = %d; want 3", len(page2.Sessions))
	}

	// Verify no duplicates across pages.
	ids := make(map[string]bool)
	for _, s := range page1.Sessions {
		ids[s.ID] = true
	}
	for _, s := range page2.Sessions {
		if ids[s.ID] {
			t.Errorf("duplicate id %q found across pages", s.ID)
		}
		ids[s.ID] = true
	}

	// Total unique IDs should be 6.
	if len(ids) != 6 {
		t.Errorf("total unique ids = %d; want 6", len(ids))
	}

	// Page 1 sessions should have older started_at than page 2 (asc order).
	maxPage1Time := page1.Sessions[len(page1.Sessions)-1].StartedAt
	minPage2Time := page2.Sessions[0].StartedAt
	t1, _ := time.Parse(time.RFC3339Nano, maxPage1Time)
	t2, _ := time.Parse(time.RFC3339Nano, minPage2Time)
	if !t2.After(t1) {
		t.Errorf("page 2 min started_at (%s) should be after page 1 max started_at (%s)",
			minPage2Time, maxPage1Time)
	}
}

// 19-REQ-14.E1: Last page sets next_cursor to null and has_more to false.
func TestCursorPagination_LastPage(t *testing.T) {
	env := newAuditTestEnv(t)

	// Seed 2 sessions.
	for i := 1; i <= 2; i++ {
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

	// Request with limit large enough to hold all sessions.
	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions?limit=10",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseSessionListJSON(t, rec)

	if resp.HasMore {
		t.Error("has_more = true; want false on last page")
	}
	if resp.NextCursor != nil {
		t.Errorf("next_cursor = %q; want nil on last page", *resp.NextCursor)
	}
}

// 19-REQ-14.E2: Invalid cursor returns 400.
func TestCursorPagination_InvalidCursor(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions?cursor=totally_invalid_cursor!!!",
		"", adminAuth())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// TS-19-52 (usage variant): Cursor for GET /api/v1/sessions/:id/usage
// encodes (reported_at, id) tuples.
func TestCursorFormat_Usage(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})

	// Seed multiple usage records.
	for i := 1; i <= 5; i++ {
		reported := time.Now().UTC().Add(time.Duration(-i) * time.Minute)
		env.seedTokenUsage(t, &TokenUsage{
			ID:            usageID(i),
			SessionID:     "sess-1",
			WorkspaceSlug: "ws-1",
			Model:         "gpt-4",
			InputTokens:   int64(i * 100),
			OutputTokens:  int64(i * 50),
			ReportedAt:    reported.Format(time.RFC3339Nano),
		})
	}

	rec := env.doRequest(t, http.MethodGet, "/api/v1/sessions/sess-1/usage?limit=2", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseUsageListJSON(t, rec)

	if resp.NextCursor == nil {
		t.Fatal("next_cursor is nil; want a cursor string")
	}

	cursor := *resp.NextCursor

	// Must be URL-safe base64 without padding.
	if strings.ContainsAny(cursor, "=+/") {
		t.Errorf("cursor %q contains padding or non-URL-safe chars", cursor)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("base64 decode cursor: %v", err)
	}

	var cursorData struct {
		TS string `json:"ts"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal(decoded, &cursorData); err != nil {
		t.Fatalf("unmarshal cursor JSON: %v (decoded: %q)", err, string(decoded))
	}

	if cursorData.TS == "" {
		t.Error("cursor.ts is empty; want a valid RFC3339 timestamp (reported_at)")
	}
	if cursorData.ID == "" {
		t.Error("cursor.id is empty; want a non-empty string")
	}
}
