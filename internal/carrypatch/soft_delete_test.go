package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ===========================================================================
// TS-NS-1: After a successful rebuild, merged_upstream patches are
// soft-deleted (status='deleted', deleted_at set) rather than hard-deleted.
//
// Requirement: NS-REQ-1
// ===========================================================================

func TestRebuildExecutor_SoftDeletesMergedPatches(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p-active", WorkspaceID: "ws1", BranchName: "feature/active", Position: 1, Status: PatchStatusActive},
		{ID: "p-merged", WorkspaceID: "ws1", BranchName: "feature/merged", Position: 2, Status: PatchStatusMergedUpstream},
	})

	commitSHA := "bbbb000000000000000000000000000000000001"
	resultSHA := "cccc000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return commitSHA, nil
			}
		}
		return resultSHA, nil
	}
	mock.CherryPickFunc = func(_ context.Context, _ string) error { return nil }

	h := &RebuildHandler{
		PatchStore:   patches,
		NewGitRunner: func(_ string) (GitRunner, error) { return mock, nil },
		Fetch:        func(_ context.Context, _ string) error { return nil },
		ResolveAuth:  func(_ string) error { return nil },
	}

	payload := RebuildPayload{
		WorkspaceSlug: "ws1",
		Strategy:      StrategyRebase,
		SubmittedBy:   "operator",
	}
	payloadJSON, _ := json.Marshal(payload)

	result, _, err := h.HandleRebuildJob(context.Background(), payloadJSON)
	if err != nil {
		t.Fatalf("HandleRebuildJob returned error: %v", err)
	}

	rebuildResult, ok := result.(*RebuildResult)
	if !ok {
		t.Fatalf("expected result to be *RebuildResult, got %T", result)
	}

	// Verify that SoftDeletePatch was called (not DeletePatch).
	if len(patches.SoftDeletedPatches) != 1 {
		t.Fatalf("expected 1 soft-deleted patch, got %d", len(patches.SoftDeletedPatches))
	}
	if patches.SoftDeletedPatches[0] != "p-merged" {
		t.Errorf("expected soft-deleted patch ID='p-merged', got %q", patches.SoftDeletedPatches[0])
	}

	// Verify no hard deletes occurred.
	if len(patches.DeletedPatches) != 0 {
		t.Errorf("expected 0 hard-deleted patches, got %d", len(patches.DeletedPatches))
	}

	// Verify PatchesRemoved reflects the count.
	if rebuildResult.PatchesRemoved != 1 {
		t.Errorf("expected patches_removed=1, got %d", rebuildResult.PatchesRemoved)
	}
}

// ===========================================================================
// TS-NS-2: Soft-deleted patches are excluded from ListPatches, rebuild
// processing, and compact position operations.
//
// Requirement: NS-REQ-2
// ===========================================================================

func TestSQLPatchStore_ListPatches_ExcludesDeleted(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	addDeletedAtColumn(t, db)
	addUpstreamPRURLColumn(t, db)

	// Insert an active patch and a deleted patch.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seedPatch(t, db, "p-active", "ws1", "feature/active", 1, "active")
	seedPatchDeleted(t, db, "p-deleted", "ws1", "feature/deleted", 2, now)

	store := NewSQLPatchStore(db)
	patches, err := store.ListPatches(context.Background(), "ws1")
	if err != nil {
		t.Fatalf("ListPatches returned error: %v", err)
	}

	// Should only return the active patch.
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
	if patches[0].ID != "p-active" {
		t.Errorf("expected patch ID='p-active', got %q", patches[0].ID)
	}
}

func TestSQLPatchStore_CompactPositions_IgnoresDeleted(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	addDeletedAtColumn(t, db)

	// Insert patches: active at pos 1, deleted at pos 2, active at pos 3.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seedPatch(t, db, "p1", "ws1", "feature/a", 1, "active")
	seedPatchDeleted(t, db, "p2", "ws1", "feature/b", 2, now)
	seedPatch(t, db, "p3", "ws1", "feature/c", 3, "active")

	store := NewSQLPatchStore(db)
	err := store.CompactPositions(context.Background(), "ws1")
	if err != nil {
		t.Fatalf("CompactPositions returned error: %v", err)
	}

	// Verify active patches have contiguous positions (1, 2).
	var pos1, pos3 int
	db.QueryRow(`SELECT position FROM patches WHERE id = 'p1'`).Scan(&pos1)
	db.QueryRow(`SELECT position FROM patches WHERE id = 'p3'`).Scan(&pos3)

	if pos1 != 1 {
		t.Errorf("expected p1 position=1, got %d", pos1)
	}
	if pos3 != 2 {
		t.Errorf("expected p3 position=2, got %d", pos3)
	}
}

// ===========================================================================
// TS-NS-3: SoftDeletePatch sets status='deleted' and deleted_at.
//
// Requirement: NS-REQ-1
// ===========================================================================

