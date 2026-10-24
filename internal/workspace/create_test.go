package workspace

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

func create(t *testing.T, v *env, body string) *httptest.ResponseRecorder {
	t.Helper()
	return v.do(t, http.MethodPost, "/api/v1/workspaces", body, testutil.User(owner))
}

// TestCreate_Defaults: a minimal request creates an active, pending workspace
// in the caller's personal org with every default, enqueues its clone job and
// records the event.
func TestCreate_Defaults(t *testing.T) {
	v := newEnv(t)
	jobs := captureQueue(t)
	swap(t, &defaultExternalURL, "https://hub.test")

	rec := create(t, v, workspaceBody("alpha", `"branch":"main"`))
	expectStatus(t, rec, http.StatusCreated)
	got := testutil.Object(t, rec)
	want := map[string]any{
		"slug": "alpha", "git_url": "https://github.com/org/alpha", "branch": "main", "owner_id": owner,
		"org_id": "personal-org-owner", "org_slug": "personal-owner", "access": "owner", "status": "active",
		"display_name": "alpha", "description": "", "clone_status": "pending", "head_sha": nil, "clone_error": nil,
		"sync_mode": "pull_only", "sync_status": "idle", "upstream_head_sha": nil, "last_sync_at": nil,
		"sync_error": nil, "workspace_mode": "standard", "upstream_url": nil, "integration_branch": nil,
		"hub_url": "https://hub.test/git/personal-owner/alpha.git",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %v; want %v", k, got[k], w)
		}
	}
	if got["created_at"] != got["updated_at"] || got["created_at"] == "" {
		t.Errorf("timestamps %v / %v", got["created_at"], got["updated_at"])
	}
	if s := got["settings"].(map[string]any); s["patch_branch_source"] != "hub" || s["auto_rebuild_after_push"] != true {
		t.Errorf("settings = %v; want the defaults", s)
	}
	if j := jobs(); len(j) != 1 || j[0].Slug != "alpha" || j[0].GitURL != "https://github.com/org/alpha" || *j[0].Branch != "main" {
		t.Errorf("enqueued %+v; want alpha's clone job", j)
	}
	ev := v.events.one(t, "hub.workspace.create")
	if ev.ActorID != owner || ev.ActorType != "api_key" || ev.ResourceType != "workspace" || ev.ResourceID != "alpha" ||
		ev.Action != "create" || ev.Workspace != "alpha" || ev.Metadata["git_url"] != "https://github.com/org/alpha" || ev.Metadata["branch"] != "main" {
		t.Errorf("event %+v", ev)
	}
}

// TestCreate_Options: the optional fields are validated, normalized and stored.
func TestCreate_Options(t *testing.T) {
	v := newEnv(t)
	rec := create(t, v, workspaceBody("full", `"display_name":"Full","description":"d","sync_mode":"disabled",
		"workspace_mode":"carry_patch","upstream_url":"git@github.com:up/repo.git","integration_branch":"integration",
		"settings":{"patch_branch_source":"origin","auto_rebuild_after_push":false}`))
	expectStatus(t, rec, http.StatusCreated)
	got := testutil.Object(t, rec)
	if got["display_name"] != "Full" || got["description"] != "d" || got["sync_mode"] != "disabled" ||
		got["workspace_mode"] != "carry_patch" || got["upstream_url"] != "git@github.com:up/repo.git" || got["integration_branch"] != "integration" {
		t.Errorf("response %v", got)
	}
	if s := got["settings"].(map[string]any); s["patch_branch_source"] != "origin" || s["auto_rebuild_after_push"] != false || s["rebuild_strategy"] != "rebase" {
		t.Errorf("settings = %v", s)
	}
	ws, _ := getWorkspaceBySlug(v.db, "full")
	if ws.SyncMode != "disabled" || ws.WorkspaceMode != "carry_patch" || *ws.IntegrationBranch != "integration" || ws.Settings.PatchBranchSource != "origin" {
		t.Errorf("stored %+v", ws)
	}

	// Null and empty display names fall back to the slug; a carry_patch
	// workspace without an integration branch gets deploy.
	rec = create(t, v, workspaceBody("cp-default", `"display_name":null,"description":null,"workspace_mode":"carry_patch","upstream_url":"https://github.com/up/repo","integration_branch":""`))
	expectStatus(t, rec, http.StatusCreated)
	if got := testutil.Object(t, rec); got["display_name"] != "cp-default" || got["description"] != "" || got["integration_branch"] != "deploy" {
		t.Errorf("response %v", got)
	}
}

