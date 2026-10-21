package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// TS-35-56 (audit): A member of a workspace's org gets the stranger's answers
// on the session, cost, per-run audit, unified audit query, SSE and
// transcript routes and on session ingestion: these stay owner-only.
// Requirement: 35-REQ-10.6
func TestMemberAuditRoutesOwnerOnly_TS_35_56(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)
	api := env.echo.Group("/api/v1")
	api.Use(testAuthMiddleware())
	RegisterAuditQueryRoutes(api, env.store, NewSSEManager(10), env.sqliteDB)
	for _, stmt := range []string{
		`ALTER TABLE workspaces ADD COLUMN org_id TEXT`,
		`CREATE TABLE org_members (org_id TEXT NOT NULL, user_id TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT '', PRIMARY KEY (org_id, user_id))`,
		`INSERT INTO workspaces (slug, owner_id, org_id) VALUES ('w', 'u1', 'O')`,
		`INSERT INTO org_members (org_id, user_id) VALUES ('O', 'u1'), ('O', 'u2')`,
	} {
		if _, err := env.sqliteDB.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	env.seedSession(t, &Session{ID: sessionID(1), WorkspaceSlug: "w", Status: "active", CredentialID: "u1", CredentialType: "api_key"})

	call := func(method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		if body != "" {
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		}
		authJSON := `{"CredentialType":"` + auth.CredentialType + `","UserID":"` + auth.UserID + `"}`
		req.Header.Set("X-Test-Auth", authJSON)
		rec := httptest.NewRecorder()
		env.echo.ServeHTTP(rec, req)
		return rec
	}
	member, stranger := apiKeyAuth("u2"), apiKeyAuth("u3")
	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/sessions?workspace_slug=w", ""},
		{http.MethodGet, "/api/v1/sessions/" + sessionID(1), ""},
		{http.MethodGet, "/api/v1/sessions/" + sessionID(1) + "/usage", ""},
		{http.MethodGet, "/api/v1/workspaces/w/cost", ""},
		{http.MethodGet, "/api/v1/workspaces/w/runs/run-1/events", ""},
		{http.MethodGet, "/api/v1/workspaces/w/runs/run-1/tools/calls", ""},
		{http.MethodGet, "/api/v1/workspaces/w/runs/run-1/transcript", ""},
		{http.MethodGet, "/api/v1/audit?workspace=w", ""},
		{http.MethodGet, "/api/v1/events?workspace=w", ""},
		{http.MethodPost, "/api/v1/sessions", `{"workspace_slug":"w"}`},
		{http.MethodPost, "/api/v1/sessions/" + sessionID(1) + "/complete", `{"status":"completed"}`},
		{http.MethodPost, "/api/v1/workspaces/w/runs/run-1/events", `{"event_type":"x"}`},
	} {
		m := call(r.method, r.path, r.body, member)
		s := call(r.method, r.path, r.body, stranger)
		if m.Code != s.Code || m.Body.String() != s.Body.String() {
			t.Errorf("%s %s: member %d %s, stranger %d %s; want the same", r.method, r.path, m.Code, m.Body, s.Code, s.Body)
		}
		if m.Code < 400 {
			t.Errorf("%s %s: member status %d; want a refusal", r.method, r.path, m.Code)
		}
	}
}
