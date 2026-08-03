package campaign

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TS-12-45: Store module executes CREATE TABLE IF NOT EXISTS for campaigns and
// campaign_specs with all columns, constraints, and CHECK constraints on hub
// initialization.
func TestInitSchema_CreatesTablesWithColumnsAndConstraints(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)

	// Verify campaigns table exists.
	var campaignsName string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='campaigns'").Scan(&campaignsName)
	if err != nil {
		t.Fatalf("campaigns table not found: %v", err)
	}
	if campaignsName != "campaigns" {
		t.Errorf("expected table name 'campaigns', got %q", campaignsName)
	}

	// Verify campaign_specs table exists.
	var specsName string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='campaign_specs'").Scan(&specsName)
	if err != nil {
		t.Fatalf("campaign_specs table not found: %v", err)
	}
	if specsName != "campaign_specs" {
		t.Errorf("expected table name 'campaign_specs', got %q", specsName)
	}

	// Verify campaigns table columns.
	campaignCols := queryTableInfo(t, db, "campaigns")
	expectedCampaignCols := []string{"id", "workspace_slug", "name", "integration_branch", "status", "dag", "created_by", "created_at", "updated_at"}
	for _, col := range expectedCampaignCols {
		if _, found := findColumn(campaignCols, col); !found {
			t.Errorf("campaigns table missing column %q", col)
		}
	}

	// Verify campaign_specs table columns.
	specCols := queryTableInfo(t, db, "campaign_specs")
	expectedSpecCols := []string{"campaign_id", "spec_id", "status", "branch_name", "branch_sha", "conflict_details", "blocked_by_merge", "updated_at"}
	for _, col := range expectedSpecCols {
		if _, found := findColumn(specCols, col); !found {
			t.Errorf("campaign_specs table missing column %q", col)
		}
	}
}