// TestCreate_Validation: each malformed field is refused with 400 and a message
// naming it, before anything is stored.
func TestCreate_Validation(t *testing.T) {
	v := newEnv(t)
	long := strings.Repeat("x", 1025)
	cases := map[string]string{
		`{"slug":"ab","git_url":"https://h/p"}`:                                                                                         "slug must be at least 3",
		`{"slug":"` + strings.Repeat("a", 65) + `","git_url":"https://h/p"}`:                                                            "slug must be at most 64",
		`{"slug":"1abc","git_url":"https://h/p"}`:                                                                                       "start with a lowercase letter",
		`{"slug":"abc-","git_url":"https://h/p"}`:                                                                                       "must not end with a hyphen",
		`{"slug":"a--b","git_url":"https://h/p"}`:                                                                                       "consecutive hyphens",
		`{"slug":"aBc","git_url":"https://h/p"}`:                                                                                        "only lowercase letters",
		`{"slug":"abc","git_url":""}`:                                                                                                   "git_url must not be empty",
		`{"slug":"abc","git_url":"http://h/p"}`:                                                                                         "not plain HTTP",
		`{"slug":"abc","git_url":"https://host"}`:                                                                                       "HTTPS format must be",
		`{"slug":"abc","git_url":"git@host"}`:                                                                                           "SSH format must be",
		`{"slug":"abc","git_url":"ftp://h/p"}`:                                                                                          "HTTPS (https://) or SSH (git@)",
		`{"slug":"abc","git_url":"https://h/p","branch":""}`:                                                                            "branch must not be empty",
		`{"slug":"abc","git_url":"https://h/p","branch":"a b"}`:                                                                         "spaces",
		`{"slug":"abc","git_url":"https://h/p","branch":"a..b"}`:                                                                        "'..'",
		`{"slug":"abc","git_url":"https://h/p","branch":"x.lock"}`:                                                                      "'.lock'",
		`{"slug":"abc","git_url":"https://h/p","branch":"x."}`:                                                                          "end with '.'",
		`{"slug":"abc","git_url":"https://h/p","branch":"a/.b"}`:                                                                        "leading dot",
		`{"slug":"abc","git_url":"https://h/p","branch":"a~b"}`:                                                                         "'~'",
		`{"slug":"abc","git_url":"https://h/p","branch":"-flag"}`:                                                                       "invalid branch",
		`{"slug":"abc","git_url":"https://h/p","display_name":"` + long + `"}`:                                                          "display_name must not exceed 128",
		`{"slug":"abc","git_url":"https://h/p","description":"` + long + `"}`:                                                           "description must not exceed 1024",
		`{"slug":"abc","git_url":"https://h/p","sync_mode":"weekly"}`:                                                                   "invalid sync_mode",
		`{"slug":"abc","git_url":"https://h/p","workspace_mode":"fork"}`:                                                                "invalid workspace_mode",
		`{"slug":"abc","git_url":"https://h/p","workspace_mode":"carry_patch"}`:                                                         "upstream_url is required",
		`{"slug":"abc","git_url":"https://h/p","workspace_mode":"carry_patch","upstream_url":"http://u/p"}`:                             "invalid upstream_url",
		`{"slug":"abc","git_url":"https://h/p","workspace_mode":"carry_patch","upstream_url":"https://u/p","integration_branch":"a b"}`: "invalid integration_branch",
		`{"slug":"abc","git_url":"https://h/p","upstream_url":"https://u/p"}`:                                                           "upstream_url is not allowed",
		`{"slug":"abc","git_url":"https://h/p","integration_branch":"deploy"}`:                                                          "integration_branch is not allowed",
		`{"slug":"abc","git_url":"https://h/p","settings":{"rebuild_strategy":"squash"}}`:                                               "rebuild_strategy must be one of",
		`{"slug":"abc","git_url":"https://h/p","git_pat":""}`:                                                                           "git_pat must not be empty",
		`{"slug":"abc","git_url":"https://h/p","git_username":"","git_password":"p"}`:                                                   "git_username must not be empty",
		`{"slug":"abc","git_url":"https://h/p","git_username":"u","git_password":""}`:                                                   "git_password must not be empty",
		`{"slug":"abc","git_url":"https://h/p","git_pat":"t","git_username":"u","git_password":"p"}`:                                    "mutually exclusive",
		`{"slug":"abc","git_url":"https://h/p","git_username":"u"}`:                                                                     "must both be provided",
		`{"slug":"abc","git_url":"https://h/p","git_password":"p"}`:                                                                     "must both be provided",
		`{"slug":"abc","git_url":"git@h:p","git_pat":"t"}`:                                                                              "credentials require an HTTPS git_url",
		`not json`: "invalid request body",
		``:         "invalid request body",
	}
	for body, want := range cases {
		if msg := expectStatus(t, create(t, v, body), http.StatusBadRequest); !strings.Contains(msg, want) {
			t.Errorf("%s: message %q; want %q", body, msg, want)
		}
	}
	if n := testutil.Count(t, v.db, `SELECT COUNT(*) FROM workspaces`); n != 0 {
		t.Errorf("%d workspaces stored by refused requests", n)
	}
}

