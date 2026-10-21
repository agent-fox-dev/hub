package boundtoken

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// TS-34-21: Listing without a credential is answered 401.
// Requirement: 34-REQ-4.1
func TestList_NoCredential_TS_34_21(t *testing.T) {
	env := newTestEnv(t, Config{})
	seedWorkspace(t, env.db, "s", "u1", "active")
	rec := env.do(http.MethodGet, "/api/v1/workspaces/s/tokens", nil, "", nil)
	if rec.Code != http.StatusUnauthorized || parseError(t, rec).Error.Message != "authentication required" {
		t.Errorf("(%d, %s); want 401 authentication required", rec.Code, rec.Body)
	}
}

// TS-34-22: A bound PAT listing is refused with token_bound and reported once;
// a binding lookup error answers 500. Requirement: 34-REQ-4.2
func TestList_BoundPAT_TS_34_22(t *testing.T) {
	env := newTestEnv(t, Config{})
	seedWorkspace(t, env.db, "s", "u1", "active")
	bind(t, env.db, "tokB", "s")
	reports := recordRefusals(t)

	rec := env.do(http.MethodGet, "/api/v1/workspaces/s/tokens", pat("u1", "tokB", "tokens:read"), "", nil)
	b := parseError(t, rec)
	if rec.Code != http.StatusForbidden || b.Error.ErrorType != "token_bound" || b.Error.Message != "this token is bound to workspace s" {
		t.Errorf("bound PAT: (%d, %s); want 403 token_bound: this token is bound to workspace s", rec.Code, rec.Body)
	}
	want := wsaccess.BoundRefusal{TokenID: "tokB", UserID: "u1", BoundSlug: "s", RequestedSlug: "s", Method: http.MethodGet,
		Route: "/api/v1/workspaces/:slug/tokens", Status: http.StatusForbidden, Surface: wsaccess.SurfaceAPI}
	if len(*reports) != 1 || (*reports)[0] != want {
		t.Errorf("reports = %+v; want exactly %+v", *reports, want)
	}

	if _, err := env.db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	rec = env.do(http.MethodGet, "/api/v1/workspaces/s/tokens", pat("u1", "tokB", "tokens:read"), "", nil)
	if rec.Code != http.StatusInternalServerError || parseError(t, rec).Error.Message != "internal server error" {
		t.Errorf("lookup error: (%d, %s); want 500 internal server error", rec.Code, rec.Body)
	}
}

// TS-34-23: An unbound PAT with neither tokens:read nor tokens:manage is
// refused; the control PATs pass. Requirement: 34-REQ-4.3
func TestList_PATScope_TS_34_23(t *testing.T) {
	env := newTestEnv(t, Config{})
	seedWorkspace(t, env.db, "s", "u1", "active")
	rec := env.do(http.MethodGet, "/api/v1/workspaces/s/tokens", pat("u1", "tokU", "workspaces:read"), "", nil)
	if rec.Code != http.StatusForbidden || parseError(t, rec).Error.Message != "PAT requires tokens:read scope" {
		t.Errorf("(%d, %s); want 403 PAT requires tokens:read scope", rec.Code, rec.Body)
	}
	for _, scope := range []string{"tokens:read", "tokens:manage"} {
		if rec := env.do(http.MethodGet, "/api/v1/workspaces/s/tokens", pat("u1", "tokU", scope), "", nil); rec.Code != http.StatusOK {
			t.Errorf("PAT with %s: (%d, %s); want 200", scope, rec.Code, rec.Body)
		}
	}
}

// TS-34-24: Listing an unknown workspace or another user's workspace answers
// 404. Requirement: 34-REQ-4.4
func TestList_NotFound_TS_34_24(t *testing.T) {
	env := newTestEnv(t, Config{})
	seedWorkspace(t, env.db, "b", "u2", "active")
	for _, c := range []struct{ name, slug string }{
		{"u1 missing", "missing"}, {"u1 other's", "b"}, {"admin missing", "missing"},
	} {
		auth := apiKey("u1")
		if strings.HasPrefix(c.name, "admin") {
			auth = admin()
		}
		rec := env.do(http.MethodGet, "/api/v1/workspaces/"+c.slug+"/tokens", auth, "", nil)
		if rec.Code != http.StatusNotFound || parseError(t, rec).Error.Message != "workspace not found" {
			t.Errorf("%s: (%d, %s); want 404 workspace not found", c.name, rec.Code, rec.Body)
		}
	}
}

