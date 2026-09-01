package audit

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 2.1 — Single audit event ingestion (POST events)
// Requirements: 17-REQ-6, 17-REQ-5, 17-REQ-23
// Test Spec: TS-17-17, TS-17-18, TS-17-19, TS-17-20
// ---------------------------------------------------------------------------

// TS-17-17: POST events handler inserts a valid audit event and returns
// HTTP 201 with the acknowledgement body.
func TestPostEvent_Created201(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"event_type":"session.start","payload":{}}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, apiKeyAuth())

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	resp := parseJSONMap(t, rec)

	// Verify response fields per spec.
	id, _ := resp["id"].(string)
	if !isValidUUID(id) {
		t.Errorf("id = %q, want valid UUID", id)
	}
	if got := resp["run_id"]; got != testRunID {
		t.Errorf("run_id = %v, want %q", got, testRunID)
	}
	if got := resp["event_type"]; got != "session.start" {
		t.Errorf("event_type = %v, want %q", got, "session.start")
	}
	if got := resp["severity"]; got != "info" {
		t.Errorf("severity = %v, want %q", got, "info")
	}
	if got, ok := resp["created_at"].(string); !ok || got == "" {
		t.Error("created_at should be a non-empty timestamp string")
	}

	// Verify DB state: exactly one row in agent_audit_events.
	if n := queryTableCount(t, env.db, "agent_audit_events"); n != 1 {
		t.Errorf("agent_audit_events row count = %d, want 1", n)
	}
}

// TS-17-18: POST events handler returns HTTP 200 with the same
// acknowledgement shape when an event with the same id is submitted again.
func TestPostEvent_Duplicate200(t *testing.T) {
	env := newAuditTestEnv(t)

	dupID := "550e8400-e29b-41d4-a716-446655440000"

	// Pre-insert a record with the known ID.
	_, err := env.db.Exec(
		`INSERT INTO agent_audit_events (id, run_id, event_type, severity, ingested_at)
		 VALUES (?, ?, 'session.start', 'info', '2026-09-01T00:00:00Z')`,
		dupID, testRunID,
	)
	if err != nil {
		t.Fatalf("pre-insert: %v", err)
	}

	// Submit the same event again via HTTP.
	body := fmt.Sprintf(`{"id":%q,"event_type":"session.start"}`, dupID)
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (duplicate should return 200)", rec.Code, http.StatusOK)
	}

	resp := parseJSONMap(t, rec)
	if got := resp["id"]; got != dupID {
		t.Errorf("id = %v, want %q", got, dupID)
	}

	// Still only one row.
	if n := queryTableCount(t, env.db, "agent_audit_events"); n != 1 {
		t.Errorf("agent_audit_events row count = %d, want 1", n)
	}
}

// TS-17-19: POST events handler accepts and stores events with unknown
// event_type, logging a warning.
func TestPostEvent_UnknownEventType201(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"event_type":"custom.unknown.type"}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, apiKeyAuth())

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	resp := parseJSONMap(t, rec)
	if got := resp["event_type"]; got != "custom.unknown.type" {
		t.Errorf("event_type = %v, want %q", got, "custom.unknown.type")
	}

	// Verify it was stored.
	var count int
	err := env.db.QueryRow(
		"SELECT COUNT(*) FROM agent_audit_events WHERE event_type = ?",
		"custom.unknown.type",
	).Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("agent_audit_events count for custom.unknown.type = %d, want 1", count)
	}
}

// TS-17-20: POST events handler returns HTTP 400 when severity is a
// non-empty invalid value.
func TestPostEvent_InvalidSeverity400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"event_type":"session.start","severity":"urgent"}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "severity") {
		t.Errorf("error message = %q, want mention of 'severity'", errResp.Error.Message)
	}
}

