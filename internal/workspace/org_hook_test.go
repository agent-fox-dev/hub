package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// TestSanitizeSlug: a username becomes a slug apikit accepts: lowercase,
// other characters to single hyphens, trimmed, prefixed when it starts with a
// digit, and replaced by u-<user id prefix> when too short.
func TestSanitizeSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Alice": "alice", "al ice!": "al-ice", "a---b": "a-b", "--alice__": "alice", "7up": "u-7up", "a_b": "a_b",
		"7": "u-abcdefgh", "!!": "u-abcdefgh", "-a-": "u-abcdefgh", "": "u-abcdefgh", "Ünïcödé Name": "n-c-d-name",
	} {
		if got := sanitizeSlug(in, "abcdefghijkl"); got != want {
			t.Errorf("sanitizeSlug(%q) = %q; want %q", in, got, want)
		}
	}
	if got := sanitizeSlug("", "short"); got != "u-short" {
		t.Errorf("short user id: %q", got)
	}
}

// orgDB is a database with apikit's schema and user-1; begin opens the
// transaction the hook runs on.
func orgDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.OpenDB(t)
	testutil.SeedUser(t, db, "user-1")
	return db
}

func begin(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

// seedOrgSlug inserts an org holding slug.
func seedOrgSlug(t *testing.T, db *sql.DB, slug string) {
	t.Helper()
	testutil.SeedOrg(t, db, "id-"+slug, slug, "")
}

// TestCreatePersonalOrg: the hook inserts, on the caller's transaction, an
// active org named after the user with the sanitized slug and owned by the
// user, and the user's membership; a taken slug gets the first free numeric
// suffix.
func TestCreatePersonalOrg(t *testing.T) {
	db := orgDB(t)
	seedOrgSlug(t, db, "alice")
	seedOrgSlug(t, db, "alice-1")
	tx := begin(t, db)
	if err := CreatePersonalOrg(context.Background(), tx, "user-1", "Alice", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	// Nothing is visible outside the transaction until it commits.
	if testutil.Count(t, db, `SELECT COUNT(*) FROM orgs WHERE owner_id = 'user-1'`) != 0 {
		t.Error("the org is visible before the commit")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var id, name, slug, url, status, created, updated string
	if err := db.QueryRow(`SELECT id, name, slug, url, owner_id, status, created_at, updated_at FROM orgs WHERE owner_id = 'user-1'`).Scan(&id, &name, &slug, &url, new(string), &status, &created, &updated); err != nil {
		t.Fatal(err)
	}
	if len(id) != 36 || name != "Alice" || slug != "alice-2" || url != "" || status != "active" || created == "" || created != updated {
		t.Errorf("org %s %s %s %q %s %s %s", id, name, slug, url, status, created, updated)
	}
	if testutil.Count(t, db, `SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = 'user-1'`, id) != 1 {
		t.Error("the membership is missing")
	}
}

// TestCreatePersonalOrg_Failures: ten taken suffixes, a failing slug check, a
// failing UUID generator and a failing insert each fail the hook, which
// inserts nothing then.
func TestCreatePersonalOrg_Failures(t *testing.T) {
	db := orgDB(t)
	seedOrgSlug(t, db, "bob")
	for i := 1; i <= maxSlugCollisionAttempts; i++ {
		seedOrgSlug(t, db, fmt.Sprintf("bob-%d", i))
	}
	tx := begin(t, db)
	calls := 0
	swap(t, &slugCheckFn, func(ctx context.Context, tx txExecutor, slug string) (bool, error) {
		calls++
		var n int
		err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM orgs WHERE slug = ?", slug).Scan(&n)
		return n > 0, err
	})
	if err := CreatePersonalOrg(context.Background(), tx, "user-1", "bob", ""); err == nil || !strings.Contains(err.Error(), "could not find unique slug") || calls != maxSlugCollisionAttempts {
		t.Errorf("exhausted suffixes: %v after %d checks", err, calls)
	}

	swap(t, &slugCheckFn, func(context.Context, txExecutor, string) (bool, error) { return false, errors.New("db down") })
	if err := CreatePersonalOrg(context.Background(), tx, "user-1", "carol", ""); err == nil || !strings.Contains(err.Error(), "checking slug uniqueness: db down") {
		t.Errorf("failing check: %v", err)
	}
	swap(t, &slugCheckFn, func(context.Context, txExecutor, string) (bool, error) { return false, nil })
	swap(t, &uuidGenFn, func() (string, error) { return "", errors.New("entropy") })
	if err := CreatePersonalOrg(context.Background(), tx, "user-1", "carol", ""); err == nil || !strings.Contains(err.Error(), "generating org UUID: entropy") {
		t.Errorf("failing UUID: %v", err)
	}
	swap(t, &uuidGenFn, func() (string, error) { return "id-bob", nil }) // taken id
	if err := CreatePersonalOrg(context.Background(), tx, "user-1", "carol", ""); err == nil || !strings.Contains(err.Error(), "inserting org") {
		t.Errorf("failing org insert: %v", err)
	}
	swap(t, &uuidGenFn, func() (string, error) { return "fresh", nil })
	if err := CreatePersonalOrg(context.Background(), tx, "ghost", "dave", ""); err == nil || !strings.Contains(err.Error(), "inserting org_members") {
		t.Errorf("failing membership insert: %v", err)
	}
	// The caller rolls back; nothing of the failed hooks remains.
	tx.Rollback()
	if testutil.Count(t, db, `SELECT COUNT(*) FROM orgs WHERE owner_id IN ('user-1', 'ghost')`) != 0 {
		t.Error("a failed hook left an org")
	}
	tx2 := begin(t, db)
	tx2.Rollback()
	if err := CreatePersonalOrg(context.Background(), tx2, "user-1", "frank", ""); err == nil || !strings.Contains(err.Error(), "checking base slug uniqueness") {
		t.Errorf("finished transaction: %v", err)
	}
}
