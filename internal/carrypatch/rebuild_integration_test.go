package carrypatch

import (
	"context"
	"encoding/json"
	"testing"
)

// ===========================================================================
// TS-16-2: Rebuild job executor fetches from upstream, applies all
// active/conflict patches in position order, force-updates the integration
// branch, updates integration_head_sha, deletes the temporary branch,
// removes merged_upstream patches, and compacts positions on success.
//
// Requirement: 16-REQ-1.2
// ===========================================================================

func TestRebuildExecutor_FullSuccessPath(t *testing.T) {
	mock := newMockGitRunner()

	// 2 active patches + 1 merged_upstream patch.
	patches := newMockPatchStore([]Patch{
		{ID: "p-active1", WorkspaceID: "ws1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p-active2", WorkspaceID: "ws1", BranchName: "feature/b", Position: 2, Status: PatchStatusActive},
		{ID: "p-merged", WorkspaceID: "ws1", BranchName: "feature/merged", Position: 3, Status: PatchStatusMergedUpstream},
	})

	integrationHead := "ffff000000000000000000000000000000000001"

	// Mock: git log returns one commit per patch.
	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return "cccc000000000000000000000000000000000001", nil
			}
		}
		// Default: return HEAD SHA. For rev-parse after cherry-picks, this
		// would be the final integration head.
		return integrationHead, nil
	}
	mock.CherryPickFunc = func(_ context.Context, _ string) error { return nil }

	fetchCalled := false
	h := &RebuildHandler{
		PatchStore:   patches,
		NewGitRunner: func(_ string) (GitRunner, error) { return mock, nil },
		Fetch: func(_ context.Context, _ string) error {
			fetchCalled = true
			return nil
		},
		ResolveAuth: func(_ string) error { return nil },
	}

	payload := RebuildPayload{
		WorkspaceSlug: "ws1",
		Strategy:      StrategyRebase,
		SubmittedBy:   "operator",
	}
	payloadJSON, _ := json.Marshal(payload)

	result, retryable, err := h.HandleRebuildJob(context.Background(), payloadJSON)
	if err != nil {
		t.Fatalf("HandleRebuildJob returned error: %v", err)
	}
	if retryable {
		t.Error("expected retryable=false on success")
	}

	// Verify fetch was called (upstream fetch).
	if !fetchCalled {
		t.Error("expected Fetch to be called for upstream fetch")
	}

	// Verify result structure.
	rebuildResult, ok := result.(*RebuildResult)
	if !ok {
		t.Fatalf("expected result to be *RebuildResult, got %T", result)
	}

	// patches_applied should be 2 (the two active patches).
	if rebuildResult.PatchesApplied != 2 {
		t.Errorf("expected patches_applied=2, got %d", rebuildResult.PatchesApplied)
	}

	// patches_skipped should be 1 (the merged_upstream patch).
	if rebuildResult.PatchesSkipped != 1 {
		t.Errorf("expected patches_skipped=1, got %d", rebuildResult.PatchesSkipped)
	}

	// patches_removed should be 1 (the merged_upstream patch deleted after success).
	if rebuildResult.PatchesRemoved != 1 {
		t.Errorf("expected patches_removed=1, got %d", rebuildResult.PatchesRemoved)
	}

	// upstream_head_sha should be set.
	if rebuildResult.UpstreamHeadSHA == "" {
		t.Error("expected non-empty upstream_head_sha")
	}

	// integration_head_sha should be set.
	if rebuildResult.IntegrationHeadSHA == "" {
		t.Error("expected non-empty integration_head_sha")
	}

	// strategy should match payload.
	if rebuildResult.Strategy != StrategyRebase {
		t.Errorf("expected strategy=%q, got %q", StrategyRebase, rebuildResult.Strategy)
	}

	// Verify per-patch results.
	if len(rebuildResult.PatchResults) != 3 {
		t.Fatalf("expected 3 patch results, got %d", len(rebuildResult.PatchResults))
	}

	// Active patches should be 'success'.
	for _, pr := range rebuildResult.PatchResults {
		switch pr.BranchName {
		case "feature/a", "feature/b":
			if pr.Status != "success" {
				t.Errorf("expected patch %q status='success', got %q", pr.BranchName, pr.Status)
			}
			if pr.NewHeadSHA == nil {
				t.Errorf("expected non-nil new_head_sha for patch %q", pr.BranchName)
			}
		case "feature/merged":
			if pr.Status != "skipped" {
				t.Errorf("expected patch %q status='skipped', got %q", pr.BranchName, pr.Status)
			}
			if pr.NewHeadSHA != nil {
				t.Errorf("expected null new_head_sha for skipped patch %q", pr.BranchName)
			}
		default:
			t.Errorf("unexpected patch branch %q in results", pr.BranchName)
		}
	}

	// Verify merged_upstream patch was soft-deleted (not hard-deleted).
	if len(patches.SoftDeletedPatches) != 1 {
		t.Fatalf("expected 1 soft-deleted patch (merged_upstream), got %d", len(patches.SoftDeletedPatches))
	}
	if patches.SoftDeletedPatches[0] != "p-merged" {
		t.Errorf("expected soft-deleted patch id='p-merged', got %q", patches.SoftDeletedPatches[0])
	}
	if len(patches.DeletedPatches) != 0 {
		t.Errorf("expected 0 hard-deleted patches, got %d", len(patches.DeletedPatches))
	}

	// Verify positions were compacted.
	if !patches.Compacted {
		t.Error("expected CompactPositions to be called after successful rebuild")
	}
}

