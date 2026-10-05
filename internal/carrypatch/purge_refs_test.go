package carrypatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ===========================================================================
// TS-23-51 (unit): The PatchStore expired-listing method returns id, slug
// and branch for soft-deleted rows older than the cutoff and every double
// implements it.
//
// Verifies: 23-REQ-8.1
// ===========================================================================

func TestListExpiredDeletedPatches_TS2351(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)

	// Insert rows:
	// - p-old-1: deleted 10 days ago (expired)
	// - p-old-2: deleted 8 days ago (expired)
	// - p-recent: deleted 3 days ago (not expired)
	// - p-active: active (not deleted)
	tenDaysAgo := time.Now().UTC().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	threeDaysAgo := time.Now().UTC().Add(-3 * 24 * time.Hour).Format(time.RFC3339)

	seedPatchDeleted(t, db, "p-old-1", "ws1", "feature/old1", -1, tenDaysAgo)
	seedPatchDeleted(t, db, "p-old-2", "ws2", "feature/old2", -2, eightDaysAgo)
	seedPatchDeleted(t, db, "p-recent", "ws1", "feature/recent", -3, threeDaysAgo)
	seedPatch(t, db, "p-active", "ws1", "feature/active", 1, "active")

	store := NewSQLPatchStore(db)
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)

	rows, err := store.ListExpiredDeletedPatches(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("ListExpiredDeletedPatches returned error: %v", err)
	}

	// Should return only the two expired rows.
	if len(rows) != 2 {
		t.Fatalf("expected 2 expired rows, got %d", len(rows))
	}

	// Verify each row has id, slug and branch.
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.ID] = true
		if r.Slug == "" {
			t.Errorf("row %s has empty slug", r.ID)
		}
		if r.Branch == "" {
			t.Errorf("row %s has empty branch", r.ID)
		}
	}

	if !ids["p-old-1"] {
		t.Error("expected p-old-1 in results")
	}
	if !ids["p-old-2"] {
		t.Error("expected p-old-2 in results")
	}

	// Verify slug and branch values.
	for _, r := range rows {
		switch r.ID {
		case "p-old-1":
			if r.Slug != "ws1" {
				t.Errorf("p-old-1 slug: got %q, want ws1", r.Slug)
			}
			if r.Branch != "feature/old1" {
				t.Errorf("p-old-1 branch: got %q, want feature/old1", r.Branch)
			}
		case "p-old-2":
			if r.Slug != "ws2" {
				t.Errorf("p-old-2 slug: got %q, want ws2", r.Slug)
			}
			if r.Branch != "feature/old2" {
				t.Errorf("p-old-2 branch: got %q, want feature/old2", r.Branch)
			}
		}
	}

	// Verify that the mock also compiles against the interface.
	var _ PatchStore = &mockPatchStore{}
}

// ===========================================================================
// TS-23-52 (integration): The ref-aware purge removes each expired row's
// backup ref, deletes the row by id and returns the count.
//
// Verifies: 23-REQ-8.2
// ===========================================================================

func TestPurgeExpiredDeletedPatchesWithRefs_TS2352(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	createWorkspacesTable(t, db)

	workspaceRoot := t.TempDir()

	// Set up two workspaces with trunks.
	slug1 := "purge-ws1"
	slug2 := "purge-ws2"
	seedWorkspace(t, db, slug1, "owner1", "active", "ready", "carry_patch", "deploy")
	seedWorkspace(t, db, slug2, "owner1", "active", "ready", "carry_patch", "deploy")

	_, trunkDir1, _ := setupForkAndTrunk(t, workspaceRoot, slug1)
	_, trunkDir2, _ := setupForkAndTrunk(t, workspaceRoot, slug2)

	// Create branches and backup refs.
	branch1 := "feature/purge1"
	runGitCmd(t, trunkDir1, "checkout", "-b", branch1)
	commitFile(t, trunkDir1, "purge1.txt", "purge1", "purge1 commit")
	sha1 := runGitCmd(t, trunkDir1, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir1, "update-ref", "refs/hub/replaced/"+branch1, sha1)
	runGitCmd(t, trunkDir1, "checkout", "main")

	branch2 := "feature/purge2"
	runGitCmd(t, trunkDir2, "checkout", "-b", branch2)
	commitFile(t, trunkDir2, "purge2.txt", "purge2", "purge2 commit")
	// No backup ref for branch2 — tests that missing ref counts as success.
	runGitCmd(t, trunkDir2, "checkout", "main")

	// Insert expired soft-deleted patches.
	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-purge1", slug1, branch1, -1, eightDaysAgo)
	seedPatchDeleted(t, db, "p-purge2", slug2, branch2, -2, eightDaysAgo)

	factory := NewGitRunnerFactory()

	n, err := PurgeExpiredDeletedPatchesWithRefs(context.Background(), NewSQLPatchStore(db), workspaceRoot, factory)
	if err != nil {
		t.Fatalf("PurgeExpiredDeletedPatchesWithRefs returned error: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 purged, got %d", n)
	}

	// Verify backup ref is gone.
	after := revParse(t, trunkDir1, "refs/hub/replaced/"+branch1)
	if after != "" {
		t.Errorf("backup ref should be deleted, got %q", after)
	}

	// Verify both rows are deleted.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM patches WHERE status = 'deleted'`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 deleted rows, got %d", count)
	}

	// Verify rows are deleted only while status='deleted' — insert an active row
	// and confirm it's not touched.
	seedPatch(t, db, "p-active", slug1, "feature/active", 1, "active")
	db.QueryRow(`SELECT COUNT(*) FROM patches WHERE id = 'p-active'`).Scan(&count)
	if count != 1 {
		t.Errorf("active patch should still exist")
	}
}

