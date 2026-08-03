package mergequeue

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// TS-11-42: The POST /merges handler generates a UUID nonce server-side,
// inserts a merge job with status=prepared and available_at=now(), then
// enqueues it setting status to queued, returning HTTP 202 Accepted.
// Requirement: 11-REQ-7.1
// ---------------------------------------------------------------------------

func TestNonce_ServerGeneratesUUID_Returns202(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user1"))

	body := `{"target_branch":"main","source_ref":"spec/07-secrets"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	// Handler must return HTTP 202 Accepted.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	// Response body must be a JSON merge job object.
	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// Status in response must be 'queued'.
	if status, ok := respBody["status"].(string); !ok || status != "queued" {
		t.Errorf("response status = %v; want 'queued'", respBody["status"])
	}

	// Nonce must NOT appear in the response.
	if _, hasNonce := respBody["nonce"]; hasNonce {
		t.Error("response body contains 'nonce' field; want nonce excluded from API response")
	}

	// Response must include an 'id' field.
	jobID, ok := respBody["id"].(string)
	if !ok || jobID == "" {
		t.Fatalf("response body has no 'id' field or it is empty")
	}

	// Verify database state: nonce is non-empty and server-generated.
	dbNonce := getJobNonce(t, env.db, jobID)
	if dbNonce == "" {
		t.Error("DB nonce is empty; want server-generated UUID")
	}

	// available_at must be set to approximately now (not a future time).
	availableAt := getJobAvailableAt(t, env.db, jobID)
	parsed, err := time.Parse(time.RFC3339, availableAt)
	if err != nil {
		t.Fatalf("failed to parse available_at %q: %v", availableAt, err)
	}
	if time.Since(parsed) > 5*time.Second {
		t.Errorf("available_at = %q; want approximately now (within 5s)", availableAt)
	}
}

// ---------------------------------------------------------------------------
// TS-11-43: The worker validates the nonce and checks status before
// executing; skips if status is running or merged.
// Requirement: 11-REQ-7.2
// ---------------------------------------------------------------------------

func TestNonce_WorkerSkipsIfStatusMerged(t *testing.T) {
	db := openDryRunTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)

	// Create a job already in 'merged' status.
	job := &MergeJob{
		ID:            newTestUUID("nc1"),
		Nonce:         newTestUUID("nnc1"),
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets",
		Status:        "merged",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
		MergedSHA:     sql.NullString{String: "abc123", Valid: true},
	}
	insertTestMergeJobFull(t, db, job)

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()
	deps := MergeDeps{Git: mockGit, Locker: mockMu}

	// CanMerge should NOT be called for a merged job.
	canMergeCalled := false
	mockCanMerge := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		canMergeCalled = true
		return true, "", nil
	}

	err := processJobByID(context.Background(), db, job.ID, deps, mockCanMerge)
	if err != nil {
		t.Fatalf("processJobByID() returned error: %v", err)
	}

	// No git operations should have been executed.
	calls := mockGit.recordedCalls()
	if len(calls) != 0 {
		t.Errorf("git operations recorded = %d; want 0 (merged job should be skipped)", len(calls))
	}

	// Job status must remain 'merged'.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("status = %q; want 'merged' (unchanged)", status)
	}

	// CanMerge should NOT have been called.
	if canMergeCalled {
		t.Error("CanMerge was called for a merged job; want it skipped before reaching CanMerge")
	}
}

// TestNonce_WorkerSkipsIfStatusRunning verifies that the worker skips
// execution when it dequeues a job that is already in 'running' status.
func TestNonce_WorkerSkipsIfStatusRunning(t *testing.T) {
	db := openDryRunTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)

	job := &MergeJob{
		ID:            newTestUUID("nc1b"),
		Nonce:         newTestUUID("nnc1b"),
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets",
		Status:        "running",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
		BaseSHA:       sql.NullString{String: "abc123", Valid: true},
	}
	insertTestMergeJobFull(t, db, job)

	mockGit := newHappyPathMockGitOps()
	deps := MergeDeps{Git: mockGit, Locker: newMockBranchLocker()}

	canMergeCalled := false
	mockCanMerge := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		canMergeCalled = true
		return true, "", nil
	}

	err := processJobByID(context.Background(), db, job.ID, deps, mockCanMerge)
	if err != nil {
		t.Fatalf("processJobByID() returned error: %v", err)
	}

	// No git operations should have been executed.
	calls := mockGit.recordedCalls()
	if len(calls) != 0 {
		t.Errorf("git operations recorded = %d; want 0 (running job should be skipped)", len(calls))
	}

	// CanMerge should NOT have been called.
	if canMergeCalled {
		t.Error("CanMerge was called for a running job; want it skipped")
	}
}

// ---------------------------------------------------------------------------
// TS-11-44: The hub server never accepts a nonce from external API callers;
// nonce is always server-generated and never included in API responses.
// Requirement: 11-REQ-7.3
// ---------------------------------------------------------------------------

func TestNonce_NotAcceptedFromCaller_NotInResponse(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user2"))

	// Send a request with a caller-supplied nonce in the body.
	body := `{"target_branch":"main","source_ref":"spec/07","nonce":"caller-supplied-nonce"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	// Handler must return HTTP 202.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	// Response must NOT contain a nonce field.
	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if _, hasNonce := respBody["nonce"]; hasNonce {
		t.Error("response contains 'nonce' field; want nonce excluded from all API responses")
	}

	// DB nonce must NOT be the caller-supplied value.
	jobID, ok := respBody["id"].(string)
	if !ok || jobID == "" {
		t.Fatal("response has no 'id'; cannot verify DB nonce")
	}
	dbNonce := getJobNonce(t, env.db, jobID)
	if dbNonce == "caller-supplied-nonce" {
		t.Error("DB nonce == 'caller-supplied-nonce'; want server-generated nonce (caller value must be ignored)")
	}
}

// ---------------------------------------------------------------------------
// TS-11-45: When the caller's database transaction is rolled back after the
// job ID is sent to the queue channel, the worker finds no record and
// discards the job without error.
// Requirement: 11-REQ-7.E1
// ---------------------------------------------------------------------------

func TestNonce_RolledBackTransaction_WorkerDiscardsJob(t *testing.T) {
	db := openDryRunTestDB(t)

	// Use a non-existent job ID (simulates a rolled-back INSERT).
	nonExistentID := newTestUUID("nc3")

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()
	deps := MergeDeps{Git: mockGit, Locker: mockMu}

	mockCanMerge := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		t.Error("CanMerge should not be called for a non-existent job")
		return false, "", nil
	}

	// processJobByID should return nil (discard without error).
	err := processJobByID(context.Background(), db, nonExistentID, deps, mockCanMerge)
	if err != nil {
		t.Errorf("processJobByID() returned error %v; want nil (discard silently)", err)
	}

	// No git operations should have been executed.
	calls := mockGit.recordedCalls()
	if len(calls) != 0 {
		t.Errorf("git operations recorded = %d; want 0 (no record to process)", len(calls))
	}
}

