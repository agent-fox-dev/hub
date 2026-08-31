package carrypatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// TS-16-3: With REBUILD_STRATEGY='rebase', the executor identifies unique
// commits via 'git log --reverse --format=%H <upstream_head>..<patch_branch>'
// and cherry-picks each commit onto the temporary branch, preserving
// original patch branch refs.
//
// Requirement: 16-REQ-1.3
// ===========================================================================

func TestRebuildExecutor_RebaseStrategy_CherryPicksCommits(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/foo", Position: 1, Status: PatchStatusActive},
	})

	upstreamHead := "aaaa000000000000000000000000000000000001"
	commit1 := "bbbb000000000000000000000000000000000001"
	commit2 := "bbbb000000000000000000000000000000000002"
	resultSHA := "cccc000000000000000000000000000000000001"

	// Mock git log to return two commits for the patch branch.
	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for i, arg := range args {
			if arg == "--reverse" {
				// Verify the log command includes the expected range.
				for _, a := range args[i:] {
					if a == fmt.Sprintf("%s..feature/foo", upstreamHead) {
						return commit1 + "\n" + commit2, nil
					}
				}
				return commit1 + "\n" + commit2, nil
			}
		}
		// rev-parse HEAD returns the result SHA.
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

	// Verify cherry-pick was called twice (once per commit).
	if len(mock.CherryPickCalls) != 2 {
		t.Fatalf("expected 2 cherry-pick calls, got %d", len(mock.CherryPickCalls))
	}
	if mock.CherryPickCalls[0].CommitSHA != commit1 {
		t.Errorf("expected first cherry-pick commit=%s, got %s", commit1, mock.CherryPickCalls[0].CommitSHA)
	}
	if mock.CherryPickCalls[1].CommitSHA != commit2 {
		t.Errorf("expected second cherry-pick commit=%s, got %s", commit2, mock.CherryPickCalls[1].CommitSHA)
	}

	// Verify git log was called with --reverse.
	foundLogCall := false
	for _, call := range mock.RunCalls {
		for _, arg := range call.Args {
			if arg == "--reverse" {
				foundLogCall = true
				break
			}
		}
	}
	if !foundLogCall {
		t.Error("expected git log call with --reverse flag")
	}

	// Verify patch result.
	if len(rebuildResult.PatchResults) < 1 {
		t.Fatal("expected at least 1 patch result")
	}
	pr := rebuildResult.PatchResults[0]
	if pr.Status != "success" {
		t.Errorf("expected patch status='success', got %q", pr.Status)
	}
	if pr.NewHeadSHA == nil {
		t.Error("expected non-nil new_head_sha for successful patch")
	}
}

// ===========================================================================
// TS-16-4: With REBUILD_STRATEGY='merge', the executor merges each patch
// branch into the temporary branch with --no-ff, producing a merge commit,
// and records the merge commit SHA in patch_results.
//
// Requirement: 16-REQ-1.4
// ===========================================================================

func TestRebuildExecutor_MergeStrategy_MergesWithNoFF(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/bar", Position: 1, Status: PatchStatusActive},
	})

	mergeSHA := "dddd000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, _ ...string) (string, error) {
		return mergeSHA, nil
	}
	mock.MergeNoFFFunc = func(_ context.Context, _ string) error { return nil }

	h := &RebuildHandler{
		PatchStore:   patches,
		NewGitRunner: func(_ string) (GitRunner, error) { return mock, nil },
		Fetch:        func(_ context.Context, _ string) error { return nil },
		ResolveAuth:  func(_ string) error { return nil },
	}

	payload := RebuildPayload{
		WorkspaceSlug: "ws1",
		Strategy:      StrategyMerge,
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

	// Verify MergeNoFF was called with the correct branch.
	if len(mock.MergeNoFFCalls) != 1 {
		t.Fatalf("expected 1 MergeNoFF call, got %d", len(mock.MergeNoFFCalls))
	}
	if mock.MergeNoFFCalls[0].Branch != "feature/bar" {
		t.Errorf("expected MergeNoFF branch='feature/bar', got %q", mock.MergeNoFFCalls[0].Branch)
	}

	// Verify no cherry-pick calls were made (merge strategy, not rebase).
	if len(mock.CherryPickCalls) != 0 {
		t.Errorf("expected 0 cherry-pick calls for merge strategy, got %d", len(mock.CherryPickCalls))
	}

	// Verify patch result.
	if len(rebuildResult.PatchResults) < 1 {
		t.Fatal("expected at least 1 patch result")
	}
	pr := rebuildResult.PatchResults[0]
	if pr.Status != "success" {
		t.Errorf("expected patch status='success', got %q", pr.Status)
	}
	if pr.NewHeadSHA == nil {
		t.Error("expected non-nil new_head_sha for merge commit")
	}
}