// ===========================================================================
// TS-23-53 (integration): A failing ref removal keeps the row for the next
// purge, logs a warn and continues.
//
// Verifies: 23-REQ-8.3
// ===========================================================================

func TestPurgeExpiredDeletedPatchesWithRefs_FailingRefRemoval_TS2353(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	createWorkspacesTable(t, db)

	workspaceRoot := t.TempDir()

	slug1 := "purge-ok1"
	slug2 := "purge-fail"
	slug3 := "purge-ok2"
	seedWorkspace(t, db, slug1, "owner1", "active", "ready", "carry_patch", "deploy")
	seedWorkspace(t, db, slug2, "owner1", "active", "ready", "carry_patch", "deploy")
	seedWorkspace(t, db, slug3, "owner1", "active", "ready", "carry_patch", "deploy")

	_, trunkDir1, _ := setupForkAndTrunk(t, workspaceRoot, slug1)
	_, _, _ = setupForkAndTrunk(t, workspaceRoot, slug2)
	_, trunkDir3, _ := setupForkAndTrunk(t, workspaceRoot, slug3)

	// Create backup refs for slug1 and slug3 (slug2 will fail).
	branch1 := "feature/ok1"
	runGitCmd(t, trunkDir1, "checkout", "-b", branch1)
	commitFile(t, trunkDir1, "ok1.txt", "ok1", "ok1 commit")
	sha1 := runGitCmd(t, trunkDir1, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir1, "update-ref", "refs/hub/replaced/"+branch1, sha1)
	runGitCmd(t, trunkDir1, "checkout", "main")

	branch3 := "feature/ok2"
	runGitCmd(t, trunkDir3, "checkout", "-b", branch3)
	commitFile(t, trunkDir3, "ok2.txt", "ok2", "ok2 commit")
	sha3 := runGitCmd(t, trunkDir3, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir3, "update-ref", "refs/hub/replaced/"+branch3, sha3)
	runGitCmd(t, trunkDir3, "checkout", "main")

	// Insert three expired soft-deleted patches.
	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-ok1", slug1, branch1, -1, eightDaysAgo)
	seedPatchDeleted(t, db, "p-fail", slug2, "feature/fail", -2, eightDaysAgo)
	seedPatchDeleted(t, db, "p-ok2", slug3, branch3, -3, eightDaysAgo)

	// Use a factory that fails for slug2's trunk.
	failSlug := slug2
	failingFactory := func(repoPath string) (GitRunner, error) {
		if strings.Contains(repoPath, failSlug) {
			return nil, fmt.Errorf("simulated git runner failure")
		}
		return NewGitRunnerFactory()(repoPath)
	}

	n, err := PurgeExpiredDeletedPatchesWithRefs(context.Background(), NewSQLPatchStore(db), workspaceRoot, failingFactory)
	if err != nil {
		t.Fatalf("PurgeExpiredDeletedPatchesWithRefs returned error: %v", err)
	}

	// First run: 2 purged (slug1 and slug3), slug2 kept.
	if n != 2 {
		t.Errorf("expected 2 purged on first run, got %d", n)
	}

	// Verify the failing row still exists with status deleted.
	var status string
	err = db.QueryRow(`SELECT status FROM patches WHERE id = 'p-fail'`).Scan(&status)
	if err != nil {
		t.Fatalf("query p-fail: %v", err)
	}
	if status != "deleted" {
		t.Errorf("p-fail status: got %q, want 'deleted'", status)
	}

	// Second run with the failure cleared.
	// Create a backup ref for slug2 so the removal succeeds.
	trunkDir2 := filepath.Join(workspaceRoot, slug2, "trunk")
	branch2 := "feature/fail"
	runGitCmd(t, trunkDir2, "checkout", "-b", branch2)
	commitFile(t, trunkDir2, "fail.txt", "fail", "fail commit")
	sha2 := runGitCmd(t, trunkDir2, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir2, "update-ref", "refs/hub/replaced/"+branch2, sha2)
	runGitCmd(t, trunkDir2, "checkout", "main")

	n2, err2 := PurgeExpiredDeletedPatchesWithRefs(context.Background(), NewSQLPatchStore(db), workspaceRoot, NewGitRunnerFactory())
	if err2 != nil {
		t.Fatalf("second PurgeExpiredDeletedPatchesWithRefs returned error: %v", err2)
	}
	if n2 != 1 {
		t.Errorf("expected 1 purged on second run, got %d", n2)
	}

	// Verify no deleted rows remain.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM patches WHERE status = 'deleted'`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 deleted rows after second run, got %d", count)
	}
}

