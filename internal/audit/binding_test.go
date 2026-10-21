package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
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

// TS-33-53 (property, sessions and audit): whatever the state of the bound
// workspace S (active, archived, deleted, owned by someone else), a bound
// token never lists more sessions or audit events than the unbound PAT of the
// same user and scopes, and nothing outside S. Requirements: 33-REQ-6.4,
// 33-REQ-6.6, 33-REQ-9.3, 33-REQ-9.5
func TestSessionsAudit_PropertyNeverWidens_TS_33_53(t *testing.T) {
	for _, state := range []string{"active", "archived", "deleted", "foreign"} {
		t.Run(state, func(t *testing.T) {
			env := newAuditTestEnvWithSQLite(t)
			query := env.echo.Group("/api/v1")
			query.Use(testAuthMiddleware())
			RegisterAuditQueryRoutes(query, env.store, &mockSSEManager{}, env.sqliteDB)
			switch state {
			case "active", "archived":
				env.seedWorkspaceWithStatus(t, "s", "u1", state)
			case "foreign":
				env.seedWorkspace(t, "s", "u2")
			}
			env.seedWorkspace(t, "x", "u1")
			env.seedWorkspace(t, "y", "u2")
			bindToken(t, env.sqliteDB, "tokB", "s")
			for i, ws := range []string{"s", "x", "y"} {
				env.seedSession(t, &Session{ID: sessionID(i + 1), WorkspaceSlug: ws, Status: "active", CredentialID: "u1", CredentialType: "pat"})
				insertTestHubEvent(t, env.db, "ev-"+ws, "hub.workspace.updated", "u1", "pat", "workspace", ws, "update", ws, "info", "2026-09-01T10:00:00Z")
			}

			scopes := []string{"sessions:read", "audit:read"}
			bound, unbound := boundPatAuth("u1", "tokB", scopes...), boundPatAuth("u1", "tokU", scopes...)
			sessionsOf := func(auth *apikit.AuthInfo) map[string]bool {
				rec := env.doJSON(t, http.MethodGet, "/api/v1/sessions", "", auth)
				if rec.Code != http.StatusOK {
					t.Fatalf("GET /sessions: (%d, %s)", rec.Code, rec.Body)
				}
				out := map[string]bool{}
				for _, s := range parseSessionListJSON(t, rec).Sessions {
					out[s.WorkspaceSlug] = true
				}
				return out
			}
			eventsOf := func(auth *apikit.AuthInfo) map[string]bool {
				rec := env.doJSON(t, http.MethodGet, "/api/v1/audit", "", auth)
				if rec.Code != http.StatusOK {
					t.Fatalf("GET /audit: (%d, %s)", rec.Code, rec.Body)
				}
				var resp auditQueryResponse
				parseJSON(t, rec, &resp)
				out := map[string]bool{}
				for _, e := range resp.Events {
					out[e["workspace"].(string)] = true
				}
				return out
			}
			for name, pair := range map[string][2]map[string]bool{
				"sessions": {sessionsOf(bound), sessionsOf(unbound)},
				"audit":    {eventsOf(bound), eventsOf(unbound)},
			} {
				for ws := range pair[0] {
					if ws != "s" || !pair[1][ws] {
						t.Errorf("%s: the bound token sees workspace %q (unbound sees %v); want only s, and only what the unbound PAT sees", name, ws, pair[1])
					}
				}
			}
		})
	}
}

