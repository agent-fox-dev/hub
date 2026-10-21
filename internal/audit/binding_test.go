package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// errOf decodes an apikit error envelope, error_type included.
func errOf(t *testing.T, rec *httptest.ResponseRecorder) apiErrorEnvelope {
	t.Helper()
	var e apiErrorEnvelope
	parseJSON(t, rec, &e)
	return e
}

// newBoundSessionEnv is an audit environment with SQLite in which u1 owns
// workspaces s and x and token tokB is bound to s.
func newBoundSessionEnv(t *testing.T) *auditTestEnv {
	t.Helper()
	env := newAuditTestEnvWithSQLite(t)
	env.seedWorkspace(t, "s", "u1")
	env.seedWorkspace(t, "x", "u1")
	bindToken(t, env.sqliteDB, "tokB", "s")
	return env
}

// TS-33-35: POST /sessions with another workspace or an idempotent replay of
// another workspace's session is 404. Requirement: 33-REQ-6.2
func TestSessions_BoundCreateOtherWorkspace_TS_33_35(t *testing.T) {
	env := newBoundSessionEnv(t)
	xID := sessionID(1)
	env.seedSession(t, &Session{ID: xID, WorkspaceSlug: "x", Status: "active", CredentialID: "u1", CredentialType: "pat"})
	bound := boundPatAuth("u1", "tokB", "sessions:write")

	rec := env.doJSON(t, http.MethodPost, "/api/v1/sessions", `{"workspace_slug":"x"}`, bound)
	if rec.Code != http.StatusNotFound || errOf(t, rec).Error.Message != "workspace not found" {
		t.Errorf("create in x: (%d, %s); want 404 workspace not found", rec.Code, rec.Body)
	}
	if n := env.countSessionRows(t, "x"); n != 1 {
		t.Errorf("sessions in x = %d; want 1, nothing created", n)
	}

	rec = env.doJSON(t, http.MethodPost, "/api/v1/sessions", `{"id":"`+xID+`","workspace_slug":"s"}`, bound)
	if rec.Code != http.StatusNotFound || errOf(t, rec).Error.Message != "workspace not found" {
		t.Errorf("replay of x's session: (%d, %s); want 404 workspace not found", rec.Code, rec.Body)
	}
	if n := env.countSessionRows(t, "s"); n != 0 {
		t.Errorf("sessions in s = %d; want 0", n)
	}
	if ws := env.getSessionField(t, xID, "workspace_slug"); ws != "x" {
		t.Errorf("replayed session workspace = %q; want x, unchanged", ws)
	}

	// The same user's unbound PAT still opens a session in x.
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/sessions", `{"workspace_slug":"x"}`, boundPatAuth("u1", "tokU", "sessions:write")); rec.Code != http.StatusCreated {
		t.Errorf("unbound PAT in x: status %d; want 201: %s", rec.Code, rec.Body)
	}
}

// TS-33-36: Reading a session or usage outside S, or listing another slug, is
// 403 workspace access denied. Requirement: 33-REQ-6.3
func TestSessions_BoundReadOtherWorkspace_TS_33_36(t *testing.T) {
	env := newBoundSessionEnv(t)
	env.seedSession(t, &Session{ID: sessionID(1), WorkspaceSlug: "x", Status: "active", CredentialID: "u1", CredentialType: "pat"})
	env.seedSession(t, &Session{ID: sessionID(2), WorkspaceSlug: "s", Status: "active", CredentialID: "u1", CredentialType: "pat"})
	bound := boundPatAuth("u1", "tokB", "sessions:read")

	for _, path := range []string{
		"/api/v1/sessions/" + sessionID(1),
		"/api/v1/sessions/" + sessionID(1) + "/usage",
		"/api/v1/sessions?workspace_slug=x",
	} {
		rec := env.doJSON(t, http.MethodGet, path, "", bound)
		if rec.Code != http.StatusForbidden || errOf(t, rec).Error.Message != "workspace access denied" {
			t.Errorf("GET %s: (%d, %s); want 403 workspace access denied", path, rec.Code, rec.Body)
		}
	}
	// S's session is readable.
	if rec := env.doJSON(t, http.MethodGet, "/api/v1/sessions/"+sessionID(2), "", bound); rec.Code != http.StatusOK {
		t.Errorf("GET S's session: status %d; want 200: %s", rec.Code, rec.Body)
	}
}

