package carrypatch

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// ===========================================================================
// TS-16-19: GET /api/v1/workspaces/:slug/patch-status returns HTTP 200 with
// a fully aggregated response including workspace metadata, last_rebuild,
// patches array with last_rebuild_result and rerere_resolution_count, and
// summary counts.
//
// Requirement: 16-REQ-6.1
// ===========================================================================

func TestPatchStatus_Returns200WithFullDashboard(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	// Seed 3 patches: 2 active, 1 conflict.
	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	seedPatch(t, env.db, "p2", "my-workspace", "feature/b", 2, PatchStatusActive)
	seedPatch(t, env.db, "p3", "my-workspace", "feature/conflict", 3, PatchStatusConflict)

	// Update the mock PatchStore to return the patches.
	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", WorkspaceID: "my-workspace", BranchName: "feature/b", Position: 2, Status: PatchStatusActive},
		{ID: "p3", WorkspaceID: "my-workspace", BranchName: "feature/conflict", Position: 3, Status: PatchStatusConflict},
	}

	// Seed a completed rebuild job.
	patchResults := []PatchResult{
		{PatchID: "p1", BranchName: "feature/a", Position: 1, Status: "success"},
		{PatchID: "p2", BranchName: "feature/b", Position: 2, Status: "success"},
		{PatchID: "p3", BranchName: "feature/conflict", Position: 3, Status: "conflict", ConflictFiles: []string{"pkg/api.go"}},
	}
	rebuildResult := RebuildResult{
		UpstreamHeadSHA:    "aaaa000000000000000000000000000000000001",
		IntegrationHeadSHA: "bbbb000000000000000000000000000000000001",
		Strategy:           "rebase",
		PatchesApplied:     2,
		PatchResults:       patchResults,
	}
	resultJSON, _ := json.Marshal(rebuildResult)
	seedRebuildJobWithResult(t, env.db, "rebuild-1", "completed", "my-workspace", "rebase",
		time.Now().UTC(), resultJSON)

	// Set up rr-cache with 1 resolution relevant to the conflict patch.
	entries := []rrCacheEntry{
		{hash: "aabbccdd1", preimage: "<<<<<<< pkg/api.go\nours\n=======\ntheirs\n>>>>>>>"},
	}
	setupRRCacheDir(t, env.workspaceRoot, "my-workspace", entries)

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/patch-status", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /patch-status status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp PatchStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v (body: %s)", err, rec.Body.String())
	}

	// Verify workspace metadata.
	if resp.WorkspaceSlug != "my-workspace" {
		t.Errorf("expected workspace_slug='my-workspace', got %q", resp.WorkspaceSlug)
	}
	if resp.WorkspaceMode != "carry_patch" {
		t.Errorf("expected workspace_mode='carry_patch', got %q", resp.WorkspaceMode)
	}
	if resp.UpstreamURL == "" {
		t.Error("expected non-empty upstream_url")
	}
	if resp.UpstreamHeadSHA == "" {
		t.Error("expected non-empty upstream_head_sha")
	}
	if resp.IntegrationBranch != "integration" {
		t.Errorf("expected integration_branch='integration', got %q", resp.IntegrationBranch)
	}
	if resp.IntegrationHeadSHA == "" {
		t.Error("expected non-empty integration_head_sha")
	}

	// Verify last_rebuild is present.
	if resp.LastRebuild == nil {
		t.Fatal("expected non-nil last_rebuild")
	}
	if resp.LastRebuild.ID == "" {
		t.Error("expected non-empty last_rebuild.id")
	}
	if resp.LastRebuild.Status != "completed" {
		t.Errorf("expected last_rebuild.status='completed', got %q", resp.LastRebuild.Status)
	}

	// Verify patches array.
	if len(resp.Patches) != 3 {
		t.Fatalf("expected 3 patches, got %d", len(resp.Patches))
	}

	// Verify summary counts.
	if resp.Summary.TotalPatches != 3 {
		t.Errorf("expected total_patches=3, got %d", resp.Summary.TotalPatches)
	}
	if resp.Summary.Active != 2 {
		t.Errorf("expected active=2, got %d", resp.Summary.Active)
	}
	if resp.Summary.Conflict != 1 {
		t.Errorf("expected conflict=1, got %d", resp.Summary.Conflict)
	}
}

// ===========================================================================
// TS-16-20: The summary object in the patch-status response has counts that
// are accurate and consistent with the patches array.
//
// Requirement: 16-REQ-6.2
// Property: 16-PROP-8
// ===========================================================================

