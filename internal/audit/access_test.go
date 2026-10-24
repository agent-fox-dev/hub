package audit

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// Admin tokens and API keys hold every scope; a PAT holds the scopes it was
// granted, and a write scope implies the read scope of its resource.
func TestScopes_WriteImpliesRead(t *testing.T) {
	for name, c := range map[string]struct {
		auth                                   *apikit.AuthInfo
		sessRead, sessWrite, audRead, audWrite bool
	}{
		"admin":          {admin, true, true, true, true},
		"api key":        {key, true, true, true, true},
		"no scopes":      {testutil.PAT("u"), false, false, false, false},
		"sessions:read":  {testutil.PAT("u", "sessions:read"), true, false, false, false},
		"sessions:write": {testutil.PAT("u", "sessions:write"), true, true, false, false},
		"audit:read":     {testutil.PAT("u", "audit:read"), false, false, true, false},
		"audit:write":    {testutil.PAT("u", "audit:write"), false, false, true, true},
	} {
		got := [4]bool{hasSessionsRead(c.auth), hasSessionsWrite(c.auth), hasAuditRead(c.auth), hasAuditWrite(c.auth)}
		if got != [4]bool{c.sessRead, c.sessWrite, c.audRead, c.audWrite} {
			t.Errorf("%s: (sessions read %v, write %v, audit read %v, write %v)", name, got[0], got[1], got[2], got[3])
		}
	}
	perms := Permissions()
	seen := map[string]bool{}
	for _, p := range perms {
		seen[p.Resource+":"+p.Action] = true
	}
	if len(perms) != 4 || !seen["audit:read"] || !seen["audit:write"] || !seen["sessions:read"] || !seen["sessions:write"] {
		t.Errorf("Permissions() = %v", perms)
	}
}

