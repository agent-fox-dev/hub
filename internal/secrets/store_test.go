package secrets

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// kind adapts the two resources of the store to one shape: entries are
// reduced to their keys, and value reads the stored plaintext.
type kind struct {
	table  string
	create func(s *Store, ownerType, ownerID string, entries []EntryInput) ([]string, error)
	list   func(s *Store, ownerType, ownerID string) ([]string, error)
	update func(s *Store, ownerType, ownerID, key, value string) (string, error)
	remove func(s *Store, ownerType, ownerID, key string) error
	value  func(s *Store, ownerType, ownerID, key string) (string, error)
}

var kinds = []kind{
	{
		table: "secrets",
		create: func(s *Store, ot, oid string, in []EntryInput) ([]string, error) {
			out, err := s.CreateSecrets(ot, oid, in)
			return keysOf(out, func(e SecretEntry) string { return e.Key }), err
		},
		list: func(s *Store, ot, oid string) ([]string, error) {
			out, err := s.ListSecrets(ot, oid)
			return keysOf(out, func(e SecretEntry) string { return e.Key }), err
		},
		update: func(s *Store, ot, oid, key, value string) (string, error) {
			e, err := s.UpdateSecret(ot, oid, key, value)
			if err != nil {
				return "", err
			}
			return e.Key, nil
		},
		remove: (*Store).DeleteSecret,
		value:  (*Store).GetSecretValue,
	},
	{
		table: "variables",
		create: func(s *Store, ot, oid string, in []EntryInput) ([]string, error) {
			out, err := s.CreateVariables(ot, oid, in)
			return keysOf(out, func(e VariableEntry) string { return e.Key }), err
		},
		list: func(s *Store, ot, oid string) ([]string, error) {
			out, err := s.ListVariables(ot, oid)
			return keysOf(out, func(e VariableEntry) string { return e.Key }), err
		},
		update: func(s *Store, ot, oid, key, value string) (string, error) {
			e, err := s.UpdateVariable(ot, oid, key, value)
			if err != nil {
				return "", err
			}
			return e.Key, nil
		},
		remove: (*Store).DeleteVariable,
		value:  (*Store).GetVariableValue,
	},
}

func keysOf[E any](entries []E, key func(E) string) []string {
	if entries == nil {
		return nil
	}
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = key(e)
	}
	return keys
}

func (v *env) count(t *testing.T, table, ownerType, ownerID string) int {
	t.Helper()
	return testutil.Count(t, v.db, `SELECT COUNT(*) FROM `+table+` WHERE owner_type = ? AND owner_id = ?`, ownerType, ownerID)
}

// Both tables exist after InitSchema, which can run again, and accept only the
// three owner types.
func TestInitSchema_OwnerTypes(t *testing.T) {
	v := newEnv(t)
	if err := InitSchema(v.db); err != nil {
		t.Fatalf("second InitSchema: %v", err)
	}
	for _, table := range []string{"secrets", "variables"} {
		for _, ownerType := range []string{"user", "org", "workspace"} {
			v.seed(t, table, ownerType, "o", "K", "v")
		}
		if _, err := v.db.Exec(`INSERT INTO ` + table + ` VALUES ('team', 'o', 'K', 'dg==', '', '')`); err == nil {
			t.Errorf("%s accepted owner_type team", table)
		}
	}
}

// Create stores the values encoded and returns the entries; the list is
// sorted by key ignoring case, and empty (not nil) for an empty scope.
func TestCreate_StoresEncodedAndListsSorted(t *testing.T) {
	v := newEnv(t)
	for _, k := range kinds {
		keys, err := k.create(v.store, "user", "u", []EntryInput{{Key: "beta", Value: "2"}, {Key: "ALPHA", Value: "1"}, {Key: "Gamma", Value: "3"}})
		if err != nil || strings.Join(keys, ",") != "beta,ALPHA,Gamma" {
			t.Fatalf("%s create: %v, %v", k.table, keys, err)
		}
		var stored string
		if err := v.db.QueryRow(`SELECT value FROM ` + k.table + ` WHERE key = 'beta'`).Scan(&stored); err != nil || stored != EncodeValue("2") {
			t.Errorf("%s stored value %q, %v; want base64 of 2", k.table, stored, err)
		}
		if keys, err = k.list(v.store, "user", "u"); err != nil || strings.Join(keys, ",") != "ALPHA,beta,Gamma" {
			t.Errorf("%s list: %v, %v; want sorted ignoring case", k.table, keys, err)
		}
		if keys, err = k.list(v.store, "user", "nobody"); err != nil || keys == nil || len(keys) != 0 {
			t.Errorf("%s list of an empty scope: %v, %v; want an empty slice", k.table, keys, err)
		}
	}
	vars, _ := v.store.ListVariables("user", "u")
	if vars[0].Value != "1" {
		t.Errorf("variable value %q, want decoded 1", vars[0].Value)
	}
}