// ---------------------------------------------------------------------------
// TS-11-46: When the worker receives the same job ID twice, the second
// dequeue finds status=running or merged and skips execution.
// Requirement: 11-REQ-7.E2
// ---------------------------------------------------------------------------

func TestNonce_DuplicateJobID_SecondDequeueSkips(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "nc4")

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()
	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: t.TempDir(),
	}

	// CanMerge returns true — merge can proceed.
	mockCanMerge := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		return true, "", nil
	}

	// First dequeue: should process and merge the job.
	err := processJobByID(context.Background(), db, job.ID, deps, mockCanMerge)
	if err != nil {
		t.Fatalf("first processJobByID() returned error: %v", err)
	}

	// Second dequeue: should find status=merged and skip.
	err = processJobByID(context.Background(), db, job.ID, deps, mockCanMerge)
	if err != nil {
		t.Fatalf("second processJobByID() returned error: %v", err)
	}

	// Git push should have been called exactly once (not twice).
	calls := mockGit.recordedCalls()
	pushCount := 0
	for _, c := range calls {
		if c.Method == "Run" && len(c.Args) >= 1 && c.Args[0] == "push" {
			pushCount++
		}
	}
	if pushCount != 1 {
		t.Errorf("push call count = %d; want 1 (second dequeue should skip)", pushCount)
	}

	// Final status must be 'merged'.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("status = %q; want 'merged' after both dequeues", status)
	}
}

// TestNonce_PreparedStatusIsProcessed verifies that a job in 'prepared'
// status is eligible for processing by the worker (it transitions to queued
// then proceeds with the merge pipeline).
func TestNonce_PreparedStatusIsProcessed(t *testing.T) {
	db := openDryRunTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)

	job := &MergeJob{
		ID:            newTestUUID("nc5"),
		Nonce:         newTestUUID("nnc5"),
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets",
		Status:        "prepared",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, db, job)

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()
	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: t.TempDir(),
	}

	// CanMerge returns true — merge can proceed.
	mockCanMerge := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		return true, "", nil
	}

	err := processJobByID(context.Background(), db, job.ID, deps, mockCanMerge)
	if err != nil {
		t.Fatalf("processJobByID() returned error: %v", err)
	}

	// Job should have been processed — status should transition to merged.
	status := getJobStatus(t, db, job.ID)
	if status == "prepared" {
		t.Error("status is still 'prepared'; want job to be processed (transition to running/merged)")
	}

	// At minimum, the merge pipeline should have been invoked.
	hasMergeOps := false
	for _, c := range mockGit.recordedCalls() {
		if c.Method == "Run" && len(c.Args) >= 1 {
			if c.Args[0] == "fetch" || c.Args[0] == "rebase" || c.Args[0] == "push" {
				hasMergeOps = true
				break
			}
		}
	}
	if !hasMergeOps {
		// Check for at least rev-parse (part of the merge pipeline).
		for _, c := range mockGit.recordedCalls() {
			if c.Method == "Run" && len(c.Args) >= 1 && c.Args[0] == "rev-parse" {
				hasMergeOps = true
				break
			}
		}
	}
	if !hasMergeOps {
		t.Error("no merge pipeline git operations recorded; want prepared job to be processed")
	}
}

// ---------------------------------------------------------------------------
// Additional: nonce is never included in any API response (property test)
// Requirement: 11-PROP-6
// ---------------------------------------------------------------------------

func TestNonce_ResponseFieldsDoNotIncludeNonce(t *testing.T) {
	env := newMergeHTTPTestEnv(t)
	auth := mergeWriteAuth(newTestUUID("user3"))

	body := `{"target_branch":"main","source_ref":"spec/07-check"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	// Parse response as raw JSON to check for nonce field.
	rawBody := rec.Body.String()
	if strings.Contains(rawBody, `"nonce"`) {
		t.Errorf("response JSON contains 'nonce' key; want nonce excluded from all API responses.\nBody: %s", rawBody)
	}
}
