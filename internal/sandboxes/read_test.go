package sandboxes

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// The workspace list is a bare array of the live leases newest first, [] for
// none, capped at 200; ?all=<bool> includes the terminal ones and anything
// else is 400. The admin list spans workspaces and needs an admin token.
func TestList_LiveLeasesNewestFirst(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	env.addWorkspace("gadgets", "", "active")
	env.addWorkspace("empty", "", "active")
	env.plantSecret("workspace", "gadgets", "GIT_PAT", "x")
	var issued []string
	for range 3 {
		env.clock.Advance(time.Second)
		issued = append(issued, env.issue(""))
	}
	env.clock.Advance(time.Second)
	other := env.mustIssueAs("gadgets", "{}", ownerKey())["id"]
	if env.delete(issued[1]).Code != http.StatusNoContent {
		t.Fatal("DELETE failed")
	}

	rec := env.get("/api/v1/workspaces/widgets/sandboxes", ownerKey())
	if got := ids(leaseList(t, rec)); !slices.Equal(got, []string{issued[2], issued[0]}) || strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "{") {
		t.Errorf("list = %v (%s); want the live leases newest first as a bare array", got, rec.Body)
	}
	if rec := env.get("/api/v1/workspaces/empty/sandboxes", ownerKey()); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty workspace: %d %q; want 200 []", rec.Code, rec.Body)
	}
	count := func(query string) int { return len(leaseList(t, env.get("/api/v1/workspaces/widgets/sandboxes"+query, ownerKey()))) }
	for _, q := range []string{"?all=true", "?all=1", "?all=T"} {
		if n := count(q); n != 3 {
			t.Errorf("%s: %d leases; want 3", q, n)
		}
	}
	for _, q := range []string{"?all=false", "?all=0", ""} {
		if n := count(q); n != 2 {
			t.Errorf("%s: %d leases; want 2", q, n)
		}
	}
	for _, tc := range []struct {
		path string
		auth *apikit.AuthInfo
	}{{"/api/v1/workspaces/widgets/sandboxes?all=maybe", ownerKey()}, {"/api/v1/workspaces/widgets/sandboxes?all=", ownerKey()}, {"/api/v1/sandboxes?all=yes", adminAuth()}} {
		if rec := env.get(tc.path, tc.auth); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: %d; want 400", tc.path, rec.Code)
		}
	}

	if got := ids(leaseList(t, env.get("/api/v1/sandboxes", adminAuth()))); !slices.Equal(got, []string{other.(string), issued[2], issued[0]}) {
		t.Errorf("admin list = %v; want both workspaces' live leases newest first", got)
	}
	if n := len(leaseList(t, env.get("/api/v1/sandboxes?all=true", adminAuth()))); n != 4 {
		t.Errorf("admin ?all=true = %d; want 4", n)
	}
	for _, auth := range []*apikit.AuthInfo{ownerKey(), pat("sandboxes:read", "sandboxes:write")} {
		if rec := env.get("/api/v1/sandboxes", auth); rec.Code != http.StatusForbidden || message(t, rec) != "admin token required" {
			t.Errorf("%s on the admin list: %d %q; want 403", auth.CredentialType, rec.Code, message(t, rec))
		}
	}

	base := time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC)
	for n := range 205 {
		env.insertLeaseAt(n+1, "empty", "leased", base.Add(time.Duration(n)*time.Second))
	}
	if list := leaseList(t, env.get("/api/v1/workspaces/empty/sandboxes", ownerKey())); len(list) != 200 || list[0]["id"] != leaseID(205) || list[199]["id"] != leaseID(6) {
		t.Errorf("the cap: %d leases, first %v, last %v; want 200 from the newest", len(list), list[0]["id"], list[199]["id"])
	}
}

// GET one returns a lease in any state; a malformed, unknown or foreign id is
// 404 'sandbox lease not found', alike.
func TestGetOne_AnyStateOrNotFound(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	env.addWorkspace("other", "", "active")
	leased, destroyed := env.issue(""), env.issue("")
	env.delete(destroyed)
	foreign := env.insertLeaseAt(1, "other", "leased", env.clock.Now())
	for id, state := range map[string]string{leased: "leased", destroyed: "destroyed"} {
		rec := env.get("/api/v1/workspaces/widgets/sandboxes/"+id, ownerKey())
		if got := testutil.Object(t, rec); rec.Code != http.StatusOK || got["id"] != id || got["state"] != state {
			t.Errorf("GET %s = %d %v; want %s", id, rec.Code, got, state)
		}
	}
	for name, id := range map[string]string{"malformed": "xyz", "wrong prefix": "sbl_" + strings.Repeat("a", 32), "unknown": leaseID(999), "foreign": foreign} {
		if rec := env.get("/api/v1/workspaces/widgets/sandboxes/"+id, ownerKey()); rec.Code != http.StatusNotFound || message(t, rec) != "sandbox lease not found" {
			t.Errorf("%s: %d %q", name, rec.Code, message(t, rec))
		}
	}
}