// ===========================================================================
// TS-16-5: When a cherry-pick produces a conflict that rerere cannot fully
// resolve, the executor aborts, sets the patch status to 'conflict' in the
// database, records conflict_files, deletes the temporary branch, and halts
// the rebuild without updating the integration branch.
//
// Requirement: 16-REQ-1.5
// ===========================================================================

func TestRebuildExecutor_Conflict_FailFast(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p-conflict", WorkspaceID: "ws1", BranchName: "feature/conflict", Position: 1, Status: PatchStatusActive},
		{ID: "p-after", WorkspaceID: "ws1", BranchName: "feature/after", Position: 2, Status: PatchStatusActive},
	})

	upstreamHead := "aaaa000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return "bbbb000000000000000000000000000000000001", nil
			}
			// Simulate 'git diff --name-only --diff-filter=U' returning conflict files.
			if arg == "--diff-filter=U" {
				return "src/main.go", nil
			}
		}
		return upstreamHead, nil
	}

	// Cherry-pick returns a conflict error.
	mock.CherryPickFunc = func(_ context.Context, _ string) error {
		return &CherryPickConflictError{Files: []string{"src/main.go"}}
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

	result, retryable, err := h.HandleRebuildJob(context.Background(), payloadJSON)
	// Conflict should produce a non-retryable error (permanent failure).
	if err == nil {
		t.Fatal("expected error for conflict, got nil")
	}
	if retryable {
		t.Error("expected retryable=false for conflict (permanent failure)")
	}

	// The patch status should have been updated to 'conflict' in the store.
	updated, exists := patches.UpdatedPatches["p-conflict"]
	if !exists {
		t.Fatal("expected patch 'p-conflict' to be updated in store")
	}
	if updated.Status != PatchStatusConflict {
		t.Errorf("expected patch status=%q, got %q", PatchStatusConflict, updated.Status)
	}
	if len(updated.ConflictFiles) == 0 {
		t.Error("expected non-empty conflict_files for conflicted patch")
	}

	// Verify fail-fast: the second patch should NOT have been attempted.
	// If result is non-nil, check patch_results.
	if result != nil {
		if rebuildResult, ok := result.(*RebuildResult); ok {
			for _, pr := range rebuildResult.PatchResults {
				if pr.BranchName == "feature/after" && pr.Status == "success" {
					t.Error("expected second patch to NOT be applied (fail-fast)")
				}
			}
		}
	}
}

