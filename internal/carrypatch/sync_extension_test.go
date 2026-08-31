package carrypatch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// ===========================================================================
// TS-16-15: Sync on a carry_patch workspace resolves upstream credentials,
// fetches from the 'upstream' remote, advances the upstream_tracking_ref,
// checks each active patch for upstream merge via IsAncestor, and returns
// a sync response with patches_merged and rebuild_triggered fields.
//
// Requirement: 16-REQ-5.1
// ===========================================================================

func TestCarryPatchSync_Returns200WithSyncFields(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	// Seed 2 active patches via the DB (not mock store).
	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	seedPatch(t, env.db, "p2", "my-workspace", "feature/b", 2, PatchStatusActive)

	// Update the mock PatchStore to return the patches.
	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", WorkspaceID: "my-workspace", BranchName: "feature/b", Position: 2, Status: PatchStatusActive},
	}

	// IsAncestor returns false for both patches (neither merged upstream).
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/sync", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify carry-patch-specific fields are present.
	if _, ok := resp["patches_merged"]; !ok {
		t.Error("expected 'patches_merged' field in sync response")
	}
	if _, ok := resp["rebuild_triggered"]; !ok {
		t.Error("expected 'rebuild_triggered' field in sync response")
	}

	// Parse patches_merged.
	var patchesMerged []string
	if err := json.Unmarshal(resp["patches_merged"], &patchesMerged); err != nil {
		t.Fatalf("failed to parse patches_merged: %v", err)
	}
	if len(patchesMerged) != 0 {
		t.Errorf("expected empty patches_merged (no patches merged), got %v", patchesMerged)
	}

	// Parse rebuild_triggered.
	var rebuildTriggered bool
	if err := json.Unmarshal(resp["rebuild_triggered"], &rebuildTriggered); err != nil {
		t.Fatalf("failed to parse rebuild_triggered: %v", err)
	}
	// rebuild_triggered should be true because upstream ref advanced and
	// AUTO_REBUILD_AFTER_SYNC defaults to true.
	if !rebuildTriggered {
		t.Error("expected rebuild_triggered=true when upstream ref advanced")
	}
}

// 16-REQ-5.E4: Sync on a standard workspace does not include carry-patch fields.
func TestCarryPatchSync_StandardWorkspace_NoCarryPatchFields(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "ws-std", "alice", "active", "ready", "standard", "")

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-std/sync", "", auth)

	// Standard workspace sync should not return carry-patch-specific fields
	// or should use the standard sync handler.
	if rec.Code == http.StatusOK {
		var resp map[string]json.RawMessage
		if err := json.NewDecoder(rec.Body).Decode(&resp); err == nil {
			if _, ok := resp["patches_merged"]; ok {
				t.Error("standard workspace sync should NOT include patches_merged field")
			}
			if _, ok := resp["rebuild_triggered"]; ok {
				t.Error("standard workspace sync should NOT include rebuild_triggered field")
			}
		}
	}
	// It's acceptable for the standard workspace to return any non-error status;
	// the key assertion is that carry-patch fields are absent.
}

// 16-REQ-5.E3: If upstream HEAD has not changed, no patches_merged and no rebuild.
func TestCarryPatchSync_UpstreamUnchanged_NoPatchesMerged(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)
	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	}

	// Mock: upstream HEAD hasn't changed (same as stored).
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		return "aaaa000000000000000000000000000000000001", nil
	}
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/sync", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync (unchanged) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.PatchesMerged) != 0 {
		t.Errorf("expected empty patches_merged, got %v", resp.PatchesMerged)
	}
	if resp.RebuildTriggered {
		t.Error("expected rebuild_triggered=false when upstream unchanged")
	}
}

// ===========================================================================
// TS-16-16: When IsAncestor returns true for a patch branch HEAD against
// the new upstream HEAD, the sync handler transitions that patch to
// 'merged_upstream' and includes its branch name in patches_merged.
//
// Requirement: 16-REQ-5.2
// ===========================================================================

