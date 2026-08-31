package carrypatch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// ===========================================================================
// TS-NS-1: Squash-merged patch is automatically detected and transitioned
// to merged_upstream during sync.
//
// Requirement: NS-REQ-1
// ===========================================================================

func TestSquashMerge_ContentBased_DetectsSquashMerge(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)
	seedPatch(t, env.db, "p1", "my-workspace", "feature/squash-merged", 1, PatchStatusActive)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/squash-merged", Position: 1, Status: PatchStatusActive},
	}

	// IsAncestor returns false (squash merge — original commits are not ancestors).
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}

	// Cherry: all commits have been applied (squash-merged content exists upstream).
	env.gitRunner.CherryFunc = func(_ context.Context, upstream, head string) ([]string, []string, error) {
		// Return 1 applied commit, 0 pending.
		return []string{"abc123"}, nil, nil
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

	// Patch should be in patches_merged.
	found := false
	for _, name := range resp.PatchesMerged {
		if name == "feature/squash-merged" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'feature/squash-merged' in patches_merged, got %v", resp.PatchesMerged)
	}

	// Verify the patch status was transitioned to merged_upstream.
	updated, exists := env.patchStore.UpdatedPatches["p1"]
	if !exists {
		t.Error("expected patch 'p1' to be updated to merged_upstream in store")
	} else if updated.Status != PatchStatusMergedUpstream {
		t.Errorf("expected patch status=%q, got %q", PatchStatusMergedUpstream, updated.Status)
	}
}

// ===========================================================================
// TS-NS-2: Content-based detection via git cherry correctly identifies
// squash-merged commits.
//
// Requirement: NS-REQ-2
// ===========================================================================

func TestSquashMerge_ContentBased_AllCommitsApplied_ReturnsTrue(t *testing.T) {
	mock := newMockGitRunner()

	// All commits applied upstream (squash merge).
	mock.CherryFunc = func(_ context.Context, _, _ string) ([]string, []string, error) {
		return []string{"abc123", "def456"}, nil, nil
	}

	result := detectSquashMergeByContent(context.Background(), mock, "feature/x", "upstream-head")
	if !result {
		t.Error("expected detectSquashMergeByContent to return true when all commits are applied")
	}
}

func TestSquashMerge_ContentBased_PartialCommits_ReturnsFalse(t *testing.T) {
	mock := newMockGitRunner()

	// Only some commits applied (partial match).
	mock.CherryFunc = func(_ context.Context, _, _ string) ([]string, []string, error) {
		return []string{"abc123"}, []string{"def456"}, nil
	}

	result := detectSquashMergeByContent(context.Background(), mock, "feature/x", "upstream-head")
	if result {
		t.Error("expected detectSquashMergeByContent to return false when some commits are pending")
	}
}

func TestSquashMerge_ContentBased_NoCommits_ReturnsFalse(t *testing.T) {
	mock := newMockGitRunner()

	// No commits to compare.
	mock.CherryFunc = func(_ context.Context, _, _ string) ([]string, []string, error) {
		return nil, nil, nil
	}

	result := detectSquashMergeByContent(context.Background(), mock, "feature/x", "upstream-head")
	if result {
		t.Error("expected detectSquashMergeByContent to return false when no commits exist")
	}
}

func TestSquashMerge_ContentBased_CherryError_ReturnsFalse(t *testing.T) {
	mock := newMockGitRunner()

	mock.CherryFunc = func(_ context.Context, _, _ string) ([]string, []string, error) {
		return nil, nil, context.DeadlineExceeded
	}

	result := detectSquashMergeByContent(context.Background(), mock, "feature/x", "upstream-head")
	if result {
		t.Error("expected detectSquashMergeByContent to return false on error")
	}
}

// ===========================================================================
// TS-NS-3: When SQUASH_MERGE_DETECTION=ancestry_only, only the existing
// IsAncestor check is used (backwards-compatible mode).
//
// Requirement: NS-REQ-3
// ===========================================================================

