package secrets

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// Spec 35 fixture: owner-1 owns workspace "team-ws" in org O (slug "org-o"),
// whose members are owner-1 and member-2; stranger-3 belongs to no org. The
// workspace has the secret db_password and the variable K; the owner's user
// tier, O's org tier and the workspace tier each hold a variable.
const (
	sOwner    = "owner-1"
	sMember   = "member-2"
	sStranger = "stranger-3"
)

var secretScopes = []string{"secrets:manage", "secrets:read", "secrets:write", "secrets:delete", "vars:manage", "vars:read", "vars:write", "vars:delete"}

func newMemberSecretsEnv(t *testing.T) *handlerTestEnv {
	t.Helper()
	env := newHandlerTestEnv(t)
	env.seedOrg(t, "O", "Org O", "org-o")
	env.seedOrgMember(t, "O", sOwner)
	env.seedOrgMember(t, "O", sMember)
	env.seedWorkspaceForTest(t, "team-ws", sOwner)
	if _, err := env.db.Exec(`UPDATE workspaces SET org_id = 'O' WHERE slug = 'team-ws'`); err != nil {
		t.Fatal(err)
	}
	store := NewStore(env.db)
	if _, err := store.CreateSecrets("workspace", "team-ws", []EntryInput{{Key: "db_password", Value: "hunter2"}}); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ ownerType, ownerID, key, value string }{
		{"workspace", "team-ws", "K", "ws-value"},
		{"user", sOwner, "USER_TIER", "u"},
		{"org", "O", "ORG_TIER", "o"},
	} {
		if _, err := store.CreateVariables(v.ownerType, v.ownerID, []EntryInput{{Key: v.key, Value: v.value}}); err != nil {
			t.Fatal(err)
		}
	}
	return env
}

// tableDump renders the secrets and variables tables.
func tableDump(t *testing.T, env *handlerTestEnv) string {
	t.Helper()
	var b strings.Builder
	for _, table := range []string{"secrets", "variables"} {
		rows, err := env.db.Query(`SELECT * FROM ` + table + ` ORDER BY 1, 2, 3`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s %v\n", table, vals)
		}
		rows.Close()
	}
	return b.String()
}

type route struct{ method, path, body string }

var workspaceSecretRoutes = []route{
	{http.MethodPost, "/api/v1/workspaces/%s/secrets", `{"entries":[{"key":"new_secret","value":"v"}]}`},
	{http.MethodGet, "/api/v1/workspaces/%s/secrets", ""},
	{http.MethodPatch, "/api/v1/workspaces/%s/secrets/db_password", `{"value":"changed"}`},
	{http.MethodDelete, "/api/v1/workspaces/%s/secrets/db_password", ""},
}

var workspaceVarRoutes = []route{
	{http.MethodPost, "/api/v1/workspaces/%s/vars", `{"entries":[{"key":"NEW","value":"v"}]}`},
	{http.MethodGet, "/api/v1/workspaces/%s/vars", ""},
	{http.MethodGet, "/api/v1/workspaces/%s/vars/resolved", ""},
	{http.MethodPatch, "/api/v1/workspaces/%s/vars/K", `{"value":"changed"}`},
	{http.MethodDelete, "/api/v1/workspaces/%s/vars/K", ""},
}

func (r route) on(slug string) string { return fmt.Sprintf(r.path, slug) }

// wantNotFound is the body of a 404 workspace refusal.
const wantNotFound = `{"error":{"code":404,"message":"workspace not found"}}`

// TS-35-45: A stranger on every workspace secret and variable route gets 404
// workspace not found in the apikit envelope, without error_type.
// Requirement: 35-REQ-9.1
func TestStrangerSecretsAndVars_TS_35_45(t *testing.T) {
	env := newMemberSecretsEnv(t)
	for _, auth := range []*apikit.AuthInfo{userAuth(sStranger), patAuth(sStranger, secretScopes...)} {
		for _, r := range append(append([]route{}, workspaceSecretRoutes...), workspaceVarRoutes...) {
			rec := env.doRequest(t, r.method, r.on("team-ws"), r.body, auth)
			if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != wantNotFound {
				t.Errorf("%s %s %s: %d %s; want 404 %s", auth.CredentialType, r.method, r.path, rec.Code, rec.Body, wantNotFound)
			}
		}
	}
}

