package mergequeue

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ===========================================================================
// 9.1 — GET /merges/:id single job endpoint tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-71: GET /merges/:id returns the full merge job object including
// check_output and conflict_details as a native JSON array; nonce is excluded.
// Requirement: 11-REQ-12.1
// ---------------------------------------------------------------------------

func TestHandlerGet_ReturnsFullJobWithCheckOutputAndConflictDetails(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader-get1"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:              "get-full-id",
		Nonce:           "get-full-nonce",
		WorkspaceSlug:   "my-workspace",
		TargetBranch:    "main",
		SourceRef:       "spec/07-secrets-variables",
		Status:          "check_failed",
		RetryCount:      0,
		AvailableAt:     now,
		SubmittedBy:     newTestUUID("user"),
		CreatedAt:       now,
		UpdatedAt:       now,
		CheckOutput:     sql.NullString{String: "FAIL: test_foo", Valid: true},
		ConflictDetails: sql.NullString{String: `["file1.go"]`, Valid: true},
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/get-full-id", "", auth)

	// Must return HTTP 200.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// id must match.
	if id, ok := respBody["id"].(string); !ok || id != "get-full-id" {
		t.Errorf("id = %v; want 'get-full-id'", respBody["id"])
	}

	// check_output must be present and match.
	co, ok := respBody["check_output"].(string)
	if !ok || co != "FAIL: test_foo" {
		t.Errorf("check_output = %v; want 'FAIL: test_foo'", respBody["check_output"])
	}

	// conflict_details must be a native JSON array, not a string.
	cd := respBody["conflict_details"]
	if cd == nil {
		t.Fatal("conflict_details is nil; want a JSON array")
	}
	switch v := cd.(type) {
	case []interface{}:
		if len(v) != 1 {
			t.Errorf("conflict_details array length = %d; want 1", len(v))
		}
		if len(v) > 0 {
			if file, ok := v[0].(string); !ok || file != "file1.go" {
				t.Errorf("conflict_details[0] = %v; want 'file1.go'", v[0])
			}
		}
	case string:
		t.Errorf("conflict_details is a string %q; want a native JSON array", v)
	default:
		t.Errorf("conflict_details is type %T; want []interface{}", cd)
	}

	// nonce must NOT appear in the response.
	if _, hasNonce := respBody["nonce"]; hasNonce {
		t.Error("response body contains 'nonce' field; want nonce excluded from API response")
	}

	// status must match.
	if status, ok := respBody["status"].(string); !ok || status != "check_failed" {
		t.Errorf("status = %v; want 'check_failed'", respBody["status"])
	}
}

// ===========================================================================
// 9.2 — GET /merges/:id edge-case tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-72: GET /merges/:id returns HTTP 404 when the job ID does not exist
// in the database.
// Requirement: 11-REQ-12.E1
// ---------------------------------------------------------------------------

func TestHandlerGet_NonexistentJob_Returns404(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader-get2"))

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/nonexistent-uuid", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if _, hasError := respBody["error"]; !hasError {
		t.Error("response body does not contain 'error' key; want apikit error body")
	}
}

// ---------------------------------------------------------------------------
// TS-11-73: GET /merges/:id returns HTTP 404 (anti-enumeration) when the job
// exists but belongs to a different workspace_slug.
// Requirement: 11-REQ-12.E2
// ---------------------------------------------------------------------------

func TestHandlerGet_DifferentWorkspace_Returns404(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeReadAuth(newTestUUID("reader-get3"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "other-ws-job-id",
		Nonce:         "other-ws-nonce",
		WorkspaceSlug: "other-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	// Request using my-workspace slug but job belongs to other-workspace.
	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/other-ws-job-id", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d (anti-enumeration: job exists in other workspace); body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if _, hasError := respBody["error"]; !hasError {
		t.Error("response body does not contain 'error' key; want apikit error body")
	}
}

// ---------------------------------------------------------------------------
// TS-11-74: GET /merges/:id returns HTTP 401 or 403 when the caller is
// unauthenticated or lacks merges:read scope.
// Requirement: 11-REQ-12.E3
// ---------------------------------------------------------------------------

func TestHandlerGet_Unauthenticated_Returns401(t *testing.T) {
	env := newMergeHTTPTestEnv(t)

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "auth-get-job-id",
		Nonce:         "auth-get-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	// No auth token.
	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/auth-get-job-id", "", nil)

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 401 or 403; body = %s",
			rec.Code, rec.Body.String())
	}
}

