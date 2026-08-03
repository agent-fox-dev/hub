package mergequeue

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Mock helpers specific to worker tests
// ---------------------------------------------------------------------------

// newSlowMockGitOps creates a mockGitOps that introduces a delay in the
// rebase step to simulate slow merge processing. All other operations
// succeed immediately. Used for testing graceful shutdown timing.
func newSlowMockGitOps(delay time.Duration) *mockGitOps {
	return &mockGitOps{
		onRun: func(_ context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 {
				switch args[0] {
				case "rev-parse":
					for _, a := range args {
						if strings.HasPrefix(a, "origin/") {
							return []byte(testTargetHead + "\n"), nil, nil
						}
					}
					return []byte(testMergedHead + "\n"), nil, nil
				case "rebase":
					// Introduce delay to simulate slow rebase.
					time.Sleep(delay)
					return nil, nil, nil
				}
			}
			return nil, nil, nil
		},
		onRunExitCode: func(_ context.Context, _ ...string) ([]byte, []byte, int, error) {
			return nil, nil, 0, nil
		},
	}
}

// alwaysCanMerge returns a CanMergeFunc that always approves the merge.
func alwaysCanMerge() CanMergeFunc {
	return func(_ context.Context, _ *sql.DB, _ MergeJob) (bool, CantMergeReason, error) {
		return true, "", nil
	}
}

// ---------------------------------------------------------------------------
// TS-11-50: The merge queue runs as a single global worker goroutine.
// Second Start() is idempotent — does not add a second worker.
// Requirement: 11-REQ-9.1
// ---------------------------------------------------------------------------

func TestWorker_SingleGoroutine_StartIdempotent(t *testing.T) {
	db := openDryRunTestDB(t)
	deps := MergeDeps{Git: newHappyPathMockGitOps(), Locker: newMockBranchLocker()}
	q := NewQueue(db, deps, nil)

	q.Start()
	t.Cleanup(func() { q.Stop() })
	time.Sleep(20 * time.Millisecond)

	// After Start(), the worker should be running.
	if !q.workerRunning() {
		t.Error("workerRunning() = false after Start(); want true")
	}

	// Second Start() should be idempotent — no second worker.
	q.Start()
	time.Sleep(20 * time.Millisecond)

	// Still exactly one worker — no additional goroutine spawned.
	if !q.workerRunning() {
		t.Error("workerRunning() = false after second Start(); want true (idempotent)")
	}
}

// ---------------------------------------------------------------------------
// TS-11-51: When a new job is enqueued, a non-blocking send on the
// buffered(1) wakeup channel interrupts the worker's poll sleep.
// Multiple rapid enqueues coalesce into a single wakeup.
// Requirement: 11-REQ-9.2
// ---------------------------------------------------------------------------

func TestWorker_WakeupCoalescing(t *testing.T) {
	db := openDryRunTestDB(t)
	job1 := insertQueuedMergeJob(t, db, "wk1")
	_ = insertQueuedMergeJob(t, db, "wk2")
	_ = insertQueuedMergeJob(t, db, "wk3")

	deps := MergeDeps{Git: newHappyPathMockGitOps(), Locker: newMockBranchLocker()}
	q := NewQueue(db, deps, alwaysCanMerge())
	q.Start()
	t.Cleanup(func() { q.Stop() })

	// Three rapid Notify calls: buffered(1) channel coalesces them.
	q.Notify()
	q.Notify()
	q.Notify()

	// Wakeup channel capacity must be exactly 1 to enable coalescing.
	if cap(q.wakeup) != 1 {
		t.Errorf("wakeup channel capacity = %d; want 1", cap(q.wakeup))
	}

	// After coalescing, at most 1 notification is pending.
	if len(q.wakeup) > 1 {
		t.Errorf("wakeup channel len = %d after 3 Notify(); want <= 1 (coalesced)", len(q.wakeup))
	}

	// Wait for worker to process jobs.
	time.Sleep(500 * time.Millisecond)

	// Worker should have processed at least the first job.
	status := getJobStatus(t, db, job1.ID)
	if status == "queued" {
		t.Errorf("job1 status = %q after wakeup + 500ms; want processed (not queued)", status)
	}
}

