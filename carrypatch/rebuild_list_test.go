package carrypatch

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// ===========================================================================
// TS-16-9: GET /api/v1/workspaces/:slug/rebuilds returns HTTP 200 with a
// JSON array of rebuild job records ordered by creation time descending.
//
// Requirement: 16-REQ-2.1
// ===========================================================================

func TestRebuildList_Returns200WithJobsDescending(t *testing.T) {
	env := newFullTestEnv(t)

	// Seed a carry_patch workspace.
	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")

	// Seed 3 rebuild jobs at different created_at timestamps.
	t1 := time.Now().UTC().Add(-3 * time.Hour)
	t2 := time.Now().UTC().Add(-2 * time.Hour)
	t3 := time.Now().UTC().Add(-1 * time.Hour)

	seedRebuildJobWithResult(t, env.db, "job-uuid-1", "completed", "my-workspace", "rebase", t1, nil)
	seedRebuildJobWithResult(t, env.db, "job-uuid-2", "failed", "my-workspace", "rebase", t2, nil)
	seedRebuildJobWithResult(t, env.db, "job-uuid-3", "completed", "my-workspace", "rebase", t3, nil)

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/rebuilds", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rebuilds status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp RebuildListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v (body: %s)", err, rec.Body.String())
	}

	if len(resp.Jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(resp.Jobs))
	}

	// Jobs must be ordered by created_at descending (latest first).
	if resp.Jobs[0].ID != "job-uuid-3" {
		t.Errorf("expected first job id='job-uuid-3' (latest), got %q", resp.Jobs[0].ID)
	}
	if resp.Jobs[2].ID != "job-uuid-1" {
		t.Errorf("expected last job id='job-uuid-1' (earliest), got %q", resp.Jobs[2].ID)
	}

	// Verify descending time order.
	for i := 0; i < len(resp.Jobs)-1; i++ {
		if resp.Jobs[i].CreatedAt < resp.Jobs[i+1].CreatedAt {
			t.Errorf("jobs not in descending order: jobs[%d].created_at=%q < jobs[%d].created_at=%q",
				i, resp.Jobs[i].CreatedAt, i+1, resp.Jobs[i+1].CreatedAt)
		}
	}

	// Each job should have required fields.
	for _, job := range resp.Jobs {
		if job.ID == "" {
			t.Error("expected non-empty job ID")
		}
		if job.Status == "" {
			t.Error("expected non-empty job status")
		}
		validStatuses := map[string]bool{"queued": true, "running": true, "completed": true, "failed": true}
		if !validStatuses[job.Status] {
			t.Errorf("unexpected job status %q", job.Status)
		}
		if job.CreatedAt == "" {
			t.Error("expected non-empty created_at")
		}
	}
}

// 16-REQ-2.E3: Empty rebuild history returns HTTP 200 with {"jobs": []}.
func TestRebuildList_NoHistory_ReturnsEmptyList(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "ws-no-rebuilds", "alice", "active", "ready", "carry_patch", "integration")

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-no-rebuilds/rebuilds", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rebuilds (no history) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp RebuildListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Jobs == nil {
		t.Error("expected non-nil jobs array (should be empty list, not null)")
	}
	if len(resp.Jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(resp.Jobs))
	}
}

// 16-REQ-2.E1: PAT without 'rebuilds:read' scope returns HTTP 403.
func TestRebuildList_PATWithoutReadScope_Returns403(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "ws-perm", "alice", "active", "ready", "carry_patch", "integration")

	// PAT with no rebuilds:read scope.
	auth := rebuildPATAuth("alice", "workspaces:read")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-perm/rebuilds", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /rebuilds (PAT without read scope) status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	resp := parseErrorEnvelope(t, rec)
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message for missing permission scope")
	}
}

// PAT with 'rebuilds:read' scope should succeed.
func TestRebuildList_PATWithReadScope_Returns200(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "ws-pat-ok", "alice", "active", "ready", "carry_patch", "integration")

	auth := rebuildPATAuth("alice", "rebuilds:read")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-pat-ok/rebuilds", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rebuilds (PAT with read scope) status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}
}

// ===========================================================================
// TS-16-10: GET /api/v1/workspaces/:slug/rebuilds/:id returns HTTP 200 with
// the full job record including patch_results payload for a completed job.
//
// Requirement: 16-REQ-2.2
// ===========================================================================

func TestRebuildStatus_Returns200WithPatchResults(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")

	// Seed a completed rebuild job with patch_results in the result field.
	patchResults := []PatchResult{
		{
			PatchID:    "p1",
			BranchName: "feature/foo",
			Position:   1,
			Status:     "success",
		},
	}
	rebuildResult := RebuildResult{
		UpstreamHeadSHA:    "aaaa000000000000000000000000000000000001",
		IntegrationHeadSHA: "bbbb000000000000000000000000000000000001",
		Strategy:           "rebase",
		PatchesApplied:     1,
		PatchResults:       patchResults,
	}
	resultJSON, err := json.Marshal(rebuildResult)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}

	seedRebuildJobWithResult(t, env.db, "job-uuid-1", "completed", "my-workspace", "rebase", time.Now().UTC(), resultJSON)

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/rebuilds/job-uuid-1", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rebuilds/:id status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp RebuildJobRecord
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v (body: %s)", err, rec.Body.String())
	}

	if resp.ID != "job-uuid-1" {
		t.Errorf("expected id='job-uuid-1', got %q", resp.ID)
	}
	if resp.Status != "completed" {
		t.Errorf("expected status='completed', got %q", resp.Status)
	}
	if resp.PatchResults == nil {
		t.Error("expected non-nil patch_results for completed job")
	}

	// Parse patch_results.
	var results []PatchResult
	if err := json.Unmarshal(resp.PatchResults, &results); err != nil {
		t.Fatalf("failed to parse patch_results: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least 1 patch result")
	}
}

// 16-REQ-2.E2: Getting a rebuild ID that doesn't belong to the workspace returns 404.
func TestRebuildStatus_CrossWorkspace_Returns404(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "ws-a", "alice", "active", "ready", "carry_patch", "integration")
	seedWorkspace(t, env.db, "ws-b", "alice", "active", "ready", "carry_patch", "integration")

	// Seed a job belonging to ws-b.
	seedRebuildJobWithResult(t, env.db, "job-from-b", "completed", "ws-b", "rebase", time.Now().UTC(), nil)

	auth := rebuildUserAuth("alice")
	// Request the job via ws-a (cross-workspace).
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/ws-a/rebuilds/job-from-b", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /rebuilds/:id (cross-workspace) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// Non-existent job ID returns 404.
func TestRebuildStatus_NotFound_Returns404(t *testing.T) {
	env := newFullTestEnv(t)

	seedWorkspace(t, env.db, "my-workspace", "alice", "active", "ready", "carry_patch", "integration")

	auth := rebuildUserAuth("alice")
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-workspace/rebuilds/nonexistent-id", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /rebuilds/:id (not found) status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}
