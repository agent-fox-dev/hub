package carrypatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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

	// Use a more sophisticated mock: distinguish by ancestor argument.
	// The force-push check passes the storedSHA as ancestor, while patch
	// merge checks pass the branch name.
	env.gitRunner.IsAncestorFunc = func(_ context.Context, ancestor, _ string) (bool, error) {
		switch ancestor {
		case "aaaa000000000000000000000000000000000001":
			return true, nil // upstream ancestry check: normal advance
		case "feature/already-merged":
			return true, nil // patch merged upstream
		default:
			return false, nil // feature/not-merged: not merged
		}
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

// ===========================================================================
// TS-NS-1: Force-push detection — upstream force-push sets force_push_detected.
//
// Requirement: NS-REQ-1
// ===========================================================================

func TestCarryPatchSync_ForcePushDetected(t *testing.T) {
	env := newFullTestEnv(t)

	storedSHA := "aaaa000000000000000000000000000000000001"
	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		storedSHA,
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	}

	// IsAncestor: return false for the upstream force-push check
	// (storedSHA is NOT an ancestor of newUpstreamHead) and false for patch
	// branch checks.
	env.gitRunner.IsAncestorFunc = func(_ context.Context, ancestor, _ string) (bool, error) {
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

	if !resp.ForcePushDetected {
		t.Error("expected force_push_detected=true when upstream was force-pushed")
	}

	// NS-REQ-3: sync still completes successfully.
	if !resp.RebuildTriggered {
		t.Error("expected rebuild_triggered=true even when force-push detected")
	}
}

// ===========================================================================
// TS-NS-2: Normal fast-forward — force_push_detected is false.
//
// Requirement: NS-REQ-2
// ===========================================================================

func TestCarryPatchSync_NormalAdvance_ForcePushFalse(t *testing.T) {
	env := newFullTestEnv(t)

	storedSHA := "aaaa000000000000000000000000000000000001"
	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		storedSHA,
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	}

	// IsAncestor: storedSHA IS an ancestor of newUpstreamHead (normal advance).
	// The first call is the upstream force-push check (storedSHA vs new HEAD),
	// patch-level calls come after.
	env.gitRunner.IsAncestorFunc = func(_ context.Context, ancestor, _ string) (bool, error) {
		if ancestor == storedSHA {
			return true, nil // normal fast-forward
		}
		return false, nil // patch not merged
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

	if resp.ForcePushDetected {
		t.Error("expected force_push_detected=false for normal fast-forward advance")
	}
}

// ===========================================================================
// TS-NS-4: Empty storedSHA (first sync) — ancestry check skipped,
// force_push_detected is false.
//
// Requirement: NS-REQ-4
// ===========================================================================

func TestCarryPatchSync_EmptyStoredSHA_ForcePushFalse(t *testing.T) {
	env := newFullTestEnv(t)

	// Seed workspace with NULL upstream_head_sha (first sync).
	now := "2024-01-01T00:00:00Z"
	_, err := env.db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, integration_branch, upstream_url, created_at, updated_at)
		 VALUES (?, ?, ?, 'active', 'ready', 'carry_patch', ?, ?, ?, ?)`,
		"my-workspace", "https://github.com/example/repo", "alice", "integration", "https://github.com/example/upstream", now, now,
	)
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	}

	// Track whether IsAncestor is called with empty-string ancestor (it should not be).
	isAncestorCalledWithEmpty := false
	env.gitRunner.IsAncestorFunc = func(_ context.Context, ancestor, _ string) (bool, error) {
		if ancestor == "" {
			isAncestorCalledWithEmpty = true
		}
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

	if resp.ForcePushDetected {
		t.Error("expected force_push_detected=false when storedSHA is empty (first sync)")
	}

	if isAncestorCalledWithEmpty {
		t.Error("IsAncestor should not be called with empty storedSHA for force-push check")
	}
}

// ===========================================================================
// TS-NS-5: IsAncestor error during force-push check — sync proceeds,
// force_push_detected is false (conservative).
//
// Requirement: NS-REQ-5
// ===========================================================================

func TestCarryPatchSync_IsAncestorError_ForcePushFalse(t *testing.T) {
	env := newFullTestEnv(t)

	storedSHA := "aaaa000000000000000000000000000000000001"
	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		storedSHA,
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	}

	// IsAncestor returns error for the force-push check (storedSHA as ancestor),
	// but works for patch-level checks.
	env.gitRunner.IsAncestorFunc = func(_ context.Context, ancestor, _ string) (bool, error) {
		if ancestor == storedSHA {
			return false, fmt.Errorf("git error: ambiguous ref")
		}
		return false, nil // patch not merged
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

	if resp.ForcePushDetected {
		t.Error("expected force_push_detected=false when IsAncestor returns error (conservative)")
	}

	// Sync should still complete normally.
	if !resp.RebuildTriggered {
		t.Error("expected rebuild_triggered=true even when IsAncestor errors for force-push check")
	}
}

// ===========================================================================
// TS-02-9 (unit): Only patches with status active, conflict or disabled are
// considered, in position order, excluding merged_upstream, deleted and the
// integration branch
//
// Verifies: 02-REQ-3.1
// ===========================================================================

func TestCarryPatchSync_TS02_9_OnlyActiveConflictDisabledConsidered(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-9"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"upstream0",
		"integration",
		"upstream0",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	// 6 patches in specific order:
	// pos 1: feat-active (active) -> considered
	// pos 2: integration (active) -> skipped because branch_name == integration_branch
	// pos 3: feat-conflict (conflict) -> considered
	// pos 4: feat-merged (merged_upstream) -> skipped
	// pos 5: feat-disabled (disabled) -> considered
	// pos 6: feat-deleted (deleted) -> skipped
	patches := []Patch{
		{ID: "p1", WorkspaceID: slug, BranchName: "feat-active", Position: 1, Status: PatchStatusActive},
		{ID: "p2", WorkspaceID: slug, BranchName: "integration", Position: 2, Status: PatchStatusActive},
		{ID: "p3", WorkspaceID: slug, BranchName: "feat-conflict", Position: 3, Status: PatchStatusConflict},
		{ID: "p4", WorkspaceID: slug, BranchName: "feat-merged", Position: 4, Status: PatchStatusMergedUpstream},
		{ID: "p5", WorkspaceID: slug, BranchName: "feat-disabled", Position: 5, Status: PatchStatusDisabled},
		{ID: "p6", WorkspaceID: slug, BranchName: "feat-deleted", Position: 6, Status: PatchStatusDeleted},
	}
	env.patchStore.Patches = patches

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			if args[2] == "refs/remotes/upstream/HEAD" {
				return "upstream0", nil
			}
			return "c111", nil
		}
		return "", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	expectedBranches := []string{"feat-active", "feat-conflict", "feat-disabled"}
	if len(resp.PatchesSynced) != len(expectedBranches) {
		t.Fatalf("expected %d patches synced, got %d: %+v", len(expectedBranches), len(resp.PatchesSynced), resp.PatchesSynced)
	}
	for i, exp := range expectedBranches {
		if resp.PatchesSynced[i].BranchName != exp {
			t.Errorf("patches_synced[%d] branch_name = %q; want %q", i, resp.PatchesSynced[i].BranchName, exp)
		}
	}
}

// ===========================================================================
// TS-02-10 (unit): A patch missing on origin leaves the local branch untouched,
// records missing_on_origin without changing status, and persists the state
//
// Verifies: 02-REQ-3.2, 02-REQ-6.2
// ===========================================================================

func TestCarryPatchSync_TS02_10_MissingOnOrigin(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-10"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"upstream0",
		"integration",
		"upstream0",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
		}
		return "", nil
	}

	patch := Patch{ID: "p10", WorkspaceID: slug, BranchName: "feature-missing", Position: 1, Status: PatchStatusActive}
	env.patchStore.Patches = []Patch{patch}
	seedPatch(t, env.db, "p10", slug, "feature-missing", 1, PatchStatusActive)

	var updateRefCalledForMissing bool
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			if args[2] == "refs/remotes/upstream/HEAD" {
				return "upstream0", nil
			}
			if args[2] == "refs/remotes/origin/feature-missing" {
				return "", fmt.Errorf("fatal: Needed a single revision")
			}
			if args[2] == "refs/heads/feature-missing" {
				return "local111", nil
			}
		}
		if len(args) >= 2 && args[0] == "update-ref" && args[1] == "refs/heads/feature-missing" {
			updateRefCalledForMissing = true
		}
		return "", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	if updateRefCalledForMissing {
		t.Error("expected refs/heads/feature-missing not to be updated via update-ref")
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	var found *PatchSyncEntry
	for i := range resp.PatchesSynced {
		if resp.PatchesSynced[i].BranchName == "feature-missing" {
			found = &resp.PatchesSynced[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected feature-missing in patches_synced: %+v", resp.PatchesSynced)
	}
	if found.State != "missing_on_origin" {
		t.Errorf("entry.State = %q; want 'missing_on_origin'", found.State)
	}

	// Verify patch status stays active
	dbPatches, _ := env.patchStore.ListPatches(context.Background(), slug)
	if len(dbPatches) == 0 || dbPatches[0].Status != PatchStatusActive {
		t.Errorf("patch status changed; want 'active', got %v", dbPatches)
	}

	// Verify persisted origin_sync_state, origin_sha, origin_synced_at
	call, ok := env.patchStore.OriginSyncStates["p10"]
	if !ok {
		t.Fatal("expected SetOriginSyncState to be called for p10")
	}
	if call.State == nil || *call.State != "missing_on_origin" {
		t.Errorf("persisted state = %v; want 'missing_on_origin'", call.State)
	}
	if call.SHA != nil {
		t.Errorf("persisted sha = %v; want nil", call.SHA)
	}
	if call.SyncedAt == "" {
		t.Error("persisted syncedAt is empty; want non-empty timestamp")
	}
}

// ===========================================================================
// TS-02-11 (unit): A patch branch missing locally but present on origin is
// created at the origin tip via update-ref and counts as patch advanced
//
// Verifies: 02-REQ-3.3, 02-REQ-4.1
// ===========================================================================

func TestCarryPatchSync_TS02_11_MissingLocallyCreated(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-11"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"upstream0",
		"integration",
		"upstream0",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	patch := Patch{ID: "p11", WorkspaceID: slug, BranchName: "feature-x", Position: 1, Status: PatchStatusActive}
	env.patchStore.Patches = []Patch{patch}

	originSHA := "originX111"
	var createdWithUpdateRef bool
	refs := map[string]string{
		"refs/remotes/upstream/HEAD":      "upstream0",
		"refs/remotes/origin/feature-x": originSHA,
	}

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			ref := args[2]
			if sha, ok := refs[ref]; ok {
				return sha, nil
			}
			return "", fmt.Errorf("fatal: Needed a single revision")
		}
		if len(args) >= 3 && args[0] == "update-ref" {
			ref := args[1]
			sha := args[2]
			refs[ref] = sha
			if ref == "refs/heads/feature-x" && sha == originSHA {
				createdWithUpdateRef = true
			}
			return "", nil
		}
		return "", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	if !createdWithUpdateRef {
		t.Error("expected update-ref to create refs/heads/feature-x at origin tip")
	}
	if refs["refs/heads/feature-x"] != originSHA {
		t.Errorf("refs/heads/feature-x = %q; want %q", refs["refs/heads/feature-x"], originSHA)
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	var found *PatchSyncEntry
	for i := range resp.PatchesSynced {
		if resp.PatchesSynced[i].BranchName == "feature-x" {
			found = &resp.PatchesSynced[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected feature-x in patches_synced: %+v", resp.PatchesSynced)
	}
	if found.Action != "created" {
		t.Errorf("entry.Action = %q; want 'created'", found.Action)
	}
	if !resp.RebuildTriggered {
		t.Error("expected rebuild_triggered=true when branch is created (counts as patch advanced)")
	}
}

// ===========================================================================
// TS-02-12 (unit): A patch whose local and origin tips are the same commit is
// recorded in_sync with no ref write
//
// Verifies: 02-REQ-3.4
// ===========================================================================

func TestCarryPatchSync_TS02_12_InSyncNoRefWrite(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-12"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"upstream0",
		"integration",
		"upstream0",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
		}
		return "", nil
	}

	patch := Patch{ID: "p12", WorkspaceID: slug, BranchName: "feature-y", Position: 1, Status: PatchStatusActive}
	env.patchStore.Patches = []Patch{patch}

	shaY := "shaY222"
	var updateRefCalled bool
	refs := map[string]string{
		"refs/remotes/upstream/HEAD":      "upstream0",
		"refs/remotes/origin/feature-y": shaY,
		"refs/heads/feature-y":          shaY,
	}

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			ref := args[2]
			if sha, ok := refs[ref]; ok {
				return sha, nil
			}
			return "", fmt.Errorf("fatal: Needed a single revision")
		}
		if len(args) >= 2 && args[0] == "update-ref" && args[1] == "refs/heads/feature-y" {
			updateRefCalled = true
			return "", nil
		}
		return "", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	if updateRefCalled {
		t.Error("expected no update-ref call when local and origin tips match")
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	var found *PatchSyncEntry
	for i := range resp.PatchesSynced {
		if resp.PatchesSynced[i].BranchName == "feature-y" {
			found = &resp.PatchesSynced[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected feature-y in patches_synced: %+v", resp.PatchesSynced)
	}
	if found.State != "in_sync" {
		t.Errorf("entry.State = %q; want 'in_sync'", found.State)
	}
	if found.Action != "none" {
		t.Errorf("entry.Action = %q; want 'none'", found.Action)
	}
}

// ===========================================================================
// TS-02-13 (unit): A local tip that is an ancestor of the origin tip is
// fast-forwarded via update-ref using GitRunner.IsAncestor on resolved SHAs
//
// Verifies: 02-REQ-3.5, 02-REQ-4.1
// ===========================================================================

func TestCarryPatchSync_TS02_13_FastForwardViaIsAncestor(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-13"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"upstream0",
		"integration",
		"upstream0",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	patch := Patch{ID: "p13", WorkspaceID: slug, BranchName: "feature-z", Position: 1, Status: PatchStatusActive}
	env.patchStore.Patches = []Patch{patch}

	shaA := "shaA333"
	shaB := "shaB333"
	refs := map[string]string{
		"refs/remotes/upstream/HEAD":      "upstream0",
		"refs/heads/feature-z":          shaA,
		"refs/remotes/origin/feature-z": shaB,
	}

	var isAncestorCalledWithResolvedSHAs bool
	env.gitRunner.IsAncestorFunc = func(_ context.Context, ancestor, descendant string) (bool, error) {
		if ancestor == shaA && descendant == shaB {
			isAncestorCalledWithResolvedSHAs = true
			return true, nil
		}
		return false, nil
	}

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			ref := args[2]
			if sha, ok := refs[ref]; ok {
				return sha, nil
			}
			return "", fmt.Errorf("fatal: Needed a single revision")
		}
		if len(args) >= 3 && args[0] == "update-ref" {
			refs[args[1]] = args[2]
			return "", nil
		}
		return "", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	if !isAncestorCalledWithResolvedSHAs {
		t.Error("expected IsAncestor to be called with resolved SHAs shaA and shaB")
	}
	if refs["refs/heads/feature-z"] != shaB {
		t.Errorf("refs/heads/feature-z = %q; want %q", refs["refs/heads/feature-z"], shaB)
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	var found *PatchSyncEntry
	for i := range resp.PatchesSynced {
		if resp.PatchesSynced[i].BranchName == "feature-z" {
			found = &resp.PatchesSynced[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected feature-z in patches_synced: %+v", resp.PatchesSynced)
	}
	if found.Action != "fast_forwarded" {
		t.Errorf("entry.Action = %q; want 'fast_forwarded'", found.Action)
	}
	if !resp.RebuildTriggered {
		t.Error("expected rebuild_triggered=true when branch is fast-forwarded (counts as patch advanced)")
	}
}

// ===========================================================================
// TS-02-14 (unit): A diverged branch under replace policy is backed up to
// refs/hub/replaced, moved to the origin tip, and recorded as replaced with both SHAs
//
// Verifies: 02-REQ-3.6, 02-REQ-4.1
// ===========================================================================

func TestCarryPatchSync_TS02_14_DivergedReplacePolicy(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-14"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"upstream0",
		"integration",
		"upstream0",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			if key == "PATCH_DIVERGENCE_POLICY" {
				return "replace", nil
			}
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
		}
		return "", nil
	}

	patch := Patch{ID: "p14", WorkspaceID: slug, BranchName: "feature-w", Position: 1, Status: PatchStatusActive}
	env.patchStore.Patches = []Patch{patch}

	shaA := "shaA444"
	shaB := "shaB444"
	priorBackup := "priorOldBackup"
	refs := map[string]string{
		"refs/remotes/upstream/HEAD":         "upstream0",
		"refs/heads/feature-w":             shaA,
		"refs/remotes/origin/feature-w":    shaB,
		"refs/hub/replaced/feature-w":      priorBackup,
	}

	// Neither is an ancestor of the other
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			ref := args[2]
			if sha, ok := refs[ref]; ok {
				return sha, nil
			}
			return "", fmt.Errorf("fatal: Needed a single revision")
		}
		if len(args) >= 3 && args[0] == "update-ref" {
			refs[args[1]] = args[2]
			return "", nil
		}
		return "", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	if refs["refs/hub/replaced/feature-w"] != shaA {
		t.Errorf("refs/hub/replaced/feature-w = %q; want %q", refs["refs/hub/replaced/feature-w"], shaA)
	}
	if refs["refs/heads/feature-w"] != shaB {
		t.Errorf("refs/heads/feature-w = %q; want %q", refs["refs/heads/feature-w"], shaB)
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	var found *PatchSyncEntry
	for i := range resp.PatchesSynced {
		if resp.PatchesSynced[i].BranchName == "feature-w" {
			found = &resp.PatchesSynced[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected feature-w in patches_synced: %+v", resp.PatchesSynced)
	}
	if found.Action != "replaced" {
		t.Errorf("entry.Action = %q; want 'replaced'", found.Action)
	}
	if found.LocalSHA != shaA {
		t.Errorf("entry.LocalSHA = %q; want %q", found.LocalSHA, shaA)
	}
	if found.OriginSHA != shaB {
		t.Errorf("entry.OriginSHA = %q; want %q", found.OriginSHA, shaB)
	}
	if found.ReplacedSHA != shaA {
		t.Errorf("entry.ReplacedSHA = %q; want %q", found.ReplacedSHA, shaA)
	}
	if !resp.RebuildTriggered {
		t.Error("expected rebuild_triggered=true when branch is replaced (counts as patch advanced)")
	}
}

// ===========================================================================
// TS-02-15 (unit): A diverged branch under report policy is left untouched
// and recorded diverged with both SHAs
//
// Verifies: 02-REQ-3.7
// ===========================================================================

func TestCarryPatchSync_TS02_15_DivergedReportPolicy(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-15"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"upstream0",
		"integration",
		"upstream0",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			if key == "PATCH_DIVERGENCE_POLICY" {
				return "report", nil
			}
		}
		return "", nil
	}

	patch := Patch{ID: "p15", WorkspaceID: slug, BranchName: "feature-v", Position: 1, Status: PatchStatusActive}
	env.patchStore.Patches = []Patch{patch}

	shaA := "shaA555"
	shaB := "shaB555"
	refs := map[string]string{
		"refs/remotes/upstream/HEAD":      "upstream0",
		"refs/heads/feature-v":          shaA,
		"refs/remotes/origin/feature-v": shaB,
	}

	// Neither is ancestor of the other
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}

	var updateRefCalled bool
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			ref := args[2]
			if sha, ok := refs[ref]; ok {
				return sha, nil
			}
			return "", fmt.Errorf("fatal: Needed a single revision")
		}
		if len(args) >= 2 && args[0] == "update-ref" {
			updateRefCalled = true
			return "", nil
		}
		return "", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	if updateRefCalled {
		t.Error("expected no update-ref call under report divergence policy")
	}
	if refs["refs/heads/feature-v"] != shaA {
		t.Errorf("refs/heads/feature-v = %q; want %q", refs["refs/heads/feature-v"], shaA)
	}
	if _, ok := refs["refs/hub/replaced/feature-v"]; ok {
		t.Error("expected no backup ref under report divergence policy")
	}

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}

	var found *PatchSyncEntry
	for i := range resp.PatchesSynced {
		if resp.PatchesSynced[i].BranchName == "feature-v" {
			found = &resp.PatchesSynced[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected feature-v in patches_synced: %+v", resp.PatchesSynced)
	}
	if found.State != "diverged" {
		t.Errorf("entry.State = %q; want 'diverged'", found.State)
	}
	if found.LocalSHA != shaA {
		t.Errorf("entry.LocalSHA = %q; want %q", found.LocalSHA, shaA)
	}
	if found.OriginSHA != shaB {
		t.Errorf("entry.OriginSHA = %q; want %q", found.OriginSHA, shaB)
	}
}

// ===========================================================================
// TS-02-16 (unit): Moving the branch that is the trunk's current checkout runs
// git reset --hard to the new tip immediately after the ref move
//
// Verifies: 02-REQ-3.8
// ===========================================================================

func TestCarryPatchSync_TS02_16_CheckedOutBranchResetHard(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-16"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"upstream0",
		"integration",
		"upstream0",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
		}
		return "", nil
	}

	patch := Patch{ID: "p16", WorkspaceID: slug, BranchName: "feature-checked-out", Position: 1, Status: PatchStatusActive}
	env.patchStore.Patches = []Patch{patch}

	shaA := "shaA666"
	shaB := "shaB666"
	refs := map[string]string{
		"refs/remotes/upstream/HEAD":                "upstream0",
		"refs/heads/feature-checked-out":          shaA,
		"refs/remotes/origin/feature-checked-out": shaB,
	}

	env.gitRunner.IsAncestorFunc = func(_ context.Context, ancestor, descendant string) (bool, error) {
		if ancestor == shaA && descendant == shaB {
			return true, nil
		}
		return false, nil
	}

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			ref := args[2]
			if sha, ok := refs[ref]; ok {
				return sha, nil
			}
			return "", fmt.Errorf("fatal: Needed a single revision")
		}
		if len(args) >= 3 && args[0] == "update-ref" {
			refs[args[1]] = args[2]
			return "", nil
		}
		if len(args) >= 3 && args[0] == "symbolic-ref" && args[1] == "--short" {
			return "feature-checked-out", nil
		}
		return "", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Verify that update-ref for feature-checked-out occurred before reset --hard shaB
	updateRefIdx := -1
	hardResetIdx := -1
	for i, call := range env.gitRunner.AllCalls {
		if strings.HasPrefix(call, "update-ref refs/heads/feature-checked-out "+shaB) {
			updateRefIdx = i
		}
		if call == "reset --hard "+shaB {
			hardResetIdx = i
		}
	}

	if updateRefIdx == -1 {
		t.Errorf("expected 'update-ref refs/heads/feature-checked-out %s' in calls, got: %v", shaB, env.gitRunner.AllCalls)
	}
	if hardResetIdx == -1 {
		t.Errorf("expected 'reset --hard %s' in calls, got: %v", shaB, env.gitRunner.AllCalls)
	}
	if updateRefIdx != -1 && hardResetIdx != -1 && updateRefIdx >= hardResetIdx {
		t.Errorf("expected update-ref (idx %d) to happen before reset --hard (idx %d)", updateRefIdx, hardResetIdx)
	}
}

// ===========================================================================
// TS-02-27 (unit): Switching to hub mode clears origin_sync_state, origin_sha
// and origin_synced_at to null for every non-deleted patch of the workspace
//
// Verifies: 02-REQ-6.3
// ===========================================================================

func TestCarryPatchSync_TS02_27_HubModeClearsOriginSyncState(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-27"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"upstream0",
		"integration",
		"upstream0",
	)

	// PATCH_BRANCH_SOURCE is unset -> resolves to hub
	env.getVariable = func(scope, scopeID, key string) (string, error) {
		return "", nil
	}

	stateInSync := "in_sync"
	sha1 := "sha111"
	syncedAt1 := "2026-01-01T00:00:00Z"

	stateDiverged := "diverged"
	sha2 := "sha222"
	syncedAt2 := "2026-01-01T00:00:00Z"

	deletedAt := "2026-01-01T00:00:00Z"
	priorDeletedState := "missing_on_origin"
	priorDeletedSHA := "sha333"

	// Seed patches in DB
	seedPatch(t, env.db, "p27-1", slug, "branch-1", 1, PatchStatusActive)
	seedPatch(t, env.db, "p27-2", slug, "branch-2", 2, PatchStatusConflict)
	seedPatch(t, env.db, "p27-3", slug, "branch-3", 3, PatchStatusDeleted)

	_, _ = env.db.Exec(`UPDATE patches SET origin_sync_state = ?, origin_sha = ?, origin_synced_at = ? WHERE id = 'p27-1'`, stateInSync, sha1, syncedAt1)
	_, _ = env.db.Exec(`UPDATE patches SET origin_sync_state = ?, origin_sha = ?, origin_synced_at = ? WHERE id = 'p27-2'`, stateDiverged, sha2, syncedAt2)
	_, _ = env.db.Exec(`UPDATE patches SET origin_sync_state = ?, origin_sha = ?, origin_synced_at = ? WHERE id = 'p27-3'`, priorDeletedState, priorDeletedSHA, syncedAt2)

	// Update mock store
	env.patchStore.Patches = []Patch{
		{ID: "p27-1", WorkspaceID: slug, BranchName: "branch-1", Position: 1, Status: PatchStatusActive, OriginSyncState: &stateInSync, OriginSHA: &sha1, OriginSyncedAt: &syncedAt1},
		{ID: "p27-2", WorkspaceID: slug, BranchName: "branch-2", Position: 2, Status: PatchStatusConflict, OriginSyncState: &stateDiverged, OriginSHA: &sha2, OriginSyncedAt: &syncedAt2},
		{ID: "p27-3", WorkspaceID: slug, BranchName: "branch-3", Position: 3, Status: PatchStatusDeleted, DeletedAt: &deletedAt, OriginSyncState: &priorDeletedState, OriginSHA: &priorDeletedSHA, OriginSyncedAt: &syncedAt2},
	}

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			return "upstream0", nil
		}
		return "", nil
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Verify in DB that non-deleted patches have origin columns cleared to NULL
	var p1State, p1SHA, p1Synced *string
	err := env.db.QueryRow("SELECT origin_sync_state, origin_sha, origin_synced_at FROM patches WHERE id = 'p27-1'").Scan(&p1State, &p1SHA, &p1Synced)
	if err != nil {
		t.Fatalf("query p27-1 failed: %v", err)
	}
	if p1State != nil || p1SHA != nil || p1Synced != nil {
		t.Errorf("p27-1 not cleared: state=%v sha=%v syncedAt=%v", p1State, p1SHA, p1Synced)
	}

	var p2State, p2SHA, p2Synced *string
	err = env.db.QueryRow("SELECT origin_sync_state, origin_sha, origin_synced_at FROM patches WHERE id = 'p27-2'").Scan(&p2State, &p2SHA, &p2Synced)
	if err != nil {
		t.Fatalf("query p27-2 failed: %v", err)
	}
	if p2State != nil || p2SHA != nil || p2Synced != nil {
		t.Errorf("p27-2 not cleared: state=%v sha=%v syncedAt=%v", p2State, p2SHA, p2Synced)
	}

	// Verify soft-deleted patch p27-3 was left untouched
	var p3State, p3SHA, p3Synced *string
	err = env.db.QueryRow("SELECT origin_sync_state, origin_sha, origin_synced_at FROM patches WHERE id = 'p27-3'").Scan(&p3State, &p3SHA, &p3Synced)
	if err != nil {
		t.Fatalf("query p27-3 failed: %v", err)
	}
	if p3State == nil || *p3State != priorDeletedState {
		t.Errorf("p27-3 state was modified; want %q, got %v", priorDeletedState, p3State)
	}
	if p3SHA == nil || *p3SHA != priorDeletedSHA {
		t.Errorf("p27-3 sha was modified; want %q, got %v", priorDeletedSHA, p3SHA)
	}
}