func TestHandlerGet_WriteScopeOnly_Returns403(t *testing.T) {
	env := newMergeHTTPTestEnv(t)

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "scope-get-job-id",
		Nonce:         "scope-get-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	// PAT with merges:write (not merges:read) — should be rejected for GET.
	auth := mergeWriteAuth(newTestUUID("reader-get4"))

	rec := env.doMergeRequest(t, http.MethodGet,
		"/api/v1/workspaces/my-workspace/merges/scope-get-job-id", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// ===========================================================================
// 9.3 — DELETE /merges/:id cancel endpoint tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-75: DELETE /merges/:id transitions a queued job to cancelled status
// and returns HTTP 204 No Content.
// Requirement: 11-REQ-13.1
// ---------------------------------------------------------------------------

func TestHandlerCancel_QueuedJob_Returns204(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("writer-cancel1"))

	// Record the creation time to verify updated_at changes later.
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	job := &MergeJob{
		ID:            "cancel-ok-id",
		Nonce:         "cancel-ok-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   createdAt,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodDelete,
		"/api/v1/workspaces/my-workspace/merges/cancel-ok-id", "", auth)

	// Must return HTTP 204 No Content.
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Job status must be 'cancelled' in the database.
	status := getJobStatus(t, env.db, "cancel-ok-id")
	if status != "cancelled" {
		t.Errorf("job status = %q; want 'cancelled'", status)
	}

	// updated_at must have been updated (different from created_at).
	updatedAt := getJobUpdatedAt(t, env.db, "cancel-ok-id")
	if updatedAt == createdAt {
		t.Error("updated_at was not updated after cancellation; want updated_at > created_at")
	}
}

// ---------------------------------------------------------------------------
// TS-11-76: DELETE /merges/:id returns HTTP 409 with the current job status
// when the job is not in queued status.
// Requirement: 11-REQ-13.2
// ---------------------------------------------------------------------------

func TestHandlerCancel_RunningJob_Returns409(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("writer-cancel2"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "cancel-running-id",
		Nonce:         "cancel-running-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "running",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodDelete,
		"/api/v1/workspaces/my-workspace/merges/cancel-running-id", "", auth)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if _, hasError := respBody["error"]; !hasError {
		t.Error("response body does not contain 'error' key; want apikit error body")
	}

	// The response should contain the current status 'running'.
	bodyStr := rec.Body.String()
	if !bodyContains(bodyStr, "running") {
		t.Error("error body does not contain 'running'; want current status in error message")
	}
}

func TestHandlerCancel_MergedJob_Returns409(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("writer-cancel3"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "cancel-merged-id",
		Nonce:         "cancel-merged-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "merged",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodDelete,
		"/api/v1/workspaces/my-workspace/merges/cancel-merged-id", "", auth)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestHandlerCancel_DeadLetterJob_Returns409(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("writer-cancel4"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "cancel-dl-id",
		Nonce:         "cancel-dl-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "dead_letter",
		RetryCount:    20,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodDelete,
		"/api/v1/workspaces/my-workspace/merges/cancel-dl-id", "", auth)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestHandlerCancel_CancelledJob_Returns409(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("writer-cancel5"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "cancel-cancelled-id",
		Nonce:         "cancel-cancelled-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "cancelled",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodDelete,
		"/api/v1/workspaces/my-workspace/merges/cancel-cancelled-id", "", auth)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

// ===========================================================================
// 9.4 — DELETE /merges/:id edge-case tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-77: DELETE /merges/:id returns HTTP 404 when the job ID does not exist.
// Requirement: 11-REQ-13.E1
// ---------------------------------------------------------------------------

func TestHandlerCancel_NonexistentJob_Returns404(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("writer-cancel6"))

	rec := env.doMergeRequest(t, http.MethodDelete,
		"/api/v1/workspaces/my-workspace/merges/nonexistent-uuid", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if _, hasError := respBody["error"]; !hasError {
		t.Error("response body does not contain 'error' key; want apikit error body")
	}
}

// ---------------------------------------------------------------------------
// TS-11-78: DELETE /merges/:id returns HTTP 401 or 403 when the caller is
// unauthenticated or lacks merges:write scope.
// Requirement: 11-REQ-13.E2
// ---------------------------------------------------------------------------

func TestHandlerCancel_Unauthenticated_Returns401(t *testing.T) {
	env := newMergeHTTPTestEnv(t)

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "cancel-noauth-id",
		Nonce:         "cancel-noauth-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodDelete,
		"/api/v1/workspaces/my-workspace/merges/cancel-noauth-id", "", nil)

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 401 or 403; body = %s",
			rec.Code, rec.Body.String())
	}
}

func TestHandlerCancel_ReadScopeOnly_Returns403(t *testing.T) {
	env := newMergeHTTPTestEnv(t)

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "cancel-readscope-id",
		Nonce:         "cancel-readscope-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	// PAT with merges:read (not merges:write) — should be rejected for DELETE.
	auth := mergeReadAuth(newTestUUID("writer-cancel7"))

	rec := env.doMergeRequest(t, http.MethodDelete,
		"/api/v1/workspaces/my-workspace/merges/cancel-readscope-id", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-11-79: DELETE /merges/:id returns HTTP 409 with the updated status when
// the job transitions from queued to running between the status check and the
// UPDATE (race condition).
// Requirement: 11-REQ-13.E3
// ---------------------------------------------------------------------------

func TestHandlerCancel_RaceCondition_Returns409WithUpdatedStatus(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("writer-cancel8"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "cancel-race-id",
		Nonce:         "cancel-race-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	// Simulate the race condition: between the handler's SELECT (which sees
	// 'queued') and the conditional UPDATE, the worker transitions the job
	// to 'running'. We simulate this by changing status directly via SQL
	// before calling the handler. The handler's conditional
	// UPDATE ... WHERE status='queued' will affect 0 rows, triggering
	// the re-read path.
	_, err := env.db.Exec("UPDATE merge_jobs SET status = 'running' WHERE id = 'cancel-race-id'")
	if err != nil {
		t.Fatalf("failed to simulate race condition: %v", err)
	}

	rec := env.doMergeRequest(t, http.MethodDelete,
		"/api/v1/workspaces/my-workspace/merges/cancel-race-id", "", auth)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// The response should contain the current status 'running'.
	bodyStr := rec.Body.String()
	if !bodyContains(bodyStr, "running") {
		t.Error("error body does not contain 'running'; want current status in error message after race condition")
	}
}

// ===========================================================================
// 9.5 — POST /merges/:id/requeue endpoint tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-80: POST /merges/:id/requeue creates a new merge job with fresh nonce,
// status=queued, retry_count=0, available_at=now(), and submitted_by from
// GetAuthInfo; original dead-lettered job is unchanged.
// Requirement: 11-REQ-14.1
// ---------------------------------------------------------------------------

func TestHandlerRequeue_DeadLetterJob_Returns202WithNewJob(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	operatorID := newTestUUID("operator1")
	auth := mergeWriteAuth(operatorID)

	now := time.Now().UTC().Format(time.RFC3339)
	deadJob := &MergeJob{
		ID:              "requeue-dl-id",
		Nonce:           "requeue-dl-nonce",
		WorkspaceSlug:   "my-workspace",
		TargetBranch:    "main",
		SourceRef:       "spec/07",
		Status:          "dead_letter",
		RejectionReason: sql.NullString{String: "BeforeDependency", Valid: true},
		RetryCount:      20,
		AvailableAt:     now,
		SubmittedBy:     newTestUUID("original-submitter"),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	insertTestMergeJobFull(t, env.db, deadJob)

	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges/requeue-dl-id/requeue", "", auth)

	// Must return HTTP 202 Accepted.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// New job must have a different ID from the original.
	newJobID, ok := respBody["id"].(string)
	if !ok || newJobID == "" {
		t.Fatal("response has no 'id' field")
	}
	if newJobID == "requeue-dl-id" {
		t.Error("new job ID is the same as original; want a fresh UUID")
	}

	// status must be 'queued'.
	if status, ok := respBody["status"].(string); !ok || status != "queued" {
		t.Errorf("status = %v; want 'queued'", respBody["status"])
	}

	// retry_count must be 0.
	if rc, ok := respBody["retry_count"].(float64); !ok || int(rc) != 0 {
		t.Errorf("retry_count = %v; want 0", respBody["retry_count"])
	}

	// submitted_by must be the operator's UUID (from GetAuthInfo), not the
	// original submitter.
	if sb, ok := respBody["submitted_by"].(string); !ok || sb != operatorID {
		t.Errorf("submitted_by = %v; want %q (from GetAuthInfo of requeue caller)",
			respBody["submitted_by"], operatorID)
	}

	// source_ref must be copied from the original dead-lettered job.
	if sr, ok := respBody["source_ref"].(string); !ok || sr != "spec/07" {
		t.Errorf("source_ref = %v; want 'spec/07'", respBody["source_ref"])
	}

	// target_branch must be copied from the original dead-lettered job.
	if tb, ok := respBody["target_branch"].(string); !ok || tb != "main" {
		t.Errorf("target_branch = %v; want 'main'", respBody["target_branch"])
	}

	// nonce must NOT appear in the response.
	if _, hasNonce := respBody["nonce"]; hasNonce {
		t.Error("response body contains 'nonce' field; want nonce excluded from API response")
	}

	// Verify the new job has a fresh server-generated nonce in the DB.
	newDBNonce := getJobNonce(t, env.db, newJobID)
	if newDBNonce == "" {
		t.Error("new job DB nonce is empty; want fresh server-generated UUID")
	}
	if newDBNonce == "requeue-dl-nonce" {
		t.Error("new job nonce is the same as original; want a fresh nonce")
	}

	// Original dead-lettered job must be unchanged.
	origStatus := getJobStatus(t, env.db, "requeue-dl-id")
	if origStatus != "dead_letter" {
		t.Errorf("original job status = %q; want 'dead_letter' (unchanged)", origStatus)
	}

	origRetry := getJobRetryCount(t, env.db, "requeue-dl-id")
	if origRetry != 20 {
		t.Errorf("original job retry_count = %d; want 20 (unchanged)", origRetry)
	}
}

// ---------------------------------------------------------------------------
// TS-11-81: POST /merges/:id/requeue returns HTTP 409 when the job is not in
// dead_letter status.
// Requirement: 11-REQ-14.2
// ---------------------------------------------------------------------------

func TestHandlerRequeue_QueuedJob_Returns409(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("operator2"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "requeue-queued-id",
		Nonce:         "requeue-queued-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges/requeue-queued-id/requeue", "", auth)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if _, hasError := respBody["error"]; !hasError {
		t.Error("response body does not contain 'error' key; want apikit error body")
	}
}

func TestHandlerRequeue_MergedJob_Returns409(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("operator2b"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "requeue-merged-id",
		Nonce:         "requeue-merged-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "merged",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges/requeue-merged-id/requeue", "", auth)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestHandlerRequeue_RunningJob_Returns409(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("operator2c"))

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "requeue-running-id",
		Nonce:         "requeue-running-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "running",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges/requeue-running-id/requeue", "", auth)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}
}

// ===========================================================================
// 9.6 — POST /merges/:id/requeue edge-case tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-82: POST /merges/:id/requeue returns HTTP 404 when the job ID does
// not exist.
// Requirement: 11-REQ-14.E1
// ---------------------------------------------------------------------------

func TestHandlerRequeue_NonexistentJob_Returns404(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("operator3"))

	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges/nonexistent-uuid/requeue", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	if _, hasError := respBody["error"]; !hasError {
		t.Error("response body does not contain 'error' key; want apikit error body")
	}
}

// ---------------------------------------------------------------------------
// TS-11-83: POST /merges/:id/requeue returns HTTP 401 or 403 when the caller
// is unauthenticated or lacks merges:write scope.
// Requirement: 11-REQ-14.E2
// ---------------------------------------------------------------------------

func TestHandlerRequeue_Unauthenticated_Returns401(t *testing.T) {
	env := newMergeHTTPTestEnv(t)

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "requeue-noauth-id",
		Nonce:         "requeue-noauth-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "dead_letter",
		RetryCount:    20,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	// No auth token.
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges/requeue-noauth-id/requeue", "", nil)

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 401 or 403; body = %s",
			rec.Code, rec.Body.String())
	}
}

