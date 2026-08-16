package carrypatch

import (
	"context"
	"encoding/json"
	"testing"
)

// ===========================================================================
// TS-16-11: When a cherry-pick produces a conflict and rerere has a recorded
// resolution, rerere auto-stages the resolved files,
// 'git diff --name-only --diff-filter=U' returns empty, and the rebuild
// continues to the next patch recording the resolved patch as 'success'.
//
// Requirement: 16-REQ-3.1
// ===========================================================================

func TestRerereIntegration_AutoResolvesConflict_RebuildContinues(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/foo", Position: 1, Status: PatchStatusActive},
		{ID: "p2", WorkspaceID: "ws1", BranchName: "feature/bar", Position: 2, Status: PatchStatusActive},
	})

	commit1 := "bbbb000000000000000000000000000000000001"
	resultSHA := "cccc000000000000000000000000000000000001"

	cherryPickCallCount := 0
	rerereCalled := false
	diffUnresolvedCalled := false

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for i, arg := range args {
			if arg == "--reverse" {
				return commit1, nil
			}
			// Detect 'git rerere' invocation.
			if arg == "rerere" {
				rerereCalled = true
				return "", nil
			}
			// Detect 'git diff --name-only --diff-filter=U' — returns empty
			// (all conflicts resolved by rerere).
			if arg == "--diff-filter=U" {
				diffUnresolvedCalled = true
				return "", nil // empty = no unresolved conflicts
			}
			// Detect 'git add' (stage resolved files).
			if arg == "add" && i+1 < len(args) {
				return "", nil
			}
		}
		return resultSHA, nil
	}

	// Cherry-pick returns a conflict error on first call (rerere will resolve).
	mock.CherryPickFunc = func(_ context.Context, _ string) error {
		cherryPickCallCount++
		if cherryPickCallCount == 1 {
			// First cherry-pick conflicts — rerere should resolve it.
			return &CherryPickConflictError{Files: []string{"src/config.go"}}
		}
		// Subsequent cherry-picks succeed.
		return nil
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
		t.Fatalf("HandleRebuildJob returned error: %v (expected rerere to resolve conflict)", err)
	}

	rebuildResult, ok := result.(*RebuildResult)
	if !ok {
		t.Fatalf("expected result to be *RebuildResult, got %T", result)
	}

	// Rerere should have been invoked.
	if !rerereCalled {
		t.Error("expected 'git rerere' to be invoked after cherry-pick conflict")
	}

	// Unresolved conflict check should have been called.
	if !diffUnresolvedCalled {
		t.Error("expected 'git diff --name-only --diff-filter=U' to be called")
	}

	// The conflicting patch should be recorded as 'success' because rerere resolved it.
	if len(rebuildResult.PatchResults) < 1 {
		t.Fatal("expected at least 1 patch result")
	}
	pr := rebuildResult.PatchResults[0]
	if pr.Status != "success" {
		t.Errorf("expected patch status='success' after rerere resolution, got %q", pr.Status)
	}
	if len(pr.ConflictFiles) > 0 {
		t.Errorf("expected empty conflict_files after rerere resolution, got %v", pr.ConflictFiles)
	}

	// Rebuild should have continued to the second patch.
	if len(rebuildResult.PatchResults) != 2 {
		t.Fatalf("expected 2 patch results (rebuild continued), got %d", len(rebuildResult.PatchResults))
	}
	if rebuildResult.PatchResults[1].Status != "success" {
		t.Errorf("expected second patch status='success', got %q", rebuildResult.PatchResults[1].Status)
	}

	// Job should complete successfully.
	if rebuildResult.PatchesApplied != 2 {
		t.Errorf("expected patches_applied=2, got %d", rebuildResult.PatchesApplied)
	}
}

// Verify rerere replay is idempotent (16-PROP-5): multiple rebuilds encountering
// the same conflict produce the same result when rerere has a recorded resolution.
func TestRerereIntegration_ReplayIsIdempotent(t *testing.T) {
	for i := 0; i < 3; i++ {
		mock := newMockGitRunner()
		patches := newMockPatchStore([]Patch{
			{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/foo", Position: 1, Status: PatchStatusActive},
		})

		commit1 := "bbbb000000000000000000000000000000000001"
		resultSHA := "cccc000000000000000000000000000000000001"

		mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
			for _, arg := range args {
				if arg == "--reverse" {
					return commit1, nil
				}
				if arg == "rerere" {
					return "", nil
				}
				if arg == "--diff-filter=U" {
					return "", nil // rerere resolved everything
				}
			}
			return resultSHA, nil
		}

		mock.CherryPickFunc = func(_ context.Context, _ string) error {
			return &CherryPickConflictError{Files: []string{"conflict.go"}}
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
			t.Fatalf("iteration %d: HandleRebuildJob returned error: %v", i, err)
		}

		rebuildResult, ok := result.(*RebuildResult)
		if !ok {
			t.Fatalf("iteration %d: expected *RebuildResult, got %T", i, result)
		}

		if len(rebuildResult.PatchResults) < 1 || rebuildResult.PatchResults[0].Status != "success" {
			t.Errorf("iteration %d: expected patch status='success', got result: %+v", i, rebuildResult.PatchResults)
		}
	}
}

// ===========================================================================
// TS-16-12: When rerere cannot resolve all conflicts (unresolved conflict
// markers remain after rerere runs), the executor aborts the cherry-pick,
// sets patch status to 'conflict', records conflict_files, deletes the
// temporary branch, and halts the rebuild.
//
// Requirement: 16-REQ-3.2
// ===========================================================================