func TestValidators(t *testing.T) {
	for id, ok := range map[string]bool{"20260704_143022_a1b2c3": true, "20260704_143022_A1B2C3": false, "2026070_143022_a1b2c3": false,
		"20260704_143022_a1b2": false, "": false, "20260704-143022-a1b2c3": false} {
		if got := ValidateRunID(id); got != ok {
			t.Errorf("ValidateRunID(%q) = %v", id, got)
		}
		if err := ValidateRunIDErr(id); (err == nil) != ok {
			t.Errorf("ValidateRunIDErr(%q) = %v", id, err)
		}
	}
	for id, ok := range map[string]bool{uuidN(1): true, "550E8400-E29B-41D4-A716-446655440000": false, "nope": false, "": false} {
		if err := ValidateUUID(id); (err == nil) != ok {
			t.Errorf("ValidateUUID(%q) = %v", id, err)
		}
	}
	for s, ok := range map[string]bool{"info": true, "warning": true, "error": true, "critical": true, "debug": false, "": false} {
		if err := ValidateSeverity(s); (err == nil) != ok {
			t.Errorf("ValidateSeverity(%q) = %v", s, err)
		}
	}
	for et, ok := range map[string]bool{"session.init": true, "assistant.message": true, "tool.use": true, "tool.error": true,
		"session.result": true, "tool.result": false, "": false} {
		if err := ValidateTraceEventType(et); (err == nil) != ok {
			t.Errorf("ValidateTraceEventType(%q) = %v", et, err)
		}
	}
	for in, want := range map[string]string{"": "", "2026-09-01T10:00:00Z": "2026-09-01T10:00:00Z", "2026-09-01T12:00:00.123456789+02:00": "2026-09-01T10:00:00.123456789Z",
		"2026-09-01T10:00:00.500Z": "2026-09-01T10:00:00.5Z"} {
		if got, err := NormalizeTimestamp(in); err != nil || got != want {
			t.Errorf("NormalizeTimestamp(%q) = (%q, %v); want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"yesterday", "2026-09-01", "2026-09-01 10:00:00"} {
		if _, err := NormalizeTimestamp(bad); err == nil {
			t.Errorf("NormalizeTimestamp(%q) accepted", bad)
		}
		if _, err := ParseTimestamp(bad); err == nil {
			t.Errorf("ParseTimestamp(%q) accepted", bad)
		}
	}
	for et, want := range map[string]string{"session.fail": "error", "run.limit_reached": "warning", "git.conflict": "warning",
		"harvest.empty": "warning", "review.parse_failure": "warning", "session.start": "info", "custom": "info"} {
		if got := DefaultSeverityFor(et); got != want {
			t.Errorf("DefaultSeverityFor(%q) = %q; want %q", et, got, want)
		}
	}
	for et, want := range map[string]bool{"session.start": true, "review.parse_failure": true, "custom": false} {
		if got := isKnownAuditEventType(et); got != want {
			t.Errorf("isKnownAuditEventType(%q) = %v", et, got)
		}
	}
}

// Every refusal of a token bound to another workspace is reported exactly
// once with the request's method, route and status; listings that merely
// confine the token to its workspace report nothing.
func TestBoundRefusals_ReportedOnce(t *testing.T) {
	var reports []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { reports = append(reports, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })
	env := newEnv(t)
	env.seedSession(t, Session{ID: "sx", WorkspaceSlug: "w-arch"})
	run := "/api/v1/workspaces/w-arch/runs/" + runID
	for _, c := range []struct {
		name, method, path, body, route string
		status                          int
	}{
		{"ingestion", "POST", run + "/events", `{"event_type":"session.start"}`, "/api/v1/workspaces/:slug/runs/:run_id/events", 404},
		{"run read", "GET", run + "/events", "", "/api/v1/workspaces/:slug/runs/:run_id/events", 404},
		{"transcript", "GET", run + "/transcript?node_id=n", "", "/api/v1/workspaces/:slug/runs/:run_id/transcript", 404},
		{"session read", "GET", sessions + "/sx", "", "/api/v1/sessions/:id", 403},
		{"session usage read", "GET", sessions + "/sx/usage", "", "/api/v1/sessions/:id/usage", 403},
		{"session list", "GET", sessions + "?workspace_slug=w-arch", "", "/api/v1/sessions", 403},
		{"session open", "POST", sessions, `{"workspace_slug":"w-arch"}`, "/api/v1/sessions", 404},
		{"session complete", "POST", sessions + "/sx/complete", `{"status":"completed"}`, "/api/v1/sessions/:id/complete", 403},
		{"usage report", "POST", sessions + "/sx/usage", `{"model":"m"}`, "/api/v1/sessions/:id/usage", 403},
		{"cost", "GET", "/api/v1/workspaces/w-arch/cost", "", "/api/v1/workspaces/:slug/cost", 403},
		{"audit query", "GET", auditPath + "?workspace=w-arch", "", "/api/v1/audit", 404},
		{"stream", "GET", "/api/v1/events?workspace=w-arch", "", "/api/v1/events", 404},
	} {
		before := len(reports)
		rec := env.do(t, c.method, c.path, c.body, bound)
		if rec.Code != c.status {
			t.Errorf("%s: %d; want %d: %s", c.name, rec.Code, c.status, rec.Body)
			continue
		}
		if len(reports)-before != 1 {
			t.Errorf("%s: %d reports; want 1", c.name, len(reports)-before)
			continue
		}
		want := wsaccess.BoundRefusal{TokenID: "tokB", UserID: "u1", BoundSlug: "w", RequestedSlug: "w-arch", Method: c.method, Route: c.route, Status: c.status, Surface: wsaccess.SurfaceAPI}
		if r := reports[before]; r != want {
			t.Errorf("%s: %+v; want %+v", c.name, r, want)
		}
	}
	before := len(reports)
	env.must(t, http.StatusOK, http.MethodGet, sessions, "", bound)
	env.must(t, http.StatusOK, http.MethodGet, auditPath, "", bound)
	if len(reports) != before {
		t.Errorf("filtered listings reported %d refusals", len(reports)-before)
	}
}