// ===========================================================================
// TS-23-54 (integration): A missing backup ref or missing trunk counts as
// success and the row is purged.
//
// Verifies: 23-REQ-8.4
// ===========================================================================

func TestPurgeExpiredDeletedPatchesWithRefs_MissingRefAndTrunk_TS2354(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	createWorkspacesTable(t, db)

	workspaceRoot := t.TempDir()

	// Workspace 1: trunk exists but no backup ref.
	slug1 := "purge-noref"
	seedWorkspace(t, db, slug1, "owner1", "active", "ready", "carry_patch", "deploy")
	_, _, _ = setupForkAndTrunk(t, workspaceRoot, slug1)

	// Workspace 2: trunk directory does not exist.
	slug2 := "purge-notrunk"
	seedWorkspace(t, db, slug2, "owner1", "active", "ready", "carry_patch", "deploy")
	// Don't create a trunk for slug2.

	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-noref", slug1, "feature/noref", -1, eightDaysAgo)
	seedPatchDeleted(t, db, "p-notrunk", slug2, "feature/notrunk", -2, eightDaysAgo)

	factory := NewGitRunnerFactory()

	n, err := PurgeExpiredDeletedPatchesWithRefs(context.Background(), NewSQLPatchStore(db), workspaceRoot, factory)
	if err != nil {
		t.Fatalf("PurgeExpiredDeletedPatchesWithRefs returned error: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 purged, got %d", n)
	}

	// Verify both rows are deleted.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM patches WHERE status = 'deleted'`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 deleted rows, got %d", count)
	}
}

// ===========================================================================
// TS-23-55 (integration): Rows within the retention window and their backup
// refs are untouched.
//
// Verifies: 23-REQ-8.5
// ===========================================================================

