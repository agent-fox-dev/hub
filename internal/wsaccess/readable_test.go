package wsaccess

import (
	"reflect"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// ReadableSlugs is the set AuthorizeWorkspace grants with NeedRead: the
// caller's own workspaces of any status as owner and the org's as member; an
// admin token has all without a query; a credential without a user has none
// without a query; a bound PAT has at most its workspace.
func TestReadableSlugs(t *testing.T) {
	db := newDB(t)
	seedTeam(t, db)
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "archived", OwnerID: owner, Status: "archived"})
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "members-own", OwnerID: member, OrgID: orgID})
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "elsewhere", OwnerID: stranger})
	testutil.BindToken(t, db, "tok-team", wsSlug)
	testutil.BindToken(t, db, "tok-elsewhere", "elsewhere")
	cases := []struct {
		name string
		auth *apikit.AuthInfo
		want map[string]Access
		all  bool
	}{
		{"admin", testutil.Admin(), nil, true},
		{"nil", nil, map[string]Access{}, false},
		{"no user", &apikit.AuthInfo{CredentialType: "api_key"}, map[string]Access{}, false},
		{"owner", testutil.User(owner), map[string]Access{wsSlug: AccessOwner, "archived": AccessOwner, "members-own": AccessMember}, false},
		{"member", testutil.User(member), map[string]Access{wsSlug: AccessMember, "members-own": AccessOwner}, false},
		{"stranger", testutil.User(stranger), map[string]Access{"elsewhere": AccessOwner}, false},
		{"bound to the team", testutil.BoundPAT(member, "tok-team"), map[string]Access{wsSlug: AccessMember}, false},
		{"bound outside the set", testutil.BoundPAT(member, "tok-elsewhere"), map[string]Access{}, false},
	}
	for _, c := range cases {
		got, all, err := ReadableSlugs(db, c.auth)
		if err != nil || all != c.all || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: (%v, %v, %v), want (%v, %v)", c.name, got, all, err, c.want, c.all)
		}
		for slug, access := range got {
			if ws, code, _ := AuthorizeWorkspace(db, c.auth, slug, NeedRead); code != 0 || ws.Access != access {
				t.Errorf("%s: %s readable as %s but AuthorizeWorkspace says %d %v", c.name, slug, access, code, ws)
			}
		}
	}
	testutil.Exec(t, db, `DROP TABLE token_workspace_bindings`)
	if _, _, err := ReadableSlugs(db, testutil.BoundPAT(owner, "tok-team")); err == nil {
		t.Error("a failed binding lookup granted a set")
	}
	if got, all, err := ReadableSlugs(db, testutil.Admin()); err != nil || !all || got != nil {
		t.Errorf("admin without the binding table: (%v, %v, %v)", got, all, err)
	}
	testutil.Exec(t, db, `DROP TABLE workspaces`)
	if _, _, err := ReadableSlugs(db, testutil.User(owner)); err == nil {
		t.Error("a failed query granted a set")
	}
}
