package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ---------------------------------------------------------------------------
// TS-NS-4: After each patch in HandleRebuildJob, progress is written to the
// queue with the current PatchResults slice.
// Requirement: NS-REQ-4
// ---------------------------------------------------------------------------

func TestHandleRebuildJob_WritesProgressAfterEachPatch(t *testing.T) {
	q, db := newTestQueue(t)

	patches := []Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", WorkspaceID: "ws1", BranchName: "feature/b", Position: 2, Status: PatchStatusActive},
		{ID: "p3", WorkspaceID: "ws1", BranchName: "feature/c", Position: 3, Status: PatchStatusActive},
	}

	mock := newMockGitRunner()
	headCallCount := 0
	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			headCallCount++
			return "sha" + string(rune('0'+headCallCount)), nil
		}
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "FETCH_HEAD" {
			return "upstream-sha", nil
		}
		if len(args) >= 2 && args[0] == "log" {
			return "commit-sha-1", nil
		}
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--verify" {
			return "branch-sha", nil
		}
		return "", nil
	}

	store := newMockPatchStore(patches)

	handler := &RebuildHandler{
		Queue:      q,
		PatchStore: store,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
	}

	_ = RegisterRebuildJob(q, handler)

	jobID, _, err := q.Enqueue(jobqueue.EnqueueParams{
		Type:        "rebuild",
		Key:         "ws1",
		Nonce:       "nonce-progress",
		Payload:     json.RawMessage(`{"workspace_slug":"ws1","strategy":"rebase"}`),
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}

	// Set the job to running.
	_, _ = db.Exec("UPDATE jobs SET status = 'running' WHERE id = ?", jobID)

	// Create context with job ID (simulating claimAndExecute).
	ctx := jobqueue.ContextWithJobID(context.Background(), jobID)

	payload := RebuildPayload{
		WorkspaceSlug: "ws1",
		Strategy:      StrategyRebase,
	}
	payloadJSON, _ := json.Marshal(payload)

	_, _, handleErr := handler.HandleRebuildJob(ctx, payloadJSON)
	if handleErr != nil {
		t.Fatalf("HandleRebuildJob() returned error: %v", handleErr)
	}

	// Verify that progress was written.
	job, err := q.GetByID(jobID)
	if err != nil {
		t.Fatalf("GetByID() returned error: %v", err)
	}
	if job.Progress == nil {
		t.Fatal("expected non-nil progress after HandleRebuildJob with 3 patches")
	}

	// The last progress write should contain all 3 patch results.
	var patchResults []PatchResult
	if err := json.Unmarshal(job.Progress, &patchResults); err != nil {
		t.Fatalf("failed to unmarshal progress: %v", err)
	}
	if len(patchResults) != 3 {
		t.Errorf("expected 3 patch results in final progress, got %d", len(patchResults))
	}

	// Verify each patch result has the right patch ID.
	for i, pr := range patchResults {
		expectedID := patches[i].ID
		if pr.PatchID != expectedID {
			t.Errorf("progress[%d]: expected patch_id=%q, got %q", i, expectedID, pr.PatchID)
		}
		if pr.Status != "success" {
			t.Errorf("progress[%d]: expected status='success', got %q", i, pr.Status)
		}
	}
}

func TestHandleRebuildJob_SkippedPatchesAlsoWriteProgress(t *testing.T) {
	q, db := newTestQueue(t)

	patches := []Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/merged", Position: 1, Status: PatchStatusMergedUpstream},
		{ID: "p2", WorkspaceID: "ws1", BranchName: "feature/active", Position: 2, Status: PatchStatusActive},
	}

	mock := newMockGitRunner()
	headCallCount := 0
	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			headCallCount++
			return "sha" + string(rune('0'+headCallCount)), nil
		}
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "FETCH_HEAD" {
			return "upstream-sha", nil
		}
		if len(args) >= 2 && args[0] == "log" {
			return "commit-sha-1", nil
		}
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--verify" {
			return "branch-sha", nil
		}
		return "", nil
	}

	store := newMockPatchStore(patches)

	handler := &RebuildHandler{
		Queue:      q,
		PatchStore: store,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
	}

	_ = RegisterRebuildJob(q, handler)

	jobID, _, _ := q.Enqueue(jobqueue.EnqueueParams{
		Type:        "rebuild",
		Key:         "ws1",
		Nonce:       "nonce-skip-prog",
		Payload:     json.RawMessage(`{"workspace_slug":"ws1","strategy":"rebase"}`),
		SubmittedBy: "test",
	})
	_, _ = db.Exec("UPDATE jobs SET status = 'running' WHERE id = ?", jobID)

	ctx := jobqueue.ContextWithJobID(context.Background(), jobID)

	payload := RebuildPayload{
		WorkspaceSlug: "ws1",
		Strategy:      StrategyRebase,
	}
	payloadJSON, _ := json.Marshal(payload)

	_, _, handleErr := handler.HandleRebuildJob(ctx, payloadJSON)
	if handleErr != nil {
		t.Fatalf("HandleRebuildJob() returned error: %v", handleErr)
	}

	job, _ := q.GetByID(jobID)
	if job.Progress == nil {
		t.Fatal("expected non-nil progress")
	}

	var patchResults []PatchResult
	_ = json.Unmarshal(job.Progress, &patchResults)
	if len(patchResults) != 2 {
		t.Errorf("expected 2 patch results (1 skipped + 1 success), got %d", len(patchResults))
	}
	if patchResults[0].Status != "skipped" {
		t.Errorf("expected first patch status='skipped', got %q", patchResults[0].Status)
	}
	if patchResults[0].SkippedReason != "merged_upstream" {
		t.Errorf("expected first patch skipped_reason='merged_upstream', got %q", patchResults[0].SkippedReason)
	}
	if patchResults[1].Status != "success" {
		t.Errorf("expected second patch status='success', got %q", patchResults[1].Status)
	}
}