func TestCarryPatchSync_PatchMergedUpstream(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)
	seedPatch(t, env.db, "p1", "my-workspace", "feature/already-merged", 1, PatchStatusActive)
	seedPatch(t, env.db, "p2", "my-workspace", "feature/not-merged", 2, PatchStatusActive)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/already-merged", Position: 1, Status: PatchStatusActive},
		{ID: "p2", WorkspaceID: "my-workspace", BranchName: "feature/not-merged", Position: 2, Status: PatchStatusActive},
	}

	// IsAncestor returns true for feature/already-merged, false for feature/not-merged.
	env.gitRunner.IsAncestorFunc = func(_ context.Context, ancestor, _ string) (bool, error) {
		// The ancestor arg corresponds to the patch branch HEAD.
		// We can't easily distinguish by SHA in mock, so we track by call order.
		return false, nil
	}

	// Use a more sophisticated mock: track which patch is being checked.
	isAncestorCallCount := 0
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		isAncestorCallCount++
		// First call for feature/already-merged: return true (merged).
		if isAncestorCallCount == 1 {
			return true, nil
		}
		// Second call for feature/not-merged: return false.
		return false, nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/sync", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// feature/already-merged should be in patches_merged.
	found := false
	for _, name := range resp.PatchesMerged {
		if name == "feature/already-merged" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'feature/already-merged' in patches_merged, got %v", resp.PatchesMerged)
	}

	// Verify the patch status was transitioned to merged_upstream in the store.
	updated, exists := env.patchStore.UpdatedPatches["p1"]
	if !exists {
		t.Error("expected patch 'p1' to be updated to merged_upstream in store")
	} else if updated.Status != PatchStatusMergedUpstream {
		t.Errorf("expected patch status=%q, got %q", PatchStatusMergedUpstream, updated.Status)
	}
}

// 16-PROP-6: Ancestry-based merge detection is monotonic — once merged_upstream,
// it stays merged_upstream.
func TestCarryPatchSync_MergedUpstreamIsMonotonic(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	// Patch is already merged_upstream — should not be reverted.
	seedPatch(t, env.db, "p1", "my-workspace", "feature/already-merged", 1, PatchStatusMergedUpstream)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/already-merged", Position: 1, Status: PatchStatusMergedUpstream},
	}

	// IsAncestor would hypothetically return false, but merged_upstream
	// should never be reverted.
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/sync", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// The patch should still be merged_upstream — not reverted to active.
	// Check that the store was NOT updated to change it back.
	if updated, exists := env.patchStore.UpdatedPatches["p1"]; exists {
		if updated.Status == PatchStatusActive {
			t.Error("merged_upstream status was reverted to active — violates monotonicity (16-PROP-6)")
		}
	}
}

// 16-REQ-5.E2: If IsAncestor returns error for a patch, skip that patch.
func TestCarryPatchSync_IsAncestorError_SkipsPatch(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)
	seedPatch(t, env.db, "p1", "my-workspace", "feature/missing-ref", 1, PatchStatusActive)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/missing-ref", Position: 1, Status: PatchStatusActive},
	}

	// IsAncestor returns error (branch ref doesn't exist locally).
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return false, context.DeadlineExceeded // simulate error
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/sync", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync (IsAncestor error) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Patch should NOT be in patches_merged (error was skipped).
	for _, name := range resp.PatchesMerged {
		if name == "feature/missing-ref" {
			t.Error("expected 'feature/missing-ref' to NOT be in patches_merged (IsAncestor errored)")
		}
	}

	// Patch status should remain unchanged.
	if _, exists := env.patchStore.UpdatedPatches["p1"]; exists {
		t.Error("expected patch 'p1' status to remain unchanged when IsAncestor errors")
	}
}

// ===========================================================================
// TS-NS-1: Sync response includes rebuild_job_id when a rebuild is triggered.
// When rebuild_triggered=true, rebuild_job_id must be a non-empty string
// matching an actual queued rebuild job. When rebuild_triggered=false,
// rebuild_job_id must be absent.
//
// Requirement: NS-REQ-1
// ===========================================================================

func TestCarryPatchSync_RebuildJobID_Present(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	}

	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/sync", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !resp.RebuildTriggered {
		t.Fatal("expected rebuild_triggered=true when upstream advanced")
	}

	if resp.RebuildJobID == nil || *resp.RebuildJobID == "" {
		t.Fatal("expected non-empty rebuild_job_id when rebuild_triggered=true")
	}

	// Verify the job ID matches an actual queued rebuild job by fetching it.
	jobRec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/rebuilds/"+*resp.RebuildJobID, "", auth)

	if jobRec.Code != http.StatusOK {
		t.Fatalf("GET /rebuilds/%s status = %d; want %d; body = %s",
			*resp.RebuildJobID, jobRec.Code, http.StatusOK, jobRec.Body.String())
	}
}