func TestSquashMerge_AncestryOnly_NoContentCheck(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "SQUASH_MERGE_DETECTION" {
			return "ancestry_only", nil
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
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
	seedPatch(t, env.db, "p1", "my-workspace", "feature/squash-merged", 1, PatchStatusActive)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/squash-merged", Position: 1, Status: PatchStatusActive},
	}

	// IsAncestor returns false (squash merge).
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}

	// Cherry should NOT be called in ancestry_only mode.
	cherryCallCount := 0
	env.gitRunner.CherryFunc = func(_ context.Context, _, _ string) ([]string, []string, error) {
		cherryCallCount++
		return []string{"abc123"}, nil, nil
	}

	// Run should NOT be called for log scanning.
	runCallCount := 0
	originalRunFunc := env.gitRunner.RunFunc
	env.gitRunner.RunFunc = func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "log" {
			runCallCount++
		}
		return originalRunFunc(ctx, args...)
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

	// Patch should NOT be in patches_merged (ancestry check failed, no fallback).
	if len(resp.PatchesMerged) != 0 {
		t.Errorf("expected empty patches_merged in ancestry_only mode, got %v", resp.PatchesMerged)
	}

	// Verify no content-based detection was invoked.
	if cherryCallCount != 0 {
		t.Errorf("expected 0 Cherry calls in ancestry_only mode, got %d", cherryCallCount)
	}
	if runCallCount != 0 {
		t.Errorf("expected 0 log Run calls in ancestry_only mode, got %d", runCallCount)
	}

	// Verify patch status remains unchanged.
	if _, exists := env.patchStore.UpdatedPatches["p1"]; exists {
		t.Error("expected patch 'p1' status to remain unchanged in ancestry_only mode")
	}
}

// ===========================================================================
// TS-NS-4: PR-number scanning detects squash merges when upstream_pr_url
// is set and the squash-merge commit message follows GitHub's (#NNN) format.
//
// Requirement: NS-REQ-4
// ===========================================================================

func TestSquashMerge_PRNumberScanning_DetectsMatch(t *testing.T) {
	mock := newMockGitRunner()

	// Cherry: not all commits applied (content comparison fails).
	mock.CherryFunc = func(_ context.Context, _, _ string) ([]string, []string, error) {
		return nil, []string{"abc123"}, nil
	}

	// Log output contains the PR number.
	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "log" {
			return "Fix some bug (#42)\nAnother commit\nYet another", nil
		}
		return "", nil
	}

	prURL := "https://github.com/org/repo/pull/42"
	patch := Patch{
		ID:            "p1",
		BranchName:    "feature/x",
		Status:        PatchStatusActive,
		UpstreamPRURL: &prURL,
	}

	result := detectSquashMerge(context.Background(), mock, patch, "old-sha", "new-sha")
	if !result {
		t.Error("expected detectSquashMerge to return true when PR number matches")
	}
}

func TestSquashMerge_PRNumberScanning_NoMatch(t *testing.T) {
	mock := newMockGitRunner()

	mock.CherryFunc = func(_ context.Context, _, _ string) ([]string, []string, error) {
		return nil, []string{"abc123"}, nil
	}

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "log" {
			return "Fix some bug (#99)\nAnother commit", nil
		}
		return "", nil
	}

	prURL := "https://github.com/org/repo/pull/42"
	patch := Patch{
		ID:            "p1",
		BranchName:    "feature/x",
		Status:        PatchStatusActive,
		UpstreamPRURL: &prURL,
	}

	result := detectSquashMerge(context.Background(), mock, patch, "old-sha", "new-sha")
	if result {
		t.Error("expected detectSquashMerge to return false when PR number doesn't match")
	}
}

func TestSquashMerge_PRNumberScanning_NoPRURL(t *testing.T) {
	mock := newMockGitRunner()

	// Content doesn't match.
	mock.CherryFunc = func(_ context.Context, _, _ string) ([]string, []string, error) {
		return nil, []string{"abc123"}, nil
	}

	patch := Patch{
		ID:         "p1",
		BranchName: "feature/x",
		Status:     PatchStatusActive,
		// No upstream_pr_url set.
	}

	result := detectSquashMerge(context.Background(), mock, patch, "old-sha", "new-sha")
	if result {
		t.Error("expected detectSquashMerge to return false when no PR URL is set")
	}
}

// ===========================================================================
// TS-NS-5: Partial or false-positive content matches do not incorrectly
// mark a patch as merged.
//
// Requirement: NS-REQ-5
// ===========================================================================