// TS-33-37: GET /sessions without a slug lists only S's sessions.
// Requirement: 33-REQ-6.4
func TestSessions_BoundListOnlyS_TS_33_37(t *testing.T) {
	env := newBoundSessionEnv(t)
	env.seedSession(t, &Session{ID: sessionID(1), WorkspaceSlug: "x", Status: "active", CredentialID: "u1", CredentialType: "pat"})
	env.seedSession(t, &Session{ID: sessionID(2), WorkspaceSlug: "s", Status: "active", CredentialID: "u1", CredentialType: "pat"})

	rec := env.doJSON(t, http.MethodGet, "/api/v1/sessions", "", boundPatAuth("u1", "tokB", "sessions:read"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d; want 200: %s", rec.Code, rec.Body)
	}
	list := parseSessionListJSON(t, rec)
	if len(list.Sessions) == 0 {
		t.Fatal("no sessions listed; want S's")
	}
	for _, s := range list.Sessions {
		if s.WorkspaceSlug != "s" {
			t.Errorf("listed session %s of workspace %q; want only s", s.ID, s.WorkspaceSlug)
		}
	}

	// The unbound PAT of the same user lists both.
	rec = env.doJSON(t, http.MethodGet, "/api/v1/sessions", "", boundPatAuth("u1", "tokU", "sessions:read"))
	if n := len(parseSessionListJSON(t, rec).Sessions); n != 2 {
		t.Errorf("unbound PAT lists %d sessions; want 2", n)
	}
}

// TS-33-38: Completing or reporting usage on a session whose workspace is not
// S is 403 not session owner. Requirement: 33-REQ-6.5
func TestSessions_BoundWriteOtherWorkspace_TS_33_38(t *testing.T) {
	env := newBoundSessionEnv(t)
	id := sessionID(1)
	env.seedSession(t, &Session{ID: id, WorkspaceSlug: "x", Status: "active", CredentialID: "u1", CredentialType: "pat"})
	bound := boundPatAuth("u1", "tokB", "sessions:write")

	rec := env.doJSON(t, http.MethodPost, "/api/v1/sessions/"+id+"/complete", `{"status":"completed"}`, bound)
	if rec.Code != http.StatusForbidden || errOf(t, rec).Error.Message != "not session owner" {
		t.Errorf("complete: (%d, %s); want 403 not session owner", rec.Code, rec.Body)
	}
	rec = env.doJSON(t, http.MethodPost, "/api/v1/sessions/"+id+"/usage", `{"model":"m","input_tokens":1}`, bound)
	if rec.Code != http.StatusForbidden || errOf(t, rec).Error.Message != "not session owner" {
		t.Errorf("usage: (%d, %s); want 403 not session owner", rec.Code, rec.Body)
	}
	if st := env.getSessionStatus(t, id); st != "active" {
		t.Errorf("session status = %q; want active, unchanged", st)
	}
	if n := env.countTokenUsageRows(t, "x"); n != 0 {
		t.Errorf("usage rows in x = %d; want 0", n)
	}
}

// TS-33-46: checkWorkspaceAccess ignores auth.KeyID and never emits
// workspace_mismatch. Requirement: 33-REQ-8.1
func TestCheckWorkspaceAccess_IgnoresKeyID_TS_33_46(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)
	env.seedWorkspace(t, testSlug, "u1")
	pat := patAuth("u1", "audit:write")
	pat.KeyID = "some-other-slug"

	rec := env.doJSON(t, http.MethodPost, eventsPath, `{"event_type":"session.start","payload":{}}`, pat)
	if rec.Code != http.StatusCreated {
		t.Errorf("status %d; want 201: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "workspace_mismatch") {
		t.Errorf("body %s carries workspace_mismatch", rec.Body)
	}

	// The source of checkWorkspaceAccess no longer reads KeyID.
	f, err := parser.ParseFile(token.NewFileSet(), "auth.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing auth.go: %v", err)
	}
	found := false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "checkWorkspaceAccess" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "KeyID" {
				t.Error("checkWorkspaceAccess reads KeyID")
			}
			if lit, ok := n.(*ast.BasicLit); ok && strings.Contains(lit.Value, "workspace_mismatch") {
				t.Error("checkWorkspaceAccess still emits workspace_mismatch")
			}
			return true
		})
	}
	if !found {
		t.Fatal("checkWorkspaceAccess not found in auth.go")
	}
}

// TS-33-47: A bound token posting to another workspace's run gets 404 and no
// event is written. Requirement: 33-REQ-8.2
func TestIngestion_BoundOtherWorkspace_TS_33_47(t *testing.T) {
	env := newBoundSessionEnv(t)
	bound := boundPatAuth("u1", "tokB", "audit:write")

	for _, path := range []string{
		"/api/v1/workspaces/x/runs/" + testRunID + "/events",
		"/api/v1/workspaces/x/runs/" + testRunID + "/tools/calls",
	} {
		rec := env.doJSON(t, http.MethodPost, path, `{"event_type":"session.start","tool_name":"t","payload":{}}`, bound)
		if rec.Code != http.StatusNotFound || errOf(t, rec).Error.Message != "workspace not found" {
			t.Errorf("POST %s: (%d, %s); want 404 workspace not found", path, rec.Code, rec.Body)
		}
	}
	if n := queryTableCount(t, env.db, "agent_audit_events"); n != 0 {
		t.Errorf("events written = %d; want 0", n)
	}
	// On its own workspace the bound token is accepted.
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/workspaces/s/runs/"+testRunID+"/events", `{"event_type":"session.start","payload":{}}`, bound); rec.Code != http.StatusCreated {
		t.Errorf("POST to s: status %d; want 201: %s", rec.Code, rec.Body)
	}
}

