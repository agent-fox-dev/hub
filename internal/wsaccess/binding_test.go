package wsaccess

import (
	"context"
	"reflect"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// InitBindingSchema can run again and keeps the rows; a token has one binding.
func TestInitBindingSchema_IdempotentWithOneBindingPerToken(t *testing.T) {
	db := newDB(t)
	testutil.BindToken(t, db, "tok", "w")
	if err := InitBindingSchema(db); err != nil {
		t.Fatalf("second InitBindingSchema: %v", err)
	}
	if got := bindings(t, db); !reflect.DeepEqual(got, []string{"tok=w"}) {
		t.Errorf("bindings %v, want the one kept", got)
	}
	if _, err := db.Exec(`INSERT INTO token_workspace_bindings VALUES ('tok', 'other', '')`); err == nil {
		t.Error("a second binding of the same token was accepted")
	}
}

// LookupBinding finds the slug of a bound token and reports an unbound one
// with no error; BoundSlug resolves the credential, and never queries for
// one that cannot be bound.
func TestLookupBinding_AndBoundSlug(t *testing.T) {
	db := newDB(t)
	testutil.BindToken(t, db, "tok", "w")
	if slug, bound, err := LookupBinding(db, "tok"); slug != "w" || !bound || err != nil {
		t.Errorf("LookupBinding(tok) = %q, %v, %v", slug, bound, err)
	}
	if slug, bound, err := LookupBinding(db, "none"); slug != "" || bound || err != nil {
		t.Errorf("LookupBinding(none) = %q, %v, %v", slug, bound, err)
	}
	if slug, bound, err := BoundSlug(db, testutil.BoundPAT("u", "tok")); slug != "w" || !bound || err != nil {
		t.Errorf("BoundSlug(bound PAT) = %q, %v, %v", slug, bound, err)
	}
	testutil.Exec(t, db, `DROP TABLE token_workspace_bindings`)
	for _, auth := range []*apikit.AuthInfo{nil, testutil.Admin(), testutil.User("u"), testutil.PAT("u", "workspaces:read")} {
		if slug, bound, err := BoundSlug(db, auth); slug != "" || bound || err != nil {
			t.Errorf("BoundSlug(%+v) = %q, %v, %v; want unbound without a query", auth, slug, bound, err)
		}
	}
	if _, bound, err := BoundSlug(db, testutil.BoundPAT("u", "tok")); err == nil {
		t.Errorf("BoundSlug of a PAT without the table: bound %v, err %v; want the error", bound, err)
	}
}

// InsertBinding writes the row only while the workspace exists, and refuses a
// second binding of the token.
func TestInsertBinding_RequiresTheWorkspace(t *testing.T) {
	db := newDB(t)
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "w", OwnerID: "u"})
	if written, err := InsertBinding(db, "tok", "w", "2024-01-01T00:00:00Z"); !written || err != nil {
		t.Errorf("InsertBinding(w) = %v, %v", written, err)
	}
	if written, err := InsertBinding(db, "tok2", "gone", "2024-01-01T00:00:00Z"); written || err != nil {
		t.Errorf("InsertBinding(gone) = %v, %v; want not written, no error", written, err)
	}
	if _, err := InsertBinding(db, "tok", "w", "2024-01-01T00:00:00Z"); err == nil {
		t.Error("a second binding of tok was accepted")
	}
	if got := bindings(t, db); !reflect.DeepEqual(got, []string{"tok=w"}) {
		t.Errorf("bindings %v", got)
	}
}

// ListBoundTokens joins the bindings of a slug with their pats rows, oldest
// first, revoked tokens included and bindings without a pats row left out.
func TestListBoundTokens(t *testing.T) {
	db := newDB(t)
	testutil.SeedUser(t, db, "u")
	seedPAT(t, db, "live", "u", `["workspaces:read"]`, "")
	seedPAT(t, db, "revoked", "u", `[]`, "2024-01-01T00:00:00Z")
	testutil.Exec(t, db, `UPDATE pats SET created_at = '2023-01-01T00:00:00Z' WHERE token_id = 'revoked'`)
	for _, id := range []string{"live", "revoked", "orphan"} {
		testutil.BindToken(t, db, id, "w")
	}
	testutil.BindToken(t, db, "live-elsewhere", "other")
	got, err := ListBoundTokens(db, "w")
	if err != nil || len(got) != 2 || got[0].TokenID != "revoked" || got[1].TokenID != "live" {
		t.Fatalf("ListBoundTokens(w) = %+v, %v; want revoked then live", got, err)
	}
	if got[1].Name != "pat-live" || got[1].Permissions != `["workspaces:read"]` || got[1].RevokedAt.Valid || !got[0].RevokedAt.Valid {
		t.Errorf("tokens %+v", got)
	}
	if empty, err := ListBoundTokens(db, "none"); err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("ListBoundTokens(none) = %v, %v; want an empty slice", empty, err)
	}
}

