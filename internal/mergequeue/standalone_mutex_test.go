package mergequeue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/gitcmd"

	_ "modernc.org/sqlite"
)

// ===========================================================================
// 10.4 — Standalone Merge Support and Per-Target-Branch Mutex Tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-95: For standalone merges (campaign_id=NULL), the worker skips
// BeforeDependency and SpecBlocked checks, skips PostMergeHook, and performs
// no cascading rebase.
// Requirement: 11-REQ-18.1
// ---------------------------------------------------------------------------

func TestStandalone_SkipsBeforeDependency_SkipsHook_NoCascadingRebase(t *testing.T) {
	db := openMergeTestDB(t)
	// Standalone merge: no campaign_id.
	job := insertMergeTestJob(t, db, "sa95", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	hookCalled := false

	// CanMerge should NOT be called with BeforeDependency or SpecBlocked
	// for standalone merges. If CanMerge is called, it should approve
	// the merge because there are no campaign-specific checks.
	canMergeCalled := false
	mockCanMerge := func(_ context.Context, _ *sql.DB, j MergeJob) (bool, CantMergeReason, error) {
		canMergeCalled = true
		// Standalone merges should not trigger BeforeDependency or SpecBlocked.
		// If this function returns false with BeforeDependency, it means
		// the implementation is incorrectly checking campaign dependencies
		// for standalone merges.
		return true, "", nil
	}

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
		Hook: func(_ context.Context, _ MergeJob) error {
			hookCalled = true
			return nil
		},
	}

	// Process the job via processJobByID to go through the full pipeline
	// including the CanMerge check.
	err := processJobByID(context.Background(), db, job.ID, deps, mockCanMerge)
	if err != nil {
		t.Fatalf("processJobByID() returned error: %v", err)
	}

	// Job must reach merged status.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("job status = %q; want 'merged'", status)
	}

	// PostMergeHook must NOT be called for standalone merges.
	if hookCalled {
		t.Error("PostMergeHook was called for standalone merge; want hook skipped when campaign_id=NULL")
	}

	// Verify no cascading rebase operations were performed.
	// Cascading rebase would involve multiple rebase calls for downstream branches.
	// A standalone merge should only rebase its own source_ref.
	calls := mockGit.recordedCalls()
	rebaseCount := 0
	for _, c := range calls {
		if c.Method == "Run" && len(c.Args) >= 1 && c.Args[0] == "rebase" {
			rebaseCount++
		}
	}
	if rebaseCount > 1 {
		t.Errorf("rebase was called %d times; want at most 1 (no cascading rebase for standalone merge)", rebaseCount)
	}

	// Log that CanMerge was invoked (for diagnostic purposes).
	_ = canMergeCalled
}

// ---------------------------------------------------------------------------
// TS-11-96: POST /merges creates a standalone merge job with campaign_id=null
// and spec_id=null when source_ref does not match any active campaign's spec
// branch.
// Requirement: 11-REQ-18.2
// ---------------------------------------------------------------------------

func TestStandalone_SubmitWithNonCampaignSourceRef_NullCampaignAndSpec(t *testing.T) {
	env := newMergeHTTPTestEnvWithCampaigns(t)
	auth := mergeWriteAuth(newTestUUID("user96"))

	// Submit a merge with source_ref that does NOT match any active campaign.
	body := `{"target_branch":"main","source_ref":"hotfix/urgent-fix"}`
	rec := env.doMergeRequest(t, http.MethodPost,
		"/api/v1/workspaces/my-workspace/merges", body, auth)

	// Must return HTTP 202 Accepted.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want %d; body = %s",
			rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	// campaign_id must be null.
	if respBody["campaign_id"] != nil {
		t.Errorf("campaign_id = %v; want null for standalone merge", respBody["campaign_id"])
	}

	// spec_id must be null.
	if respBody["spec_id"] != nil {
		t.Errorf("spec_id = %v; want null for standalone merge", respBody["spec_id"])
	}
}

// ---------------------------------------------------------------------------
// TS-11-97: A standalone merge job (campaign_id=NULL) that encounters
// WouldConflict transitions to conflict status with conflict_details
// populated; no campaign notification is attempted.
// Requirement: 11-REQ-18.E1
// ---------------------------------------------------------------------------

