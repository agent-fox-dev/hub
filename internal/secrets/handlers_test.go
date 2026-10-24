package secrets

import (
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// Every route answers 401 without a credential.
func TestHandle_RequiresCredential(t *testing.T) {
	v := newEnv(t)
	all := append(routes("/user", "secrets"), routes("/orgs/acme", "vars")...)
	all = append(all, routes("/workspaces/w", "secrets")...)
	all = append(all, route{http.MethodGet, "/workspaces/w/vars/resolved", ""})
	for _, r := range all {
		wantError(t, v.do(t, r.method, "/api/v1"+r.path, r.body, nil), http.StatusUnauthorized, "authentication required")
	}
}

// A PAT needs the resource's read scope to list and its write scope, which
// implies read, to create, update and delete; the other resource's scopes do
// not count. API keys and admin tokens are not scoped.
func TestHandle_PATNeedsResourceScope(t *testing.T) {
	v := newEnv(t)
	testutil.SeedUser(t, v.db, owner)
	cases := []struct {
		resource string
		scopes   []string
		read     bool // list is allowed
		write    bool // create, update and delete are allowed
	}{
		{"secrets", nil, false, false},
		{"secrets", []string{"vars:write", "workspaces:read"}, false, false},
		{"secrets", []string{"secrets:read"}, true, false},
		{"secrets", []string{"secrets:write"}, true, true},
		{"vars", []string{"secrets:write"}, false, false},
		{"vars", []string{"vars:read"}, true, false},
		{"vars", []string{"vars:write"}, true, true},
	}
	for _, c := range cases {
		for _, r := range routes("/user", c.resource) {
			v.seed(t, map[string]string{"secrets": "secrets", "vars": "variables"}[c.resource], "user", owner, "K", "v")
			allowed := c.read
			if r.method != http.MethodGet {
				allowed = c.write
			}
			rec := v.do(t, r.method, "/api/v1"+r.path, r.body, testutil.PAT(owner, c.scopes...))
			if allowed && rec.Code != okStatus(r.method) {
				t.Errorf("%v %s %s: %d %s, want success", c.scopes, r.method, r.path, rec.Code, rec.Body)
			}
			if !allowed && (rec.Code != http.StatusForbidden || testutil.Error(t, rec).Message != "insufficient permission scope") {
				t.Errorf("%v %s %s: %d %s, want the scope refusal", c.scopes, r.method, r.path, rec.Code, rec.Body)
			}
			testutil.Exec(t, v.db, `DELETE FROM secrets; DELETE FROM variables`)
		}
	}
}

// The user tier is the caller's own scope, so an admin token, which has no
// user identity, is refused.
func TestUserTier_RefusesAdminToken(t *testing.T) {
	v := newEnv(t)
	for _, r := range routes("/user", "vars") {
		rec := v.do(t, r.method, "/api/v1"+r.path, r.body, testutil.Admin())
		if rec.Code != http.StatusForbidden || !strings.Contains(testutil.Error(t, rec).Message, "admin tokens have no user identity") {
			t.Errorf("%s %s: %d %s", r.method, r.path, rec.Code, rec.Body)
		}
	}
}

// The org tier is visible to members of the org and admin tokens; a non-member
// gets the unknown org's 404.
func TestOrgTier_MembersAndAdminOnly(t *testing.T) {
	v := newEnv(t)
	v.seedTeam(t)
	for _, c := range []struct {
		slug string
		auth *apikit.AuthInfo
		code int
	}{
		{orgSlug, testutil.User(member), http.StatusOK},
		{orgSlug, testutil.Admin(), http.StatusOK},
		{orgSlug, testutil.User(stranger), http.StatusNotFound},
		{orgSlug, testutil.PAT(stranger, "secrets:write"), http.StatusNotFound},
		{"ghost", testutil.User(member), http.StatusNotFound},
		{"ghost", testutil.Admin(), http.StatusNotFound},
	} {
		rec := v.do(t, http.MethodGet, "/api/v1/orgs/"+c.slug+"/secrets", "", c.auth)
		if rec.Code != c.code || (c.code != http.StatusOK && testutil.Error(t, rec).Message != "not found") {
			t.Errorf("%s as %s/%s: %d %s, want %d", c.slug, c.auth.CredentialType, c.auth.UserID, rec.Code, rec.Body, c.code)
		}
	}
	want(t, v.do(t, http.MethodPost, "/api/v1/orgs/acme/vars", `{"entries":[{"key":"K","value":"v"}]}`, testutil.User(owner)), http.StatusCreated)
	if n := v.count(t, "variables", "org", orgID); n != 1 {
		t.Errorf("org variables stored under %q: %d, want 1 under the org id", orgID, n)
	}
}

// The workspace tier: the owner and admin tokens do everything; a member of
// the workspace's org reads its variables, is forbidden to write them, and
// gets the stranger's 404 on its secrets; strangers, and PATs bound to another
// workspace, get the unknown workspace's 404 everywhere.
func TestWorkspaceTier_AccessMatrix(t *testing.T) {
	v := newEnv(t)
	v.seedTeam(t)
	testutil.SeedWorkspace(t, v.db, testutil.Workspace{Slug: "other-ws", OwnerID: owner})
	testutil.BindToken(t, v.db, "tok-other", "other-ws")
	ghost := v.do(t, http.MethodGet, "/api/v1/workspaces/ghost/secrets", "", testutil.User(owner))
	wantError(t, ghost, http.StatusNotFound, "workspace not found")
	cases := []struct {
		name     string
		auth     *apikit.AuthInfo
		resource string
		read     int // status of list; 0 for success
		write    int // status of create, update and delete; 0 for success
	}{
		{"owner secrets", testutil.User(owner), "secrets", 0, 0},
		{"owner vars", testutil.PAT(owner, "vars:write"), "vars", 0, 0},
		{"admin secrets", testutil.Admin(), "secrets", 0, 0},
		{"admin vars", testutil.Admin(), "vars", 0, 0},
		{"member vars", testutil.User(member), "vars", 0, http.StatusForbidden},
		{"member secrets", testutil.PAT(member, "secrets:write"), "secrets", http.StatusNotFound, http.StatusNotFound},
		{"stranger vars", testutil.User(stranger), "vars", http.StatusNotFound, http.StatusNotFound},
		{"stranger secrets", testutil.User(stranger), "secrets", http.StatusNotFound, http.StatusNotFound},
		{"bound elsewhere", testutil.BoundPAT(owner, "tok-other", "vars:write"), "vars", http.StatusNotFound, http.StatusNotFound},
	}
	for _, c := range cases {
		for _, r := range routes("/workspaces/"+wsSlug, c.resource) {
			v.seed(t, map[string]string{"secrets": "secrets", "vars": "variables"}[c.resource], "workspace", wsSlug, "K", "v")
			wantStatus := c.read
			if r.method != http.MethodGet {
				wantStatus = c.write
			}
			rec := v.do(t, r.method, "/api/v1"+r.path, r.body, c.auth)
			switch {
			case wantStatus == 0 && rec.Code != okStatus(r.method):
				t.Errorf("%s %s %s: %d %s, want success", c.name, r.method, r.path, rec.Code, rec.Body)
			case wantStatus == http.StatusForbidden && (rec.Code != wantStatus || testutil.Error(t, rec).ErrorType != wsaccess.ErrorTypeForbidden):
				t.Errorf("%s %s %s: %d %s, want 403 with error_type forbidden", c.name, r.method, r.path, rec.Code, rec.Body)
			case wantStatus == http.StatusNotFound && (rec.Code != wantStatus || rec.Body.String() != ghost.Body.String()):
				t.Errorf("%s %s %s: %d %s, want the unknown workspace's answer", c.name, r.method, r.path, rec.Code, rec.Body)
			}
			if wantStatus != 0 && strings.Contains(rec.Body.String(), `"K"`) {
				t.Errorf("%s %s %s: the refusal names the key: %s", c.name, r.method, r.path, rec.Body)
			}
			testutil.Exec(t, v.db, `DELETE FROM secrets; DELETE FROM variables`)
		}
	}
}

// Create answers 201 with the created entries: a secret's entry has no value,
// a variable's has the plaintext.
func TestCreate_ReturnsEntries(t *testing.T) {
	v := newEnv(t)
	testutil.SeedUser(t, v.db, owner)
	body := `{"entries":[{"key":"B","value":"two"},{"key":"A","value":"one"}]}`
	secrets := testutil.Objects(t, want(t, v.do(t, http.MethodPost, "/api/v1/user/secrets", body, testutil.User(owner)), http.StatusCreated))
	if _, has := secrets[0]["value"]; len(secrets) != 2 || secrets[0]["key"] != "B" || has || secrets[0]["created_at"] == "" {
		t.Errorf("secrets created: %v", secrets)
	}
	vars := testutil.Objects(t, want(t, v.do(t, http.MethodPost, "/api/v1/user/vars", body, testutil.User(owner)), http.StatusCreated))
	if len(vars) != 2 || vars[1]["key"] != "A" || vars[1]["value"] != "one" {
		t.Errorf("variables created: %v", vars)
	}
}

// The create errors: a malformed body, invalid entries and the scope limit
// are 400, an existing key 409.
func TestCreate_Errors(t *testing.T) {
	v := newEnv(t)
	testutil.SeedUser(t, v.db, owner)
	for i := 0; i < MaxEntriesPerScope; i++ {
		v.seed(t, "variables", "user", owner, "FULL_"+strings.Repeat("X", i%3)+string(rune('A'+i/3)), "v")
	}
	v.seed(t, "secrets", "user", owner, "TAKEN", "v")
	cases := []struct {
		name, resource, body string
		code                 int
		message              string
	}{
		{"malformed", "secrets", `{"entries":`, http.StatusBadRequest, "invalid request body"},
		{"empty", "secrets", `{"entries":[]}`, http.StatusBadRequest, "entries array must not be empty"},
		{"invalid key", "secrets", `{"entries":[{"key":"my-key","value":"v"}]}`, http.StatusBadRequest, `key "my-key" contains invalid character "-"; only alphanumeric and underscore allowed`},
		{"oversized", "vars", `{"entries":[{"key":"K","value":"` + strings.Repeat("x", MaxValueSize+1) + `"}]}`, http.StatusBadRequest, "value size 262145 bytes exceeds maximum of 262144 bytes (256 KB)"},
		{"duplicate in body", "secrets", `{"entries":[{"key":"a"},{"key":"A"}]}`, http.StatusBadRequest, `duplicate key "A" in entries (case-insensitive)`},
		{"conflict", "secrets", `{"entries":[{"key":"taken","value":"v"}]}`, http.StatusConflict, `key "taken" already exists`},
		{"limit", "vars", `{"entries":[{"key":"ONE_MORE","value":"v"}]}`, http.StatusBadRequest, "maximum of 100 entries per scope exceeded"},
	}
	for _, c := range cases {
		rec := v.do(t, http.MethodPost, "/api/v1/user/"+c.resource, c.body, testutil.User(owner))
		if rec.Code != c.code || testutil.Error(t, rec).Message != c.message {
			t.Errorf("%s: %d %s, want %d %q", c.name, rec.Code, rec.Body, c.code, c.message)
		}
	}
	if n := v.count(t, "secrets", "user", owner); n != 1 {
		t.Errorf("%d secrets after the refused creates, want 1", n)
	}
}

// List answers the scope's entries sorted by key ignoring case, without the
// values of secrets and with the plaintext of variables.
func TestList_SortedEntries(t *testing.T) {
	v := newEnv(t)
	v.seedTeam(t)
	for _, key := range []string{"b", "A", "C"} {
		v.seed(t, "secrets", "org", orgID, key, "s-"+key)
		v.seed(t, "variables", "org", orgID, key, "v-"+key)
	}
	rec := want(t, v.do(t, http.MethodGet, "/api/v1/orgs/acme/secrets", "", testutil.User(member)), http.StatusOK)
	if secrets := testutil.Objects(t, rec); len(secrets) != 3 || secrets[0]["key"] != "A" || secrets[1]["key"] != "b" || strings.Contains(rec.Body.String(), "s-") {
		t.Errorf("secrets listed: %s", rec.Body)
	}
	rec = want(t, v.do(t, http.MethodGet, "/api/v1/orgs/acme/vars", "", testutil.PAT(member, "vars:read")), http.StatusOK)
	if vars := testutil.Objects(t, rec); len(vars) != 3 || vars[2]["key"] != "C" || vars[2]["value"] != "v-C" {
		t.Errorf("variables listed: %s", rec.Body)
	}
	if body := want(t, v.do(t, http.MethodGet, "/api/v1/user/vars", "", testutil.User(member)), http.StatusOK).Body.String(); strings.TrimSpace(body) != "[]" {
		t.Errorf("empty scope lists %s, want []", body)
	}
}

// Update takes a value (the empty string included) and answers the entry with
// the stored key casing and a new updated_at; a missing or null value and a
// malformed body are 400, an unknown key 404.
func TestUpdate_ValueRequired(t *testing.T) {
	v := newEnv(t)
	testutil.SeedUser(t, v.db, owner)
	testutil.Exec(t, v.db, `INSERT INTO variables VALUES ('user', 'owner', 'My_Key', ?, '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z')`, EncodeValue("old"))
	rec := want(t, v.do(t, http.MethodPatch, "/api/v1/user/vars/MY_KEY", `{"value":""}`, testutil.User(owner)), http.StatusOK)
	if got := testutil.Object(t, rec); got["key"] != "My_Key" || got["value"] != "" || got["updated_at"] == "2020-01-01T00:00:00Z" || got["created_at"] != "2020-01-01T00:00:00Z" {
		t.Errorf("updated: %v", got)
	}
	v.seed(t, "secrets", "user", owner, "S", "v")
	rec = want(t, v.do(t, http.MethodPatch, "/api/v1/user/secrets/s", `{"value":"new"}`, testutil.User(owner)), http.StatusOK)
	if got := testutil.Object(t, rec); got["key"] != "S" || got["value"] != nil {
		t.Errorf("updated secret: %v", got)
	}
	if plain, _ := v.store.GetSecretValue("user", owner, "S"); plain != "new" {
		t.Errorf("stored secret %q, want new", plain)
	}
	for _, c := range []struct {
		path, body, message string
		code                int
	}{
		{"/user/vars/MY_KEY", `{}`, "value field is required", http.StatusBadRequest},
		{"/user/vars/MY_KEY", `{"value":null}`, "value field is required", http.StatusBadRequest},
		{"/user/vars/MY_KEY", `{"value":`, "invalid request body", http.StatusBadRequest},
		{"/user/vars/MY_KEY", `{"value":"` + strings.Repeat("x", MaxValueSize+1) + `"}`, "value size 262145 bytes exceeds maximum of 262144 bytes (256 KB)", http.StatusBadRequest},
		{"/user/vars/NOPE", `{"value":"v"}`, `key "NOPE" not found`, http.StatusNotFound},
		{"/user/secrets/NOPE", `{"value":"v"}`, `key "NOPE" not found`, http.StatusNotFound},
	} {
		wantError(t, v.do(t, http.MethodPatch, "/api/v1"+c.path, c.body, testutil.User(owner)), c.code, c.message)
	}
}

// Delete removes the key ignoring case and answers 204; a key that is not
// there is 404.
func TestDelete_RemovesKey(t *testing.T) {
	v := newEnv(t)
	v.seedTeam(t)
	v.seed(t, "secrets", "workspace", wsSlug, "KEY", "v")
	v.seed(t, "variables", "workspace", wsSlug, "KEY", "v")
	for _, resource := range []string{"secrets", "vars"} {
		want(t, v.do(t, http.MethodDelete, "/api/v1/workspaces/team-ws/"+resource+"/key", "", testutil.Admin()), http.StatusNoContent)
		wantError(t, v.do(t, http.MethodDelete, "/api/v1/workspaces/team-ws/"+resource+"/key", "", testutil.User(owner)), http.StatusNotFound, `key "key" not found`)
	}
	if n := v.count(t, "secrets", "workspace", wsSlug) + v.count(t, "variables", "workspace", wsSlug); n != 0 {
		t.Errorf("%d rows left", n)
	}
}

// The resolved variables of a workspace are the owner's user tier, the
// workspace's org tier and the workspace tier merged, with each entry's
// origin; a member reads the same document as the owner; the org tier is
// dropped once the owner is no longer a member of the org, except for an
// admin token.
func TestResolved_MergesTiersOfTheOwner(t *testing.T) {
	v := newEnv(t)
	v.seedTeam(t)
	v.seed(t, "variables", "user", owner, "A", "user")
	v.seed(t, "variables", "user", member, "M", "member-tier-ignored")
	v.seed(t, "variables", "org", orgID, "B", "org")
	v.seed(t, "variables", "org", orgID, "a", "org")
	v.seed(t, "variables", "workspace", wsSlug, "C", "ws")
	resolved := func(auth *apikit.AuthInfo) string {
		rec := want(t, v.do(t, http.MethodGet, "/api/v1/workspaces/team-ws/vars/resolved", "", auth), http.StatusOK)
		var lines []string
		for _, e := range testutil.Objects(t, rec) {
			lines = append(lines, e["key"].(string)+"="+e["value"].(string)+"@"+e["origin"].(string))
		}
		return strings.Join(lines, " ")
	}
	if got := resolved(testutil.User(owner)); got != "a=org@org B=org@org C=ws@workspace" {
		t.Errorf("owner resolved %q", got)
	}
	if got := resolved(testutil.PAT(member, "vars:read")); got != "a=org@org B=org@org C=ws@workspace" {
		t.Errorf("member resolved %q, want the owner's document", got)
	}
	testutil.Exec(t, v.db, `DELETE FROM org_members WHERE user_id = ?`, owner)
	if got := resolved(testutil.User(owner)); got != "A=user@user C=ws@workspace" {
		t.Errorf("owner out of the org resolved %q, want no org tier", got)
	}
	if got := resolved(testutil.Admin()); got != "a=org@org B=org@org C=ws@workspace" {
		t.Errorf("admin resolved %q, want the org tier", got)
	}
}

// The resolved route applies the credential, scope and workspace rules of a
// variable read.
func TestResolved_Refusals(t *testing.T) {
	v := newEnv(t)
	v.seedTeam(t)
	for _, c := range []struct {
		slug    string
		auth    *apikit.AuthInfo
		code    int
		message string
	}{
		{wsSlug, testutil.PAT(owner, "secrets:write"), http.StatusForbidden, "insufficient permission scope"},
		{wsSlug, testutil.User(stranger), http.StatusNotFound, "workspace not found"},
		{"ghost", testutil.User(owner), http.StatusNotFound, "workspace not found"},
	} {
		wantError(t, v.do(t, http.MethodGet, "/api/v1/workspaces/"+c.slug+"/vars/resolved", "", c.auth), c.code, c.message)
	}
	if body := want(t, v.do(t, http.MethodGet, "/api/v1/workspaces/team-ws/vars/resolved", "", testutil.User(owner)), http.StatusOK).Body.String(); strings.TrimSpace(body) != "[]" {
		t.Errorf("nothing to resolve: %s, want []", body)
	}
}
