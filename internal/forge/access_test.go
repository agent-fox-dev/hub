package forge

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

func TestPermissions_AreForgeReadAndWrite(t *testing.T) {
	want := []apikit.Permission{{Resource: "forge", Action: "read"}, {Resource: "forge", Action: "write"}}
	if got := Permissions(); !reflect.DeepEqual(got, want) {
		t.Errorf("Permissions() = %v; want %v", got, want)
	}
}

// forge:write implies forge:read but not the other way round; API keys and admin tokens
// hold both without scopes; a credential type hub does not know holds nothing.
func TestScopes_ReadAndWrite(t *testing.T) {
	mystery := &apikit.AuthInfo{CredentialType: "mystery", Permissions: []string{"forge:read", "forge:write"}}
	for _, tc := range []struct {
		name        string
		auth        *apikit.AuthInfo
		read, write bool
	}{
		{"api key", apiKeyAuth(), true, true},
		{"admin token", adminAuth(), true, true},
		{"pat with forge:read", patAuth("forge:read"), true, false},
		{"pat with forge:write", patAuth("forge:write"), true, true},
		{"pat with another scope", patAuth("sandboxes:write"), false, false},
		{"pat with no scope", patAuth(), false, false},
		{"unknown type with the scopes", mystery, false, false},
	} {
		if got := canReadForge(tc.auth); got != tc.read {
			t.Errorf("%s: canReadForge = %v; want %v", tc.name, got, tc.read)
		}
		if got := canWriteForge(tc.auth); got != tc.write {
			t.Errorf("%s: canWriteForge = %v; want %v", tc.name, got, tc.write)
		}
	}
}

// Every forge route applies the shared checks in order, before any lookup of a
// credential or a variable and before any request to the forge: no credential is 401; a
// PAT without the scope of the verb and an unknown credential type are 403 naming the
// scope or the type, for an unknown slug too; a stranger and an unknown slug get one 404;
// a member of the workspace's org reads but is 403 forbidden on a write; a write on a
// workspace that is not active is 400 after the ownership check, except the webhook unset.
func TestAccess_SharedChecksOnEveryRoute(t *testing.T) {
	e := memberEnv(t)
	addWorkspace(t, e.db, "theirs", withOwner(otherID))
	addWorkspace(t, e.db, "retired", archived)
	addWorkspace(t, e.db, "theirs-retired", withOwner(otherID), archived)
	stranger := testutil.User("user-stranger")
	mystery := &apikit.AuthInfo{UserID: ownerID, CredentialType: "mystery", Permissions: []string{"forge:read", "forge:write"}}
	member := testutil.User(memberID)

	type check struct {
		name      string
		slug      string
		auth      *apikit.AuthInfo
		status    int
		message   string
		errorType string
	}
	run := func(routes []route, verb string, checks []check) {
		for _, r := range routes {
			for _, c := range checks {
				rec := e.sendRoute(r, c.slug, c.auth)
				if rec.Code != c.status {
					t.Errorf("%s, %s: status = %d; want %d\n%s", r.name, c.name, rec.Code, c.status, rec.Body)
					continue
				}
				got := testutil.Error(t, rec)
				if msg := strings.ReplaceAll(c.message, "VERB", verb); !strings.Contains(got.Message, msg) || got.ErrorType != c.errorType {
					t.Errorf("%s, %s: error = %+v; want %q with error_type %q", r.name, c.name, got, msg, c.errorType)
				}
			}
		}
	}
	shared := []check{
		{"no credential", "widgets", nil, 401, "authentication required", ""},
		{"a PAT without the scope", "widgets", patAuth("sandboxes:read"), 403, "PAT requires forge:VERB scope", ""},
		{"a PAT without the scope on an unknown slug", "ghost", patAuth("workspaces:read"), 403, "PAT requires forge:VERB scope", ""},
		{"an unknown credential type", "widgets", mystery, 403, `credential type "mystery" may not VERB`, ""},
		{"a stranger", "widgets", stranger, 404, "workspace not found", ""},
		{"an unknown slug", "ghost", apiKeyAuth(), 404, "workspace not found", ""},
		{"another user's archived workspace", "theirs-retired", apiKeyAuth(), 404, "workspace not found", ""},
	}
	reads := append([]route{otherRoutes[0]}, readRoutes...)
	run(reads, "read", shared)
	writes := append([]route{otherRoutes[1]}, writeRoutes...)
	run(writes, "write", append(shared,
		check{"a PAT with forge:read only", "widgets", patAuth("forge:read"), 403, "PAT requires forge:write scope", ""},
		check{"a member", "widgets", member, 403, "org members have read-only access", "forbidden"},
		check{"a member on an archived workspace", "retired", testutil.User(memberID), 403, "org members have read-only access", "forbidden"},
		check{"an archived workspace", "retired", apiKeyAuth(), 400, "workspace is not active", ""},
	))
	// The unset of the webhook works on a workspace of any status.
	run([]route{otherRoutes[2]}, "write", append(shared,
		check{"a member", "widgets", member, 403, "org members have read-only access", "forbidden"}))
	if rec := e.sendRoute(otherRoutes[2], "retired", apiKeyAuth()); rec.Code != http.StatusNoContent {
		t.Errorf("unset webhook on an archived workspace: status = %d; want 204", rec.Code)
	}
	if n := e.originCalls.Load() + e.upstreamCalls.Load() + e.varCalls.Load() + int64(e.stub.count()); n != 0 {
		t.Errorf("%d credential, variable or forge lookups for refused requests; want none", n)
	}
}

