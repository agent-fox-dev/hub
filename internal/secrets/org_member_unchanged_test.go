package secrets

import (
	"net/http"
	"strings"
	"testing"
)

// TS-35-53 (secrets): A member's PAT without a route's scope gets today's 403
// without error_type, the same for an existing and an unknown slug.
// Requirement: 35-REQ-10.2
func TestMemberSecretScopeAnswers_TS_35_53(t *testing.T) {
	env := newMemberSecretsEnv(t)
	for _, r := range append(append([]route{}, workspaceSecretRoutes...), workspaceVarRoutes...) {
		pat := patAuth(sMember, "workspaces:read")
		w := env.doRequest(t, r.method, r.on("team-ws"), r.body, pat)
		ghost := env.doRequest(t, r.method, r.on("ghost"), r.body, pat)
		if w.Code != http.StatusForbidden || w.Body.String() != ghost.Body.String() || strings.Contains(w.Body.String(), "error_type") {
			t.Errorf("%s %s: team-ws %d %s, ghost %d %s; want the same 403 without error_type", r.method, r.path, w.Code, w.Body, ghost.Code, ghost.Body)
		}
	}
}

// TS-35-54 (secrets): A stranger gets the unknown slug's 404 on every
// workspace secret and variable route.
// Requirement: 35-REQ-10.3
func TestStrangerSecretAnswers_TS_35_54(t *testing.T) {
	env := newMemberSecretsEnv(t)
	for _, r := range append(append([]route{}, workspaceSecretRoutes...), workspaceVarRoutes...) {
		w := env.doRequest(t, r.method, r.on("team-ws"), r.body, userAuth(sStranger))
		ghost := env.doRequest(t, r.method, r.on("ghost"), r.body, userAuth(sStranger))
		if w.Code != http.StatusNotFound || w.Body.String() != ghost.Body.String() || strings.TrimSpace(w.Body.String()) != wantNotFound {
			t.Errorf("%s %s: team-ws %d %s, ghost %d %s; want the same 404", r.method, r.path, w.Code, w.Body, ghost.Code, ghost.Body)
		}
	}
}