func TestPatchStatus_SummaryCounts_Consistent(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	// 5 patches: 2 active, 1 merged_upstream, 1 conflict, 1 disabled.
	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	seedPatch(t, env.db, "p2", "my-workspace", "feature/b", 2, PatchStatusActive)
	seedPatch(t, env.db, "p3", "my-workspace", "feature/merged", 3, PatchStatusMergedUpstream)
	seedPatch(t, env.db, "p4", "my-workspace", "feature/conflict", 4, PatchStatusConflict)
	seedPatch(t, env.db, "p5", "my-workspace", "feature/disabled", 5, PatchStatusDisabled)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", WorkspaceID: "my-workspace", BranchName: "feature/b", Position: 2, Status: PatchStatusActive},
		{ID: "p3", WorkspaceID: "my-workspace", BranchName: "feature/merged", Position: 3, Status: PatchStatusMergedUpstream},
		{ID: "p4", WorkspaceID: "my-workspace", BranchName: "feature/conflict", Position: 4, Status: PatchStatusConflict},
		{ID: "p5", WorkspaceID: "my-workspace", BranchName: "feature/disabled", Position: 5, Status: PatchStatusDisabled},
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/patch-status", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /patch-status status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp PatchStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	s := resp.Summary

	// 16-PROP-8: total_patches equals len(patches).
	if s.TotalPatches != len(resp.Patches) {
		t.Errorf("summary.total_patches=%d != len(patches)=%d", s.TotalPatches, len(resp.Patches))
	}

	// 16-PROP-8: sum of status counts equals total_patches.
	sum := s.Active + s.MergedUpstream + s.Conflict + s.Disabled
	if sum != s.TotalPatches {
		t.Errorf("active(%d) + merged_upstream(%d) + conflict(%d) + disabled(%d) = %d != total_patches(%d)",
			s.Active, s.MergedUpstream, s.Conflict, s.Disabled, sum, s.TotalPatches)
	}

	// Verify individual counts.
	if s.TotalPatches != 5 {
		t.Errorf("expected total_patches=5, got %d", s.TotalPatches)
	}
	if s.Active != 2 {
		t.Errorf("expected active=2, got %d", s.Active)
	}
	if s.MergedUpstream != 1 {
		t.Errorf("expected merged_upstream=1, got %d", s.MergedUpstream)
	}
	if s.Conflict != 1 {
		t.Errorf("expected conflict=1, got %d", s.Conflict)
	}
	if s.Disabled != 1 {
		t.Errorf("expected disabled=1, got %d", s.Disabled)
	}
}

// ===========================================================================
// TS-16-21: When no rebuild has been attempted for the workspace,
// patch-status returns last_rebuild=null and all patch last_rebuild_result
// fields as null.
//
// Requirement: 16-REQ-6.3
// ===========================================================================

func TestPatchStatus_NoRebuild_NullFields(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"",
	)

	// 2 active patches, no rebuild jobs.
	seedPatch(t, env.db, "p1", "my-workspace", "feature/a", 1, PatchStatusActive)
	seedPatch(t, env.db, "p2", "my-workspace", "feature/b", 2, PatchStatusActive)

	env.patchStore.Patches = []Patch{
		{ID: "p1", WorkspaceID: "my-workspace", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", WorkspaceID: "my-workspace", BranchName: "feature/b", Position: 2, Status: PatchStatusActive},
	}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/patch-status", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /patch-status (no rebuild) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp PatchStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// last_rebuild should be null.
	if resp.LastRebuild != nil {
		t.Errorf("expected last_rebuild=null when no rebuild attempted, got %+v", resp.LastRebuild)
	}

	// Each patch's last_rebuild_result should be null.
	for _, patch := range resp.Patches {
		if patch.LastRebuildResult != nil {
			t.Errorf("expected last_rebuild_result=null for patch %q, got %q",
				patch.BranchName, *patch.LastRebuildResult)
		}
	}
}

// 16-REQ-6.E1: Workspace not in carry_patch mode returns 400.
func TestPatchStatus_StandardMode_Returns400(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "ws-std", "alice", "active", "ready", "standard", "")

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-std/patch-status", "", auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET /patch-status (standard mode) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for non-carry_patch workspace")
	}
}

// 16-REQ-6.E2: PAT without 'workspaces:read' scope returns 403.
func TestPatchStatus_PATWithoutScope_Returns403(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	auth := rebuildPATAuth("alice", "rebuilds:read") // no workspaces:read
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/patch-status", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /patch-status (PAT without scope) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// 16-REQ-6.E3: Empty patches table returns empty array and zero summary.
func TestPatchStatus_EmptyPatches_ReturnsZeroCounts(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspaceCarryPatch(t, env.db, "my-workspace", "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	// No patches seeded.
	env.patchStore.Patches = []Patch{}

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/patch-status", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /patch-status (no patches) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp PatchStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Patches) != 0 {
		t.Errorf("expected 0 patches, got %d", len(resp.Patches))
	}

	s := resp.Summary
	if s.TotalPatches != 0 {
		t.Errorf("expected total_patches=0, got %d", s.TotalPatches)
	}
	if s.Active != 0 || s.MergedUpstream != 0 || s.Conflict != 0 || s.Disabled != 0 {
		t.Errorf("expected all summary counts=0, got active=%d, merged=%d, conflict=%d, disabled=%d",
			s.Active, s.MergedUpstream, s.Conflict, s.Disabled)
	}
}

// 16-REQ-6.E4: If rr-cache is inaccessible, rerere_resolution_count=0 for all patches.
func TestPatchStatus_InaccessibleRRCache_ZeroResolutionCount(t *testing.T) {
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

	// Don't set up rr-cache directory — it's inaccessible.

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/patch-status", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /patch-status (no rr-cache) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp PatchStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	for _, patch := range resp.Patches {
		if patch.RerereResolutionCount != 0 {
			t.Errorf("expected rerere_resolution_count=0 for patch %q (rr-cache inaccessible), got %d",
				patch.BranchName, patch.RerereResolutionCount)
		}
	}
}
