package sandboxes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"
)

// get sends a GET to path.
func (e *leaseEnv) get(path string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodGet, path, "", auth)
}

// leaseList decodes a response body that is a bare JSON array of leases.
func leaseList(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200: %s", rec.Code, rec.Body)
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("body is not a JSON array: %v\n%s", err, rec.Body)
	}
	return list
}

// leaseListOrNil decodes a 200 response that is a JSON array of leases, and
// returns nil for anything else.
func leaseListOrNil(rec *httptest.ResponseRecorder) []map[string]any {
	var list []map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		return nil
	}
	return list
}

// ids returns the id of every lease in a list.
func ids(list []map[string]any) []string {
	out := make([]string, len(list))
	for i, l := range list {
		out[i], _ = l["id"].(string)
	}
	return out
}

// insertLeaseAt inserts a lease row straight through SQL with the given
// creation time.
func (e *leaseEnv) insertLeaseAt(n int, workspace, state string, created time.Time) string {
	e.t.Helper()
	id := leaseID(n)
	stamp := apikit.FormatUTC(created)
	if _, err := e.db.Exec(`INSERT INTO sandbox_leases
		(id, workspace, profile, state, token_hash, image, network, ttl_seconds, expires_at, created_by, created_at, updated_at)
		VALUES (?, ?, 'tools', ?, ?, 'img', 'none', 3600, ?, ?, ?, ?)`,
		id, workspace, state, HashToken(testToken(n)), apikit.FormatUTC(created.Add(time.Hour)), leaseOwner, stamp, stamp); err != nil {
		e.t.Fatalf("inserting lease %d: %v", n, err)
	}
	return id
}

// TS-26-41: List returns the workspace's live leases newest first as a bare
// array, and [] for a workspace with none.
// Requirement: 26-REQ-7.1
func TestList_TS_26_41(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	env.addWorkspace("gadgets", leaseOwner, "", "active")
	env.addWorkspace("empty", leaseOwner, "", "active")
	env.plantSecret("workspace", "gadgets", "GIT_PAT", "x")

	var issued []string
	for range 4 {
		env.clock.Advance(time.Second)
		issued = append(issued, env.mustIssue()["id"].(string))
	}
	env.clock.Advance(time.Second)
	other := env.post("gadgets", `{}`, apiKeyAuth())
	if other.Code != http.StatusCreated {
		t.Fatalf("lease in another workspace: status %d", other.Code)
	}
	// The second lease issued has ended.
	if changed, err := env.store.EndLease(&Lease{ID: issued[1]}, "destroyed", "api"); err != nil || !changed {
		t.Fatalf("EndLease = (%v, %v)", changed, err)
	}

	rec := env.get("/api/v1/workspaces/widgets/sandboxes", apiKeyAuth())
	got := ids(leaseList(t, rec))
	if want := []string{issued[3], issued[2], issued[0]}; !slices.Equal(got, want) {
		t.Errorf("list = %v; want the three live leases newest first %v", got, want)
	}
	if strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "{") {
		t.Errorf("the list is not a bare array: %s", rec.Body)
	}

	empty := env.get("/api/v1/workspaces/empty/sandboxes", apiKeyAuth())
	if body := strings.TrimSpace(empty.Body.String()); empty.Code != http.StatusOK || body != "[]" {
		t.Errorf("empty workspace: status %d, body %q; want 200 and []", empty.Code, body)
	}
}

// TS-26-42: List is capped at 200 rows, newest first.
// Requirement: 26-REQ-7.1
func TestList_Cap_TS_26_42(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	base := time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC)
	var newest string
	for n := range 205 {
		newest = env.insertLeaseAt(n+1, "widgets", "leased", base.Add(time.Duration(n)*time.Second))
	}

	list := leaseList(t, env.get("/api/v1/workspaces/widgets/sandboxes", apiKeyAuth()))
	if len(list) != 200 {
		t.Fatalf("list has %d entries; want 200", len(list))
	}
	if list[0]["id"] != newest {
		t.Errorf("first entry is %v; want the newest lease %s", list[0]["id"], newest)
	}
	if list[199]["id"] != leaseID(6) {
		t.Errorf("last entry is %v; want the 200th newest, %s", list[199]["id"], leaseID(6))
	}
	admin := leaseList(t, env.get("/api/v1/sandboxes", adminAuth()))
	if len(admin) != 200 {
		t.Errorf("the admin list has %d entries; want 200", len(admin))
	}
}