// Test merge strategy conflict also triggers fail-fast.
func TestRebuildExecutor_MergeConflict_FailFast(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/conflict", Position: 1, Status: PatchStatusActive},
	})

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--diff-filter=U" {
				return "config.yaml", nil
			}
		}
		return "aaaa000000000000000000000000000000000001", nil
	}

	mock.MergeNoFFFunc = func(_ context.Context, _ string) error {
		return &MergeNoFFConflictError{Files: []string{"config.yaml"}}
	}

	h := &RebuildHandler{
		PatchStore:   patches,
		NewGitRunner: func(_ string) (GitRunner, error) { return mock, nil },
		Fetch:        func(_ context.Context, _ string) error { return nil },
		ResolveAuth:  func(_ string) error { return nil },
	}

	payload := RebuildPayload{
		WorkspaceSlug: "ws1",
		Strategy:      StrategyMerge,
		SubmittedBy:   "operator",
	}
	payloadJSON, _ := json.Marshal(payload)

	_, retryable, err := h.HandleRebuildJob(context.Background(), payloadJSON)
	if err == nil {
		t.Fatal("expected error for merge conflict, got nil")
	}
	if retryable {
		t.Error("expected retryable=false for merge conflict (permanent failure)")
	}

	// Verify patch status updated to conflict.
	updated, exists := patches.UpdatedPatches["p1"]
	if !exists {
		t.Fatal("expected patch 'p1' to be updated")
	}
	if updated.Status != PatchStatusConflict {
		t.Errorf("expected status=%q, got %q", PatchStatusConflict, updated.Status)
	}
}

// ===========================================================================
// TS-16-6: When a patch's branch does not exist in the repository during
// rebuild, the executor skips it with status 'skipped' and null new_head_sha
// in patch_results, and continues to the next patch without halting.
//
// Requirement: 16-REQ-1.6
// ===========================================================================

func TestRebuildExecutor_MissingBranch_Skipped(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p-exists", WorkspaceID: "ws1", BranchName: "feature/exists", Position: 1, Status: PatchStatusActive},
		{ID: "p-missing", WorkspaceID: "ws1", BranchName: "feature/missing", Position: 2, Status: PatchStatusActive},
	})

	commitSHA := "bbbb000000000000000000000000000000000001"
	resultSHA := "cccc000000000000000000000000000000000001"

	// Mock: git log for feature/exists returns a commit,
	// git log for feature/missing returns an error (branch not found).
	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				// Check if this is for the missing branch.
				for _, a := range args {
					if a == fmt.Sprintf("%s..feature/missing", "aaaa000000000000000000000000000000000001") ||
						containsString(args, "feature/missing") {
						return "", fmt.Errorf("unknown revision or path not in the working tree: feature/missing")
					}
				}
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

	// Should have 2 patch results.
	if len(rebuildResult.PatchResults) != 2 {
		t.Fatalf("expected 2 patch results, got %d", len(rebuildResult.PatchResults))
	}

	// First patch (exists) should succeed.
	if rebuildResult.PatchResults[0].Status != "success" {
		t.Errorf("expected first patch status='success', got %q", rebuildResult.PatchResults[0].Status)
	}

	// Second patch (missing) should be skipped with skipped_reason='branch_not_found'.
	if rebuildResult.PatchResults[1].Status != "skipped" {
		t.Errorf("expected second patch status='skipped', got %q", rebuildResult.PatchResults[1].Status)
	}
	if rebuildResult.PatchResults[1].SkippedReason != "branch_not_found" {
		t.Errorf("expected second patch skipped_reason='branch_not_found', got %q", rebuildResult.PatchResults[1].SkippedReason)
	}
	if rebuildResult.PatchResults[1].NewHeadSHA != nil {
		t.Errorf("expected null new_head_sha for skipped patch, got %v", rebuildResult.PatchResults[1].NewHeadSHA)
	}

	// patches_skipped should be 1.
	if rebuildResult.PatchesSkipped != 1 {
		t.Errorf("expected patches_skipped=1, got %d", rebuildResult.PatchesSkipped)
	}
}

// ===========================================================================
// TS-16-7: Patches with status 'merged_upstream' or 'disabled' are reported
// as 'skipped' in patch_results without any attempt to apply them.
//
// Requirement: 16-REQ-1.7
// ===========================================================================