// TS-12-46: Campaigns table enforces CHECK(status IN ('pending','active','completed',
// 'failed','cancelled')) and UNIQUE(workspace_slug, name) constraints.
func TestCampaignsTable_CheckAndUniqueConstraints(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)

	// Test CHECK constraint rejects invalid status.
	_, err := db.Exec(
		`INSERT INTO campaigns (id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"id-check", "ws-slug", "camp-check", "main", "running", "{}", "user", now, now,
	)
	if err == nil {
		t.Fatal("expected CHECK constraint error for invalid status 'running', got nil")
	}
	if !strings.Contains(err.Error(), "CHECK constraint") {
		t.Errorf("expected CHECK constraint error, got: %v", err)
	}

	// Insert a valid campaign for the UNIQUE test.
	_, err = db.Exec(
		`INSERT INTO campaigns (id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"id-unique-1", "ws-slug", "sprint-42", "main", "active", "{}", "user", now, now,
	)
	if err != nil {
		t.Fatalf("failed to insert first campaign: %v", err)
	}

	// Test UNIQUE(workspace_slug, name) constraint.
	_, err = db.Exec(
		`INSERT INTO campaigns (id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"id-unique-2", "ws-slug", "sprint-42", "main", "completed", "{}", "user", now, now,
	)
	if err == nil {
		t.Fatal("expected UNIQUE constraint error for duplicate (workspace_slug, name), got nil")
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint") {
		t.Errorf("expected UNIQUE constraint error, got: %v", err)
	}
}

// TS-12-1: Campaign store enforces that status must be one of pending, active,
// completed, failed, or cancelled via a CHECK constraint.
func TestCampaignsTable_StatusCheckConstraint(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)

	// Try inserting with invalid status 'running'.
	_, err := db.Exec(
		`INSERT INTO campaigns (id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"uuid-1", "ws", "camp", "main", "running", "{}", "user", now, now,
	)
	if err == nil {
		t.Fatal("expected error inserting campaign with invalid status 'running', got nil")
	}
	if !strings.Contains(err.Error(), "CHECK constraint") {
		t.Errorf("error should mention CHECK constraint, got: %v", err)
	}

	// Verify all valid statuses are accepted.
	validStatuses := []string{"pending", "active", "completed", "failed", "cancelled"}
	for i, status := range validStatuses {
		_, err := db.Exec(
			`INSERT INTO campaigns (id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"uuid-valid-"+status, "ws", "camp-"+status, "main", status, "{}", "user", now, now,
		)
		if err != nil {
			t.Errorf("valid status[%d] %q was rejected: %v", i, status, err)
		}
	}
}

// TS-12-47: Campaign_specs table enforces CHECK(status IN ('pending','active',
// 'merged','blocked','failed','cancelled')) and REFERENCES campaigns(id) FK.
func TestCampaignSpecsTable_CheckAndFKConstraints(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)

	// First create a valid campaign so we can test campaign_specs constraints.
	_, err := db.Exec(
		`INSERT INTO campaigns (id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"camp-1", "ws", "test-camp", "main", "active", "{}", "user", now, now,
	)
	if err != nil {
		t.Fatalf("failed to insert parent campaign: %v", err)
	}

	// Test CHECK constraint rejects invalid status.
	_, err = db.Exec(
		`INSERT INTO campaign_specs (campaign_id, spec_id, status, branch_name, branch_sha, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"camp-1", "07", "running", "", "", now,
	)
	if err == nil {
		t.Fatal("expected CHECK constraint error for invalid spec status 'running', got nil")
	}
	if !strings.Contains(err.Error(), "CHECK constraint") {
		t.Errorf("expected CHECK constraint error, got: %v", err)
	}

	// Verify all valid spec statuses are accepted.
	validStatuses := []string{"pending", "active", "merged", "blocked", "failed", "cancelled"}
	for i, status := range validStatuses {
		specID := fmt.Sprintf("spec-%d", i)
		_, err := db.Exec(
			`INSERT INTO campaign_specs (campaign_id, spec_id, status, branch_name, branch_sha, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			"camp-1", specID, status, "", "", now,
		)
		if err != nil {
			t.Errorf("valid spec status[%d] %q was rejected: %v", i, status, err)
		}
	}

	// Test REFERENCES campaigns(id) foreign key.
	_, err = db.Exec(
		`INSERT INTO campaign_specs (campaign_id, spec_id, status, branch_name, branch_sha, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"nonexistent-campaign", "99", "pending", "", "", now,
	)
	if err == nil {
		t.Fatal("expected FK constraint error for nonexistent campaign_id, got nil")
	}
}

// TS-12-7: The campaigns table enforces UNIQUE(workspace_slug, name) across all
// statuses, preventing duplicate campaign names within a workspace.
func TestCampaignsTable_UniqueNamePerWorkspace(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)

	// Insert a campaign with status='completed'.
	_, err := db.Exec(
		`INSERT INTO campaigns (id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"id-1", "ws-slug", "sprint-42", "main", "completed", "{}", "user", now, now,
	)
	if err != nil {
		t.Fatalf("failed to insert first campaign: %v", err)
	}

	// Try inserting another campaign with the same (workspace_slug, name) but different status.
	_, err = db.Exec(
		`INSERT INTO campaigns (id, workspace_slug, name, integration_branch, status, dag, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"id-2", "ws-slug", "sprint-42", "main", "active", "{}", "user", now, now,
	)
	if err == nil {
		t.Fatal("expected UNIQUE constraint error for duplicate (workspace_slug, name), got nil")
	}
	if !strings.Contains(err.Error(), "UNIQUE constraint") {
		t.Errorf("expected UNIQUE constraint error, got: %v", err)
	}
}

// Helper: verify the InitSchema returns an error when the DB is unusable.
// TS-12-15.E1: Schema initialization SQL failure returns non-nil error.
func TestInitSchema_ReturnsErrorOnFailure(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	// Close the database before calling InitSchema to simulate failure.
	db.Close()

	err = InitSchema(db)
	if err == nil {
		t.Fatal("expected InitSchema to return error for closed database, got nil")
	}
}
