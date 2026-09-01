package audit

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 3.4 — GET sessions/outcomes, tools/calls, tools/errors, and traces query
// Requirements: 17-REQ-15, 17-REQ-16, 17-REQ-17, 17-REQ-18, 17-REQ-21
// Test Spec: TS-17-43, TS-17-44, TS-17-45, TS-17-46
// ---------------------------------------------------------------------------

// outcomesResponse is the JSON response body for GET sessions/outcomes.
type outcomesResponse struct {
	Outcomes   []map[string]any `json:"outcomes"`
	NextCursor *string          `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

// callsResponse is the JSON response body for GET tools/calls.
type callsResponse struct {
	Calls      []map[string]any `json:"calls"`
	NextCursor *string          `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

// toolErrorsResponse is the JSON response body for GET tools/errors.
type toolErrorsResponse struct {
	Errors     []map[string]any `json:"errors"`
	NextCursor *string          `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

// tracesResponse is the JSON response body for GET traces.
type tracesResponse struct {
	Traces     []map[string]any `json:"traces"`
	NextCursor *string          `json:"next_cursor"`
	HasMore    bool             `json:"has_more"`
}

// TS-17-43: GET sessions/outcomes handler returns paginated session outcomes.
func TestGetSessionOutcomes_BasicQuery(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 5 session outcomes.
	for i := 0; i < 5; i++ {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedSessionOutcome(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "completed", ts,
		)
	}

	rec := env.doJSON(t, http.MethodGet, outcomesPath+"?limit=10&order=asc", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp outcomesResponse
	parseJSON(t, rec, &resp)

	if len(resp.Outcomes) != 5 {
		t.Errorf("outcomes count = %d, want 5", len(resp.Outcomes))
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
	if resp.NextCursor != nil {
		t.Errorf("next_cursor = %v, want nil", resp.NextCursor)
	}
}

// TS-17-43: GET sessions/outcomes with status filter.
func TestGetSessionOutcomes_FilterByStatus(t *testing.T) {
	env := newAuditTestEnv(t)

	statuses := []string{"completed", "failed", "completed", "timeout", "completed"}
	for i, status := range statuses {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedSessionOutcome(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, status, ts,
		)
	}

	rec := env.doJSON(t, http.MethodGet,
		outcomesPath+"?status=completed&order=asc",
		"", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp outcomesResponse
	parseJSON(t, rec, &resp)

	if len(resp.Outcomes) != 3 {
		t.Errorf("outcomes count = %d, want 3 (only completed)", len(resp.Outcomes))
	}
	for _, o := range resp.Outcomes {
		if s, _ := o["status"].(string); s != "completed" {
			t.Errorf("outcome status = %q, want %q", s, "completed")
		}
	}
}

// 17-REQ-15.E1: GET sessions/outcomes returns empty array when no outcomes exist.
func TestGetSessionOutcomes_Empty(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet, outcomesPath+"?limit=10", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp outcomesResponse
	parseJSON(t, rec, &resp)

	if len(resp.Outcomes) != 0 {
		t.Errorf("outcomes count = %d, want 0", len(resp.Outcomes))
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
	if resp.NextCursor != nil {
		t.Errorf("next_cursor = %v, want nil", resp.NextCursor)
	}
}

// 17-REQ-15.E2: GET sessions/outcomes returns 400 for malformed cursor.
func TestGetSessionOutcomes_MalformedCursor400(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet,
		outcomesPath+"?cursor=bad_cursor!!!",
		"", apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TS-17-44: GET tools/calls handler returns paginated tool calls.
func TestGetToolCalls_BasicQuery(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 4 tool calls: 2 with tool_name=bash, 2 with tool_name=read.
	toolNames := []string{"bash", "read", "bash", "read"}
	for i, tn := range toolNames {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedToolCall(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, tn, ts,
		)
	}

	rec := env.doJSON(t, http.MethodGet, callsPath+"?tool_name=bash", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp callsResponse
	parseJSON(t, rec, &resp)

	if len(resp.Calls) != 2 {
		t.Errorf("calls count = %d, want 2", len(resp.Calls))
	}
	for _, c := range resp.Calls {
		if tn, _ := c["tool_name"].(string); tn != "bash" {
			t.Errorf("tool_name = %q, want %q", tn, "bash")
		}
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
}

// 17-REQ-16.E1: GET tools/calls returns empty array when no calls exist.
func TestGetToolCalls_Empty(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet, callsPath+"?limit=10", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp callsResponse
	parseJSON(t, rec, &resp)

	if len(resp.Calls) != 0 {
		t.Errorf("calls count = %d, want 0", len(resp.Calls))
	}
}

// 17-REQ-16.E2: GET tools/calls returns 400 for malformed cursor.
func TestGetToolCalls_MalformedCursor400(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet,
		callsPath+"?cursor=bad_cursor!!!",
		"", apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TS-17-45: GET tools/errors handler returns paginated tool errors.
func TestGetToolErrors_BasicQuery(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 3 tool errors.
	for i := 0; i < 3; i++ {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedToolError(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "bash", ts,
		)
	}

	rec := env.doJSON(t, http.MethodGet, errorsPath+"?limit=10", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp toolErrorsResponse
	parseJSON(t, rec, &resp)

	if len(resp.Errors) != 3 {
		t.Errorf("errors count = %d, want 3", len(resp.Errors))
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
}

// 17-REQ-17.E1: GET tools/errors returns empty array when no errors exist.
func TestGetToolErrors_Empty(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet, errorsPath+"?limit=10", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp toolErrorsResponse
	parseJSON(t, rec, &resp)

	if len(resp.Errors) != 0 {
		t.Errorf("errors count = %d, want 0", len(resp.Errors))
	}
}

// 17-REQ-17.E2: GET tools/errors returns 400 for malformed cursor.
func TestGetToolErrors_MalformedCursor400(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet,
		errorsPath+"?cursor=bad_cursor!!!",
		"", apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TS-17-46: GET traces handler returns paginated trace events.
func TestGetTraces_BasicQuery(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 6 traces: 2 with event_type=tool.use, 4 others.
	eventTypes := []string{
		"session.init", "assistant.message", "tool.use",
		"tool.error", "session.result", "tool.use",
	}
	for i, et := range eventTypes {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedTrace(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, et, ts,
		)
	}

	rec := env.doJSON(t, http.MethodGet, tracesPath+"?event_type=tool.use", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp tracesResponse
	parseJSON(t, rec, &resp)

	if len(resp.Traces) != 2 {
		t.Errorf("traces count = %d, want 2", len(resp.Traces))
	}
	for _, tr := range resp.Traces {
		if et, _ := tr["event_type"].(string); et != "tool.use" {
			t.Errorf("event_type = %q, want %q", et, "tool.use")
		}
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
}

// 17-REQ-18.E1: GET traces returns empty array when no traces exist.
func TestGetTraces_Empty(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet, tracesPath+"?limit=10", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp tracesResponse
	parseJSON(t, rec, &resp)

	if len(resp.Traces) != 0 {
		t.Errorf("traces count = %d, want 0", len(resp.Traces))
	}
}

// 17-REQ-18.E2: GET traces returns 400 for malformed cursor.
func TestGetTraces_MalformedCursor400(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet,
		tracesPath+"?cursor=bad_cursor!!!",
		"", apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TS-17-46: GET traces with cursor pagination across pages.
func TestGetTraces_CursorPagination(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert 10 traces.
	for i := 0; i < 10; i++ {
		ts := time.Date(2026, 9, 1, 13, i, 0, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedTrace(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, "session.init", ts,
		)
	}

	// Page 1: limit=3.
	rec1 := env.doJSON(t, http.MethodGet, tracesPath+"?limit=3&order=asc", "", apiKeyAuth())
	if rec1.Code != http.StatusOK {
		t.Fatalf("page 1: status = %d, want %d", rec1.Code, http.StatusOK)
	}
	var page1 tracesResponse
	parseJSON(t, rec1, &page1)

	if len(page1.Traces) != 3 {
		t.Fatalf("page 1: traces count = %d, want 3", len(page1.Traces))
	}
	if !page1.HasMore {
		t.Error("page 1: has_more = false, want true")
	}
	if page1.NextCursor == nil {
		t.Fatal("page 1: next_cursor is nil")
	}

	// Page 2.
	rec2 := env.doJSON(t, http.MethodGet,
		tracesPath+"?limit=3&order=asc&cursor="+*page1.NextCursor,
		"", apiKeyAuth())
	if rec2.Code != http.StatusOK {
		t.Fatalf("page 2: status = %d, want %d", rec2.Code, http.StatusOK)
	}
	var page2 tracesResponse
	parseJSON(t, rec2, &page2)

	if len(page2.Traces) != 3 {
		t.Fatalf("page 2: traces count = %d, want 3", len(page2.Traces))
	}
	if !page2.HasMore {
		t.Error("page 2: has_more = false, want true")
	}

	// Page 3.
	rec3 := env.doJSON(t, http.MethodGet,
		tracesPath+"?limit=3&order=asc&cursor="+*page2.NextCursor,
		"", apiKeyAuth())
	if rec3.Code != http.StatusOK {
		t.Fatalf("page 3: status = %d, want %d", rec3.Code, http.StatusOK)
	}
	var page3 tracesResponse
	parseJSON(t, rec3, &page3)

	if len(page3.Traces) != 3 {
		t.Fatalf("page 3: traces count = %d, want 3", len(page3.Traces))
	}
	if !page3.HasMore {
		t.Error("page 3: has_more = false, want true")
	}

	// Page 4 (last page, 1 trace left).
	rec4 := env.doJSON(t, http.MethodGet,
		tracesPath+"?limit=3&order=asc&cursor="+*page3.NextCursor,
		"", apiKeyAuth())
	if rec4.Code != http.StatusOK {
		t.Fatalf("page 4: status = %d, want %d", rec4.Code, http.StatusOK)
	}
	var page4 tracesResponse
	parseJSON(t, rec4, &page4)

	if len(page4.Traces) != 1 {
		t.Errorf("page 4: traces count = %d, want 1", len(page4.Traces))
	}
	if page4.HasMore {
		t.Error("page 4: has_more = true, want false")
	}

	// Verify no duplicates across all pages.
	allIDs := make(map[string]bool)
	for _, pages := range [][]map[string]any{page1.Traces, page2.Traces, page3.Traces, page4.Traces} {
		for _, tr := range pages {
			id, _ := tr["id"].(string)
			if allIDs[id] {
				t.Errorf("duplicate trace id %q across pages", id)
			}
			allIDs[id] = true
		}
	}
	if len(allIDs) != 10 {
		t.Errorf("total unique trace ids = %d, want 10", len(allIDs))
	}
}

// TS-17-44: GET tools/calls with tool_name filter returns only matching.
func TestGetToolCalls_FilterByToolName(t *testing.T) {
	env := newAuditTestEnv(t)

	toolNames := []string{"bash", "read", "bash", "write"}
	for i, tn := range toolNames {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedToolCall(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, tn, ts,
		)
	}

	rec := env.doJSON(t, http.MethodGet, callsPath+"?tool_name=bash", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp callsResponse
	parseJSON(t, rec, &resp)

	if len(resp.Calls) != 2 {
		t.Errorf("calls count = %d, want 2", len(resp.Calls))
	}
	for _, c := range resp.Calls {
		if tn, _ := c["tool_name"].(string); tn != "bash" {
			t.Errorf("tool_name = %q, want %q", tn, "bash")
		}
	}
}

// GET tools/errors with node_id filter.
func TestGetToolErrors_FilterByNodeID(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert errors with different node_ids directly.
	nodeIDs := []string{"node-1", "node-2", "node-1"}
	for i, nid := range nodeIDs {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err := env.db.Exec(
			`INSERT INTO tool_errors (id, run_id, workspace, tool_name, node_id, error_msg, timestamp, ingested_at)
			 VALUES (?, ?, ?, 'bash', ?, 'test error', ?, ?)`,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, nid, ts, now,
		)
		if err != nil {
			t.Fatalf("seed tool error: %v", err)
		}
	}

	rec := env.doJSON(t, http.MethodGet, errorsPath+"?node_id=node-1", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp toolErrorsResponse
	parseJSON(t, rec, &resp)

	if len(resp.Errors) != 2 {
		t.Errorf("errors count = %d, want 2", len(resp.Errors))
	}
}

// GET traces with event_type filter returns only matching.
func TestGetTraces_FilterByEventType(t *testing.T) {
	env := newAuditTestEnv(t)

	eventTypes := []string{
		"session.init", "assistant.message", "tool.use",
		"tool.error", "session.result", "tool.use",
	}
	for i, et := range eventTypes {
		ts := time.Date(2026, 9, 1, 13, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		env.seedTrace(t,
			fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			testRunID, testSlug, et, ts,
		)
	}

	rec := env.doJSON(t, http.MethodGet, tracesPath+"?event_type=tool.use", "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp tracesResponse
	parseJSON(t, rec, &resp)

	if len(resp.Traces) != 2 {
		t.Errorf("traces count = %d, want 2", len(resp.Traces))
	}
	for _, tr := range resp.Traces {
		if et, _ := tr["event_type"].(string); et != "tool.use" {
			t.Errorf("event_type = %q, want %q", et, "tool.use")
		}
	}
}