// 17-REQ-23: POST events handler applies defaultSeverityFor when severity
// is absent from the request body.
func TestPostEvent_DefaultSeverityApplied(t *testing.T) {
	env := newAuditTestEnv(t)

	// session.fail should default to severity "error".
	body := `{"event_type":"session.fail"}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, apiKeyAuth())

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	resp := parseJSONMap(t, rec)
	if got := resp["severity"]; got != "error" {
		t.Errorf("severity = %v, want %q (defaultSeverityFor session.fail)", got, "error")
	}
}

// 17-REQ-5.2 / 17-REQ-5.E1: POST events handler rejects run_id with
// uppercase hex characters.
func TestPostEvent_InvalidRunID400(t *testing.T) {
	env := newAuditTestEnv(t)

	// URL contains uppercase hex in run_id suffix.
	badPath := "/api/v1/workspaces/ws1/runs/20260704_143022_A1B2C3/events"
	body := `{"event_type":"session.start"}`
	rec := env.doJSON(t, http.MethodPost, badPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// 17-REQ-6.E1: POST events handler rejects request when event_type is
// missing from the request body.
func TestPostEvent_MissingEventType400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"payload":{}}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "event_type") {
		t.Errorf("error message = %q, want mention of 'event_type'", errResp.Error.Message)
	}
}

// 17-REQ-6.E2: POST events handler rejects request when payload is not a
// JSON object.
func TestPostEvent_InvalidPayload400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"event_type":"session.start","payload":"not an object"}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "payload") {
		t.Errorf("error message = %q, want mention of 'payload'", errResp.Error.Message)
	}
}

// 17-REQ-6.E5: POST events handler rejects request when id is present but
// not a valid UUID format.
func TestPostEvent_InvalidUUID400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"id":"not-a-uuid","event_type":"session.start"}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// 17-REQ-5.3: POST events handler rejects when body run_id mismatches URL
// run_id.
func TestPostEvent_RunIDMismatch400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"event_type":"session.start","run_id":"20260101_000000_000000"}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "mismatch") {
		t.Errorf("error message = %q, want mention of 'mismatch'", errResp.Error.Message)
	}
}

// 17-REQ-5.E3: POST events handler accepts when body run_id field is absent;
// uses URL path run_id as the authoritative value.
func TestPostEvent_RunIDAbsentFromBody(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"event_type":"session.start"}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, apiKeyAuth())

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	resp := parseJSONMap(t, rec)
	if got := resp["run_id"]; got != testRunID {
		t.Errorf("run_id = %v, want %q (from URL path)", got, testRunID)
	}
}

// ---------------------------------------------------------------------------
// 2.2 — Session outcome ingestion (POST sessions/outcomes)
// Requirements: 17-REQ-8
// Test Spec: TS-17-25, TS-17-26
// ---------------------------------------------------------------------------

// TS-17-25: POST sessions/outcomes handler inserts a valid session outcome
// and returns HTTP 201 with the acknowledgement body.
func TestPostSessionOutcome_Created201(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"session_id":"sess-1","status":"completed","timestamp":"2026-09-01T13:00:00Z"}`
	rec := env.doJSON(t, http.MethodPost, outcomesPath, body, apiKeyAuth())

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	resp := parseJSONMap(t, rec)

	id, _ := resp["id"].(string)
	if !isValidUUID(id) {
		t.Errorf("id = %q, want valid UUID", id)
	}
	if got := resp["run_id"]; got != testRunID {
		t.Errorf("run_id = %v, want %q", got, testRunID)
	}
	if got := resp["status"]; got != "completed" {
		t.Errorf("status = %v, want %q", got, "completed")
	}
	if got, ok := resp["created_at"].(string); !ok || got == "" {
		t.Error("created_at should be a non-empty timestamp string")
	}

	if n := queryTableCount(t, env.db, "session_outcomes"); n != 1 {
		t.Errorf("session_outcomes row count = %d, want 1", n)
	}
}

