package workspace

import (
	"net/http"
	"testing"
)

// The tests of this file used to live beside the patch routes; they test that
// deleting a workspace, which the hub does, deletes its patches, and moved here
// when the routes moved to the carrypatch module.

// ========================================================================
// Spec 15 Task 4.3: Cascade delete patches on workspace deletion
// (TS-15-40)
// Requirements: 15-REQ-13
// ========================================================================

// TS-15-40: Deleting a workspace also deletes all associated patches
// atomically within the same transaction.
// Requirement: 15-REQ-13.1
func TestCarryPatch_CascadeDeletePatches(t *testing.T) {
	slug := "cp-cascade-del"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed patches.
	seedPatchRaw(t, env.db, "cascade-1", slug, "feature/a", 1)
	seedPatchRaw(t, env.db, "cascade-2", slug, "feature/b", 2)
	seedPatchRaw(t, env.db, "cascade-3", slug, "feature/c", 3)

	// Verify patches exist before deletion.
	var countBefore int
	err := env.db.QueryRow("SELECT COUNT(*) FROM patches WHERE workspace_slug = ?", slug).Scan(&countBefore)
	if err != nil {
		t.Fatalf("count before: %v", err)
	}
	if countBefore != 3 {
		t.Fatalf("patches count before = %d; want 3", countBefore)
	}

	// Archive the workspace first (required before delete in existing impl).
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/archive", "", auth)
	if rec.Code != http.StatusOK {
		// If archive isn't supported for carry_patch workspaces, try direct delete.
		t.Logf("archive returned %d; trying direct delete", rec.Code)
	}

	// Delete the workspace.
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug, "", auth)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE workspace status = %d; want %d; body: %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Verify workspace is gone.
	var wsCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", slug).Scan(&wsCount)
	if err != nil {
		t.Fatalf("workspace count query: %v", err)
	}
	if wsCount != 0 {
		t.Errorf("workspaces count = %d; want 0", wsCount)
	}

	// Verify all patches are gone.
	var patchCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM patches WHERE workspace_slug = ?", slug).Scan(&patchCount)
	if err != nil {
		t.Fatalf("patches count query: %v", err)
	}
	if patchCount != 0 {
		t.Errorf("patches count after workspace delete = %d; want 0", patchCount)
	}
}

// 15-REQ-13.E2: Deleting a workspace with no patches works normally.
// Requirement: 15-REQ-13.E2
func TestCarryPatch_CascadeDeletePatches_NoPatches(t *testing.T) {
	slug := "cp-cascade-empty"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// No patches seeded — just the workspace.

	// Archive first.
	env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/archive", "", auth)

	// Delete should succeed.
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug, "", auth)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE workspace (no patches) status = %d; want %d; body: %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Verify workspace is gone.
	var wsCount int
	err := env.db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", slug).Scan(&wsCount)
	if err != nil {
		t.Fatalf("workspace count query: %v", err)
	}
	if wsCount != 0 {
		t.Errorf("workspaces count = %d; want 0", wsCount)
	}
}

// 15-REQ-13.1 (store-level): deleteWorkspace function deletes patches
// alongside the workspace row.
// Requirement: 15-REQ-13.1
func TestCarryPatch_CascadeDeletePatches_StoreLevel(t *testing.T) {
	db := openTestDB(t)
	ensureCarryPatchColumns(t, db)

	slug := "cp-cascade-store"

	// Insert a carry_patch workspace directly.
	seedCarryPatchWorkspaceRaw(t, db, slug,
		"https://github.com/fork/repo.git",
		"https://github.com/upstream/repo.git",
		"deploy")

	// Seed patches.
	seedPatchRaw(t, db, "store-1", slug, "feature/a", 1)
	seedPatchRaw(t, db, "store-2", slug, "feature/b", 2)

	// Call deleteWorkspace directly.
	if err := deleteWorkspace(db, slug); err != nil {
		t.Fatalf("deleteWorkspace() returned error: %v", err)
	}

	// Verify no patches remain.
	var patchCount int
	err := db.QueryRow("SELECT COUNT(*) FROM patches WHERE workspace_slug = ?", slug).Scan(&patchCount)
	if err != nil {
		t.Fatalf("patches count query: %v", err)
	}
	if patchCount != 0 {
		t.Errorf("patches count after deleteWorkspace = %d; want 0", patchCount)
	}

	// Verify workspace is gone.
	var wsCount int
	err = db.QueryRow("SELECT COUNT(*) FROM workspaces WHERE slug = ?", slug).Scan(&wsCount)
	if err != nil {
		t.Fatalf("workspace count query: %v", err)
	}
	if wsCount != 0 {
		t.Errorf("workspace count = %d; want 0", wsCount)
	}
}
