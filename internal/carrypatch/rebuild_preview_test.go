package carrypatch

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
)

// ===========================================================================
// TS-NS-2: GET /api/v1/workspaces/:slug/rebuild-preview returns a per-patch
// prediction array with would_succeed or would_conflict status and a
// conflict_files array for conflicting patches.
//
// Requirement: NS-REQ-2
// ===========================================================================

func TestRebuildPreview_Success_ReturnsPatchResults(t *testing.T) {
	env := newFullTestEnv(t)

	// Seed a carry_patch workspace.
	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/repo", "abc123", "integration", "")

	// Seed two active patches.
	seedPatch(t, env.db, "patch-1", "my-workspace", "feature/a", 1, PatchStatusActive)
	seedPatch(t, env.db, "patch-2", "my-workspace", "feature/b", 2, PatchStatusActive)

	// Configure the mock to return patches via the PatchStore.
	env.patchStore.mu.Lock()
	env.patchStore.Patches = []Patch{
		{ID: "patch-1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "patch-2", WorkspaceID: "my-workspace", BranchName: "feature/b", Position: 2, Status: PatchStatusActive},
	}
	env.patchStore.mu.Unlock()

	// Configure the mock git runner:
	// - rev-parse HEAD -> upstream SHA
	// - rev-parse --verify <branch> -> branch head SHA
	// - MergeTree -> clean merge for both patches
	// - commit-tree -> temporary commit SHA
	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			return "upstream-sha", nil
		}
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			return "branch-head-" + args[2], nil
		}
		if len(args) >= 1 && args[0] == "commit-tree" {
			return "commit-sha-result", nil
		}
		return "", nil
	}
	env.gitRunner.MergeTreeFunc = func(_ context.Context, base, head string) (string, error) {
		return "tree-sha-" + head, nil
	}

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/rebuild-preview", "",
		rebuildUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rebuild-preview status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp RebuildPreviewResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.PatchResults) != 2 {
		t.Fatalf("expected 2 patch_results; got %d", len(resp.PatchResults))
	}

	// First patch: should succeed.
	p1 := resp.PatchResults[0]
	if p1.PatchID != "patch-1" {
		t.Errorf("patch_results[0].patch_id = %q; want %q", p1.PatchID, "patch-1")
	}
	if p1.BranchName != "feature/a" {
		t.Errorf("patch_results[0].branch_name = %q; want %q", p1.BranchName, "feature/a")
	}
	if p1.Position != 1 {
		t.Errorf("patch_results[0].position = %d; want %d", p1.Position, 1)
	}
	if p1.Status != "would_succeed" {
		t.Errorf("patch_results[0].status = %q; want %q", p1.Status, "would_succeed")
	}
	if p1.TreeSHA == "" {
		t.Error("patch_results[0].tree_sha should be non-empty for successful merges")
	}
	if len(p1.ConflictFiles) != 0 {
		t.Errorf("patch_results[0].conflict_files should be empty; got %v", p1.ConflictFiles)
	}

	// Second patch: should succeed.
	p2 := resp.PatchResults[1]
	if p2.PatchID != "patch-2" {
		t.Errorf("patch_results[1].patch_id = %q; want %q", p2.PatchID, "patch-2")
	}
	if p2.Status != "would_succeed" {
		t.Errorf("patch_results[1].status = %q; want %q", p2.Status, "would_succeed")
	}
}

