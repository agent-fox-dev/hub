package audit

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// Each ingestion route stores one row with 201, the server's defaults filled
// in, and answers a replay of the same id with 200 and no second row.
func TestIngestion_StoresOnceAndReplays(t *testing.T) {
	env := newEnv(t)
	for _, c := range []struct {
		path, table, body string
		check             func(t *testing.T, m map[string]any)
	}{
		{"/events", "agent_audit_events", `{"id":"` + uuidN(1) + `","event_type":"session.fail","node_id":"n1","session_id":"s1","timestamp":"2026-09-01T10:00:00.5+02:00","payload":{"k":1}}`,
			func(t *testing.T, m map[string]any) {
				if m["severity"] != "error" || m["created_at"] != "2026-09-01T08:00:00.5Z" || m["run_id"] != runID {
					t.Errorf("event answer %v; want default severity error and the normalized timestamp", m)
				}
			}},
		{"/sessions/outcomes", "session_outcomes", `{"id":"` + uuidN(2) + `","session_id":"s1","node_id":"n1","status":"completed","duration_ms":12,"token_usage":{"in":1}}`,
			func(t *testing.T, m map[string]any) {
				if m["status"] != "completed" || m["node_id"] != "n1" || m["created_at"] == "" {
					t.Errorf("outcome answer %v", m)
				}
			}},
		{"/tools/calls", "tool_calls", `{"id":"` + uuidN(3) + `","tool_name":"bash","input":{"cmd":"ls"},"output":{"ok":true}}`,
			func(t *testing.T, m map[string]any) {
				if m["tool_name"] != "bash" || m["called_at"] == "" {
					t.Errorf("tool call answer %v", m)
				}
			}},
		{"/tools/errors", "tool_errors", `{"id":"` + uuidN(4) + `","tool_name":"bash","error_code":"E1","error_msg":"boom"}`,
			func(t *testing.T, m map[string]any) {
				if m["tool_name"] != "bash" || m["failed_at"] == "" {
					t.Errorf("tool error answer %v", m)
				}
			}},
		{"/traces", "agent_traces", `{"id":"` + uuidN(5) + `","event_type":"assistant.message","node_id":"n1","sequence":3,"data":{"content":"hi"}}`,
			func(t *testing.T, m map[string]any) {
				if m["event_type"] != "assistant.message" || m["timestamp"] == "" {
					t.Errorf("trace answer %v", m)
				}
			}},
		{"/postmortem", "postmortems", `{"run_status":"stalled","started_at":"2026-09-01T10:00:00Z","completed_at":"2026-09-01T11:00:00Z","task_summary":{"total":3},"cost_summary":{"usd":1},"session_history":[{"n":1}]}`,
			func(t *testing.T, m map[string]any) {
				if m["run_status"] != "stalled" || m["run_id"] != runID || m["created_at"] == "" {
					t.Errorf("postmortem answer %v", m)
				}
			}},
	} {
		t.Run(c.table, func(t *testing.T) {
			first := testutil.Object(t, env.must(t, http.StatusCreated, http.MethodPost, runPath+c.path, c.body, owner))
			c.check(t, first)
			env.must(t, http.StatusOK, http.MethodPost, runPath+c.path, c.body, owner)
			if n := env.count(t, c.table, "run_id = ? AND workspace = 'w'", runID); n != 1 {
				t.Errorf("%d rows in %s; want 1", n, c.table)
			}
		})
	}
	if n := env.count(t, "postmortems", "session_history = '[{\"n\":1}]'"); n != 1 {
		t.Error("session_history not stored as given")
	}
	generated := testutil.Object(t, env.must(t, http.StatusCreated, http.MethodPost, runPath+"/events", `{"event_type":"custom.thing"}`, owner))
	if ValidateUUID(fmt.Sprint(generated["id"])) != nil || generated["severity"] != "info" {
		t.Errorf("event without id or severity: %v; want a UUID and info", generated)
	}
	if got := counter(t, env.metrics.AuditEventsTotal, "agent", "session.fail"); got != 1 {
		t.Errorf("audit events counter %v; want 1 for the one new session.fail", got)
	}
}