// TestWorker_WakeupPicksUpJobImmediately verifies that Notify() triggers
// immediate job processing rather than waiting for the 10-second fallback
// timer to fire. The job must be picked up well before the timer interval.
func TestWorker_WakeupPicksUpJobImmediately(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "wkimm")

	deps := MergeDeps{Git: newHappyPathMockGitOps(), Locker: newMockBranchLocker()}
	q := NewQueue(db, deps, alwaysCanMerge())
	q.Start()
	t.Cleanup(func() { q.Stop() })

	q.Notify()

	// Wait up to 2 seconds — well under the 10-second fallback timer.
	// If the wakeup mechanism works, the job should be processed quickly.
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			status := getJobStatus(t, db, job.ID)
			t.Fatalf("job status = %q after 2s; want processed within 2s of Notify()", status)
			return
		case <-ticker.C:
			status := getJobStatus(t, db, job.ID)
			if status != "queued" {
				// Job was processed — pass.
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Task 7.2: Worker three-way select and fallback poll
// ---------------------------------------------------------------------------

// TestWorker_FallbackTimerPolls verifies that the fallback timer picks up
// backoff-delayed jobs whose available_at has elapsed, even without an
// explicit Notify() call. Uses a shortened pollInterval for test speed.
func TestWorker_FallbackTimerPolls(t *testing.T) {
	db := openDryRunTestDB(t)
	// Insert a job with available_at in the past — should be eligible.
	job := insertQueuedMergeJob(t, db, "wktmr")

	deps := MergeDeps{Git: newHappyPathMockGitOps(), Locker: newMockBranchLocker()}
	q := NewQueue(db, deps, alwaysCanMerge())
	q.pollInterval = 100 * time.Millisecond // use short interval for test speed
	q.Start()
	t.Cleanup(func() { q.Stop() })

	// Do NOT call Notify() — rely on the fallback timer.
	time.Sleep(500 * time.Millisecond) // wait for several timer ticks

	// The fallback timer should have polled and picked up the job.
	status := getJobStatus(t, db, job.ID)
	if status == "queued" {
		t.Errorf("job status = %q; want processed by fallback timer poll (no Notify)", status)
	}
}

// TestWorker_StopExitsWorkerPromptly verifies that calling Stop() on a
// queue with no in-flight operations returns promptly. This tests the
// stopCh arm of the three-way select.
func TestWorker_StopExitsWorkerPromptly(t *testing.T) {
	db := openDryRunTestDB(t)
	deps := MergeDeps{Git: newHappyPathMockGitOps(), Locker: newMockBranchLocker()}
	q := NewQueue(db, deps, nil)
	q.Start()

	time.Sleep(20 * time.Millisecond)

	// Verify worker is running before we stop.
	if !q.workerRunning() {
		t.Fatal("workerRunning() = false after Start(); want true")
	}

	// Stop should return promptly when no in-flight operations exist.
	done := make(chan struct{})
	go func() {
		q.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Stop returned — worker exited cleanly.
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return within 5s; want prompt return when no in-flight work")
	}
}

// TestWorker_ThreeWaySelectHandlesWakeup verifies the wakeup arm of the
// three-way select: when a wakeup signal is received, the worker polls
// for eligible jobs and processes them.
func TestWorker_ThreeWaySelectHandlesWakeup(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "wksel")

	mockGit := newHappyPathMockGitOps()
	deps := MergeDeps{Git: mockGit, Locker: newMockBranchLocker()}
	q := NewQueue(db, deps, alwaysCanMerge())
	q.pollInterval = 1 * time.Hour // very long timer to ensure wakeup is the trigger
	q.Start()
	t.Cleanup(func() { q.Stop() })

	// With a 1-hour poll interval, the only way for the worker to process
	// this job within 2 seconds is via the wakeup channel.
	q.Notify()
	time.Sleep(500 * time.Millisecond)

	status := getJobStatus(t, db, job.ID)
	if status == "queued" {
		t.Errorf("job status = %q; want processed via wakeup (timer set to 1h)", status)
	}
}

// ---------------------------------------------------------------------------
// TS-11-52: Stop() closes stopCh and blocks on stopWaitGroup.Wait()
// until all in-flight merge operations complete before returning.
// Requirement: 11-REQ-9.3
// ---------------------------------------------------------------------------

func TestGracefulShutdown_StopBlocksUntilInFlightCompletes(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "wkstop")

	// Slow mock git takes 100ms for the rebase step.
	slowGit := newSlowMockGitOps(100 * time.Millisecond)
	deps := MergeDeps{Git: slowGit, Locker: newMockBranchLocker()}
	q := NewQueue(db, deps, alwaysCanMerge())
	q.Start()

	q.Notify()
	time.Sleep(10 * time.Millisecond) // let the worker pick up the job

	start := time.Now()
	q.Stop()
	elapsed := time.Since(start)

	// Stop() must block until the in-flight operation completes (~100ms).
	if elapsed < 80*time.Millisecond {
		t.Errorf("Stop() returned in %v; want >= 80ms (in-flight operation takes 100ms)", elapsed)
	}

	// After Stop, the job should have completed successfully.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("job status = %q; want 'merged' after in-flight operation completed", status)
	}
}

