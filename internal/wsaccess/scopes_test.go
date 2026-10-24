package wsaccess

import (
	"database/sql"
	"encoding/json"
	"slices"
	"testing"

	"github.com/txsvc/apikit"
	_ "modernc.org/sqlite"
)

func TestCanReadCanWrite(t *testing.T) {
	pat := func(scopes ...string) *apikit.AuthInfo {
		return &apikit.AuthInfo{CredentialType: "pat", UserID: "u", Permissions: scopes}
	}
	cases := []struct {
		name        string
		auth        *apikit.AuthInfo
		read, write bool
	}{
		{"read scope", pat("workspaces:read"), true, false},
		{"write scope", pat("workspaces:write"), true, true},
		{"other resource", pat("secrets:write"), false, false},
		{"no scopes", pat(), false, false},
		{"api key", &apikit.AuthInfo{CredentialType: "api_key", UserID: "u"}, true, true},
		{"admin token", &apikit.AuthInfo{CredentialType: "admin_token"}, true, true},
	}
	for _, c := range cases {
		if got := CanRead(c.auth, "workspaces"); got != c.read {
			t.Errorf("%s: CanRead = %v; want %v", c.name, got, c.read)
		}
		if got := CanWrite(c.auth, "workspaces"); got != c.write {
			t.Errorf("%s: CanWrite = %v; want %v", c.name, got, c.write)
		}
	}
}

func TestRenameScopes(t *testing.T) {
	got, changed := RenameScopes([]string{"workspaces:read", "workspaces:write", "secrets:read", "secrets:write", "vars:write", "vars:write", "git:read"})
	want := []string{"workspaces:read", "workspaces:write", "secrets:read", "secrets:write", "vars:write", "git:read"}
	if !changed || !slices.Equal(got, want) {
		t.Errorf("RenameScopes = %v (changed %v); want %v", got, changed, want)
	}
	got, changed = RenameScopes([]string{"workspaces:read", "forge:write"})
	if changed || !slices.Equal(got, []string{"workspaces:read", "forge:write"}) {
		t.Errorf("RenameScopes on current scopes = %v (changed %v); want unchanged", got, changed)
	}
}

func TestMigrateScopes(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE pats (token_id TEXT PRIMARY KEY, permissions TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for id, perms := range map[string]string{
		"old":     `["workspaces:write","workspaces:write","vars:write","vars:read"]`,
		"current": `["workspaces:write","audit:read"]`,
		"broken":  `not json`,
	} {
		if _, err := db.Exec(`INSERT INTO pats (token_id, permissions) VALUES (?, ?)`, id, perms); err != nil {
			t.Fatal(err)
		}
	}
	n, err := MigrateScopes(db)
	if err != nil || n != 1 {
		t.Fatalf("MigrateScopes = %d, %v; want 1 token rewritten", n, err)
	}
	var raw string
	if err := db.QueryRow(`SELECT permissions FROM pats WHERE token_id = 'old'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if want := []string{"workspaces:write", "vars:write", "vars:read"}; !slices.Equal(got, want) {
		t.Errorf("rewritten permissions = %v; want %v", got, want)
	}
	for id, want := range map[string]string{"current": `["workspaces:write","audit:read"]`, "broken": `not json`} {
		if err := db.QueryRow(`SELECT permissions FROM pats WHERE token_id = ?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if raw != want {
			t.Errorf("%s permissions = %s; want untouched %s", id, raw, want)
		}
	}
	if n, err := MigrateScopes(db); err != nil || n != 0 {
		t.Errorf("second MigrateScopes = %d, %v; want 0 (idempotent)", n, err)
	}
}
