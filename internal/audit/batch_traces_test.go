package audit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ---------------------------------------------------------------------------
// 3.2 — Batch trace event ingestion (POST traces/batch)
// Requirements: 17-REQ-12
// Test Spec: TS-17-34, TS-17-35, TS-17-36
// ---------------------------------------------------------------------------

// TS-17-34: POST traces/batch handler pre-validates all items including
// event_type, inserts valid ones in a single transaction, and returns HTTP 200
// with summary.
func TestPostTracesBatch_AllAccepted200(t *testing.T) {
	env := newAuditTestEnv(t)

	validTypes := []string{"session.init", "assistant.message", "tool.use"}
	var traces []map[string]any
	for i, et := range validTypes {
		traces = append(traces, map[string]any{
			"id":         fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			"event_type": et,
			"timestamp":  "2026-09-01T13:00:00Z",
		})
	}
	body, _ := json.Marshal(traces)
	rec := env.doJSON(t, http.MethodPost, tracesBatchPath, string(body), apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var result batchResult
	parseJSON(t, rec, &result)

	if result.Accepted != 3 {
		t.Errorf("accepted = %d, want 3", result.Accepted)
	}
	if len(result.Errors) != 0 {
		t.Errorf("errors = %v, want empty", result.Errors)
	}

	// Verify DB state.
	if n := queryTableCount(t, env.db, "agent_traces"); n != 3 {
		t.Errorf("agent_traces row count = %d, want 3", n)
	}
}

// 17-REQ-12.E1: POST traces/batch with an invalid event_type should list
// that item in the errors array.
func TestPostTracesBatch_InvalidEventType(t *testing.T) {
	env := newAuditTestEnv(t)

	traces := []map[string]any{
		{
			"id":         "550e8400-e29b-41d4-a716-446655440000",
			"event_type": "session.init",
			"timestamp":  "2026-09-01T13:00:00Z",
		},
		{
			"id":         "550e8400-e29b-41d4-a716-446655440001",
			"event_type": "invalid.unknown.type",
			"timestamp":  "2026-09-01T13:01:00Z",
		},
		{
			"id":         "550e8400-e29b-41d4-a716-446655440002",
			"event_type": "tool.use",
			"timestamp":  "2026-09-01T13:02:00Z",
		},
	}
	body, _ := json.Marshal(traces)
	rec := env.doJSON(t, http.MethodPost, tracesBatchPath, string(body), apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var result batchResult
	parseJSON(t, rec, &result)

	if result.Accepted != 2 {
		t.Errorf("accepted = %d, want 2", result.Accepted)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("errors count = %d, want 1", len(result.Errors))
	}
	if result.Errors[0].Index != 1 {
		t.Errorf("errors[0].index = %d, want 1", result.Errors[0].Index)
	}
}

// TS-17-35: POST traces/batch handler returns HTTP 413 when the batch array
// contains more than 1000 items.
func TestPostTracesBatch_ExceedsLimit413(t *testing.T) {
	env := newAuditTestEnv(t)

	var traces []map[string]any
	for i := 0; i < 1001; i++ {
		traces = append(traces, map[string]any{
			"id":         fmt.Sprintf("550e8400-e29b-41d4-a716-%012d", i),
			"event_type": "session.init",
		})
	}
	body, _ := json.Marshal(traces)
	rec := env.doJSON(t, http.MethodPost, tracesBatchPath, string(body), apiKeyAuth())

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}

	// No items should be inserted.
	if n := queryTableCount(t, env.db, "agent_traces"); n != 0 {
		t.Errorf("agent_traces row count = %d, want 0", n)
	}
}

// TS-17-36: POST traces/batch handler returns HTTP 400 when the batch array
// is empty.
func TestPostTracesBatch_EmptyArray400(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodPost, tracesBatchPath, "[]", apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// 17-REQ-12: POST traces/batch with all 5 valid trace event types.
func TestPostTracesBatch_AllValidEventTypes(t *testing.T) {
	env := newAuditTestEnv(t)

	validTypes := []string{
		"session.init",
		"assistant.message",
		"tool.use",
		"tool.error",
		"session.result",
	}

	var traces []map[string]any
	for i, et := range validTypes {
		traces = append(traces, map[string]any{
			"id":         fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			"event_type": et,
			"timestamp":  "2026-09-01T13:00:00Z",
		})
	}
	body, _ := json.Marshal(traces)
	rec := env.doJSON(t, http.MethodPost, tracesBatchPath, string(body), apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var result batchResult
	parseJSON(t, rec, &result)

	if result.Accepted != 5 {
		t.Errorf("accepted = %d, want 5", result.Accepted)
	}
	if len(result.Errors) != 0 {
		t.Errorf("errors = %v, want empty", result.Errors)
	}
}