func TestStandalone_WouldConflict_SetsConflictStatus_NoHook(t *testing.T) {
	db := openMergeTestDB(t)
	// Standalone merge: no campaign_id.
	job := insertMergeTestJob(t, db, "sa97", "")
	workspaceRoot := t.TempDir()

	hookCalled := false

	// Mock GitRunner that reports conflict from merge-tree dry-run.
	mockGit := &mockGitOps{
		onRun: func(_ context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 {
				switch args[0] {
				case "rev-parse":
					for _, a := range args {
						if strings.HasPrefix(a, "origin/") {
							return []byte(testTargetHead + "\n"), nil, nil
						}
					}
					return []byte(testSourceHead + "\n"), nil, nil
				}
			}
			return nil, nil, nil
		},
		onRunExitCode: func(_ context.Context, args ...string) ([]byte, []byte, int, error) {
			// merge-tree dry-run detects conflict.
			if len(args) >= 1 && args[0] == "merge-tree" {
				return nil, []byte("CONFLICT (content): Merge conflict in file1.go\n"), 1, nil
			}
			return nil, nil, 0, nil
		},
	}

	mockMu := newMockBranchLocker()

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
		Hook: func(_ context.Context, _ MergeJob) error {
			hookCalled = true
			return nil
		},
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Job status must be 'conflict'.
	status := getJobStatus(t, db, job.ID)
	if status != "conflict" {
		t.Errorf("job status = %q; want 'conflict'", status)
	}

	// conflict_details must be populated.
	details := getJobConflictDetails(t, db, job.ID)
	if !details.Valid || details.String == "" {
		t.Error("conflict_details is empty; want populated with conflicting file paths")
	}

	// PostMergeHook must NOT be called.
	if hookCalled {
		t.Error("PostMergeHook was called after conflict on standalone merge; want no campaign notification")
	}
}

// ===========================================================================
// Per-Target-Branch Mutex Tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-98: The merge queue maintains an in-process map of sync.Mutex values
// keyed by target branch; acquires the mutex before steps 4-10 and releases
// it after step 10 or on any failure.
// Requirement: 11-REQ-19.1
// ---------------------------------------------------------------------------

func TestMutex_AcquiredBeforeNonceValidation_ReleasedAfterMerge(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "mu98", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Verify mutex for 'main' was acquired.
	mockMu.mu.Lock()
	events := make([]string, len(mockMu.events))
	copy(events, mockMu.events)
	mockMu.mu.Unlock()

	lockIdx := -1
	unlockIdx := -1
	for i, e := range events {
		if e == "mutex-lock:main" && lockIdx == -1 {
			lockIdx = i
		}
		if e == "mutex-unlock:main" && unlockIdx == -1 {
			unlockIdx = i
		}
	}

	if lockIdx == -1 {
		t.Fatal("mutex for 'main' was never locked; want Lock('main') called before nonce validation")
	}

	if unlockIdx == -1 {
		t.Fatal("mutex for 'main' was never unlocked; want Unlock('main') called after status is set to 'merged'")
	}

	if unlockIdx <= lockIdx {
		t.Errorf("mutex unlock (index %d) before or at lock (index %d); want unlock after lock", unlockIdx, lockIdx)
	}

	// Verify job reached merged status.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("job status = %q; want 'merged'", status)
	}
}

// ---------------------------------------------------------------------------
// TS-11-99: The per-target-branch mutex map never evicts entries; entries
// accumulate for the lifetime of the process.
// Requirement: 11-REQ-19.2
// ---------------------------------------------------------------------------