func TestHandlerRequeue_ReadScopeOnly_Returns403(t *testing.T) {
	env := newMergeHTTPTestEnv(t)

	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            "requeue-readscope-id",
		Nonce:         "requeue-readscope-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "dead_letter",
		RetryCount:    20,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, job)

	// PAT with merges:read (not merges:write) — should be rejected for POST /requeue.
	auth := mergeReadAuth(newTestUUID("operator4"))

	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges/requeue-readscope-id/requeue", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-11-84: POST /merges/:id/requeue returns HTTP 409 with existing_job_id
// when an active job already exists for the same (workspace_slug, source_ref).
// Requirement: 11-REQ-14.E3
// ---------------------------------------------------------------------------

func TestHandlerRequeue_DuplicateActiveJob_Returns409WithExistingJobID(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("operator5"))

	now := time.Now().UTC().Format(time.RFC3339)

	// Create a dead-lettered job to requeue.
	deadJob := &MergeJob{
		ID:            "requeue-dup-dead-id",
		Nonce:         "requeue-dup-dead-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "dead_letter",
		RetryCount:    20,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, deadJob)

	// Create an existing active (queued) job for the same source_ref.
	activeJob := &MergeJob{
		ID:            "requeue-dup-active-id",
		Nonce:         "requeue-dup-active-nonce",
		WorkspaceSlug: "my-workspace",
		TargetBranch:  "main",
		SourceRef:     "spec/07",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, env.db, activeJob)

	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges/requeue-dup-dead-id/requeue", "", auth)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusConflict, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// Must contain 'error' field.
	errVal := respBody["error"]
	if errVal == nil {
		t.Fatal("response body does not contain 'error' key; want error message")
	}
	errStr := fmt.Sprintf("%v", errVal)
	if !bodyContains(errStr, "merge already in progress") {
		t.Errorf("error = %q; want message containing 'merge already in progress'", errStr)
	}

	// Must contain 'existing_job_id' field matching the active job.
	existingID, ok := respBody["existing_job_id"].(string)
	if !ok || existingID == "" {
		t.Fatal("response body does not contain 'existing_job_id' field")
	}
	if existingID != "requeue-dup-active-id" {
		t.Errorf("existing_job_id = %q; want 'requeue-dup-active-id'", existingID)
	}
}
