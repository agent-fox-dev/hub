package wsaccess

import (
	"reflect"
	"testing"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// A PAT reads a resource with its read or write scope and writes it with the
// write scope only; other credentials are not scope-checked.
func TestCanReadCanWrite(t *testing.T) {
	for _, c := range []struct {
		scopes      []string
		read, write bool
	}{
		{nil, false, false},
		{[]string{"vars:read"}, true, false},
		{[]string{"vars:write"}, true, true},
		{[]string{"secrets:read", "secrets:write"}, false, false},
	} {
		pat := testutil.PAT("u", c.scopes...)
		if CanRead(pat, "vars") != c.read || CanWrite(pat, "vars") != c.write {
			t.Errorf("PAT %v: read %v write %v, want %v %v", c.scopes, CanRead(pat, "vars"), CanWrite(pat, "vars"), c.read, c.write)
		}
	}
	if !CanRead(testutil.User("u"), "vars") || !CanWrite(testutil.User("u"), "vars") || !CanWrite(testutil.Admin(), "vars") {
		t.Error("an API key or admin token was scope-checked")
	}
}

// RenameScopes replaces the retired scopes by their successors, keeps the
// first occurrence of each in order, and reports a change (a duplicate
// counts).
func TestRenameScopes(t *testing.T) {
	for _, c := range []struct {
		in, want []string
		changed  bool
	}{
		{[]string{"workspaces:read", "secrets:write"}, []string{"workspaces:read", "secrets:write"}, false},
		{[]string{"workspaces:create", "workspaces:sync", "workspaces:read"}, []string{"workspaces:write", "workspaces:read"}, true},
		{[]string{"secrets:list", "secrets:manage", "secrets:delete", "vars:manage", "vars:delete"}, []string{"secrets:read", "secrets:write", "vars:write"}, true},
		{[]string{"vars:read", "vars:read"}, []string{"vars:read"}, true},
		{[]string{}, []string{}, false},
	} {
		got, changed := RenameScopes(c.in)
		if !reflect.DeepEqual(got, c.want) || changed != c.changed {
			t.Errorf("RenameScopes(%v) = %v, %v; want %v, %v", c.in, got, changed, c.want, c.changed)
		}
	}
}

// MigrateScopes rewrites the stored PATs that name a retired scope, counts
// them, leaves the others and unreadable permissions alone, and finds nothing
// on a second run.
func TestMigrateScopes(t *testing.T) {
	db := newDB(t)
	testutil.SeedUser(t, db, "u")
	seedPAT(t, db, "old", "u", `["secrets:manage","workspaces:sync","secrets:list","workspaces:read"]`, "")
	seedPAT(t, db, "new", "u", `["workspaces:write"]`, "")
	seedPAT(t, db, "bad", "u", `not json`, "")
	n, err := MigrateScopes(db)
	if err != nil || n != 1 {
		t.Fatalf("MigrateScopes: %d, %v; want 1 token rewritten", n, err)
	}
	perms := func(id string) string {
		var p string
		if err := db.QueryRow(`SELECT permissions FROM pats WHERE token_id = ?`, id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if perms("old") != `["secrets:write","workspaces:write","secrets:read","workspaces:read"]` || perms("new") != `["workspaces:write"]` || perms("bad") != "not json" {
		t.Errorf("after migration: old %s new %s bad %s", perms("old"), perms("new"), perms("bad"))
	}
	if n, err := MigrateScopes(db); err != nil || n != 0 {
		t.Errorf("second run: %d, %v; want nothing to rewrite", n, err)
	}
	testutil.Exec(t, db, `DROP TABLE pats`)
	if _, err := MigrateScopes(db); err == nil {
		t.Error("without a pats table: nil error")
	}
}
