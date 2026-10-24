package carrypatch

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// Soft-deleting a patch in the middle of the stack and compacting must not
// collide on UNIQUE(workspace_slug, position): the deleted row is parked at
// a negative position and the survivors are renumbered 1..n.
func TestSQLPatchStore_SoftDeleteThenCompact_NoPositionCollision(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE patches (
		id TEXT PRIMARY KEY, workspace_slug TEXT NOT NULL, branch_name TEXT NOT NULL,
		position INTEGER NOT NULL, status TEXT NOT NULL DEFAULT 'active', conflict_files TEXT,
		upstream_pr_url TEXT, description TEXT, deleted_at TEXT, added_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		UNIQUE(workspace_slug, branch_name), UNIQUE(workspace_slug, position))`); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"a", "b", "c"} {
		if _, err := db.Exec(`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at) VALUES (?, 'ws', ?, ?, 'active', 'x', 'x')`, id, "br-"+id, i+1); err != nil {
			t.Fatal(err)
		}
	}
	store := NewSQLPatchStore(db)
	if err := store.SoftDeletePatch(context.Background(), "b"); err != nil {
		t.Fatalf("SoftDeletePatch: %v", err)
	}
	if err := store.CompactPositions(context.Background(), "ws"); err != nil {
		t.Fatalf("CompactPositions after soft-delete: %v", err)
	}
	want := map[string]int{"a": 1, "c": 2}
	for id, pos := range want {
		var got int
		if err := db.QueryRow(`SELECT position FROM patches WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != pos {
			t.Errorf("patch %s position = %d; want %d", id, got, pos)
		}
	}
	var deletedPos int
	if err := db.QueryRow(`SELECT position FROM patches WHERE id = 'b'`).Scan(&deletedPos); err != nil {
		t.Fatal(err)
	}
	if deletedPos >= 0 {
		t.Errorf("soft-deleted patch position = %d; want negative", deletedPos)
	}
	// Restoring appends after the survivors.
	if err := store.RestorePatch(context.Background(), "b"); err != nil {
		t.Fatalf("RestorePatch: %v", err)
	}
}