// TestCreate_Refusals: who may create, and what a conflicting slug gets.
func TestCreate_Refusals(t *testing.T) {
	v := newEnv(t)
	v.seed(t, &Workspace{Slug: "taken", OwnerID: stranger})
	cases := []struct {
		name string
		auth func() *apikit.AuthInfo
		body string
		code int
		msg  string
	}{
		{"unauthenticated", nil, workspaceBody("abc", ""), http.StatusUnauthorized, "authentication required"},
		{"admin token", func() *apikit.AuthInfo { return testutil.Admin() }, workspaceBody("abc", ""), http.StatusForbidden, "admin tokens cannot create"},
		{"read PAT", func() *apikit.AuthInfo { return testutil.PAT(owner, "workspaces:read") }, workspaceBody("abc", ""), http.StatusForbidden, "workspaces:write scope"},
		{"duplicate slug", func() *apikit.AuthInfo { return testutil.User(owner) }, workspaceBody("taken", ""), http.StatusConflict, "already exists"},
	}
	for _, c := range cases {
		var auth *apikit.AuthInfo
		if c.auth != nil {
			auth = c.auth()
		}
		rec := v.do(t, http.MethodPost, "/api/v1/workspaces", c.body, auth)
		if msg := expectStatus(t, rec, c.code); !strings.Contains(msg, c.msg) {
			t.Errorf("%s: %q; want %q", c.name, msg, c.msg)
		}
	}
	// A write PAT creates, owned by its user.
	rec := v.do(t, http.MethodPost, "/api/v1/workspaces", workspaceBody("by-pat", ""), testutil.PAT(owner, "workspaces:write"))
	expectStatus(t, rec, http.StatusCreated)
	if testutil.Object(t, rec)["owner_id"] != owner {
		t.Error("the PAT's workspace is not owned by its user")
	}
}