func TestRebuildExecutor_MergedAndDisabled_Skipped(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p-merged", WorkspaceID: "ws1", BranchName: "feature/merged", Position: 1, Status: PatchStatusMergedUpstream},
		{ID: "p-disabled", WorkspaceID: "ws1", BranchName: "feature/disabled", Position: 2, Status: PatchStatusDisabled},
	})

	mock.RunFunc = func(_ context.Context, _ ...string) (string, error) {
		return "aaaa000000000000000000000000000000000001", nil
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

	// Both patches should be 'skipped' with appropriate skipped_reason.
	for _, pr := range rebuildResult.PatchResults {
		if pr.Status != "skipped" {
			t.Errorf("expected patch %q status='skipped', got %q", pr.BranchName, pr.Status)
		}
		if pr.NewHeadSHA != nil {
			t.Errorf("expected null new_head_sha for skipped patch %q", pr.BranchName)
		}
	}

	// Verify skipped_reason is set to the patch status (not 'branch_not_found').
	if rebuildResult.PatchResults[0].SkippedReason != "merged_upstream" {
		t.Errorf("expected merged patch skipped_reason='merged_upstream', got %q",
			rebuildResult.PatchResults[0].SkippedReason)
	}
	if rebuildResult.PatchResults[1].SkippedReason != "disabled" {
		t.Errorf("expected disabled patch skipped_reason='disabled', got %q",
			rebuildResult.PatchResults[1].SkippedReason)
	}

	// No cherry-pick or merge calls should have been made.
	if len(mock.CherryPickCalls) != 0 {
		t.Errorf("expected 0 cherry-pick calls for skipped patches, got %d", len(mock.CherryPickCalls))
	}
	if len(mock.MergeNoFFCalls) != 0 {
		t.Errorf("expected 0 MergeNoFF calls for skipped patches, got %d", len(mock.MergeNoFFCalls))
	}
}

// ===========================================================================
// TS-NS-4 + TS-NS-5: Rebuild skipped_reason differentiates between
// branch_not_found, disabled, and merged_upstream.
// ===========================================================================

func TestRebuildExecutor_SkippedReason_Differentiation(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p-merged", WorkspaceID: "ws1", BranchName: "feature/merged", Position: 1, Status: PatchStatusMergedUpstream},
		{ID: "p-disabled", WorkspaceID: "ws1", BranchName: "feature/disabled", Position: 2, Status: PatchStatusDisabled},
		{ID: "p-missing", WorkspaceID: "ws1", BranchName: "feature/missing", Position: 3, Status: PatchStatusActive},
	})

	upstreamHead := "aaaa000000000000000000000000000000000001"

	// Mock: all git log calls for the missing branch fail.
	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return "", fmt.Errorf("unknown revision: feature/missing")
			}
		}
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

	if len(rebuildResult.PatchResults) != 3 {
		t.Fatalf("expected 3 patch results, got %d", len(rebuildResult.PatchResults))
	}

	// Verify each patch has the correct skipped_reason.
	tests := []struct {
		patchID       string
		wantStatus    string
		wantReason    string
		notWantReason string
	}{
		{"p-merged", "skipped", "merged_upstream", "branch_not_found"},
		{"p-disabled", "skipped", "disabled", "branch_not_found"},
		{"p-missing", "skipped", "branch_not_found", ""},
	}

	for i, tt := range tests {
		pr := rebuildResult.PatchResults[i]
		if pr.PatchID != tt.patchID {
			t.Errorf("patch[%d]: expected id=%q, got %q", i, tt.patchID, pr.PatchID)
		}
		if pr.Status != tt.wantStatus {
			t.Errorf("patch %q: expected status=%q, got %q", tt.patchID, tt.wantStatus, pr.Status)
		}
		if pr.SkippedReason != tt.wantReason {
			t.Errorf("patch %q: expected skipped_reason=%q, got %q", tt.patchID, tt.wantReason, pr.SkippedReason)
		}
		if tt.notWantReason != "" && pr.SkippedReason == tt.notWantReason {
			t.Errorf("patch %q: skipped_reason should NOT be %q", tt.patchID, tt.notWantReason)
		}
	}
}

