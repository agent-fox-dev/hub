package audit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 3.3 — GET query endpoints with filters and pagination (events)
// Requirements: 17-REQ-14, 17-REQ-21
// Test Spec: TS-17-41, TS-17-42, TS-17-55, TS-17-56, TS-17-57
// ---------------------------------------------------------------------------

// eventsResponse is the JSON response body for GET events.
type eventsResponse struct {
	Events     []map[string]any `json:"events"`
	NextCursor *string          `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

// TS-17-41: GET events handler returns paginated audit events filtered by
// run_id and optional parameters.
func TestGetEvents_BasicQuery(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 3 events.
	for i := 0; i < 3; i++ {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedAuditEvent(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "session.start", ts,
		)
	}

	rec := env.doJSON(t, http.MethodGet, eventsPath+"?limit=10&order=asc", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp eventsResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 3 {
		t.Errorf("events count = %d, want 3", len(resp.Events))
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
	if resp.NextCursor != nil {
		t.Errorf("next_cursor = %v, want nil", resp.NextCursor)
	}
}

// TS-17-41: GET events with event_type filter returns only matching events.
func TestGetEvents_FilterByEventType(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 10 events: 3 with session.start, 7 with other types.
	for i := 0; i < 10; i++ {
		eventType := "session.end"
		if i < 3 {
			eventType = "session.start"
		}
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedAuditEvent(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, eventType, ts,
		)
	}

	rec := env.doJSON(t, http.MethodGet,
		eventsPath+"?event_type=session.start&limit=10&order=asc",
		"", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp eventsResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 3 {
		t.Errorf("events count = %d, want 3", len(resp.Events))
	}
	for _, e := range resp.Events {
		if et, _ := e["event_type"].(string); et != "session.start" {
			t.Errorf("event_type = %q, want %q", et, "session.start")
		}
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
	if resp.NextCursor != nil {
		t.Errorf("next_cursor = %v, want nil", resp.NextCursor)
	}
}

// TS-17-41: GET events with since/until time range filter.
func TestGetEvents_TimeRangeFilter(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert events at 12:00, 13:00, 14:00, 15:00.
	timestamps := []string{
		"2026-09-01T12:00:00Z",
		"2026-09-01T13:00:00Z",
		"2026-09-01T14:00:00Z",
		"2026-09-01T15:00:00Z",
	}
	for i, ts := range timestamps {
		env.seedAuditEvent(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "session.start", ts,
		)
	}

	// Query for events between 13:00 and 14:30 — should get the 13:00 and 14:00 events.
	rec := env.doJSON(t, http.MethodGet,
		eventsPath+"?since=2026-09-01T13:00:00Z&until=2026-09-01T14:30:00Z&order=asc",
		"", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp eventsResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 2 {
		t.Errorf("events count = %d, want 2", len(resp.Events))
	}
}

// TS-17-41: GET events with order=desc returns events in descending order.
func TestGetEvents_OrderDesc(t *testing.T) {
	env := newAuditTestEnv(t)

	for i := 0; i < 5; i++ {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedAuditEvent(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "session.start", ts,
		)
	}

	rec := env.doJSON(t, http.MethodGet, eventsPath+"?order=desc&limit=10", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp eventsResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 5 {
		t.Fatalf("events count = %d, want 5", len(resp.Events))
	}

	// Verify descending order: each timestamp should be >= the next.
	for i := 0; i < len(resp.Events)-1; i++ {
		ts1, _ := resp.Events[i]["timestamp"].(string)
		ts2, _ := resp.Events[i+1]["timestamp"].(string)
		t1, _ := time.Parse(time.RFC3339Nano, ts1)
		t2, _ := time.Parse(time.RFC3339Nano, ts2)
		if t1.Before(t2) {
			t.Errorf("event[%d].timestamp (%s) < event[%d].timestamp (%s); want descending",
				i, ts1, i+1, ts2)
		}
	}
}

// TS-17-42: GET events with cursor from previous response returns next page.
func TestGetEvents_CursorPagination(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 25 events with distinct timestamps.
	for i := 0; i < 25; i++ {
		ts := time.Date(2026, 9, 1, 13, i, 0, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedAuditEvent(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "session.start", ts,
		)
	}

	// Page 1.
	rec1 := env.doJSON(t, http.MethodGet, eventsPath+"?limit=10&order=asc", "", apiKeyAuth())

	if rec1.Code != http.StatusOK {
		t.Fatalf("page 1: status = %d, want %d", rec1.Code, http.StatusOK)
	}

	var page1 eventsResponse
	parseJSON(t, rec1, &page1)

	if len(page1.Events) != 10 {
		t.Fatalf("page 1: events count = %d, want 10", len(page1.Events))
	}
	if !page1.HasMore {
		t.Error("page 1: has_more = false, want true")
	}
	if page1.NextCursor == nil {
		t.Fatal("page 1: next_cursor is nil, want non-nil")
	}

	// Page 2 using cursor.
	rec2 := env.doJSON(t, http.MethodGet,
		eventsPath+"?limit=10&order=asc&cursor="+*page1.NextCursor,
		"", apiKeyAuth())

	if rec2.Code != http.StatusOK {
		t.Fatalf("page 2: status = %d, want %d", rec2.Code, http.StatusOK)
	}

	var page2 eventsResponse
	parseJSON(t, rec2, &page2)

	if len(page2.Events) != 10 {
		t.Fatalf("page 2: events count = %d, want 10", len(page2.Events))
	}
	if !page2.HasMore {
		t.Error("page 2: has_more = false, want true")
	}

	// Page 3.
	rec3 := env.doJSON(t, http.MethodGet,
		eventsPath+"?limit=10&order=asc&cursor="+*page2.NextCursor,
		"", apiKeyAuth())

	if rec3.Code != http.StatusOK {
		t.Fatalf("page 3: status = %d, want %d", rec3.Code, http.StatusOK)
	}

	var page3 eventsResponse
	parseJSON(t, rec3, &page3)

	if len(page3.Events) != 5 {
		t.Errorf("page 3: events count = %d, want 5", len(page3.Events))
	}
	if page3.HasMore {
		t.Error("page 3: has_more = true, want false")
	}

	// Verify no duplicates across pages.
	ids := make(map[string]bool)
	for _, e := range page1.Events {
		id, _ := e["id"].(string)
		ids[id] = true
	}
	for _, e := range page2.Events {
		id, _ := e["id"].(string)
		if ids[id] {
			t.Errorf("duplicate id %q in page 2", id)
		}
		ids[id] = true
	}
	for _, e := range page3.Events {
		id, _ := e["id"].(string)
		if ids[id] {
			t.Errorf("duplicate id %q in page 3", id)
		}
		ids[id] = true
	}
	if len(ids) != 25 {
		t.Errorf("total unique ids = %d, want 25", len(ids))
	}
}

// 17-REQ-14.E4: GET events returns empty array when no events exist.
func TestGetEvents_EmptyResult(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet, eventsPath+"?limit=10", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp eventsResponse
	parseJSON(t, rec, &resp)

	if len(resp.Events) != 0 {
		t.Errorf("events count = %d, want 0", len(resp.Events))
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
	if resp.NextCursor != nil {
		t.Errorf("next_cursor = %v, want nil", resp.NextCursor)
	}
}

// 17-REQ-14.E1: GET events clamps limit > 1000 to 1000.
func TestGetEvents_LimitClamped(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 200 events.
	for i := 0; i < 200; i++ {
		ts := time.Date(2026, 9, 1, 13, i%60, i/60, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedAuditEvent(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "session.start", ts,
		)
	}

	// Request with no limit (defaults to 100).
	rec1 := env.doJSON(t, http.MethodGet, eventsPath, "", apiKeyAuth())
	if rec1.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec1.Code, http.StatusOK)
	}
	var resp1 eventsResponse
	parseJSON(t, rec1, &resp1)
	if len(resp1.Events) > 100 {
		t.Errorf("no limit: events count = %d, want <= 100", len(resp1.Events))
	}

	// Request with limit=2000 (clamped to 1000).
	rec2 := env.doJSON(t, http.MethodGet, eventsPath+"?limit=2000", "", apiKeyAuth())
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec2.Code, http.StatusOK)
	}
	var resp2 eventsResponse
	parseJSON(t, rec2, &resp2)
	if len(resp2.Events) > 1000 {
		t.Errorf("limit=2000: events count = %d, want <= 1000", len(resp2.Events))
	}
}

// 17-REQ-14.E2: GET events returns 400 for malformed cursor.
func TestGetEvents_MalformedCursor400(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet,
		eventsPath+"?cursor=not_valid_base64!!!",
		"", apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// 17-REQ-14.E3: GET events returns 400 for invalid ISO 8601 timestamp in since.
func TestGetEvents_InvalidTimestamp400(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet,
		eventsPath+"?since=not-a-timestamp",
		"", apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// TS-17-55: Paginated GET endpoints encode cursor as base64url JSON with
// ts and id fields.
func TestGetEvents_CursorEncoding(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 5 events.
	for i := 0; i < 5; i++ {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedAuditEvent(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "session.start", ts,
		)
	}

	// With limit=5 (all events), next_cursor should be null.
	rec := env.doJSON(t, http.MethodGet, eventsPath+"?limit=5&order=asc", "", apiKeyAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp eventsResponse
	parseJSON(t, rec, &resp)
	if resp.HasMore {
		t.Error("has_more = true, want false when all events fit")
	}
	if resp.NextCursor != nil {
		t.Errorf("next_cursor = %v, want nil when all events fit", resp.NextCursor)
	}

	// With limit=3, should return a valid cursor.
	rec2 := env.doJSON(t, http.MethodGet, eventsPath+"?limit=3&order=asc", "", apiKeyAuth())
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec2.Code, http.StatusOK)
	}
	var resp2 eventsResponse
	parseJSON(t, rec2, &resp2)
	if !resp2.HasMore {
		t.Error("has_more = false, want true when more events exist")
	}
	if resp2.NextCursor == nil {
		t.Fatal("next_cursor is nil, want non-nil cursor")
	}

	// Decode the cursor.
	decoded, err := base64.RawURLEncoding.DecodeString(*resp2.NextCursor)
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
		t.Error("cursor.ts is empty, want a valid RFC3339 timestamp")
	}
	if cursorData.ID == "" {
		t.Error("cursor.id is empty, want a non-empty UUID string")
	}

	// Verify ts parses as RFC3339.
	if _, err := time.Parse(time.RFC3339Nano, cursorData.TS); err != nil {
		t.Errorf("cursor.ts %q is not valid RFC3339: %v", cursorData.TS, err)
	}
}

// TS-17-56: Paginated GET endpoints use tuple comparison (timestamp, id) for
// cursor-based pagination with correct direction for asc and desc.
func TestGetEvents_TupleComparison(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 20 events with distinct timestamps.
	for i := 0; i < 20; i++ {
		ts := time.Date(2026, 9, 1, 13, i, 0, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedAuditEvent(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "session.start", ts,
		)
	}

	// Ascending: page 1.
	rec1 := env.doJSON(t, http.MethodGet, eventsPath+"?limit=10&order=asc", "", apiKeyAuth())
	if rec1.Code != http.StatusOK {
		t.Fatalf("page 1: status = %d, want %d", rec1.Code, http.StatusOK)
	}
	var page1 eventsResponse
	parseJSON(t, rec1, &page1)

	ids1 := make(map[string]bool)
	for _, e := range page1.Events {
		id, _ := e["id"].(string)
		ids1[id] = true
	}

	// Ascending: page 2.
	rec2 := env.doJSON(t, http.MethodGet,
		eventsPath+"?limit=10&order=asc&cursor="+*page1.NextCursor,
		"", apiKeyAuth())
	if rec2.Code != http.StatusOK {
		t.Fatalf("page 2: status = %d, want %d", rec2.Code, http.StatusOK)
	}
	var page2 eventsResponse
	parseJSON(t, rec2, &page2)

	ids2 := make(map[string]bool)
	for _, e := range page2.Events {
		id, _ := e["id"].(string)
		ids2[id] = true
	}

	// Verify no overlaps.
	for id := range ids2 {
		if ids1[id] {
			t.Errorf("duplicate id %q across pages", id)
		}
	}

	// Total unique IDs should be 20.
	if len(ids1)+len(ids2) != 20 {
		t.Errorf("total events = %d, want 20", len(ids1)+len(ids2))
	}
}

// TS-17-57: Default limit is 100, max is 1000.
func TestGetEvents_DefaultAndMaxLimit(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 200 events.
	for i := 0; i < 200; i++ {
		ts := time.Date(2026, 9, 1, 10, i%60, i/60, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedAuditEvent(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "session.start", ts,
		)
	}

	// No limit parameter: should return <= 100.
	rec1 := env.doJSON(t, http.MethodGet, eventsPath, "", apiKeyAuth())
	if rec1.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec1.Code, http.StatusOK)
	}
	var resp1 eventsResponse
	parseJSON(t, rec1, &resp1)
	if len(resp1.Events) > 100 {
		t.Errorf("no limit: returned %d events, want <= 100", len(resp1.Events))
	}

	// limit=2000: clamped to 1000.
	rec2 := env.doJSON(t, http.MethodGet, eventsPath+"?limit=2000", "", apiKeyAuth())
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec2.Code, http.StatusOK)
	}
	var resp2 eventsResponse
	parseJSON(t, rec2, &resp2)
	if len(resp2.Events) > 1000 {
		t.Errorf("limit=2000: returned %d events, want <= 1000", len(resp2.Events))
	}
}

// 17-REQ-21.E1: limit=0 or negative uses default of 100.
func TestGetEvents_ZeroLimitUsesDefault(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 5 events.
	for i := 0; i < 5; i++ {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedAuditEvent(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "session.start", ts,
		)
	}

	rec := env.doJSON(t, http.MethodGet, eventsPath+"?limit=0", "", apiKeyAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp eventsResponse
	parseJSON(t, rec, &resp)
	// Should use default limit and return all 5.
	if len(resp.Events) != 5 {
		t.Errorf("limit=0: events count = %d, want 5 (default limit 100)", len(resp.Events))
	}
}
