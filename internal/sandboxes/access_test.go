package sandboxes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// leaseRoute is one of the five lease routes.
type leaseRoute struct {
	name, method, path, body string
	write, workspaceScoped   bool
}

func leaseRoutes(slug, id string) []leaseRoute {
	base := "/api/v1/workspaces/" + slug + "/sandboxes"
	return []leaseRoute{
		{"POST", http.MethodPost, base, `{"profile":"tools"}`, true, true},
		{"GET list", http.MethodGet, base, "", false, true},
		{"GET one", http.MethodGet, base + "/" + id, "", false, true},
		{"DELETE", http.MethodDelete, base + "/" + id, "", true, true},
		{"GET /sandboxes", http.MethodGet, "/api/v1/sandboxes", "", false, false},
	}
}

// Every lease route refuses in order: no credential 401, a hub without [sandbox]
// 404 sandboxes_disabled, a PAT without the scope 403 before the workspace lookup
// (write needs sandboxes:write, read either scope). Refusals change nothing.
func TestAccess_RefusalsInOrder(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	id := env.issue("")
	before := env.readState(id)
	off, api := testutil.API()
	RegisterLeaseRoutes(api, nil)

	for _, r := range leaseRoutes("widgets", id) {
		if rec := env.do(r.method, r.path, r.body, nil); rec.Code != http.StatusUnauthorized || testutil.Error(t, rec).Code != 401 {
			t.Errorf("%s unauthenticated: %d %s; want 401", r.name, rec.Code, rec.Body)
		}
		if rec := testutil.Request(t, off, r.method, r.path, r.body, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s unauthenticated, feature off: %d; want 401", r.name, rec.Code)
		}
		for _, auth := range []*apikit.AuthInfo{pat("secrets:read"), pat("sandboxes:read", "sandboxes:write"), ownerKey(), adminAuth()} {
			rec := testutil.Request(t, off, r.method, r.path, r.body, auth)
			if rec.Code != http.StatusNotFound || errorType(t, rec) != "sandboxes_disabled" || message(t, rec) != "sandboxes are not enabled on this hub" {
				t.Errorf("%s feature off, %s: %d %s", r.name, auth.CredentialType, rec.Code, rec.Body)
			}
		}
		if !r.workspaceScoped {
			continue
		}
		if rec := env.do(r.method, r.path, r.body, pat("secrets:read")); rec.Code != http.StatusForbidden || !strings.Contains(message(t, rec), "scope") {
			t.Errorf("%s with no sandbox scope: %d %q; want 403", r.name, rec.Code, message(t, rec))
		}
		if r.write {
			for _, slug := range []string{"widgets", "nosuch"} {
				if rec := env.do(r.method, strings.Replace(r.path, "widgets", slug, 1), r.body, pat("sandboxes:read")); rec.Code != http.StatusForbidden || message(t, rec) != "PAT requires sandboxes:write scope" {
					t.Errorf("%s on %s with sandboxes:read: %d %q; want 403 before the workspace lookup", r.name, slug, rec.Code, message(t, rec))
				}
			}
		} else {
			for _, scope := range []string{"sandboxes:read", "sandboxes:write"} {
				if rec := env.do(r.method, r.path, r.body, pat(scope)); rec.Code != http.StatusOK {
					t.Errorf("%s with %s: %d; want 200", r.name, scope, rec.Code)
				}
			}
		}
	}
	if env.countLeases() != 1 || env.readState(id) != before {
		t.Error("a refusal changed the leases")
	}
	env.mustIssueAs("widgets", "", pat("sandboxes:write"))
	if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", pat("sandboxes:write")); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE with sandboxes:write: %d", rec.Code)
	}
}