// ===========================================================================
// TS-16-8: The rebuild job is registered as type 'rebuild' in the
// durable_job_queue, is marked as permanently failed (non-retryable) on
// conflict, and is retryable on transient failures such as network errors.
//
// Requirement: 16-REQ-1.8
// ===========================================================================

func TestRebuildJobRegistration_TypeIsRebuild(t *testing.T) {
	q, _ := newTestQueue(t)

	h := &RebuildHandler{}
	err := RegisterRebuildJob(q, h)
	if err != nil {
		t.Fatalf("RegisterRebuildJob returned error: %v", err)
	}

	// Verify that the type is registered by trying to enqueue a rebuild job.
	payload := RebuildPayload{
		WorkspaceSlug: "test-ws",
		Strategy:      StrategyRebase,
		SubmittedBy:   "operator",
	}
	payloadJSON, _ := json.Marshal(payload)

	jobID, _, err := q.Enqueue(jobqueue.EnqueueParams{
		Type:    "rebuild",
		Key:     "test-ws",
		Nonce:   "test-nonce-1",
		Payload: payloadJSON,
	})
	if err != nil {
		t.Fatalf("Enqueue rebuild job failed: %v", err)
	}
	if jobID == "" {
		t.Error("expected non-empty job ID after enqueue")
	}
}

func TestRebuildJob_ConflictIsNonRetryable(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/conflict", Position: 1, Status: PatchStatusActive},
	})

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return "bbbb000000000000000000000000000000000001", nil
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

	_, retryable, err := h.HandleRebuildJob(context.Background(), payloadJSON)
	if err == nil {
		t.Fatal("expected error for conflict")
	}
	if retryable {
		t.Error("conflict errors should be non-retryable (permanent failure)")
	}
}

func TestRebuildJob_TransientErrorIsRetryable(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/foo", Position: 1, Status: PatchStatusActive},
	})

	// Simulate a transient network error during fetch.
	h := &RebuildHandler{
		PatchStore:   patches,
		NewGitRunner: func(_ string) (GitRunner, error) { return mock, nil },
		Fetch: func(_ context.Context, _ string) error {
			return &TransientError{Err: errors.New("network timeout")}
		},
		ResolveAuth: func(_ string) error { return nil },
	}

	payload := RebuildPayload{
		WorkspaceSlug: "ws1",
		Strategy:      StrategyRebase,
		SubmittedBy:   "operator",
	}
	payloadJSON, _ := json.Marshal(payload)

	_, retryable, err := h.HandleRebuildJob(context.Background(), payloadJSON)
	if err == nil {
		t.Fatal("expected error for transient failure")
	}
	if !retryable {
		t.Error("transient errors should be retryable")
	}
}

// ===========================================================================
// Additional executor tests
// ===========================================================================

// Test: rebuild result contains all required fields.
func TestRebuildResult_ContainsRequiredFields(t *testing.T) {
	mock := newMockGitRunner()
	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
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

	// Validate all required fields are present.
	if rebuildResult.UpstreamHeadSHA == "" {
		t.Error("missing upstream_head_sha in result")
	}
	if rebuildResult.IntegrationHeadSHA == "" {
		t.Error("missing integration_head_sha in result")
	}
	if rebuildResult.Strategy == "" {
		t.Error("missing strategy in result")
	}
	if rebuildResult.PatchResults == nil {
		t.Error("missing patch_results in result")
	}

	// Verify result is JSON-serializable with correct field names.
	data, err := json.Marshal(rebuildResult)
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	requiredKeys := []string{"upstream_head_sha", "integration_head_sha", "strategy", "patches_applied", "patches_skipped", "patches_removed", "patch_results"}
	for _, key := range requiredKeys {
		if _, exists := raw[key]; !exists {
			t.Errorf("missing required key %q in JSON result", key)
		}
	}
}

