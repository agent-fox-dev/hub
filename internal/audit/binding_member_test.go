package audit

import (
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// ---------------------------------------------------------------------------
// Spec 36 — bound tokens of org members, and refusal reports
// Requirements: 36-REQ-8
// ---------------------------------------------------------------------------

// recordRefusals records the wsaccess bound-refusal reports for the rest
// of the test.
func recordRefusals(t *testing.T) *[]wsaccess.BoundRefusal {
	t.Helper()
	var reports []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { reports = append(reports, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })
	return &reports
}

// newBoundMemberEnv is newMemberQueryEnv with a session in each of w,
// w-own, w-x and a workspace "missing" that does not exist, and u2's token
// tokM bound to w, the workspace u2 reads as a member of O.
func newBoundMemberEnv(t *testing.T) *auditTestEnv {
	t.Helper()
	env, _ := newMemberQueryEnv(t)
	for _, ws := range []string{"w", "w-own", "w-x", "missing"} {
		env.seedSession(t, &Session{ID: "sess-" + ws, WorkspaceSlug: ws, Status: "active", CredentialID: "u1", CredentialType: "pat"})
	}
	bindToken(t, env.sqliteDB, "tokM", "w")
	return env
}

// boundMember is u2's PAT bound to w, with every scope these routes need.
var boundMember = boundPatAuth("u2", "tokM", "audit:read", "sessions:read", "sessions:write")

// TS-36-35: a member's PAT bound to the member workspace reads it on the
// per-run, session and audit routes. Requirement: 36-REQ-8.1
func TestBoundMember_ReadsBoundWorkspace_TS_36_35(t *testing.T) {
	env := newBoundMemberEnv(t)
	for _, path := range []string{
		"/api/v1/workspaces/w/runs/" + testRunID + "/events",
		"/api/v1/sessions/sess-w",
		"/api/v1/sessions/sess-w/usage",
		"/api/v1/workspaces/w/cost",
		"/api/v1/sessions?workspace_slug=w",
		"/api/v1/audit?workspace=w",
	} {
		if rec := env.doJSON(t, http.MethodGet, path, "", boundMember); rec.Code != http.StatusOK {
			t.Errorf("%s: (%d, %s); want 200", path, rec.Code, rec.Body)
		}
	}
}

// boundRefusalCase is one request of a bound token naming slug, with the
// answer, the error message and the route template of its report.
type boundRefusalCase struct {
	name, method, path, body string
	status                   int
	message, route           string
}

// boundRefusalCases are the requests of the routes of this spec that name
// the workspace slug, whose session is sess-<slug>.
func boundRefusalCases(slug string) []boundRefusalCase {
	run := "/api/v1/workspaces/" + slug + "/runs/" + testRunID
	return []boundRefusalCase{
		{"run events", http.MethodGet, run + "/events", "", 404, "workspace not found", "/api/v1/workspaces/:slug/runs/:run_id/events"},
		{"run traces", http.MethodGet, run + "/traces", "", 404, "workspace not found", "/api/v1/workspaces/:slug/runs/:run_id/traces"},
		{"transcript", http.MethodGet, run + "/transcript?node_id=n", "", 404, "workspace not found", "/api/v1/workspaces/:slug/runs/:run_id/transcript"},
		{"open session", http.MethodPost, "/api/v1/sessions", `{"workspace_slug":"` + slug + `"}`, 404, "workspace not found", "/api/v1/sessions"},
		{"audit filter", http.MethodGet, "/api/v1/audit?workspace=" + slug, "", 404, "workspace not found", "/api/v1/audit"},
		{"session", http.MethodGet, "/api/v1/sessions/sess-" + slug, "", 403, "workspace access denied", "/api/v1/sessions/:id"},
		{"session usage", http.MethodGet, "/api/v1/sessions/sess-" + slug + "/usage", "", 403, "workspace access denied", "/api/v1/sessions/:id/usage"},
		{"session list", http.MethodGet, "/api/v1/sessions?workspace_slug=" + slug, "", 403, "workspace access denied", "/api/v1/sessions"},
		{"cost", http.MethodGet, "/api/v1/workspaces/" + slug + "/cost", "", 403, "workspace access denied", "/api/v1/workspaces/:slug/cost"},
	}
}

// TS-36-36: a bound PAT naming a workspace other than its bound one gets
// the route's usual refusal, even for a workspace its user owns.
// Requirement: 36-REQ-8.2
func TestBoundMember_OtherWorkspaceRefused_TS_36_36(t *testing.T) {
	env := newBoundMemberEnv(t)
	before := queryTableCount(t, env.db, "agent_sessions")
	for _, c := range boundRefusalCases("w-own") {
		rec := env.doJSON(t, c.method, c.path, c.body, boundMember)
		if rec.Code != c.status || errOf(t, rec).Error.Message != c.message {
			t.Errorf("%s: (%d, %s); want %d %s", c.name, rec.Code, rec.Body, c.status, c.message)
		}
	}
	if after := queryTableCount(t, env.db, "agent_sessions"); after != before {
		t.Errorf("session rows %d -> %d; want unchanged", before, after)
	}
}

// TS-36-37: each request refused because of the binding is reported exactly
// once, with its method, route template and status, whether the requested
// workspace is readable, unreadable or missing. Requirement: 36-REQ-8.3
func TestBoundMember_OneReportPerRefusal_TS_36_37(t *testing.T) {
	reports := recordRefusals(t)
	env := newBoundMemberEnv(t)
	for _, slug := range []string{"w-own", "w-x", "missing"} {
		cases := append(boundRefusalCases(slug), boundRefusalCase{
			"stream filter", http.MethodGet, sseEndpoint + "?workspace=" + slug, "", 404, "workspace not found", "/api/v1/events"})
		for _, c := range cases {
			n := len(*reports)
			var status int
			if c.name == "stream filter" {
				status = refusedStream(t, env, "?workspace="+slug, boundMember).Code
			} else {
				status = env.doJSON(t, c.method, c.path, c.body, boundMember).Code
			}
			if status != c.status {
				t.Errorf("%s of %s: status %d; want %d", c.name, slug, status, c.status)
				continue
			}
			if got := len(*reports) - n; got != 1 {
				t.Errorf("%s of %s: %d reports; want exactly 1", c.name, slug, got)
				continue
			}
			want := wsaccess.BoundRefusal{TokenID: "tokM", UserID: "u2", BoundSlug: "w", RequestedSlug: slug,
				Method: c.method, Route: c.route, Status: c.status, Surface: wsaccess.SurfaceAPI}
			if r := (*reports)[n]; r != want {
				t.Errorf("%s of %s: report %+v; want %+v", c.name, slug, r, want)
			}
		}
	}
}

// TS-36-38: a bound PAT's session list, audit query and stream inside its
// binding answer 200 and make no refusal report. Requirement: 36-REQ-8.4
func TestBoundMember_FilteringReportsNothing_TS_36_38(t *testing.T) {
	reports := recordRefusals(t)
	env, mgr := newMemberQueryEnv(t)
	bindToken(t, env.sqliteDB, "tokM", "w")
	for _, path := range []string{"/api/v1/sessions", "/api/v1/audit", "/api/v1/audit?workspace=w"} {
		if rec := env.doJSON(t, http.MethodGet, path, "", boundMember); rec.Code != http.StatusOK {
			t.Errorf("%s: (%d, %s); want 200", path, rec.Code, rec.Body)
		}
	}
	openMemberStream(t, memberStreamServer(t, env), mgr, boundMember, 1)
	if len(*reports) != 0 {
		t.Errorf("%d refusal reports %+v; want 0", len(*reports), *reports)
	}
}