// TS-35-46: A member gets the stranger's 404 on every workspace secret route,
// learns no secret name, and changes nothing.
// Requirement: 35-REQ-9.2
func TestMemberSecrets_TS_35_46(t *testing.T) {
	env := newMemberSecretsEnv(t)
	before := tableDump(t, env)
	for _, auth := range []*apikit.AuthInfo{userAuth(sMember), patAuth(sMember, secretScopes...)} {
		for _, r := range workspaceSecretRoutes {
			member := env.doRequest(t, r.method, r.on("team-ws"), r.body, auth)
			stranger := env.doRequest(t, r.method, r.on("team-ws"), r.body, userAuth(sStranger))
			if member.Code != http.StatusNotFound || member.Body.String() != stranger.Body.String() ||
				strings.TrimSpace(member.Body.String()) != wantNotFound {
				t.Errorf("%s %s %s: %d %s; want the stranger's %d %s", auth.CredentialType, r.method, r.path, member.Code, member.Body, stranger.Code, stranger.Body)
			}
			if strings.Contains(member.Body.String(), "db_password") {
				t.Errorf("%s %s: the body names the secret", r.method, r.path)
			}
		}
	}
	if after := tableDump(t, env); after != before {
		t.Errorf("secrets changed:\nbefore %s\nafter %s", before, after)
	}
}

// TS-35-47: A member lists the workspace's variables like the owner.
// Requirement: 35-REQ-9.3
func TestMemberListsVars_TS_35_47(t *testing.T) {
	env := newMemberSecretsEnv(t)
	owner := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/team-ws/vars", "", userAuth(sOwner))
	for _, auth := range []*apikit.AuthInfo{userAuth(sMember), patAuth(sMember, "vars:read")} {
		member := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/team-ws/vars", "", auth)
		if owner.Code != http.StatusOK || member.Code != http.StatusOK || owner.Body.String() != member.Body.String() {
			t.Errorf("%s: owner %d %s, member %d %s; want 200 and equal bodies", auth.CredentialType, owner.Code, owner.Body, member.Code, member.Body)
		}
	}
}

// TS-35-48: A member's resolved variables are the owner's document, with the
// org tier only while the owner is a member of the org.
// Requirement: 35-REQ-9.4
func TestMemberResolvedVars_TS_35_48(t *testing.T) {
	env := newMemberSecretsEnv(t)
	resolved := func(user string) (int, string) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/team-ws/vars/resolved", "", userAuth(user))
		return rec.Code, rec.Body.String()
	}
	for round, wantOrg := range []bool{true, false} {
		if round == 1 {
			if _, err := env.db.Exec(`DELETE FROM org_members WHERE org_id = 'O' AND user_id = ?`, sOwner); err != nil {
				t.Fatal(err)
			}
		}
		oc, ob := resolved(sOwner)
		mc, mb := resolved(sMember)
		if oc != http.StatusOK || mc != http.StatusOK || ob != mb {
			t.Errorf("round %d: owner %d %s, member %d %s; want 200 and the same document", round, oc, ob, mc, mb)
		}
		var doc any
		if err := json.Unmarshal([]byte(mb), &doc); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if got := strings.Contains(mb, "ORG_TIER"); got != wantOrg {
			t.Errorf("round %d: org tier present = %v; want %v. body %s", round, got, wantOrg, mb)
		}
		if !strings.Contains(mb, "USER_TIER") || !strings.Contains(mb, `"K"`) {
			t.Errorf("round %d: body %s lacks the user or workspace tier", round, mb)
		}
	}
}

// TS-35-49: A member's variable writes are refused with 403 forbidden and
// change no variable.
// Requirement: 35-REQ-9.5
func TestMemberVarWrites_TS_35_49(t *testing.T) {
	env := newMemberSecretsEnv(t)
	before := tableDump(t, env)
	for _, auth := range []*apikit.AuthInfo{userAuth(sMember), patAuth(sMember, secretScopes...)} {
		for _, r := range []route{workspaceVarRoutes[0], workspaceVarRoutes[3], workspaceVarRoutes[4]} {
			rec := env.doRequest(t, r.method, r.on("team-ws"), r.body, auth)
			var body struct {
				Error struct {
					Message   string `json:"message"`
					ErrorType string `json:"error_type"`
				} `json:"error"`
			}
			if rec.Code != http.StatusForbidden || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
				body.Error.ErrorType != "forbidden" || body.Error.Message != "org members have read-only access to this workspace" {
				t.Errorf("%s %s %s: %d %s; want 403 forbidden", auth.CredentialType, r.method, r.path, rec.Code, rec.Body)
			}
		}
	}
	if after := tableDump(t, env); after != before {
		t.Errorf("variables changed:\nbefore %s\nafter %s", before, after)
	}
}