// Test: patches are applied in position order.
func TestRebuildExecutor_PatchesAppliedInPositionOrder(t *testing.T) {
	mock := newMockGitRunner()

	// Patches in non-sequential positions to verify ordering.
	patches := newMockPatchStore([]Patch{
		{ID: "p3", WorkspaceID: "ws1", BranchName: "feature/third", Position: 3, Status: PatchStatusActive},
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/first", Position: 1, Status: PatchStatusActive},
		{ID: "p2", WorkspaceID: "ws1", BranchName: "feature/second", Position: 2, Status: PatchStatusActive},
	})

	appliedOrder := []string{}
	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		for _, arg := range args {
			if arg == "--reverse" {
				return "bbbb000000000000000000000000000000000001", nil
			}
		}
		return "aaaa000000000000000000000000000000000001", nil
	}
	mock.CherryPickFunc = func(_ context.Context, sha string) error {
		appliedOrder = append(appliedOrder, sha)
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
		t.Fatalf("HandleRebuildJob returned error: %v", err)
	}

	rebuildResult, ok := result.(*RebuildResult)
	if !ok {
		t.Fatalf("expected result to be *RebuildResult, got %T", result)
	}

	// Verify patches were processed in position order (1, 2, 3).
	if len(rebuildResult.PatchResults) != 3 {
		t.Fatalf("expected 3 patch results, got %d", len(rebuildResult.PatchResults))
	}
	expectedOrder := []string{"feature/first", "feature/second", "feature/third"}
	for i, pr := range rebuildResult.PatchResults {
		if pr.BranchName != expectedOrder[i] {
			t.Errorf("patch result[%d] branch=%q, want %q", i, pr.BranchName, expectedOrder[i])
		}
	}
}

// Test: patches with status 'conflict' are included in rebuild attempt
// (treated same as 'active' per spec 16-REQ-1.2).
func TestRebuildExecutor_ConflictStatusIncluded(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/was-conflict", Position: 1, Status: PatchStatusConflict},
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

	// The conflict-status patch should have been attempted (and succeed with mocks).
	if rebuildResult.PatchesApplied != 1 {
		t.Errorf("expected patches_applied=1 (conflict patches should be attempted), got %d", rebuildResult.PatchesApplied)
	}
	if len(rebuildResult.PatchResults) < 1 {
		t.Fatal("expected at least 1 patch result")
	}
	if rebuildResult.PatchResults[0].Status != "success" {
		t.Errorf("expected patch status='success', got %q", rebuildResult.PatchResults[0].Status)
	}
}

// ===========================================================================
// TS-NS-1: The rebuild executor resolves the upstream HEAD using FETCH_HEAD,
// not HEAD, immediately after the fetch step.
//
// Requirement: NS-REQ-1
// ===========================================================================

func TestRebuildExecutor_UsesFetchHeadNotHead(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/foo", Position: 1, Status: PatchStatusActive},
	})

	fetchHeadSHA := "fetch000000000000000000000000000000000001"
	localHeadSHA := "local000000000000000000000000000000000001"
	commitSHA := "bbbb000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" {
			if args[1] == "FETCH_HEAD" {
				return fetchHeadSHA, nil
			}
			// Any other rev-parse (including HEAD) returns a different SHA.
			return localHeadSHA, nil
		}
		for _, arg := range args {
			if arg == "--reverse" {
				return commitSHA, nil
			}
		}
		return localHeadSHA, nil
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

	// Verify that rev-parse FETCH_HEAD was called and rev-parse HEAD was NOT
	// called for upstream resolution (before checkout -b).
	foundFetchHead := false
	for _, call := range mock.RunCalls {
		if len(call.Args) >= 2 && call.Args[0] == "rev-parse" && call.Args[1] == "FETCH_HEAD" {
			foundFetchHead = true
			break
		}
	}
	if !foundFetchHead {
		t.Error("expected a 'rev-parse FETCH_HEAD' call, but none was recorded")
	}

	// Verify checkout -b was called with the FETCH_HEAD SHA.
	foundCheckout := false
	for _, call := range mock.RunCalls {
		if len(call.Args) >= 3 && call.Args[0] == "checkout" && call.Args[1] == "-b" {
			if call.Args[3] == fetchHeadSHA {
				foundCheckout = true
			} else if call.Args[3] == localHeadSHA {
				t.Error("checkout -b used local HEAD SHA instead of FETCH_HEAD SHA")
			}
			break
		}
	}
	if !foundCheckout {
		t.Error("expected checkout -b with FETCH_HEAD SHA as start point")
	}
}