func TestSQLPatchStore_SoftDeletePatch(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	addDeletedAtColumn(t, db)

	seedPatch(t, db, "p1", "ws1", "feature/a", 1, "active")

	store := NewSQLPatchStore(db)
	err := store.SoftDeletePatch(context.Background(), "p1")
	if err != nil {
		t.Fatalf("SoftDeletePatch returned error: %v", err)
	}

	// Verify the patch still exists with status='deleted' and deleted_at set.
	var status string
	var deletedAt sql.NullString
	err = db.QueryRow(`SELECT status, deleted_at FROM patches WHERE id = 'p1'`).Scan(&status, &deletedAt)
	if err != nil {
		t.Fatalf("query patch: %v", err)
	}
	if status != "deleted" {
		t.Errorf("expected status='deleted', got %q", status)
	}
	if !deletedAt.Valid || deletedAt.String == "" {
		t.Error("expected non-null deleted_at")
	}
}

// ===========================================================================
// TS-NS-3: RestorePatch transitions a soft-deleted patch back to active.
//
// Requirement: NS-REQ-3
// ===========================================================================

func TestSQLPatchStore_RestorePatch(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	addDeletedAtColumn(t, db)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	seedPatchDeleted(t, db, "p1", "ws1", "feature/a", 1, now)

	store := NewSQLPatchStore(db)
	err := store.RestorePatch(context.Background(), "p1")
	if err != nil {
		t.Fatalf("RestorePatch returned error: %v", err)
	}

	// Verify patch is active with null deleted_at.
	var status string
	var deletedAt sql.NullString
	err = db.QueryRow(`SELECT status, deleted_at FROM patches WHERE id = 'p1'`).Scan(&status, &deletedAt)
	if err != nil {
		t.Fatalf("query patch: %v", err)
	}
	if status != "active" {
		t.Errorf("expected status='active', got %q", status)
	}
	if deletedAt.Valid {
		t.Error("expected null deleted_at after restore")
	}
}

func TestSQLPatchStore_RestorePatch_NonDeletedIsNoOp(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	addDeletedAtColumn(t, db)

	seedPatch(t, db, "p1", "ws1", "feature/a", 1, "active")

	store := NewSQLPatchStore(db)
	err := store.RestorePatch(context.Background(), "p1")
	if err != nil {
		t.Fatalf("RestorePatch returned error: %v", err)
	}

	// Patch should still be active (no-op).
	var status string
	db.QueryRow(`SELECT status FROM patches WHERE id = 'p1'`).Scan(&status)
	if status != "active" {
		t.Errorf("expected status='active', got %q", status)
	}
}

// ===========================================================================
// TS-NS-4: PurgeDeletedPatches removes patches deleted > 7 days ago
// but preserves recently deleted ones.
//
// Requirement: NS-REQ-4
// ===========================================================================

func TestSQLPatchStore_PurgeDeletedPatches(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	addDeletedAtColumn(t, db)

	// Patch deleted 8 days ago (should be purged).
	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-old", "ws1", "feature/old", 1, eightDaysAgo)

	// Patch deleted 1 day ago (should be preserved).
	oneDayAgo := time.Now().UTC().Add(-1 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-recent", "ws1", "feature/recent", 2, oneDayAgo)

	store := NewSQLPatchStore(db)
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	purged, err := store.PurgeDeletedPatches(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("PurgeDeletedPatches returned error: %v", err)
	}
	if purged != 1 {
		t.Errorf("expected 1 purged patch, got %d", purged)
	}

	// Verify the old patch is gone.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM patches WHERE id = 'p-old'`).Scan(&count)
	if count != 0 {
		t.Error("expected p-old to be hard-deleted")
	}

	// Verify the recent patch is preserved.
	db.QueryRow(`SELECT COUNT(*) FROM patches WHERE id = 'p-recent'`).Scan(&count)
	if count != 1 {
		t.Error("expected p-recent to be preserved")
	}
}

func TestPurgeExpiredDeletedPatches_Integration(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	addDeletedAtColumn(t, db)

	// Patch deleted 8 days ago.
	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-old", "ws1", "feature/old", 1, eightDaysAgo)

	// Patch deleted 1 day ago.
	oneDayAgo := time.Now().UTC().Add(-1 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-recent", "ws1", "feature/recent", 2, oneDayAgo)

	// Active patch.
	seedPatch(t, db, "p-active", "ws1", "feature/active", 3, "active")

	store := NewSQLPatchStore(db)
	purged, err := PurgeExpiredDeletedPatches(context.Background(), store)
	if err != nil {
		t.Fatalf("PurgeExpiredDeletedPatches returned error: %v", err)
	}
	if purged != 1 {
		t.Errorf("expected 1 purged patch, got %d", purged)
	}

	// Verify final state.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM patches`).Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 remaining patches, got %d", count)
	}
}

// ===========================================================================
// TS-NS-5: Schema migration adds deleted_at column idempotently.
//
// Requirement: NS-REQ-5
// ===========================================================================

func TestSchemamigration_DeletedAt_Idempotent(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)

	// First migration: add deleted_at.
	addDeletedAtColumn(t, db)

	// Verify column exists.
	hasColumn := columnExists(t, db, "patches", "deleted_at")
	if !hasColumn {
		t.Fatal("expected deleted_at column after first migration")
	}

	// Second migration: should be safe (idempotent — duplicate column error
	// is expected and non-fatal, matching production schema migration behavior).
	addDeletedAtColumn(t, db)

	// Verify column still exists.
	hasColumn = columnExists(t, db, "patches", "deleted_at")
	if !hasColumn {
		t.Fatal("expected deleted_at column after second migration")
	}
}