func TestSquashMerge_PartialContentMatch_PatchRemainsActive(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "SQUASH_MERGE_DETECTION" {
			return "content_based", nil
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
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
	seedPatch(t, env.db, "p1", "my-workspace", "feature/partial", 1, PatchStatusActive)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/partial", Position: 1, Status: PatchStatusActive},
	}

	// Cherry: 2 of 3 commits applied, 1 still pending.
	env.gitRunner.CherryFunc = func(_ context.Context, _, _ string) ([]string, []string, error) {
		return []string{"abc123", "def456"}, []string{"ghi789"}, nil
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

	// Patch should NOT be in patches_merged.
	if len(resp.PatchesMerged) != 0 {
		t.Errorf("expected empty patches_merged for partial content match, got %v", resp.PatchesMerged)
	}

	// Verify patch status remains unchanged.
	if _, exists := env.patchStore.UpdatedPatches["p1"]; exists {
		t.Error("expected patch 'p1' status to remain unchanged for partial match")
	}
}

// ===========================================================================
// extractPRNumber unit tests
// ===========================================================================

func TestExtractPRNumber(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/org/repo/pull/42", "42"},
		{"https://github.com/org/repo/pull/123", "123"},
		{"https://github.com/org/repo/pull/1", "1"},
		{"https://github.com/org/repo/pull/42/files", "42"},
		{"https://github.com/org/repo/pull/42?query=1", "42"},
		{"https://github.com/org/repo/issues/42", ""},
		{"", ""},
		{"not-a-url", ""},
		{"https://github.com/org/repo/pull/", ""},
		{"https://github.com/org/repo/pull/abc", ""},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			got := extractPRNumber(tc.url)
			if got != tc.want {
				t.Errorf("extractPRNumber(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// ===========================================================================
// detectSquashMergeByPRNumber unit tests
// ===========================================================================

func TestDetectSquashMergeByPRNumber_WithRange(t *testing.T) {
	mock := newMockGitRunner()

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		// Expect: log --oneline --format=%s old..new
		return "Fix login bug (#42)\nRefactor auth module", nil
	}

	result := detectSquashMergeByPRNumber(context.Background(), mock, "42", "old-sha", "new-sha")
	if !result {
		t.Error("expected true when commit message contains (#42)")
	}
}

func TestDetectSquashMergeByPRNumber_NoMatch(t *testing.T) {
	mock := newMockGitRunner()

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		return "Fix login bug (#99)\nRefactor auth module", nil
	}

	result := detectSquashMergeByPRNumber(context.Background(), mock, "42", "old-sha", "new-sha")
	if result {
		t.Error("expected false when commit message doesn't contain (#42)")
	}
}

func TestDetectSquashMergeByPRNumber_EmptyOldHead(t *testing.T) {
	mock := newMockGitRunner()

	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		// When old head is empty, should use -50 limit.
		for _, arg := range args {
			if arg == "-50" {
				return "Fix login bug (#42)", nil
			}
		}
		return "", nil
	}

	result := detectSquashMergeByPRNumber(context.Background(), mock, "42", "", "new-sha")
	if !result {
		t.Error("expected true when commit message contains (#42)")
	}
}

// ===========================================================================
// Integration: content_based mode skips ancestry check
// ===========================================================================

func TestSquashMerge_ContentBasedMode_SkipsAncestryCheck(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "SQUASH_MERGE_DETECTION" {
			return "content_based", nil
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		if key == "AUTO_REBUILD_AFTER_SYNC" {
			return "true", nil
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
	seedPatch(t, env.db, "p1", "my-workspace", "feature/squash-merged", 1, PatchStatusActive)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/squash-merged", Position: 1, Status: PatchStatusActive},
	}

	// IsAncestor should NOT be called in content_based mode.
	isAncestorCalled := false
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		isAncestorCalled = true
		return false, nil
	}

	// Cherry: all commits applied.
	env.gitRunner.CherryFunc = func(_ context.Context, _, _ string) ([]string, []string, error) {
		return []string{"abc123"}, nil, nil
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

	if isAncestorCalled {
		t.Error("expected IsAncestor NOT to be called in content_based mode")
	}

	// Patch should be in patches_merged.
	found := false
	for _, name := range resp.PatchesMerged {
		if name == "feature/squash-merged" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'feature/squash-merged' in patches_merged, got %v", resp.PatchesMerged)
	}
}

// ===========================================================================
// Default mode (both) uses both ancestry and content checks
// ===========================================================================

func TestSquashMerge_DefaultBothMode_FallsBackToContent(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)
	seedPatch(t, env.db, "p1", "my-workspace", "feature/squash-merged", 1, PatchStatusActive)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/squash-merged", Position: 1, Status: PatchStatusActive},
	}

	// Ancestry check fails (squash merge).
	env.gitRunner.IsAncestorFunc = func(_ context.Context, _, _ string) (bool, error) {
		return false, nil
	}

	// Content check succeeds.
	env.gitRunner.CherryFunc = func(_ context.Context, _, _ string) ([]string, []string, error) {
		return []string{"abc123"}, nil, nil
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

	// Should detect via content-based fallback.
	found := false
	for _, name := range resp.PatchesMerged {
		if name == "feature/squash-merged" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'feature/squash-merged' in patches_merged, got %v", resp.PatchesMerged)
	}
}
