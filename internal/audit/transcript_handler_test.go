package audit

import (
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// ===========================================================================
// Test infrastructure for transcript handler
// (GET /api/v1/workspaces/:slug/runs/:run_id/transcript)
// Requirements: 18-REQ-7
// ===========================================================================

// transcriptResponse is the JSON response body for the transcript endpoint.
type transcriptResponse struct {
	RunID    string              `json:"run_id"`
	NodeID   string              `json:"node_id"`
	Messages []transcriptMessage `json:"messages"`
}

// transcriptMessage represents a single message in the transcript.
type transcriptMessage struct {
	Role      string  `json:"role"`
	Content   string  `json:"content"`
	ToolName  *string `json:"tool_name"`
	Timestamp string  `json:"timestamp"`
}

const transcriptBasePath = "/api/v1/workspaces/ws-1/runs/run-uuid/transcript"

// newTranscriptTestEnv creates a test environment with the transcript route
// registered and agent_traces table available for seeding.
func newTranscriptTestEnv(t *testing.T) *auditTestEnv {
	t.Helper()
	duckDB := openTestAuditDB(t)
	initHandlerTestSchema(t, duckDB)
	store := NewStore(duckDB)

	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	RegisterAuditQueryRoutes(api, store, &mockSSEManager{})

	return &auditTestEnv{
		echo:  e,
		db:    duckDB,
		store: store,
	}
}

// seedTranscriptTrace inserts a trace event into agent_traces with full
// details for transcript testing, including node_id and data (JSON).
func seedTranscriptTrace(t *testing.T, db *sql.DB, id, runID, workspace, eventType, nodeID, dataJSON, timestamp string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO agent_traces (id, run_id, workspace, event_type, node_id, timestamp, data, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, runID, workspace, eventType, nodeID, timestamp, dataJSON, now,
	)
	if err != nil {
		t.Fatalf("seedTranscriptTrace(%q): %v", id, err)
	}
}

// ===========================================================================
// 4.1 — Transcript handler tests
// Requirements: 18-REQ-7.1, 18-REQ-7.2, 18-REQ-7.3, 18-REQ-7.E1–E5
// Test Spec: TS-18-34, TS-18-35, TS-18-36
// ===========================================================================

// TS-18-34: GET /api/v1/workspaces/:slug/runs/:run_id/transcript with node_id
// returns HTTP 200 with messages ordered by timestamp.
func TestTranscript_ReturnsOrderedMessages_TS18_34(t *testing.T) {
	env := newTranscriptTestEnv(t)

	// Seed 3 trace events at T1, T2, T3.
	seedTranscriptTrace(t, env.db, "t-1", "run-uuid", "ws-1", "assistant.message", "node-uuid",
		`{"content": "First message"}`, "2026-09-01T12:00:00Z")
	seedTranscriptTrace(t, env.db, "t-2", "run-uuid", "ws-1", "assistant.message", "node-uuid",
		`{"content": "Second message"}`, "2026-09-01T13:00:00Z")
	seedTranscriptTrace(t, env.db, "t-3", "run-uuid", "ws-1", "assistant.message", "node-uuid",
		`{"content": "Third message"}`, "2026-09-01T14:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		transcriptBasePath+"?node_id=node-uuid", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp transcriptResponse
	parseJSON(t, rec, &resp)

	if resp.RunID != "run-uuid" {
		t.Errorf("run_id = %q, want %q", resp.RunID, "run-uuid")
	}
	if resp.NodeID != "node-uuid" {
		t.Errorf("node_id = %q, want %q", resp.NodeID, "node-uuid")
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("messages count = %d, want 3", len(resp.Messages))
	}

	// Verify ascending timestamp order (18-PROP-6).
	for i := 0; i < len(resp.Messages)-1; i++ {
		if resp.Messages[i].Timestamp > resp.Messages[i+1].Timestamp {
			t.Errorf("messages not in ascending order: messages[%d].timestamp=%s > messages[%d].timestamp=%s",
				i, resp.Messages[i].Timestamp, i+1, resp.Messages[i+1].Timestamp)
		}
	}
}

// TS-18-35: Transcript handler maps event types to conversation roles.
// session.init→system, assistant.message→assistant, tool.use→tool_use,
// tool.error→tool_error. tool_name is populated for tool roles and null
// for others.
func TestTranscript_MapsRolesToEventTypes_TS18_35(t *testing.T) {
	env := newTranscriptTestEnv(t)

	// Seed one of each recognized event type.
	seedTranscriptTrace(t, env.db, "t-1", "run-uuid", "ws-1", "session.init", "node-uuid",
		`{"content": "You are a helpful assistant."}`, "2026-09-01T12:00:00Z")
	seedTranscriptTrace(t, env.db, "t-2", "run-uuid", "ws-1", "assistant.message", "node-uuid",
		`{"content": "Hello, how can I help?"}`, "2026-09-01T12:01:00Z")
	seedTranscriptTrace(t, env.db, "t-3", "run-uuid", "ws-1", "tool.use", "node-uuid",
		`{"content": "ls -la", "tool_name": "bash"}`, "2026-09-01T12:02:00Z")
	seedTranscriptTrace(t, env.db, "t-4", "run-uuid", "ws-1", "tool.error", "node-uuid",
		`{"content": "command not found", "tool_name": "bash"}`, "2026-09-01T12:03:00Z")

	rec := env.doRequest(t, http.MethodGet,
		transcriptBasePath+"?node_id=node-uuid", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp transcriptResponse
	parseJSON(t, rec, &resp)

	if len(resp.Messages) != 4 {
		t.Fatalf("messages count = %d, want 4", len(resp.Messages))
	}

	// Check role mappings.
	expectedRoles := []string{"system", "assistant", "tool_use", "tool_error"}
	for i, want := range expectedRoles {
		if resp.Messages[i].Role != want {
			t.Errorf("messages[%d].role = %q, want %q", i, resp.Messages[i].Role, want)
		}
	}

	// system and assistant should have nil tool_name.
	if resp.Messages[0].ToolName != nil {
		t.Errorf("system message: tool_name = %v, want nil", *resp.Messages[0].ToolName)
	}
	if resp.Messages[1].ToolName != nil {
		t.Errorf("assistant message: tool_name = %v, want nil", *resp.Messages[1].ToolName)
	}

	// tool_use and tool_error should have non-nil tool_name.
	if resp.Messages[2].ToolName == nil {
		t.Error("tool_use message: tool_name is nil, want non-nil")
	} else if *resp.Messages[2].ToolName != "bash" {
		t.Errorf("tool_use message: tool_name = %q, want %q", *resp.Messages[2].ToolName, "bash")
	}
	if resp.Messages[3].ToolName == nil {
		t.Error("tool_error message: tool_name is nil, want non-nil")
	} else if *resp.Messages[3].ToolName != "bash" {
		t.Errorf("tool_error message: tool_name = %q, want %q", *resp.Messages[3].ToolName, "bash")
	}

	// Verify content is populated.
	if resp.Messages[0].Content != "You are a helpful assistant." {
		t.Errorf("system content = %q, want %q", resp.Messages[0].Content, "You are a helpful assistant.")
	}
}

// TS-18-36: Transcript handler returns HTTP 200 with an empty messages array
// when no traces exist for the given run_id and node_id.
func TestTranscript_EmptyMessages_TS18_36(t *testing.T) {
	env := newTranscriptTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/runs/nonexistent-run/transcript?node_id=nonexistent-node",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp transcriptResponse
	parseJSON(t, rec, &resp)

	if resp.RunID != "nonexistent-run" {
		t.Errorf("run_id = %q, want %q", resp.RunID, "nonexistent-run")
	}
	if resp.NodeID != "nonexistent-node" {
		t.Errorf("node_id = %q, want %q", resp.NodeID, "nonexistent-node")
	}
	if len(resp.Messages) != 0 {
		t.Errorf("messages count = %d, want 0", len(resp.Messages))
	}
}

// 18-REQ-7.E1: Missing node_id query parameter returns HTTP 400 with
// message "node_id is required".
func TestTranscript_MissingNodeID_Returns400(t *testing.T) {
	env := newTranscriptTestEnv(t)

	// Request without node_id query parameter.
	rec := env.doRequest(t, http.MethodGet,
		transcriptBasePath, "", adminAuth())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)

	if errResp.Error.Message != "node_id is required" {
		t.Errorf("error message = %q, want %q", errResp.Error.Message, "node_id is required")
	}
}

// 18-REQ-7.E3: Unauthenticated request returns HTTP 401.
func TestTranscript_Unauthenticated_Returns401(t *testing.T) {
	env := newTranscriptTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		transcriptBasePath+"?node_id=node-uuid", "", nil)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

// 18-REQ-7.E4: Caller without audit:read scope returns HTTP 403.
func TestTranscript_MissingAuditReadScope_Returns403(t *testing.T) {
	env := newTranscriptTestEnv(t)

	auth := patAuth("user-1", "other:scope")
	rec := env.doRequest(t, http.MethodGet,
		transcriptBasePath+"?node_id=node-uuid", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected status 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// 18-REQ-7.E5: Unrecognized event types are skipped in the transcript.
func TestTranscript_SkipsUnrecognizedEventTypes(t *testing.T) {
	env := newTranscriptTestEnv(t)

	// Seed known and unknown event types.
	seedTranscriptTrace(t, env.db, "t-1", "run-uuid", "ws-1", "session.init", "node-uuid",
		`{"content": "System prompt"}`, "2026-09-01T12:00:00Z")
	seedTranscriptTrace(t, env.db, "t-2", "run-uuid", "ws-1", "unknown.event", "node-uuid",
		`{"content": "Unknown"}`, "2026-09-01T12:01:00Z")
	seedTranscriptTrace(t, env.db, "t-3", "run-uuid", "ws-1", "assistant.message", "node-uuid",
		`{"content": "Hello"}`, "2026-09-01T12:02:00Z")

	rec := env.doRequest(t, http.MethodGet,
		transcriptBasePath+"?node_id=node-uuid", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp transcriptResponse
	parseJSON(t, rec, &resp)

	// Should have only 2 messages (unknown event skipped).
	if len(resp.Messages) != 2 {
		t.Fatalf("messages count = %d, want 2 (unknown event should be skipped)", len(resp.Messages))
	}
	if resp.Messages[0].Role != "system" {
		t.Errorf("messages[0].role = %q, want %q", resp.Messages[0].Role, "system")
	}
	if resp.Messages[1].Role != "assistant" {
		t.Errorf("messages[1].role = %q, want %q", resp.Messages[1].Role, "assistant")
	}
}

// 18-REQ-7.1: A run_id that exists in a different workspace returns an
// empty messages array (not 404 or 403).
func TestTranscript_DifferentWorkspaceReturnsEmpty(t *testing.T) {
	env := newTranscriptTestEnv(t)

	// Seed traces for ws-2 (different from ws-1 in the URL path).
	seedTranscriptTrace(t, env.db, "t-1", "run-uuid", "ws-2", "assistant.message", "node-uuid",
		`{"content": "Should not appear"}`, "2026-09-01T12:00:00Z")

	rec := env.doRequest(t, http.MethodGet,
		transcriptBasePath+"?node_id=node-uuid", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp transcriptResponse
	parseJSON(t, rec, &resp)

	if len(resp.Messages) != 0 {
		t.Errorf("messages count = %d, want 0 (run exists in different workspace)", len(resp.Messages))
	}
}

// 18-REQ-7.E2: DuckDB query failure returns HTTP 500.
func TestTranscript_DBFailure_Returns500(t *testing.T) {
	env := newTranscriptTestEnv(t)

	// Close the DB to simulate a query failure.
	env.db.Close()

	rec := env.doRequest(t, http.MethodGet,
		transcriptBasePath+"?node_id=node-uuid", "", adminAuth())

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// 18-REQ-7.E4: PAT with audit:read scope is authorized for transcript.
func TestTranscript_AuditReadScopeAllowed(t *testing.T) {
	env := newTranscriptTestEnv(t)

	auth := patAuth("user-1", "audit:read")
	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws-1/runs/no-run/transcript?node_id=no-node", "", auth)

	// Should return 200 (not 403), even if empty.
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 for PAT with audit:read, got %d: %s",
			rec.Code, rec.Body.String())
	}
}

// Transcript filters by node_id: only traces with matching node_id are
// included.
func TestTranscript_FiltersbyNodeID(t *testing.T) {
	env := newTranscriptTestEnv(t)

	// Seed traces with different node_ids.
	seedTranscriptTrace(t, env.db, "t-1", "run-uuid", "ws-1", "assistant.message", "node-A",
		`{"content": "Message for node-A"}`, "2026-09-01T12:00:00Z")
	seedTranscriptTrace(t, env.db, "t-2", "run-uuid", "ws-1", "assistant.message", "node-B",
		`{"content": "Message for node-B"}`, "2026-09-01T12:01:00Z")
	seedTranscriptTrace(t, env.db, "t-3", "run-uuid", "ws-1", "assistant.message", "node-A",
		`{"content": "Another for node-A"}`, "2026-09-01T12:02:00Z")

	rec := env.doRequest(t, http.MethodGet,
		transcriptBasePath+"?node_id=node-A", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp transcriptResponse
	parseJSON(t, rec, &resp)

	if len(resp.Messages) != 2 {
		t.Fatalf("messages count = %d, want 2 (only node-A traces)", len(resp.Messages))
	}
}