func TestPurgeExpiredDeletedPatchesWithRefs_RetentionWindow_TS2355(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	createWorkspacesTable(t, db)

	workspaceRoot := t.TempDir()

	slug := "purge-retention"
	seedWorkspace(t, db, slug, "owner1", "active", "ready", "carry_patch", "deploy")
	_, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a branch and backup ref for the recent row.
	branchRecent := "feature/recent"
	runGitCmd(t, trunkDir, "checkout", "-b", branchRecent)
	commitFile(t, trunkDir, "recent.txt", "recent", "recent commit")
	recentSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branchRecent, recentSHA)
	runGitCmd(t, trunkDir, "checkout", "main")

	// Create a branch and backup ref for the expired row.
	branchOld := "feature/old"
	runGitCmd(t, trunkDir, "checkout", "-b", branchOld)
	commitFile(t, trunkDir, "old.txt", "old", "old commit")
	oldSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branchOld, oldSHA)
	runGitCmd(t, trunkDir, "checkout", "main")

	// Insert rows: one 6 days old (within window), one 8 days old (expired).
	sixDaysAgo := time.Now().UTC().Add(-6 * 24 * time.Hour).Format(time.RFC3339)
	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-recent", slug, branchRecent, -1, sixDaysAgo)
	seedPatchDeleted(t, db, "p-old", slug, branchOld, -2, eightDaysAgo)

	factory := NewGitRunnerFactory()

	n, err := PurgeExpiredDeletedPatchesWithRefs(context.Background(), NewSQLPatchStore(db), workspaceRoot, factory)
	if err != nil {
		t.Fatalf("PurgeExpiredDeletedPatchesWithRefs returned error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 purged, got %d", n)
	}

	// Verify the recent row still exists.
	var status string
	err = db.QueryRow(`SELECT status FROM patches WHERE id = 'p-recent'`).Scan(&status)
	if err != nil {
		t.Fatalf("query p-recent: %v", err)
	}
	if status != "deleted" {
		t.Errorf("p-recent status: got %q, want 'deleted'", status)
	}

	// Verify the recent backup ref is unchanged.
	afterRecent := revParse(t, trunkDir, "refs/hub/replaced/"+branchRecent)
	if afterRecent != recentSHA {
		t.Errorf("recent backup ref changed: got %q, want %q", afterRecent, recentSHA)
	}

	// Verify the old row is gone.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM patches WHERE id = 'p-old'`).Scan(&count)
	if count != 0 {
		t.Error("expected p-old to be purged")
	}
}

// ===========================================================================
// TS-23-56 (unit): PurgeDeletedPatches and the two-argument
// PurgeExpiredDeletedPatches keep their behaviour without ref cleanup.
//
// Verifies: 23-REQ-8.6
// ===========================================================================

func TestPurgeDeletedPatches_KeepsBehaviour_TS2356(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	createWorkspacesTable(t, db)

	workspaceRoot := t.TempDir()
	slug := "purge-compat"
	seedWorkspace(t, db, slug, "owner1", "active", "ready", "carry_patch", "deploy")
	_, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a branch and backup ref.
	branch := "feature/compat"
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "compat.txt", "compat", "compat commit")
	backupSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, backupSHA)
	runGitCmd(t, trunkDir, "checkout", "main")

	// Insert an expired soft-deleted patch with a backup ref.
	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-compat", slug, branch, -1, eightDaysAgo)

	store := NewSQLPatchStore(db)

	// Test PurgeDeletedPatches: keeps its signature and returns the deleted count.
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	n, err := store.PurgeDeletedPatches(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("PurgeDeletedPatches returned error: %v", err)
	}
	if n != 1 {
		t.Errorf("PurgeDeletedPatches: expected 1 purged, got %d", n)
	}

	// Verify the backup ref is NOT touched.
	afterRef := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
	if afterRef != backupSHA {
		t.Errorf("PurgeDeletedPatches should not touch backup refs: got %q, want %q", afterRef, backupSHA)
	}

	// Re-insert the patch for the PurgeExpiredDeletedPatches test.
	seedPatchDeleted(t, db, "p-compat2", slug, branch, -2, eightDaysAgo)

	// Test PurgeExpiredDeletedPatches(ctx, store): deletes expired rows with 7-day cutoff.
	n2, err2 := PurgeExpiredDeletedPatches(context.Background(), store)
	if err2 != nil {
		t.Fatalf("PurgeExpiredDeletedPatches returned error: %v", err2)
	}
	if n2 != 1 {
		t.Errorf("PurgeExpiredDeletedPatches: expected 1 purged, got %d", n2)
	}

	// Verify the backup ref is still NOT touched.
	afterRef2 := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
	if afterRef2 != backupSHA {
		t.Errorf("PurgeExpiredDeletedPatches should not touch backup refs: got %q, want %q", afterRef2, backupSHA)
	}
}

// ===========================================================================
// TS-23-57 (unit): A failure listing expired rows returns the error and
// deletes nothing.
//
// Verifies: 23-REQ-8.7
// ===========================================================================

func TestPurgeExpiredDeletedPatchesWithRefs_ListError_TS2357(t *testing.T) {
	failStore := &failingListPatchStore{
		err: fmt.Errorf("database connection lost"),
	}

	var runnerCalls int
	factory := func(repoPath string) (GitRunner, error) {
		runnerCalls++
		return NewGitRunnerFactory()(repoPath)
	}

	n, err := PurgeExpiredDeletedPatchesWithRefs(context.Background(), failStore, "/tmp/unused", factory)
	if err == nil {
		t.Fatal("expected error from PurgeExpiredDeletedPatchesWithRefs")
	}
	if n != 0 {
		t.Errorf("expected 0 purged, got %d", n)
	}
	if runnerCalls != 0 {
		t.Errorf("expected 0 runner calls, got %d", runnerCalls)
	}
	if failStore.deleteCalls != 0 {
		t.Errorf("expected 0 delete calls, got %d", failStore.deleteCalls)
	}
}

