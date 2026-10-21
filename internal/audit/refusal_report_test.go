package audit

import (
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// TS-34-58 (audit and sessions): each audit or session refusal of a bound
// token reached without the guard reports exactly once; filtering reports
// nothing. Requirement: 34-REQ-10.3
func TestAuditRefusals_Report_TS_34_58(t *testing.T) {
	var reports []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { reports = append(reports, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })

	env, _ := newBoundQueryEnv(t) // u1 owns s and x, tokB is bound to s
	RegisterSessionRoutes(env.echo.Group("/api/v1", testAuthMiddleware()), env.store, env.sqliteDB, nil)
	RegisterRoutes(env.echo.Group("/api/v1", testAuthMiddleware()), env.store, &nopEmitter{}, env.sqliteDB)
	initHandlerTestSchema(t, env.db)
	xID := sessionID(9)
	env.seedSession(t, &Session{ID: xID, WorkspaceSlug: "x", Status: "active", CredentialID: "u1", CredentialType: "pat"})
	bound := boundPatAuth("u1", "tokB", "sessions:read", "sessions:write", "audit:read", "audit:write")

	cases := []struct {
		name, method, path, body string
		status                   int
		route                    string
	}{
		{"ingestion", "POST", "/api/v1/workspaces/x/runs/" + testRunID + "/events", `{"event_type":"session.start","payload":{}}`, 404, "/api/v1/workspaces/:slug/runs/:run_id/events"},
		{"run events read", "GET", "/api/v1/workspaces/x/runs/" + testRunID + "/events", "", 404, "/api/v1/workspaces/:slug/runs/:run_id/events"},
		{"session read", "GET", "/api/v1/sessions/" + xID, "", 403, "/api/v1/sessions/:id"},
		{"session usage", "GET", "/api/v1/sessions/" + xID + "/usage", "", 403, "/api/v1/sessions/:id/usage"},
		{"session list of x", "GET", "/api/v1/sessions?workspace_slug=x", "", 403, "/api/v1/sessions"},
		{"session create", "POST", "/api/v1/sessions", `{"workspace_slug":"x"}`, 404, "/api/v1/sessions"},
		{"session complete", "POST", "/api/v1/sessions/" + xID + "/complete", `{"status":"completed"}`, 403, "/api/v1/sessions/:id/complete"},
		{"session usage report", "POST", "/api/v1/sessions/" + xID + "/usage", `{"model":"m","input_tokens":1}`, 403, "/api/v1/sessions/:id/usage"},
		{"workspace cost", "GET", "/api/v1/workspaces/x/cost", "", 403, "/api/v1/workspaces/:slug/cost"},
		{"audit query of x", "GET", "/api/v1/audit?workspace=x", "", 404, "/api/v1/audit"},
	}
	for _, c := range cases {
		before := len(reports)
		rec := env.doJSON(t, c.method, c.path, c.body, bound)
		if rec.Code != c.status {
			t.Errorf("%s: status %d; want %d: %s", c.name, rec.Code, c.status, rec.Body)
			continue
		}
		if got := len(reports) - before; got != 1 {
			t.Errorf("%s: %d reports; want exactly 1", c.name, got)
			continue
		}
		want := wsaccess.BoundRefusal{TokenID: "tokB", UserID: "u1", BoundSlug: "s", RequestedSlug: "x", Method: c.method,
			Route: c.route, Status: c.status, Surface: wsaccess.SurfaceAPI}
		if r := reports[before]; r != want {
			t.Errorf("%s: report %+v; want %+v", c.name, r, want)
		}
	}
	before := len(reports)
	for _, path := range []string{"/api/v1/sessions", "/api/v1/audit"} {
		if rec := env.doJSON(t, http.MethodGet, path, "", bound); rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d", path, rec.Code)
		}
	}
	if got := len(reports) - before; got != 0 {
		t.Errorf("filtered lists produced %d reports; want 0", got)
	}
}