// TS-26-43: ?all=true includes terminal leases, and any value strconv.ParseBool
// reads as true does.
// Requirement: 26-REQ-7.2
func TestList_All_TS_26_43(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	live := env.mustIssue()["id"].(string)
	env.clock.Advance(time.Second)
	ended := env.mustIssue()["id"].(string)
	if _, err := env.store.EndLease(&Lease{ID: ended}, "destroyed", "api"); err != nil {
		t.Fatal(err)
	}

	count := func(query string) int {
		return len(leaseList(t, env.get("/api/v1/workspaces/widgets/sandboxes"+query, apiKeyAuth())))
	}
	if n := count(""); n != 1 {
		t.Errorf("without all: %d leases; want only the live one", n)
	}
	for _, q := range []string{"?all=true", "?all=1", "?all=T", "?all=TRUE"} {
		if n := count(q); n != 2 {
			t.Errorf("%s: %d leases; want both", q, n)
		}
	}
	for _, q := range []string{"?all=false", "?all=0", "?all=F"} {
		if n := count(q); n != 1 {
			t.Errorf("%s: %d leases; want only the live one", q, n)
		}
	}
	all := ids(leaseList(t, env.get("/api/v1/workspaces/widgets/sandboxes?all=true", apiKeyAuth())))
	if !slices.Equal(all, []string{ended, live}) {
		t.Errorf("?all=true = %v; want newest first %v", all, []string{ended, live})
	}
}

// TS-26-44: An unparseable all parameter answers 400 on both list routes.
// Requirement: 26-REQ-7.3
func TestList_BadAll_TS_26_44(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	for _, tc := range []struct {
		path string
		auth *apikit.AuthInfo
	}{
		{"/api/v1/workspaces/widgets/sandboxes?all=maybe", apiKeyAuth()},
		{"/api/v1/workspaces/widgets/sandboxes?all=", apiKeyAuth()},
		{"/api/v1/sandboxes?all=maybe", adminAuth()},
		{"/api/v1/sandboxes?all=yes", adminAuth()},
	} {
		if rec := env.get(tc.path, tc.auth); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status %d; want 400", tc.path, rec.Code)
		}
	}
}

// TS-26-45: GET one returns a lease in any state.
// Requirement: 26-REQ-7.4
func TestGetOne_AnyState_TS_26_45(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	leased := env.mustIssue()["id"].(string)
	env.clock.Advance(time.Second)
	destroyed := env.mustIssue()["id"].(string)
	if _, err := env.store.EndLease(&Lease{ID: destroyed}, "destroyed", "api"); err != nil {
		t.Fatal(err)
	}

	for id, state := range map[string]string{leased: "leased", destroyed: "destroyed"} {
		rec := env.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth())
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d: %s", id, rec.Code, rec.Body)
		}
		if got := jsonObject(t, rec); got["id"] != id || got["state"] != state {
			t.Errorf("GET %s = id %v, state %v; want %s in %s", id, got["id"], got["state"], id, state)
		}
	}
}

// TS-26-46: GET one with a malformed, unknown or foreign id answers 404
// 'sandbox lease not found', the same for all three.
// Requirement: 26-REQ-7.5
func TestGetOne_NotFound_TS_26_46(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	env.addWorkspace("other", leaseOwner, "", "active")
	foreign := env.insertLeaseAt(1, "other", "leased", env.clock.Now())

	for name, id := range map[string]string{
		"malformed":           "xyz",
		"wrong prefix":        "sbl_" + strings.Repeat("a", 32),
		"upper-case hex":      "sbx_" + strings.Repeat("A", 32),
		"unknown":             leaseID(999),
		"another workspace's": foreign,
	} {
		rec := env.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth())
		if rec.Code != http.StatusNotFound || message(t, rec) != "sandbox lease not found" {
			t.Errorf("%s: status %d, message %q; want 404 'sandbox lease not found'", name, rec.Code, message(t, rec))
		}
	}
}

// leaseFields are the keys of a rendered lease and the keys of its resources.
var (
	leaseFields = []string{"id", "workspace", "profile", "purpose", "state", "expires_at", "image", "network", "resources",
		"require_landlock", "secrets", "optional_secrets", "labels", "created_by", "created_at", "updated_at",
		"sandbox_id", "error", "ended_at", "end_reason"}
	resourceFields = []string{"cpus", "memory_mb", "pids", "disk_mb"}
)

