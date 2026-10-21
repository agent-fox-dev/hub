package audit

import (
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// ---------------------------------------------------------------------------
// Spec 36 — per-run audit reads and the transcript
// Requirements: 36-REQ-1, 36-REQ-9.1
// ---------------------------------------------------------------------------

// archivedRunID is the run of the archived workspace w-arch (a run has one
// postmortem, whatever the workspace).
const archivedRunID = "20260704_143022_aaaaaa"

// runOf is the seeded run of workspace slug.
func runOf(slug string) string {
	if slug == "w-arch" {
		return archivedRunID
	}
	return testRunID
}

// runReadRoutes are the per-run read routes and the transcript of workspace
// slug for its run (see runOf), with the transcript's node.
func runReadRoutes(slug string) []string {
	base := "/api/v1/workspaces/" + slug + "/runs/" + runOf(slug)
	return []string{
		base + "/events",
		base + "/sessions/outcomes",
		base + "/tools/calls",
		base + "/tools/errors",
		base + "/traces",
		base + "/postmortem",
		base + "/transcript?node_id=node-1",
	}
}

// newRunReadEnv is newOrgMemberEnv with the transcript route mounted and
// run data (events, outcomes, tool calls and errors, traces, a
// postmortem and a transcript node) in w and w-arch.
func newRunReadEnv(t *testing.T) *auditTestEnv {
	t.Helper()
	env := newOrgMemberEnv(t)
	query := env.echo.Group("/api/v1")
	query.Use(testAuthMiddleware())
	RegisterAuditQueryRoutes(query, env.store, &mockSSEManager{}, env.sqliteDB)
	for _, ws := range []string{"w", "w-arch"} {
		ts, run := "2026-09-01T12:00:00Z", runOf(ws)
		env.seedAuditEvent(t, "ev-"+ws, run, ws, "session.start", ts)
		env.seedSessionOutcome(t, "out-"+ws, run, ws, "completed", ts)
		env.seedToolCall(t, "call-"+ws, run, ws, "bash", ts)
		env.seedToolError(t, "err-"+ws, run, ws, "bash", ts)
		seedTranscriptTrace(t, env.db, "tr-"+ws, run, ws, "assistant.message", "node-1", `{"content":"hello"}`, ts)
		if _, err := env.db.Exec(
			`INSERT INTO postmortems (run_id, workspace, schema_version, run_status,
			 started_at, completed_at, task_summary, cost_summary, blocked_tasks,
			 session_history, ingested_at)
			 VALUES (?, ?, 1, 'stalled', '2026-09-01T12:00:00Z', '2026-09-01T13:00:00Z',
			 '{"total":1}', '{"total_tokens":1}', '[]', '[]', '2026-09-01T14:00:00Z')`,
			run, ws,
		); err != nil {
			t.Fatalf("seed postmortem: %v", err)
		}
	}
	return env
}

var (
	ownerRead    = patAuth("u1", "audit:read", "sessions:write")
	memberRead   = patAuth("u2", "audit:read", "sessions:write")
	strangerRead = patAuth("u3", "audit:read", "sessions:write")
)

// TS-36-1: a member of the workspace's org gets 200 on every per-run read
// route and the transcript, with the owner's body. Requirement: 36-REQ-1.1
func TestRunReads_MemberReads_TS_36_1(t *testing.T) {
	env := newRunReadEnv(t)
	// What each route's body carries of the seeded run.
	markers := []string{`"ev-w"`, `"out-w"`, `"call-w"`, `"err-w"`, `"tr-w"`, `"stalled"`, `"hello"`}
	for i, path := range runReadRoutes("w") {
		m := env.doJSON(t, http.MethodGet, path, "", memberRead)
		o := env.doJSON(t, http.MethodGet, path, "", ownerRead)
		if m.Code != http.StatusOK || o.Code != http.StatusOK {
			t.Errorf("%s: member %d, owner %d (%s); want 200 for both", path, m.Code, o.Code, m.Body)
			continue
		}
		if m.Body.String() != o.Body.String() {
			t.Errorf("%s: member body %s; want the owner's %s", path, m.Body, o.Body)
		}
		if !strings.Contains(m.Body.String(), markers[i]) {
			t.Errorf("%s: body %s lacks the seeded %s", path, m.Body, markers[i])
		}
	}
}

// TS-36-2: the owner and an admin token keep reading every per-run route,
// with the same body. Requirement: 36-REQ-1.1
func TestRunReads_OwnerAndAdmin_TS_36_2(t *testing.T) {
	env := newRunReadEnv(t)
	for _, path := range runReadRoutes("w") {
		o := env.doJSON(t, http.MethodGet, path, "", ownerRead)
		a := env.doJSON(t, http.MethodGet, path, "", adminAuth())
		if o.Code != http.StatusOK || a.Code != http.StatusOK || o.Body.String() != a.Body.String() {
			t.Errorf("%s: owner (%d, %s), admin (%d, %s); want 200 and equal bodies", path, o.Code, o.Body, a.Code, a.Body)
		}
	}
}

// TS-36-3: a member reads the per-run routes of an archived workspace.
// Requirement: 36-REQ-1.2
func TestRunReads_MemberArchived_TS_36_3(t *testing.T) {
	env := newRunReadEnv(t)
	for _, path := range runReadRoutes("w-arch") {
		if rec := env.doJSON(t, http.MethodGet, path, "", memberRead); rec.Code != http.StatusOK {
			t.Errorf("%s: (%d, %s); want 200", path, rec.Code, rec.Body)
		}
	}
}

// TS-36-4: a caller without audit:read is refused with the scope message
// before any workspace lookup. Requirement: 36-REQ-1.3
func TestRunReads_ScopeFirst_TS_36_4(t *testing.T) {
	env := newRunReadEnv(t)
	for _, path := range runReadRoutes("nope") {
		rec := env.doJSON(t, http.MethodGet, path, "", patAuth("u2", "sessions:read"))
		if rec.Code != http.StatusForbidden || errOf(t, rec).Error.Message != "insufficient scope: audit:read required" {
			t.Errorf("%s: (%d, %s); want 403 with the audit:read scope message", path, rec.Code, rec.Body)
		}
	}
}

// TS-36-5: a stranger, and anyone naming an unknown workspace, gets 404
// workspace not found on every per-run route, with no audit data.
// Requirement: 36-REQ-1.4
func TestRunReads_StrangerAndUnknown404_TS_36_5(t *testing.T) {
	env := newRunReadEnv(t)
	for _, slug := range []string{"w", "missing"} {
		for _, path := range runReadRoutes(slug) {
			rec := env.doJSON(t, http.MethodGet, path, "", strangerRead)
			if rec.Code != http.StatusNotFound || errOf(t, rec).Error.Message != "workspace not found" {
				t.Errorf("%s: (%d, %s); want 404 workspace not found", path, rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "ev-") || strings.Contains(rec.Body.String(), "hello") {
				t.Errorf("%s: body carries audit data: %s", path, rec.Body)
			}
		}
	}
}

// TS-36-6: the validations after the check apply to a member as to the
// owner: the run_id format, node_id is required and postmortem_not_found.
// Requirement: 36-REQ-1.5
func TestRunReads_MemberValidations_TS_36_6(t *testing.T) {
	env := newRunReadEnv(t)
	bad := "/api/v1/workspaces/w/runs/not_a_run/events"
	m, o := env.doJSON(t, http.MethodGet, bad, "", memberRead), env.doJSON(t, http.MethodGet, bad, "", ownerRead)
	if m.Code != http.StatusBadRequest || m.Body.String() != o.Body.String() {
		t.Errorf("malformed run_id: member (%d, %s), owner (%d, %s); want the owner's 400", m.Code, m.Body, o.Code, o.Body)
	}
	rec := env.doJSON(t, http.MethodGet, "/api/v1/workspaces/w/runs/"+testRunID+"/transcript", "", memberRead)
	if rec.Code != http.StatusBadRequest || errOf(t, rec).Error.Message != "node_id is required" {
		t.Errorf("transcript without node_id: (%d, %s); want 400 node_id is required", rec.Code, rec.Body)
	}
	rec = env.doJSON(t, http.MethodGet, "/api/v1/workspaces/w/runs/20260704_143022_ffffff/postmortem", "", memberRead)
	if rec.Code != http.StatusNotFound || errOf(t, rec).Error.ErrorType != "postmortem_not_found" {
		t.Errorf("postmortem of R2: (%d, %s); want 404 postmortem_not_found", rec.Code, rec.Body)
	}
}

// TS-36-7: with a nil SQLite handle the per-run routes skip the workspace
// check for every caller. Requirement: 36-REQ-1.6
func TestRunReads_NilSQLiteSkips_TS_36_7(t *testing.T) {
	env := newAuditTestEnv(t)
	query := env.echo.Group("/api/v1")
	query.Use(testAuthMiddleware())
	RegisterAuditQueryRoutes(query, env.store, &mockSSEManager{}, nil)
	for _, path := range runReadRoutes("any-slug") {
		rec := env.doJSON(t, http.MethodGet, path, "", patAuth("u9", "audit:read"))
		if rec.Code == http.StatusNotFound && errOf(t, rec).Error.Message == "workspace not found" {
			t.Errorf("%s: refused by the workspace check: %s", path, rec.Body)
		}
		if !strings.HasSuffix(path, "/postmortem") && rec.Code != http.StatusOK {
			t.Errorf("%s: (%d, %s); want 200", path, rec.Code, rec.Body)
		}
	}
}

// TS-36-39: a failed membership or binding read while deciding access on a
// per-run read, the transcript or POST /sessions is 500, with no audit data
// and no session row. Requirement: 36-REQ-9.1
func TestRunReads_DatabaseFailure500_TS_36_39(t *testing.T) {
	for table, auth := range map[string]*apikit.AuthInfo{
		"org_members":              memberRead,
		"token_workspace_bindings": boundPatAuth("u1", "tokU", "audit:read", "sessions:write"),
	} {
		t.Run(table, func(t *testing.T) {
			env := newRunReadEnv(t)
			if _, err := env.sqliteDB.Exec("DROP TABLE " + table); err != nil {
				t.Fatal(err)
			}
			before := queryTableCount(t, env.db, "agent_sessions")
			paths := runReadRoutes("w")
			for _, path := range []string{paths[0], paths[6]} {
				rec := env.doJSON(t, http.MethodGet, path, "", auth)
				if rec.Code != http.StatusInternalServerError || errOf(t, rec).Error.Message != "internal server error" {
					t.Errorf("GET %s: (%d, %s); want 500 internal server error", path, rec.Code, rec.Body)
				}
			}
			rec := env.doJSON(t, http.MethodPost, "/api/v1/sessions", `{"workspace_slug":"w"}`, auth)
			if rec.Code != http.StatusInternalServerError || errOf(t, rec).Error.Message != "internal server error" {
				t.Errorf("POST /sessions: (%d, %s); want 500 internal server error", rec.Code, rec.Body)
			}
			if after := queryTableCount(t, env.db, "agent_sessions"); after != before {
				t.Errorf("session rows %d -> %d; want unchanged", before, after)
			}
		})
	}
}
