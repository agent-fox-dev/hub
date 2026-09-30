package carrypatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// ===========================================================================
// TS-02-17 (unit): Sync returns early with empty patches_merged and
// rebuild_triggered false when upstream did not advance and no patch advanced
//
// Verifies: 02-REQ-4.2
// ===========================================================================

func TestCarryPatchSync_TS02_17_UpstreamUnchanged_NoPatchAdvanced_ReturnsEarly(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-17"
	upstreamSHA := "aaaa000000000000000000000000000000000001"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		upstreamSHA,
		"integration",
		"bbbb000000000000000000000000000000000001",
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

	patch := Patch{ID: "p17", WorkspaceID: slug, BranchName: "feature-insync", Position: 1, Status: PatchStatusActive}
	env.patchStore.Patches = []Patch{patch}

	// Both local and origin tips are the same SHA => in_sync => no patch advanced
	sameSHA := "sha-same-17"
	refs := map[string]string{
		"refs/remotes/upstream/HEAD":        upstreamSHA,
		"refs/remotes/upstream/HEAD^{commit}": upstreamSHA,
		"refs/heads/feature-insync":          sameSHA,
		"refs/remotes/origin/feature-insync": sameSHA,
	}

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			ref := args[2]
			if sha, ok := refs[ref]; ok {
				return sha, nil
			}
			return "", fmt.Errorf("ref not found: %s", ref)
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
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.PatchesMerged) != 0 {
		t.Errorf("expected empty patches_merged, got %v", resp.PatchesMerged)
	}
	if resp.RebuildTriggered {
		t.Error("expected rebuild_triggered=false when neither upstream nor patch advanced")
	}
	if resp.RebuildJobID != nil {
		t.Errorf("expected rebuild_job_id to be nil, got %v", *resp.RebuildJobID)
	}

	// Verify no job was enqueued
	var jobCount int
	err := env.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type = 'rebuild' AND key = ?`, slug).Scan(&jobCount)
	if err != nil {
		t.Fatalf("failed to query jobs count: %v", err)
	}
	if jobCount != 0 {
		t.Errorf("expected 0 rebuild jobs enqueued, got %d", jobCount)
	}
}

// ===========================================================================
// TS-02-18 (unit): last_sync_at is updated on every error-free sync including
// the early-return no-op case
//
// Verifies: 02-REQ-4.3
// ===========================================================================

func TestCarryPatchSync_TS02_18_LastSyncAtUpdatedOnEarlyReturn(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-18"
	upstreamSHA := "aaaa000000000000000000000000000000000001"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		upstreamSHA,
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	// Set an old last_sync_at in the past
	oldTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	_, err := env.db.Exec("UPDATE workspaces SET last_sync_at = ? WHERE slug = ?", oldTime, slug)
	if err != nil {
		t.Fatalf("failed to set old last_sync_at: %v", err)
	}

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
		}
		return "", nil
	}

	patch := Patch{ID: "p18", WorkspaceID: slug, BranchName: "feature-old", Position: 1, Status: PatchStatusActive}
	env.patchStore.Patches = []Patch{patch}

	// Both local and origin tips are the same SHA => in_sync => no patch advanced, upstream unchanged
	sameSHA := "sha-same-18"
	refs := map[string]string{
		"refs/remotes/upstream/HEAD":        upstreamSHA,
		"refs/remotes/upstream/HEAD^{commit}": upstreamSHA,
		"refs/heads/feature-old":          sameSHA,
		"refs/remotes/origin/feature-old": sameSHA,
	}

	env.gitRunner.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify" {
			ref := args[2]
			if sha, ok := refs[ref]; ok {
				return sha, nil
			}
			return "", fmt.Errorf("ref not found: %s", ref)
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
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.RebuildTriggered {
		t.Fatal("expected early return with rebuild_triggered=false")
	}

	// Verify last_sync_at was updated to a more recent time
	var afterSyncStr string
	err = env.db.QueryRow("SELECT last_sync_at FROM workspaces WHERE slug = ?", slug).Scan(&afterSyncStr)
	if err != nil {
		t.Fatalf("failed to query last_sync_at: %v", err)
	}
	if afterSyncStr == oldTime {
		t.Errorf("last_sync_at was not updated; still %s", oldTime)
	}
	afterTime, parseErr := time.Parse(time.RFC3339, afterSyncStr)
	if parseErr != nil {
		t.Fatalf("failed to parse updated last_sync_at %q: %v", afterSyncStr, parseErr)
	}
	oldParsed, _ := time.Parse(time.RFC3339, oldTime)
	if !afterTime.After(oldParsed) {
		t.Errorf("expected afterTime (%v) > oldTime (%v)", afterTime, oldParsed)
	}
}

// ===========================================================================
// TS-02-19 (unit): An auto-rebuild is enqueued with the existing group key
// and duplicate suppression when the sync did not return early and
// AUTO_REBUILD_AFTER_SYNC is not the string false
//
// Verifies: 02-REQ-4.4
// ===========================================================================

func TestCarryPatchSync_TS02_19_AutoRebuildEnqueuedOnPatchAdvance(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-19"
	upstreamSHA := "aaaa000000000000000000000000000000000001"
	integrationBranch := "integration"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		upstreamSHA,
		integrationBranch,
		"bbbb000000000000000000000000000000000001",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
		}
		// AUTO_REBUILD_AFTER_SYNC unset (defaults to true)
		return "", nil
	}

	patch := Patch{ID: "p19", WorkspaceID: slug, BranchName: "feature-adv", Position: 1, Status: PatchStatusActive}
	env.patchStore.Patches = []Patch{patch}

	// Upstream HEAD is unchanged (upstream did not advance),
	// but feature-adv is fast-forwarded (shaA is ancestor of shaB) -> patch advanced!
	shaA := "sha-local-19"
	shaB := "sha-origin-19"
	refs := map[string]string{
		"refs/remotes/upstream/HEAD":        upstreamSHA,
		"refs/remotes/upstream/HEAD^{commit}": upstreamSHA,
		"refs/heads/feature-adv":          shaA,
		"refs/remotes/origin/feature-adv": shaB,
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
			return "", fmt.Errorf("ref not found: %s", ref)
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

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !resp.RebuildTriggered {
		t.Fatal("expected rebuild_triggered=true when patch advanced")
	}
	if resp.RebuildJobID == nil {
		t.Fatal("expected rebuild_job_id to be non-nil")
	}

	// Verify job enqueued with correct group key in jobs table
	var groupKey string
	err := env.db.QueryRow(`SELECT group_key FROM jobs WHERE id = ?`, *resp.RebuildJobID).Scan(&groupKey)
	if err != nil {
		t.Fatalf("failed to query group_key: %v", err)
	}
	expectedGroup := slug + ":" + integrationBranch
	if groupKey != expectedGroup {
		t.Errorf("job.group_key = %q; want %q", groupKey, expectedGroup)
	}

	// Second sync: duplicate queued job for the same group is silently ignored
	rec2 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second POST /sync failed: status=%d body=%s", rec2.Code, rec2.Body.String())
	}
	var resp2 CarryPatchSyncResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp2); err != nil {
		t.Fatalf("failed to decode second response: %v", err)
	}
	// On second sync: local ref is already shaB, so in_sync -> returns early; rebuild_triggered=false
	if resp2.RebuildTriggered {
		t.Errorf("expected rebuild_triggered=false on duplicate sync")
	}

	// Also test duplicate suppression directly when patch advances again while earlier job is still queued
	refs["refs/heads/feature-adv"] = "sha-local-19b"
	refs["refs/remotes/origin/feature-adv"] = "sha-origin-19b"
	env.gitRunner.IsAncestorFunc = func(_ context.Context, ancestor, descendant string) (bool, error) {
		if ancestor == "sha-local-19b" && descendant == "sha-origin-19b" {
			return true, nil
		}
		return false, nil
	}

	rec3 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", auth)
	if rec3.Code != http.StatusOK {
		t.Fatalf("third POST /sync failed: status=%d body=%s", rec3.Code, rec3.Body.String())
	}
	var resp3 CarryPatchSyncResponse
	if err := json.NewDecoder(rec3.Body).Decode(&resp3); err != nil {
		t.Fatalf("failed to decode third response: %v", err)
	}
	// Patch advanced, but job with group slug:integration is already queued -> duplicate suppressed
	if resp3.RebuildTriggered {
		t.Errorf("expected rebuild_triggered=false when duplicate job is in queue")
	}
	if resp3.RebuildJobID != nil {
		t.Errorf("expected rebuild_job_id=nil when duplicate job is suppressed")
	}
}

// ===========================================================================
// TS-02-20 (unit): Merge-upstream detection runs over branch tips as refreshed
// by the fork-refresh step, not their pre-sync tips
//
// Verifies: 02-REQ-4.5
// ===========================================================================

func TestCarryPatchSync_TS02_20_MergeUpstreamDetectionUsesRefreshedTips(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "ws-ts02-20"
	oldUpstreamSHA := "upstream-old"
	newUpstreamSHA := "upstream-new"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		oldUpstreamSHA,
		"integration",
		"integration-branch",
	)

	env.getVariable = func(scope, scopeID, key string) (string, error) {
		if scope == "workspace" && scopeID == slug {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
		}
		return "", nil
	}

	patch := Patch{ID: "p20", WorkspaceID: slug, BranchName: "feature-merged-post-refresh", Position: 1, Status: PatchStatusActive}
	env.patchStore.Patches = []Patch{patch}

	// Pre-sync tip: shaPreSync (NOT an ancestor of upstream-new)
	// Origin tip: shaPostRefresh (IS an ancestor of upstream-new, and descendant of shaPreSync)
	shaPreSync := "sha-pre-sync"
	shaPostRefresh := "sha-post-refresh"

	refs := map[string]string{
		"refs/remotes/upstream/HEAD":                 newUpstreamSHA,
		"refs/remotes/upstream/HEAD^{commit}":        newUpstreamSHA,
		"refs/heads/feature-merged-post-refresh":          shaPreSync,
		"refs/remotes/origin/feature-merged-post-refresh": shaPostRefresh,
	}

	env.gitRunner.IsAncestorFunc = func(_ context.Context, ancestor, descendant string) (bool, error) {
		// During fork-refresh: check if shaPreSync is ancestor of shaPostRefresh -> true (fast-forward)
		if ancestor == shaPreSync && descendant == shaPostRefresh {
			return true, nil
		}
		// Force-push check: oldUpstreamSHA vs newUpstreamSHA
		if ancestor == oldUpstreamSHA && descendant == newUpstreamSHA {
			return true, nil
		}
		// Merge detection check: checks patch.BranchName ("feature-merged-post-refresh") vs newUpstreamSHA
		if ancestor == "feature-merged-post-refresh" || ancestor == shaPostRefresh {
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
			return "", fmt.Errorf("ref not found: %s", ref)
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

	var resp CarryPatchSyncResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify that the branch was fast-forwarded in refs
	if refs["refs/heads/feature-merged-post-refresh"] != shaPostRefresh {
		t.Fatalf("branch tip was not fast-forwarded to shaPostRefresh: got %s", refs["refs/heads/feature-merged-post-refresh"])
	}

	// Verify that the patch was detected as merged upstream
	foundMerged := false
	for _, b := range resp.PatchesMerged {
		if b == "feature-merged-post-refresh" {
			foundMerged = true
			break
		}
	}
	if !foundMerged {
		t.Errorf("expected 'feature-merged-post-refresh' in patches_merged, got: %v", resp.PatchesMerged)
	}

	// Verify patch status was updated to merged_upstream in store
	updated, ok := env.patchStore.UpdatedPatches["p20"]
	if !ok {
		t.Fatalf("expected patch p20 to be updated in patch store")
	}
	if updated.Status != PatchStatusMergedUpstream {
		t.Errorf("patch status = %q; want %q", updated.Status, PatchStatusMergedUpstream)
	}
}