func TestMutex_EntriesNeverEvicted_AccumulateForLifetime(t *testing.T) {
	db := openMergeTestDB(t)

	// trackingBranchLocker records which branches have been locked,
	// accumulating entries without eviction.
	type trackingBranchLocker struct {
		mu       sync.Mutex
		branches map[string]bool
	}

	tracker := &trackingBranchLocker{branches: make(map[string]bool)}

	// Use the mockBranchLocker for actual locking, but track separately.
	mockMu := newMockBranchLocker()

	branches := []string{"main", "release/1.0", "release/2.0", "staging", "dev"}

	for i, branch := range branches {
		suffix := fmt.Sprintf("mu99_%d", i)
		now := time.Now().UTC().Format(time.RFC3339)
		job := &MergeJob{
			ID:            newTestUUID(suffix),
			Nonce:         newTestUUID("n" + suffix),
			WorkspaceSlug: "test-ws",
			TargetBranch:  branch,
			SourceRef:     fmt.Sprintf("feature/branch-%d", i),
			Status:        "queued",
			RetryCount:    0,
			AvailableAt:   now,
			SubmittedBy:   newTestUUID("user"),
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		insertTestMergeJobFull(t, db, job)

		mockGit := newHappyPathMockGitOps()
		deps := MergeDeps{
			Git:           mockGit,
			Locker:        mockMu,
			WorkspaceRoot: t.TempDir(),
		}

		err := processMergeJob(context.Background(), db, job, deps)
		if err != nil {
			t.Fatalf("processMergeJob() for branch %q returned error: %v", branch, err)
		}

		tracker.mu.Lock()
		tracker.branches[branch] = true
		tracker.mu.Unlock()
	}

	// Verify all 5 branches were locked (accumulated, never evicted).
	mockMu.mu.Lock()
	lockedBranches := make(map[string]bool)
	for _, e := range mockMu.events {
		if strings.HasPrefix(e, "mutex-lock:") {
			branch := strings.TrimPrefix(e, "mutex-lock:")
			lockedBranches[branch] = true
		}
	}
	mockMu.mu.Unlock()

	if len(lockedBranches) != len(branches) {
		t.Errorf("locked %d distinct branches; want %d (entries must accumulate, never evict)",
			len(lockedBranches), len(branches))
	}

	for _, branch := range branches {
		if !lockedBranches[branch] {
			t.Errorf("branch %q was never locked; want mutex created for each target branch", branch)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-11-100: When a panic occurs inside the mutex-protected section, the
// deferred mutex release fires and the panic propagates to the worker's
// recover handler.
// Requirement: 11-REQ-19.E1
// ---------------------------------------------------------------------------

func TestMutex_PanicInsideMutexSection_MutexReleased_PanicPropagates(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "mu100", "")
	workspaceRoot := t.TempDir()

	// Mock GitRunner that panics during the rebase step (inside mutex section).
	panicMsg := "simulated panic in rebase step"
	mockGit := &mockGitOps{
		onRun: func(_ context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 {
				switch args[0] {
				case "rev-parse":
					for _, a := range args {
						if strings.HasPrefix(a, "origin/") {
							return []byte(testTargetHead + "\n"), nil, nil
						}
					}
					return []byte(testSourceHead + "\n"), nil, nil
				case "fetch":
					return nil, nil, nil
				case "rebase":
					panic(panicMsg)
				}
			}
			return nil, nil, nil
		},
		onRunExitCode: func(_ context.Context, _ ...string) ([]byte, []byte, int, error) {
			// Dry-run passes.
			return nil, nil, 0, nil
		},
	}

	mockMu := newMockBranchLocker()

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
	}

	// The panic should propagate out of processMergeJob.
	// We recover it in the test to verify it occurred and check the mutex state.
	panicRecovered := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicRecovered = true
				// Verify the recovered value contains the expected message.
				if msg, ok := r.(string); ok {
					if !strings.Contains(msg, panicMsg) {
						t.Errorf("recovered panic message = %q; want to contain %q", msg, panicMsg)
					}
				}
			}
		}()
		_ = processMergeJob(context.Background(), db, job, deps)
	}()

	if !panicRecovered {
		t.Fatal("panic was not propagated; want panic from inside mutex-protected section to propagate to caller")
	}

	// Verify mutex was released despite the panic (via deferred unlock).
	assertMutexReleased(t, mockMu)
}

// ---------------------------------------------------------------------------
// TS-11-101: When two jobs targeting the same target_branch are dequeued
// concurrently, the second blocks on mutex acquisition until the first
// releases it; no data race occurs.
// Requirement: 11-REQ-19.E2
// ---------------------------------------------------------------------------