// TS-17-26: POST sessions/outcomes handler returns HTTP 200 when a
// duplicate id is submitted.
func TestPostSessionOutcome_Duplicate200(t *testing.T) {
	env := newAuditTestEnv(t)

	dupID := "550e8400-e29b-41d4-a716-446655440001"

	// Pre-insert.
	_, err := env.db.Exec(
		`INSERT INTO session_outcomes (id, run_id, session_id, status, ingested_at)
		 VALUES (?, ?, 'sess-1', 'completed', '2026-09-01T00:00:00Z')`,
		dupID, testRunID,
	)
	if err != nil {
		t.Fatalf("pre-insert: %v", err)
	}

	body := fmt.Sprintf(`{"id":%q,"session_id":"sess-1","status":"completed","timestamp":"2026-09-01T13:00:00Z"}`, dupID)
	rec := env.doJSON(t, http.MethodPost, outcomesPath, body, apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (duplicate)", rec.Code, http.StatusOK)
	}

	if n := queryTableCount(t, env.db, "session_outcomes"); n != 1 {
		t.Errorf("session_outcomes row count = %d, want 1", n)
	}
}

// 17-REQ-8.E2: POST sessions/outcomes handler rejects request when status
// field is missing.
func TestPostSessionOutcome_MissingStatus400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"session_id":"sess-1","timestamp":"2026-09-01T13:00:00Z"}`
	rec := env.doJSON(t, http.MethodPost, outcomesPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "status") {
		t.Errorf("error message = %q, want mention of 'status'", errResp.Error.Message)
	}
}

// 17-REQ-8.E1: POST sessions/outcomes handler rejects request when
// session_id field is missing.
func TestPostSessionOutcome_MissingSessionID400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"status":"completed","timestamp":"2026-09-01T13:00:00Z"}`
	rec := env.doJSON(t, http.MethodPost, outcomesPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "session_id") {
		t.Errorf("error message = %q, want mention of 'session_id'", errResp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// 2.3 — Tool call and tool error ingestion
// Requirements: 17-REQ-9, 17-REQ-10
// Test Spec: TS-17-27, TS-17-28, TS-17-29, TS-17-30
// ---------------------------------------------------------------------------

// TS-17-27: POST tools/calls handler inserts a valid tool call and returns
// HTTP 201 with the acknowledgement body.
func TestPostToolCall_Created201(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"tool_name":"bash","timestamp":"2026-09-01T13:00:00Z"}`
	rec := env.doJSON(t, http.MethodPost, callsPath, body, apiKeyAuth())

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	resp := parseJSONMap(t, rec)

	id, _ := resp["id"].(string)
	if !isValidUUID(id) {
		t.Errorf("id = %q, want valid UUID", id)
	}
	if got := resp["run_id"]; got != testRunID {
		t.Errorf("run_id = %v, want %q", got, testRunID)
	}
	if got := resp["tool_name"]; got != "bash" {
		t.Errorf("tool_name = %v, want %q", got, "bash")
	}
	if got, ok := resp["called_at"].(string); !ok || got == "" {
		t.Error("called_at should be a non-empty timestamp string")
	}

	if n := queryTableCount(t, env.db, "tool_calls"); n != 1 {
		t.Errorf("tool_calls row count = %d, want 1", n)
	}
}

// TS-17-28: POST tools/calls handler returns HTTP 200 when a duplicate id
// is submitted.
func TestPostToolCall_Duplicate200(t *testing.T) {
	env := newAuditTestEnv(t)

	dupID := "550e8400-e29b-41d4-a716-446655440002"

	_, err := env.db.Exec(
		`INSERT INTO tool_calls (id, run_id, tool_name, ingested_at)
		 VALUES (?, ?, 'bash', '2026-09-01T00:00:00Z')`,
		dupID, testRunID,
	)
	if err != nil {
		t.Fatalf("pre-insert: %v", err)
	}

	body := fmt.Sprintf(`{"id":%q,"tool_name":"bash","timestamp":"2026-09-01T13:00:00Z"}`, dupID)
	rec := env.doJSON(t, http.MethodPost, callsPath, body, apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (duplicate)", rec.Code, http.StatusOK)
	}

	if n := queryTableCount(t, env.db, "tool_calls"); n != 1 {
		t.Errorf("tool_calls row count = %d, want 1", n)
	}
}

// 17-REQ-9.E1: POST tools/calls handler rejects request when tool_name
// is missing.
func TestPostToolCall_MissingToolName400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"timestamp":"2026-09-01T13:00:00Z"}`
	rec := env.doJSON(t, http.MethodPost, callsPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "tool_name") {
		t.Errorf("error message = %q, want mention of 'tool_name'", errResp.Error.Message)
	}
}

// TS-17-29: POST tools/errors handler inserts a valid tool error and returns
// HTTP 201 with the acknowledgement body.
func TestPostToolError_Created201(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"tool_name":"bash","error_msg":"command not found","timestamp":"2026-09-01T13:00:00Z"}`
	rec := env.doJSON(t, http.MethodPost, errorsPath, body, apiKeyAuth())

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	resp := parseJSONMap(t, rec)

	id, _ := resp["id"].(string)
	if !isValidUUID(id) {
		t.Errorf("id = %q, want valid UUID", id)
	}
	if got := resp["run_id"]; got != testRunID {
		t.Errorf("run_id = %v, want %q", got, testRunID)
	}
	if got := resp["tool_name"]; got != "bash" {
		t.Errorf("tool_name = %v, want %q", got, "bash")
	}
	if got, ok := resp["failed_at"].(string); !ok || got == "" {
		t.Error("failed_at should be a non-empty timestamp string")
	}

	if n := queryTableCount(t, env.db, "tool_errors"); n != 1 {
		t.Errorf("tool_errors row count = %d, want 1", n)
	}
}

// TS-17-30: POST tools/errors handler returns HTTP 200 when a duplicate id
// is submitted.
func TestPostToolError_Duplicate200(t *testing.T) {
	env := newAuditTestEnv(t)

	dupID := "550e8400-e29b-41d4-a716-446655440003"

	_, err := env.db.Exec(
		`INSERT INTO tool_errors (id, run_id, tool_name, error_msg, ingested_at)
		 VALUES (?, ?, 'bash', 'fail', '2026-09-01T00:00:00Z')`,
		dupID, testRunID,
	)
	if err != nil {
		t.Fatalf("pre-insert: %v", err)
	}

	body := fmt.Sprintf(`{"id":%q,"tool_name":"bash","error_msg":"fail","timestamp":"2026-09-01T13:00:00Z"}`, dupID)
	rec := env.doJSON(t, http.MethodPost, errorsPath, body, apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (duplicate)", rec.Code, http.StatusOK)
	}

	if n := queryTableCount(t, env.db, "tool_errors"); n != 1 {
		t.Errorf("tool_errors row count = %d, want 1", n)
	}
}

// 17-REQ-10.E1: POST tools/errors handler rejects request when error_msg
// is missing.
func TestPostToolError_MissingErrorMsg400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"tool_name":"bash","timestamp":"2026-09-01T13:00:00Z"}`
	rec := env.doJSON(t, http.MethodPost, errorsPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "error_msg") {
		t.Errorf("error message = %q, want mention of 'error_msg'", errResp.Error.Message)
	}
}

