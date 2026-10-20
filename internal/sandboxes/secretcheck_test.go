package sandboxes

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// newChainDB returns a database with the tables the resolution chain reads:
// workspaces and org_members.
func newChainDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openLeaseDB(t)
	createWorkspaceTables(t, db)
	return db
}

// insertWorkspace adds a workspace row; org may be "" for a NULL org_id.
func insertWorkspace(t *testing.T, db *sql.DB, slug, owner, org string) {
	t.Helper()
	var orgID any
	if org != "" {
		orgID = org
	}
	if _, err := db.Exec(`INSERT INTO workspaces (slug, owner_id, org_id) VALUES (?, ?, ?)`, slug, owner, orgID); err != nil {
		t.Fatalf("inserting workspace %s: %v", slug, err)
	}
}

// fakeLister is a SecretLister that holds key names per scope and records
// every call. It has no way to hand out a value.
type fakeLister struct {
	names map[Scope][]string
	err   error
	calls []Scope
}

func newFakeLister() *fakeLister { return &fakeLister{names: map[Scope][]string{}} }

func (f *fakeLister) add(scopeType, owner string, keys ...string) {
	s := Scope{scopeType, owner}
	f.names[s] = append(f.names[s], keys...)
}

func (f *fakeLister) ListSecrets(ownerType, ownerID string) ([]secrets.SecretEntry, error) {
	f.calls = append(f.calls, Scope{ownerType, ownerID})
	if f.err != nil {
		return nil, f.err
	}
	var out []secrets.SecretEntry
	for _, k := range f.names[Scope{ownerType, ownerID}] {
		out = append(out, secrets.SecretEntry{Key: k})
	}
	return out, nil
}

// TS-26-24: The presence check only lists key names, never reads a value, and
// ignores optional secrets.
// Requirements: 26-REQ-4.1, 26-REQ-4.5
func TestCheckRequiredSecrets_NamesOnly_TS_26_24(t *testing.T) {
	db := newChainDB(t)
	insertWorkspace(t, db, "widgets", "u1", "")
	lister := newFakeLister()
	lister.add("workspace", "widgets", "GIT_PAT")

	// The profile requires GIT_PAT; its optional secret EXTRA exists nowhere and
	// is never handed to the check.
	if err := CheckRequiredSecrets(lister, db, "widgets", []string{"GIT_PAT"}); err != nil {
		t.Fatalf("CheckRequiredSecrets returned error: %v", err)
	}
	if len(lister.calls) == 0 {
		t.Error("the check did not list any scope")
	}

	// The interface the check works through has one method, ListSecrets.
	iface := reflect.TypeOf((*SecretLister)(nil)).Elem()
	if iface.NumMethod() != 1 || iface.Method(0).Name != "ListSecrets" {
		t.Errorf("SecretLister has %d methods; want only ListSecrets", iface.NumMethod())
	}

	// No production file calls the store's value reader, except the one that
	// reads the values for a box when the sandbox gateway creates it (spec 27,
	// 27-REQ-5.1). The lease service, which issues leases, only lists names.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "secret_resolve.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), ".GetSecretValue(") {
			t.Errorf("%s calls GetSecretValue; the lease service may only list names", f)
		}
	}
}

// TS-26-25: Required secret names match stored keys case-insensitively.
// Requirement: 26-REQ-4.2
func TestCheckRequiredSecrets_CaseInsensitive_TS_26_25(t *testing.T) {
	db := newChainDB(t)
	insertWorkspace(t, db, "widgets", "u1", "")
	lister := newFakeLister()
	lister.add("workspace", "widgets", "git_pat")
	lister.add("user", "u1", "Other_Key")

	if err := CheckRequiredSecrets(lister, db, "widgets", []string{"GIT_PAT", "OTHER_KEY"}); err != nil {
		t.Errorf("CheckRequiredSecrets returned error: %v", err)
	}
}

