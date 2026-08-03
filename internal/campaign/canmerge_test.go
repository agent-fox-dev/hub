package campaign

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/mergequeue"
)

// TS-12-41: CanMerge implementation rejects any merge queue job for a
// cancelled campaign spec by returning a non-nil CantMergeReason.
func TestCheckCanMerge_RejectsCancelledSpec(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	// Seed a cancelled campaign with a cancelled spec.
	seedCampaign(t, db, "camp-1", "ws-slug", "sprint-42", "main", "cancelled",
		`{"specs":["07"],"edges":[]}`, "user")
	seedCampaignSpec(t, db, "camp-1", "07", "cancelled", "spec/07-secrets-variables", "abc123")

	ctx := context.Background()
	job := mergequeue.MergeJob{
		CampaignID: sql.NullString{String: "camp-1", Valid: true},
		SpecID:     sql.NullString{String: "07", Valid: true},
	}

	reason, err := CheckCanMerge(ctx, db, job)
	if err != nil {
		t.Fatalf("CheckCanMerge() returned error: %v", err)
	}

	if reason == "" {
		t.Fatal("CheckCanMerge() returned empty reason; want non-empty for cancelled spec")
	}

	if !strings.Contains(strings.ToLower(string(reason)), "cancel") {
		t.Errorf("reason = %q; want reason containing 'cancel'", reason)
	}
}

// CheckCanMerge allows merges for active specs (returns empty reason).
func TestCheckCanMerge_AllowsActiveSpec(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedCampaign(t, db, "camp-1", "ws-slug", "sprint-42", "main", "active",
		`{"specs":["07"],"edges":[]}`, "user")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "abc123")

	ctx := context.Background()
	job := mergequeue.MergeJob{
		CampaignID: sql.NullString{String: "camp-1", Valid: true},
		SpecID:     sql.NullString{String: "07", Valid: true},
	}

	reason, err := CheckCanMerge(ctx, db, job)
	if err != nil {
		t.Fatalf("CheckCanMerge() returned error: %v", err)
	}

	if reason != "" {
		t.Errorf("CheckCanMerge() reason = %q; want empty for active spec", reason)
	}
}

// CheckCanMerge rejects merges for blocked specs.
func TestCheckCanMerge_RejectsBlockedSpec(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	seedCampaign(t, db, "camp-1", "ws-slug", "sprint-42", "main", "active",
		`{"specs":["07"],"edges":[]}`, "user")
	seedCampaignSpecFull(t, db, "camp-1", "07", "blocked",
		"spec/07-secrets-variables", "abc123",
		`["conflict.go"]`, "merge-uuid")

	ctx := context.Background()
	job := mergequeue.MergeJob{
		CampaignID: sql.NullString{String: "camp-1", Valid: true},
		SpecID:     sql.NullString{String: "07", Valid: true},
	}

	reason, err := CheckCanMerge(ctx, db, job)
	if err != nil {
		t.Fatalf("CheckCanMerge() returned error: %v", err)
	}

	if reason == "" {
		t.Fatal("CheckCanMerge() returned empty reason; want non-empty for blocked spec")
	}
}

// CheckCanMerge allows merges for jobs without a campaign (non-campaign merges).
func TestCheckCanMerge_AllowsNonCampaignJob(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	ctx := context.Background()
	job := mergequeue.MergeJob{
		CampaignID: sql.NullString{Valid: false},
		SpecID:     sql.NullString{Valid: false},
	}

	reason, err := CheckCanMerge(ctx, db, job)
	if err != nil {
		t.Fatalf("CheckCanMerge() returned error: %v", err)
	}

	if reason != "" {
		t.Errorf("CheckCanMerge() reason = %q; want empty for non-campaign job", reason)
	}
}
