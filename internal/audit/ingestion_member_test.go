package audit

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"
)

// ---------------------------------------------------------------------------
// Spec 36 — writes that do not change for org members
// Requirements: 36-REQ-7
// ---------------------------------------------------------------------------

// ingestionPaths are the eight audit ingestion routes of workspace slug.
func ingestionPaths(slug string) []string {
	base := "/api/v1/workspaces/" + slug + "/runs/" + testRunID
	return []string{
		base + "/events", base + "/events/batch", base + "/sessions/outcomes", base + "/tools/calls",
		base + "/tools/errors", base + "/traces", base + "/traces/batch", base + "/postmortem",
	}
}

// auditRowCounts counts the rows of every audit table.
func auditRowCounts(t *testing.T, env *auditTestEnv) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"agent_audit_events", "session_outcomes", "tool_calls", "tool_errors", "agent_traces", "postmortems"} {
		out[table] = queryTableCount(t, env.db, table)
	}
	return out
}

// TS-36-32: a member completing or reporting usage on the owner's session
// gets 403 not session owner, and no session or usage row changes.
// Requirement: 36-REQ-7.1
func TestSessionWrites_MemberNotOwner_TS_36_32(t *testing.T) {
	env := newOrgMemberEnv(t)
	env.seedSession(t, &Session{ID: "sess-s", WorkspaceSlug: "w", Status: "active", CredentialID: "u1", CredentialType: "pat"})
	member := patAuth("u2", "sessions:write")
	for _, c := range []struct{ path, body string }{
		{"/api/v1/sessions/sess-s/complete", `{"status":"completed"}`},
		{"/api/v1/sessions/sess-s/usage", `{"model":"m","input_tokens":1}`},
	} {
		rec := env.doJSON(t, http.MethodPost, c.path, c.body, member)
		if rec.Code != http.StatusForbidden || errOf(t, rec).Error.Message != "not session owner" {
			t.Errorf("%s: (%d, %s); want 403 not session owner", c.path, rec.Code, rec.Body)
		}
	}
	if s := env.getSessionStatus(t, "sess-s"); s != "active" {
		t.Errorf("session status %q; want active", s)
	}
	if n := env.countTokenUsageRows(t, "w"); n != 0 {
		t.Errorf("%d usage rows; want 0", n)
	}
}

// TS-36-33: a member is refused on every ingestion route of an active
// workspace with workspace_access_denied, and no audit row is written.
// Requirement: 36-REQ-7.2
func TestIngestion_MemberDenied_TS_36_33(t *testing.T) {
	env := newOrgMemberEnv(t)
	before := auditRowCounts(t, env)
	for _, path := range ingestionPaths("w") {
		rec := env.doJSON(t, http.MethodPost, path, `{"event_type":"session.start","payload":{}}`, patAuth("u2", "audit:write"))
		if rec.Code != http.StatusForbidden || errOf(t, rec).Error.ErrorType != "workspace_access_denied" {
			t.Errorf("%s: (%d, %s); want 403 workspace_access_denied", path, rec.Code, rec.Body)
		}
	}
	after := auditRowCounts(t, env)
	for table, n := range before {
		if after[table] != n {
			t.Errorf("%s rows %d -> %d; want unchanged", table, n, after[table])
		}
	}
}

// TS-36-34 (property): for every workspace state and caller kind, members
// included, ingestion answers in checkWorkspaceAccess's order: missing 404,
// binding 404, archived 409 workspace_archived, non-owner 403
// workspace_access_denied. Requirement: 36-REQ-7.3
func TestIngestion_PropertyOrder_TS_36_34(t *testing.T) {
	callers := map[string]*apikit.AuthInfo{
		"owner":         patAuth("u1", "audit:write"),
		"member":        patAuth("u2", "audit:write"),
		"stranger":      patAuth("u3", "audit:write"),
		"admin":         adminAuth(),
		"bound to else": boundPatAuth("u1", "tokE", "audit:write"),
	}
	type answer struct {
		status    int
		errorType string
	}
	expected := func(state, caller string) answer {
		switch {
		case caller == "admin":
			return answer{http.StatusCreated, ""}
		case state == "missing", caller == "bound to else":
			return answer{http.StatusNotFound, ""}
		case state == "archived":
			return answer{http.StatusConflict, "workspace_archived"}
		case caller == "owner":
			return answer{http.StatusCreated, ""}
		}
		return answer{http.StatusForbidden, "workspace_access_denied"}
	}
	for _, state := range []string{"missing", "archived", "ready"} {
		for name, auth := range callers {
			env := newAuditTestEnvWithSQLite(t)
			if state != "missing" {
				env.seedWorkspaceInOrg(t, "w", "u1", "O", state)
			}
			env.seedWorkspaceInOrg(t, "w-else", "u1", "O", "ready")
			seedOrgMember(t, env.sqliteDB, "O", "u1")
			seedOrgMember(t, env.sqliteDB, "O", "u2")
			bindToken(t, env.sqliteDB, "tokE", "w-else")
			rec := env.doJSON(t, http.MethodPost, ingestionPaths("w")[0], `{"event_type":"session.start","payload":{}}`, auth)
			got := answer{rec.Code, ""}
			if rec.Code >= 400 {
				got.errorType = errOf(t, rec).Error.ErrorType
			}
			if want := expected(state, name); got != want {
				t.Errorf("%s workspace, %s: %+v (%s); want %+v", state, name, got, rec.Body, want)
			}
		}
	}
}
