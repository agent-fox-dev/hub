package forge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// writeRouteCase is one of the nine write routes; path holds %s where the slug goes and
// a sample number where a number is taken, and body is a valid body for the route.
type writeRouteCase struct {
	name   string
	method string
	path   string
	body   string
}

var writeRouteCases = []writeRouteCase{
	{"create issue", http.MethodPost, "/workspaces/%s/issues", `{"title":"t","body":"b"}`},
	{"update issue", http.MethodPatch, "/workspaces/%s/issues/42", `{"title":"t"}`},
	{"close issue", http.MethodPost, "/workspaces/%s/issues/42/close", `{}`},
	{"comment on issue", http.MethodPost, "/workspaces/%s/issues/42/comments", `{"body":"b"}`},
	{"add labels", http.MethodPost, "/workspaces/%s/issues/42/labels", `["af:ready"]`},
	{"remove label", http.MethodDelete, "/workspaces/%s/issues/42/labels/af:fix", ``},
	{"ensure label", http.MethodPost, "/workspaces/%s/labels", `{"name":"af:triage"}`},
	{"open pull request", http.MethodPost, "/workspaces/%s/pulls", `{"title":"t","head":"feature","base":"main"}`},
	{"comment on pull request", http.MethodPost, "/workspaces/%s/pulls/42/comments", `{"body":"b"}`},
}

func (r writeRouteCase) url(slug string) string {
	return strings.Replace(r.path, "%s", slug, 1)
}

// send calls method on /api/v1<path> with the credential, or without one when auth is
// nil, and with body as a JSON body when it is not empty.
func (e *env) send(method, path string, auth *apikit.AuthInfo, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != nil {
		b, err := json.Marshal(auth)
		if err != nil {
			e.t.Fatalf("marshal auth: %v", err)
		}
		req.Header.Set("X-Test-Auth", string(b))
	}
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	return rec
}

// sendRoute is send for one of the nine routes, with its valid body.
func (e *env) sendRoute(r writeRouteCase, slug string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.send(r.method, r.url(slug), auth, r.body)
}

// refused reports whether a status is one of the refusals of the checks every write route
// starts with.
func refused(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound
}

// TS-31-2: A PAT without forge:write, including one holding only forge:read, is refused
// with 403 on every write route, before the workspace is looked up.
// Requirement: 31-REQ-1.2
func TestWriteRoutes_PATWithoutWriteScope_TS_31_2(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "widgets")

	const want = "PAT requires forge:write scope"
	for _, route := range writeRouteCases {
		for _, scopes := range [][]string{{"forge:read"}, {"workspaces:read"}, nil} {
			for _, slug := range []string{"widgets", "ghost"} {
				rec := e.sendRoute(route, slug, patAuth(scopes...))
				if rec.Code != http.StatusForbidden {
					t.Errorf("%s on %s as a PAT holding %v: status = %d; want 403\n%s", route.name, slug, scopes, rec.Code, rec.Body)
					continue
				}
				if got := decodeError(t, rec).Message; got != want {
					t.Errorf("%s on %s as a PAT holding %v: message = %q; want %q", route.name, slug, scopes, got, want)
				}
			}
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
	if e.originCalls.Load() != 0 || e.upstreamCalls.Load() != 0 {
		t.Error("a credential was resolved for a refused request")
	}

	// A PAT with forge:write alone is enough: it passes the scope check.
	for _, route := range writeRouteCases {
		rec := e.sendRoute(route, "widgets", patAuth("forge:write"))
		if refused(rec.Code) {
			t.Errorf("%s as a PAT holding forge:write: status = %d; want the checks passed", route.name, rec.Code)
		}
	}
}

// TS-31-3: A credential of a type hub does not know is denied the write scope check.
// Requirement: 31-REQ-1.3
func TestWriteRoutes_UnknownCredentialType_TS_31_3(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "widgets")

	service := &apikit.AuthInfo{UserID: ownerID, CredentialType: "service", Permissions: []string{"forge:write", "forge:read"}}
	for _, route := range writeRouteCases {
		rec := e.sendRoute(route, "widgets", service)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s as a credential of type service: status = %d; want 403\n%s", route.name, rec.Code, rec.Body)
			continue
		}
		if msg := decodeError(t, rec).Message; !strings.Contains(msg, "service") {
			t.Errorf("%s: message = %q; want it to name the credential type", route.name, msg)
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
}

// TS-31-5: A workspace that does not exist, and one the caller does not own, answer
// 404 workspace not found on every write route, alike, and no credential is resolved.
// Requirement: 31-REQ-1.5
func TestWriteRoutes_MissingOrNotOwnedWorkspace_TS_31_5(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "widgets")
	addWorkspace(t, e.db, "theirs", func(r *workspaceRow) { r.owner = otherID })

	for _, route := range writeRouteCases {
		for _, slug := range []string{"theirs", "ghost"} {
			rec := e.sendRoute(route, slug, patAuth("forge:write"))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s on %s: status = %d; want 404\n%s", route.name, slug, rec.Code, rec.Body)
				continue
			}
			if got := decodeError(t, rec).Message; got != "workspace not found" {
				t.Errorf("%s on %s: message = %q; want %q", route.name, slug, got, "workspace not found")
			}
		}
	}
	if e.originCalls.Load() != 0 || e.upstreamCalls.Load() != 0 {
		t.Error("a credential was resolved for a refused request")
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}

	// The workspace is reachable by its owner, and by an admin token, which may act on
	// any workspace: the 404 above is the ownership check and not the route.
	for _, route := range writeRouteCases {
		if rec := e.sendRoute(route, "widgets", apiKeyAuth()); refused(rec.Code) {
			t.Errorf("%s on the caller's own workspace: status = %d; want it past the checks", route.name, rec.Code)
		}
		if rec := e.sendRoute(route, "theirs", adminAuth()); refused(rec.Code) {
			t.Errorf("%s on another user's workspace as an admin token: status = %d; want it past the checks", route.name, rec.Code)
		}
	}
}

