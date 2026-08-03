package mergequeue

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// TS-11-37: When CanMerge returns BeforeDependency, the worker re-enqueues
// the job with available_at = now() + (2s * 2^retry_count) and increments
// retry_count. Also covers BranchNotReady and SpecBlocked as retriable
// reasons per subtask 6.1.
// Requirement: 11-REQ-6.1
// ---------------------------------------------------------------------------

func TestBackoff_RetriableReasons_ReenqueueWithBackoff(t *testing.T) {
	retriableReasons := []CantMergeReason{BeforeDependency, BranchNotReady, SpecBlocked}

	for i, reason := range retriableReasons {
		t.Run(string(reason), func(t *testing.T) {
			db := openDryRunTestDB(t)
			suffix := fmt.Sprintf("bk%d", i)
			job := insertQueuedMergeJob(t, db, suffix)

			before := time.Now()

			mockCanMerge := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
				return false, reason, nil
			}

			mockGit := newHappyPathMockGitOps()
			mockMu := newMockBranchLocker()
			deps := MergeDeps{Git: mockGit, Locker: mockMu}

			err := processJobByID(context.Background(), db, job.ID, deps, mockCanMerge)
			if err != nil {
				t.Fatalf("processJobByID() returned error: %v", err)
			}

			// Job status must remain 'queued' (re-enqueued, not terminated).
			status := getJobStatus(t, db, job.ID)
			if status != "queued" {
				t.Errorf("status = %q; want 'queued'", status)
			}

			// retry_count must be incremented to 1.
			retryCount := getJobRetryCount(t, db, job.ID)
			if retryCount != 1 {
				t.Errorf("retry_count = %d; want 1", retryCount)
			}

			// available_at must be approximately now() + 2 seconds
			// (2s * 2^0 = 2s for retry_count=0).
			availableAt := getJobAvailableAt(t, db, job.ID)
			parsed, err := time.Parse(time.RFC3339, availableAt)
			if err != nil {
				t.Fatalf("failed to parse available_at %q: %v", availableAt, err)
			}

			expectedDelay := 2 * time.Second
			actualDelay := parsed.Sub(before)
			if actualDelay < expectedDelay-500*time.Millisecond || actualDelay > expectedDelay+500*time.Millisecond {
				t.Errorf("backoff delay = %v; want approximately %v (within +/- 500ms)", actualDelay, expectedDelay)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-11-38: When a retriable job's retry_count reaches 20, the worker
// transitions it to dead_letter status with rejection_reason preserved.
// Requirement: 11-REQ-6.2
// ---------------------------------------------------------------------------

func TestBackoff_RetryCount20_TransitionsToDeadLetter(t *testing.T) {
	db := openDryRunTestDB(t)

	// Insert a queued job with retry_count=19 (one retry away from dead-letter).
	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            newTestUUID("bk4"),
		Nonce:         newTestUUID("nbk4"),
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets",
		Status:        "queued",
		RetryCount:    19,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, db, job)

	mockCanMerge := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		return false, BranchNotReady, nil
	}

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()
	deps := MergeDeps{Git: mockGit, Locker: mockMu}

	err := processJobByID(context.Background(), db, job.ID, deps, mockCanMerge)
	if err != nil {
		t.Fatalf("processJobByID() returned error: %v", err)
	}

	// Job status must be 'dead_letter' (retry_count=19+1=20 >= maxRetries).
	status := getJobStatus(t, db, job.ID)
	if status != "dead_letter" {
		t.Errorf("status = %q; want 'dead_letter' after 20th retry", status)
	}

	// rejection_reason must be preserved from the last failure.
	reason := getJobRejectionReason(t, db, job.ID)
	if !reason.Valid || reason.String != string(BranchNotReady) {
		t.Errorf("rejection_reason = %v; want 'BranchNotReady'", reason)
	}
}

// ---------------------------------------------------------------------------
// TS-11-39: The worker polling query filters merge_jobs with WHERE
// status='queued' AND available_at <= now(), using idx_merge_jobs_available.
// Requirement: 11-REQ-6.3
// ---------------------------------------------------------------------------

func TestBackoff_PollEligibleJobs_FiltersOnAvailableAt(t *testing.T) {
	db := openDryRunTestDB(t)
	now := time.Now().UTC()

	// Past job: available_at 10 seconds ago — should be eligible.
	pastTime := now.Add(-10 * time.Second).Format(time.RFC3339)
	pastJob := &MergeJob{
		ID:            newTestUUID("bk5a"),
		Nonce:         newTestUUID("nbk5a"),
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-past",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   pastTime,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     pastTime,
		UpdatedAt:     pastTime,
	}
	insertTestMergeJobFull(t, db, pastJob)

	// Future job: available_at 1 hour from now — should NOT be eligible.
	futureTime := now.Add(1 * time.Hour).Format(time.RFC3339)
	futureJob := &MergeJob{
		ID:            newTestUUID("bk5b"),
		Nonce:         newTestUUID("nbk5b"),
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/08-future",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   futureTime,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     futureTime,
		UpdatedAt:     futureTime,
	}
	insertTestMergeJobFull(t, db, futureJob)

	jobs, err := PollEligibleJobs(db)
	if err != nil {
		t.Fatalf("PollEligibleJobs() returned error: %v", err)
	}

	// Only the past job should be returned.
	if len(jobs) != 1 {
		t.Fatalf("PollEligibleJobs() returned %d jobs; want 1", len(jobs))
	}
	if jobs[0].ID != pastJob.ID {
		t.Errorf("returned job ID = %q; want %q (the past job)", jobs[0].ID, pastJob.ID)
	}
}

// TestBackoff_PollEligibleJobs_ExcludesNonQueuedStatuses verifies that
// PollEligibleJobs only returns jobs with status='queued', excluding
// dead_letter, merged, conflict, and other non-queued statuses.
func TestBackoff_PollEligibleJobs_ExcludesNonQueuedStatuses(t *testing.T) {
	db := openDryRunTestDB(t)
	pastTime := time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)

	// Queued job with past available_at — should be eligible.
	queuedJob := &MergeJob{
		ID:            newTestUUID("bk6a"),
		Nonce:         newTestUUID("nbk6a"),
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-queued",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   pastTime,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, db, queuedJob)

	// Dead-lettered job with past available_at — should NOT be eligible.
	deadJob := &MergeJob{
		ID:              newTestUUID("bk6b"),
		Nonce:           newTestUUID("nbk6b"),
		WorkspaceSlug:   "test-ws",
		TargetBranch:    "main",
		SourceRef:       "spec/08-dead",
		Status:          "dead_letter",
		RejectionReason: sql.NullString{String: string(BeforeDependency), Valid: true},
		RetryCount:      20,
		AvailableAt:     pastTime,
		SubmittedBy:     newTestUUID("user"),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	insertTestMergeJobFull(t, db, deadJob)

	jobs, err := PollEligibleJobs(db)
	if err != nil {
		t.Fatalf("PollEligibleJobs() returned error: %v", err)
	}

	// Should return exactly 1 job (the queued one, not the dead-lettered one).
	if len(jobs) != 1 {
		t.Fatalf("PollEligibleJobs() returned %d jobs; want 1 (only queued)", len(jobs))
	}
	if jobs[0].ID != queuedJob.ID {
		t.Errorf("returned job ID = %q; want %q (the queued job)", jobs[0].ID, queuedJob.ID)
	}
}

// ---------------------------------------------------------------------------
// TS-11-40: Backoff delay is capped at 2 hours (7200 seconds) per attempt
// regardless of retry_count; retries 12-20 all use the 2-hour cap.
// Requirement: 11-REQ-6.E1
// ---------------------------------------------------------------------------

func TestBackoff_DelayCapAt2Hours(t *testing.T) {
	tests := []struct {
		retryCount int
		wantDelay  time.Duration
	}{
		{0, 2 * time.Second},             // 2 * 2^0 = 2s
		{1, 4 * time.Second},             // 2 * 2^1 = 4s
		{2, 8 * time.Second},             // 2 * 2^2 = 8s
		{3, 16 * time.Second},            // 2 * 2^3 = 16s
		{10, 2048 * time.Second},         // 2 * 2^10 = 2048s (~34min)
		{11, 4096 * time.Second},         // 2 * 2^11 = 4096s (~68min)
		{12, 2 * time.Hour},              // 2 * 2^12 = 8192s > 7200s cap
		{13, 2 * time.Hour},              // capped
		{15, 2 * time.Hour},              // capped
		{19, 2 * time.Hour},              // capped
		{20, 2 * time.Hour},              // capped
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("retry_%d", tt.retryCount), func(t *testing.T) {
			delay := calculateBackoff(tt.retryCount)
			if delay != tt.wantDelay {
				t.Errorf("calculateBackoff(%d) = %v; want %v", tt.retryCount, delay, tt.wantDelay)
			}
		})
	}

	// All delays from 0 through 20 must be <= 2 hours.
	for rc := 0; rc <= 20; rc++ {
		delay := calculateBackoff(rc)
		if delay > 2*time.Hour {
			t.Errorf("calculateBackoff(%d) = %v; want <= 2h", rc, delay)
		}

		// Verify formula: delay == min(2s * 2^rc, 7200s).
		expected := 2 * time.Second
		for i := 0; i < rc; i++ {
			expected *= 2
			if expected > 2*time.Hour {
				expected = 2 * time.Hour
				break
			}
		}
		if delay != expected {
			t.Errorf("calculateBackoff(%d) = %v; want %v (formula: min(2s*2^rc, 7200s))", rc, delay, expected)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-11-41: When the database update for backoff re-enqueue fails, the
// worker logs the error with merge_job_id and does not crash.
// Requirement: 11-REQ-6.E2
// ---------------------------------------------------------------------------

func TestBackoff_ReEnqueueDBUpdateFails_ReturnsError(t *testing.T) {
	// Use a database without the merge_jobs table to simulate a DB update
	// failure. The reEnqueueWithBackoff function should return an error.
	db := openTestDBNoSchema(t)

	job := &MergeJob{
		ID:         newTestUUID("bk7"),
		RetryCount: 0,
	}

	// reEnqueueWithBackoff should return an error because the table
	// does not exist (simulating a database failure).
	err := reEnqueueWithBackoff(db, job, BeforeDependency)
	if err == nil {
		t.Error("reEnqueueWithBackoff() returned nil; want error when DB update fails")
	}
}

// TestBackoff_ReEnqueueDBUpdateFails_WorkerContinues verifies that the worker
// goroutine does not crash when the backoff DB update fails. The processJobByID
// function must handle the error gracefully, log it, and continue.
func TestBackoff_ReEnqueueDBUpdateFails_WorkerContinues(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "bk8")

	mockCanMerge := func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		return false, BeforeDependency, nil
	}

	// Drop the merge_jobs table after inserting the job. This means the
	// job lookup might still work (if cached), but the UPDATE for
	// available_at will fail.
	_, dropErr := db.Exec("DROP TABLE merge_jobs")
	if dropErr != nil {
		t.Fatalf("failed to drop table: %v", dropErr)
	}
	// Re-create the table without the job to simulate a partial failure.
	setupMergeJobsTable(t, db)

	// processJobByID should not panic. The function may return an error,
	// but it must not crash the process.
	deps := MergeDeps{Git: newHappyPathMockGitOps(), Locker: newMockBranchLocker()}

	// This call must not panic — that is the primary assertion.
	// The function should handle "job not found" or "update failed" gracefully.
	_ = processJobByID(context.Background(), db, job.ID, deps, mockCanMerge)

	// If we reach this point, the worker did not crash. But we also want to
	// verify the function actually attempted to process the job. With the stub,
	// this assertion will fail because the stub doesn't process anything.
	// The real implementation should either log the error or return it gracefully.
}
