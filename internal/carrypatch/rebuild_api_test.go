package carrypatch

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
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

// ===========================================================================
// TS-NS-1: POST /rebuild with body {"strategy":"merge"} uses merge strategy
// regardless of the REBUILD_STRATEGY variable.
// Requirement: NS-REQ-1
// ===========================================================================

func TestSubmitRebuild_BodyStrategyOverride(t *testing.T) {
	// Create env where GetVariable returns 'rebase' (default).
	env := newRebuildTestEnvWithStrategy(t, "rebase")

	seedWorkspace(t, env.db, "ws-override", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-o1", "ws-override", "feature/foo", 1, PatchStatusActive)

	// Send POST with body strategy=merge; variable says rebase.
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-override/rebuild",
		`{"strategy":"merge"}`, rebuildUserAuth("alice"))

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
		t.Errorf("expected payload.strategy='merge' (body override), got %q", payload.Strategy)
	}
}

// Also test that body strategy=rebase overrides a merge variable.
func TestSubmitRebuild_BodyStrategyOverride_Rebase(t *testing.T) {
	env := newRebuildTestEnvWithStrategy(t, "merge")

	seedWorkspace(t, env.db, "ws-override2", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-o2", "ws-override2", "feature/foo", 1, PatchStatusActive)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-override2/rebuild",
		`{"strategy":"rebase"}`, rebuildUserAuth("alice"))

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
		t.Errorf("expected payload.strategy='rebase' (body override), got %q", payload.Strategy)
	}
}

// ===========================================================================
// TS-NS-3: POST /rebuild with an invalid strategy value returns 400.
// Requirement: NS-REQ-3
// ===========================================================================

func TestSubmitRebuild_InvalidBodyStrategy_Returns400(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-invalid", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-i1", "ws-invalid", "feature/foo", 1, PatchStatusActive)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-invalid/rebuild",
		`{"strategy":"squash"}`, rebuildUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /rebuild (invalid strategy) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for invalid strategy")
	}
}

// ===========================================================================
// TS-NS-1: GET /workspaces/:slug/rebuilds/:id returns
// previous_integration_head_sha and integration_head_sha when the rebuild
// result contains them.
//
// Requirement: NS-REQ-1
// ===========================================================================

func TestGetRebuild_IncludesPreviousIntegrationHeadSHA(t *testing.T) {
	env := newFullTestEnv(t)

	slug := "ws-prev-sha"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream", "upstream-sha", "integration", "")
	seedPatch(t, env.db, "patch-1", slug, "feature/foo", 1, PatchStatusActive)

	// Insert a completed rebuild job with a result containing both SHAs.
	result := RebuildResult{
		UpstreamHeadSHA:            "aaaa000000000000000000000000000000000001",
		IntegrationHeadSHA:         "bbbb000000000000000000000000000000000001",
		PreviousIntegrationHeadSHA: "cccc000000000000000000000000000000000001",
		Strategy:                   "rebase",
		PatchesApplied:             1,
		PatchResults:               []PatchResult{{PatchID: "patch-1", Status: "success"}},
	}
	resultJSON, _ := json.Marshal(result)
	seedRebuildJobWithResult(t, env.db, "job-prev-sha", "completed", slug, "rebase",
		time.Now(), resultJSON)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/"+slug+"/rebuilds/job-prev-sha", "",
		rebuildUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rebuilds/:id status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	prevSHA, ok := body["previous_integration_head_sha"].(string)
	if !ok || prevSHA == "" {
		t.Error("expected non-empty 'previous_integration_head_sha' in response")
	}
	if prevSHA != "cccc000000000000000000000000000000000001" {
		t.Errorf("previous_integration_head_sha = %q, want %q",
			prevSHA, "cccc000000000000000000000000000000000001")
	}

	intSHA, ok := body["integration_head_sha"].(string)
	if !ok || intSHA == "" {
		t.Error("expected non-empty 'integration_head_sha' in response")
	}
	if intSHA != "bbbb000000000000000000000000000000000001" {
		t.Errorf("integration_head_sha = %q, want %q",
			intSHA, "bbbb000000000000000000000000000000000001")
	}

	// The two SHAs must differ.
	if prevSHA == intSHA {
		t.Error("previous_integration_head_sha and integration_head_sha should differ")
	}
}