// The ingestion routes validate the run id in the path and the body fields
// before storing anything.
func TestIngestion_ValidationErrors(t *testing.T) {
	env := newEnv(t)
	pm := func(fields string) string {
		return `{"run_status":"stalled","started_at":"2026-09-01T10:00:00Z","completed_at":"2026-09-01T11:00:00Z","task_summary":{"total":1},"cost_summary":{}` + fields + `}`
	}
	cases := []struct {
		name, path, body, msg string
		status                int
	}{
		{"bad run id", "/api/v1/workspaces/w/runs/not-a-run/events", `{"event_type":"x"}`, `invalid run_id format: "not-a-run" (expected YYYYMMDD_HHMMSS_6hexchars)`, 400},
		{"event body", runPath + "/events", `{`, "invalid request body", 400},
		{"event type missing", runPath + "/events", `{}`, "missing required field: event_type", 400},
		{"event run mismatch", runPath + "/events", `{"event_type":"x","run_id":"20260704_143022_ffffff"}`, "run_id mismatch between URL path and request body", 400},
		{"event bad id", runPath + "/events", `{"event_type":"x","id":"nope"}`, `invalid UUID format: "nope"`, 400},
		{"event bad timestamp", runPath + "/events", `{"event_type":"x","timestamp":"yesterday"}`, `invalid timestamp: "yesterday" (must be RFC 3339)`, 400},
		{"event bad severity", runPath + "/events", `{"event_type":"x","severity":"loud"}`, `invalid severity: "loud" (must be one of: info, warning, error, critical)`, 400},
		{"event payload not object", runPath + "/events", `{"event_type":"x","payload":[1]}`, "invalid payload: must be a JSON object", 400},
		{"outcome body", runPath + "/sessions/outcomes", `[]`, "invalid request body", 400},
		{"outcome session missing", runPath + "/sessions/outcomes", `{"status":"ok"}`, "missing required field: session_id", 400},
		{"outcome status missing", runPath + "/sessions/outcomes", `{"session_id":"s"}`, "missing required field: status", 400},
		{"outcome bad timestamp", runPath + "/sessions/outcomes", `{"session_id":"s","status":"ok","timestamp":"x"}`, `invalid timestamp: "x" (must be RFC 3339)`, 400},
		{"outcome bad id", runPath + "/sessions/outcomes", `{"session_id":"s","status":"ok","id":"1"}`, `invalid UUID format: "1"`, 400},
		{"call body", runPath + "/tools/calls", `[]`, "invalid request body", 400},
		{"call tool missing", runPath + "/tools/calls", `{}`, "missing required field: tool_name", 400},
		{"call bad timestamp", runPath + "/tools/calls", `{"tool_name":"t","timestamp":"x"}`, `invalid timestamp: "x" (must be RFC 3339)`, 400},
		{"call bad id", runPath + "/tools/calls", `{"tool_name":"t","id":"1"}`, `invalid UUID format: "1"`, 400},
		{"error body", runPath + "/tools/errors", `[]`, "invalid request body", 400},
		{"error tool missing", runPath + "/tools/errors", `{"error_msg":"m"}`, "missing required field: tool_name", 400},
		{"error msg missing", runPath + "/tools/errors", `{"tool_name":"t"}`, "missing required field: error_msg", 400},
		{"error bad timestamp", runPath + "/tools/errors", `{"tool_name":"t","error_msg":"m","timestamp":"x"}`, `invalid timestamp: "x" (must be RFC 3339)`, 400},
		{"error bad id", runPath + "/tools/errors", `{"tool_name":"t","error_msg":"m","id":"1"}`, `invalid UUID format: "1"`, 400},
		{"trace body", runPath + "/traces", `[]`, "invalid request body", 400},
		{"trace type missing", runPath + "/traces", `{}`, "missing required field: event_type", 400},
		{"trace type unknown", runPath + "/traces", `{"event_type":"tool.result"}`, `unknown trace event_type: "tool.result"`, 400},
		{"trace bad timestamp", runPath + "/traces", `{"event_type":"tool.use","timestamp":"x"}`, `invalid timestamp: "x" (must be RFC 3339)`, 400},
		{"trace bad id", runPath + "/traces", `{"event_type":"tool.use","id":"1"}`, `invalid UUID format: "1"`, 400},
		{"postmortem body", runPath + "/postmortem", `[]`, "invalid request body", 400},
		{"postmortem schema", runPath + "/postmortem", pm(`,"schema_version":2`), "unsupported schema_version", 422},
		{"postmortem status missing", runPath + "/postmortem", `{"task_summary":{},"cost_summary":{}}`, "missing required field: run_status", 400},
		{"postmortem status unknown", runPath + "/postmortem", `{"run_status":"done","task_summary":{},"cost_summary":{}}`, "invalid run_status: must be one of stalled, block_limit, cost_limit, session_limit", 400},
		{"postmortem task missing", runPath + "/postmortem", `{"run_status":"stalled","cost_summary":{}}`, "missing required field: task_summary", 400},
		{"postmortem cost missing", runPath + "/postmortem", `{"run_status":"stalled","task_summary":{}}`, "missing required field: cost_summary", 400},
		{"postmortem started missing", runPath + "/postmortem", `{"run_status":"stalled","task_summary":{},"cost_summary":{},"completed_at":"2026-09-01T11:00:00Z"}`, "missing required field: started_at", 400},
		{"postmortem completed missing", runPath + "/postmortem", `{"run_status":"stalled","task_summary":{},"cost_summary":{},"started_at":"2026-09-01T11:00:00Z"}`, "missing required field: completed_at", 400},
		{"postmortem bad started", runPath + "/postmortem", `{"run_status":"stalled","task_summary":{},"cost_summary":{},"started_at":"x","completed_at":"2026-09-01T11:00:00Z"}`, "invalid started_at: must be RFC 3339", 400},
		{"postmortem bad completed", runPath + "/postmortem", `{"run_status":"stalled","task_summary":{},"cost_summary":{},"started_at":"2026-09-01T11:00:00Z","completed_at":"x"}`, "invalid completed_at: must be RFC 3339", 400},
		{"postmortem total missing", runPath + "/postmortem", `{"run_status":"stalled","task_summary":{},"cost_summary":{},"started_at":"2026-09-01T10:00:00Z","completed_at":"2026-09-01T11:00:00Z"}`, "task_summary missing required field: total", 400},
		{"batch body", runPath + "/events/batch", `{}`, "invalid request body", 400},
		{"batch empty", runPath + "/events/batch", `[]`, "batch array must not be empty", 400},
		{"batch too large", runPath + "/events/batch", "[" + strings.Repeat(`{"event_type":"x"},`, 1000) + `{"event_type":"x"}]`, "batch exceeds maximum of 1000 items", 413},
		{"trace batch body", runPath + "/traces/batch", `{}`, "invalid request body", 400},
		{"trace batch empty", runPath + "/traces/batch", `[]`, "batch array must not be empty", 400},
		{"trace batch too large", runPath + "/traces/batch", "[" + strings.Repeat(`{"event_type":"tool.use"},`, 1000) + `{"event_type":"tool.use"}]`, "batch exceeds maximum of 1000 items", 413},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodPost, c.path, c.body, owner), c.status, c.msg)
		})
	}
	if rec := env.do(t, http.MethodPost, runPath+"/postmortem", pm(`,"schema_version":2`), owner); testutil.Error(t, rec).ErrorType != "unknown_schema_version" {
		t.Errorf("schema_version refusal type %q", testutil.Error(t, rec).ErrorType)
	}
	for _, status := range []string{"stalled", "block_limit", "cost_limit", "session_limit"} {
		path := "/api/v1/workspaces/w/runs/20260704_143022_" + fmt.Sprintf("%06x", len(status)) + "/postmortem"
		env.must(t, http.StatusCreated, http.MethodPost, path, strings.Replace(pm(""), "stalled", status, 1), owner)
	}
	for i, et := range []string{"session.init", "assistant.message", "tool.use", "tool.error", "session.result"} {
		env.must(t, http.StatusCreated, http.MethodPost, runPath+"/traces", fmt.Sprintf(`{"event_type":"%s","sequence":%d}`, et, i), owner)
	}
}

