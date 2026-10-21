package wsaccess_test

import (
	"fmt"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// TS-35-15: ReadableSlugs for an admin token is unrestricted, without a query.
// Requirement: 35-REQ-3.1
func TestReadableSlugs_Admin_TS_35_15(t *testing.T) {
	db := openAccessDB(t, true)
	db.Close()
	slugs, all, err := wsaccess.ReadableSlugs(db, adminToken())
	if slugs != nil || !all || err != nil {
		t.Errorf("ReadableSlugs(admin) = (%v, %v, %v); want (nil, true, nil)", slugs, all, err)
	}
}

// TS-35-16: ReadableSlugs for a nil auth or an empty UserID is an empty
// non-nil map, without a query.
// Requirement: 35-REQ-3.2
func TestReadableSlugs_NoUser_TS_35_16(t *testing.T) {
	db := openAccessDB(t, true)
	db.Close()
	for _, auth := range []*apikit.AuthInfo{
		nil,
		{CredentialType: "api_key"},
		{CredentialType: "pat", TokenID: "tok", Permissions: []string{"workspaces:read"}},
	} {
		slugs, all, err := wsaccess.ReadableSlugs(db, auth)
		if slugs == nil || len(slugs) != 0 || all || err != nil {
			t.Errorf("ReadableSlugs(%+v) = (%#v, %v, %v); want (empty map, false, nil)", auth, slugs, all, err)
		}
	}
}

// TS-35-17: ReadableSlugs maps owned workspaces to owner and org workspaces
// to member, every status included.
// Requirement: 35-REQ-3.3
func TestReadableSlugs_OwnerAndMember_TS_35_17(t *testing.T) {
	db := openAccessDB(t, true)
	if _, err := db.Exec(`INSERT INTO workspaces (slug, owner_id, org_id, status) VALUES
		('W1', 'u2', NULL, 'active'), ('W2', 'u2', 'P', 'archived'), ('W3', 'u1', 'O', 'archived'),
		('W4', 'u1', 'O', 'active'), ('W5', 'u2', 'O', 'active'), ('W6', 'u1', 'Z', 'active')`); err != nil {
		t.Fatal(err)
	}
	addMember(t, db, "O", "u2")
	addMember(t, db, "Z", "u1")
	slugs, all, err := wsaccess.ReadableSlugs(db, apiKeyOf("u2"))
	want := map[string]wsaccess.Access{
		"W1": wsaccess.AccessOwner, "W2": wsaccess.AccessOwner, "W5": wsaccess.AccessOwner,
		"W3": wsaccess.AccessMember, "W4": wsaccess.AccessMember,
	}
	if !maps.Equal(slugs, want) || all || err != nil {
		t.Errorf("ReadableSlugs(u2) = (%v, %v, %v); want (%v, false, nil)", slugs, all, err, want)
	}
}

// TS-35-18: A bound PAT gets at most its bound slug, resolved before rows
// open on a single-connection database.
// Requirement: 35-REQ-3.4
func TestReadableSlugs_BoundPAT_TS_35_18(t *testing.T) {
	db := openAccessDB(t, true) // one open connection
	addWorkspace(t, db, "A", "u1", "O")
	addWorkspace(t, db, "B", "u2", nil)
	addWorkspace(t, db, "C", "u1", "Q")
	addMember(t, db, "O", "u2")
	bind(t, db, "tokA", "A")
	bind(t, db, "tokC", "C")
	pat := func(tok string) *apikit.AuthInfo {
		return &apikit.AuthInfo{CredentialType: "pat", UserID: "u2", TokenID: tok, Permissions: []string{"workspaces:read"}}
	}
	for tok, want := range map[string]map[string]wsaccess.Access{
		"tokA": {"A": wsaccess.AccessMember},
		"tokC": {},
		"tokU": {"A": wsaccess.AccessMember, "B": wsaccess.AccessOwner},
	} {
		type result struct {
			slugs map[string]wsaccess.Access
			all   bool
			err   error
		}
		done := make(chan result, 1)
		go func() {
			s, a, e := wsaccess.ReadableSlugs(db, pat(tok))
			done <- result{s, a, e}
		}()
		select {
		case r := <-done:
			if r.slugs == nil || !maps.Equal(r.slugs, want) || r.all || r.err != nil {
				t.Errorf("ReadableSlugs(%s) = (%v, %v, %v); want (%v, false, nil)", tok, r.slugs, r.all, r.err, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("ReadableSlugs(%s) deadlocked on a single-connection database", tok)
		}
	}
}

// TS-35-19: A binding lookup or readable-set query failure is returned as an
// error, and grants nothing.
// Requirement: 35-REQ-3.5
func TestReadableSlugs_Errors_TS_35_19(t *testing.T) {
	db := openAccessDB(t, true)
	if _, err := db.Exec(`DROP TABLE workspaces`); err != nil {
		t.Fatal(err)
	}
	if slugs, all, err := wsaccess.ReadableSlugs(db, apiKeyOf("u1")); slugs != nil || all || err == nil {
		t.Errorf("ReadableSlugs without workspaces = (%v, %v, %v); want (nil, false, error)", slugs, all, err)
	}

	db = openAccessDB(t, true)
	addWorkspace(t, db, "A", "u1", nil)
	if _, err := db.Exec(`DROP TABLE token_workspace_bindings`); err != nil {
		t.Fatal(err)
	}
	pat := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tok", Permissions: []string{"workspaces:read"}}
	if slugs, all, err := wsaccess.ReadableSlugs(db, pat); slugs != nil || all || err == nil {
		t.Errorf("ReadableSlugs with a broken binding table = (%v, %v, %v); want (nil, false, error)", slugs, all, err)
	}
}

// TS-35-20: ReadableSlugs agrees exactly with AuthorizeWorkspace(NeedRead)
// for every non-admin caller and every workspace.
// Requirement: 35-REQ-3.6
func TestReadableSlugs_AgreesWithAuthorize_TS_35_20(t *testing.T) {
	db := openAccessDB(t, true)
	owners := []string{"u1", "u2", "u3"}
	orgs := []any{nil, "", "O", "P", "Q"}
	statuses := []string{"active", "archived"}
	var slugs []string
	n := 0
	for _, owner := range owners {
		for _, org := range orgs {
			for _, status := range statuses {
				slug := fmt.Sprintf("w%d", n)
				n++
				if _, err := db.Exec(`INSERT INTO workspaces (slug, owner_id, org_id, status) VALUES (?, ?, ?, ?)`, slug, owner, org, status); err != nil {
					t.Fatal(err)
				}
				slugs = append(slugs, slug)
			}
		}
	}
	for _, m := range [][2]string{{"O", "u1"}, {"O", "u2"}, {"P", "u2"}, {"Q", "u4"}, {"", "u3"}} {
		addMember(t, db, m[0], m[1])
	}
	bind(t, db, "tok-w0", "w0")
	bind(t, db, "tok-w5", "w5")
	bind(t, db, "tok-ghost", "ghost")

	var callers []*apikit.AuthInfo
	for _, user := range []string{"", "u1", "u2", "u3", "u4", "u5"} {
		callers = append(callers, &apikit.AuthInfo{CredentialType: "api_key", UserID: user})
		for _, tok := range []string{"", "tok-unbound", "tok-w0", "tok-w5", "tok-ghost"} {
			callers = append(callers, &apikit.AuthInfo{CredentialType: "pat", UserID: user, TokenID: tok, Permissions: []string{"workspaces:read"}})
		}
	}
	for _, caller := range callers {
		set, all, err := wsaccess.ReadableSlugs(db, caller)
		if err != nil || all {
			t.Fatalf("ReadableSlugs(%+v) = (%v, %v, %v)", caller, set, all, err)
		}
		for _, slug := range slugs {
			ws, code, _ := wsaccess.AuthorizeWorkspace(db, caller, slug, wsaccess.NeedRead)
			access, in := set[slug]
			if in != (code == 0) {
				t.Errorf("%+v on %s: in readable set = %v; AuthorizeWorkspace status %d", caller, slug, in, code)
				continue
			}
			if code == 0 && access != ws.Access {
				t.Errorf("%+v on %s: readable access %q; AuthorizeWorkspace access %q", caller, slug, access, ws.Access)
			}
			if code != 0 && code != http.StatusNotFound {
				t.Errorf("%+v on %s: AuthorizeWorkspace status %d; want 0 or 404", caller, slug, code)
			}
		}
	}
}