// RevokeToken revokes a live token the way apikit does and leaves a revoked
// one's time alone.
func TestRevokeToken(t *testing.T) {
	db := newDB(t)
	testutil.SeedUser(t, db, "u")
	seedPAT(t, db, "live", "u", `[]`, "")
	seedPAT(t, db, "old", "u", `[]`, "2024-01-01T00:00:00Z")
	for _, id := range []string{"live", "old"} {
		if err := RevokeToken(db, id); err != nil {
			t.Fatal(err)
		}
	}
	if revokedAt(t, db, "live") == "" || revokedAt(t, db, "old") != "2024-01-01T00:00:00Z" {
		t.Errorf("revoked_at live %q old %q", revokedAt(t, db, "live"), revokedAt(t, db, "old"))
	}
}

// RevokeAndUnbindSlug revokes the live tokens bound to the slug, deletes all
// its bindings and returns the revoked ids sorted; a slug without bindings
// touches no pats row.
func TestRevokeAndUnbindSlug(t *testing.T) {
	db := newDB(t)
	testutil.SeedUser(t, db, "u")
	seedPAT(t, db, "b", "u", `[]`, "")
	seedPAT(t, db, "a", "u", `[]`, "")
	seedPAT(t, db, "gone", "u", `[]`, "2024-01-01T00:00:00Z")
	seedPAT(t, db, "other", "u", `[]`, "")
	for _, id := range []string{"b", "a", "gone", "no-pats-row"} {
		testutil.BindToken(t, db, id, "w")
	}
	testutil.BindToken(t, db, "other", "other-ws")
	tx, _ := db.Begin()
	revoked, err := RevokeAndUnbindSlug(tx, "w")
	if err != nil || !reflect.DeepEqual(revoked, []string{"a", "b"}) {
		t.Fatalf("RevokeAndUnbindSlug(w) = %v, %v; want [a b]", revoked, err)
	}
	if none, err := RevokeAndUnbindSlug(tx, "unbound"); err != nil || none == nil || len(none) != 0 {
		t.Errorf("RevokeAndUnbindSlug(unbound) = %v, %v; want an empty slice", none, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := bindings(t, db); !reflect.DeepEqual(got, []string{"other=other-ws"}) {
		t.Errorf("bindings %v, want the other workspace's only", got)
	}
	if revokedAt(t, db, "a") == "" || revokedAt(t, db, "other") != "" || revokedAt(t, db, "gone") != "2024-01-01T00:00:00Z" {
		t.Error("the wrong tokens were revoked")
	}
	testutil.Exec(t, db, `DROP TABLE pats`)
	tx, _ = db.Begin()
	defer tx.Rollback()
	if _, err := RevokeAndUnbindSlug(tx, "unbound"); err != nil {
		t.Errorf("a slug without bindings needs the pats table: %v", err)
	}
}

// The sweep retires the bindings of workspaces that are gone (revoking their
// live tokens) and drops the bindings of tokens that are gone, keeping the
// bindings of revoked tokens whose workspace exists; a cancelled context ends
// it without a change.
func TestSweepBindings(t *testing.T) {
	db := newDB(t)
	testutil.SeedUser(t, db, "u")
	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "w", OwnerID: "u"})
	seedPAT(t, db, "kept", "u", `[]`, "")
	seedPAT(t, db, "kept-revoked", "u", `[]`, "2024-01-01T00:00:00Z")
	seedPAT(t, db, "orphan-live", "u", `[]`, "")
	seedPAT(t, db, "orphan-revoked", "u", `[]`, "2024-01-01T00:00:00Z")
	for id, slug := range map[string]string{"kept": "w", "kept-revoked": "w", "no-pats": "w", "orphan-live": "gone", "orphan-revoked": "gone"} {
		testutil.BindToken(t, db, id, slug)
	}
	before := bindings(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SweepBindings(ctx, db); err == nil {
		t.Error("a cancelled sweep returned nil")
	}
	if got := bindings(t, db); !reflect.DeepEqual(got, before) {
		t.Errorf("a cancelled sweep changed the bindings: %v", got)
	}
	if err := SweepBindings(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if got := bindings(t, db); !reflect.DeepEqual(got, []string{"kept=w", "kept-revoked=w"}) {
		t.Errorf("bindings after the sweep %v", got)
	}
	if revokedAt(t, db, "orphan-live") == "" || revokedAt(t, db, "kept") != "" || revokedAt(t, db, "orphan-revoked") != "2024-01-01T00:00:00Z" {
		t.Error("the sweep revoked the wrong tokens")
	}
}
