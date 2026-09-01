package audit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// ---------------------------------------------------------------------------
// 3.1 — Batch audit event ingestion (POST events/batch)
// Requirements: 17-REQ-7
// Test Spec: TS-17-21, TS-17-22, TS-17-23, TS-17-24
// ---------------------------------------------------------------------------

// batchResult represents the JSON response from batch ingestion endpoints.
type batchResult struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Errors     []struct {
		Index   int    `json:"index"`
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"errors"`
}

// TS-17-21: POST events/batch handler pre-validates all items, inserts valid
// ones in a single transaction, and returns HTTP 200 with
// accepted/duplicates/errors summary.
func TestPostEventsBatch_AllAccepted200(t *testing.T) {
	env := newAuditTestEnv(t)

	// Build an array of 5 valid events.
	var events []map[string]any
	for i := 0; i < 5; i++ {
		events = append(events, map[string]any{
			"id":         fmt.Sprintf("550e8400-e29b-41d4-a716-44665544%04d", i),
			"event_type": "session.start",
			"timestamp":  "2026-09-01T13:00:00Z",
			"payload":    map[string]any{},
		})
	}
	body, _ := json.Marshal(events)
	rec := env.doJSON(t, http.MethodPost, eventsBatchPath, string(body), apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var result batchResult
	parseJSON(t, rec, &result)

	if result.Accepted != 5 {
		t.Errorf("accepted = %d, want 5", result.Accepted)
	}
	if result.Duplicates != 0 {
		t.Errorf("duplicates = %d, want 0", result.Duplicates)
	}
	if len(result.Errors) != 0 {
		t.Errorf("errors = %v, want empty", result.Errors)
	}

	// Verify DB state.
	if n := queryTableCount(t, env.db, "agent_audit_events"); n != 5 {
		t.Errorf("agent_audit_events row count = %d, want 5", n)
	}
}

// TS-17-22: POST events/batch handler inserts only valid items and lists
// invalid items in the errors array without aborting the batch.
func TestPostEventsBatch_PartialInvalid(t *testing.T) {
	env := newAuditTestEnv(t)

	// Index 0: valid, index 1: missing event_type (invalid), index 2: valid.
	events := []map[string]any{
		{
			"id":         "550e8400-e29b-41d4-a716-446655440000",
			"event_type": "session.start",
			"timestamp":  "2026-09-01T13:00:00Z",
		},
		{
			"id":      "550e8400-e29b-41d4-a716-446655440001",
			"payload": map[string]any{},
			// missing event_type
		},
		{
			"id":         "550e8400-e29b-41d4-a716-446655440002",
			"event_type": "session.end",
			"timestamp":  "2026-09-01T13:01:00Z",
		},
	}
	body, _ := json.Marshal(events)
	rec := env.doJSON(t, http.MethodPost, eventsBatchPath, string(body), apiKeyAuth())

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

	// Verify DB state: only 2 valid items inserted.
	if n := queryTableCount(t, env.db, "agent_audit_events"); n != 2 {
		t.Errorf("agent_audit_events row count = %d, want 2", n)
	}
}

// TS-17-23: POST events/batch handler returns HTTP 413 when the batch array
// contains more than 1000 items.
func TestPostEventsBatch_ExceedsLimit413(t *testing.T) {
	env := newAuditTestEnv(t)

	// Create 1001 events.
	var events []map[string]any
	for i := 0; i < 1001; i++ {
		events = append(events, map[string]any{
			"id":         fmt.Sprintf("550e8400-e29b-41d4-a716-%012d", i),
			"event_type": "session.start",
		})
	}
	body, _ := json.Marshal(events)
	rec := env.doJSON(t, http.MethodPost, eventsBatchPath, string(body), apiKeyAuth())

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}

	// No items should be inserted.
	if n := queryTableCount(t, env.db, "agent_audit_events"); n != 0 {
		t.Errorf("agent_audit_events row count = %d, want 0", n)
	}
}

// TS-17-24: POST events/batch handler returns HTTP 400 when the batch array
// is empty.
func TestPostEventsBatch_EmptyArray400(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodPost, eventsBatchPath, "[]", apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// 17-REQ-7.E2: POST events/batch all 1000 items are duplicates.
func TestPostEventsBatch_AllDuplicates(t *testing.T) {
	env := newAuditTestEnv(t)

	// Pre-insert 2 events.
	dupIDs := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"550e8400-e29b-41d4-a716-446655440001",
	}
	for _, id := range dupIDs {
		_, err := env.db.Exec(
			`INSERT INTO agent_audit_events (id, run_id, workspace, event_type, severity, ingested_at)
			 VALUES (?, ?, 'ws1', 'session.start', 'info', '2026-09-01T00:00:00Z')`,
			id, testRunID,
		)
		if err != nil {
			t.Fatalf("pre-insert %s: %v", id, err)
		}
	}

	// Submit the same events as a batch.
	events := []map[string]any{
		{"id": dupIDs[0], "event_type": "session.start"},
		{"id": dupIDs[1], "event_type": "session.start"},
	}
	body, _ := json.Marshal(events)
	rec := env.doJSON(t, http.MethodPost, eventsBatchPath, string(body), apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var result batchResult
	parseJSON(t, rec, &result)

	if result.Accepted != 0 {
		t.Errorf("accepted = %d, want 0", result.Accepted)
	}
	if result.Duplicates != 2 {
		t.Errorf("duplicates = %d, want 2", result.Duplicates)
	}
	if len(result.Errors) != 0 {
		t.Errorf("errors = %v, want empty", result.Errors)
	}

	// Still only the 2 pre-inserted rows.
	if n := queryTableCount(t, env.db, "agent_audit_events"); n != 2 {
		t.Errorf("agent_audit_events row count = %d, want 2", n)
	}
}

// 17-REQ-7.E3: All items in the batch fail pre-validation.
func TestPostEventsBatch_AllInvalid(t *testing.T) {
	env := newAuditTestEnv(t)

	events := []map[string]any{
		{"payload": map[string]any{}}, // missing event_type
		{"payload": map[string]any{}}, // missing event_type
	}
	body, _ := json.Marshal(events)
	rec := env.doJSON(t, http.MethodPost, eventsBatchPath, string(body), apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var result batchResult
	parseJSON(t, rec, &result)

	if result.Accepted != 0 {
		t.Errorf("accepted = %d, want 0", result.Accepted)
	}
	if result.Duplicates != 0 {
		t.Errorf("duplicates = %d, want 0", result.Duplicates)
	}
	if len(result.Errors) != 2 {
		t.Errorf("errors count = %d, want 2", len(result.Errors))
	}

	// No items should be inserted.
	if n := queryTableCount(t, env.db, "agent_audit_events"); n != 0 {
		t.Errorf("agent_audit_events row count = %d, want 0", n)
	}
}