// Every representation has exactly the lease fields (plus token on the issuing
// POST), lists and maps never null, lease_expires_at null for a row from before
// the column, the same gateway_url on every lease in every state, and never the
// token, its hash or an environment value.
func TestRender_ExactFieldsAndNoLeak(t *testing.T) {
	cfg := defaultLeaseConfig()
	cfg.Profiles[0].OptionalSecrets = nil
	env := newLeaseEnv(t, leaseEnvOptions{config: cfg})
	env.svc.gatewayURL = "http://127.0.0.1:4711"
	issued := env.mustIssue("")
	id, token := issued["id"].(string), issued["token"].(string)
	active, failed := env.issue(""), env.issue(`{"profile":"data"}`)
	env.store.MarkActive(active, "box")
	env.store.FailLease(failed, "boom")
	legacy := env.insertLeaseAt(9, "widgets", "leased", env.clock.Now())

	wantFields := []string{"created_at", "created_by", "end_reason", "ended_at", "error", "expires_at", "gateway_url", "id", "image", "labels", "lease_expires_at", "network",
		"optional_secrets", "profile", "purpose", "require_landlock", "resources", "sandbox_id", "secrets", "state", "updated_at", "workspace"}
	if got := slices.Sorted(mapKeys(issued)); !slices.Equal(got, append(slices.Clone(wantFields), "token")) {
		t.Errorf("POST keys = %v", got)
	}
	check := func(what string, body string, lease map[string]any) {
		t.Helper()
		if got := slices.Sorted(mapKeys(lease)); !slices.Equal(got, wantFields) {
			t.Errorf("%s: keys = %v", what, got)
		}
		if got := slices.Sorted(mapKeys(lease["resources"].(map[string]any))); !slices.Equal(got, []string{"cpus", "disk_mb", "memory_mb", "pids"}) {
			t.Errorf("%s: resources keys = %v", what, got)
		}
		if lease["gateway_url"] != "http://127.0.0.1:4711" {
			t.Errorf("%s: gateway_url = %v", what, lease["gateway_url"])
		}
		if _, ok := lease["optional_secrets"].([]any); !ok {
			t.Errorf("%s: optional_secrets = %v; want []", what, lease["optional_secrets"])
		}
		if _, ok := lease["labels"].(map[string]any); !ok {
			t.Errorf("%s: labels = %v; want {}", what, lease["labels"])
		}
		if (lease["id"] == legacy) != (lease["lease_expires_at"] == nil) {
			t.Errorf("%s: lease %v has lease_expires_at %v", what, lease["id"], lease["lease_expires_at"])
		}
		containsNone(t, what, body, token, HashToken(token), "envval", "SECRET_ENV", `"token"`, "token_hash")
	}
	for _, lid := range []string{id, active, failed, legacy} {
		rec := env.get("/api/v1/workspaces/widgets/sandboxes/"+lid, ownerKey())
		check("GET one", rec.Body.String(), testutil.Object(t, rec))
	}
	states := map[string]bool{}
	for _, tc := range []struct {
		path string
		auth *apikit.AuthInfo
	}{{"/api/v1/workspaces/widgets/sandboxes?all=true", ownerKey()}, {"/api/v1/sandboxes?all=true", adminAuth()}} {
		rec := env.get(tc.path, tc.auth)
		for _, l := range leaseList(t, rec) {
			states[l["state"].(string)] = true
			check(tc.path, rec.Body.String(), l)
		}
	}
	if !states["leased"] || !states["active"] || !states["failed"] {
		t.Errorf("listed states = %v; want leased, active and failed", states)
	}
	if secretsList, ok := env.mustIssue(`{"profile":"data"}`)["secrets"].([]any); !ok || len(secretsList) != 0 {
		t.Error("a profile without secrets does not render []")
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