// The batch routes store the valid items in one transaction, count the
// duplicates and list each invalid item with its index without refusing the
// others.
func TestIngestion_BatchesAcceptValidItemsAndListErrors(t *testing.T) {
	env := newEnv(t)
	events := `[{"id":"` + uuidN(1) + `","event_type":"session.start"},{"event_type":"session.fail","payload":{"a":1}},
		{"event_type":""},{"id":"bad","event_type":"x"},{"event_type":"x","severity":"loud"},{"event_type":"x","timestamp":"soon"}]`
	resp := decode[BatchIngestResponse](t, env.must(t, http.StatusOK, http.MethodPost, runPath+"/events/batch", events, owner))
	if resp.Accepted != 2 || resp.Duplicates != 0 || len(resp.Errors) != 4 || resp.Errors[0].Index != 2 || resp.Errors[1].ID != "bad" ||
		resp.Errors[0].Message != "missing required field: event_type" || !strings.HasPrefix(resp.Errors[2].Message, "invalid severity") || !strings.HasPrefix(resp.Errors[3].Message, "invalid timestamp") {
		t.Errorf("events batch: %+v", resp)
	}
	if env.count(t, "agent_audit_events", "event_type = 'session.fail' AND severity = 'error'") != 1 {
		t.Error("default severity not applied in the batch")
	}
	again := decode[BatchIngestResponse](t, env.must(t, http.StatusOK, http.MethodPost, runPath+"/events/batch", `[{"id":"`+uuidN(1)+`","event_type":"session.start"}]`, owner))
	if again.Accepted != 0 || again.Duplicates != 1 || len(again.Errors) != 0 {
		t.Errorf("replayed batch: %+v", again)
	}
	if got := counter(t, env.metrics.AuditEventsTotal, "agent", "batch"); got != 2 {
		t.Errorf("batch counter %v; want 2", got)
	}
	invalid := decode[BatchIngestResponse](t, env.must(t, http.StatusOK, http.MethodPost, runPath+"/events/batch", `[{"event_type":""}]`, owner))
	if invalid.Accepted != 0 || len(invalid.Errors) != 1 {
		t.Errorf("all-invalid batch: %+v", invalid)
	}

	traces := `[{"id":"` + uuidN(2) + `","event_type":"tool.use","sequence":1,"data":{"tool_name":"bash"}},{"event_type":"session.result"},
		{"event_type":""},{"event_type":"nope"},{"id":"bad","event_type":"tool.use"},{"event_type":"tool.use","timestamp":"x"}]`
	tr := decode[BatchIngestResponse](t, env.must(t, http.StatusOK, http.MethodPost, runPath+"/traces/batch", traces, owner))
	if tr.Accepted != 2 || len(tr.Errors) != 4 || tr.Errors[1].Message != `unknown trace event_type: "nope"` || tr.Errors[2].ID != "bad" {
		t.Errorf("traces batch: %+v", tr)
	}
	trAgain := decode[BatchIngestResponse](t, env.must(t, http.StatusOK, http.MethodPost, runPath+"/traces/batch", `[{"id":"`+uuidN(2)+`","event_type":"tool.use"},{"event_type":""}]`, owner))
	if trAgain.Duplicates != 1 || trAgain.Accepted != 0 || len(trAgain.Errors) != 1 {
		t.Errorf("replayed traces batch: %+v", trAgain)
	}
	if env.count(t, "agent_traces", "") != 2 {
		t.Errorf("%d traces stored; want 2", env.count(t, "agent_traces", ""))
	}
}