// TS-NS-2 (conflict case): When a patch would conflict, the preview returns
// would_conflict status with conflict_files listing the affected files.
func TestRebuildPreview_Conflict_ReturnsConflictFiles(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/repo", "abc123", "integration", "")

	seedPatch(t, env.db, "patch-1", "my-workspace", "feature/clean", 1, PatchStatusActive)
	seedPatch(t, env.db, "patch-2", "my-workspace", "feature/conflict", 2, PatchStatusActive)

	env.patchStore.mu.Lock()
	env.patchStore.Patches = []Patch{
		{ID: "patch-1", WorkspaceID: "my-workspace", BranchName: "feature/clean", Position: 1, Status: PatchStatusActive},
		{ID: "patch-2", WorkspaceID: "my-workspace", BranchName: "feature/conflict", Position: 2, Status: PatchStatusActive},
	}
	env.patchStore.mu.Unlock()

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			return "upstream-sha", nil
		}
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			return "branch-head-" + args[2], nil
		}
		if len(args) >= 1 && args[0] == "commit-tree" {
			return "commit-sha-result", nil
		}
		return "", nil
	}

	callCount := 0
	env.gitRunner.MergeTreeFunc = func(_ context.Context, base, head string) (string, error) {
		callCount++
		if callCount == 1 {
			// First patch: clean merge.
			return "tree-sha-clean", nil
		}
		// Second patch: conflict.
		return "", &gitcmd.MergeConflictError{
			ConflictingFiles: []string{"base.txt", "config.yml"},
		}
	}

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/rebuild-preview", "",
		rebuildUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rebuild-preview status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp RebuildPreviewResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.PatchResults) != 2 {
		t.Fatalf("expected 2 patch_results; got %d", len(resp.PatchResults))
	}

	// First patch: clean.
	if resp.PatchResults[0].Status != "would_succeed" {
		t.Errorf("patch_results[0].status = %q; want %q", resp.PatchResults[0].Status, "would_succeed")
	}

	// Second patch: conflict.
	p2 := resp.PatchResults[1]
	if p2.Status != "would_conflict" {
		t.Errorf("patch_results[1].status = %q; want %q", p2.Status, "would_conflict")
	}
	if len(p2.ConflictFiles) != 2 {
		t.Fatalf("expected 2 conflict_files; got %d: %v", len(p2.ConflictFiles), p2.ConflictFiles)
	}
	if p2.ConflictFiles[0] != "base.txt" {
		t.Errorf("conflict_files[0] = %q; want %q", p2.ConflictFiles[0], "base.txt")
	}
	if p2.ConflictFiles[1] != "config.yml" {
		t.Errorf("conflict_files[1] = %q; want %q", p2.ConflictFiles[1], "config.yml")
	}
}

// ===========================================================================
// TS-NS-3: The rebuild-preview endpoint does not mutate any git refs,
// branches, or patch statuses.
//
// Requirement: NS-REQ-3
// ===========================================================================

func TestRebuildPreview_NoMutation(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/repo", "abc123", "integration", "")

	seedPatch(t, env.db, "patch-1", "my-workspace", "feature/a", 1, PatchStatusActive)

	env.patchStore.mu.Lock()
	env.patchStore.Patches = []Patch{
		{ID: "patch-1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	}
	env.patchStore.mu.Unlock()

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			return "upstream-sha", nil
		}
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			return "branch-head-sha", nil
		}
		if len(args) >= 1 && args[0] == "commit-tree" {
			return "commit-sha-result", nil
		}
		return "", nil
	}
	env.gitRunner.MergeTreeFunc = func(_ context.Context, _, _ string) (string, error) {
		return "tree-sha", nil
	}

	// Record patch statuses before.
	var beforeStatus string
	err := env.db.QueryRow(`SELECT status FROM patches WHERE id = ?`, "patch-1").Scan(&beforeStatus)
	if err != nil {
		t.Fatalf("failed to read pre-call patch status: %v", err)
	}

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/rebuild-preview", "",
		rebuildUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rebuild-preview status = %d; want %d", rec.Code, http.StatusOK)
	}

	// Verify patch status is unchanged.
	var afterStatus string
	err = env.db.QueryRow(`SELECT status FROM patches WHERE id = ?`, "patch-1").Scan(&afterStatus)
	if err != nil {
		t.Fatalf("failed to read post-call patch status: %v", err)
	}
	if beforeStatus != afterStatus {
		t.Errorf("patch status changed from %q to %q", beforeStatus, afterStatus)
	}

	// Verify the mock PatchStore was NOT called for UpdatePatchStatus or DeletePatch.
	env.patchStore.mu.Lock()
	updatedCount := len(env.patchStore.UpdatedPatches)
	deletedCount := len(env.patchStore.DeletedPatches)
	env.patchStore.mu.Unlock()

	if updatedCount != 0 {
		t.Errorf("expected 0 UpdatePatchStatus calls; got %d", updatedCount)
	}
	if deletedCount != 0 {
		t.Errorf("expected 0 DeletePatch calls; got %d", deletedCount)
	}

	// Verify no mutating git commands were run (no checkout, branch -f, reset, etc.).
	env.gitRunner.mu.Lock()
	for _, call := range env.gitRunner.RunCalls {
		if len(call.Args) > 0 {
			switch call.Args[0] {
			case "checkout", "reset", "branch":
				if len(call.Args) > 1 && call.Args[1] == "-f" {
					t.Errorf("unexpected mutating git command: %v", call.Args)
				}
			}
		}
	}
	env.gitRunner.mu.Unlock()
}

// ===========================================================================
// TS-NS-5: The endpoint returns 400 for non-carry-patch workspaces and 404
// for missing workspaces.
//
// Requirement: NS-REQ-5
// ===========================================================================

