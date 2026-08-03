package mergequeue

import (
	"testing"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// TS-11-6: The state machine allows only the documented transitions.
// Requirement: 11-REQ-2.1
// ---------------------------------------------------------------------------

func TestStateMachine_ValidTransitions(t *testing.T) {
	// Table-driven tests for all valid transitions documented in the spec:
	// prepared → queued
	// queued → running
	// running → merged
	// running → conflict
	// running → check_failed
	// running → push_failed
	// queued → cancelled
	// queued → dead_letter
	validTransitions := []struct {
		name string
		from string
		to   string
	}{
		{"prepared_to_queued", "prepared", "queued"},
		{"queued_to_running", "queued", "running"},
		{"running_to_merged", "running", "merged"},
		{"running_to_conflict", "running", "conflict"},
		{"running_to_check_failed", "running", "check_failed"},
		{"running_to_push_failed", "running", "push_failed"},
		{"queued_to_cancelled", "queued", "cancelled"},
		{"queued_to_dead_letter", "queued", "dead_letter"},
	}

	for _, tc := range validTransitions {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			id := newTestUUID(tc.from[:3] + tc.to[:3])
			insertTestMergeJob(t, db, id, newTestUUID("n"+tc.from[:3]+tc.to[:3]),
				"ws", "main", "spec/01", tc.from, newTestUUID("user"))

			err := UpdateStatus(db, id, tc.to)
			if err != nil {
				t.Fatalf("UpdateStatus(%q -> %q) returned error: %v", tc.from, tc.to, err)
			}

			got := getJobStatus(t, db, id)
			if got != tc.to {
				t.Errorf("status = %q; want %q", got, tc.to)
			}
		})
	}
}

func TestStateMachine_FullHappyPath(t *testing.T) {
	db := openTestDB(t)
	id := newTestUUID("happy1")
	insertTestMergeJob(t, db, id, newTestUUID("nhappy1"),
		"ws", "main", "spec/01", "prepared", newTestUUID("user"))

	// prepared → queued → running → merged
	transitions := []string{"queued", "running", "merged"}
	for _, target := range transitions {
		err := UpdateStatus(db, id, target)
		if err != nil {
			t.Fatalf("UpdateStatus(-> %q) returned error: %v", target, err)
		}
	}

	got := getJobStatus(t, db, id)
	if got != "merged" {
		t.Errorf("final status = %q; want %q", got, "merged")
	}
}

// ---------------------------------------------------------------------------
// TS-11-9: Attempting to transition a terminal-status job to another status
// is rejected with a non-nil error.
// Requirement: 11-REQ-2.E1
// ---------------------------------------------------------------------------

func TestStateMachine_TerminalStatusRejected(t *testing.T) {
	terminalStatuses := []string{
		"merged", "conflict", "check_failed", "push_failed",
		"cancelled", "dead_letter",
	}

	for _, terminal := range terminalStatuses {
		t.Run("from_"+terminal, func(t *testing.T) {
			db := openTestDB(t)
			id := newTestUUID("term" + terminal[:3])
			insertTestMergeJob(t, db, id, newTestUUID("nt"+terminal[:3]),
				"ws", "main", "spec/01", terminal, newTestUUID("user"))

			err := UpdateStatus(db, id, "queued")
			if err == nil {
				t.Errorf("UpdateStatus(%q -> queued) returned nil; want error", terminal)
			}

			// Verify the status is unchanged.
			got := getJobStatus(t, db, id)
			if got != terminal {
				t.Errorf("status changed to %q; want %q (unchanged)", got, terminal)
			}
		})
	}
}