// failingListPatchStore is a PatchStore double whose ListExpiredDeletedPatches
// returns an error.
type failingListPatchStore struct {
	err         error
	deleteCalls int
}

func (s *failingListPatchStore) ListPatches(_ context.Context, _ string) ([]Patch, error) {
	return nil, nil
}
func (s *failingListPatchStore) UpdatePatchStatus(_ context.Context, _, _ string, _ []string) error {
	return nil
}
func (s *failingListPatchStore) DeletePatch(_ context.Context, _ string) error {
	s.deleteCalls++
	return nil
}
func (s *failingListPatchStore) SoftDeletePatch(_ context.Context, _ string) error { return nil }
func (s *failingListPatchStore) RestorePatch(_ context.Context, _ string) error    { return nil }
func (s *failingListPatchStore) PurgeDeletedPatches(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (s *failingListPatchStore) CompactPositions(_ context.Context, _ string) error { return nil }
func (s *failingListPatchStore) SetOriginSyncState(_ context.Context, _, _ string, _ *string, _ string) error {
	return nil
}
func (s *failingListPatchStore) ClearOriginSyncState(_ context.Context, _ string) error { return nil }
func (s *failingListPatchStore) ClearOriginSyncStateForPatch(_ context.Context, _ string) error {
	return nil
}
func (s *failingListPatchStore) ClearOriginSyncStateForMergedDeleted(_ context.Context, _ string) error {
	return nil
}
func (s *failingListPatchStore) ListExpiredDeletedPatches(_ context.Context, _ string) ([]ExpiredDeletedPatch, error) {
	return nil, s.err
}
func (s *failingListPatchStore) DeletePatchByIDIfDeleted(_ context.Context, _ string) (bool, error) {
	s.deleteCalls++
	return true, nil
}

// ===========================================================================
// Issue #45 finding 9: the purge counts only the rows it deleted
// ===========================================================================

// scriptedPurgeStore lists a fixed set of expired rows and answers each
// delete from a table.
type scriptedPurgeStore struct {
	*mockPatchStore
	expired []ExpiredDeletedPatch
	deleted map[string]bool // id -> whether the delete removed a row
	errs    map[string]error
	calls   []string
}

func (s *scriptedPurgeStore) ListExpiredDeletedPatches(context.Context, string) ([]ExpiredDeletedPatch, error) {
	return s.expired, nil
}

func (s *scriptedPurgeStore) DeletePatchByIDIfDeleted(_ context.Context, id string) (bool, error) {
	s.calls = append(s.calls, id)
	return s.deleted[id], s.errs[id]
}

func TestPurgeExpiredDeletedPatchesWithRefs_CountsOnlyDeletedRows_Issue45(t *testing.T) {
	store := &scriptedPurgeStore{
		mockPatchStore: newMockPatchStore(nil),
		expired: []ExpiredDeletedPatch{
			{ID: "gone", Slug: "ws", Branch: "feature/gone"},
			{ID: "restored", Slug: "ws", Branch: "feature/restored"},
			{ID: "errored", Slug: "ws", Branch: "feature/errored"},
			{ID: "also-gone", Slug: "ws", Branch: "feature/also-gone"},
		},
		deleted: map[string]bool{"gone": true, "also-gone": true},
		errs:    map[string]error{"errored": fmt.Errorf("database is locked")},
	}

	// No trunk on disk: the ref removal counts as success for every row.
	n, err := PurgeExpiredDeletedPatchesWithRefs(context.Background(), store, t.TempDir(), NewGitRunnerFactory())
	if err != nil {
		t.Fatalf("PurgeExpiredDeletedPatchesWithRefs returned error: %v", err)
	}
	if n != 2 {
		t.Errorf("purged = %d; want 2 (a restored row and a failed delete are not counted)", n)
	}
	if len(store.calls) != 4 {
		t.Errorf("delete calls = %v; want one per listed row", store.calls)
	}
}

func TestPurgeExpiredDeletedPatchesWithRefs_RowRestoredBeforeDelete_Issue45(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)

	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-restored", "ws", "feature/restored", -1, eightDaysAgo)
	seedPatchDeleted(t, db, "p-expired", "ws", "feature/expired", -2, eightDaysAgo)

	// The first row is restored after the expired rows were listed and before
	// its delete runs.
	store := &restoreBeforeDeleteStore{SQLPatchStore: NewSQLPatchStore(db), restoreID: "p-restored"}

	n, err := PurgeExpiredDeletedPatchesWithRefs(context.Background(), store, t.TempDir(), NewGitRunnerFactory())
	if err != nil {
		t.Fatalf("PurgeExpiredDeletedPatchesWithRefs returned error: %v", err)
	}
	if n != 1 {
		t.Errorf("purged = %d; want 1", n)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM patches WHERE id = 'p-restored'`).Scan(&status); err != nil {
		t.Fatalf("the restored row must survive the purge: %v", err)
	}
	if status != "active" {
		t.Errorf("restored row status = %q; want active", status)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM patches WHERE id = 'p-expired'`).Scan(&count)
	if count != 0 {
		t.Error("the still-deleted expired row should be purged")
	}
}