func listOf(t *testing.T, env *testEnv, slug string, auth any) []map[string]any {
	t.Helper()
	a := apiKey("u1")
	if auth != nil {
		a = admin()
	}
	rec := env.do(http.MethodGet, "/api/v1/workspaces/"+slug+"/tokens", a, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: (%d, %s); want 200", slug, rec.Code, rec.Body)
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out == nil {
		t.Fatalf("body %s is not a JSON array: %v", rec.Body, err)
	}
	return out
}

// TS-34-25: The listing returns every binding joined with pats, ordered by the
// token's created_at then token_id, revoked and expired included, orphans
// omitted, archived workspaces listed, [] when empty. Requirement: 34-REQ-4.5
func TestList_JoinedOrderedComplete_TS_34_25(t *testing.T) {
	env := newTestEnv(t, Config{})
	seedWorkspace(t, env.db, "s", "u1", "archived")
	seedWorkspace(t, env.db, "e", "u1", "active")
	seedPAT(t, env.db, patRow{id: "c-live", name: "live", createdAt: "2026-09-01T10:00:02Z"})
	seedPAT(t, env.db, patRow{id: "b-revoked", name: "revoked", revokedAt: "2026-09-02T00:00:00Z", createdAt: "2026-09-01T10:00:01Z"})
	seedPAT(t, env.db, patRow{id: "a-expired", name: "expired", expiresAt: "2026-09-01T11:00:00Z", createdAt: "2026-09-01T10:00:01Z"})
	for _, id := range []string{"c-live", "b-revoked", "a-expired", "orphan"} {
		bind(t, env.db, id, "s")
	}

	for _, who := range []any{nil, "admin"} {
		var ids []string
		for _, e := range listOf(t, env, "s", who) {
			ids = append(ids, e["token_id"].(string))
		}
		if want := []string{"a-expired", "b-revoked", "c-live"}; !slices.Equal(ids, want) {
			t.Errorf("listing (admin=%v) = %v; want %v", who != nil, ids, want)
		}
	}
	rec := env.do(http.MethodGet, "/api/v1/workspaces/e/tokens", apiKey("u1"), "", nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty workspace: (%d, %s); want 200 []", rec.Code, rec.Body)
	}
}

// TS-34-26: Listing renders permissions as an array ([] when corrupt), nulls
// for unset times, exactly the seven keys, and never a secret.
// Requirement: 34-REQ-4.6
func TestList_Rendering_TS_34_26(t *testing.T) {
	env := newTestEnv(t, Config{})
	seedWorkspace(t, env.db, "s", "u1", "active")
	seedPAT(t, env.db, patRow{id: "t1", name: "good", perms: `["a","b"]`, createdAt: "2026-09-01T10:00:00Z"})
	seedPAT(t, env.db, patRow{id: "t2", name: "corrupt", perms: `not json`, expiresAt: "2026-12-01T00:00:00Z", revokedAt: "2026-10-01T00:00:00Z", createdAt: "2026-09-01T10:00:01Z"})
	bind(t, env.db, "t1", "s")
	bind(t, env.db, "t2", "s")

	rec := env.do(http.MethodGet, "/api/v1/workspaces/s/tokens", apiKey("u1"), "", nil)
	raw := rec.Body.String()
	list := listOf(t, env, "s", nil)
	if len(list) != 2 {
		t.Fatalf("entries = %d; want 2", len(list))
	}
	keys := []string{"created_at", "expires_at", "name", "permissions", "revoked_at", "token_id", "workspace"}
	for _, e := range list {
		var got []string
		for k := range e {
			got = append(got, k)
		}
		slices.Sort(got)
		if !slices.Equal(got, keys) {
			t.Errorf("keys %v; want %v", got, keys)
		}
		if e["workspace"] != "s" {
			t.Errorf("workspace = %v; want s", e["workspace"])
		}
	}
	if p, ok := list[0]["permissions"].([]any); !ok || len(p) != 2 || p[0] != "a" || p[1] != "b" {
		t.Errorf("permissions = %#v; want [a b]", list[0]["permissions"])
	}
	if p, ok := list[1]["permissions"].([]any); !ok || len(p) != 0 {
		t.Errorf("corrupt permissions = %#v; want []", list[1]["permissions"])
	}
	if list[0]["expires_at"] != nil || list[0]["revoked_at"] != nil {
		t.Errorf("unset times = %v, %v; want null", list[0]["expires_at"], list[0]["revoked_at"])
	}
	if list[1]["expires_at"] != "2026-12-01T00:00:00Z" || list[1]["revoked_at"] != "2026-10-01T00:00:00Z" {
		t.Errorf("set times = %v, %v", list[1]["expires_at"], list[1]["revoked_at"])
	}
	for _, secret := range []string{"secret_hash", "hash-t1", "hash-t2", `"token"`} {
		if strings.Contains(raw, secret) {
			t.Errorf("body contains %q: %s", secret, raw)
		}
	}
}

// TS-34-27: A listing query error answers 500 internal server error.
// Requirement: 34-REQ-4.7
func TestList_QueryError_TS_34_27(t *testing.T) {
	env := newTestEnv(t, Config{})
	seedWorkspace(t, env.db, "s", "u1", "active")
	if _, err := env.db.Exec(`DROP TABLE pats`); err != nil {
		t.Fatal(err)
	}
	rec := env.do(http.MethodGet, "/api/v1/workspaces/s/tokens", apiKey("u1"), "", nil)
	if rec.Code != http.StatusInternalServerError || parseError(t, rec).Error.Message != "internal server error" {
		t.Errorf("(%d, %s); want 500 internal server error", rec.Code, rec.Body)
	}
}