// Task 2.3 additional: POST tools/errors handler rejects request when
// tool_name is missing.
func TestPostToolError_MissingToolName400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"error_msg":"command not found","timestamp":"2026-09-01T13:00:00Z"}`
	rec := env.doJSON(t, http.MethodPost, errorsPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------------------
// 2.4 — Single trace event ingestion (POST traces)
// Requirements: 17-REQ-11
// Test Spec: TS-17-31, TS-17-32, TS-17-33
// ---------------------------------------------------------------------------

// TS-17-31: POST traces handler inserts a valid trace event with a valid
// event_type and returns HTTP 201.
func TestPostTrace_Created201(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"event_type":"session.init","timestamp":"2026-09-01T13:00:00Z"}`
	rec := env.doJSON(t, http.MethodPost, tracesPath, body, apiKeyAuth())

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	resp := parseJSONMap(t, rec)

	id, _ := resp["id"].(string)
	if !isValidUUID(id) {
		t.Errorf("id = %q, want valid UUID", id)
	}
	if got := resp["run_id"]; got != testRunID {
		t.Errorf("run_id = %v, want %q", got, testRunID)
	}
	if got := resp["event_type"]; got != "session.init" {
		t.Errorf("event_type = %v, want %q", got, "session.init")
	}
	if got, ok := resp["timestamp"].(string); !ok || got == "" {
		t.Error("timestamp should be a non-empty string")
	}

	if n := queryTableCount(t, env.db, "agent_traces"); n != 1 {
		t.Errorf("agent_traces row count = %d, want 1", n)
	}
}

