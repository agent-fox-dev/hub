package carrypatch

import (
	"encoding/json"
	"net/http"
	"testing"
)

// ===========================================================================
// TS-16-1: POST /api/v1/workspaces/:slug/rebuild on a valid carry_patch
// workspace enqueues a rebuild job with correct key, group_key, and strategy
// captured at enqueue time, returning HTTP 202 with the queued job record.
//
// Requirement: 16-REQ-1.1
// ===========================================================================

func TestSubmitRebuild_ValidRequest_Returns202(t *testing.T) {
	env := newRebuildTestEnv(t)

	// Seed an active carry_patch workspace with clone_status=ready.
	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")

	// Seed at least one active patch.
	seedPatch(t, env.db, "patch-1", "my-workspace", "feature/foo", 1, PatchStatusActive)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/rebuild", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /rebuild status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	resp := parseRebuildJobResponse(t, rec)

	if resp.ID == "" {
		t.Error("expected non-empty job ID in response")
	}
	if resp.Status != "queued" {
		t.Errorf("expected status='queued', got %q", resp.Status)
	}
	if resp.Type != "rebuild" {
		t.Errorf("expected type='rebuild', got %q", resp.Type)
	}
	if resp.Key != "my-workspace" {
		t.Errorf("expected key='my-workspace', got %q", resp.Key)
	}
	// group_key should be '<workspace_slug>:<integration_branch>'
	expectedGroupKey := "my-workspace:integration"
	if resp.GroupKey != expectedGroupKey {
		t.Errorf("expected group_key=%q, got %q", expectedGroupKey, resp.GroupKey)
	}

	// Verify payload contains captured strategy.
	var payload RebuildPayload
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.Strategy != "rebase" {
		t.Errorf("expected payload.strategy='rebase', got %q", payload.Strategy)
	}
	if payload.WorkspaceSlug != "my-workspace" {
		t.Errorf("expected payload.workspace_slug='my-workspace', got %q", payload.WorkspaceSlug)
	}
}

// Test that the REBUILD_STRATEGY is captured at enqueue time, not at
// execution time. This validates 16-PROP-3.
func TestSubmitRebuild_StrategyCapturedAtEnqueueTime(t *testing.T) {
	// Override GetVariable to return 'merge' strategy.
	mergeEnv := newRebuildTestEnvWithStrategy(t, "merge")

	seedWorkspace(t, mergeEnv.db, "ws-merge", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, mergeEnv.db, "patch-m1", "ws-merge", "feature/bar", 1, PatchStatusActive)

	rec := mergeEnv.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-merge/rebuild", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /rebuild status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	resp := parseRebuildJobResponse(t, rec)
	var payload RebuildPayload
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.Strategy != "merge" {
		t.Errorf("expected payload.strategy='merge' (captured at enqueue time), got %q", payload.Strategy)
	}
}

// Test that when REBUILD_STRATEGY is not set, defaults to 'rebase'.
func TestSubmitRebuild_DefaultStrategy_IsRebase(t *testing.T) {
	env := newRebuildTestEnvWithStrategy(t, "")

	seedWorkspace(t, env.db, "ws-default", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-d1", "ws-default", "feature/foo", 1, PatchStatusActive)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-default/rebuild", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /rebuild status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	resp := parseRebuildJobResponse(t, rec)
	var payload RebuildPayload
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.Strategy != "rebase" {
		t.Errorf("expected default strategy='rebase', got %q", payload.Strategy)
	}
}

// ===========================================================================
// 16-REQ-1.E1: POST /rebuild on a 'standard' mode workspace returns HTTP 400.
// ===========================================================================

func TestSubmitRebuild_StandardMode_Returns400(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-std", "alice", "active", "ready", "standard", "")

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-std/rebuild", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /rebuild (standard mode) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for standard mode workspace")
	}
}

// ===========================================================================
// 16-REQ-1.E2: POST /rebuild when workspace is not active or clone not ready
// returns HTTP 400.
// ===========================================================================

func TestSubmitRebuild_WorkspaceNotActive_Returns400(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-archived", "alice", "archived", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-a1", "ws-archived", "feature/foo", 1, PatchStatusActive)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-archived/rebuild", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /rebuild (archived workspace) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for archived workspace")
	}
}

func TestSubmitRebuild_CloneNotReady_Returns400(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-cloning", "alice", "active", "cloning", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-c1", "ws-cloning", "feature/foo", 1, PatchStatusActive)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-cloning/rebuild", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /rebuild (clone not ready) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for clone not ready")
	}
}

// ===========================================================================
// 16-REQ-1.E3: POST /rebuild when no patches have status 'active' or
// 'conflict' returns HTTP 400.
// ===========================================================================

func TestSubmitRebuild_NoActivePatches_Returns400(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-nopatch", "alice", "active", "ready", "carry_patch", "integration")

	// Seed only disabled and merged_upstream patches — no active or conflict.
	seedPatch(t, env.db, "patch-d1", "ws-nopatch", "feature/disabled", 1, PatchStatusDisabled)
	seedPatch(t, env.db, "patch-m1", "ws-nopatch", "feature/merged", 2, PatchStatusMergedUpstream)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-nopatch/rebuild", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /rebuild (no active patches) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for no active patches")
	}
}

func TestSubmitRebuild_EmptyPatchList_Returns400(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-empty", "alice", "active", "ready", "carry_patch", "integration")
	// No patches at all.

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-empty/rebuild", "", rebuildUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /rebuild (empty patch list) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// ===========================================================================
// 16-REQ-1.E4: POST /rebuild when a rebuild job is already queued or running
// returns HTTP 409.
// ===========================================================================

func TestSubmitRebuild_DuplicateJob_Returns409(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-dup", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-dup1", "ws-dup", "feature/foo", 1, PatchStatusActive)

	// First submission should succeed.
	rec1 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-dup/rebuild", "", rebuildUserAuth("alice"))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first POST /rebuild status = %d; want %d; body = %s",
			rec1.Code, http.StatusAccepted, rec1.Body.String())
	}

	// Second submission should return 409.
	rec2 := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-dup/rebuild", "", rebuildUserAuth("alice"))
	if rec2.Code != http.StatusConflict {
		t.Fatalf("duplicate POST /rebuild status = %d; want %d; body = %s",
			rec2.Code, http.StatusConflict, rec2.Body.String())
	}

	resp := parseErrorEnvelope(t, rec2)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for duplicate rebuild submission")
	}
}

// ===========================================================================
// 16-REQ-1.E8: POST /rebuild by a PAT without 'rebuilds:write' scope
// returns HTTP 403.
// ===========================================================================

func TestSubmitRebuild_PATWithoutScope_Returns403(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-perm", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-p1", "ws-perm", "feature/foo", 1, PatchStatusActive)

	// PAT with only rebuilds:read scope (no rebuilds:write).
	auth := rebuildPATAuth("alice", "rebuilds:read")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-perm/rebuild", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /rebuild (PAT without write scope) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for missing permission scope")
	}
}

func TestSubmitRebuild_PATWithWriteScope_Returns202(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-pat-ok", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-pa1", "ws-pat-ok", "feature/foo", 1, PatchStatusActive)

	// PAT with rebuilds:write scope should succeed.
	auth := rebuildPATAuth("alice", "rebuilds:write")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-pat-ok/rebuild", "", auth)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /rebuild (PAT with write scope) status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}
}
