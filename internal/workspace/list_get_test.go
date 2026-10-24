package workspace

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// seedListed inserts a workspace with an explicit created_at; org "" is NULL.
func seedListed(t *testing.T, v *env, slug, ownerID, org, status, createdAt string) {
	t.Helper()
	var orgID any
	if org != "" {
		orgID = org
	}
	testutil.Exec(t, v.db, `INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, display_name, clone_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'ready', ?, ?)`, slug, "https://github.com/org/"+slug, ownerID, orgID, status, slug, createdAt, createdAt)
}

// listed returns the slugs of a listing, in order, and the access of each.
func listed(t *testing.T, v *env, path string, auth *apikit.AuthInfo) ([]string, map[string]string) {
	t.Helper()
	rec := v.do(t, http.MethodGet, path, "", auth)
	expectStatus(t, rec, http.StatusOK)
	var slugs []string
	access := map[string]string{}
	for _, obj := range testutil.Objects(t, rec) {
		slug := obj["slug"].(string)
		slugs = append(slugs, slug)
		access[slug], _ = obj["access"].(string)
	}
	return slugs, access
}

// TestList_ReadableSet: an admin lists everything with access admin; a user
// lists its own and its orgs' workspaces, newest first, archived ones only on
// request; a stranger lists nothing.
func TestList_ReadableSet(t *testing.T) {
	v := newEnv(t)
	testutil.SeedOrg(t, v.db, "O", "org-o", "")
	testutil.SeedOrg(t, v.db, "Z", "org-z", "")
	testutil.SeedOrgMember(t, v.db, "O", member)
	testutil.SeedOrgMember(t, v.db, "Z", owner)
	seedListed(t, v, "w1-owned", member, "", "active", "2026-01-01T00:00:01Z")
	seedListed(t, v, "w2-member", owner, "O", "active", "2026-01-01T00:00:03Z")
	seedListed(t, v, "w3-archived", owner, "O", "archived", "2026-01-01T00:00:02Z")
	seedListed(t, v, "w4-other", owner, "Z", "active", "2026-01-01T00:00:04Z")

	slugs, access := listed(t, v, "/api/v1/workspaces", testutil.Admin())
	if want := []string{"w4-other", "w2-member", "w1-owned"}; !slices.Equal(slugs, want) || access["w1-owned"] != "admin" {
		t.Errorf("admin: %v %v; want %v, all admin", slugs, access, want)
	}
	slugs, _ = listed(t, v, "/api/v1/workspaces?include_archived=true", testutil.Admin())
	if want := []string{"w4-other", "w2-member", "w3-archived", "w1-owned"}; !slices.Equal(slugs, want) {
		t.Errorf("admin with archived: %v; want %v", slugs, want)
	}
	slugs, access = listed(t, v, "/api/v1/workspaces?include_archived=true", testutil.User(member))
	if want := []string{"w2-member", "w3-archived", "w1-owned"}; !slices.Equal(slugs, want) || access["w1-owned"] != "owner" || access["w2-member"] != "member" {
		t.Errorf("member: %v %v; want %v with owner and member access", slugs, access, want)
	}
	slugs, _ = listed(t, v, "/api/v1/workspaces", testutil.PAT(member, "workspaces:read"))
	if want := []string{"w2-member", "w1-owned"}; !slices.Equal(slugs, want) {
		t.Errorf("read PAT: %v; want %v", slugs, want)
	}
	if slugs, _ := listed(t, v, "/api/v1/workspaces", testutil.User(stranger)); len(slugs) != 0 {
		t.Errorf("stranger: %v; want nothing", slugs)
	}
	// A bound PAT lists at most its workspace, archived only on request.
	testutil.BindToken(t, v.db, "tok-a", "w3-archived")
	bound := testutil.BoundPAT(owner, "tok-a", "workspaces:read")
	if slugs, _ := listed(t, v, "/api/v1/workspaces", bound); len(slugs) != 0 {
		t.Errorf("bound to an archived workspace: %v; want nothing", slugs)
	}
	if slugs, _ := listed(t, v, "/api/v1/workspaces?include_archived=true", bound); !slices.Equal(slugs, []string{"w3-archived"}) {
		t.Errorf("bound with archived: %v; want w3-archived", slugs)
	}
}

// TestList_Refusals: no credential is 401, a PAT without the read scope 404,
// a failing query 500.
func TestList_Refusals(t *testing.T) {
	v := newEnv(t)
	expectStatus(t, v.do(t, http.MethodGet, "/api/v1/workspaces", "", nil), http.StatusUnauthorized)
	expectError(t, v.do(t, http.MethodGet, "/api/v1/workspaces", "", testutil.PAT(owner, "users:read")), http.StatusNotFound, "workspace not found", "")
	testutil.Exec(t, v.db, `DROP TABLE workspaces`)
	expectStatus(t, v.do(t, http.MethodGet, "/api/v1/workspaces", "", testutil.Admin()), http.StatusInternalServerError)
	expectStatus(t, v.do(t, http.MethodGet, "/api/v1/workspaces", "", testutil.User(owner)), http.StatusInternalServerError)
}