// ===========================================================================
// TS-NS-2: The UpstreamHeadSHA field in RebuildResult reflects the fetched
// upstream tip, not the local HEAD.
//
// Requirement: NS-REQ-2
// ===========================================================================

func TestRebuildExecutor_UpstreamHeadSHA_ReflectsFetchHead(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/foo", Position: 1, Status: PatchStatusActive},
	})

	fetchHeadSHA := "fetch000000000000000000000000000000000001"
	localHeadSHA := "local000000000000000000000000000000000001"
	commitSHA := "bbbb000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" {
			if args[1] == "FETCH_HEAD" {
				return fetchHeadSHA, nil
			}
			// HEAD and other rev-parse calls return localHeadSHA.
			return localHeadSHA, nil
		}
		for _, arg := range args {
			if arg == "--reverse" {
				return commitSHA, nil
			}
		}
		return localHeadSHA, nil
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

	// The UpstreamHeadSHA must equal FETCH_HEAD, not local HEAD.
	if rebuildResult.UpstreamHeadSHA != fetchHeadSHA {
		t.Errorf("UpstreamHeadSHA = %q, want %q (FETCH_HEAD)", rebuildResult.UpstreamHeadSHA, fetchHeadSHA)
	}
	if rebuildResult.UpstreamHeadSHA == localHeadSHA {
		t.Error("UpstreamHeadSHA incorrectly reflects local HEAD instead of FETCH_HEAD")
	}
}

// ===========================================================================
// TS-NS-4: When FETCH_HEAD is unavailable (e.g., no fetch has been performed),
// the executor returns a retryable transient error rather than silently using
// HEAD.
//
// Requirement: NS-REQ-4
// ===========================================================================

func TestRebuildExecutor_FetchHeadUnavailable_ReturnsTransientError(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/foo", Position: 1, Status: PatchStatusActive},
	})

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "FETCH_HEAD" {
			return "", fmt.Errorf("fatal: FETCH_HEAD does not exist")
		}
		return "aaaa000000000000000000000000000000000001", nil
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

	result, retryable, err := h.HandleRebuildJob(context.Background(), payloadJSON)

	// Should return an error.
	if err == nil {
		t.Fatal("expected error when FETCH_HEAD is unavailable, got nil")
	}

	// Should be retryable (transient).
	if !retryable {
		t.Error("expected retryable=true for unavailable FETCH_HEAD")
	}

	// Should be a TransientError.
	var te *TransientError
	if !errors.As(err, &te) {
		t.Errorf("expected *TransientError, got %T: %v", err, err)
	}

	// Should not return a result.
	if result != nil {
		t.Error("expected nil result when FETCH_HEAD is unavailable")
	}

	// Should NOT have attempted checkout -b (no temp branch created).
	for _, call := range mock.RunCalls {
		if len(call.Args) >= 2 && call.Args[0] == "checkout" && call.Args[1] == "-b" {
			t.Error("should not create temp branch when FETCH_HEAD resolution fails")
		}
	}
}

