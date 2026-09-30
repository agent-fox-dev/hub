package workspace

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TS-02-28 (integration): GET and PATCH patch endpoints expose the three fork-sync fields,
// null when unset, matching the upstream_pr_url convention.
// Verifies: 02-REQ-6.4
func TestCarryPatch_TS02_28_PatchEndpointsExposeOriginSyncFields(t *testing.T) {
	slug := "cp-origin-fields"
	env := newPatchTestEnv(t, slug, "deploy")
	auth := userAuth("user-1")

	// Seed two patches: one with origin fields set, one with them unset.
	setID := "patch-set-1"
	unsetID := "patch-unset-2"
	seedPatchRaw(t, env.db, setID, slug, "feature/set", 1)
	seedPatchRaw(t, env.db, unsetID, slug, "feature/unset", 2)

	// Set origin sync fields on setID.
	originState := "in_sync"
	originSHA := "abcdef1234567890abcdef1234567890abcdef12"
	originSyncedAt := "2026-03-29T10:00:00Z"
	_, err := env.db.Exec(
		`UPDATE patches SET origin_sync_state = ?, origin_sha = ?, origin_synced_at = ? WHERE id = ?`,
		originState, originSHA, originSyncedAt, setID,
	)
	if err != nil {
		t.Fatalf("failed to update origin sync fields: %v", err)
	}

	// 1. GET /api/v1/workspaces/:slug/patches
	recList := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches", "", auth)
	if recList.Code != http.StatusOK {
		t.Fatalf("GET /patches status = %d; want %d; body: %s", recList.Code, http.StatusOK, recList.Body.String())
	}
	var listResp []map[string]any
	if err := json.Unmarshal(recList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("failed to decode GET /patches response: %v", err)
	}
	if len(listResp) != 2 {
		t.Fatalf("got %d patches; want 2", len(listResp))
	}
	var setInList, unsetInList map[string]any
	for _, p := range listResp {
		if p["id"] == setID {
			setInList = p
		} else if p["id"] == unsetID {
			unsetInList = p
		}
	}
	if setInList == nil || unsetInList == nil {
		t.Fatalf("patches not found in list response: set=%v, unset=%v", setInList, unsetInList)
	}
	if state, ok := setInList["origin_sync_state"]; !ok || state != originState {
		t.Errorf("list set patch origin_sync_state = %v; want %q", state, originState)
	}
	if sha, ok := setInList["origin_sha"]; !ok || sha != originSHA {
		t.Errorf("list set patch origin_sha = %v; want %q", sha, originSHA)
	}
	if syncedAt, ok := setInList["origin_synced_at"]; !ok || syncedAt != originSyncedAt {
		t.Errorf("list set patch origin_synced_at = %v; want %q", syncedAt, originSyncedAt)
	}
	if state, ok := unsetInList["origin_sync_state"]; !ok || state != nil {
		t.Errorf("list unset patch origin_sync_state = %v; want nil", state)
	}
	if sha, ok := unsetInList["origin_sha"]; !ok || sha != nil {
		t.Errorf("list unset patch origin_sha = %v; want nil", sha)
	}
	if syncedAt, ok := unsetInList["origin_synced_at"]; !ok || syncedAt != nil {
		t.Errorf("list unset patch origin_synced_at = %v; want nil", syncedAt)
	}

	// 2. GET /api/v1/workspaces/:slug/patches/:id (set patch)
	recGetSet := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches/"+setID, "", auth)
	if recGetSet.Code != http.StatusOK {
		t.Fatalf("GET /patches/%s status = %d; want %d; body: %s", setID, recGetSet.Code, http.StatusOK, recGetSet.Body.String())
	}
	var getSetResp map[string]any
	if err := json.Unmarshal(recGetSet.Body.Bytes(), &getSetResp); err != nil {
		t.Fatalf("failed to decode GET /patches/%s response: %v", setID, err)
	}
	if state, ok := getSetResp["origin_sync_state"]; !ok || state != originState {
		t.Errorf("GET set patch origin_sync_state = %v; want %q", state, originState)
	}
	if sha, ok := getSetResp["origin_sha"]; !ok || sha != originSHA {
		t.Errorf("GET set patch origin_sha = %v; want %q", sha, originSHA)
	}
	if syncedAt, ok := getSetResp["origin_synced_at"]; !ok || syncedAt != originSyncedAt {
		t.Errorf("GET set patch origin_synced_at = %v; want %q", syncedAt, originSyncedAt)
	}

	// 3. GET /api/v1/workspaces/:slug/patches/:id (unset patch)
	recGetUnset := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug+"/patches/"+unsetID, "", auth)
	if recGetUnset.Code != http.StatusOK {
		t.Fatalf("GET /patches/%s status = %d; want %d; body: %s", unsetID, recGetUnset.Code, http.StatusOK, recGetUnset.Body.String())
	}
	var getUnsetResp map[string]any
	if err := json.Unmarshal(recGetUnset.Body.Bytes(), &getUnsetResp); err != nil {
		t.Fatalf("failed to decode GET /patches/%s response: %v", unsetID, err)
	}
	if state, ok := getUnsetResp["origin_sync_state"]; !ok || state != nil {
		t.Errorf("GET unset patch origin_sync_state = %v; want nil", state)
	}
	if sha, ok := getUnsetResp["origin_sha"]; !ok || sha != nil {
		t.Errorf("GET unset patch origin_sha = %v; want nil", sha)
	}
	if syncedAt, ok := getUnsetResp["origin_synced_at"]; !ok || syncedAt != nil {
		t.Errorf("GET unset patch origin_synced_at = %v; want nil", syncedAt)
	}

	// 4. PATCH /api/v1/workspaces/:slug/patches/:id (set patch)
	patchBody := `{"description": "updated description"}`
	recPatchSet := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/"+setID, patchBody, auth)
	if recPatchSet.Code != http.StatusOK {
		t.Fatalf("PATCH /patches/%s status = %d; want %d; body: %s", setID, recPatchSet.Code, http.StatusOK, recPatchSet.Body.String())
	}
	var patchSetResp map[string]any
	if err := json.Unmarshal(recPatchSet.Body.Bytes(), &patchSetResp); err != nil {
		t.Fatalf("failed to decode PATCH /patches/%s response: %v", setID, err)
	}
	if state, ok := patchSetResp["origin_sync_state"]; !ok || state != originState {
		t.Errorf("PATCH set patch origin_sync_state = %v; want %q", state, originState)
	}
	if sha, ok := patchSetResp["origin_sha"]; !ok || sha != originSHA {
		t.Errorf("PATCH set patch origin_sha = %v; want %q", sha, originSHA)
	}
	if syncedAt, ok := patchSetResp["origin_synced_at"]; !ok || syncedAt != originSyncedAt {
		t.Errorf("PATCH set patch origin_synced_at = %v; want %q", syncedAt, originSyncedAt)
	}

	// 5. PATCH /api/v1/workspaces/:slug/patches/:id (unset patch)
	recPatchUnset := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug+"/patches/"+unsetID, patchBody, auth)
	if recPatchUnset.Code != http.StatusOK {
		t.Fatalf("PATCH /patches/%s status = %d; want %d; body: %s", unsetID, recPatchUnset.Code, http.StatusOK, recPatchUnset.Body.String())
	}
	var patchUnsetResp map[string]any
	if err := json.Unmarshal(recPatchUnset.Body.Bytes(), &patchUnsetResp); err != nil {
		t.Fatalf("failed to decode PATCH /patches/%s response: %v", unsetID, err)
	}
	if state, ok := patchUnsetResp["origin_sync_state"]; !ok || state != nil {
		t.Errorf("PATCH unset patch origin_sync_state = %v; want nil", state)
	}
	if sha, ok := patchUnsetResp["origin_sha"]; !ok || sha != nil {
		t.Errorf("PATCH unset patch origin_sha = %v; want nil", sha)
	}
	if syncedAt, ok := patchUnsetResp["origin_synced_at"]; !ok || syncedAt != nil {
		t.Errorf("PATCH unset patch origin_synced_at = %v; want nil", syncedAt)
	}
}