// GET list, GET one and DELETE work on an archived workspace; POST does not.
func TestAccess_ArchivedWorkspace(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	id := env.issue("")
	testutil.Exec(t, env.db, `UPDATE workspaces SET status = 'archived' WHERE slug = 'widgets'`)
	if list := leaseList(t, env.get("/api/v1/workspaces/widgets/sandboxes", ownerKey())); len(list) != 1 {
		t.Errorf("list = %v", ids(list))
	}
	if rec := env.get("/api/v1/workspaces/widgets/sandboxes/"+id, ownerKey()); rec.Code != http.StatusOK {
		t.Errorf("GET one: %d", rec.Code)
	}
	if rec := env.delete(id); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE: %d", rec.Code)
	}
	if rec := env.post("widgets", "{}", ownerKey()); rec.Code != http.StatusBadRequest {
		t.Errorf("POST: %d; want 400", rec.Code)
	}
}

// An org member reads leases with the owner's bodies and is refused writes with
// 403 forbidden; a member's PAT without the scope gets that 403 for an existing
// and an unknown slug alike; a stranger gets the unknown slug's 404 everywhere.
func TestAccess_OrgMemberAndStranger(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	env.addWorkspace("team", "O", "ready")
	env.addMember("O", "member-2")
	env.plantSecret("workspace", "team", "GIT_PAT", "TEAM-VALUE-456")
	id := env.mustIssueAs("team", "", ownerKey())["id"].(string)
	env.mustCreate(id)
	member, memberPAT := testutil.User("member-2"), testutil.PAT("member-2", "sandboxes:read", "sandboxes:write")
	rows, row, creates := env.countLeases(), rowText(t, env.db, id), env.prov.creates()

	for _, path := range []string{"/api/v1/workspaces/team/sandboxes", "/api/v1/workspaces/team/sandboxes/" + id} {
		asOwner := env.get(path, ownerKey())
		for _, auth := range []*apikit.AuthInfo{member, testutil.PAT("member-2", "sandboxes:read")} {
			if asMember := env.get(path, auth); asMember.Code != http.StatusOK || asMember.Body.String() != asOwner.Body.String() || !strings.Contains(asOwner.Body.String(), `"GIT_PAT"`) {
				t.Errorf("GET %s as a member: %d %s; want the owner's %s", path, asMember.Code, asMember.Body, asOwner.Body)
			}
		}
	}
	for _, auth := range []*apikit.AuthInfo{member, memberPAT} {
		for name, rec := range map[string]*httptest.ResponseRecorder{
			"POST":   env.post("team", "{}", auth),
			"DELETE": env.do(http.MethodDelete, "/api/v1/workspaces/team/sandboxes/"+id, "", auth),
		} {
			if rec.Code != http.StatusForbidden || errorType(t, rec) != "forbidden" || message(t, rec) != "org members have read-only access to this workspace" {
				t.Errorf("%s as a member: %d %s; want 403 forbidden", name, rec.Code, rec.Body)
			}
		}
	}
	if env.countLeases() != rows || rowText(t, env.db, id) != row || env.prov.creates() != creates || len(env.prov.live()) != 1 {
		t.Error("a member's write changed a lease or reached the backend")
	}

	stranger := testutil.User("stranger-3")
	for _, r := range leaseRoutes("team", id) {
		if !r.workspaceScoped {
			continue
		}
		ghost := strings.Replace(r.path, "team", "ghost", 1)
		w, g := env.do(r.method, r.path, r.body, testutil.PAT("member-2", "workspaces:read")), env.do(r.method, ghost, r.body, testutil.PAT("member-2", "workspaces:read"))
		if w.Code != http.StatusForbidden || w.Body.String() != g.Body.String() || errorType(t, w) != "" {
			t.Errorf("%s, member PAT without the scope: team %d %s, ghost %d %s; want the same 403 without error_type", r.name, w.Code, w.Body, g.Code, g.Body)
		}
		w, g = env.do(r.method, r.path, r.body, stranger), env.do(r.method, ghost, r.body, stranger)
		if w.Code != http.StatusNotFound || w.Body.String() != g.Body.String() || message(t, w) != "workspace not found" {
			t.Errorf("%s, stranger: team %d %s, ghost %d %s; want the same 404", r.name, w.Code, w.Body, g.Code, g.Body)
		}
	}
}