// TS-33-48: An unbound non-owner posting to a run gets 403
// workspace_access_denied. Requirement: 33-REQ-8.3
func TestIngestion_UnboundNonOwner_TS_33_48(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)
	env.seedWorkspace(t, testSlug, "u2")

	rec := env.doJSON(t, http.MethodPost, eventsPath, `{"event_type":"session.start"}`, boundPatAuth("u1", "tokU", "audit:write"))
	if rec.Code != http.StatusForbidden || errOf(t, rec).Error.ErrorType != "workspace_access_denied" {
		t.Errorf("(%d, %s); want 403 workspace_access_denied", rec.Code, rec.Body)
	}
	if n := queryTableCount(t, env.db, "agent_audit_events"); n != 0 {
		t.Errorf("events written = %d; want 0, a refused request writes nothing", n)
	}
}

// TS-33-49: Archived workspace gives 409 workspace_archived and unknown
// workspace gives 404. Requirements: 33-REQ-8.4, 33-REQ-8.5
func TestIngestion_ArchivedAndUnknown_TS_33_49(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)
	env.seedWorkspaceWithStatus(t, "arch", "u1", "archived")
	pat := boundPatAuth("u1", "tokU", "audit:write")

	rec := env.doJSON(t, http.MethodPost, "/api/v1/workspaces/arch/runs/"+testRunID+"/events", `{"event_type":"session.start"}`, pat)
	if rec.Code != http.StatusConflict || errOf(t, rec).Error.ErrorType != "workspace_archived" {
		t.Errorf("archived: (%d, %s); want 409 workspace_archived", rec.Code, rec.Body)
	}
	rec = env.doJSON(t, http.MethodPost, "/api/v1/workspaces/ghost/runs/"+testRunID+"/events", `{"event_type":"session.start"}`, pat)
	if rec.Code != http.StatusNotFound {
		t.Errorf("ghost: (%d, %s); want 404", rec.Code, rec.Body)
	}
	if n := queryTableCount(t, env.db, "agent_audit_events"); n != 0 {
		t.Errorf("events written = %d; want 0, refused requests write nothing", n)
	}

	// A token bound to the archived workspace still reaches it (33-REQ-9.4).
	bindToken(t, env.sqliteDB, "tokA", "arch")
	rec = env.doJSON(t, http.MethodPost, "/api/v1/workspaces/arch/runs/"+testRunID+"/events", `{"event_type":"session.start"}`, boundPatAuth("u1", "tokA", "audit:write"))
	if rec.Code != http.StatusConflict || errOf(t, rec).Error.ErrorType != "workspace_archived" {
		t.Errorf("bound to the archived workspace: (%d, %s); want 409 workspace_archived", rec.Code, rec.Body)
	}
}

// TS-33-50: With a nil SQLite handle the workspace access checks are skipped,
// and every outcome is the one it was before spec 33: API keys and admin
// tokens are accepted for any workspace, and a PAT, whose workspace cannot be
// verified, keeps its 403 workspace_access_denied (TS-17-52).
// Requirement: 33-REQ-8.6
func TestIngestion_NilSQLiteSkipsChecks_TS_33_50(t *testing.T) {
	env := newAuditTestEnv(t)
	path := "/api/v1/workspaces/any-workspace/runs/" + testRunID + "/events"
	body := `{"event_type":"session.start","payload":{}}`

	for name, auth := range map[string]*apikit.AuthInfo{
		"api_key":     apiKeyAuth("u1"),
		"admin_token": adminAuth(),
	} {
		if rec := env.doJSON(t, http.MethodPost, path, body, auth); rec.Code != http.StatusCreated {
			t.Errorf("%s: status %d; want 201, the checks skipped: %s", name, rec.Code, rec.Body)
		}
	}
	rec := env.doJSON(t, http.MethodPost, path, body, boundPatAuth("u1", "tokB", "audit:write"))
	if rec.Code != http.StatusForbidden || errOf(t, rec).Error.ErrorType != "workspace_access_denied" {
		t.Errorf("PAT: (%d, %s); want the unchanged 403 workspace_access_denied", rec.Code, rec.Body)
	}
}