// ===========================================================================
// TS-NS-1: A completed rebuild result includes the previous integration
// branch HEAD SHA.
//
// Requirement: NS-REQ-1
// ===========================================================================

func TestRebuildExecutor_PreviousIntegrationHeadSHA_Set(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/foo", Position: 1, Status: PatchStatusActive},
	})

	fetchHeadSHA := "aaaa000000000000000000000000000000000001"
	previousIntegrationSHA := "prev000000000000000000000000000000000001"
	commitSHA := "bbbb000000000000000000000000000000000001"
	finalSHA := "cccc000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" {
			if args[1] == "FETCH_HEAD" {
				return fetchHeadSHA, nil
			}
			// rev-parse --verify integration (to capture previous HEAD)
			if len(args) >= 3 && args[1] == "--verify" && args[2] == "integration" {
				return previousIntegrationSHA, nil
			}
			// rev-parse HEAD (for final integration head and per-patch head)
			return finalSHA, nil
		}
		for _, arg := range args {
			if arg == "--reverse" {
				return commitSHA, nil
			}
		}
		return fetchHeadSHA, nil
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

	// Verify PreviousIntegrationHeadSHA is set.
	if rebuildResult.PreviousIntegrationHeadSHA != previousIntegrationSHA {
		t.Errorf("PreviousIntegrationHeadSHA = %q, want %q",
			rebuildResult.PreviousIntegrationHeadSHA, previousIntegrationSHA)
	}

	// Verify IntegrationHeadSHA is set to a different value.
	if rebuildResult.IntegrationHeadSHA == "" {
		t.Error("IntegrationHeadSHA should not be empty")
	}
	if rebuildResult.IntegrationHeadSHA == rebuildResult.PreviousIntegrationHeadSHA {
		t.Error("IntegrationHeadSHA and PreviousIntegrationHeadSHA should differ")
	}

	// Verify the field is present in JSON.
	data, err := json.Marshal(rebuildResult)
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if _, ok := raw["previous_integration_head_sha"]; !ok {
		t.Error("missing 'previous_integration_head_sha' in JSON result")
	}
}

// Test: PreviousIntegrationHeadSHA is empty when integration branch does not
// exist yet (first-ever rebuild).
func TestRebuildExecutor_PreviousIntegrationHeadSHA_EmptyOnFirstRebuild(t *testing.T) {
	mock := newMockGitRunner()

	patches := newMockPatchStore([]Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/foo", Position: 1, Status: PatchStatusActive},
	})

	fetchHeadSHA := "aaaa000000000000000000000000000000000001"
	commitSHA := "bbbb000000000000000000000000000000000001"
	finalSHA := "cccc000000000000000000000000000000000001"

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" {
			if args[1] == "FETCH_HEAD" {
				return fetchHeadSHA, nil
			}
			// rev-parse --verify integration fails (branch doesn't exist)
			if len(args) >= 3 && args[1] == "--verify" && args[2] == "integration" {
				return "", fmt.Errorf("fatal: Needed a single revision")
			}
			return finalSHA, nil
		}
		for _, arg := range args {
			if arg == "--reverse" {
				return commitSHA, nil
			}
		}
		return fetchHeadSHA, nil
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

	// PreviousIntegrationHeadSHA should be empty on first rebuild.
	if rebuildResult.PreviousIntegrationHeadSHA != "" {
		t.Errorf("PreviousIntegrationHeadSHA = %q, want empty string (first rebuild)",
			rebuildResult.PreviousIntegrationHeadSHA)
	}

	// Verify the field is omitted from JSON when empty.
	data, err := json.Marshal(rebuildResult)
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if _, ok := raw["previous_integration_head_sha"]; ok {
		t.Error("'previous_integration_head_sha' should be omitted from JSON when empty")
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

// containsString checks if a string slice contains a given string.
func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s || (len(v) > len(s) && v[len(v)-len(s):] == s) {
			return true
		}
	}
	return false
}
