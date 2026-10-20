package sandboxes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// leaseRoute is one of the five lease routes.
type leaseRoute struct {
	name, method, path, body string
	write                    bool // needs sandboxes:write rather than read access
	workspaceScoped          bool
}

// leaseRoutes returns the five lease routes for workspace slug and lease id.
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

// TS-26-8: Unauthenticated requests to every lease route answer 401 and change
// nothing.
// Requirement: 26-REQ-2.1
func TestAccess_Unauthenticated_TS_26_8(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	id := env.mustIssue()["id"].(string)
	before := env.readState(id)

	for _, r := range leaseRoutes("widgets", id) {
		rec := env.do(r.method, r.path, r.body, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d; want 401", r.name, rec.Code)
			continue
		}
		if obj, ok := jsonObject(t, rec)["error"].(map[string]any); !ok || obj["code"] != float64(401) {
			t.Errorf("%s: body %s is not an apikit 401 envelope", r.name, rec.Body)
		}
	}
	if env.countLeases() != 1 || env.readState(id) != before {
		t.Errorf("an unauthenticated request changed the leases: %d rows, %+v", env.countLeases(), env.readState(id))
	}

	// With the feature off the answer is still 401, not sandboxes_disabled.
	off := newDisabledEcho()
	for _, r := range leaseRoutes("widgets", id) {
		if rec := serve(off, r.method, r.path, r.body, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("feature off, %s: status %d; want 401", r.name, rec.Code)
		}
	}
}

// newDisabledEcho mounts the lease routes with a nil service, the state of a hub
// without a [sandbox] section.
func newDisabledEcho() *echo.Echo {
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	RegisterLeaseRoutes(api, nil)
	return e
}

// serve sends a request to e as auth (unauthenticated when nil).
func serve(e *echo.Echo, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	env := &leaseEnv{echo: e, t: nil}
	return env.serve(method, path, body, auth)
}

// TS-26-9: With the feature off every authenticated lease route answers 404
// sandboxes_disabled before the scope and workspace checks.
// Requirement: 26-REQ-2.2
func TestAccess_FeatureOff_TS_26_9(t *testing.T) {
	off := newDisabledEcho()
	for name, auth := range map[string]*apikit.AuthInfo{
		"PAT without scopes": patAuth("secrets:list"),
		"PAT with both":      patAuth("sandboxes:read", "sandboxes:write"),
		"API key":            apiKeyAuth(),
		"admin token":        adminAuth(),
	} {
		for _, r := range leaseRoutes("nosuch", leaseID(1)) {
			rec := serve(off, r.method, r.path, r.body, auth)
			if rec.Code != http.StatusNotFound || errorType(t, rec) != "sandboxes_disabled" {
				t.Errorf("%s, %s: status %d, error_type %q; want 404 sandboxes_disabled", name, r.name, rec.Code, errorType(t, rec))
			}
			if message(t, rec) != "sandboxes are not enabled on this hub" {
				t.Errorf("%s, %s: message %q", name, r.name, message(t, rec))
			}
		}
	}
}

// TS-26-10: A PAT lacking the required scope gets 403 before the workspace
// lookup; either sandbox scope reads, only sandboxes:write changes.
// Requirement: 26-REQ-2.3
func TestAccess_PATScopes_TS_26_10(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	id := env.mustIssue()["id"].(string)
	before := env.readState(id)
	readOnly, writeOnly, none := patAuth("sandboxes:read"), patAuth("sandboxes:write"), patAuth("secrets:list")

	// POST and DELETE need sandboxes:write, even for a workspace that does not exist.
	for _, slug := range []string{"widgets", "nosuch"} {
		for _, r := range leaseRoutes(slug, id) {
			if !r.write {
				continue
			}
			if rec := env.do(r.method, r.path, r.body, readOnly); rec.Code != http.StatusForbidden {
				t.Errorf("%s on %s with sandboxes:read only: status %d; want 403", r.name, slug, rec.Code)
			}
		}
	}
	// A GET needs sandboxes:read or sandboxes:write.
	for _, r := range leaseRoutes("widgets", id) {
		if r.write || !r.workspaceScoped {
			continue
		}
		if rec := env.do(r.method, r.path, r.body, none); rec.Code != http.StatusForbidden {
			t.Errorf("%s with no sandbox scope: status %d; want 403", r.name, rec.Code)
		}
		for name, pat := range map[string]*apikit.AuthInfo{"sandboxes:read": readOnly, "sandboxes:write": writeOnly} {
			if rec := env.do(r.method, r.path, r.body, pat); rec.Code != http.StatusOK {
				t.Errorf("%s with %s: status %d; want 200", r.name, name, rec.Code)
			}
		}
	}
	// A PAT with sandboxes:write can issue and end.
	if rec := env.do(http.MethodPost, "/api/v1/workspaces/widgets/sandboxes", `{"profile":"tools"}`, writeOnly); rec.Code != http.StatusCreated {
		t.Errorf("POST with sandboxes:write: status %d; want 201: %s", rec.Code, rec.Body)
	}
	if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", writeOnly); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE with sandboxes:write: status %d; want 204", rec.Code)
	}

	// The refusals created and changed nothing: the one lease issued above and the
	// one ended above are the only changes.
	if env.countLeases() != 2 || env.readState(id) == before {
		t.Errorf("%d leases; want the original (now ended) and the one the write-scoped PAT issued", env.countLeases())
	}
}

// TS-26-13: GET list, GET one and DELETE work on an archived workspace.
// Requirement: 26-REQ-2.6
func TestAccess_ArchivedWorkspace_TS_26_13(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	id := env.mustIssue()["id"].(string)
	if _, err := env.db.Exec(`UPDATE workspaces SET status = 'archived' WHERE slug = 'widgets'`); err != nil {
		t.Fatal(err)
	}

	list := leaseList(t, env.get("/api/v1/workspaces/widgets/sandboxes", apiKeyAuth()))
	if len(list) != 1 || list[0]["id"] != id {
		t.Errorf("list on an archived workspace = %v; want the lease", ids(list))
	}
	if rec := env.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth()); rec.Code != http.StatusOK {
		t.Errorf("GET one: status %d; want 200", rec.Code)
	}
	if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE: status %d; want 204", rec.Code)
	}
	if rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth()); rec.Code != http.StatusBadRequest {
		t.Errorf("POST on the archived workspace: status %d; want 400", rec.Code)
	}
}
