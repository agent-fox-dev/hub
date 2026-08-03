package mergequeue

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// TS-11-47: POST /merges returns HTTP 409 with existing_job_id when a job
// in queued or running status already exists for the same
// (workspace_slug, source_ref).
// Requirement: 11-REQ-8.1
// ---------------------------------------------------------------------------

func TestDuplicate_ActiveQueuedJobExists_Returns409(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user1"))

	// Pre-create a queued job for the same (workspace_slug, source_ref).
	existingID := newTestUUID("dp1")
	insertTestMergeJob(t, env.db,
		existingID, newTestUUID("ndp1"),
		"my-workspace", "main", "spec/07",
		"queued", newTestUUID("user"))

	body := `{"target_branch":"main","source_ref":"spec/07"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	// Must return HTTP 409 Conflict.
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	// Response body must contain the error message and existing_job_id.
	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	errMsg, ok := respBody["error"].(string)
	if !ok || errMsg != "merge already in progress for this source branch" {
		t.Errorf("error = %v; want 'merge already in progress for this source branch'", respBody["error"])
	}

	existingJobID, ok := respBody["existing_job_id"].(string)
	if !ok || existingJobID != existingID {
		t.Errorf("existing_job_id = %v; want %q", respBody["existing_job_id"], existingID)
	}
}

// TestDuplicate_ActiveRunningJobExists_Returns409 verifies that submitting
// a merge for a source_ref with an existing running job also returns 409.
func TestDuplicate_ActiveRunningJobExists_Returns409(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user2"))

	// Pre-create a running job for the same (workspace_slug, source_ref).
	existingID := newTestUUID("dp2")
	now := time.Now().UTC().Format(time.RFC3339)
	runningJob := &MergeJob{
		ID:            existingID,
		Nonce:         newTestUUID("ndp2"),
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07-running",
		Status:        "running",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, runningJob)

	body := `{"target_branch":"main","source_ref":"spec/07-running"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	// Must return HTTP 409 Conflict.
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	// Response must contain existing_job_id.
	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if respBody["existing_job_id"] != existingID {
		t.Errorf("existing_job_id = %v; want %q", respBody["existing_job_id"], existingID)
	}
}

// TestDuplicate_TerminalJobExists_Succeeds verifies that submitting a merge
// after the active job reaches a terminal state (merged, cancelled) succeeds.
func TestDuplicate_TerminalJobExists_Succeeds(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user3"))

	// Pre-create a merged (terminal) job for the same (workspace_slug, source_ref).
	now := time.Now().UTC().Format(time.RFC3339)
	mergedJob := &MergeJob{
		ID:            newTestUUID("dp3"),
		Nonce:         newTestUUID("ndp3"),
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07-merged",
		Status:        "merged",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, mergedJob)

	body := `{"target_branch":"main","source_ref":"spec/07-merged"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	// Must return HTTP 202 Accepted (terminal job does not block new submissions).
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-11-48: POST /merges proceeds with insert and returns HTTP 202 when
// no active job exists for the (workspace_slug, source_ref) pair.
// Requirement: 11-REQ-8.2
// ---------------------------------------------------------------------------

func TestDuplicate_NoActiveJob_Returns202(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user4"))

	body := `{"target_branch":"main","source_ref":"spec/07-new"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	// Must return HTTP 202 Accepted.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	// Response body must contain a merge job object with status='queued'.
	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if status, ok := respBody["status"].(string); !ok || status != "queued" {
		t.Errorf("response status = %v; want 'queued'", respBody["status"])
	}

	if ws, ok := respBody["workspace_slug"].(string); !ok || ws != "my-workspace" {
		t.Errorf("response workspace_slug = %v; want 'my-workspace'", respBody["workspace_slug"])
	}
}

// ---------------------------------------------------------------------------
// TS-11-49: POST /merges returns HTTP 500 when the pre-insert SELECT check
// query fails due to a database error.
// Requirement: 11-REQ-8.E1
// ---------------------------------------------------------------------------

func TestDuplicate_DBError_Returns500(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user5"))

	// Drop the merge_jobs table to simulate a database error on the
	// pre-insert SELECT check.
	_, err := env.db.Exec("DROP TABLE merge_jobs")
	if err != nil {
		t.Fatalf("failed to drop table: %v", err)
	}

	body := `{"target_branch":"main","source_ref":"spec/07-dberror"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	// Must return HTTP 500 Internal Server Error.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	// Response should contain an error body.
	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}

	// The error envelope should contain an 'error' key (apikit format).
	if _, hasError := respBody["error"]; !hasError {
		t.Error("response body does not contain 'error' key; want apikit error body")
	}
}