// A key that exists in the scope, in any casing, is a ConflictError, and a
// batch with one such key writes nothing.
func TestCreate_RejectsExistingKeyIgnoringCase(t *testing.T) {
	v := newEnv(t)
	for _, k := range kinds {
		v.seed(t, k.table, "org", "o", "Key", "v")
		_, err := k.create(v.store, "org", "o", []EntryInput{{Key: "NEW", Value: "n"}, {Key: "KEY", Value: "v"}})
		var conflict *ConflictError
		if !errors.As(err, &conflict) || conflict.Key != "KEY" {
			t.Errorf("%s: %v, want ConflictError for KEY", k.table, err)
		}
		if n := v.count(t, k.table, "org", "o"); n != 1 {
			t.Errorf("%s: %d rows, want the seeded one only", k.table, n)
		}
		if _, err := k.create(v.store, "org", "o", nil); err == nil {
			t.Errorf("%s: empty entries accepted", k.table)
		}
	}
}

// A scope holds at most MaxEntriesPerScope entries; a batch that would
// exceed it is refused whole, and other scopes are not counted.
func TestCreate_EnforcesScopeLimit(t *testing.T) {
	v := newEnv(t)
	for _, k := range kinds {
		for i := 0; i < MaxEntriesPerScope-1; i++ {
			v.seed(t, k.table, "workspace", "w", fmt.Sprintf("K_%d", i), "v")
		}
		if _, err := k.create(v.store, "workspace", "w", []EntryInput{{Key: "A"}, {Key: "B"}}); err == nil || !strings.Contains(err.Error(), "maximum of 100") {
			t.Errorf("%s: two over the limit: %v", k.table, err)
		}
		if _, err := k.create(v.store, "workspace", "w", []EntryInput{{Key: "A"}}); err != nil {
			t.Errorf("%s: the hundredth entry: %v", k.table, err)
		}
		if _, err := k.create(v.store, "workspace", "w", []EntryInput{{Key: "B"}}); err == nil {
			t.Errorf("%s: the hundred and first entry accepted", k.table)
		}
		if _, err := k.create(v.store, "workspace", "other", []EntryInput{{Key: "B"}}); err != nil {
			t.Errorf("%s: another scope: %v", k.table, err)
		}
		if n := v.count(t, k.table, "workspace", "w"); n != MaxEntriesPerScope {
			t.Errorf("%s: %d rows, want %d", k.table, n, MaxEntriesPerScope)
		}
	}
}

// Update finds the key ignoring case, keeps the stored casing and created_at,
// and sets a new value and updated_at; an unknown key is a NotFoundError and
// an oversized value is refused.
func TestUpdate_FindsKeyIgnoringCase(t *testing.T) {
	v := newEnv(t)
	for _, k := range kinds {
		testutil.Exec(t, v.db, `INSERT INTO `+k.table+` VALUES ('user', 'u', 'Stored_Key', ?, '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z')`, EncodeValue("old"))
		key, err := k.update(v.store, "user", "u", "STORED_KEY", "new")
		if err != nil || key != "Stored_Key" {
			t.Fatalf("%s update: %q, %v; want the stored casing", k.table, key, err)
		}
		var value, created, updated string
		if err := v.db.QueryRow(`SELECT value, created_at, updated_at FROM `+k.table+` WHERE owner_id = 'u'`).Scan(&value, &created, &updated); err != nil {
			t.Fatal(err)
		}
		if value != EncodeValue("new") || created != "2020-01-01T00:00:00Z" || updated == created {
			t.Errorf("%s row after update: value %q created %s updated %s", k.table, value, created, updated)
		}
		var nf *NotFoundError
		if _, err := k.update(v.store, "user", "u", "OTHER", "x"); !errors.As(err, &nf) || nf.Key != "OTHER" {
			t.Errorf("%s update of an unknown key: %v, want NotFoundError", k.table, err)
		}
		if _, err := k.update(v.store, "user", "u", "STORED_KEY", strings.Repeat("x", MaxValueSize+1)); err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
			t.Errorf("%s update with an oversized value: %v", k.table, err)
		}
	}
}