// TS-31-6: A workspace that is not active, archived for one, answers 400 workspace is
// not active on every write route, after the ownership check and before the body is read.
// Requirement: 31-REQ-1.6
func TestWriteRoutes_InactiveWorkspace_TS_31_6(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "retired", func(r *workspaceRow) { r.status = "archived" })
	addWorkspace(t, e.db, "theirs", func(r *workspaceRow) { r.status = "archived"; r.owner = otherID })
	addWorkspace(t, e.db, "widgets")

	const want = "workspace is not active"
	for _, route := range writeRouteCases {
		rec := e.sendRoute(route, "retired", apiKeyAuth())
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s on an archived workspace: status = %d; want 400\n%s", route.name, rec.Code, rec.Body)
			continue
		}
		if got := decodeError(t, rec).Message; got != want {
			t.Errorf("%s on an archived workspace: message = %q; want %q", route.name, got, want)
		}

		// The request is not read: a body that is not JSON is still the inactive 400.
		bad := e.send(route.method, route.url("retired"), apiKeyAuth(), "{not json")
		if got := decodeError(t, bad).Message; bad.Code != http.StatusBadRequest || got != want {
			t.Errorf("%s with a broken body on an archived workspace: %d %q; want 400 %q", route.name, bad.Code, got, want)
		}

		// Ownership comes first: another user's archived workspace is not found.
		if other := e.sendRoute(route, "theirs", apiKeyAuth()); other.Code != http.StatusNotFound {
			t.Errorf("%s on another user's archived workspace: status = %d; want 404", route.name, other.Code)
		}

		// The same request on an active workspace is not refused as inactive.
		active := e.sendRoute(route, "widgets", apiKeyAuth())
		if active.Code == http.StatusBadRequest && strings.Contains(active.Body.String(), want) {
			t.Errorf("%s on an active workspace: refused as not active", route.name)
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
	if e.originCalls.Load() != 0 {
		t.Error("a credential was resolved for a refused request")
	}
}

// A request without a credential is 401 on every write route, before anything else.
// Requirement: 31-REQ-1.1
func TestWriteRoutes_NoCredential_401(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "widgets")
	for _, route := range writeRouteCases {
		if rec := e.sendRoute(route, "widgets", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a credential: status = %d; want 401", route.name, rec.Code)
		}
	}
}
