package forge

import (
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// allForgeRoutes are the forge read routes, the deliveries, the nine writes
// and the webhook set and unset, as writeRouteCases.
func allForgeRoutes() []writeRouteCase {
	var out []writeRouteCase
	for _, r := range forgeRoutes {
		out = append(out, writeRouteCase{r.name, http.MethodGet, r.path, ""})
	}
	out = append(out, writeRouteCase{"deliveries", http.MethodGet, "/workspaces/%s/forge/deliveries", ""})
	out = append(out, writeRouteCases...)
	return append(out,
		writeRouteCase{"set webhook", http.MethodPut, "/workspaces/%s/forge/webhook", `{}`},
		writeRouteCase{"unset webhook", http.MethodDelete, "/workspaces/%s/forge/webhook", ``})
}

// TS-35-52 (forge): A member's forge write on an archived workspace is 403;
// the owner's is today's 400.
// Requirement: 35-REQ-10.1
func TestMemberForgeWriteOnArchived_TS_35_52(t *testing.T) {
	e := memberEnv(t)
	if _, err := e.db.Exec(`UPDATE workspaces SET status = 'archived' WHERE slug = 'widgets'`); err != nil {
		t.Fatal(err)
	}
	route := writeRouteCases[0]
	if rec := e.sendRoute(route, "widgets", memberKey()); !isForbidden(t, rec) {
		t.Errorf("member %s: %d %s; want 403 forbidden", route.name, rec.Code, rec.Body)
	}
	rec := e.sendRoute(route, "widgets", apiKeyAuth())
	if rec.Code != http.StatusBadRequest || decodeError(t, rec).Message != "workspace is archived" {
		t.Errorf("owner %s: %d %s; want today's 400", route.name, rec.Code, rec.Body)
	}
}

// TS-35-53 (forge): A member's PAT without a forge scope gets today's 403
// without error_type, the same for an existing and an unknown slug.
// Requirement: 35-REQ-10.2
func TestMemberForgeScopeAnswers_TS_35_53(t *testing.T) {
	e := memberEnv(t)
	for _, r := range allForgeRoutes() {
		pat := memberPAT("workspaces:read")
		w := e.sendRoute(r, "widgets", pat)
		ghost := e.sendRoute(r, "ghost", pat)
		if w.Code != http.StatusForbidden || w.Body.String() != ghost.Body.String() || decodeError(t, w).ErrorType != "" {
			t.Errorf("%s: widgets %d %s, ghost %d %s; want the same 403 without error_type", r.name, w.Code, w.Body, ghost.Code, ghost.Body)
		}
	}
}

// TS-35-54 (forge): A stranger gets the unknown slug's 404 on every forge
// route.
// Requirement: 35-REQ-10.3
func TestStrangerForgeAnswers_TS_35_54(t *testing.T) {
	e := memberEnv(t)
	stranger := &apikit.AuthInfo{UserID: "user-stranger", CredentialType: "api_key"}
	for _, r := range allForgeRoutes() {
		w := e.sendRoute(r, "widgets", stranger)
		ghost := e.sendRoute(r, "ghost", stranger)
		if w.Code != http.StatusNotFound || w.Body.String() != ghost.Body.String() || !strings.Contains(w.Body.String(), `"workspace not found"`) {
			t.Errorf("%s: widgets %d %s, ghost %d %s; want the same 404", r.name, w.Code, w.Body, ghost.Code, ghost.Body)
		}
	}
}