// Writing audit data of a workspace takes audit:write and, unless the token
// is an admin token, an existing, non-archived workspace the caller owns; a
// token bound to another workspace sees it as missing.
func TestIngestion_WorkspaceAccess(t *testing.T) {
	env := newEnv(t)
	for _, c := range []struct {
		name, slug string
		auth       *apikit.AuthInfo
		status     int
		msg, typ   string
	}{
		{"unauthenticated", "w", nil, 401, "authentication required", ""},
		{"missing scope", "w", testutil.PAT("u1", "audit:read"), 403, "insufficient scope: audit:write required", ""},
		{"owner", "w", owner, 201, "", ""},
		{"api key of owner", "w", key, 201, "", ""},
		{"admin anywhere", "nope", admin, 201, "", ""},
		{"unknown workspace", "nope", owner, 404, "workspace not found", ""},
		{"archived", "w-arch", owner, 409, "workspace is archived", "workspace_archived"},
		{"member", "w", member, 403, "token owner does not have write access to this workspace", "workspace_access_denied"},
		{"stranger", "w", stranger, 403, "token owner does not have write access to this workspace", "workspace_access_denied"},
		{"bound elsewhere", "w-arch", bound, 404, "workspace not found", ""},
		{"bound to it", "w", bound, 201, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := env.do(t, http.MethodPost, "/api/v1/workspaces/"+c.slug+"/runs/"+runID+"/tools/calls", `{"tool_name":"t"}`, c.auth)
			expect(t, rec, c.status, c.msg)
			if c.typ != "" && testutil.Error(t, rec).ErrorType != c.typ {
				t.Errorf("error type %q; want %q", testutil.Error(t, rec).ErrorType, c.typ)
			}
		})
	}
	if n := env.count(t, "tool_calls", "workspace IN ('w-arch')"); n != 0 {
		t.Errorf("%d rows stored for refused requests", n)
	}
	testutil.Exec(t, env.sqlite, "DROP TABLE token_workspace_bindings")
	expect(t, env.do(t, http.MethodPost, runPath+"/events", `{"event_type":"x"}`, bound), 500, "internal server error")
	testutil.Exec(t, env.sqlite, "DROP TABLE workspaces")
	expect(t, env.do(t, http.MethodPost, runPath+"/events", `{"event_type":"x"}`, owner), 500, "failed to check workspace access")
}