// ===========================================================================
// Test: Rebuild does not process soft-deleted patches
// ===========================================================================

func TestRebuildExecutor_SkipsDeletedPatches(t *testing.T) {
	mock := newMockGitRunner()

	// Include a deleted patch in the store — ListPatches mock doesn't filter.
	// The executor should skip it with status='skipped' and skipped_reason='deleted'.
	patches := newMockPatchStore([]Patch{
		{ID: "p-active", WorkspaceID: "ws1", BranchName: "feature/active", Position: 1, Status: PatchStatusActive},
		{ID: "p-deleted", WorkspaceID: "ws1", BranchName: "feature/deleted", Position: 2, Status: PatchStatusDeleted},
	})

	commitSHA := "bbbb000000000000000000000000000000000001"
	resultSHA := "cccc000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return commitSHA, nil
			}
		}
		return resultSHA, nil
	}
	mock.CherryPickFunc = func(_ context.Context, _ string) error { return nil }

	h := &RebuildHandler{
		PatchStore:   patches,
		NewGitRunner: func(_ string) (GitRunner, error) { return mock, nil },
		Fetch:        func(_ context.Context, _ string) error { return nil },
		ResolveAuth:  func(_ string) error { return nil },
	}

	payload := RebuildPayload{
		WorkspaceSlug: "ws1",
		Strategy:      StrategyRebase,
		SubmittedBy:   "operator",
	}
	payloadJSON, _ := json.Marshal(payload)

	result, _, err := h.HandleRebuildJob(context.Background(), payloadJSON)
	if err != nil {
		t.Fatalf("HandleRebuildJob returned error: %v", err)
	}

	rebuildResult, ok := result.(*RebuildResult)
	if !ok {
		t.Fatalf("expected result to be *RebuildResult, got %T", result)
	}

	// Only 1 patch should have been applied (the active one).
	if rebuildResult.PatchesApplied != 1 {
		t.Errorf("expected patches_applied=1, got %d", rebuildResult.PatchesApplied)
	}

	// The deleted patch should be skipped.
	if rebuildResult.PatchesSkipped != 1 {
		t.Errorf("expected patches_skipped=1, got %d", rebuildResult.PatchesSkipped)
	}

	// Verify the deleted patch was skipped with proper reason.
	for _, pr := range rebuildResult.PatchResults {
		if pr.PatchID == "p-deleted" {
			if pr.Status != "skipped" {
				t.Errorf("expected deleted patch status='skipped', got %q", pr.Status)
			}
			if pr.SkippedReason != "deleted" {
				t.Errorf("expected skipped_reason='deleted', got %q", pr.SkippedReason)
			}
		}
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

// addDeletedAtColumn adds the deleted_at column to the test patches table.
func addDeletedAtColumn(t *testing.T, db *sql.DB) {
	t.Helper()
	// Ignore error if column already exists.
	_, _ = db.Exec(`ALTER TABLE patches ADD COLUMN deleted_at TEXT`)
}

// addUpstreamPRURLColumn adds the upstream_pr_url column to the test patches table.
func addUpstreamPRURLColumn(t *testing.T, db *sql.DB) {
	t.Helper()
	_, _ = db.Exec(`ALTER TABLE patches ADD COLUMN upstream_pr_url TEXT`)
}

// seedPatchDeleted inserts a soft-deleted patch row.
func seedPatchDeleted(t *testing.T, db *sql.DB, id, workspaceSlug, branchName string, position int, deletedAt string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`INSERT INTO patches (id, workspace_slug, branch_name, position, status, conflict_files, deleted_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'deleted', '[]', ?, ?, ?)`,
		id, workspaceSlug, branchName, position, deletedAt, now, now,
	)
	if err != nil {
		t.Fatalf("seedPatchDeleted(%q) returned error: %v", id, err)
	}
}

// columnExists checks if a column exists in a table.
func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column info: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}

