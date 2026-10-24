package workspace

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// seedListed inserts a workspace with an explicit created_at; org is "" for
// a NULL org_id.
func seedListed(t *testing.T, env *testEnv, slug, owner, org, status, createdAt string) {
	t.Helper()
	var orgID any
	if org != "" {
		orgID = org
	}
	if _, err := env.db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, display_name, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`, slug, "https://github.com/org/"+slug, owner, orgID, status, slug, createdAt, createdAt); err != nil {
		t.Fatalf("seeding %s: %v", slug, err)
	}
}

// listed returns the slugs and access values of a listing, in order.
func listed(t *testing.T, env *testEnv, path string, auth *apikit.AuthInfo) (slugs []string, access map[string]string) {
	t.Helper()
	rec := env.doRequest(t, http.MethodGet, path, "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d. body: %s", path, rec.Code, rec.Body)
	}
	access = map[string]string{}
	for _, obj := range parseListObjects(t, rec.Body.Bytes()) {
		slug, _ := obj["slug"].(string)
		slugs = append(slugs, slug)
		a, _ := obj["access"].(string)
		access[slug] = a
	}
	return slugs, access
}

// listFixture: u2 owns W1; org O holds W2 (u1) and W3 (u1, archived), and
// u2 is a member of O; W4 is in org Z, which u2 does not belong to.
func listFixture(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	env.seedOrg(t, "O", "Org O", "org-o")
	env.seedOrg(t, "Z", "Org Z", "org-z")
	env.seedOrgMember(t, "O", "u1")
	env.seedOrgMember(t, "O", "u2")
	env.seedOrgMember(t, "Z", "u1")
	seedListed(t, env, "w1-owned", "u2", "", "ready", "2026-01-01T00:00:01Z")
	seedListed(t, env, "w2-member", "u1", "O", "ready", "2026-01-01T00:00:03Z")
	seedListed(t, env, "w3-archived", "u1", "O", "archived", "2026-01-01T00:00:02Z")
	seedListed(t, env, "w4-other", "u1", "Z", "ready", "2026-01-01T00:00:04Z")
	return env
}

// TS-35-21: An admin token lists every workspace with access admin, newest
// first, archived ones only with include_archived.
// Requirement: 35-REQ-4.1
func TestListWorkspaces_Admin_TS_35_21(t *testing.T) {
	env := listFixture(t)
	slugs, access := listed(t, env, "/api/v1/workspaces", adminAuth())
	if want := []string{"w4-other", "w2-member", "w1-owned"}; !slices.Equal(slugs, want) {
		t.Errorf("admin listing %v; want %v", slugs, want)
	}
	slugs, access2 := listed(t, env, "/api/v1/workspaces?include_archived=true", adminAuth())
	if want := []string{"w4-other", "w2-member", "w3-archived", "w1-owned"}; !slices.Equal(slugs, want) {
		t.Errorf("admin listing with archived %v; want %v", slugs, want)
	}
	for _, m := range []map[string]string{access, access2} {
		for slug, a := range m {
			if a != "admin" {
				t.Errorf("admin listing: %s access %q; want admin", slug, a)
			}
		}
	}
}

// TS-35-22: A non-admin caller lists owned and org workspaces with the
// recorded access, newest first.
// Requirement: 35-REQ-4.2
func TestListWorkspaces_OwnerAndMember_TS_35_22(t *testing.T) {
	env := listFixture(t)
	slugs, access := listed(t, env, "/api/v1/workspaces", userAuth("u2"))
	if want := []string{"w2-member", "w1-owned"}; !slices.Equal(slugs, want) {
		t.Errorf("u2 listing %v; want %v", slugs, want)
	}
	if access["w1-owned"] != "owner" || access["w2-member"] != "member" {
		t.Errorf("u2 access %v; want w1-owned owner, w2-member member", access)
	}
	slugs, access = listed(t, env, "/api/v1/workspaces?include_archived=true", userAuth("u2"))
	if want := []string{"w2-member", "w3-archived", "w1-owned"}; !slices.Equal(slugs, want) {
		t.Errorf("u2 listing with archived %v; want %v", slugs, want)
	}
	if access["w3-archived"] != "member" {
		t.Errorf("w3-archived access %q; want member", access["w3-archived"])
	}
}

// TS-35-23: A caller with 501 readable workspaces receives all of them,
// newest first, without duplicates.
// Requirement: 35-REQ-4.3
func TestListWorkspaces_ManyChunks_TS_35_23(t *testing.T) {
	env := newTestEnv(t)
	env.seedOrg(t, "O", "Org O", "org-o")
	env.seedOrgMember(t, "O", "u2")
	for i := 0; i < 501; i++ {
		owner, org := "u1", "O"
		if i%3 == 0 {
			owner, org = "u2", ""
		}
		// Slugs are not in created_at order, so the merge must sort.
		seedListed(t, env, fmt.Sprintf("ws-%03d", (i*7)%501), owner, org, "ready",
			fmt.Sprintf("2026-01-01T%02d:%02d:%02dZ", i/3600, (i/60)%60, i%60))
	}
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", userAuth("u2"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	objs := parseListObjects(t, rec.Body.Bytes())
	if len(objs) != 501 {
		t.Fatalf("listed %d workspaces; want 501", len(objs))
	}
	seen := map[string]bool{}
	for i, obj := range objs {
		slug := obj["slug"].(string)
		if seen[slug] {
			t.Errorf("slug %s listed twice", slug)
		}
		seen[slug] = true
		if i > 0 && obj["created_at"].(string) > objs[i-1]["created_at"].(string) {
			t.Errorf("created_at increases at %d: %v after %v", i, obj["created_at"], objs[i-1]["created_at"])
		}
	}
}

// TS-35-24: A caller with an empty readable set gets an empty array.
// Requirement: 35-REQ-4.4
func TestListWorkspaces_Empty_TS_35_24(t *testing.T) {
	env := listFixture(t)
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", userAuth("nobody"))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("listing for nobody: %d %q; want 200 []", rec.Code, rec.Body)
	}
}

// TS-35-25: A PAT without a read-implying scope gets 404 before the readable
// set is queried (org_members is gone, so the query would fail with 500).
// Requirement: 35-REQ-4.5
func TestListWorkspaces_ScopeFirst_TS_35_25(t *testing.T) {
	env := listFixture(t)
	if _, err := env.db.Exec(`DROP TABLE org_members`); err != nil {
		t.Fatal(err)
	}
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", patAuth("u2", "tokens:read"))
	if rec.Code != http.StatusNotFound || parseErrorEnvelope(t, rec).Error.Message != "workspace not found" {
		t.Errorf("listing without scope: %d %s; want 404 workspace not found", rec.Code, rec.Body)
	}
}

// TS-35-26: A bound PAT lists at most its bound workspace, and only if its
// user may read it.
// Requirement: 35-REQ-4.6
func TestListWorkspaces_BoundPAT_TS_35_26(t *testing.T) {
	env := listFixture(t)
	bindToken(t, env.db, "tok-a", "w2-member")
	bindToken(t, env.db, "tok-c", "w4-other")
	for tok, want := range map[string][]string{"tok-a": {"w2-member"}, "tok-c": nil} {
		pat := &apikit.AuthInfo{CredentialType: "pat", UserID: "u2", TokenID: tok, Permissions: []string{"workspaces:read"}}
		slugs, access := listed(t, env, "/api/v1/workspaces", pat)
		if !slices.Equal(slugs, want) {
			t.Errorf("%s listing %v; want %v", tok, slugs, want)
		}
		if tok == "tok-a" && access["w2-member"] != "member" {
			t.Errorf("%s access %q; want member", tok, access["w2-member"])
		}
	}
}

// TS-35-27: A readable-set failure (org_members is gone) gives 500 and no
// workspace objects.
// Requirement: 35-REQ-4.7
func TestListWorkspaces_QueryError_TS_35_27(t *testing.T) {
	env := listFixture(t)
	if _, err := env.db.Exec(`DROP TABLE org_members`); err != nil {
		t.Fatal(err)
	}
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", userAuth("u2"))
	if rec.Code != http.StatusInternalServerError || parseErrorEnvelope(t, rec).Error.Message != "internal server error" {
		t.Errorf("listing: %d %s; want 500 internal server error", rec.Code, rec.Body)
	}
}

// TS-35-27 (by-slug load): the by-slug query's error is returned.
// Requirement: 35-REQ-4.7
func TestListWorkspacesBySlugs_Error_TS_35_27(t *testing.T) {
	env := listFixture(t)
	if _, err := env.db.Exec(`ALTER TABLE workspaces DROP COLUMN integration_branch`); err != nil {
		t.Fatal(err)
	}
	if got, err := listWorkspacesBySlugs(env.db, []string{"w1-owned"}, true); got != nil || err == nil {
		t.Errorf("listWorkspacesBySlugs = (%v, %v); want (nil, error)", got, err)
	}
}

// parseListObjects decodes a listing body.
func parseListObjects(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var objs []map[string]any
	if err := json.Unmarshal(body, &objs); err != nil {
		t.Fatalf("decoding listing %s: %v", body, err)
	}
	return objs
}

// TS-35-23 (chunks): the by-slug query merges several chunks into one list
// ordered by created_at descending (by slug where equal), without duplicates
// or losses; small chunks make the merge observable.
// Requirement: 35-REQ-4.3
func TestListWorkspacesBySlugChunks_Merge_TS_35_23(t *testing.T) {
	env := newTestEnv(t)
	var slugs []string
	for i := 0; i < 11; i++ {
		slug := fmt.Sprintf("chunk-%02d", i)
		// Three workspaces share each created_at, so ties need the slug order.
		seedListed(t, env, slug, "u1", "", "ready", fmt.Sprintf("2026-01-01T00:00:%02dZ", (i*7)%4))
		slugs = append(slugs, slug)
	}
	want, err := listWorkspacesBySlugChunks(env.db, slugs, false, len(slugs))
	if err != nil || len(want) != 11 {
		t.Fatalf("one chunk: %d workspaces, %v", len(want), err)
	}
	for i := 1; i < len(want); i++ {
		a, b := want[i-1], want[i]
		if a.CreatedAt < b.CreatedAt || (a.CreatedAt == b.CreatedAt && a.Slug > b.Slug) {
			t.Errorf("order broken at %d: %s %s before %s %s", i, a.Slug, a.CreatedAt, b.Slug, b.CreatedAt)
		}
	}
	got, err := listWorkspacesBySlugChunks(env.db, slugs, false, 3)
	if err != nil || len(got) != len(want) {
		t.Fatalf("chunks of 3: %d workspaces, %v; want %d", len(got), err, len(want))
	}
	for i := range want {
		if got[i].Slug != want[i].Slug {
			t.Errorf("chunks of 3, position %d: %s; want %s", i, got[i].Slug, want[i].Slug)
		}
	}
}