// ---------------------------------------------------------------------------
// TS-11-53: When the polling query returns no eligible jobs, the worker
// returns to the three-way select and waits without spinning.
// Requirement: 11-REQ-9.E1
// ---------------------------------------------------------------------------

func TestGracefulShutdown_NoEligibleJobs_NoSpin(t *testing.T) {
	db := openDryRunTestDB(t)
	// No jobs inserted — worker should have nothing to process.

	mockGit := newHappyPathMockGitOps()
	deps := MergeDeps{Git: mockGit, Locker: newMockBranchLocker()}
	q := NewQueue(db, deps, nil)
	q.Start()
	t.Cleanup(func() { q.Stop() })

	time.Sleep(50 * time.Millisecond)

	// Worker should be running but idle.
	if !q.workerRunning() {
		t.Error("workerRunning() = false; want true (worker should be running even with no jobs)")
	}

	// No git operations should have occurred (no jobs to process).
	calls := mockGit.recordedCalls()
	if len(calls) > 0 {
		t.Errorf("mockGit recorded %d calls; want 0 (no jobs to process)", len(calls))
	}
}

// ---------------------------------------------------------------------------
// TS-11-54: When stopCh is closed while a merge operation is in progress,
// the in-flight operation runs to completion and no new jobs are started.
// Requirement: 11-REQ-9.E2
// ---------------------------------------------------------------------------

func TestGracefulShutdown_InFlightCompletes_SecondSkipped(t *testing.T) {
	db := openDryRunTestDB(t)
	job1 := insertQueuedMergeJob(t, db, "wksh1")
	job2 := insertQueuedMergeJob(t, db, "wksh2")

	// Slow mock git ensures the first job takes ~100ms to process.
	slowGit := newSlowMockGitOps(100 * time.Millisecond)
	deps := MergeDeps{Git: slowGit, Locker: newMockBranchLocker()}
	q := NewQueue(db, deps, alwaysCanMerge())
	q.Start()

	q.Notify()
	time.Sleep(10 * time.Millisecond) // let the worker pick up job1

	// Stop while job1 is in progress. This should:
	// 1. Let job1 complete (in-flight operations finish).
	// 2. NOT start processing job2 after the shutdown signal.
	q.Stop()

	// First job should have completed.
	status1 := getJobStatus(t, db, job1.ID)
	if status1 != "merged" {
		t.Errorf("job1 status = %q; want 'merged' (in-flight should complete)", status1)
	}

	// Second job should NOT have been processed.
	status2 := getJobStatus(t, db, job2.ID)
	if status2 != "queued" {
		t.Errorf("job2 status = %q; want 'queued' (should not be processed after shutdown)", status2)
	}
}

// ---------------------------------------------------------------------------
// TS-11-55: When the database polling query fails, the worker logs the
// error and waits for the next timer tick without crashing.
// Requirement: 11-REQ-9.E3
// ---------------------------------------------------------------------------

func TestGracefulShutdown_DBPollError_WorkerContinues(t *testing.T) {
	db := openDryRunTestDB(t)

	deps := MergeDeps{Git: newHappyPathMockGitOps(), Locker: newMockBranchLocker()}
	q := NewQueue(db, deps, alwaysCanMerge())
	q.pollInterval = 50 * time.Millisecond
	q.Start()
	t.Cleanup(func() { q.Stop() })

	// Drop the merge_jobs table to force a DB poll error.
	_, err := db.Exec("DROP TABLE merge_jobs")
	if err != nil {
		t.Fatalf("failed to drop merge_jobs table: %v", err)
	}

	// Wait for the worker to encounter the poll error.
	time.Sleep(150 * time.Millisecond)

	// Worker goroutine should still be running despite the error.
	if !q.workerRunning() {
		t.Error("workerRunning() = false after DB error; want true (worker should survive poll errors)")
	}

	// Recreate the table and insert a job to verify the worker can recover.
	setupMergeJobsTable(t, db)
	job := insertQueuedMergeJob(t, db, "wkrcvr")
	q.Notify()
	time.Sleep(300 * time.Millisecond)

	// After table is recreated, the worker should eventually process the job.
	status := getJobStatus(t, db, job.ID)
	if status == "queued" {
		t.Errorf("job status = %q after DB recovery; want processed", status)
	}
}