// TestCreate_Org: an explicit org needs membership and resolves a slug to the
// org's id; without one the personal org is used, and its absence or a lookup
// failure is refused.
func TestCreate_Org(t *testing.T) {
	v := newEnv(t)
	testutil.SeedOrg(t, v.db, "org-acme", "acme", "")
	testutil.SeedOrgMember(t, v.db, "org-acme", owner)
	testutil.SeedOrg(t, v.db, "org-other", "other", "")

	for body, want := range map[string]struct {
		code int
		msg  string
	}{
		workspaceBody("by-slug", `"org_id":"acme"`):     {http.StatusCreated, ""},
		workspaceBody("by-id", `"org_id":"org-acme"`):   {http.StatusCreated, ""},
		workspaceBody("not-member", `"org_id":"other"`): {http.StatusForbidden, "not a member"},
		workspaceBody("no-org", `"org_id":"nope"`):      {http.StatusBadRequest, "organization not found"},
		workspaceBody("empty-org", `"org_id":""`):       {http.StatusCreated, ""},
	} {
		rec := create(t, v, body)
		if msg := expectStatus(t, rec, want.code); !strings.Contains(msg, want.msg) {
			t.Errorf("%s: %q; want %q", body, msg, want.msg)
		}
	}
	for slug, org := range map[string]string{"by-slug": "org-acme", "by-id": "org-acme", "empty-org": "personal-org-owner"} {
		if got := v.column(t, slug, "org_id"); got != org {
			t.Errorf("%s in org %q; want %q", slug, got, org)
		}
	}

	// A failing membership service is a 500.
	swap(t, &orgMembershipCheckFn, func(*sql.DB, string, string) (int, string) {
		return http.StatusInternalServerError, "membership check failed"
	})
	expectStatus(t, create(t, v, workspaceBody("svc", `"org_id":"acme"`)), http.StatusInternalServerError)
	swap(t, &orgMembershipCheckFn, checkOrgMembership)

	// Two personal orgs: the first is used. None: 400. A failing lookup: 500.
	testutil.SeedOrg(t, v.db, "personal-org-owner-2", "personal-owner-2", owner)
	expectStatus(t, create(t, v, workspaceBody("two-orgs", "")), http.StatusCreated)
	if got := v.column(t, "two-orgs", "org_id"); got != "personal-org-owner" {
		t.Errorf("org_id %q; want the first personal org", got)
	}
	testutil.Exec(t, v.db, `DELETE FROM orgs WHERE owner_id = ?`, owner)
	if msg := expectStatus(t, create(t, v, workspaceBody("orphan", "")), http.StatusBadRequest); !strings.Contains(msg, "no personal organization") {
		t.Errorf("message %q", msg)
	}
	testutil.Exec(t, v.db, `DROP TABLE orgs`)
	expectStatus(t, create(t, v, workspaceBody("orphan", "")), http.StatusInternalServerError)
	if n := testutil.Count(t, v.db, `SELECT COUNT(*) FROM workspaces WHERE slug = 'orphan'`); n != 0 {
		t.Error("a refused request stored a workspace")
	}
}