func TestRerereIntegration_PartialResolve_AbortsAndRecordsConflict(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p-conflict", WorkspaceID: "ws1", BranchName: "feature/conflict", Position: 1, Status: PatchStatusActive},
		{ID: "p-after", WorkspaceID: "ws1", BranchName: "feature/after", Position: 2, Status: PatchStatusActive},
	})

	commit1 := "bbbb000000000000000000000000000000000001"
	upstreamHead := "aaaa000000000000000000000000000000000001"
	branchDeleteCalled := false

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return commit1, nil
			}
			if arg == "rerere" {
				return "", nil // rerere runs but doesn't resolve everything
			}
			// 'git diff --name-only --diff-filter=U' returns unresolved files.
			if arg == "--diff-filter=U" {
				return "pkg/api.go", nil
			}
			// Detect temp branch deletion.
			if arg == "-D" || arg == "-d" {
				branchDeleteCalled = true
				return "", nil
			}
		}
		return upstreamHead, nil
	}

	mock.CherryPickFunc = func(_ context.Context, _ string) error {
		return &CherryPickConflictError{Files: []string{"pkg/api.go", "pkg/handler.go"}}
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

	_, retryable, err := h.HandleRebuildJob(context.Background(), payloadJSON)

	// Should fail with non-retryable error.
	if err == nil {
		t.Fatal("expected error for unresolved conflict after rerere, got nil")
	}
	if retryable {
		t.Error("expected retryable=false for conflict (permanent failure)")
	}

	// Patch status should be updated to 'conflict' with conflict_files.
	updated, exists := patches.UpdatedPatches["p-conflict"]
	if !exists {
		t.Fatal("expected patch 'p-conflict' to be updated in store")
	}
	if updated.Status != PatchStatusConflict {
		t.Errorf("expected patch status=%q, got %q", PatchStatusConflict, updated.Status)
	}

	// conflict_files should contain only the unresolved files from
	// 'git diff --name-only --diff-filter=U' (not the initial conflict files).
	if len(updated.ConflictFiles) == 0 {
		t.Error("expected non-empty conflict_files for partially-resolved patch")
	}
	foundAPI := false
	for _, f := range updated.ConflictFiles {
		if f == "pkg/api.go" {
			foundAPI = true
		}
	}
	if !foundAPI {
		t.Errorf("expected conflict_files to contain 'pkg/api.go', got %v", updated.ConflictFiles)
	}

	// Temporary branch should be deleted.
	if !branchDeleteCalled {
		t.Error("expected temporary branch to be deleted after conflict abort")
	}

	// Second patch should NOT have been attempted (fail-fast).
	if _, attempted := patches.UpdatedPatches["p-after"]; attempted {
		t.Error("expected second patch to NOT be attempted (fail-fast halt)")
	}
}

// 16-REQ-3.E1: If rerere is not enabled, rebuild proceeds without rerere replay;
// conflicts cause fail-fast halt.
func TestRerereIntegration_NotEnabled_ConflictHalts(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/foo", Position: 1, Status: PatchStatusActive},
	})

	commit1 := "bbbb000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return commit1, nil
			}
			// 'git diff --name-only --diff-filter=U' returns unresolved files.
			if arg == "--diff-filter=U" {
				return "conflict.go", nil
			}
		}
		return "aaaa000000000000000000000000000000000001", nil
	}

	mock.CherryPickFunc = func(_ context.Context, _ string) error {
		return &CherryPickConflictError{Files: []string{"conflict.go"}}
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

	_, _, err := h.HandleRebuildJob(context.Background(), payloadJSON)
	if err == nil {
		t.Fatal("expected error for conflict when rerere is not enabled")
	}

	// Patch should be marked as conflict.
	updated, exists := patches.UpdatedPatches["p1"]
	if !exists {
		t.Fatal("expected patch 'p1' to be updated")
	}
	if updated.Status != PatchStatusConflict {
		t.Errorf("expected status=%q, got %q", PatchStatusConflict, updated.Status)
	}
}

// 16-REQ-3.E2: When rerere partially resolves conflicts (some files resolved,
// others not), conflict_files contains only the unresolved files.
func TestRerereIntegration_PartialResolve_ConflictFilesContainOnlyUnresolved(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/foo", Position: 1, Status: PatchStatusActive},
	})

	commit1 := "bbbb000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return commit1, nil
			}
			if arg == "rerere" {
				return "", nil
			}
			// After rerere, only one file remains unresolved (the other was resolved).
			if arg == "--diff-filter=U" {
				return "still-conflicting.go", nil
			}
		}
		return "aaaa000000000000000000000000000000000001", nil
	}

	// Cherry-pick initially conflicts on two files.
	mock.CherryPickFunc = func(_ context.Context, _ string) error {
		return &CherryPickConflictError{Files: []string{"resolved-by-rerere.go", "still-conflicting.go"}}
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

	_, _, err := h.HandleRebuildJob(context.Background(), payloadJSON)
	if err == nil {
		t.Fatal("expected error for partially resolved conflict")
	}

	updated := patches.UpdatedPatches["p1"]
	// conflict_files should contain ONLY the unresolved file from the
	// diff filter, not the initially-reported conflict files.
	if len(updated.ConflictFiles) != 1 {
		t.Fatalf("expected 1 conflict file (only unresolved), got %d: %v",
			len(updated.ConflictFiles), updated.ConflictFiles)
	}
	if updated.ConflictFiles[0] != "still-conflicting.go" {
		t.Errorf("expected conflict_files=['still-conflicting.go'], got %v", updated.ConflictFiles)
	}
}