// TS-26-26: The resolution chain helper returns the workspace, org and user
// scopes in order, with the workspace owner's user.
// Requirement: 26-REQ-4.3
func TestResolutionChain_TS_26_26(t *testing.T) {
	db := newChainDB(t)
	insertWorkspace(t, db, "a", "u1", "O")
	insertWorkspace(t, db, "b", "u2", "O")
	insertWorkspace(t, db, "c", "u3", "")
	if _, err := db.Exec(`INSERT INTO org_members (org_id, user_id) VALUES ('O', 'u1')`); err != nil {
		t.Fatal(err)
	}

	for slug, want := range map[string][]Scope{
		"a": {{"workspace", "a"}, {"org", "O"}, {"user", "u1"}},
		"b": {{"workspace", "b"}, {"user", "u2"}},
		"c": {{"workspace", "c"}, {"user", "u3"}},
	} {
		got, err := ResolutionChain(db, slug)
		if err != nil {
			t.Fatalf("ResolutionChain(%s) returned error: %v", slug, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ResolutionChain(%s) = %v; want %v", slug, got, want)
		}
	}

	// The chain takes the database and the slug, nothing about the caller, so it
	// cannot put the caller's user (an admin token has none) in the user scope.
	typ := reflect.TypeOf(ResolutionChain)
	if typ.NumIn() != 2 || typ.In(0) != reflect.TypeOf((*sql.DB)(nil)) || typ.In(1).Kind() != reflect.String {
		t.Errorf("ResolutionChain has parameters %v; want (*sql.DB, string)", typ)
	}

	if _, err := ResolutionChain(db, "nosuch"); err == nil {
		t.Error("ResolutionChain on an unknown workspace returned no error")
	}
	if _, err := db.Exec(`DROP TABLE org_members`); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolutionChain(db, "a"); err == nil {
		t.Error("ResolutionChain without the org_members table returned no error")
	}
	if got, err := ResolutionChain(db, "c"); err != nil || len(got) != 2 {
		t.Errorf("a workspace without an org still needs org_members: (%v, %v)", got, err)
	}
}

// The check reports every missing name in profile order and the scope types it
// searched, and names neither the owner nor the org.
// Requirement: 26-REQ-4.4
func TestCheckRequiredSecrets_Missing(t *testing.T) {
	db := newChainDB(t)
	insertWorkspace(t, db, "widgets", "owner-u1", "")
	insertWorkspace(t, db, "teamed", "owner-u2", "org-77")
	if _, err := db.Exec(`INSERT INTO org_members (org_id, user_id) VALUES ('org-77', 'owner-u2')`); err != nil {
		t.Fatal(err)
	}
	lister := newFakeLister()
	lister.add("user", "owner-u1", "B_KEY")
	lister.add("org", "org-77", "C_KEY")

	err := CheckRequiredSecrets(lister, db, "widgets", []string{"C_KEY", "A_KEY", "B_KEY", "D_KEY"})
	var missing *MissingSecretsError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v (%T); want a *MissingSecretsError", err, err)
	}
	if !reflect.DeepEqual(missing.Missing, []string{"C_KEY", "A_KEY", "D_KEY"}) {
		t.Errorf("missing = %v; want [C_KEY A_KEY D_KEY] in profile order", missing.Missing)
	}
	if !reflect.DeepEqual(missing.Searched, []string{"workspace", "user"}) {
		t.Errorf("searched = %v; want [workspace user]", missing.Searched)
	}
	for _, leak := range []string{"owner-u1", "widgets", "org-77"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("message %q names %q", err, leak)
		}
	}
	for _, want := range []string{"C_KEY", "A_KEY", "D_KEY", "workspace, user"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "B_KEY") {
		t.Errorf("message %q names a secret that exists", err)
	}

	// With an org the owner belongs to, the org scope is searched and says so.
	err = CheckRequiredSecrets(lister, db, "teamed", []string{"C_KEY", "Z_KEY"})
	if !errors.As(err, &missing) || !reflect.DeepEqual(missing.Missing, []string{"Z_KEY"}) ||
		!reflect.DeepEqual(missing.Searched, []string{"workspace", "org", "user"}) {
		t.Errorf("teamed workspace: error = %v; want Z_KEY missing after searching workspace, org, user", err)
	}
	if strings.Contains(err.Error(), "org-77") || strings.Contains(err.Error(), "owner-u2") {
		t.Errorf("message %q names the org or the owner", err)
	}
}

// A lister or lookup failure is an error of its own, not a missing secret, and
// nothing required means nothing to look up.
// Requirement: 26-REQ-4.6
func TestCheckRequiredSecrets_Failures(t *testing.T) {
	db := newChainDB(t)
	insertWorkspace(t, db, "widgets", "u1", "")
	lister := newFakeLister()
	lister.err = errors.New("boom")

	err := CheckRequiredSecrets(lister, db, "widgets", []string{"A_KEY"})
	var missing *MissingSecretsError
	if err == nil || errors.As(err, &missing) || !errors.Is(err, lister.err) {
		t.Errorf("a lister error produced %v; want it wrapped and not a missing secret", err)
	}

	lister = newFakeLister()
	if err := CheckRequiredSecrets(lister, db, "widgets", nil); err != nil || len(lister.calls) != 0 {
		t.Errorf("no required secrets: error %v, %d calls; want nil and no lookups", err, len(lister.calls))
	}
	if err := CheckRequiredSecrets(lister, db, "nosuch", []string{"A_KEY"}); err == nil || errors.As(err, &missing) {
		t.Errorf("an unknown workspace produced %v; want a lookup error", err)
	}
}
