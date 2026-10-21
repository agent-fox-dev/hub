package sandboxes

import (
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// memberLeaseRoutes are the per-workspace lease routes; %s is the slug.
var memberLeaseRoutes = []struct{ method, path, body string }{
	{http.MethodGet, "/api/v1/workspaces/%s/sandboxes", ""},
	{http.MethodGet, "/api/v1/workspaces/%s/sandboxes/sbx_x", ""},
	{http.MethodPost, "/api/v1/workspaces/%s/sandboxes", `{"profile":"tools"}`},
	{http.MethodDelete, "/api/v1/workspaces/%s/sandboxes/sbx_x", ""},
}

// TS-35-53 (leases): A member's PAT without the sandbox scopes gets today's
// 403 without error_type, the same for an existing and an unknown slug.
// Requirement: 35-REQ-10.2
func TestMemberLeaseScopeAnswers_TS_35_53(t *testing.T) {
	e := newLeaseEnv(t, leaseEnvOptions{})
	addTeam(t, e)
	for _, r := range memberLeaseRoutes {
		pat := memberPAT("workspaces:read")
		w := e.do(r.method, strings.Replace(r.path, "%s", "team", 1), r.body, pat)
		ghost := e.do(r.method, strings.Replace(r.path, "%s", "ghost", 1), r.body, pat)
		if w.Code != http.StatusForbidden || w.Body.String() != ghost.Body.String() || errorType(t, w) != "" {
			t.Errorf("%s %s: team %d %s, ghost %d %s; want the same 403 without error_type", r.method, r.path, w.Code, w.Body, ghost.Code, ghost.Body)
		}
	}
}

// TS-35-54 (leases): A stranger gets the unknown slug's 404 on every lease
// route.
// Requirement: 35-REQ-10.3
func TestStrangerLeaseAnswers_TS_35_54(t *testing.T) {
	e := newLeaseEnv(t, leaseEnvOptions{})
	addTeam(t, e)
	stranger := &apikit.AuthInfo{CredentialType: "api_key", UserID: "stranger-3", KeyID: "key-3"}
	for _, r := range memberLeaseRoutes {
		w := e.do(r.method, strings.Replace(r.path, "%s", "team", 1), r.body, stranger)
		ghost := e.do(r.method, strings.Replace(r.path, "%s", "ghost", 1), r.body, stranger)
		if w.Code != http.StatusNotFound || w.Body.String() != ghost.Body.String() || message(t, w) != "workspace not found" {
			t.Errorf("%s %s: team %d %s, ghost %d %s; want the same 404", r.method, r.path, w.Code, w.Body, ghost.Code, ghost.Body)
		}
	}
}