func TestStateMachine_InvalidTransitions(t *testing.T) {
	// Test transitions that are not in the valid set but where the source
	// is non-terminal (i.e., the transition is undefined).
	invalidTransitions := []struct {
		name string
		from string
		to   string
	}{
		{"prepared_to_running", "prepared", "running"},
		{"prepared_to_merged", "prepared", "merged"},
		{"prepared_to_conflict", "prepared", "conflict"},
		{"prepared_to_cancelled", "prepared", "cancelled"},
		{"queued_to_merged", "queued", "merged"},
		{"queued_to_conflict", "queued", "conflict"},
		{"queued_to_check_failed", "queued", "check_failed"},
		{"queued_to_push_failed", "queued", "push_failed"},
		{"queued_to_prepared", "queued", "prepared"},
		{"running_to_queued", "running", "queued"},
		{"running_to_prepared", "running", "prepared"},
		{"running_to_cancelled", "running", "cancelled"},
		{"running_to_dead_letter", "running", "dead_letter"},
	}

	for _, tc := range invalidTransitions {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			id := newTestUUID("inv" + tc.from[:2] + tc.to[:2])
			insertTestMergeJob(t, db, id, newTestUUID("ni"+tc.from[:2]+tc.to[:2]),
				"ws", "main", "spec/01", tc.from, newTestUUID("user"))

			err := UpdateStatus(db, id, tc.to)
			if err == nil {
				t.Errorf("UpdateStatus(%q -> %q) returned nil; want error for invalid transition", tc.from, tc.to)
			}

			// Verify the status is unchanged.
			got := getJobStatus(t, db, id)
			if got != tc.from {
				t.Errorf("status changed to %q; want %q (unchanged)", got, tc.from)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-11-7: The worker transitions a queued job with available_at <= now()
// to running status before processing.
// Requirement: 11-REQ-2.2
//
// Note: This test validates the polling query and status transition.
// Full worker goroutine integration is tested in a later task group.
// For group 1, we verify the transition path is valid and the query
// semantics work.
// ---------------------------------------------------------------------------

func TestStateMachine_QueuedToRunning_AvailableAtPast(t *testing.T) {
	db := openTestDB(t)
	id := newTestUUID("poll1")
	insertTestMergeJob(t, db, id, newTestUUID("npoll1"),
		"ws", "main", "spec/01", "queued", newTestUUID("user"))

	// Verify the queued → running transition succeeds.
	err := UpdateStatus(db, id, "running")
	if err != nil {
		t.Fatalf("UpdateStatus(queued -> running) returned error: %v", err)
	}

	got := getJobStatus(t, db, id)
	if got != "running" {
		t.Errorf("status = %q; want 'running'", got)
	}
}

func TestStateMachine_QueuedToRunning_AvailableAtFuture_NotVisible(t *testing.T) {
	db := openTestDB(t)

	// Insert a job with available_at far in the future.
	id := newTestUUID("fut1")
	insertTestMergeJob(t, db, id, newTestUUID("nfut1"),
		"ws", "main", "spec/01", "queued", newTestUUID("user"))

	// Update available_at to the future.
	futureTime := "2099-01-01T00:00:00Z"
	_, err := db.Exec("UPDATE merge_jobs SET available_at = ? WHERE id = ?", futureTime, id)
	if err != nil {
		t.Fatalf("UPDATE available_at failed: %v", err)
	}

	// Query for eligible jobs (available_at <= now) should not return this job.
	var count int
	err = db.QueryRow(
		"SELECT COUNT(*) FROM merge_jobs WHERE status = 'queued' AND available_at <= datetime('now')",
	).Scan(&count)
	if err != nil {
		t.Fatalf("polling query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("polling query returned %d jobs; want 0 (job has future available_at)", count)
	}
}

// ---------------------------------------------------------------------------
// TS-11-8: After all merge steps succeed, the worker transitions the job to
// merged status and records merged_sha.
// Requirement: 11-REQ-2.3
//
// Note: Full worker processing with mock GitRunner is tested in later task
// groups. For group 1, we validate the transition path and verify
// merged_sha can be set on the record.
// ---------------------------------------------------------------------------

func TestStateMachine_RunningToMerged_WithMergedSHA(t *testing.T) {
	db := openTestDB(t)
	id := newTestUUID("msha1")
	insertTestMergeJob(t, db, id, newTestUUID("nmsha1"),
		"ws", "main", "spec/01", "running", newTestUUID("user"))

	// Transition to merged and set merged_sha (simulating what the worker does).
	err := UpdateStatus(db, id, "merged")
	if err != nil {
		t.Fatalf("UpdateStatus(running -> merged) returned error: %v", err)
	}

	// Set merged_sha via direct update (worker would do this atomically).
	mergedSHA := "abc123def456"
	_, err = db.Exec("UPDATE merge_jobs SET merged_sha = ? WHERE id = ?", mergedSHA, id)
	if err != nil {
		t.Fatalf("UPDATE merged_sha failed: %v", err)
	}

	// Verify both status and merged_sha.
	got := getJobStatus(t, db, id)
	if got != "merged" {
		t.Errorf("status = %q; want 'merged'", got)
	}

	var sha string
	err = db.QueryRow("SELECT merged_sha FROM merge_jobs WHERE id = ?", id).Scan(&sha)
	if err != nil {
		t.Fatalf("query merged_sha failed: %v", err)
	}
	if sha != mergedSHA {
		t.Errorf("merged_sha = %q; want %q", sha, mergedSHA)
	}
}

// ---------------------------------------------------------------------------
// TS-11-10: A job found in 'running' status at worker startup is treated as
// stale and not re-executed.
// Requirement: 11-REQ-2.E2
//
// Note: Full worker startup behavior is tested in later task groups.
// For group 1, we verify that a polling query for eligible jobs does NOT
// pick up jobs with status='running' — only 'queued' jobs are eligible.
// ---------------------------------------------------------------------------

func TestStateMachine_RunningJobNotPickedByPoll(t *testing.T) {
	db := openTestDB(t)

	// Insert a stale running job (simulating a crash during previous run).
	insertTestMergeJob(t, db, newTestUUID("stale1"), newTestUUID("nstale1"),
		"ws", "main", "spec/01", "running", newTestUUID("user"))

	// The worker's polling query should only look for queued jobs.
	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM merge_jobs WHERE status = 'queued' AND available_at <= datetime('now')",
	).Scan(&count)
	if err != nil {
		t.Fatalf("polling query failed: %v", err)
	}
	if count != 0 {
		t.Errorf("polling query returned %d jobs; want 0 (running job should not be picked up)", count)
	}

	// The running job should still be in running status (untouched).
	got := getJobStatus(t, db, newTestUUID("stale1"))
	if got != "running" {
		t.Errorf("stale job status = %q; want 'running' (untouched)", got)
	}
}

// ---------------------------------------------------------------------------
// Additional state machine edge case tests.
// Subtask 1.4: Boundary conditions for the state machine.
// ---------------------------------------------------------------------------

func TestStateMachine_CancelFromRunning_Rejected(t *testing.T) {
	db := openTestDB(t)
	id := newTestUUID("canrun1")
	insertTestMergeJob(t, db, id, newTestUUID("ncanrun1"),
		"ws", "main", "spec/01", "running", newTestUUID("user"))

	err := UpdateStatus(db, id, "cancelled")
	if err == nil {
		t.Fatal("UpdateStatus(running -> cancelled) returned nil; want error")
	}

	got := getJobStatus(t, db, id)
	if got != "running" {
		t.Errorf("status changed to %q; want 'running' (unchanged)", got)
	}
}

func TestStateMachine_CancelFromDeadLetter_Rejected(t *testing.T) {
	db := openTestDB(t)
	id := newTestUUID("candl1")
	insertTestMergeJob(t, db, id, newTestUUID("ncandl1"),
		"ws", "main", "spec/01", "dead_letter", newTestUUID("user"))

	err := UpdateStatus(db, id, "cancelled")
	if err == nil {
		t.Fatal("UpdateStatus(dead_letter -> cancelled) returned nil; want error")
	}

	got := getJobStatus(t, db, id)
	if got != "dead_letter" {
		t.Errorf("status changed to %q; want 'dead_letter' (unchanged)", got)
	}
}

func TestStateMachine_PreparedNotReachableFromOther(t *testing.T) {
	// No status should be able to transition to 'prepared'.
	fromStatuses := []string{
		"queued", "running", "merged", "conflict",
		"check_failed", "push_failed", "cancelled", "dead_letter",
	}

	for _, from := range fromStatuses {
		t.Run("from_"+from+"_to_prepared", func(t *testing.T) {
			db := openTestDB(t)
			id := newTestUUID("prp" + from[:3])
			insertTestMergeJob(t, db, id, newTestUUID("np"+from[:3]),
				"ws", "main", "spec/01", from, newTestUUID("user"))

			err := UpdateStatus(db, id, "prepared")
			if err == nil {
				t.Errorf("UpdateStatus(%q -> prepared) returned nil; want error", from)
			}

			got := getJobStatus(t, db, id)
			if got != from {
				t.Errorf("status changed to %q; want %q (unchanged)", got, from)
			}
		})
	}
}