// ===========================================================================
// TS-NS-2: Rollback via POST /workspaces/:slug/rebuilds/:id/rollback
// resets the integration branch to the previous HEAD.
//
// Requirement: NS-REQ-2
// ===========================================================================

func TestRollbackRebuild_Success_Returns200(t *testing.T) {
	env := newFullTestEnv(t)

	slug := "ws-rollback"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream", "upstream-sha", "integration", "")
	seedPatch(t, env.db, "patch-rb1", slug, "feature/foo", 1, PatchStatusActive)

	previousSHA := "prev000000000000000000000000000000000001"
	result := RebuildResult{
		UpstreamHeadSHA:            "aaaa000000000000000000000000000000000001",
		IntegrationHeadSHA:         "bbbb000000000000000000000000000000000001",
		PreviousIntegrationHeadSHA: previousSHA,
		Strategy:                   "rebase",
		PatchesApplied:             1,
		PatchResults:               []PatchResult{{PatchID: "patch-rb1", Status: "success"}},
	}
	resultJSON, _ := json.Marshal(result)
	seedRebuildJobWithResult(t, env.db, "job-rb1", "completed", slug, "rebase",
		time.Now(), resultJSON)

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/"+slug+"/rebuilds/job-rb1/rollback", "",
		rebuildUserAuth("alice"))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /rollback status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	rolledBackTo, ok := body["rolled_back_to"].(string)
	if !ok || rolledBackTo == "" {
		t.Error("expected non-empty 'rolled_back_to' in response")
	}
	if rolledBackTo != previousSHA {
		t.Errorf("rolled_back_to = %q, want %q", rolledBackTo, previousSHA)
	}

	// Verify git branch -f was called with the previous SHA.
	found := false
	for _, call := range env.gitRunner.RunCalls {
		if len(call.Args) >= 4 &&
			call.Args[0] == "branch" &&
			call.Args[1] == "-f" &&
			call.Args[2] == "integration" &&
			call.Args[3] == previousSHA {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'git branch -f integration <previousSHA>' call")
	}
}

// ===========================================================================
// TS-NS-3: Rollback is rejected when there is no previous SHA (first-ever
// rebuild for the workspace).
//
// Requirement: NS-REQ-3
// ===========================================================================

func TestRollbackRebuild_NoPreviousSHA_Returns409(t *testing.T) {
	env := newFullTestEnv(t)

	slug := "ws-rollback-409"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream", "upstream-sha", "integration", "")
	seedPatch(t, env.db, "patch-no-prev", slug, "feature/foo", 1, PatchStatusActive)

	// Result with empty PreviousIntegrationHeadSHA (first rebuild).
	result := RebuildResult{
		UpstreamHeadSHA:    "aaaa000000000000000000000000000000000001",
		IntegrationHeadSHA: "bbbb000000000000000000000000000000000001",
		Strategy:           "rebase",
		PatchesApplied:     1,
		PatchResults:       []PatchResult{{PatchID: "patch-no-prev", Status: "success"}},
	}
	resultJSON, _ := json.Marshal(result)
	seedRebuildJobWithResult(t, env.db, "job-no-prev", "completed", slug, "rebase",
		time.Now(), resultJSON)

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/"+slug+"/rebuilds/job-no-prev/rollback", "",
		rebuildUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /rollback (no previous SHA) status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for no previous SHA")
	}
}