// TestCreate_Credentials: git credentials are checked against the remote with
// the credential type's auth, stored as the workspace's secrets after the row,
// and never echoed.
func TestCreate_Credentials(t *testing.T) {
	v := newEnv(t)
	var seen []*githttp.BasicAuth
	var fail error
	swap(t, &validateCredentialsFn, func(_ context.Context, _ string, auth transport.AuthMethod) error {
		seen = append(seen, auth.(*githttp.BasicAuth))
		return fail
	})
	expectStatus(t, create(t, v, workspaceBody("with-pat", `"git_pat":"tok-secret"`)), http.StatusCreated)
	expectStatus(t, create(t, v, workspaceBody("with-basic", `"git_username":"bob","git_password":"pw-secret"`)), http.StatusCreated)
	if len(seen) != 2 || *seen[0] != (githttp.BasicAuth{Username: "x-token-auth", Password: "tok-secret"}) || *seen[1] != (githttp.BasicAuth{Username: "bob", Password: "pw-secret"}) {
		t.Errorf("validated with %+v", seen)
	}
	store := secrets.NewStore(v.db)
	if pat, _ := store.GetSecretValue("workspace", "with-pat", "GIT_PAT"); pat != "tok-secret" {
		t.Errorf("GIT_PAT = %q", pat)
	}
	if u, _ := store.GetSecretValue("workspace", "with-basic", "GIT_USERNAME"); u != "bob" {
		t.Errorf("GIT_USERNAME = %q", u)
	}
	if p, _ := store.GetSecretValue("workspace", "with-basic", "GIT_PASSWORD"); p != "pw-secret" {
		t.Errorf("GIT_PASSWORD = %q", p)
	}
	if list, _ := store.ListSecrets("workspace", "with-pat"); len(list) != 1 {
		t.Errorf("secrets of with-pat = %v; want GIT_PAT only", list)
	}

	// A rejected credential is a sanitised 400 that names the URL only.
	fail = errors.New("authentication required: tok-bad")
	rec := create(t, v, workspaceBody("bad-cred", `"git_pat":"tok-bad"`))
	if msg := expectStatus(t, rec, http.StatusBadRequest); msg != "credential validation failed for https://github.com/org/bad-cred: unable to authenticate" {
		t.Errorf("message %q", msg)
	}
	if n := testutil.Count(t, v.db, `SELECT COUNT(*) FROM workspaces WHERE slug = 'bad-cred'`); n != 0 {
		t.Error("a workspace was stored before the credential check")
	}
	fail = nil

	// Without credentials nothing is validated or stored.
	expectStatus(t, create(t, v, workspaceBody("public", "")), http.StatusCreated)
	if len(seen) != 3 {
		t.Errorf("%d validations; want the public repository skipped", len(seen)-3)
	}
	if list, _ := store.ListSecrets("workspace", "public"); len(list) != 0 {
		t.Errorf("secrets of public = %v", list)
	}
	for _, body := range []string{"tok-secret", "pw-secret"} {
		if strings.Contains(rec.Body.String(), body) {
			t.Errorf("the response holds a credential: %s", rec.Body)
		}
	}
}

// TestCreate_CredentialStorageFailure: when the secrets cannot be stored the
// workspace row is deleted again and the request fails; a failed compensation
// is logged and still answers 500.
func TestCreate_CredentialStorageFailure(t *testing.T) {
	v := newEnv(t)
	logs := captureLog(t)
	testutil.Exec(t, v.db, `DROP TABLE secrets`)
	if msg := expectStatus(t, create(t, v, workspaceBody("lost", `"git_pat":"tok-secret"`)), http.StatusInternalServerError); msg != "failed to store credentials" {
		t.Errorf("message %q", msg)
	}
	if n := testutil.Count(t, v.db, `SELECT COUNT(*) FROM workspaces WHERE slug = 'lost'`); n != 0 {
		t.Error("the workspace row survived the failed credential storage")
	}

	compensated := false
	swap(t, &compensatingDeleteFn, func(*sql.DB, string) error { compensated = true; return errors.New("locked") })
	expectStatus(t, create(t, v, workspaceBody("stuck", `"git_pat":"tok-secret"`)), http.StatusInternalServerError)
	if !compensated || !strings.Contains(logs.String(), `CRITICAL: compensating DELETE failed for workspace "stuck"`) {
		t.Errorf("compensated %v; logs: %s", compensated, logs)
	}
	if strings.Contains(logs.String(), "tok-secret") {
		t.Errorf("the logs hold the credential: %s", logs)
	}
}

// TestCreate_StoreFailure: a database failure while inserting is a 500.
func TestCreate_StoreFailure(t *testing.T) {
	v := newEnv(t)
	testutil.Exec(t, v.db, `CREATE TRIGGER no_insert BEFORE INSERT ON workspaces BEGIN SELECT RAISE(FAIL, 'down'); END`)
	if msg := expectStatus(t, create(t, v, workspaceBody("down", "")), http.StatusInternalServerError); msg != "failed to create workspace" {
		t.Errorf("message %q", msg)
	}
}
