package audit

import (
	"net/http"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// at is the RFC 3339 time i minutes into 2026-09-01T10:00Z.
func at(i int) string {
	return time.Date(2026, 9, 1, 10, i, 0, 0, time.UTC).Format(time.RFC3339)
}

// GET .../events lists the events of the run in the workspace, oldest first
// (100 at most by default, 1000 at most), filtered by type, severity, node,
// session and time, and paged by a (timestamp, id) cursor that keeps
// events sharing a timestamp apart.
func TestGetRunEvents_FiltersAndPages(t *testing.T) {
	env := newEnv(t)
	for i := 0; i < 5; i++ {
		env.seedAgentEvent(t, agentEvent{id: uuidN(i), ts: at(i)})
	}
	env.seedAgentEvent(t, agentEvent{id: uuidN(5), ts: at(4), eventType: "session.fail", severity: "error", nodeID: "n1", sessionID: "s1"})
	env.seedAgentEvent(t, agentEvent{id: uuidN(6), run: "20260704_143022_ffffff"})
	env.seedAgentEvent(t, agentEvent{id: uuidN(7), ws: "w-own"})
	get := func(query string) page {
		t.Helper()
		return listPage(t, env.must(t, http.StatusOK, http.MethodGet, runPath+"/events"+query, owner), "events")
	}
	all := get("")
	if len(all.Items) != 6 || all.HasMore || all.Items[0]["id"] != uuidN(0) || all.Items[5]["id"] != uuidN(5) {
		t.Errorf("default listing: %v has_more %v", ids(all.Items), all.HasMore)
	}
	if ev := all.Items[5]; ev["severity"] != "error" || ev["node_id"] != "n1" || ev["session_id"] != "s1" || ev["timestamp"] != at(4) || ev["workspace"] != "w" || ev["run_id"] != runID {
		t.Errorf("event fields %v", ev)
	}
	for query, want := range map[string][]string{
		"?event_type=session.fail":    {uuidN(5)},
		"?severity=error":             {uuidN(5)},
		"?node_id=n1":                 {uuidN(5)},
		"?session_id=s1":              {uuidN(5)},
		"?since=" + at(3):             {uuidN(3), uuidN(4), uuidN(5)},
		"?until=" + at(1):             {uuidN(0), uuidN(1)},
		"?since=" + at(1) + "&until=" + at(2): {uuidN(1), uuidN(2)},
		"?order=desc&limit=2":         {uuidN(5), uuidN(4)},
		"?limit=0":                    {uuidN(0), uuidN(1), uuidN(2), uuidN(3), uuidN(4), uuidN(5)},
	} {
		got := ids(get(query).Items)
		if len(got) != len(want) {
			t.Errorf("%s: %v; want %v", query, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: %v; want %v", query, got, want)
				break
			}
		}
	}
	first := get("?limit=4")
	if !first.HasMore || first.NextCursor == nil || len(first.Items) != 4 {
		t.Fatalf("first page: %v", first)
	}
	second := get("?limit=4&cursor=" + *first.NextCursor)
	if len(second.Items) != 2 || second.HasMore || second.Items[0]["id"] != uuidN(4) || second.Items[1]["id"] != uuidN(5) {
		t.Errorf("second page: %v; want the two events at %s", ids(second.Items), at(4))
	}
	desc := get("?order=desc&limit=1")
	if next := get("?order=desc&limit=1&cursor=" + *desc.NextCursor); next.Items[0]["id"] != uuidN(4) {
		t.Errorf("descending second page starts at %v; want %s", next.Items[0]["id"], uuidN(4))
	}
	if _, id, _ := decodeCursor(*first.NextCursor); id != uuidN(3) {
		t.Errorf("cursor names %s; want the last event of the page", id)
	}
	for name, query := range map[string]string{
		"malformed cursor": "?cursor=***",
		"cursor without time": "?cursor=" + encodeCursor("x", "y"),
		"bad since":        "?since=yesterday",
		"bad until":        "?until=tomorrow",
	} {
		t.Run(name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodGet, runPath+"/events"+query, "", owner), http.StatusBadRequest, "")
		})
	}
	if big := get("?limit=5000"); len(big.Items) != 6 {
		t.Errorf("clamped limit: %d events", len(big.Items))
	}
}