// TS-17-32: POST traces handler returns HTTP 400 when event_type is not
// one of the five valid trace event types.
func TestPostTrace_UnknownEventType400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"event_type":"unknown.type","timestamp":"2026-09-01T13:00:00Z"}`
	rec := env.doJSON(t, http.MethodPost, tracesPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "event_type") {
		t.Errorf("error message = %q, want mention of 'event_type'", errResp.Error.Message)
	}

	// No row should be inserted.
	if n := queryTableCount(t, env.db, "agent_traces"); n != 0 {
		t.Errorf("agent_traces row count = %d, want 0", n)
	}
}

// TS-17-33: POST traces handler returns HTTP 200 when a duplicate id is
// submitted.
func TestPostTrace_Duplicate200(t *testing.T) {
	env := newAuditTestEnv(t)

	dupID := "550e8400-e29b-41d4-a716-446655440004"

	_, err := env.db.Exec(
		`INSERT INTO agent_traces (id, run_id, event_type, ingested_at)
		 VALUES (?, ?, 'session.init', '2026-09-01T00:00:00Z')`,
		dupID, testRunID,
	)
	if err != nil {
		t.Fatalf("pre-insert: %v", err)
	}

	body := fmt.Sprintf(`{"id":%q,"event_type":"session.init","timestamp":"2026-09-01T13:00:00Z"}`, dupID)
	rec := env.doJSON(t, http.MethodPost, tracesPath, body, apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (duplicate)", rec.Code, http.StatusOK)
	}

	if n := queryTableCount(t, env.db, "agent_traces"); n != 1 {
		t.Errorf("agent_traces row count = %d, want 1", n)
	}
}

// 17-REQ-11.E1: POST traces handler rejects request when event_type is
// missing from the request body.
func TestPostTrace_MissingEventType400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"timestamp":"2026-09-01T13:00:00Z"}`
	rec := env.doJSON(t, http.MethodPost, tracesPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "event_type") {
		t.Errorf("error message = %q, want mention of 'event_type'", errResp.Error.Message)
	}
}

