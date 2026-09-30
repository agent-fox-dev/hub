package carrypatch

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TS-02-29 (integration): GET /workspaces/:slug/patch-status includes the three fork-sync
// fields per patch and adds patches_diverged and patches_missing_on_origin summary counts.
// Verifies: 02-REQ-6.5
func TestPatchStatus_TS02_29_OriginSyncFieldsAndSummaryCounts(t *testing.T) {
	env := newFullTestEnv(t)
	slug := "cp-status-origin"

	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream",
		"aaaa000000000000000000000000000000000001",
		"integration",
		"bbbb000000000000000000000000000000000001",
	)

	// Seed 5 patches: in_sync, diverged, diverged, missing_on_origin, and null.
	seedPatch(t, env.db, "p1", slug, "feature/one", 1, PatchStatusActive)
	seedPatch(t, env.db, "p2", slug, "feature/two", 2, PatchStatusActive)
	seedPatch(t, env.db, "p3", slug, "feature/three", 3, PatchStatusConflict)
	seedPatch(t, env.db, "p4", slug, "feature/four", 4, PatchStatusDisabled)
	seedPatch(t, env.db, "p5", slug, "feature/five", 5, PatchStatusActive)

	// Update origin sync fields in database.
	stateInSync := "in_sync"
	sha1 := "1111111111111111111111111111111111111111"
	syncedAt1 := "2026-03-29T10:00:00Z"

	stateDiverged := "diverged"
	sha2 := "2222222222222222222222222222222222222222"
	syncedAt2 := "2026-03-29T10:01:00Z"

	sha3 := "3333333333333333333333333333333333333333"
	syncedAt3 := "2026-03-29T10:02:00Z"

	stateMissing := "missing_on_origin"
	syncedAt4 := "2026-03-29T10:03:00Z"

	if _, err := env.db.Exec(`UPDATE patches SET origin_sync_state = ?, origin_sha = ?, origin_synced_at = ? WHERE id = 'p1'`, stateInSync, sha1, syncedAt1); err != nil {
		t.Fatalf("failed to update p1: %v", err)
	}
	if _, err := env.db.Exec(`UPDATE patches SET origin_sync_state = ?, origin_sha = ?, origin_synced_at = ? WHERE id = 'p2'`, stateDiverged, sha2, syncedAt2); err != nil {
		t.Fatalf("failed to update p2: %v", err)
	}
	if _, err := env.db.Exec(`UPDATE patches SET origin_sync_state = ?, origin_sha = ?, origin_synced_at = ? WHERE id = 'p3'`, stateDiverged, sha3, syncedAt3); err != nil {
		t.Fatalf("failed to update p3: %v", err)
	}
	if _, err := env.db.Exec(`UPDATE patches SET origin_sync_state = ?, origin_sha = NULL, origin_synced_at = ? WHERE id = 'p4'`, stateMissing, syncedAt4); err != nil {
		t.Fatalf("failed to update p4: %v", err)
	}
	// p5 has origin_sync_state, origin_sha, origin_synced_at as NULL.

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patch-status", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp PatchStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify summary counts.
	if resp.Summary.PatchesDiverged != 2 {
		t.Errorf("summary.patches_diverged = %d; want 2", resp.Summary.PatchesDiverged)
	}
	if resp.Summary.PatchesMissingOnOrigin != 1 {
		t.Errorf("summary.patches_missing_on_origin = %d; want 1", resp.Summary.PatchesMissingOnOrigin)
	}

	// Also verify raw JSON keys are present on each patch entry.
	var rawResp struct {
		Patches []map[string]any `json:"patches"`
		Summary map[string]any   `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rawResp); err != nil {
		t.Fatalf("failed to decode raw response: %v", err)
	}

	if len(rawResp.Patches) != 5 {
		t.Fatalf("got %d patches; want 5", len(rawResp.Patches))
	}

	// Each patch entry in the response carries origin_sync_state, origin_sha and origin_synced_at
	for i, p := range rawResp.Patches {
		if _, ok := p["origin_sync_state"]; !ok {
			t.Errorf("patch[%d] missing 'origin_sync_state'", i)
		}
		if _, ok := p["origin_sha"]; !ok {
			t.Errorf("patch[%d] missing 'origin_sha'", i)
		}
		if _, ok := p["origin_synced_at"]; !ok {
			t.Errorf("patch[%d] missing 'origin_synced_at'", i)
		}
	}

	// Check specific values
	p1 := rawResp.Patches[0]
	if p1["origin_sync_state"] != stateInSync {
		t.Errorf("p1 origin_sync_state = %v; want %q", p1["origin_sync_state"], stateInSync)
	}
	if p1["origin_sha"] != sha1 {
		t.Errorf("p1 origin_sha = %v; want %q", p1["origin_sha"], sha1)
	}
	if p1["origin_synced_at"] != syncedAt1 {
		t.Errorf("p1 origin_synced_at = %v; want %q", p1["origin_synced_at"], syncedAt1)
	}

	p5 := rawResp.Patches[4]
	if p5["origin_sync_state"] != nil {
		t.Errorf("p5 origin_sync_state = %v; want nil", p5["origin_sync_state"])
	}
	if p5["origin_sha"] != nil {
		t.Errorf("p5 origin_sha = %v; want nil", p5["origin_sha"])
	}
	if p5["origin_synced_at"] != nil {
		t.Errorf("p5 origin_synced_at = %v; want nil", p5["origin_synced_at"])
	}
}
