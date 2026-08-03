package campaign

import (
	"context"
	"testing"
)

// TS-12-42: Campaign scheduler re-evaluates all active campaigns by
// recomputing the frontier from the DAG and current campaign_specs status
// rows in the DB on hub restart, and re-initializes the per-campaign mutex
// map as empty.
//
// Requirement: 12-REQ-14.1
func TestRecoverFromDB_RecomputesFrontierForActiveCampaigns(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Campaign with DAG: 07 -> 09 (09 depends on 07).
	// Spec 07 is already merged, so spec 09 should be in the frontier.
	dagJSON := `{"specs":["07","09"],"edges":[{"from":"07","to":"09","relationship":"depends_on"}]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "merged", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "09", "pending", "", "")

	store := NewStore(db)
	gitOps := newMockGitOps()
	authz := NewAuthz()
	rebaseEngine := NewRebaseEngine(store, gitOps, authz)
	scheduler := NewScheduler(store)
	scheduler.gitOps = gitOps
	scheduler.authz = authz
	scheduler.rebaseEngine = rebaseEngine

	err := scheduler.RecoverFromDB(ctx)
	if err != nil {
		t.Fatalf("RecoverFromDB returned error: %v", err)
	}

	// After recovery, the frontier should include spec 09 (its dependency
	// 07 is merged).
	frontier := scheduler.GetFrontier("camp-1")
	found := false
	for _, specID := range frontier {
		if specID == "09" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("frontier = %v; want to include %q (upstream 07 is merged)", frontier, "09")
	}
}

// TS-12-42 (continued): Verify that the per-campaign mutex map is
// re-initialized as empty on restart.
func TestRecoverFromDB_MutexMapIsEmpty(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")

	store := NewStore(db)
	scheduler := NewScheduler(store)

	// Simulate a prior run: store a mutex entry before recovery.
	scheduler.mutexes.Store("camp-1", &mutexEntry{})

	if scheduler.MutexMapSize() != 1 {
		t.Fatalf("pre-recovery mutex map size = %d; want 1", scheduler.MutexMapSize())
	}

	err := scheduler.RecoverFromDB(ctx)
	if err != nil {
		t.Fatalf("RecoverFromDB returned error: %v", err)
	}

	if scheduler.MutexMapSize() != 0 {
		t.Errorf("post-recovery mutex map size = %d; want 0", scheduler.MutexMapSize())
	}
}

// TS-12-42 (multi-campaign): Verify that recovery handles multiple active
// campaigns independently, each getting its own frontier.
func TestRecoverFromDB_MultipleCampaigns_IndependentFrontiers(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Campaign A: 07 merged -> 09 pending
	dagA := `{"specs":["07","09"],"edges":[{"from":"07","to":"09","relationship":"depends_on"}]}`
	seedCampaign(t, db, "camp-a", "ws", "campaign-a", "main", "active", dagA, "user-1")
	seedCampaignSpec(t, db, "camp-a", "07", "merged", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-a", "09", "pending", "", "")

	// Campaign B: all specs still active (no frontier change needed)
	dagB := `{"specs":["10","11"],"edges":[{"from":"10","to":"11","relationship":"depends_on"}]}`
	seedCampaign(t, db, "camp-b", "ws", "campaign-b", "develop", "active", dagB, "user-1")
	seedCampaignSpec(t, db, "camp-b", "10", "active", "spec/10-gitcmd", "bbb2222222222222222222222222222222222222")
	seedCampaignSpec(t, db, "camp-b", "11", "pending", "", "")

	store := NewStore(db)
	gitOps := newMockGitOps()
	authz := NewAuthz()
	rebaseEngine := NewRebaseEngine(store, gitOps, authz)
	scheduler := NewScheduler(store)
	scheduler.gitOps = gitOps
	scheduler.authz = authz
	scheduler.rebaseEngine = rebaseEngine

	err := scheduler.RecoverFromDB(ctx)
	if err != nil {
		t.Fatalf("RecoverFromDB returned error: %v", err)
	}

	// Campaign A: 09 should be in frontier (07 is merged).
	frontierA := scheduler.GetFrontier("camp-a")
	found09 := false
	for _, id := range frontierA {
		if id == "09" {
			found09 = true
		}
	}
	if !found09 {
		t.Errorf("campaign-a frontier = %v; want to include %q", frontierA, "09")
	}

	// Campaign B: 11 should NOT be in frontier (10 is not merged).
	frontierB := scheduler.GetFrontier("camp-b")
	for _, id := range frontierB {
		if id == "11" {
			t.Errorf("campaign-b frontier includes %q but 10 is still active", id)
		}
	}
}

// TS-12-43: Campaign scheduler assumes an agent is still working on a spec
// with status=active after hub restart and takes no action, leaving the
// spec active.
//
// Requirement: 12-REQ-14.2
func TestRecoverFromDB_ActiveSpecRemainsActive(t *testing.T) {
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

	err := scheduler.RecoverFromDB(ctx)
	if err != nil {
		t.Fatalf("RecoverFromDB returned error: %v", err)
	}

	// Spec 07 should still be active — recovery does not reset it.
	spec07, err := store.GetCampaignSpec(ctx, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec(07) error: %v", err)
	}
	if spec07 == nil {
		t.Fatal("GetCampaignSpec(07) returned nil")
	}
	if spec07.Status != "active" {
		t.Errorf("spec 07 status = %q; want %q (unchanged after recovery)", spec07.Status, "active")
	}
}

// TS-12-44: Campaign scheduler retains blocked state for a spec with
// status=blocked after hub restart; the agent must call the resolve
// endpoint to unblock.
//
// Requirement: 12-REQ-14.3
func TestRecoverFromDB_BlockedSpecRetainsBlockedState(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpecFull(t, db, "camp-1", "07", "blocked", "spec/07-secrets-variables",
		"aaa1111111111111111111111111111111111111", `["file1.go"]`, "merge-uuid-1")

	store := NewStore(db)
	gitOps := newMockGitOps()
	authz := NewAuthz()
	rebaseEngine := NewRebaseEngine(store, gitOps, authz)
	scheduler := NewScheduler(store)
	scheduler.gitOps = gitOps
	scheduler.authz = authz
	scheduler.rebaseEngine = rebaseEngine

	err := scheduler.RecoverFromDB(ctx)
	if err != nil {
		t.Fatalf("RecoverFromDB returned error: %v", err)
	}

	// Spec 07 should remain blocked with conflict details preserved.
	spec07, err := store.GetCampaignSpec(ctx, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec(07) error: %v", err)
	}
	if spec07 == nil {
		t.Fatal("GetCampaignSpec(07) returned nil")
	}
	if spec07.Status != "blocked" {
		t.Errorf("spec 07 status = %q; want %q (retained after recovery)", spec07.Status, "blocked")
	}
	if len(spec07.ConflictDetails) == 0 {
		t.Error("spec 07 conflict_details is empty; want preserved after recovery")
	}
}

// TS-12-44 (continued): Verify that conflict_details content is preserved
// exactly after recovery.
func TestRecoverFromDB_BlockedSpec_ConflictDetailsPreserved(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpecFull(t, db, "camp-1", "07", "blocked", "spec/07-secrets-variables",
		"aaa1111111111111111111111111111111111111", `["file1.go","dir/file2.go"]`, "merge-uuid-1")

	store := NewStore(db)
	gitOps := newMockGitOps()
	authz := NewAuthz()
	rebaseEngine := NewRebaseEngine(store, gitOps, authz)
	scheduler := NewScheduler(store)
	scheduler.gitOps = gitOps
	scheduler.authz = authz
	scheduler.rebaseEngine = rebaseEngine

	err := scheduler.RecoverFromDB(ctx)
	if err != nil {
		t.Fatalf("RecoverFromDB returned error: %v", err)
	}

	spec07, err := store.GetCampaignSpec(ctx, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec(07) error: %v", err)
	}
	if spec07 == nil {
		t.Fatal("GetCampaignSpec(07) returned nil")
	}

	// Verify exact conflict details are preserved.
	expected := []string{"file1.go", "dir/file2.go"}
	if len(spec07.ConflictDetails) != len(expected) {
		t.Fatalf("conflict_details length = %d; want %d", len(spec07.ConflictDetails), len(expected))
	}
	for i, path := range expected {
		if spec07.ConflictDetails[i] != path {
			t.Errorf("conflict_details[%d] = %q; want %q", i, spec07.ConflictDetails[i], path)
		}
	}
}

// Edge case 12-REQ-14.E1: If the DB is unavailable during hub restart,
// RecoverFromDB should return an error rather than starting with incomplete state.
func TestRecoverFromDB_DBUnavailable_ReturnsError(t *testing.T) {
	db := openTestDB(t)

	// Close the DB to simulate unavailability.
	db.Close()

	store := NewStore(db)
	scheduler := NewScheduler(store)

	err := scheduler.RecoverFromDB(context.Background())
	if err == nil {
		t.Error("RecoverFromDB with closed DB returned nil error; want non-nil error")
	}
}

// Recovery should skip non-active campaigns (completed, failed, cancelled).
func TestRecoverFromDB_SkipsNonActiveCampaigns(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// A completed campaign — should not appear in any frontier.
	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-done", "ws", "done-campaign", "main", "completed", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-done", "07", "merged", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")

	store := NewStore(db)
	scheduler := NewScheduler(store)

	err := scheduler.RecoverFromDB(ctx)
	if err != nil {
		t.Fatalf("RecoverFromDB returned error: %v", err)
	}

	frontier := scheduler.GetFrontier("camp-done")
	if len(frontier) != 0 {
		t.Errorf("frontier for completed campaign = %v; want empty", frontier)
	}
}
