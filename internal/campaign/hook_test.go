package campaign

import (
	"context"
	"database/sql"
	"testing"

	"github.com/agent-fox-dev/hub/internal/mergequeue"
)

// TS-12-19: PostMergeHook with DeadLetter=false acquires the per-campaign
// mutex, performs cascading rebase in topological order, advances the DAG
// frontier, checks completion, and releases the mutex.
//
// Requirement: 12-REQ-6.1
func TestPostMergeHook_SuccessfulMerge_CascadesRebaseAndAdvancesFrontier(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Campaign with DAG: 07 -> 09 (09 depends on 07).
	dagJSON := `{"specs":["07","09"],"edges":[{"from":"07","to":"09","relationship":"depends_on"}]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "09", "active", "spec/09-other", "bbb2222222222222222222222222222222222222")

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

	// Verify the merged spec 07 is now status=merged.
	spec07, err := store.GetCampaignSpec(ctx, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec(07) error: %v", err)
	}
	if spec07 == nil {
		t.Fatal("GetCampaignSpec(07) returned nil")
	}
	if spec07.Status != "merged" {
		t.Errorf("spec 07 status = %q; want %q", spec07.Status, "merged")
	}

	// Verify the sibling spec 09 was rebased (branch_sha should change).
	spec09, err := store.GetCampaignSpec(ctx, "camp-1", "09")
	if err != nil {
		t.Fatalf("GetCampaignSpec(09) error: %v", err)
	}
	if spec09 == nil {
		t.Fatal("GetCampaignSpec(09) returned nil")
	}
	if spec09.BranchSHA == "bbb2222222222222222222222222222222222222" {
		t.Error("spec 09 branch_sha unchanged after rebase; want different SHA")
	}
}

// TS-12-19 (continued): Verify that newly-unblocked specs have branches
// created and status set to active after the frontier is advanced.
func TestPostMergeHook_SuccessfulMerge_AdvancesFrontier(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Campaign with DAG: 07 -> 09 where 09 is pending (not yet started).
	dagJSON := `{"specs":["07","09"],"edges":[{"from":"07","to":"09","relationship":"depends_on"}]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "09", "pending", "", "")

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

	// Verify spec 09 was activated (frontier advanced after 07 merged).
	spec09, err := store.GetCampaignSpec(ctx, "camp-1", "09")
	if err != nil {
		t.Fatalf("GetCampaignSpec(09) error: %v", err)
	}
	if spec09 == nil {
		t.Fatal("GetCampaignSpec(09) returned nil")
	}
	if spec09.Status != "active" {
		t.Errorf("spec 09 status = %q; want %q (frontier should advance)", spec09.Status, "active")
	}
	if spec09.BranchName == "" {
		t.Error("spec 09 branch_name is empty; want branch created for frontier spec")
	}
}

// TS-12-20: PostMergeHook is a no-op and returns immediately when invoked
// for a MergeJob with campaign_id equal to NULL.
//
// Requirement: 12-REQ-6.2
func TestPostMergeHook_NullCampaignID_NoOp(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	store := NewStore(db)
	scheduler := NewScheduler(store)

	// MergeJob with NULL campaign_id (standalone merge, not part of a campaign).
	job := mergequeue.MergeJob{
		ID:            "job-1",
		CampaignID:    sql.NullString{Valid: false},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "ws",
		TargetBranch:  "main",
		Status:        "merged",
	}

	err := scheduler.HandlePostMerge(ctx, job)
	if err != nil {
		t.Fatalf("HandlePostMerge with NULL campaign_id returned error: %v", err)
	}

	// Verify no campaigns were created or modified.
	campaigns, err := store.ListCampaigns(ctx, "ws", "")
	if err != nil {
		t.Fatalf("ListCampaigns error: %v", err)
	}
	if len(campaigns) != 0 {
		t.Errorf("expected no campaigns after no-op hook; got %d", len(campaigns))
	}
}

// TS-12-21: Campaign scheduler sets campaign status to completed when the
// last spec's merge is processed and all specs have status merged.
//
// Requirement: 12-REQ-6.3
func TestPostMergeHook_LastSpecMerged_CampaignCompleted(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Campaign with two specs: 08 already merged, 07 being merged now.
	dagJSON := `{"specs":["07","08"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "08", "merged", "spec/08-other", "bbb2222222222222222222222222222222222222")

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

	// Campaign should be completed since all specs are now merged.
	campaign, err := store.GetCampaign(ctx, "camp-1")
	if err != nil {
		t.Fatalf("GetCampaign error: %v", err)
	}
	if campaign == nil {
		t.Fatal("GetCampaign returned nil")
	}
	if campaign.Status != "completed" {
		t.Errorf("campaign status = %q; want %q", campaign.Status, "completed")
	}
}

// TS-12-21 (edge case): Verify that campaign is not set to completed when
// some specs are still active/pending.
func TestPostMergeHook_NotAllSpecsMerged_CampaignStaysActive(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Campaign with three specs: 07 being merged, 08 active, 09 pending.
	dagJSON := `{"specs":["07","08","09"],"edges":[{"from":"07","to":"09","relationship":"depends_on"}]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "08", "active", "spec/08-other", "bbb2222222222222222222222222222222222222")
	seedCampaignSpec(t, db, "camp-1", "09", "pending", "", "")

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

	// Campaign should remain active (not all specs merged yet).
	campaign, err := store.GetCampaign(ctx, "camp-1")
	if err != nil {
		t.Fatalf("GetCampaign error: %v", err)
	}
	if campaign == nil {
		t.Fatal("GetCampaign returned nil")
	}
	if campaign.Status != "active" {
		t.Errorf("campaign status = %q; want %q (not all specs merged)", campaign.Status, "active")
	}
}

// Edge case 12-REQ-6.E3: PostMergeHook invoked for a campaign already in
// failed status should skip all state mutations and return immediately.
func TestPostMergeHook_FailedCampaign_SkipsMutations(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07","08"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "failed", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "08", "failed", "spec/08-other", "bbb2222222222222222222222222222222222222")

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

	// Spec 07 should remain in its original status (no mutations on failed campaign).
	spec07, err := store.GetCampaignSpec(ctx, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec(07) error: %v", err)
	}
	if spec07 == nil {
		t.Fatal("GetCampaignSpec(07) returned nil")
	}
	if spec07.Status != "active" {
		t.Errorf("spec 07 status = %q; want %q (no mutations on failed campaign)", spec07.Status, "active")
	}

	// No rebase should have been performed.
	if len(gitOps.rebaseCalls) != 0 {
		t.Errorf("expected no rebase calls on failed campaign; got %d", len(gitOps.rebaseCalls))
	}
}

// Edge case 12-REQ-6.E3: PostMergeHook invoked for a cancelled campaign
// should skip all state mutations and return immediately.
func TestPostMergeHook_CancelledCampaign_SkipsMutations(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "cancelled", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "cancelled", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")

	store := NewStore(db)
	gitOps := newMockGitOps()
	scheduler := NewScheduler(store)
	scheduler.gitOps = gitOps

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

	// Verify campaign status was not changed.
	campaign, err := store.GetCampaign(ctx, "camp-1")
	if err != nil {
		t.Fatalf("GetCampaign error: %v", err)
	}
	if campaign == nil {
		t.Fatal("GetCampaign returned nil")
	}
	if campaign.Status != "cancelled" {
		t.Errorf("campaign status = %q; want %q (skipped)", campaign.Status, "cancelled")
	}
}