// 17-PROP-10: All five valid trace event_types are accepted.
func TestPostTrace_ValidEventTypes(t *testing.T) {
	validTypes := []string{
		"session.init",
		"assistant.message",
		"tool.use",
		"tool.error",
		"session.result",
	}

	for _, eventType := range validTypes {
		t.Run(eventType, func(t *testing.T) {
			env := newAuditTestEnv(t)

			body := fmt.Sprintf(`{"event_type":%q,"timestamp":"2026-09-01T13:00:00Z"}`, eventType)
			rec := env.doJSON(t, http.MethodPost, tracesPath, body, apiKeyAuth())

			if rec.Code != http.StatusCreated {
				t.Errorf("status = %d, want %d for event_type %q",
					rec.Code, http.StatusCreated, eventType)
			}

			resp := parseJSONMap(t, rec)
			if got := resp["event_type"]; got != eventType {
				t.Errorf("event_type = %v, want %q", got, eventType)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2.5 — Postmortem ingestion and retrieval
// Requirements: 17-REQ-13, 17-REQ-19
// Test Spec: TS-17-37, TS-17-38, TS-17-39, TS-17-40, TS-17-47, TS-17-48, TS-17-49
// ---------------------------------------------------------------------------

// validPostmortemBody returns a JSON string for a valid postmortem request.
func validPostmortemBody() string {
	return `{
		"run_status": "stalled",
		"started_at": "2026-09-01T12:00:00Z",
		"completed_at": "2026-09-01T13:00:00Z",
		"task_summary": {"total": 10, "completed": 5, "failed": 2, "pending": 3},
		"cost_summary": {"total_tokens": 1000, "total_cost_usd": 0.50}
	}`
}

// TS-17-37: POST postmortem handler inserts a valid postmortem and returns
// HTTP 201 with the acknowledgement body.
func TestPostPostmortem_Created201(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodPost, pmPath, validPostmortemBody(), apiKeyAuth())

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	resp := parseJSONMap(t, rec)

	if got := resp["run_id"]; got != testRunID {
		t.Errorf("run_id = %v, want %q", got, testRunID)
	}
	if got := resp["run_status"]; got != "stalled" {
		t.Errorf("run_status = %v, want %q", got, "stalled")
	}
	if got, ok := resp["created_at"].(string); !ok || got == "" {
		t.Error("created_at should be a non-empty timestamp string")
	}

	if n := queryTableCount(t, env.db, "postmortems"); n != 1 {
		t.Errorf("postmortems row count = %d, want 1", n)
	}
}

// TS-17-38: POST postmortem handler returns HTTP 200 when a postmortem for
// the run_id already exists.
func TestPostPostmortem_Duplicate200(t *testing.T) {
	env := newAuditTestEnv(t)

	// Pre-insert a postmortem.
	_, err := env.db.Exec(
		`INSERT INTO postmortems (run_id, workspace, run_status, started_at, completed_at,
		 task_summary, cost_summary, ingested_at)
		 VALUES (?, 'ws1', 'stalled', '2026-09-01T12:00:00Z', '2026-09-01T13:00:00Z',
		 '{"total":10}', '{"total_tokens":1000}', '2026-09-01T00:00:00Z')`,
		testRunID,
	)
	if err != nil {
		t.Fatalf("pre-insert: %v", err)
	}

	rec := env.doJSON(t, http.MethodPost, pmPath, validPostmortemBody(), apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (duplicate)", rec.Code, http.StatusOK)
	}

	if n := queryTableCount(t, env.db, "postmortems"); n != 1 {
		t.Errorf("postmortems row count = %d, want 1", n)
	}
}

// TS-17-39: POST postmortem handler returns HTTP 422 with error type
// unknown_schema_version when schema_version is not 1.
func TestPostPostmortem_UnknownSchemaVersion422(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{
		"schema_version": 2,
		"run_status": "stalled",
		"started_at": "2026-09-01T12:00:00Z",
		"completed_at": "2026-09-01T13:00:00Z",
		"task_summary": {"total": 10, "completed": 5, "failed": 2, "pending": 3},
		"cost_summary": {"total_tokens": 1000, "total_cost_usd": 0.50}
	}`
	rec := env.doJSON(t, http.MethodPost, pmPath, body, apiKeyAuth())

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if errResp.Error.ErrorType != "unknown_schema_version" {
		t.Errorf("error_type = %q, want %q", errResp.Error.ErrorType, "unknown_schema_version")
	}
}

// TS-17-40: POST postmortem handler stores session_history as-is without
// field-level validation.
func TestPostPostmortem_SessionHistoryStoredAsIs(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{
		"run_status": "stalled",
		"started_at": "2026-09-01T12:00:00Z",
		"completed_at": "2026-09-01T13:00:00Z",
		"task_summary": {"total": 10, "completed": 5, "failed": 2, "pending": 3},
		"cost_summary": {"total_tokens": 1000, "total_cost_usd": 0.50},
		"session_history": [{"node_id":"n1","unknown_field":"value"},{"completely_unknown":"data"}]
	}`
	rec := env.doJSON(t, http.MethodPost, pmPath, body, apiKeyAuth())

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}

	// Verify session_history was stored as-is.
	var storedHistory string
	err := env.db.QueryRow(
		"SELECT session_history FROM postmortems WHERE run_id = ?", testRunID,
	).Scan(&storedHistory)
	if err != nil {
		t.Fatalf("query stored session_history: %v", err)
	}

	if !strings.Contains(storedHistory, "unknown_field") {
		t.Errorf("stored session_history = %q, want to contain 'unknown_field'", storedHistory)
	}
	if !strings.Contains(storedHistory, "completely_unknown") {
		t.Errorf("stored session_history = %q, want to contain 'completely_unknown'", storedHistory)
	}
}

// 17-REQ-13.E1: POST postmortem handler rejects request when run_status
// is missing.
func TestPostPostmortem_MissingRunStatus400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{
		"started_at": "2026-09-01T12:00:00Z",
		"completed_at": "2026-09-01T13:00:00Z",
		"task_summary": {"total": 10, "completed": 5, "failed": 2, "pending": 3},
		"cost_summary": {"total_tokens": 1000, "total_cost_usd": 0.50}
	}`
	rec := env.doJSON(t, http.MethodPost, pmPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "run_status") {
		t.Errorf("error message = %q, want mention of 'run_status'", errResp.Error.Message)
	}
}

// 17-REQ-13.E3: POST postmortem handler rejects request when run_status
// is not one of the four accepted values.
func TestPostPostmortem_InvalidRunStatus400(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{
		"run_status": "unknown_status",
		"started_at": "2026-09-01T12:00:00Z",
		"completed_at": "2026-09-01T13:00:00Z",
		"task_summary": {"total": 10, "completed": 5, "failed": 2, "pending": 3},
		"cost_summary": {"total_tokens": 1000, "total_cost_usd": 0.50}
	}`
	rec := env.doJSON(t, http.MethodPost, pmPath, body, apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// 17-REQ-13.1: POST postmortem handler validates run_status accepts
// all four valid values.
func TestPostPostmortem_ValidRunStatuses(t *testing.T) {
	validStatuses := []string{"stalled", "block_limit", "cost_limit", "session_limit"}

	for _, status := range validStatuses {
		t.Run(status, func(t *testing.T) {
			env := newAuditTestEnv(t)

			body := fmt.Sprintf(`{
				"run_status": %q,
				"started_at": "2026-09-01T12:00:00Z",
				"completed_at": "2026-09-01T13:00:00Z",
				"task_summary": {"total": 10, "completed": 5, "failed": 2, "pending": 3},
				"cost_summary": {"total_tokens": 1000, "total_cost_usd": 0.50}
			}`, status)
			rec := env.doJSON(t, http.MethodPost, pmPath, body, apiKeyAuth())

			if rec.Code != http.StatusCreated {
				t.Errorf("status = %d, want %d for run_status %q",
					rec.Code, http.StatusCreated, status)
			}
		})
	}
}

// TS-17-47: GET postmortem handler returns the full postmortem record for
// a run that has one.
func TestGetPostmortem_Found200(t *testing.T) {
	env := newAuditTestEnv(t)

	// Pre-insert a full postmortem.
	_, err := env.db.Exec(
		`INSERT INTO postmortems (run_id, workspace, schema_version, run_status,
		 started_at, completed_at, task_summary, cost_summary, blocked_tasks,
		 session_history, ingested_at)
		 VALUES (?, 'ws1', 1, 'stalled',
		 '2026-09-01T12:00:00Z', '2026-09-01T13:00:00Z',
		 '{"total":10,"completed":5,"failed":2,"pending":3}',
		 '{"total_tokens":1000,"total_cost_usd":0.50}',
		 '[]', '[]', '2026-09-01T14:00:00Z')`,
		testRunID,
	)
	if err != nil {
		t.Fatalf("pre-insert: %v", err)
	}

	rec := env.doJSON(t, http.MethodGet, pmPath, "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	resp := parseJSONMap(t, rec)

	// Verify all required fields are present.
	requiredFields := []string{
		"run_id", "workspace", "schema_version", "run_status",
		"started_at", "completed_at", "task_summary", "cost_summary",
		"blocked_tasks", "session_history", "ingested_at",
	}
	for _, field := range requiredFields {
		if _, ok := resp[field]; !ok {
			t.Errorf("response missing required field %q", field)
		}
	}

	if got := resp["run_id"]; got != testRunID {
		t.Errorf("run_id = %v, want %q", got, testRunID)
	}
	if got := resp["run_status"]; got != "stalled" {
		t.Errorf("run_status = %v, want %q", got, "stalled")
	}

	// schema_version should be 1 (JSON numbers are float64 in Go).
	if sv, ok := resp["schema_version"].(float64); !ok || sv != 1 {
		t.Errorf("schema_version = %v, want 1", resp["schema_version"])
	}
}

// TS-17-48: GET postmortem handler returns HTTP 404 with error type
// postmortem_not_found when no postmortem exists for the run_id.
func TestGetPostmortem_NotFound404(t *testing.T) {
	env := newAuditTestEnv(t)

	rec := env.doJSON(t, http.MethodGet, pmPath, "", apiKeyAuth())

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if errResp.Error.ErrorType != "postmortem_not_found" {
		t.Errorf("error_type = %q, want %q", errResp.Error.ErrorType, "postmortem_not_found")
	}
}

// TS-17-49: GET postmortem handler does not accept or apply cursor, limit,
// since, until, or order parameters — single-record lookup only.
func TestGetPostmortem_IgnoresPaginationParams(t *testing.T) {
	env := newAuditTestEnv(t)

	// Pre-insert a postmortem.
	_, err := env.db.Exec(
		`INSERT INTO postmortems (run_id, workspace, schema_version, run_status,
		 started_at, completed_at, task_summary, cost_summary, blocked_tasks,
		 session_history, ingested_at)
		 VALUES (?, 'ws1', 1, 'stalled',
		 '2026-09-01T12:00:00Z', '2026-09-01T13:00:00Z',
		 '{"total":10}', '{"total_tokens":1000}', '[]', '[]', '2026-09-01T14:00:00Z')`,
		testRunID,
	)
	if err != nil {
		t.Fatalf("pre-insert: %v", err)
	}

	// Include all pagination params in query string.
	pathWithParams := pmPath + "?cursor=abc&limit=10&since=2026-01-01T00:00:00Z&until=2026-12-31T00:00:00Z&order=desc"
	rec := env.doJSON(t, http.MethodGet, pathWithParams, "", apiKeyAuth())

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	resp := parseJSONMap(t, rec)

	// Response should not contain pagination fields.
	if _, ok := resp["next_cursor"]; ok {
		t.Error("response should not contain 'next_cursor' field")
	}
	if _, ok := resp["has_more"]; ok {
		t.Error("response should not contain 'has_more' field")
	}
}

// 17-REQ-19.E1: GET postmortem handler validates run_id format.
func TestGetPostmortem_InvalidRunID400(t *testing.T) {
	env := newAuditTestEnv(t)

	badPath := "/api/v1/workspaces/ws1/runs/INVALID/postmortem"
	rec := env.doJSON(t, http.MethodGet, badPath, "", apiKeyAuth())

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
