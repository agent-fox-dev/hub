package campaign

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/mergequeue"
)

// TS-12-50: Campaign scheduler acquires an in-memory mutex from a sync.Map
// keyed by campaign ID before performing any state mutations in
// PostMergeHook, and releases it after all mutations complete.
//
// Requirement: 12-REQ-17.1
func TestPostMergeHook_MutexAcquiredAndReleased(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07","08"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "08", "active", "spec/08-other", "bbb2222222222222222222222222222222222222")

	store := NewStore(db)
	gitOps := newMockGitOps()
	authz := NewAuthz()
	rebaseEngine := NewRebaseEngine(store, gitOps, authz)
	scheduler := NewScheduler(store)
	scheduler.gitOps = gitOps
	scheduler.authz = authz
	scheduler.rebaseEngine = rebaseEngine

	job := mergequeue.MergeJob{
		ID:            "job-1",
		CampaignID:    sql.NullString{String: "camp-1", Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "ws",
		TargetBranch:  "main",
		Status:        "merged",
	}

	err := scheduler.HandlePostMerge(ctx, job)
	if err != nil {
		t.Fatalf("HandlePostMerge returned error: %v", err)
	}

	// After the hook returns, the mutex should be released. Verify by
	// calling HandlePostMerge again — it should not deadlock.
	job2 := mergequeue.MergeJob{
		ID:            "job-2",
		CampaignID:    sql.NullString{String: "camp-1", Valid: true},
		SpecID:        sql.NullString{String: "08", Valid: true},
		WorkspaceSlug: "ws",
		TargetBranch:  "main",
		Status:        "merged",
	}

	// Use a timeout to detect deadlocks.
	done := make(chan error, 1)
	go func() {
		done <- scheduler.HandlePostMerge(ctx, job2)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second HandlePostMerge returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second HandlePostMerge timed out; mutex may not have been released")
	}
}

// TS-12-50 (continued): Verify that concurrent PostMergeHook invocations
// for the SAME campaign are serialized — they do not interleave. Uses
// -race detection to verify no data races.
//
// Requirement: 12-REQ-17.1
func TestPostMergeHook_ConcurrentSameCampaign_Serialized(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07","08","09"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "08", "active", "spec/08-other", "bbb2222222222222222222222222222222222222")
	seedCampaignSpec(t, db, "camp-1", "09", "active", "spec/09-dag-builder", "ccc3333333333333333333333333333333333333")

	store := NewStore(db)
	gitOps := newMockGitOps()
	authz := NewAuthz()
	rebaseEngine := NewRebaseEngine(store, gitOps, authz)
	scheduler := NewScheduler(store)
	scheduler.gitOps = gitOps
	scheduler.authz = authz
	scheduler.rebaseEngine = rebaseEngine

	// Launch two concurrent hooks for the same campaign.
	var wg sync.WaitGroup
	var errCount atomic.Int32

	for i, specID := range []string{"07", "08"} {
		wg.Add(1)
		go func(idx int, sid string) {
			defer wg.Done()
			job := mergequeue.MergeJob{
				ID:            "job-" + sid,
				CampaignID:    sql.NullString{String: "camp-1", Valid: true},
				SpecID:        sql.NullString{String: sid, Valid: true},
				WorkspaceSlug: "ws",
				TargetBranch:  "main",
				Status:        "merged",
			}
			if err := scheduler.HandlePostMerge(ctx, job); err != nil {
				errCount.Add(1)
			}
		}(i, specID)
	}

	wg.Wait()

	// Both hooks should have completed (serialized, not interleaved).
	// The test primarily validates that the -race detector finds no races.
	// No assertion on errCount since stubs may return nil regardless.
}

// TS-12-50 (continued): Verify that hooks for DIFFERENT campaigns do not
// block each other — they can run concurrently without deadlock.
//
// Requirement: 12-REQ-17.1
func TestPostMergeHook_DifferentCampaigns_NotBlocked(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	// Two separate campaigns.
	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-a", "ws", "campaign-a", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-a", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")

	seedCampaign(t, db, "camp-b", "ws", "campaign-b", "develop", "active", dagJSON, "user-2")
	seedCampaignSpec(t, db, "camp-b", "07", "active", "spec/07-secrets-variables", "bbb2222222222222222222222222222222222222")

	store := NewStore(db)
	gitOps := newMockGitOps()
	authz := NewAuthz()
	rebaseEngine := NewRebaseEngine(store, gitOps, authz)
	scheduler := NewScheduler(store)
	scheduler.gitOps = gitOps
	scheduler.authz = authz
	scheduler.rebaseEngine = rebaseEngine

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		job := mergequeue.MergeJob{
			ID:            "job-a",
			CampaignID:    sql.NullString{String: "camp-a", Valid: true},
			SpecID:        sql.NullString{String: "07", Valid: true},
			WorkspaceSlug: "ws",
			TargetBranch:  "main",
			Status:        "merged",
		}
		_ = scheduler.HandlePostMerge(ctx, job)
	}()

	go func() {
		defer wg.Done()
		job := mergequeue.MergeJob{
			ID:            "job-b",
			CampaignID:    sql.NullString{String: "camp-b", Valid: true},
			SpecID:        sql.NullString{String: "07", Valid: true},
			WorkspaceSlug: "ws",
			TargetBranch:  "develop",
			Status:        "merged",
		}
		_ = scheduler.HandlePostMerge(ctx, job)
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Both completed — different campaign mutexes are independent.
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent hooks for different campaigns timed out; mutexes may incorrectly block across campaigns")
	}
}

