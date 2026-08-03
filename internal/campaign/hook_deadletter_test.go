package campaign

import (
	"context"
	"database/sql"
	"testing"

	"github.com/agent-fox-dev/hub/internal/mergequeue"
)

// TS-12-22: PostMergeHook with DeadLetter=true sets spec status to failed,
// immediately sets campaign status to failed, releases the mutex, and returns
// without performing any rebase or frontier advancement.
//
// Requirement: 12-REQ-7.1
func TestPostMergeHook_DeadLetter_SetsSpecAndCampaignFailed(t *testing.T) {
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

	// Dead-letter job: the merge exhausted all retries.
	job := mergequeue.MergeJob{
		ID:            "job-dead",
		CampaignID:    sql.NullString{String: "camp-1", Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "ws",
		TargetBranch:  "main",
		Status:        "dead_letter",
	}

	err := scheduler.HandlePostMerge(ctx, job)
	if err != nil {
		t.Fatalf("HandlePostMerge (dead_letter) returned error: %v", err)
	}

	// Spec 07 should be failed.
	spec07, err := store.GetCampaignSpec(ctx, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec(07) error: %v", err)
	}
	if spec07 == nil {
		t.Fatal("GetCampaignSpec(07) returned nil")
	}
	if spec07.Status != "failed" {
		t.Errorf("spec 07 status = %q; want %q", spec07.Status, "failed")
	}

	// Campaign should be immediately failed.
	campaign, err := store.GetCampaign(ctx, "camp-1")
	if err != nil {
		t.Fatalf("GetCampaign error: %v", err)
	}
	if campaign == nil {
		t.Fatal("GetCampaign returned nil")
	}
	if campaign.Status != "failed" {
		t.Errorf("campaign status = %q; want %q", campaign.Status, "failed")
	}

	// No rebase should have been performed for dead-letter.
	if len(gitOps.rebaseCalls) != 0 {
		t.Errorf("expected 0 rebase calls for dead-letter; got %d", len(gitOps.rebaseCalls))
	}
}

// TS-12-22 (continued): Verify that the per-campaign mutex is released
// after dead-letter handling so subsequent hooks can proceed.
func TestPostMergeHook_DeadLetter_ReleasesMutex(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")

	store := NewStore(db)
	scheduler := NewScheduler(store)

	// First: dead-letter hook.
	job := mergequeue.MergeJob{
		ID:            "job-dead",
		CampaignID:    sql.NullString{String: "camp-1", Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "ws",
		TargetBranch:  "main",
		Status:        "dead_letter",
	}

	err := scheduler.HandlePostMerge(ctx, job)
	if err != nil {
		t.Fatalf("first HandlePostMerge returned error: %v", err)
	}

	// Second call should not deadlock (mutex was released).
	err = scheduler.HandlePostMerge(ctx, job)
	if err != nil {
		t.Fatalf("second HandlePostMerge returned error (mutex may not have been released): %v", err)
	}
}

// Edge case 12-REQ-7.E1: If the spec referenced in the dead-letter MergeJob
// is not found in campaign_specs, the campaign should still be set to failed.
func TestPostMergeHook_DeadLetter_SpecNotFound_CampaignStillFails(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	// Note: no campaign_spec seeded for spec "99" — the dead-letter references
	// a spec that doesn't exist in campaign_specs.

	store := NewStore(db)
	scheduler := NewScheduler(store)

	job := mergequeue.MergeJob{
		ID:            "job-dead",
		CampaignID:    sql.NullString{String: "camp-1", Valid: true},
		SpecID:        sql.NullString{String: "99", Valid: true},
		WorkspaceSlug: "ws",
		TargetBranch:  "main",
		Status:        "dead_letter",
	}

	// Should not panic or return a fatal error — the anomaly is logged.
	_ = scheduler.HandlePostMerge(ctx, job)

	// Campaign should be failed as a safety measure.
	campaign, err := store.GetCampaign(ctx, "camp-1")
	if err != nil {
		t.Fatalf("GetCampaign error: %v", err)
	}
	if campaign == nil {
		t.Fatal("GetCampaign returned nil")
	}
	if campaign.Status != "failed" {
		t.Errorf("campaign status = %q; want %q (safety failure)", campaign.Status, "failed")
	}
}