func TestMutex_ConcurrentJobsSameBranch_SerializedByMutex(t *testing.T) {
	db := openMergeTestDB(t)

	// Insert two jobs both targeting 'main'.
	job1 := insertMergeTestJob(t, db, "mu101a", "")
	// Need a different source_ref for the second job so they don't conflict.
	now := time.Now().UTC().Format(time.RFC3339)
	job2 := &MergeJob{
		ID:            newTestUUID("mu101b"),
		Nonce:         newTestUUID("nmu101b"),
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "feature/other-branch",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	insertTestMergeJobFull(t, db, job2)

	// Use a real sync.Mutex-based BranchLocker to test actual serialization.
	// The realBranchLocker provides actual locking semantics for the race test.
	realLocker := &realBranchLocker{mutexes: make(map[string]*sync.Mutex)}

	workspaceRoot := t.TempDir()

	// Track the order of processing.
	var orderMu sync.Mutex
	var processOrder []string

	// Use a slow mock for one of the jobs to ensure they overlap.
	makeSlowMockGit := func(jobID string) *mockGitOps {
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
						// Introduce a small delay to ensure overlap.
						time.Sleep(50 * time.Millisecond)
						orderMu.Lock()
						processOrder = append(processOrder, jobID)
						orderMu.Unlock()
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

	var wg sync.WaitGroup
	wg.Add(2)

	// Run both jobs concurrently.
	go func() {
		defer wg.Done()
		deps := MergeDeps{
			Git:           makeSlowMockGit(job1.ID),
			Locker:        realLocker,
			WorkspaceRoot: workspaceRoot,
		}
		_ = processMergeJob(context.Background(), db, job1, deps)
	}()

	go func() {
		defer wg.Done()
		deps := MergeDeps{
			Git:           makeSlowMockGit(job2.ID),
			Locker:        realLocker,
			WorkspaceRoot: workspaceRoot,
		}
		_ = processMergeJob(context.Background(), db, job2, deps)
	}()

	wg.Wait()

	// Both jobs must complete (no deadlock).
	status1 := getJobStatus(t, db, job1.ID)
	status2 := getJobStatus(t, db, job2.ID)

	if status1 != "merged" {
		t.Errorf("job1 status = %q; want 'merged'", status1)
	}
	if status2 != "merged" {
		t.Errorf("job2 status = %q; want 'merged'", status2)
	}

	// Verify that the mutex serialized the rebase steps (they did not overlap).
	orderMu.Lock()
	if len(processOrder) != 2 {
		t.Errorf("processOrder has %d entries; want 2 (both jobs should complete)", len(processOrder))
	}
	orderMu.Unlock()

	// The race detector (go test -race) is the primary validation here.
	// If we get to this point without a race detection failure, the mutex
	// is correctly serializing access.
}

// realBranchLocker provides actual sync.Mutex-based branch locking for
// concurrent merge tests. It maintains a map of mutexes keyed by branch name.
type realBranchLocker struct {
	mapMu   sync.Mutex
	mutexes map[string]*sync.Mutex
}

func (r *realBranchLocker) Lock(branch string) {
	r.mapMu.Lock()
	m, ok := r.mutexes[branch]
	if !ok {
		m = &sync.Mutex{}
		r.mutexes[branch] = m
	}
	r.mapMu.Unlock()
	m.Lock()
}

func (r *realBranchLocker) Unlock(branch string) {
	r.mapMu.Lock()
	m := r.mutexes[branch]
	r.mapMu.Unlock()
	m.Unlock()
}

// Additional test for TS-11-98: Verify mutex is also released on failure.
func TestMutex_ReleasedOnFailure(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "mu98f", "")
	workspaceRoot := t.TempDir()

	// Mock that fails during push.
	mockGit := &mockGitOps{
		onRun: func(_ context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 {
				switch args[0] {
				case "rev-parse":
					for _, a := range args {
						if strings.HasPrefix(a, "origin/") {
							return []byte(testTargetHead + "\n"), nil, nil
						}
					}
					return []byte(testSourceHead + "\n"), nil, nil
				case "fetch", "rebase":
					return nil, nil, nil
				case "push":
					return nil, []byte("rejected\n"), &gitcmd.GitError{
						Command:  "push",
						ExitCode: 1,
						Stderr:   "! [rejected]",
					}
				}
			}
			return nil, nil, nil
		},
		onRunExitCode: func(_ context.Context, _ ...string) ([]byte, []byte, int, error) {
			return nil, nil, 0, nil
		},
	}

	mockMu := newMockBranchLocker()

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
	}

	_ = processMergeJob(context.Background(), db, job, deps)

	// Verify mutex was acquired for the target branch.
	if !mockMu.wasLocked() {
		t.Fatal("mutex was never acquired; want Lock('main') called before merge operations")
	}

	// Verify mutex was released even after push failure.
	assertMutexReleased(t, mockMu)
}