func TestCarryPatchSync_RebuildJobID_Absent_WhenNoRebuild(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)
	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	}

	// Mock: upstream HEAD hasn't changed.
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		return "aaaa000000000000000000000000000000000001", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/sync", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.RebuildTriggered {
		t.Error("expected rebuild_triggered=false when upstream unchanged")
	}

	if resp.RebuildJobID != nil {
		t.Errorf("expected rebuild_job_id to be nil when rebuild not triggered, got %q", *resp.RebuildJobID)
	}
}

// ===========================================================================
// TS-16-17: After sync completes with patches merged and AUTO_REBUILD_AFTER_SYNC
// ='true', a rebuild job is enqueued and rebuild_triggered=true; if a rebuild
// is already queued, rebuild_triggered=false.
//
// Requirement: 16-REQ-5.3
// ===========================================================================

func TestCarryPatchSync_AutoRebuild_EnqueuesJob(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)
	seedPatch(t, env.db, "p1", "my-workspace", "feature/merged", 1, PatchStatusActive)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/merged", Position: 1, Status: PatchStatusActive},
	}

	// IsAncestor returns true (patch merged upstream).
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return true, nil
	}

	auth := rebuildUserAuth("alice")

	// First sync: should enqueue rebuild job.
	rec1 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/sync", "", auth)

	if rec1.Code != http.StatusOK {
		t.Fatalf("POST /sync (first) status = %d; want %d; body = %s",
			rec1.Code, http.StatusOK, rec1.Body.String())
	}

	var resp1 CarryPatchSyncResponse
	if err := json.NewDecoder(rec1.Body).Decode(&resp1); err != nil {
		t.Fatalf("failed to decode first sync response: %v", err)
	}

	if !resp1.RebuildTriggered {
		t.Error("expected rebuild_triggered=true on first sync with merged patch")
	}

	// Second sync: rebuild already queued, should be rebuild_triggered=false.
	// (16-PROP-7: Duplicate rebuild enqueue is idempotent)
	rec2 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/sync", "", auth)

	if rec2.Code != http.StatusOK {
		t.Fatalf("POST /sync (second) status = %d; want %d; body = %s",
			rec2.Code, http.StatusOK, rec2.Body.String())
	}

	var resp2 CarryPatchSyncResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp2); err != nil {
		t.Fatalf("failed to decode second sync response: %v", err)
	}

	if resp2.RebuildTriggered {
		t.Error("expected rebuild_triggered=false when rebuild already queued (duplicate)")
	}
}

// ===========================================================================
// TS-16-18: When AUTO_REBUILD_AFTER_SYNC is 'false', no rebuild job is
// enqueued after sync regardless of merged patches, and rebuild_triggered=false.
//
// Requirement: 16-REQ-5.4
// ===========================================================================

func TestCarryPatchSync_AutoRebuildDisabled_NoJobEnqueued(t *testing.T) {
	// Use a custom GetVariable that returns 'false' for AUTO_REBUILD_AFTER_SYNC.
	getVar := func(scope, slug, key string) (string, error) {
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "false", nil
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		return "", nil
	}
	env := newFullTestEnvWithGetVariable(t, getVar)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)
	seedPatch(t, env.db, "p1", "my-workspace", "feature/merged", 1, PatchStatusActive)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/merged", Position: 1, Status: PatchStatusActive},
	}

	// IsAncestor returns true (patch merged upstream).
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return true, nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/sync", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync (auto-rebuild disabled) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.RebuildTriggered {
		t.Error("expected rebuild_triggered=false when AUTO_REBUILD_AFTER_SYNC='false'")
	}

	// Verify no rebuild job was enqueued by checking the jobs table.
	var jobCount int
	err := env.db.QueryRow(
		`SELECT COUNT(*) FROM jobs WHERE type='rebuild' AND key='my-workspace'`,
	).Scan(&jobCount)
	if err != nil {
		t.Fatalf("query job count: %v", err)
	}
	if jobCount != 0 {
		t.Errorf("expected 0 rebuild jobs when auto-rebuild disabled, got %d", jobCount)
	}
}
