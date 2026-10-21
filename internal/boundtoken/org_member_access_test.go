package boundtoken

import (
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// countRows counts the rows of table.
func countRows(t *testing.T, env *testEnv, table string) int {
	t.Helper()
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TS-35-50: A member of the workspace's org minting or listing bound tokens
// gets 404 workspace not found; no token is minted and none is shown.
// Requirement: 35-REQ-9.6
func TestMemberMintAndList_TS_35_50(t *testing.T) {
	stub := newStub(http.StatusCreated, okAnswer)
	env := newTestEnv(t, Config{APIKit: stub})
	seedWorkspace(t, env.db, "w", "u1", "active")
	addOrgMember(t, env.db, "w", "O", "u2")
	seedPAT(t, env.db, patRow{id: "existing-tok", name: "svc", perms: `["workspaces:read"]`})
	bind(t, env.db, "existing-tok", "w")
	pats, bindings := countRows(t, env, "pats"), countRows(t, env, "token_workspace_bindings")

	for _, auth := range []*apikit.AuthInfo{apiKey("u2"), pat("u2", "T2", "tokens:manage", "tokens:read")} {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			rec := env.do(method, "/api/v1/workspaces/w/tokens", auth, `{"name":"n","permissions":["workspaces:read"]}`, nil)
			if rec.Code != http.StatusNotFound || parseError(t, rec).Error.Message != "workspace not found" {
				t.Errorf("%s %s: (%d, %s); want 404 workspace not found", auth.CredentialType, method, rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "existing-tok") {
				t.Errorf("%s %s: the body shows a token", auth.CredentialType, method)
			}
		}
	}
	if stub.calls != 0 || countRows(t, env, "pats") != pats || countRows(t, env, "token_workspace_bindings") != bindings {
		t.Errorf("apikit calls %d, pats %d -> %d, bindings %d -> %d; want nothing minted", stub.calls, pats, countRows(t, env, "pats"), bindings, countRows(t, env, "token_workspace_bindings"))
	}
}

// TS-35-51: Mint refuses a bound PAT and an admin token before the workspace
// lookup, with spec 34's answers, whether or not the workspace exists.
// Requirement: 35-REQ-9.7
func TestMintRefusalsBeforeLookup_TS_35_51(t *testing.T) {
	stub := newStub(http.StatusCreated, okAnswer)
	env := newTestEnv(t, Config{APIKit: stub})
	seedWorkspace(t, env.db, "w", "u1", "active")
	addOrgMember(t, env.db, "w", "O", "u2")
	bind(t, env.db, "TB", "w")
	reports := recordRefusals(t)

	for _, tc := range []struct {
		name              string
		auth              *apikit.AuthInfo
		msg, errType      string
		reportsPerAttempt int
	}{
		{"bound PAT", pat("u2", "TB", "tokens:manage"), "this token is bound to workspace w", "token_bound", 1},
		{"admin token", admin(), "admin tokens cannot mint bound tokens; a real user is required as the token's owner", "", 0},
	} {
		var bodies []string
		for _, slug := range []string{"w", "ghost"} {
			*reports = nil
			rec := env.do(http.MethodPost, "/api/v1/workspaces/"+slug+"/tokens", tc.auth, `{"name":"n"}`, nil)
			e := parseError(t, rec)
			if rec.Code != http.StatusForbidden || e.Error.Message != tc.msg || e.Error.ErrorType != tc.errType {
				t.Errorf("%s on %s: (%d, %s); want 403 %q error_type %q", tc.name, slug, rec.Code, rec.Body, tc.msg, tc.errType)
			}
			if len(*reports) != tc.reportsPerAttempt {
				t.Errorf("%s on %s: %d reports; want %d", tc.name, slug, len(*reports), tc.reportsPerAttempt)
			}
			bodies = append(bodies, rec.Body.String())
		}
		if bodies[0] != bodies[1] {
			t.Errorf("%s: the answer depends on the workspace existing: %s vs %s", tc.name, bodies[0], bodies[1])
		}
	}
	if stub.calls != 0 {
		t.Errorf("apikit calls = %d; want 0", stub.calls)
	}
}