// The outcome, tool call, tool error and trace reads of a run list the rows
// of the workspace oldest first, with their own filters, and answer an empty
// list when there is nothing.
func TestGetRunRecords_ListsAndFilters(t *testing.T) {
	env := newEnv(t)
	env.seedRow(t, "session_outcomes", uuidN(1), at(1), map[string]any{"session_id": "s1", "node_id": "n1", "status": "completed", "duration_ms": 5, "token_usage": `{"in":1}`})
	env.seedRow(t, "session_outcomes", uuidN(2), at(2), map[string]any{"session_id": "s2", "node_id": "n2", "status": "failed"})
	env.seedRow(t, "tool_calls", uuidN(3), at(1), map[string]any{"tool_name": "bash", "node_id": "n1", "session_id": "s1", "input": `{"cmd":"ls"}`})
	env.seedRow(t, "tool_calls", uuidN(4), at(2), map[string]any{"tool_name": "read", "node_id": "n2", "session_id": "s2"})
	env.seedRow(t, "tool_errors", uuidN(5), at(1), map[string]any{"tool_name": "bash", "node_id": "n1", "session_id": "s1", "error_code": "E", "error_msg": "boom"})
	env.seedRow(t, "tool_errors", uuidN(6), at(2), map[string]any{"tool_name": "read", "node_id": "n2", "session_id": "s2", "error_msg": "x"})
	env.seedTrace(t, uuidN(7), "n1", "tool.use", at(1), 1, `{"tool_name":"bash"}`)
	env.seedTrace(t, uuidN(8), "n2", "assistant.message", at(2), 2, "")
	for _, c := range []struct {
		path, key, filter, field string
		want                     any
	}{
		{"/sessions/outcomes", "outcomes", "?status=failed", "duration_ms", float64(5)},
		{"/sessions/outcomes", "outcomes", "?node_id=n2", "token_usage", map[string]any{"in": float64(1)}},
		{"/sessions/outcomes", "outcomes", "?session_id=s2", "status", "completed"},
		{"/tools/calls", "calls", "?tool_name=read", "input", map[string]any{"cmd": "ls"}},
		{"/tools/calls", "calls", "?node_id=n2", "tool_name", "bash"},
		{"/tools/calls", "calls", "?session_id=s2", "output", map[string]any{}},
		{"/tools/errors", "errors", "?tool_name=read", "error_code", "E"},
		{"/tools/errors", "errors", "?node_id=n2", "error_msg", "boom"},
		{"/tools/errors", "errors", "?session_id=s2", "tool_name", "bash"},
		{"/traces", "traces", "?event_type=assistant.message", "sequence", float64(1)},
		{"/traces", "traces", "?node_id=n2", "data", map[string]any{"tool_name": "bash"}},
	} {
		t.Run(c.path+c.filter, func(t *testing.T) {
			all := listPage(t, env.must(t, http.StatusOK, http.MethodGet, runPath+c.path, "", owner), c.key)
			if len(all.Items) != 2 || all.Items[0]["timestamp"] != at(1) || all.HasMore {
				t.Errorf("listing: %v", all)
			}
			if first := all.Items[0]; jsonOf(first[c.field]) != jsonOf(c.want) {
				t.Errorf("first row %s = %v; want %v", c.field, first[c.field], c.want)
			}
			filtered := listPage(t, env.must(t, http.StatusOK, http.MethodGet, runPath+c.path+c.filter, "", owner), c.key)
			if len(filtered.Items) != 1 || filtered.Items[0]["timestamp"] != at(2) {
				t.Errorf("filter %s: %v; want the second row", c.filter, ids(filtered.Items))
			}
			ranged := listPage(t, env.must(t, http.StatusOK, http.MethodGet, runPath+c.path+"?since="+at(2)+"&until="+at(3)+"&order=desc", "", owner), c.key)
			if len(ranged.Items) != 1 || ranged.Items[0]["timestamp"] != at(2) {
				t.Errorf("time range: %v", ids(ranged.Items))
			}
			paged := listPage(t, env.must(t, http.StatusOK, http.MethodGet, runPath+c.path+"?limit=1", "", owner), c.key)
			if !paged.HasMore || paged.NextCursor == nil {
				t.Fatalf("first page: %v", paged)
			}
			next := listPage(t, env.must(t, http.StatusOK, http.MethodGet, runPath+c.path+"?limit=1&cursor="+*paged.NextCursor, "", owner), c.key)
			if len(next.Items) != 1 || next.Items[0]["timestamp"] != at(2) || next.HasMore {
				t.Errorf("second page: %v", next)
			}
			other := listPage(t, env.must(t, http.StatusOK, http.MethodGet, "/api/v1/workspaces/w-own/runs/"+runID+c.path, "", admin), c.key)
			if len(other.Items) != 0 || other.Items == nil {
				t.Errorf("another workspace: %v; want an empty list", other.Items)
			}
			expect(t, env.do(t, http.MethodGet, runPath+c.path+"?cursor=***", "", owner), http.StatusBadRequest, "invalid cursor")
		})
	}
}

