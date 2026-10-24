package carrypatch

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"
)

// three seeds a workspace with patches feature/a, feature/b, feature/c at
// positions 1..3 and returns the database and the store over it.
func three(t *testing.T) (*sql.DB, *SQLPatchStore) {
	t.Helper()
	db := openDB(t)
	seedWS(t, db, ws{slug: "ws"})
	for i, b := range []string{"a", "b", "c"} {
		seedPatch(t, db, "ws", "p"+b, "feature/"+b, i+1, "active")
	}
	return db, NewSQLPatchStore(db)
}

func TestSQLPatchStore_ListPatchesExcludesDeletedAndParsesConflictFiles(t *testing.T) {
	db, store := three(t)
	ctx := context.Background()
	if err := store.UpdatePatchStatus(ctx, "pb", PatchStatusConflict, []string{"x.go", "y.go"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SoftDeletePatch(ctx, "pc"); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE patches SET upstream_pr_url = 'https://github.com/o/r/pull/7' WHERE id = 'pa'`)

	patches, err := store.ListPatches(ctx, "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 2 || patches[0].ID != "pa" || patches[1].ID != "pb" {
		t.Fatalf("patches = %+v", patches)
	}
	if patches[0].UpstreamPRURL == nil || *patches[0].UpstreamPRURL != "https://github.com/o/r/pull/7" {
		t.Error("upstream_pr_url not read")
	}
	if patches[1].Status != PatchStatusConflict || !equalStrings(patches[1].ConflictFiles, []string{"x.go", "y.go"}) {
		t.Errorf("conflict patch = %+v", patches[1])
	}
	if err := store.UpdatePatchStatus(ctx, "pb", PatchStatusActive, nil); err != nil {
		t.Fatal(err)
	}
	var files string
	if err := db.QueryRow(`SELECT conflict_files FROM patches WHERE id = 'pb'`).Scan(&files); err != nil || files != "[]" {
		t.Errorf("conflict_files after clearing = %q, %v", files, err)
	}
	if err := store.DeletePatch(ctx, "pa"); err != nil {
		t.Fatal(err)
	}
	if got := patchStatuses(t, db, "ws"); len(got) != 2 || got["feature/c"] != "deleted" {
		t.Errorf("rows after delete = %v", got)
	}
}

// A soft-deleted row gives up its slot (parked at a negative position) so
// compaction never collides with it; restore appends it after the visible
// patches; restoring a visible patch changes nothing.
func TestSQLPatchStore_SoftDeleteCompactRestore(t *testing.T) {
	db, store := three(t)
	ctx := context.Background()
	if err := store.SoftDeletePatch(ctx, "pb"); err != nil {
		t.Fatal(err)
	}
	var pos int
	var status string
	var deletedAt sql.NullString
	if err := db.QueryRow(`SELECT position, status, deleted_at FROM patches WHERE id = 'pb'`).Scan(&pos, &status, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if pos >= 0 || status != "deleted" || !deletedAt.Valid {
		t.Errorf("soft-deleted row: pos=%d status=%s deleted_at=%v", pos, status, deletedAt)
	}
	if err := store.CompactPositions(ctx, "ws"); err != nil {
		t.Fatal(err)
	}
	if got := positions(t, db, "ws"); !equalStrings(got, []string{"feature/a", "feature/c"}) {
		t.Errorf("after compaction = %v", got)
	}
	if err := store.RestorePatch(ctx, "pb"); err != nil {
		t.Fatal(err)
	}
	if got := positions(t, db, "ws"); !equalStrings(got, []string{"feature/a", "feature/c", "feature/b"}) {
		t.Errorf("after restore = %v", got)
	}
	if err := db.QueryRow(`SELECT deleted_at FROM patches WHERE id = 'pb'`).Scan(&deletedAt); err != nil || deletedAt.Valid {
		t.Errorf("deleted_at after restore = %v, %v", deletedAt, err)
	}
	if err := store.RestorePatch(ctx, "pa"); err != nil {
		t.Fatal(err)
	}
	if got := positions(t, db, "ws"); !equalStrings(got, []string{"feature/a", "feature/c", "feature/b"}) {
		t.Errorf("restore of a visible patch changed positions: %v", got)
	}
}

// The purge removes only rows soft-deleted before the cutoff; the 7-day
// routine, the expired listing and the by-id delete agree on that set.
func TestSQLPatchStore_PurgeAndExpiredRows(t *testing.T) {
	db, store := three(t)
	ctx := context.Background()
	old := apikit.FormatUTC(time.Now().Add(-8 * 24 * time.Hour))
	mustExec(t, db, `UPDATE patches SET status = 'deleted', deleted_at = ?, position = -rowid WHERE id = 'pa'`, old)
	mustExec(t, db, `UPDATE patches SET status = 'deleted', deleted_at = ?, position = -rowid WHERE id = 'pb'`, apikit.NowUTC())

	expired, err := store.ListExpiredDeletedPatches(ctx, apikit.FormatUTC(time.Now().Add(-7*24*time.Hour)))
	if err != nil || len(expired) != 1 || expired[0] != (ExpiredDeletedPatch{ID: "pa", Slug: "ws", Branch: "feature/a"}) {
		t.Fatalf("expired = %+v, %v", expired, err)
	}
	if deleted, err := store.DeletePatchByIDIfDeleted(ctx, "pc"); err != nil || deleted {
		t.Errorf("delete of an active row = %v, %v", deleted, err)
	}
	if deleted, err := store.DeletePatchByIDIfDeleted(ctx, "nope"); err != nil || deleted {
		t.Errorf("delete of a missing row = %v, %v", deleted, err)
	}
	n, err := PurgeExpiredDeletedPatches(ctx, store)
	if err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	if got := patchStatuses(t, db, "ws"); len(got) != 2 || got["feature/b"] != "deleted" {
		t.Errorf("rows after purge = %v", got)
	}
	if deleted, err := store.DeletePatchByIDIfDeleted(ctx, "pb"); err != nil || !deleted {
		t.Errorf("delete of a soft-deleted row = %v, %v", deleted, err)
	}
}

func TestSQLPatchStore_OriginSyncState(t *testing.T) {
	db, store := three(t)
	ctx := context.Background()
	sha := "abc123"
	if err := store.SetOriginSyncState(ctx, "pa", StateInSync, &sha, "2024-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetOriginSyncState(ctx, "pb", StateMissingOnOrigin, nil, "2024-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetOriginSyncState(ctx, "pc", StateDiverged, &sha, "2024-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	state := func(id string) (string, bool) {
		var st, originSHA sql.NullString
		if err := db.QueryRow(`SELECT origin_sync_state, origin_sha FROM patches WHERE id = ?`, id).Scan(&st, &originSHA); err != nil {
			t.Fatal(err)
		}
		return st.String, originSHA.Valid
	}
	if st, hasSHA := state("pa"); st != StateInSync || !hasSHA {
		t.Errorf("pa = %s/%v", st, hasSHA)
	}
	if st, hasSHA := state("pb"); st != StateMissingOnOrigin || hasSHA {
		t.Errorf("pb = %s/%v (origin_sha must be NULL when missing on origin)", st, hasSHA)
	}
	patches, _ := store.ListPatches(ctx, "ws")
	if patches[0].OriginSyncState == nil || *patches[0].OriginSyncState != StateInSync || *patches[0].OriginSHA != sha || patches[0].OriginSyncedAt == nil {
		t.Errorf("ListPatches origin fields = %+v", patches[0])
	}

	mustExec(t, db, `UPDATE patches SET status = 'merged_upstream' WHERE id = 'pa'`)
	if err := store.ClearOriginSyncStateForMergedDeleted(ctx, "ws"); err != nil {
		t.Fatal(err)
	}
	if st, _ := state("pa"); st != "" {
		t.Error("merged_upstream row kept its origin state")
	}
	if st, _ := state("pc"); st != StateDiverged {
		t.Error("active row lost its origin state")
	}
	if err := store.ClearOriginSyncStateForPatch(ctx, "pc"); err != nil {
		t.Fatal(err)
	}
	if st, _ := state("pc"); st != "" {
		t.Error("ClearOriginSyncStateForPatch left state")
	}
	if err := store.ClearOriginSyncState(ctx, "ws"); err != nil {
		t.Fatal(err)
	}
	if st, _ := state("pb"); st != "" {
		t.Error("ClearOriginSyncState left state")
	}
}

// Inserting with a position shifts the patches at and after it; no position
// or one past the end appends.
func TestPatchStore_AddPatchWithPositionShiftsOrAppends(t *testing.T) {
	db, _ := three(t)
	add := func(branch string, pos int) *patchRow {
		p, err := addPatchWithPosition(db, &patchRow{WorkspaceSlug: "ws", BranchName: branch, Position: pos})
		if err != nil {
			t.Fatalf("add %s: %v", branch, err)
		}
		return p
	}
	// Inserting before the last patch shifts it down. (Shifting two or more
	// rows at once trips UNIQUE(workspace_slug, position) in SQLite, so an
	// insert further up fails in production; that shape is not asserted.)
	if p := add("feature/mid", 3); p.Position != 3 || p.ID == "" || p.Status != "active" || p.AddedAt == "" {
		t.Errorf("insert before last = %+v", p)
	}
	if p := add("feature/end", 0); p.Position != 5 {
		t.Errorf("append = %d", p.Position)
	}
	if p := add("feature/far", 99); p.Position != 6 {
		t.Errorf("clamped = %d", p.Position)
	}
	want := []string{"feature/a", "feature/b", "feature/mid", "feature/c", "feature/end", "feature/far"}
	if got := positions(t, db, "ws"); !equalStrings(got, want) {
		t.Errorf("order = %v", got)
	}
	if _, err := addPatchWithPosition(db, &patchRow{WorkspaceSlug: "ws", BranchName: "feature/a"}); err == nil {
		t.Error("duplicate branch accepted")
	}
}

// A batch inserts atomically: explicit positions take their slot in batch
// order, the rest append, soft-deleted rows keep their parking slot, and a
// failing element rolls back the whole batch.
func TestPatchStore_AddPatchesBatchOrdersAndRollsBack(t *testing.T) {
	db, store := three(t)
	if err := store.SoftDeletePatch(context.Background(), "pc"); err != nil {
		t.Fatal(err)
	}
	if out, err := addPatchesBatch(db, nil); err != nil || len(out) != 0 {
		t.Errorf("empty batch = %v, %v", out, err)
	}
	out, err := addPatchesBatch(db, []*patchRow{
		{WorkspaceSlug: "ws", BranchName: "feature/x"},
		{WorkspaceSlug: "ws", BranchName: "feature/first", Position: 1},
		{WorkspaceSlug: "ws", BranchName: "feature/y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Position != 4 || out[1].Position != 1 || out[2].Position != 5 {
		t.Errorf("positions = %d %d %d", out[0].Position, out[1].Position, out[2].Position)
	}
	if got := positions(t, db, "ws"); !equalStrings(got, []string{"feature/first", "feature/a", "feature/b", "feature/x", "feature/y"}) {
		t.Errorf("order = %v", got)
	}
	if got := patchStatuses(t, db, "ws")["feature/c"]; got != "deleted" {
		t.Error("soft-deleted row touched by the batch")
	}
	_, err = addPatchesBatch(db, []*patchRow{{WorkspaceSlug: "ws", BranchName: "feature/z"}, {WorkspaceSlug: "ws", BranchName: "feature/a"}})
	if err == nil || !strings.Contains(err.Error(), "patch[1]") {
		t.Errorf("duplicate in batch = %v", err)
	}
	if _, ok := patchStatuses(t, db, "ws")["feature/z"]; ok {
		t.Error("failed batch left a row behind")
	}
}

func TestPatchStore_ReorderValidatesAndRenumbers(t *testing.T) {
	db, store := three(t)
	if err := store.SoftDeletePatch(context.Background(), "pc"); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]string{
		"empty with patches": {},
		"duplicate":          {"pa", "pa"},
		"foreign":            {"pa", "zz"},
		"incomplete":         {"pa"},
		"soft-deleted":       {"pa", "pb", "pc"},
	}
	for name, ids := range bad {
		if _, err := reorderPatches(db, "ws", ids); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	got, err := reorderPatches(db, "ws", []string{"pb", "pa"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "pb" || got[0].Position != 1 || got[1].Position != 2 {
		t.Errorf("reordered = %+v", got)
	}
	if err := updatePatchPosition(db, "ws", "pa", 1); err != nil {
		t.Fatal(err)
	}
	if err := updatePatchPosition(db, "ws", "pa", 1); err != nil {
		t.Errorf("no-op move: %v", err)
	}
	if got := positions(t, db, "ws"); !equalStrings(got, []string{"feature/a", "feature/b"}) {
		t.Errorf("after move = %v", got)
	}
	if err := updatePatchPosition(db, "ws", "missing", 1); err == nil {
		t.Error("move of an unknown patch accepted")
	}
}

func TestPatchStore_DeleteCompactsAndReportsMissing(t *testing.T) {
	db, _ := three(t)
	if err := deletePatchAndCompact(db, "ws", "pa"); err != nil {
		t.Fatal(err)
	}
	if got := positions(t, db, "ws"); !equalStrings(got, []string{"feature/b", "feature/c"}) {
		t.Errorf("after delete = %v", got)
	}
	err := deletePatchAndCompact(db, "ws", "pa")
	if err == nil || err.Error() != `patch "pa" not found in workspace "ws"` {
		t.Errorf("missing = %v", err)
	}
	if p, err := getPatchByBranch(db, "ws", "feature/none"); err != nil || p != nil {
		t.Errorf("getPatchByBranch missing = %v, %v", p, err)
	}
	if p, err := getPatch(db, "ws", "pb"); err != nil || p == nil || p.BranchName != "feature/b" {
		t.Errorf("getPatch = %v, %v", p, err)
	}
	if n, err := patchCount(db, "ws"); err != nil || n != 2 {
		t.Errorf("count = %d, %v", n, err)
	}
}