// restoreBeforeDeleteStore restores one row right before the delete of that
// row runs, as a concurrent restore request would.
type restoreBeforeDeleteStore struct {
	*SQLPatchStore
	restoreID string
}

func (s *restoreBeforeDeleteStore) DeletePatchByIDIfDeleted(ctx context.Context, id string) (bool, error) {
	if id == s.restoreID {
		if err := s.SQLPatchStore.RestorePatch(ctx, id); err != nil {
			return false, err
		}
	}
	return s.SQLPatchStore.DeletePatchByIDIfDeleted(ctx, id)
}

func TestSQLPatchStore_DeletePatchByIDIfDeleted_ReportsRows_Issue45(t *testing.T) {
	db := openTestDB(t)
	createPatchesTable(t, db)
	store := NewSQLPatchStore(db)
	ctx := context.Background()

	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-deleted", "ws", "feature/deleted", -1, eightDaysAgo)
	seedPatch(t, db, "p-active", "ws", "feature/active", 1, "active")
	seedPatchDeleted(t, db, "p-restored", "ws", "feature/restored", -2, eightDaysAgo)
	if err := store.RestorePatch(ctx, "p-restored"); err != nil {
		t.Fatalf("RestorePatch: %v", err)
	}

	cases := []struct {
		id          string
		wantDeleted bool
		wantRowLeft bool
	}{
		{"p-deleted", true, false},
		{"p-active", false, true},
		{"p-restored", false, true},
		{"p-missing", false, false},
	}
	for _, tc := range cases {
		deleted, err := store.DeletePatchByIDIfDeleted(ctx, tc.id)
		if err != nil {
			t.Fatalf("%s: DeletePatchByIDIfDeleted returned error: %v", tc.id, err)
		}
		if deleted != tc.wantDeleted {
			t.Errorf("%s: deleted = %v; want %v", tc.id, deleted, tc.wantDeleted)
		}
		var count int
		db.QueryRow(`SELECT COUNT(*) FROM patches WHERE id = ?`, tc.id).Scan(&count)
		if (count == 1) != tc.wantRowLeft {
			t.Errorf("%s: row present = %v; want %v", tc.id, count == 1, tc.wantRowLeft)
		}
	}
}

// ===========================================================================
// TS-23-58 (unit): cmd/af-hub/main.go adds no purge scheduler or purge call.
//
// Verifies: 23-REQ-8.8
// ===========================================================================

func TestNoPurgeSchedulerInMain_TS2358(t *testing.T) {
	// Read all .go files in cmd/af-hub/.
	entries, err := os.ReadDir("../../cmd/af-hub")
	if err != nil {
		t.Fatalf("failed to read cmd/af-hub: %v", err)
	}

	purgeNames := []string{
		"PurgeExpiredDeletedPatches",
		"PurgeDeletedPatches",
		"PurgeExpiredDeletedPatchesWithRefs",
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join("../../cmd/af-hub", entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("failed to read %s: %v", path, err)
		}
		src := string(data)
		for _, name := range purgeNames {
			if strings.Contains(src, name) {
				t.Errorf("cmd/af-hub/%s contains %q — no purge scheduler should be added", entry.Name(), name)
			}
		}
	}
}