// GET .../postmortem returns the stored report with its JSON fields decoded,
// 404 postmortem_not_found when the run has none, and ignores pagination.
func TestGetPostmortem_ReturnsReport(t *testing.T) {
	env := newEnv(t)
	body := `{"run_status":"cost_limit","started_at":"2026-09-01T10:00:00Z","completed_at":"2026-09-01T11:00:00Z","task_summary":{"total":3,"done":1},"cost_summary":{"usd":1.5},"blocked_tasks":["t1"]}`
	env.must(t, http.StatusCreated, http.MethodPost, runPath+"/postmortem", body, owner)
	pm := testutil.Object(t, env.must(t, http.StatusOK, http.MethodGet, runPath+"/postmortem?limit=1&cursor=x", "", member))
	if pm["run_status"] != "cost_limit" || pm["schema_version"] != float64(1) || pm["started_at"] != "2026-09-01T10:00:00Z" ||
		jsonOf(pm["task_summary"]) != `{"done":1,"total":3}` || jsonOf(pm["blocked_tasks"]) != `["t1"]` || jsonOf(pm["session_history"]) != `[]` {
		t.Errorf("postmortem %v", pm)
	}
	rec := env.do(t, http.MethodGet, "/api/v1/workspaces/w-own/runs/"+runID+"/postmortem", "", admin)
	expect(t, rec, http.StatusNotFound, "no postmortem found for the given run_id")
	if testutil.Error(t, rec).ErrorType != "postmortem_not_found" {
		t.Errorf("error type %q", testutil.Error(t, rec).ErrorType)
	}
	expect(t, env.do(t, http.MethodGet, "/api/v1/workspaces/w/runs/nope/postmortem", "", owner), http.StatusBadRequest, "")
}

// Reading a run's audit data takes audit:read (which audit:write implies)
// and a workspace the caller may read, whatever its status: the owner, a
// member of its org, an admin token, a token bound to it. Anyone else and an
// unknown workspace get 404, so slugs cannot be enumerated; the scope is
// checked first and the run id after the workspace.
func TestRunReads_WorkspaceAccess(t *testing.T) {
	env := newEnv(t)
	env.seedAgentEvent(t, agentEvent{id: uuidN(1), ws: "w-arch"})
	env.seedAgentEvent(t, agentEvent{id: uuidN(2)})
	for _, c := range []struct {
		name, slug, run string
		auth            *apikit.AuthInfo
		status          int
		msg             string
	}{
		{"unauthenticated", "w", runID, nil, 401, "authentication required"},
		{"missing scope", "w", runID, testutil.PAT("u1", "sessions:read"), 403, "insufficient scope: audit:read required"},
		{"scope before workspace", "nope", "bad", testutil.PAT("u1", "sessions:read"), 403, "insufficient scope: audit:read required"},
		{"write scope implies read", "w", runID, testutil.PAT("u1", "audit:write"), 200, ""},
		{"owner", "w", runID, owner, 200, ""},
		{"member", "w", runID, member, 200, ""},
		{"member of archived", "w-arch", runID, member, 200, ""},
		{"admin", "w-x", runID, admin, 200, ""},
		{"bound to it", "w", runID, bound, 200, ""},
		{"bound elsewhere", "w-arch", runID, bound, 404, "workspace not found"},
		{"stranger", "w", runID, stranger, 404, "workspace not found"},
		{"unknown workspace", "nope", runID, owner, 404, "workspace not found"},
		{"workspace before run id", "nope", "bad", owner, 404, "workspace not found"},
		{"bad run id", "w", "bad", member, 400, `invalid run_id format: "bad" (expected YYYYMMDD_HHMMSS_6hexchars)`},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := env.do(t, http.MethodGet, "/api/v1/workspaces/"+c.slug+"/runs/"+c.run+"/events", "", c.auth)
			expect(t, rec, c.status, c.msg)
			if c.status == 200 && c.slug != "w-x" && len(listPage(t, rec, "events").Items) != 1 {
				t.Errorf("%s read %s", c.name, rec.Body)
			}
		})
	}
	for _, path := range []string{"/sessions/outcomes", "/tools/calls", "/tools/errors", "/traces", "/postmortem", "/transcript?node_id=n"} {
		if rec := env.do(t, http.MethodGet, "/api/v1/workspaces/w/runs/"+runID+path, "", stranger); rec.Code != 404 {
			t.Errorf("GET %s as a stranger: %d; want 404", path, rec.Code)
		}
	}
	testutil.Exec(t, env.sqlite, "DROP TABLE org_members")
	expect(t, env.do(t, http.MethodGet, runPath+"/events", "", member), 500, "internal server error")
	testutil.Exec(t, env.sqlite, "DROP TABLE token_workspace_bindings")
	expect(t, env.do(t, http.MethodGet, runPath+"/events", "", bound), 500, "internal server error")
}

// A failed DuckDB query is 500 on every per-run read.
func TestRunReads_StoreFailure(t *testing.T) {
	env := newBareEnv(t)
	for _, table := range []string{"agent_audit_events", "session_outcomes", "tool_calls", "tool_errors", "agent_traces", "postmortems"} {
		testutil.Exec(t, env.duck, "DROP TABLE "+table)
	}
	for _, path := range []string{"/events", "/sessions/outcomes", "/tools/calls", "/tools/errors", "/traces", "/postmortem"} {
		expect(t, env.do(t, http.MethodGet, runPath+path, "", admin), http.StatusInternalServerError, "internal server error")
	}
}
