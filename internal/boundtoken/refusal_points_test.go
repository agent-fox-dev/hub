package boundtoken

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// TS-34-58: Every refusal point of the running hub reports exactly once per
// refused request, with the request's details, and list filtering reports
// nothing. (lookupWorkspaceForAuth, the git server and the audit refusals that
// spec 33's guard keeps out of reach over HTTP are covered in their packages
// under the same test id.) Requirement: 34-REQ-10.3
func TestRefusalPoints_ReportOnce_TS_34_58(t *testing.T) {
	reports := recordRefusals(t)
	h := newRealHub(t)
	h.seed(t, "S", "ready")
	h.seed(t, "X", "ready")
	token, id := h.mintBound(t, "S", "workspaces:read", "sessions:read", "sessions:write", "audit:read", "tokens:read", "tokens:manage", "secrets:read")
	rec := h.do(http.MethodPost, "/api/v1/sessions", h.apiKey, `{"workspace_slug":"X"}`)
	var xs struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &xs); rec.Code != http.StatusCreated || err != nil {
		t.Fatalf("seeding a session in X: (%d, %s)", rec.Code, rec.Body)
	}
	*reports = nil

	cases := []struct {
		name, method, path, body string
		status                   int
		route, requested, user   string
	}{
		{"binding guard 404", "GET", "/api/v1/workspaces/X/patches", "", 404, "/api/v1/workspaces/:slug/patches", "X", "u1"},
		{"binding guard 403", "GET", "/api/v1/user/secrets", "", 403, "/api/v1/user/secrets", "", "u1"},
		{"apikit route guard", "GET", "/api/v1/user", "", 403, "/api/v1/user", "", ""},
		{"session read", "GET", "/api/v1/sessions/" + xs.ID, "", 403, "/api/v1/sessions/:id", "X", "u1"},
		{"session list of X", "GET", "/api/v1/sessions?workspace_slug=X", "", 403, "/api/v1/sessions", "X", "u1"},
		{"session create", "POST", "/api/v1/sessions", `{"workspace_slug":"X"}`, 404, "/api/v1/sessions", "X", "u1"},
		{"session complete", "POST", "/api/v1/sessions/" + xs.ID + "/complete", `{"status":"completed"}`, 403, "/api/v1/sessions/:id/complete", "X", "u1"},
		{"audit query of X", "GET", "/api/v1/audit?workspace=X", "", 404, "/api/v1/audit", "X", "u1"},
		{"mint", "POST", "/api/v1/workspaces/S/tokens", `{"name":"n","permissions":["workspaces:read"]}`, 403, "/api/v1/workspaces/:slug/tokens", "S", "u1"},
		{"list", "GET", "/api/v1/workspaces/S/tokens", "", 403, "/api/v1/workspaces/:slug/tokens", "S", "u1"},
	}
	for _, c := range cases {
		before := len(*reports)
		rec := h.do(c.method, c.path, token, c.body)
		if rec.Code != c.status {
			t.Errorf("%s: status %d; want %d: %s", c.name, rec.Code, c.status, rec.Body)
			continue
		}
		if got := len(*reports) - before; got != 1 {
			t.Errorf("%s: %d reports; want exactly 1", c.name, got)
			continue
		}
		want := wsaccess.BoundRefusal{TokenID: id, UserID: c.user, BoundSlug: "S", RequestedSlug: c.requested,
			Method: c.method, Route: c.route, Status: c.status, Surface: wsaccess.SurfaceAPI}
		if r := (*reports)[before]; r != want {
			t.Errorf("%s: report %+v; want %+v", c.name, r, want)
		}
	}

	// AuthorizeWorkspace has no request: it reports without method and route.
	before := len(*reports)
	if _, code, _ := wsaccess.AuthorizeWorkspace(h.db, &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: id}, "X", wsaccess.NeedPrivate); code != http.StatusNotFound {
		t.Fatalf("AuthorizeWorkspace(X) = %d; want 404", code)
	}
	if got := (*reports)[before:]; len(got) != 1 || got[0] != (wsaccess.BoundRefusal{TokenID: id, UserID: "u1", BoundSlug: "S", RequestedSlug: "X", Status: 404, Surface: wsaccess.SurfaceAPI}) {
		t.Errorf("AuthorizeWorkspace reports %+v; want exactly one for X", got)
	}

	// Filtering a list is not a refusal.
	before = len(*reports)
	for _, path := range []string{"/api/v1/workspaces", "/api/v1/sessions", "/api/v1/audit"} {
		if rec := h.do(http.MethodGet, path, token, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s: (%d, %s); want 200", path, rec.Code, rec.Body)
		}
	}
	if got := len(*reports) - before; got != 0 {
		t.Errorf("filtered lists produced %d reports; want 0", got)
	}
}