// Delete removes the key ignoring case; deleting it again is a NotFoundError.
func TestDelete_FindsKeyIgnoringCase(t *testing.T) {
	v := newEnv(t)
	for _, k := range kinds {
		v.seed(t, k.table, "org", "o", "KEY", "v")
		if err := k.remove(v.store, "org", "o", "key"); err != nil {
			t.Fatalf("%s delete: %v", k.table, err)
		}
		var nf *NotFoundError
		if err := k.remove(v.store, "org", "o", "key"); !errors.As(err, &nf) || nf.Error() != `key "key" not found` {
			t.Errorf("%s second delete: %v, want NotFoundError", k.table, err)
		}
	}
}

// GetSecretValue and GetVariableValue return the plaintext of the key found
// ignoring case within the owner; an empty key, an unknown key (or owner type)
// and a stored value that is not base64 are errors.
func TestGetValue_ReturnsPlaintext(t *testing.T) {
	v := newEnv(t)
	for _, k := range kinds {
		v.seed(t, k.table, "workspace", "w", "Db_Url", "postgres://x\nsecond line")
		if got, err := k.value(v.store, "workspace", "w", "DB_URL"); err != nil || got != "postgres://x\nsecond line" {
			t.Errorf("%s value: %q, %v", k.table, got, err)
		}
		if _, err := k.value(v.store, "workspace", "w", ""); err == nil || !strings.Contains(err.Error(), "must not be empty") {
			t.Errorf("%s empty key: %v", k.table, err)
		}
		var nf *NotFoundError
		if _, err := k.value(v.store, "workspace", "w", "OTHER"); !errors.As(err, &nf) {
			t.Errorf("%s unknown key: %v, want NotFoundError", k.table, err)
		}
		if _, err := k.value(v.store, "user", "w", "DB_URL"); !errors.As(err, &nf) {
			t.Errorf("%s other owner type: %v, want NotFoundError", k.table, err)
		}
		testutil.Exec(t, v.db, `UPDATE `+k.table+` SET value = '%%%' WHERE key = 'Db_Url'`)
		if _, err := k.value(v.store, "workspace", "w", "DB_URL"); err == nil || !strings.Contains(err.Error(), "decode") {
			t.Errorf("%s malformed stored value: %v", k.table, err)
		}
		if _, err := k.list(v.store, "workspace", "w"); (err == nil) != (k.table == "secrets") {
			t.Errorf("%s list with a malformed value: %v", k.table, err)
		}
	}
}

// Resolution merges the three tiers by key ignoring case, the workspace over
// the org over the user, each entry naming its origin, sorted by key; an empty
// org id skips the org tier.
func TestResolveVariables_WorkspaceOverOrgOverUser(t *testing.T) {
	v := newEnv(t)
	for _, row := range [][4]string{
		{"user", "u", "shared", "from-user"}, {"user", "u", "USER_ONLY", "u"}, {"user", "u", "Pair", "from-user"},
		{"org", "o", "SHARED", "from-org"}, {"org", "o", "org_only", "o"}, {"org", "o", "PAIR", "from-org"},
		{"workspace", "w", "Shared", "from-ws"}, {"workspace", "w", "ws_only", "w"},
	} {
		v.seed(t, "variables", row[0], row[1], row[2], row[3])
	}
	got, err := v.store.ResolveVariables("u", "o", "w")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range got {
		lines = append(lines, e.Key+"="+e.Value+"@"+e.Origin)
	}
	want := "org_only=o@org PAIR=from-org@org Shared=from-ws@workspace USER_ONLY=u@user ws_only=w@workspace"
	if strings.Join(lines, " ") != want {
		t.Errorf("resolved %s\nwant     %s", strings.Join(lines, " "), want)
	}
	got, _ = v.store.ResolveVariables("u", "", "w")
	lines = lines[:0]
	for _, e := range got {
		lines = append(lines, e.Key+"@"+e.Origin)
	}
	if want := "Pair@user Shared@workspace USER_ONLY@user ws_only@workspace"; strings.Join(lines, " ") != want {
		t.Errorf("resolved without org %s\nwant %s", strings.Join(lines, " "), want)
	}
}