// Sessions of a user with no readable workspace: GET /sessions lists none,
// for an unbound PAT as for a bound one (erratum 33, entry 8).
func TestSessions_NoAccessibleWorkspaceListsNothing(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)
	env.seedWorkspace(t, "y", "u2")
	env.seedSession(t, &Session{ID: sessionID(1), WorkspaceSlug: "y", Status: "active", CredentialID: "u2", CredentialType: "pat"})

	rec := env.doJSON(t, http.MethodGet, "/api/v1/sessions", "", boundPatAuth("u1", "tokU", "sessions:read"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if n := len(parseSessionListJSON(t, rec).Sessions); n != 0 {
		t.Errorf("u1, who owns no workspace, lists %d sessions of u2; want 0", n)
	}
}

// newSweepAuditEnv registers the session, ingestion and query routes on a
// SQLite-backed environment, with the binding guard in front when guarded,
// and seeds identical workspaces, sessions and events.
func newSweepAuditEnv(t *testing.T, guarded bool) *auditTestEnv {
	t.Helper()
	duckDB := openTestAuditDB(t)
	initHandlerTestSchema(t, duckDB)
	sqliteDB := openTestSQLiteDB(t)
	store := NewStore(duckDB)
	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	if guarded {
		api.Use(wsaccess.BindingGuard(sqliteDB))
	}
	RegisterSessionRoutes(api, store, sqliteDB, nil)
	RegisterRoutes(api, store, &nopEmitter{}, sqliteDB)
	RegisterAuditQueryRoutes(api, store, &mockSSEManager{}, sqliteDB)
	env := &auditTestEnv{echo: e, db: duckDB, sqliteDB: sqliteDB, store: store}
	env.seedWorkspace(t, "x", "u1")
	env.seedWorkspace(t, "y", "u2")
	env.seedSession(t, &Session{ID: sessionID(1), WorkspaceSlug: "x", Status: "active", CredentialID: "u1", CredentialType: "pat", StartedAt: "2026-09-01T10:00:00Z"})
	env.seedSession(t, &Session{ID: sessionID(2), WorkspaceSlug: "y", Status: "active", CredentialID: "u2", CredentialType: "pat", StartedAt: "2026-09-01T10:00:00Z"})
	insertTestHubEvent(t, duckDB, "ev-x", "hub.workspace.updated", "u1", "pat", "workspace", "x", "update", "x", "info", "2026-09-01T10:00:00Z")
	return env
}

var generatedValues = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// TS-33-51 (property, sessions and audit): for any request by an unbound PAT
// (with and without a token id), an API key, an admin token or no credential,
// the session, ingestion and audit routes answer the same behind the binding
// guard as without it (generated timestamps and ids masked).
// Requirement: 33-REQ-9.1
func TestSessionsAudit_PropertyUnboundUnchanged_TS_33_51(t *testing.T) {
	creds := map[string]*apikit.AuthInfo{
		"pat without token id": patAuth("u1", "sessions:read", "sessions:write", "audit:read", "audit:write"),
		"unbound pat":          boundPatAuth("u1", "tokU", "sessions:read", "sessions:write", "audit:read", "audit:write"),
		"api_key":              apiKeyAuth("u1"),
		"admin_token":          adminAuth(),
		"none":                 nil,
	}
	requests := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/sessions", ""},
		{http.MethodGet, "/api/v1/sessions?workspace_slug=y", ""},
		{http.MethodGet, "/api/v1/sessions/" + sessionID(1), ""},
		{http.MethodGet, "/api/v1/sessions/" + sessionID(2) + "/usage", ""},
		{http.MethodPost, "/api/v1/sessions", `{"workspace_slug":"x"}`},
		{http.MethodPost, "/api/v1/sessions", `{"workspace_slug":"y"}`},
		{http.MethodPost, "/api/v1/sessions/" + sessionID(1) + "/complete", `{"status":"completed"}`},
		{http.MethodPost, "/api/v1/sessions/" + sessionID(2) + "/usage", `{"model":"m","input_tokens":1}`},
		{http.MethodGet, "/api/v1/workspaces/x/cost", ""},
		{http.MethodPost, "/api/v1/workspaces/x/runs/" + testRunID + "/events", `{"event_type":"session.start","payload":{}}`},
		{http.MethodPost, "/api/v1/workspaces/y/runs/" + testRunID + "/events", `{"event_type":"session.start","payload":{}}`},
		{http.MethodGet, "/api/v1/workspaces/x/runs/" + testRunID + "/events", ""},
		{http.MethodGet, "/api/v1/audit", ""},
		{http.MethodGet, "/api/v1/audit?workspace=y", ""},
	}
	for name, auth := range creds {
		for _, r := range requests {
			g := newSweepAuditEnv(t, true).doJSON(t, r.method, r.path, r.body, auth)
			p := newSweepAuditEnv(t, false).doJSON(t, r.method, r.path, r.body, auth)
			gb := generatedValues.ReplaceAllString(g.Body.String(), "<generated>")
			pb := generatedValues.ReplaceAllString(p.Body.String(), "<generated>")
			if g.Code != p.Code || gb != pb {
				t.Errorf("%s %s as %s: guarded (%d, %s); want unguarded (%d, %s)", r.method, r.path, name, g.Code, g.Body, p.Code, p.Body)
			}
		}
	}
}