func TestRebuildPreview_StandardWorkspace_Returns400(t *testing.T) {
	env := newFullTestEnv(t)

	// Seed a standard-mode workspace.
	seedWorkspace(t, env.db, "standard-ws", "alice", "active", "ready", "standard", "")

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/standard-ws/rebuild-preview", "",
		rebuildUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET /rebuild-preview on standard workspace: status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// Verify error message mentions carry_patch.
	errResp := parseErrorEnvelope(t, rec)
	expected := "rebuild-preview is only supported for carry_patch workspaces"
	if errResp.Error.Message != expected {
		t.Errorf("error message = %q; want %q", errResp.Error.Message, expected)
	}
}

func TestRebuildPreview_MissingWorkspace_Returns404(t *testing.T) {
	env := newFullTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/nonexistent/rebuild-preview", "",
		rebuildUserAuth("alice"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /rebuild-preview on missing workspace: status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// ===========================================================================
// Auth tests for rebuild-preview
// ===========================================================================

func TestRebuildPreview_Unauthenticated_Returns401(t *testing.T) {
	env := newFullTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/rebuild-preview", "",
		nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /rebuild-preview unauthenticated: status = %d; want %d",
			rec.Code, http.StatusUnauthorized)
	}
}

func TestRebuildPreview_PAT_WithoutScope_Returns403(t *testing.T) {
	env := newFullTestEnv(t)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/rebuild-preview", "",
		rebuildPATAuth("alice", "workspaces:read"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /rebuild-preview without rebuilds:read scope: status = %d; want %d",
			rec.Code, http.StatusForbidden)
	}
}

func TestRebuildPreview_PAT_WithScope_Allowed(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/repo", "abc123", "integration", "")

	env.patchStore.mu.Lock()
	env.patchStore.Patches = []Patch{}
	env.patchStore.mu.Unlock()

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			return "upstream-sha", nil
		}
		return "", nil
	}

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/rebuild-preview", "",
		rebuildPATAuth("alice", "rebuilds:read"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rebuild-preview with rebuilds:read scope: status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// ===========================================================================
// Edge case: empty patch list returns empty array (not null)
// ===========================================================================

func TestRebuildPreview_NoPatches_ReturnsEmptyArray(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/repo", "abc123", "integration", "")

	env.patchStore.mu.Lock()
	env.patchStore.Patches = []Patch{}
	env.patchStore.mu.Unlock()

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			return "upstream-sha", nil
		}
		return "", nil
	}

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/rebuild-preview", "",
		rebuildUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rebuild-preview status = %d; want %d", rec.Code, http.StatusOK)
	}

	var resp RebuildPreviewResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.PatchResults == nil {
		t.Error("patch_results should be an empty array, not null")
	}
	if len(resp.PatchResults) != 0 {
		t.Errorf("expected 0 patch_results; got %d", len(resp.PatchResults))
	}
}

// ===========================================================================
// Edge case: disabled and merged_upstream patches are skipped
// ===========================================================================

func TestRebuildPreview_SkipsDisabledAndMergedPatches(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/repo", "abc123", "integration", "")

	env.patchStore.mu.Lock()
	env.patchStore.Patches = []Patch{
		{ID: "patch-1", WorkspaceID: "my-workspace", BranchName: "feature/disabled", Position: 1, Status: PatchStatusDisabled},
		{ID: "patch-2", WorkspaceID: "my-workspace", BranchName: "feature/merged", Position: 2, Status: PatchStatusMergedUpstream},
		{ID: "patch-3", WorkspaceID: "my-workspace", BranchName: "feature/active", Position: 3, Status: PatchStatusActive},
	}
	env.patchStore.mu.Unlock()

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			return "upstream-sha", nil
		}
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			return "branch-head-sha", nil
		}
		if len(args) >= 1 && args[0] == "commit-tree" {
			return "commit-sha-result", nil
		}
		return "", nil
	}
	env.gitRunner.MergeTreeFunc = func(_ context.Context, _, _ string) (string, error) {
		return "tree-sha", nil
	}

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/rebuild-preview", "",
		rebuildUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	var resp RebuildPreviewResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Only the active patch should appear in results.
	if len(resp.PatchResults) != 1 {
		t.Fatalf("expected 1 patch_result (only active); got %d", len(resp.PatchResults))
	}
	if resp.PatchResults[0].PatchID != "patch-3" {
		t.Errorf("expected patch-3 (active); got %q", resp.PatchResults[0].PatchID)
	}
}