// Without SQLite, admin tokens and API keys write anywhere while PATs are
// refused, since their workspace cannot be verified.
func TestIngestion_WithoutSQLiteRefusesPATs(t *testing.T) {
	env := newBareEnv(t)
	for _, c := range []struct {
		auth   *apikit.AuthInfo
		status int
	}{{admin, 201}, {key, 201}, {owner, 403}} {
		expect(t, env.do(t, http.MethodPost, runPath+"/events", `{"event_type":"x"}`, c.auth), c.status, "")
	}
}

// Every ingestion and per-run read route is registered under the run path
// with its method.
func TestIngestion_RoutesRegistered(t *testing.T) {
	env := newBareEnv(t)
	have := map[string]bool{}
	for _, r := range env.e.Routes() {
		have[r.Method+" "+r.Path] = true
	}
	base := "/api/v1/workspaces/:slug/runs/:run_id"
	for _, want := range []string{"POST /events", "GET /events", "POST /events/batch", "POST /sessions/outcomes", "GET /sessions/outcomes",
		"POST /tools/calls", "GET /tools/calls", "POST /tools/errors", "GET /tools/errors", "POST /traces", "GET /traces",
		"POST /traces/batch", "POST /postmortem", "GET /postmortem", "GET /transcript"} {
		method, path, _ := strings.Cut(want, " ")
		if !have[method+" "+base+path] {
			t.Errorf("route %s %s%s not registered", method, base, path)
		}
	}
	for _, want := range []string{"POST /api/v1/sessions", "POST /api/v1/sessions/:id/complete", "POST /api/v1/sessions/:id/usage",
		"GET /api/v1/sessions", "GET /api/v1/sessions/:id", "GET /api/v1/sessions/:id/usage", "GET /api/v1/workspaces/:slug/cost",
		"GET /api/v1/audit", "GET /api/v1/events"} {
		if !have[want] {
			t.Errorf("route %s not registered", want)
		}
	}
}
