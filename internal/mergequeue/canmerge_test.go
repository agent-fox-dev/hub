package mergequeue

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// TS-11-11: CanMerge returns (true, empty string, nil) when all prerequisites
// for a campaign merge are satisfied.
// Requirement: 11-REQ-3.1
// ---------------------------------------------------------------------------

func TestCanMerge_AllPrerequisitesSatisfied(t *testing.T) {
	db := openCanMergeTestDB(t)

	campaignID := newTestUUID("camp1")
	now := time.Now().UTC().Format(time.RFC3339)

	// Campaign with DAG: spec 07 depends on spec 06.
	insertTestCampaign(t, db, campaignID, "test-ws", "main",
		`{"specs":["06","07"],"edges":[{"from":"06","to":"07","relationship":"depends_on"}]}`,
		now)

	// Upstream spec 06 is already merged.
	insertTestCampaignSpec(t, db, campaignID, "06", "merged", "sha-upstream-06", now)
	// Current spec 07 is active with branch commits present.
	insertTestCampaignSpec(t, db, campaignID, "07", "active", "sha-current-07", now)

	job := MergeJob{
		ID:            newTestUUID("job1"),
		CampaignID:    sql.NullString{String: campaignID, Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets-variables",
	}

	canMerge, reason, err := CanMerge(context.Background(), db, job)
	if err != nil {
		t.Fatalf("CanMerge() returned error: %v", err)
	}
	if !canMerge {
		t.Errorf("CanMerge() = false; want true")
	}
	if reason != "" {
		t.Errorf("reason = %q; want empty string", reason)
	}
}

// ---------------------------------------------------------------------------
// TS-11-12: CanMerge returns (false, BeforeDependency, nil) when an upstream
// spec has not yet been merged.
// Requirement: 11-REQ-3.2
// ---------------------------------------------------------------------------

func TestCanMerge_BeforeDependency(t *testing.T) {
	db := openCanMergeTestDB(t)

	campaignID := newTestUUID("camp2")
	now := time.Now().UTC().Format(time.RFC3339)

	// Campaign with DAG: spec 07 depends on spec 06.
	insertTestCampaign(t, db, campaignID, "test-ws", "main",
		`{"specs":["06","07"],"edges":[{"from":"06","to":"07","relationship":"depends_on"}]}`,
		now)

	// Upstream spec 06 is NOT merged — still active.
	insertTestCampaignSpec(t, db, campaignID, "06", "active", "sha-upstream-06", now)
	// Current spec 07 is active with branch commits present.
	insertTestCampaignSpec(t, db, campaignID, "07", "active", "sha-current-07", now)

	job := MergeJob{
		ID:            newTestUUID("job2"),
		CampaignID:    sql.NullString{String: campaignID, Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets-variables",
	}

	canMerge, reason, err := CanMerge(context.Background(), db, job)
	if err != nil {
		t.Fatalf("CanMerge() returned error: %v", err)
	}
	if canMerge {
		t.Errorf("CanMerge() = true; want false")
	}
	if reason != BeforeDependency {
		t.Errorf("reason = %q; want %q", reason, BeforeDependency)
	}
}

// ---------------------------------------------------------------------------
// TS-11-13: CanMerge returns (false, AlreadyMerged, nil) when the source
// branch is already integrated into the target.
// Requirement: 11-REQ-3.3
// ---------------------------------------------------------------------------

func TestCanMerge_AlreadyMerged(t *testing.T) {
	db := openCanMergeTestDB(t)

	campaignID := newTestUUID("camp3")
	now := time.Now().UTC().Format(time.RFC3339)

	// Campaign with no dependencies (single spec).
	insertTestCampaign(t, db, campaignID, "test-ws", "main",
		`{"specs":["07"],"edges":[]}`,
		now)

	// The spec is already merged in campaign_specs.
	insertTestCampaignSpec(t, db, campaignID, "07", "merged", "sha-already-merged", now)

	// Also insert a prior merge job that completed successfully for the
	// same workspace/source_ref/target_branch.
	insertTestMergeJob(t, db, newTestUUID("prior1"), newTestUUID("pnonce1"),
		"test-ws", "main", "spec/07-secrets-variables", "merged", newTestUUID("user"))

	job := MergeJob{
		ID:            newTestUUID("job3"),
		CampaignID:    sql.NullString{String: campaignID, Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets-variables",
	}

	canMerge, reason, err := CanMerge(context.Background(), db, job)
	if err != nil {
		t.Fatalf("CanMerge() returned error: %v", err)
	}
	if canMerge {
		t.Errorf("CanMerge() = true; want false")
	}
	if reason != AlreadyMerged {
		t.Errorf("reason = %q; want %q", reason, AlreadyMerged)
	}
}

// ---------------------------------------------------------------------------
// TS-11-14: CanMerge returns (false, BranchNotReady, nil) when no new commits
// exist on source_ref.
// Requirement: 11-REQ-3.4
// ---------------------------------------------------------------------------

func TestCanMerge_BranchNotReady(t *testing.T) {
	db := openCanMergeTestDB(t)

	campaignID := newTestUUID("camp4")
	now := time.Now().UTC().Format(time.RFC3339)

	// Campaign with no dependencies (single spec).
	insertTestCampaign(t, db, campaignID, "test-ws", "main",
		`{"specs":["07"],"edges":[]}`,
		now)

	// The spec's branch_sha is NULL — no new commits on the source branch.
	insertTestCampaignSpec(t, db, campaignID, "07", "active", "", now)

	job := MergeJob{
		ID:            newTestUUID("job4"),
		CampaignID:    sql.NullString{String: campaignID, Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets-variables",
	}

	canMerge, reason, err := CanMerge(context.Background(), db, job)
	if err != nil {
		t.Fatalf("CanMerge() returned error: %v", err)
	}
	if canMerge {
		t.Errorf("CanMerge() = true; want false")
	}
	if reason != BranchNotReady {
		t.Errorf("reason = %q; want %q", reason, BranchNotReady)
	}
}

// ---------------------------------------------------------------------------
// TS-11-15: CanMerge returns (false, SpecBlocked, nil) when the spec is in
// blocked status in the campaign.
// Requirement: 11-REQ-3.5
// ---------------------------------------------------------------------------

func TestCanMerge_SpecBlocked(t *testing.T) {
	db := openCanMergeTestDB(t)

	campaignID := newTestUUID("camp5")
	now := time.Now().UTC().Format(time.RFC3339)

	// Campaign with no dependencies (single spec).
	insertTestCampaign(t, db, campaignID, "test-ws", "main",
		`{"specs":["07"],"edges":[]}`,
		now)

	// The spec is blocked in the campaign.
	insertTestCampaignSpec(t, db, campaignID, "07", "blocked", "sha-07", now)

	job := MergeJob{
		ID:            newTestUUID("job5"),
		CampaignID:    sql.NullString{String: campaignID, Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets-variables",
	}

	canMerge, reason, err := CanMerge(context.Background(), db, job)
	if err != nil {
		t.Fatalf("CanMerge() returned error: %v", err)
	}
	if canMerge {
		t.Errorf("CanMerge() = true; want false")
	}
	if reason != SpecBlocked {
		t.Errorf("reason = %q; want %q", reason, SpecBlocked)
	}
}

// ---------------------------------------------------------------------------
// TS-11-16: CanMerge skips BeforeDependency and SpecBlocked checks for
// standalone merges (campaign_id is empty).
// Requirement: 11-REQ-3.6
// ---------------------------------------------------------------------------

func TestCanMerge_StandaloneSkipsCampaignChecks(t *testing.T) {
	db := openCanMergeTestDB(t)

	campaignID := newTestUUID("camp6")
	now := time.Now().UTC().Format(time.RFC3339)

	// Set up a campaign where upstream is NOT merged and spec IS blocked.
	// These conditions would normally prevent merging, but the job below
	// is standalone (campaign_id is empty), so they must be skipped.
	insertTestCampaign(t, db, campaignID, "test-ws", "main",
		`{"specs":["06","07"],"edges":[{"from":"06","to":"07","relationship":"depends_on"}]}`,
		now)
	insertTestCampaignSpec(t, db, campaignID, "06", "active", "sha-06", now)
	insertTestCampaignSpec(t, db, campaignID, "07", "blocked", "sha-07", now)

	// Standalone job — campaign_id is empty.
	job := MergeJob{
		ID:            newTestUUID("job6"),
		CampaignID:    sql.NullString{Valid: false}, // standalone
		SpecID:        sql.NullString{Valid: false},
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets-variables",
	}

	canMerge, reason, err := CanMerge(context.Background(), db, job)
	if err != nil {
		t.Fatalf("CanMerge() returned error: %v", err)
	}
	if !canMerge {
		t.Errorf("CanMerge() = false; want true (standalone should skip campaign checks)")
	}
	if reason != "" {
		t.Errorf("reason = %q; want empty string", reason)
	}
}

// ---------------------------------------------------------------------------
// TS-11-17: CanMerge returns (false, empty string, non-nil error) when a
// database query fails unexpectedly.
// Requirement: 11-REQ-3.E1
// ---------------------------------------------------------------------------

func TestCanMerge_DatabaseError(t *testing.T) {
	// Open a DB and close it immediately to produce query errors.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.Close() // closed — any query will fail

	job := MergeJob{
		ID:            newTestUUID("job7"),
		CampaignID:    sql.NullString{String: newTestUUID("camp7"), Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets-variables",
	}

	canMerge, reason, err := CanMerge(context.Background(), db, job)
	if err == nil {
		t.Fatal("CanMerge() returned nil error; want non-nil error for closed DB")
	}
	if canMerge {
		t.Errorf("CanMerge() = true; want false on error")
	}
	if reason != "" {
		t.Errorf("reason = %q; want empty string on error", reason)
	}
}

// ---------------------------------------------------------------------------
// TS-11-18: CanMerge returns immediately with context error when the context
// is already cancelled.
// Requirement: 11-REQ-3.E2
// ---------------------------------------------------------------------------

func TestCanMerge_CancelledContext(t *testing.T) {
	db := openCanMergeTestDB(t)

	// Create a context that is already cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	job := MergeJob{
		ID:            newTestUUID("job8"),
		CampaignID:    sql.NullString{String: newTestUUID("camp8"), Valid: true},
		SpecID:        sql.NullString{String: "07", Valid: true},
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets-variables",
	}

	canMerge, reason, err := CanMerge(ctx, db, job)
	if err == nil {
		t.Fatal("CanMerge() returned nil error; want context.Canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v; want context.Canceled", err)
	}
	if canMerge {
		t.Errorf("CanMerge() = true; want false on cancelled context")
	}
	if reason != "" {
		t.Errorf("reason = %q; want empty string on error", reason)
	}
}
