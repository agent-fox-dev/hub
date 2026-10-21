package workspace

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// smokeCall sends one request over HTTP to a running hub.
func smokeCall(t *testing.T, base, method, path, credential, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// errorOf decodes an apikit error envelope from a body.
func errorOf(t *testing.T, body string) errorEnvelope {
	t.Helper()
	var e errorEnvelope
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("not an error envelope: %v: %s", err, body)
	}
	return e
}

// TS-33-56 (smoke, 33-PATH-1): A bound token reads its own workspace and is
// refused another through the running hub; its unbound twin reaches both.
// Real: apikit server and auth middleware, binding guard, workspace handlers,
// SQLite.
func TestSmoke_BoundTokenOwnAndOtherWorkspace_TS_33_56(t *testing.T) {
	h := newRealHub(t)
	h.seed(t, "s", "u1", "active")
	h.seed(t, "x", "u1", "active")
	srv := httptest.NewServer(h.server.Echo())
	defer srv.Close()

	bound, id := h.mintPAT(t, "workspaces:read", "patches:read")
	bindToken(t, h.db.SqlDB, id, "s")
	twin, _ := h.mintPAT(t, "workspaces:read", "patches:read")

	code, body := smokeCall(t, srv.URL, http.MethodGet, "/api/v1/workspaces/s", bound, "")
	var ws workspaceJSON
	if err := json.Unmarshal([]byte(body), &ws); code != http.StatusOK || err != nil || ws.Slug != "s" {
		t.Errorf("GET /workspaces/s: (%d, %s); want 200 with workspace s", code, body)
	}
	code, body = smokeCall(t, srv.URL, http.MethodGet, "/api/v1/workspaces/x/patches", bound, "")
	if code != http.StatusNotFound || errorOf(t, body).Error.Message != "workspace not found" {
		t.Errorf("GET /workspaces/x/patches: (%d, %s); want 404 workspace not found", code, body)
	}
	for _, path := range []string{"/api/v1/workspaces/s", "/api/v1/workspaces/x/patches"} {
		if code, body := smokeCall(t, srv.URL, http.MethodGet, path, twin, ""); code != http.StatusOK {
			t.Errorf("unbound twin GET %s: (%d, %s); want 200", path, code, body)
		}
	}
}

// TS-33-57 (smoke, 33-PATH-2): A bound PAT with tokens:manage cannot mint a
// PAT through the running hub. Real: apikit server, apikit route guard, the
// pats table, SQLite.
func TestSmoke_BoundTokenCannotMint_TS_33_57(t *testing.T) {
	h := newRealHub(t)
	srv := httptest.NewServer(h.server.Echo())
	defer srv.Close()
	bound, id := h.mintPAT(t, "tokens:manage")
	bindToken(t, h.db.SqlDB, id, "s")

	count := func() int {
		var n int
		if err := h.db.SqlDB.QueryRow(`SELECT COUNT(*) FROM pats`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	code, body := smokeCall(t, srv.URL, http.MethodPost, "/api/v1/user/tokens", bound, `{"name":"escape","permissions":["tokens:manage"]}`)
	e := errorOf(t, body)
	if code != http.StatusForbidden || e.Error.ErrorType != "token_bound" || e.Error.Message != "this token is bound to a workspace" {
		t.Errorf("POST /user/tokens: (%d, %s); want 403 token_bound: this token is bound to a workspace", code, body)
	}
	if after := count(); after != before {
		t.Errorf("pats rows %d after; want %d", after, before)
	}
}

// TS-33-58 (smoke, 33-PATH-3): A bound PAT is refused on a non-allowlisted hub
// route through the running hub. Real: apikit server and auth middleware,
// binding guard, secrets handlers, SQLite.
func TestSmoke_BoundTokenNonAllowlistedRoute_TS_33_58(t *testing.T) {
	h := newRealHub(t)
	// As cmd/af-hub does, after MountWorkspaceHandlers.
	if err := secrets.InitSchema(h.db.SqlDB); err != nil {
		t.Fatal(err)
	}
	if err := secrets.RegisterRoutesWithAudit(h.server.APIGroup(), secrets.SecretsRouteConfig{DB: h.db.SqlDB}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.server.Echo())
	defer srv.Close()
	bound, id := h.mintPAT(t, "secrets:manage", "secrets:list")
	bindToken(t, h.db.SqlDB, id, "s")

	code, body := smokeCall(t, srv.URL, http.MethodGet, "/api/v1/user/secrets", bound, "")
	e := errorOf(t, body)
	if code != http.StatusForbidden || e.Error.ErrorType != "token_bound" || e.Error.Message != "this token is bound to workspace s" {
		t.Errorf("GET /user/secrets: (%d, %s); want 403 token_bound: this token is bound to workspace s", code, body)
	}
	// The same user's API key reaches the handler.
	if code, body := smokeCall(t, srv.URL, http.MethodGet, "/api/v1/user/secrets", h.apiKey, ""); code != http.StatusOK {
		t.Errorf("API key GET /user/secrets: (%d, %s); want 200", code, body)
	}
}
