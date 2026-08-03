package campaign

import (
	"context"
	"testing"
)

// TS-12-3: Campaign scheduler transitions campaign status to completed when
// all specs in the campaign reach merged status.
func TestScheduler_CheckCompletion_AllSpecsMerged(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	store := NewStore(db)
	scheduler := NewScheduler(store)
	ctx := context.Background()

	// Seed a campaign with two active specs.
	seedCampaign(t, db, "camp-1", "ws", "test-completion", "main", "active",
		`{"specs":["07","08"],"edges":[]}`, "user")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "abc123")
	seedCampaignSpec(t, db, "camp-1", "08", "active", "spec/08-other-feature", "def456")

	// Mark both specs as merged.
	if err := store.UpdateSpecStatus(ctx, "camp-1", "07", "merged"); err != nil {
		t.Fatalf("UpdateSpecStatus('07', 'merged') failed: %v", err)
	}
	if err := store.UpdateSpecStatus(ctx, "camp-1", "08", "merged"); err != nil {
		t.Fatalf("UpdateSpecStatus('08', 'merged') failed: %v", err)
	}

	// Check completion should transition campaign to completed.
	if err := scheduler.CheckCompletion(ctx, "camp-1"); err != nil {
		t.Fatalf("CheckCompletion() failed: %v", err)
	}

	// Verify campaign status is now completed.
	campaign, err := store.GetCampaign(ctx, "camp-1")
	if err != nil {
		t.Fatalf("GetCampaign() failed: %v", err)
	}
	if campaign == nil {
		t.Fatal("GetCampaign() returned nil campaign")
	}
	if campaign.Status != "completed" {
		t.Errorf("campaign status = %q; want %q", campaign.Status, "completed")
	}
}

// TS-12-4: Campaign scheduler immediately transitions campaign status to failed
// when any single spec reaches terminal failed status.
func TestScheduler_PropagateSpecFailure_CampaignFails(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	store := NewStore(db)
	scheduler := NewScheduler(store)
	ctx := context.Background()

	// Seed a campaign with two active specs.
	seedCampaign(t, db, "camp-1", "ws", "test-failure", "main", "active",
		`{"specs":["07","08"],"edges":[]}`, "user")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "abc123")
	seedCampaignSpec(t, db, "camp-1", "08", "active", "spec/08-other-feature", "def456")

	// Propagate failure for spec 07.
	if err := scheduler.PropagateSpecFailure(ctx, "camp-1", "07"); err != nil {
		t.Fatalf("PropagateSpecFailure() failed: %v", err)
	}

	// Verify campaign status is now failed.
	campaign, err := store.GetCampaign(ctx, "camp-1")
	if err != nil {
		t.Fatalf("GetCampaign() failed: %v", err)
	}
	if campaign == nil {
		t.Fatal("GetCampaign() returned nil campaign")
	}
	if campaign.Status != "failed" {
		t.Errorf("campaign status = %q; want %q", campaign.Status, "failed")
	}

	// Verify the failed spec has status failed.
	specs, err := store.GetCampaignSpecs(ctx, "camp-1")
	if err != nil {
		t.Fatalf("GetCampaignSpecs() failed: %v", err)
	}
	var spec07Found bool
	for _, s := range specs {
		if s.SpecID == "07" {
			spec07Found = true
			if s.Status != "failed" {
				t.Errorf("spec 07 status = %q; want %q", s.Status, "failed")
			}
		}
	}
	if !spec07Found {
		t.Error("spec 07 not found in campaign_specs")
	}
}

// TS-12-1.E2: Invalid state transitions are rejected.
func TestStore_InvalidStatusTransition(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Seed a completed campaign.
	seedCampaign(t, db, "camp-1", "ws", "test-transition", "main", "completed",
		`{"specs":["07"],"edges":[]}`, "user")

	// Attempt to transition completed → active (invalid).
	err := store.UpdateCampaignStatus(ctx, "camp-1", "active")
	if err == nil {
		t.Fatal("expected error transitioning completed → active, got nil")
	}
}