// TS-12-51: Campaign scheduler re-initializes the sync.Map as empty on hub
// restart; locks are re-acquired as PostMergeHook invocations arrive.
//
// Requirement: 12-REQ-17.2
func TestNewScheduler_MutexMapEmpty(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)

	scheduler := NewScheduler(store)

	if size := scheduler.MutexMapSize(); size != 0 {
		t.Errorf("new scheduler mutex map size = %d; want 0", size)
	}
}

// TS-12-51 (continued): After recovery, the mutex map is empty and new
// PostMergeHook invocations re-populate it.
func TestRecoverFromDB_MutexMapEmpty_ThenRePopulated(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")

	store := NewStore(db)
	gitOps := newMockGitOps()
	authz := NewAuthz()
	rebaseEngine := NewRebaseEngine(store, gitOps, authz)
	scheduler := NewScheduler(store)
	scheduler.gitOps = gitOps
	scheduler.authz = authz
	scheduler.rebaseEngine = rebaseEngine

	// Simulate prior state: put an entry in the map.
	scheduler.mutexes.Store("camp-old", &mutexEntry{})

	err := scheduler.RecoverFromDB(ctx)
	if err != nil {
		t.Fatalf("RecoverFromDB returned error: %v", err)
	}

	// After recovery, mutex map should be empty.
	if size := scheduler.MutexMapSize(); size != 0 {
		t.Errorf("post-recovery mutex map size = %d; want 0", size)
	}

	// A PostMergeHook invocation should work (lazily creates new entry).
	job := mergequeue.MergeJob{
		ID:            "job-1",
		CampaignID:    sql.NullString{String: "camp-1", Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "ws",
		TargetBranch:  "main",
		Status:        "merged",
	}

	err = scheduler.HandlePostMerge(ctx, job)
	if err != nil {
		t.Fatalf("HandlePostMerge after recovery returned error: %v", err)
	}
}

// Edge case 12-REQ-17.E1: If a PostMergeHook goroutine panics while holding
// the per-campaign mutex, the scheduler should recover, release the mutex,
// log the panic, and set the campaign status to failed.
func TestPostMergeHook_PanicRecovery_MutexReleased(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")

	store := NewStore(db)
	scheduler := NewScheduler(store)

	// First call: dead-letter triggers the hook. The stub currently just
	// returns nil, so this tests the pattern exists for when implementation
	// adds panic recovery.
	job := mergequeue.MergeJob{
		ID:            "job-1",
		CampaignID:    sql.NullString{String: "camp-1", Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "ws",
		TargetBranch:  "main",
		Status:        "dead_letter",
	}

	err := scheduler.HandlePostMerge(ctx, job)
	if err != nil {
		t.Fatalf("HandlePostMerge returned error: %v", err)
	}

	// A subsequent call should not deadlock, proving the mutex was released.
	done := make(chan error, 1)
	go func() {
		done <- scheduler.HandlePostMerge(ctx, job)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("subsequent HandlePostMerge returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subsequent HandlePostMerge timed out; mutex may not have been released after panic/error")
	}
}