// The credentials the checks let through: a write-only PAT reads, an api_key and an
// admin token on another user's workspace pass without scopes, a member reads with the
// workspace's credential and sees the same answer as the owner.
func TestAccess_AllowedCredentials(t *testing.T) {
	e := memberEnv(t)
	e.stub.setHandler(respond(http.StatusOK, repositoryBody))
	addWorkspace(t, e.db, "theirs", withOwner(otherID))
	e.setVar("theirs", "FORGE_API_URL", e.stub.URL)
	for _, tc := range []struct {
		name string
		slug string
		auth *apikit.AuthInfo
	}{
		{"a PAT with forge:write only", "widgets", patAuth("forge:write")},
		{"a PAT with forge:read", "widgets", patAuth("forge:read")},
		{"an api key", "widgets", apiKeyAuth()},
		{"an admin token on another user's workspace", "theirs", adminAuth()},
		{"a member's api key", "widgets", testutil.User(memberID)},
		{"a member's PAT", "widgets", testutil.PAT(memberID, "forge:read")},
	} {
		rec := e.get("/workspaces/"+tc.slug+"/forge/repository", tc.auth)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d; want 200\n%s", tc.name, rec.Code, rec.Body)
		}
	}
	for _, r := range e.stub.received() {
		if bearer(r) != "origin-token" {
			t.Errorf("a read carried the token %q; want the workspace's", bearer(r))
		}
	}
	owner, member := e.get("/workspaces/widgets/forge", apiKeyAuth()), e.get("/workspaces/widgets/forge", testutil.User(memberID))
	if owner.Body.String() != member.Body.String() || !strings.Contains(member.Body.String(), `"credential":"pat"`) {
		t.Errorf("the diagnostic differs for a member: %s vs %s", member.Body, owner.Body)
	}
	for _, r := range writeRoutes {
		if rec := e.sendRoute(r, "widgets", patAuth("forge:write")); rec.Code == 401 || rec.Code == 403 || rec.Code == 404 {
			t.Errorf("%s as a PAT with forge:write: status = %d; want the checks passed", r.name, rec.Code)
		}
	}
}

// A member's writes change nothing: no forge request and the webhook secret as it was.
func TestAccess_MemberWritesChangeNothing(t *testing.T) {
	e := memberEnv(t)
	if _, err := e.secrets.CreateSecrets("workspace", "widgets", []secrets.EntryInput{{Key: webhookSecretKey, Value: "s3cret-hook-of-16"}}); err != nil {
		t.Fatal(err)
	}
	for _, r := range append(writeRoutes, otherRoutes[1], otherRoutes[2]) {
		if rec := e.sendRoute(r, "widgets", testutil.PAT(memberID, "forge:write")); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d; want 403", r.name, rec.Code)
		}
	}
	if stored, _ := e.storedSecret("widgets"); stored != "s3cret-hook-of-16" || e.stub.count() != 0 {
		t.Errorf("secret = %q, forge requests %d; want the secret kept and no request", stored, e.stub.count())
	}
}

// A PAT bound to another workspace does not see this one, with the 404 of an unknown slug.
func TestAccess_BoundTokenOfAnotherWorkspaceIs404(t *testing.T) {
	e := widgetsEnv(t)
	addWorkspace(t, e.db, "other")
	testutil.BindToken(t, e.db, "tok-1", "other")
	bound := testutil.BoundPAT(ownerID, "tok-1", "forge:read", "forge:write")
	for _, r := range []route{readRoutes[1], writeRoutes[0], otherRoutes[0]} {
		if rec := e.sendRoute(r, "widgets", bound); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d; want 404\n%s", r.name, rec.Code, rec.Body)
		}
	}
	e.stub.setHandler(respond(http.StatusOK, repositoryBody))
	e.setVar("other", "FORGE_API_URL", e.stub.URL)
	if rec := e.get("/workspaces/other/forge/repository", bound); rec.Code != http.StatusOK {
		t.Errorf("the bound workspace: status = %d; want 200\n%s", rec.Code, rec.Body)
	}
}