// Test: when all patches are merged_upstream/disabled (no active patches
// to apply), the rebuild succeeds with patches_applied=0 and
// integration_branch set to upstream HEAD.
// Edge case: 16-REQ-1.E7
func TestRebuildExecutor_NoActivePatchesAtExecutionTime_Succeeds(t *testing.T) {
	mock := newMockGitRunner()

	// Only merged_upstream and disabled patches (no active at execution time).
	// Note: the enqueue handler checks for active patches, but by execution
	// time they might all have been transitioned.
	patches := newMockPatchStore([]Patch{
		{ID: "p-merged", WorkspaceID: "ws1", BranchName: "feature/merged", Position: 1, Status: PatchStatusMergedUpstream},
		{ID: "p-disabled", WorkspaceID: "ws1", BranchName: "feature/disabled", Position: 2, Status: PatchStatusDisabled},
	})

	upstreamHead := "aaaa000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, _ ...string) (string, error) {
		return upstreamHead, nil
	}

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

	// patches_applied should be 0.
	if rebuildResult.PatchesApplied != 0 {
		t.Errorf("expected patches_applied=0, got %d", rebuildResult.PatchesApplied)
	}

	// integration_head_sha should equal upstream_head_sha when no patches applied.
	if rebuildResult.IntegrationHeadSHA != rebuildResult.UpstreamHeadSHA {
		t.Errorf("expected integration_head_sha=%q to equal upstream_head_sha=%q when no patches applied",
			rebuildResult.IntegrationHeadSHA, rebuildResult.UpstreamHeadSHA)
	}

	// merged_upstream patches should be soft-deleted (not hard-deleted).
	if len(patches.SoftDeletedPatches) != 1 {
		t.Errorf("expected 1 soft-deleted patch, got %d", len(patches.SoftDeletedPatches))
	}
}

// Test: merged_upstream patches are deleted and remaining positions compacted
// after successful rebuild (16-PROP-4).
func TestRebuildExecutor_MergedPatchesCleanedUp(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", WorkspaceID: "ws1", BranchName: "feature/merged1", Position: 2, Status: PatchStatusMergedUpstream},
		{ID: "p3", WorkspaceID: "ws1", BranchName: "feature/b", Position: 3, Status: PatchStatusActive},
		{ID: "p4", WorkspaceID: "ws1", BranchName: "feature/merged2", Position: 4, Status: PatchStatusMergedUpstream},
	})

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return "bbbb000000000000000000000000000000000001", nil
			}
		}
		return "aaaa000000000000000000000000000000000001", nil
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

	// patches_removed should be 2 (both merged_upstream patches).
	if rebuildResult.PatchesRemoved != 2 {
		t.Errorf("expected patches_removed=2, got %d", rebuildResult.PatchesRemoved)
	}

	// Both merged_upstream patches should have been soft-deleted.
	if len(patches.SoftDeletedPatches) != 2 {
		t.Fatalf("expected 2 soft-deleted patches, got %d", len(patches.SoftDeletedPatches))
	}

	// Verify specific patches were soft-deleted.
	deletedSet := make(map[string]bool)
	for _, id := range patches.SoftDeletedPatches {
		deletedSet[id] = true
	}
	if !deletedSet["p2"] {
		t.Error("expected patch 'p2' (merged1) to be soft-deleted")
	}
	if !deletedSet["p4"] {
		t.Error("expected patch 'p4' (merged2) to be soft-deleted")
	}

	// Verify no hard deletes occurred.
	if len(patches.DeletedPatches) != 0 {
		t.Errorf("expected 0 hard-deleted patches, got %d", len(patches.DeletedPatches))
	}

	// Positions should have been compacted.
	if !patches.Compacted {
		t.Error("expected CompactPositions to be called after successful rebuild")
	}
}

// Test: temporary branch is always cleaned up (16-PROP-9).
// On success, the temp branch should be deleted.
func TestRebuildExecutor_TempBranchDeleted_OnSuccess(t *testing.T) {
	mock := newMockGitRunner()
	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	})

	// Track whether branch delete was called.
	branchDeleteCalled := false
	originalRunFunc := mock.RunFunc
	mock.RunFunc = func(ctx context.Context, args ...string) (string, error) {
		// Detect branch -D or branch -d calls (temp branch deletion).
		if len(args) >= 2 && args[0] == "branch" && (args[1] == "-D" || args[1] == "-d") {
			branchDeleteCalled = true
			return "", nil
		}
		for _, arg := range args {
			if arg == "--reverse" {
				return "bbbb000000000000000000000000000000000001", nil
			}
		}
		return originalRunFunc(ctx, args...)
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

	_, _, err := h.HandleRebuildJob(context.Background(), payloadJSON)
	if err != nil {
		t.Fatalf("HandleRebuildJob returned error: %v", err)
	}

	if !branchDeleteCalled {
		t.Error("expected temporary branch to be deleted after successful rebuild")
	}
}

// Test: integration_head_sha is set in the rebuild result.
func TestRebuildExecutor_IntegrationHeadSHA_Updated(t *testing.T) {
	mock := newMockGitRunner()
	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	})

	expectedSHA := "ffff000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return "bbbb000000000000000000000000000000000001", nil
			}
		}
		return expectedSHA, nil
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

	if rebuildResult.IntegrationHeadSHA == "" {
		t.Error("expected non-empty integration_head_sha in result")
	}
}