// sortedKeys returns the keys of m, sorted.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// TS-26-47: Rendered leases have exactly the specified fields and never leak the
// token, its hash or an environment value.
// Requirement: 26-REQ-7.6
func TestRender_ExactFields_TS_26_47(t *testing.T) {
	cfg := defaultLeaseConfig()
	cfg.Profiles[0].OptionalSecrets = nil
	env := newLeaseEnv(t, leaseEnvOptions{config: cfg})
	issued := env.mustIssue()
	id, token := issued["id"].(string), issued["token"].(string)
	hash := HashToken(token)

	wantFields := slices.Sorted(slices.Values(leaseFields))
	check := func(what string, rec *httptest.ResponseRecorder, lease map[string]any) {
		t.Helper()
		if got := sortedKeys(lease); !slices.Equal(got, wantFields) {
			t.Errorf("%s: keys = %v; want exactly %v", what, got, wantFields)
		}
		resources, _ := lease["resources"].(map[string]any)
		if got := sortedKeys(resources); !slices.Equal(got, slices.Sorted(slices.Values(resourceFields))) {
			t.Errorf("%s: resources keys = %v", what, got)
		}
		if secrets, ok := lease["secrets"].([]any); !ok || len(secrets) != 1 {
			t.Errorf("%s: secrets = %v; want an array of one name", what, lease["secrets"])
		}
		if optional, ok := lease["optional_secrets"].([]any); !ok || len(optional) != 0 {
			t.Errorf("%s: optional_secrets = %v (%T); want []", what, lease["optional_secrets"], lease["optional_secrets"])
		}
		if labels, ok := lease["labels"].(map[string]any); !ok || len(labels) != 0 {
			t.Errorf("%s: labels = %v (%T); want {}", what, lease["labels"], lease["labels"])
		}
		for _, f := range []string{"sandbox_id", "error", "ended_at", "end_reason"} {
			if v, present := lease[f]; !present || v != nil {
				t.Errorf("%s: %s = %v; want null", what, f, v)
			}
		}
		body := rec.Body.String()
		for _, leak := range []string{token, hash, "envval", "SECRET_ENV", `"token"`, "token_hash"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: the body contains %q", what, leak)
			}
		}
	}

	one := env.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth())
	check("GET one", one, jsonObject(t, one))
	list := env.get("/api/v1/workspaces/widgets/sandboxes", apiKeyAuth())
	if items := leaseListOrNil(list); len(items) > 0 {
		check("GET list", list, items[0])
	} else {
		t.Errorf("GET list: status %d, body %s; want a list holding the lease", list.Code, list.Body)
	}
	admin := env.get("/api/v1/sandboxes", adminAuth())
	if items := leaseListOrNil(admin); len(items) > 0 {
		check("GET /sandboxes", admin, items[0])
	} else {
		t.Errorf("GET /sandboxes: status %d, body %s; want a list holding the lease", admin.Code, admin.Body)
	}

	// The issuing POST has the same fields plus the token.
	posted := append(slices.Clone(wantFields), "token")
	slices.Sort(posted)
	if got := sortedKeys(issued); !slices.Equal(got, posted) {
		t.Errorf("POST keys = %v; want the lease fields and token", got)
	}
}

// TS-26-48: The admin list returns leases across workspaces newest first, live
// only unless all=true.
// Requirement: 26-REQ-7.7
func TestAdminList_TS_26_48(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	env.addWorkspace("gadgets", leaseOwner, "", "active")
	env.plantSecret("workspace", "gadgets", "GIT_PAT", "x")

	first := env.mustIssue()["id"].(string)
	env.clock.Advance(time.Second)
	second := env.post("gadgets", `{}`, apiKeyAuth())
	env.clock.Advance(time.Second)
	gone := env.mustIssue()["id"].(string)
	if _, err := env.store.EndLease(&Lease{ID: gone}, "destroyed", "api"); err != nil {
		t.Fatal(err)
	}

	list := leaseList(t, env.get("/api/v1/sandboxes", adminAuth()))
	secondID := jsonObject(t, second)["id"].(string)
	if got := ids(list); !slices.Equal(got, []string{secondID, first}) {
		t.Errorf("admin list = %v; want the two live leases of both workspaces newest first", got)
	}
	workspaces := map[any]bool{}
	for _, l := range list {
		workspaces[l["workspace"]] = true
	}
	if !workspaces["widgets"] || !workspaces["gadgets"] {
		t.Errorf("workspaces in the admin list = %v; want widgets and gadgets", workspaces)
	}
	all := leaseList(t, env.get("/api/v1/sandboxes?all=true", adminAuth()))
	if len(all) != 3 || all[0]["id"] != gone {
		t.Errorf("?all=true = %v; want three leases, the ended one newest", ids(all))
	}
}

// TS-26-14: Credentials other than an admin token get 403 on GET /sandboxes.
// Requirement: 26-REQ-2.7
func TestAdminList_NonAdmin_TS_26_14(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	for name, auth := range map[string]*apikit.AuthInfo{
		"API key":            apiKeyAuth(),
		"PAT with both":      patAuth("sandboxes:read", "sandboxes:write"),
		"PAT with read only": patAuth("sandboxes:read"),
	} {
		rec := env.get("/api/v1/sandboxes", auth)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d; want 403", name, rec.Code)
			continue
		}
		if obj, ok := jsonObject(t, rec)["error"].(map[string]any); !ok || obj["code"] != float64(403) {
			t.Errorf("%s: body %s is not an apikit 403 envelope", name, rec.Body)
		}
	}
	if rec := env.get("/api/v1/sandboxes", adminAuth()); rec.Code != http.StatusOK {
		t.Errorf("admin token: status %d; want 200", rec.Code)
	}
	_ = fmt.Sprint
}