// TestList_ManySlugs: a readable set larger than one query's chunk is listed
// once per workspace, newest first, with ties broken by slug.
func TestList_ManySlugs(t *testing.T) {
	v := newEnv(t)
	for i := 0; i < listSlugChunk+1; i++ {
		seedListed(t, v, fmt.Sprintf("ws-%03d", (i*7)%(listSlugChunk+1)), owner, "", "active",
			fmt.Sprintf("2026-01-01T00:%02d:%02dZ", (i/60)%60, i%60))
	}
	slugs, _ := listed(t, v, "/api/v1/workspaces", testutil.User(owner))
	if len(slugs) != listSlugChunk+1 || len(slices.Compact(slices.Clone(slugs))) != len(slugs) {
		t.Fatalf("listed %d workspaces (%d distinct); want %d", len(slugs), len(slices.Compact(slices.Clone(slugs))), listSlugChunk+1)
	}
	rows, err := listWorkspacesBySlugChunks(v.db, slugs, false, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(rows); i++ {
		a, b := rows[i-1], rows[i]
		if a.CreatedAt < b.CreatedAt || (a.CreatedAt == b.CreatedAt && a.Slug > b.Slug) || a.Slug != slugs[i-1] {
			t.Fatalf("order broken at %d: %s %s before %s %s (listing had %s)", i, a.Slug, a.CreatedAt, b.Slug, b.CreatedAt, slugs[i-1])
		}
	}
}

// TestGet_Workspace: the owner, a member of its org and an admin read a
// workspace, each with its own access; org_slug and hub_url come from the org;
// a stranger, a bound token of another workspace and a PAT without the read
// scope get 404.
func TestGet_Workspace(t *testing.T) {
	v := newEnv(t)
	swap(t, &defaultExternalURL, "https://hub.test")
	v.orgWorkspace(t, "team")
	v.seed(t, &Workspace{Slug: "solo", OwnerID: owner})
	testutil.BindToken(t, v.db, "tok-solo", "solo")

	for _, c := range []struct {
		auth   *apikit.AuthInfo
		access string
	}{
		{testutil.User(owner), "owner"}, {testutil.User(member), "member"}, {testutil.Admin(), "admin"},
		{testutil.PAT(member, "workspaces:write"), "member"},
	} {
		rec := v.do(t, http.MethodGet, "/api/v1/workspaces/team", "", c.auth)
		expectStatus(t, rec, http.StatusOK)
		got := testutil.Object(t, rec)
		if got["slug"] != "team" || got["access"] != c.access || got["org_slug"] != "acme" || got["hub_url"] != "https://hub.test/git/acme/team.git" {
			t.Errorf("%v: %v", c.auth, got)
		}
	}
	for name, auth := range map[string]*apikit.AuthInfo{
		"stranger": testutil.User(stranger), "read PAT of a stranger": testutil.PAT(stranger, "workspaces:read"),
		"bound elsewhere": testutil.BoundPAT(owner, "tok-solo", "workspaces:read"), "PAT without scope": testutil.PAT(owner, "users:read"),
	} {
		expectError(t, v.do(t, http.MethodGet, "/api/v1/workspaces/team", "", auth), http.StatusNotFound, "workspace not found", "")
		_ = name
	}
	expectStatus(t, v.do(t, http.MethodGet, "/api/v1/workspaces/team", "", nil), http.StatusUnauthorized)
	expectError(t, v.do(t, http.MethodGet, "/api/v1/workspaces/nope", "", testutil.Admin()), http.StatusNotFound, "workspace not found", "")

	// A workspace without an org has no org_slug and no hub_url; without an
	// external URL there is no hub_url either.
	testutil.Exec(t, v.db, `UPDATE workspaces SET org_id = NULL WHERE slug = 'solo'`)
	got := testutil.Object(t, v.do(t, http.MethodGet, "/api/v1/workspaces/solo", "", testutil.User(owner)))
	if got["org_slug"] != nil || got["hub_url"] != nil {
		t.Errorf("without org: org_slug %v hub_url %v", got["org_slug"], got["hub_url"])
	}
	defaultExternalURL = ""
	if got := testutil.Object(t, v.do(t, http.MethodGet, "/api/v1/workspaces/team", "", testutil.User(owner))); got["hub_url"] != nil {
		t.Errorf("without an external URL: hub_url %v", got["hub_url"])
	}
}