// Test: rollback rejected when job has no result (not yet completed).
func TestRollbackRebuild_NoResult_Returns409(t *testing.T) {
	env := newFullTestEnv(t)

	slug := "ws-rollback-noresult"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream", "upstream-sha", "integration", "")
	seedPatch(t, env.db, "patch-nr", slug, "feature/foo", 1, PatchStatusActive)

	// Insert a queued job (no result).
	seedRebuildJob(t, env.db, "job-nr", "queued", slug, "rebase", "operator")

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/"+slug+"/rebuilds/job-nr/rollback", "",
		rebuildUserAuth("alice"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /rollback (no result) status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

// ===========================================================================
// TS-NS-5: Rollback requires 'rebuilds:write' scope for PATs and is gated
// to the owning workspace.
//
// Requirement: NS-REQ-5
// ===========================================================================

func TestRollbackRebuild_PATReadOnly_Returns403(t *testing.T) {
	env := newFullTestEnv(t)

	slug := "ws-rollback-auth"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream", "upstream-sha", "integration", "")
	seedPatch(t, env.db, "patch-auth", slug, "feature/foo", 1, PatchStatusActive)

	previousSHA := "prev000000000000000000000000000000000001"
	result := RebuildResult{
		UpstreamHeadSHA:            "aaaa000000000000000000000000000000000001",
		IntegrationHeadSHA:         "bbbb000000000000000000000000000000000001",
		PreviousIntegrationHeadSHA: previousSHA,
		Strategy:                   "rebase",
		PatchesApplied:             1,
		PatchResults:               []PatchResult{{PatchID: "patch-auth", Status: "success"}},
	}
	resultJSON, _ := json.Marshal(result)
	seedRebuildJobWithResult(t, env.db, "job-auth", "completed", slug, "rebase",
		time.Now(), resultJSON)

	// PAT with only rebuilds:read should be rejected.
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/"+slug+"/rebuilds/job-auth/rollback", "",
		rebuildPATAuth("alice", "rebuilds:read"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /rollback (read-only PAT) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestRollbackRebuild_PATWriteScope_Returns200(t *testing.T) {
	env := newFullTestEnv(t)

	slug := "ws-rollback-write"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream", "upstream-sha", "integration", "")
	seedPatch(t, env.db, "patch-write", slug, "feature/foo", 1, PatchStatusActive)

	previousSHA := "prev000000000000000000000000000000000001"
	result := RebuildResult{
		UpstreamHeadSHA:            "aaaa000000000000000000000000000000000001",
		IntegrationHeadSHA:         "bbbb000000000000000000000000000000000001",
		PreviousIntegrationHeadSHA: previousSHA,
		Strategy:                   "rebase",
		PatchesApplied:             1,
		PatchResults:               []PatchResult{{PatchID: "patch-write", Status: "success"}},
	}
	resultJSON, _ := json.Marshal(result)
	seedRebuildJobWithResult(t, env.db, "job-write", "completed", slug, "rebase",
		time.Now(), resultJSON)

	// PAT with rebuilds:write should succeed.
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/"+slug+"/rebuilds/job-write/rollback", "",
		rebuildPATAuth("alice", "rebuilds:write"))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /rollback (write PAT) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestRollbackRebuild_CrossWorkspace_Returns404(t *testing.T) {
	env := newFullTestEnv(t)

	slug := "ws-rollback-cross"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream", "upstream-sha", "integration", "")
	seedPatch(t, env.db, "patch-cross", slug, "feature/foo", 1, PatchStatusActive)

	previousSHA := "prev000000000000000000000000000000000001"
	result := RebuildResult{
		UpstreamHeadSHA:            "aaaa000000000000000000000000000000000001",
		IntegrationHeadSHA:         "bbbb000000000000000000000000000000000001",
		PreviousIntegrationHeadSHA: previousSHA,
		Strategy:                   "rebase",
		PatchesApplied:             1,
		PatchResults:               []PatchResult{{PatchID: "patch-cross", Status: "success"}},
	}
	resultJSON, _ := json.Marshal(result)
	seedRebuildJobWithResult(t, env.db, "job-cross", "completed", slug, "rebase",
		time.Now(), resultJSON)

	// Create a second workspace and try to access the job from it.
	seedWorkspaceCarryPatch(t, env.db, "ws-other", "bob",
		"https://github.com/example/other", "other-sha", "integration", "")

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-other/rebuilds/job-cross/rollback", "",
		rebuildUserAuth("bob"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /rollback (cross-workspace) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// ===========================================================================
// TS-NS-5: POST /rebuild with {"fail_mode":"continue"} passes fail_mode
// to the payload.
//
// Requirement: NS-REQ-5
// ===========================================================================

func TestSubmitRebuild_FailModeContinue_InPayload(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-failmode", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-fm1", "ws-failmode", "feature/foo", 1, PatchStatusActive)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-failmode/rebuild",
		`{"fail_mode":"continue"}`, rebuildUserAuth("alice"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /rebuild status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	resp := parseRebuildJobResponse(t, rec)
	var payload RebuildPayload
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.FailMode != "continue" {
		t.Errorf("expected payload.fail_mode='continue', got %q", payload.FailMode)
	}
}

// TS-NS-5: POST /rebuild with no fail_mode omits it from payload.
func TestSubmitRebuild_NoFailMode_OmittedFromPayload(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-nofm", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-nofm", "ws-nofm", "feature/foo", 1, PatchStatusActive)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-nofm/rebuild",
		"", rebuildUserAuth("alice"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /rebuild status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	resp := parseRebuildJobResponse(t, rec)
	var payload RebuildPayload
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	// FailMode should be empty (omitted) when not supplied.
	if payload.FailMode != "" {
		t.Errorf("expected empty fail_mode when not supplied, got %q", payload.FailMode)
	}
}

// TS-NS-5: Invalid fail_mode value returns 400.
func TestSubmitRebuild_InvalidFailMode_Returns400(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws-badfm", "alice", "active", "ready", "carry_patch", "integration")
	seedPatch(t, env.db, "patch-badfm", "ws-badfm", "feature/foo", 1, PatchStatusActive)

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/ws-badfm/rebuild",
		`{"fail_mode":"invalid"}`, rebuildUserAuth("alice"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /rebuild (invalid fail_mode) status = %d; want %d; body = %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for invalid fail_mode")
	}
}

// ===========================================================================
// TS-NS-4: REBUILD_FAIL_MODE workspace variable sets the default when no
// body override is given.
//
// Requirement: NS-REQ-4
// ===========================================================================

func TestSubmitRebuild_FailModeFromWorkspaceVariable(t *testing.T) {
	// Create env where GetVariable returns 'continue' for REBUILD_FAIL_MODE.
	env := newFullTestEnvWithGetVariable(t, func(scope, slug, key string) (string, error) {
		if key == "REBUILD_FAIL_MODE" {
			return "continue", nil
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		return "", nil
	})

	slug := "ws-fmvar"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream", "upstream-sha", "integration", "")
	seedPatch(t, env.db, "patch-fmvar", slug, "feature/foo", 1, PatchStatusActive)

	// Submit without body fail_mode.
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/rebuild",
		"", rebuildUserAuth("alice"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /rebuild status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	resp := parseRebuildJobResponse(t, rec)
	var payload RebuildPayload
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.FailMode != "continue" {
		t.Errorf("expected payload.fail_mode='continue' (from variable), got %q", payload.FailMode)
	}
}

// Body fail_mode overrides workspace variable.
func TestSubmitRebuild_BodyFailModeOverridesVariable(t *testing.T) {
	env := newFullTestEnvWithGetVariable(t, func(scope, slug, key string) (string, error) {
		if key == "REBUILD_FAIL_MODE" {
			return "continue", nil // variable says continue
		}
		if key == "REBUILD_STRATEGY" {
			return "rebase", nil
		}
		return "", nil
	})

	slug := "ws-fmoverride"
	seedWorkspaceCarryPatch(t, env.db, slug, "alice",
		"https://github.com/example/upstream", "upstream-sha", "integration", "")
	seedPatch(t, env.db, "patch-fmo", slug, "feature/foo", 1, PatchStatusActive)

	// Body says fail_fast, overriding variable.
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/rebuild",
		`{"fail_mode":"fail_fast"}`, rebuildUserAuth("alice"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /rebuild status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	resp := parseRebuildJobResponse(t, rec)
	var payload RebuildPayload
	if err := json.Unmarshal(resp.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payload.FailMode != "fail_fast" {
		t.Errorf("expected payload.fail_mode='fail_fast' (body override), got %q", payload.FailMode)
	}
}
