package wsaccess_test

import (
	"database/sql"
	"testing"

	"github.com/txsvc/apikit"
	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// openBindingDB opens an in-memory SQLite database restricted to one
// connection (as the hub's test databases are) with a minimal workspaces
// table and the binding schema.
func openBindingDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE workspaces (
		slug TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL,
		org_id TEXT,
		status TEXT NOT NULL DEFAULT 'active',
		clone_status TEXT NOT NULL DEFAULT 'ready'
	)`); err != nil {
		t.Fatalf("creating workspaces: %v", err)
	}
	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatalf("InitBindingSchema: %v", err)
	}
	return db
}

// bind inserts a binding row for tokenID naming slug.
func bind(t *testing.T, db *sql.DB, tokenID, slug string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO token_workspace_bindings (token_id, slug, created_at) VALUES (?, ?, ?)`,
		tokenID, slug, apikit.NowUTC()); err != nil {
		t.Fatalf("binding %s to %s: %v", tokenID, slug, err)
	}
}

type bindingRow struct{ tokenID, slug, createdAt string }

func bindingRows(t *testing.T, db *sql.DB) []bindingRow {
	t.Helper()
	rows, err := db.Query(`SELECT token_id, slug, created_at FROM token_workspace_bindings ORDER BY token_id`)
	if err != nil {
		t.Fatalf("reading bindings: %v", err)
	}
	defer rows.Close()
	var out []bindingRow
	for rows.Next() {
		var r bindingRow
		if err := rows.Scan(&r.tokenID, &r.slug, &r.createdAt); err != nil {
			t.Fatalf("scanning binding: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// TS-33-1: The binding schema creates token_workspace_bindings with the
// specified columns, index and no foreign key. Requirement: 33-REQ-1.1
func TestInitBindingSchema_Shape_TS_33_1(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatalf("InitBindingSchema = %v; want nil", err)
	}

	type column struct {
		name, typ string
		notNull   bool
		pk        int
	}
	rows, err := db.Query(`SELECT name, type, "notnull", pk FROM pragma_table_info('token_workspace_bindings') ORDER BY cid`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	var cols []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.typ, &c.notNull, &c.pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	rows.Close()
	want := []column{
		{"token_id", "TEXT", false, 1},
		{"slug", "TEXT", true, 0},
		{"created_at", "TEXT", true, 0},
	}
	if len(cols) != len(want) {
		t.Fatalf("columns = %+v; want %+v", cols, want)
	}
	for i := range want {
		if cols[i] != want[i] {
			t.Errorf("column %d = %+v; want %+v", i, cols[i], want[i])
		}
	}

	var indexed string
	if err := db.QueryRow(`SELECT name FROM pragma_index_info('idx_token_workspace_bindings_slug')`).Scan(&indexed); err != nil {
		t.Fatalf("index idx_token_workspace_bindings_slug: %v", err)
	}
	if indexed != "slug" {
		t.Errorf("index column = %q; want slug", indexed)
	}

	var fks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('token_workspace_bindings')`).Scan(&fks); err != nil {
		t.Fatal(err)
	}
	if fks != 0 {
		t.Errorf("foreign keys = %d; want 0", fks)
	}

	now := apikit.NowUTC()
	if _, err := db.Exec(`INSERT INTO token_workspace_bindings (token_id, slug, created_at) VALUES ('t1', 's1', ?)`, now); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var got string
	if err := db.QueryRow(`SELECT created_at FROM token_workspace_bindings WHERE token_id = 't1'`).Scan(&got); err != nil || got != now {
		t.Errorf("created_at = %q (%v); want %q", got, err, now)
	}
}

// TS-33-2: Re-running the binding schema initialisation leaves existing rows
// unchanged. Requirement: 33-REQ-1.2
func TestInitBindingSchema_Idempotent_TS_33_2(t *testing.T) {
	db := openBindingDB(t)
	bind(t, db, "t1", "a")
	bind(t, db, "t2", "b")
	before := bindingRows(t, db)

	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatalf("second InitBindingSchema = %v; want nil", err)
	}
	after := bindingRows(t, db)
	if len(before) != 2 || len(after) != 2 {
		t.Fatalf("rows before %d, after %d; want 2 and 2", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("row %d = %+v after; want %+v", i, after[i], before[i])
		}
	}
}

// TS-33-7: A PAT whose token id has no binding row is unbound.
// Requirement: 33-REQ-1.7
func TestLookupBinding_NoRowIsUnbound_TS_33_7(t *testing.T) {
	db := openBindingDB(t)
	bind(t, db, "tokB", "S")

	slug, bound, err := wsaccess.LookupBinding(db, "tokX")
	if slug != "" || bound || err != nil {
		t.Errorf("LookupBinding(tokX) = (%q, %v, %v); want (\"\", false, nil)", slug, bound, err)
	}
	pat := &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokX"}
	if slug, bound, err := wsaccess.BoundSlug(db, pat); bound || err != nil || slug != "" {
		t.Errorf("BoundSlug(tokX) = (%q, %v, %v); want (\"\", false, nil)", slug, bound, err)
	}
	if slug, bound, err := wsaccess.LookupBinding(db, "tokB"); slug != "S" || !bound || err != nil {
		t.Errorf("LookupBinding(tokB) = (%q, %v, %v); want (S, true, nil)", slug, bound, err)
	}
}

// TS-33-8: The binding resolver issues no query for api_key, admin_token, nil
// auth or a PAT without a token id. Requirement: 33-REQ-2.1
func TestBoundSlug_NoQueryForUnbindableCredentials_TS_33_8(t *testing.T) {
	db := openBindingDB(t)
	bind(t, db, "tokB", "S")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cases := map[string]*apikit.AuthInfo{
		"api_key":         {CredentialType: "api_key", UserID: "u1", KeyID: "tokB"},
		"admin_token":     {CredentialType: "admin_token", Role: "admin"},
		"nil":             nil,
		"pat no token id": {CredentialType: "pat", UserID: "u1"},
	}
	for name, auth := range cases {
		slug, bound, err := wsaccess.BoundSlug(db, auth)
		if slug != "" || bound || err != nil {
			t.Errorf("%s: BoundSlug on a closed database = (%q, %v, %v); want (\"\", false, nil), so no query", name, slug, bound, err)
		}
	}

	// A PAT with a token id is looked up by exactly that id.
	live := openBindingDB(t)
	bind(t, live, "tokB", "S")
	slug, bound, err := wsaccess.BoundSlug(live, &apikit.AuthInfo{CredentialType: "pat", UserID: "u1", TokenID: "tokB"})
	if slug != "S" || !bound || err != nil {
		t.Errorf("BoundSlug(pat tokB) = (%q, %v, %v); want (S, true, nil)", slug, bound, err)
	}
	// On the closed database a PAT with a token id does query, and fails.
	if _, _, err := wsaccess.BoundSlug(db, &apikit.AuthInfo{CredentialType: "pat", TokenID: "tokB"}); err == nil {
		t.Error("BoundSlug(pat tokB) on a closed database = nil error; want the query's error")
	}
}