func TestHandleRebuildJob_NoProgressWrittenWithoutQueue(t *testing.T) {
	// When Queue is nil, progress should not be written (no panic).
	patches := []Patch{
		{ID: "p1", WorkspaceID: "ws1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
	}

	mock := newMockGitRunner()
	headCallCount := 0
	mock.RunFunc = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			headCallCount++
			return "sha" + string(rune('0'+headCallCount)), nil
		}
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "FETCH_HEAD" {
			return "upstream-sha", nil
		}
		if len(args) >= 2 && args[0] == "log" {
			return "commit-sha-1", nil
		}
		return "", nil
	}

	store := newMockPatchStore(patches)

	handler := &RebuildHandler{
		Queue:      nil, // No queue — should not panic.
		PatchStore: store,
		NewGitRunner: func(_ string) (GitRunner, error) {
			return mock, nil
		},
	}

	ctx := context.Background()
	payload := RebuildPayload{
		WorkspaceSlug: "ws1",
		Strategy:      StrategyRebase,
	}
	payloadJSON, _ := json.Marshal(payload)

	_, _, handleErr := handler.HandleRebuildJob(ctx, payloadJSON)
	if handleErr != nil {
		t.Fatalf("HandleRebuildJob() should not error with nil Queue: %v", handleErr)
	}
}

// ---------------------------------------------------------------------------
// TS-NS-5: GET /workspaces/:slug/rebuilds/:id returns patch_results for a
// running job by reading the progress column.
// Requirement: NS-REQ-5
// ---------------------------------------------------------------------------

func TestGetRebuild_RunningJobReturnsProgressAsPatchResults(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "user-1", "active", "ready", "carry_patch", "integration")

	progressJSON := `[{"patch_id":"p1","branch_name":"feature/a","position":1,"status":"success","new_head_sha":"abc123"},{"patch_id":"p2","branch_name":"feature/b","position":2,"status":"success","new_head_sha":"def456"},{"patch_id":"p3","branch_name":"feature/c","position":3,"status":"skipped","skipped_reason":"merged_upstream","new_head_sha":null}]`
	seedRebuildJobWithProgress(t, env.db, "job-running", "running", "ws1", "rebase", progressJSON)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws1/rebuilds/job-running", "",
		rebuildUserAuth("user-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp RebuildJobRecord
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Status != "running" {
		t.Errorf("expected status='running', got %q", resp.Status)
	}

	if resp.PatchResults == nil {
		t.Fatal("expected non-nil patch_results for running job with progress")
	}

	var patchResults []PatchResult
	if err := json.Unmarshal(resp.PatchResults, &patchResults); err != nil {
		t.Fatalf("unmarshal patch_results: %v", err)
	}
	if len(patchResults) != 3 {
		t.Errorf("expected 3 patch results, got %d", len(patchResults))
	}
	if patchResults[0].PatchID != "p1" {
		t.Errorf("expected first patch_id='p1', got %q", patchResults[0].PatchID)
	}
}

func TestGetRebuild_RunningJobWithoutProgressOmitsPatchResults(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "user-1", "active", "ready", "carry_patch", "integration")
	seedRebuildJob(t, env.db, "job-no-prog", "running", "ws1", "rebase", "user-1")

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws1/rebuilds/job-no-prog", "",
		rebuildUserAuth("user-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var rawResp map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&rawResp); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}

	if _, ok := rawResp["patch_results"]; ok {
		t.Error("expected patch_results to be omitted for running job with no progress")
	}
}

func TestListRebuilds_RunningJobIncludesProgress(t *testing.T) {
	env := newRebuildTestEnv(t)

	seedWorkspace(t, env.db, "ws1", "user-1", "active", "ready", "carry_patch", "integration")

	progressJSON := `[{"patch_id":"p1","branch_name":"feature/a","position":1,"status":"success","new_head_sha":"abc"}]`
	seedRebuildJobWithProgress(t, env.db, "job-list-prog", "running", "ws1", "rebase", progressJSON)

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/ws1/rebuilds", "",
		rebuildUserAuth("user-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp RebuildListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(resp.Jobs))
	}
	if resp.Jobs[0].PatchResults == nil {
		t.Error("expected patch_results in list response for running job with progress")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// seedRebuildJobWithProgress inserts a rebuild job with progress data.
func seedRebuildJobWithProgress(t *testing.T, db *sql.DB, id, status, workspaceSlug, strategy, progressJSON string) {
	t.Helper()
	payload := RebuildPayload{
		WorkspaceSlug: workspaceSlug,
		Strategy:      strategy,
		SubmittedBy:   "operator",
	}
	payloadJSONBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("seedRebuildJobWithProgress: marshal payload: %v", err)
	}
	now := "2026-08-31T00:00:00Z"
	key := workspaceSlug
	groupKey := workspaceSlug + ":integration"

	_, err = db.Exec(
		`INSERT INTO jobs (id, type, key, group_key, nonce, status, payload, result, error, progress, retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES (?, 'rebuild', ?, ?, ?, ?, ?, NULL, NULL, ?, 0, ?, 'operator', ?, ?)`,
		id, key, groupKey, id, status, string(payloadJSONBytes), progressJSON, now, now, now,
	)
	if err != nil {
		t.Fatalf("seedRebuildJobWithProgress(%q) failed: %v", id, err)
	}
}
