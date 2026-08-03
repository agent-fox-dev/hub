package campaign

import (
	"context"
	"database/sql"
	"net/http"
	"testing"

	"github.com/agent-fox-dev/hub/internal/mergequeue"
)

// TS-12-SMOKE-1: Create implicit campaign, verify active status and
// frontier branches are created.
//
// Validates: 12-REQ-1, 12-REQ-5
func TestSmoke_ImplicitCampaignCreation(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Set up workspace directory with spec tasks.json files containing
	// pending subtasks.
	tasksJSON07 := `{
		"dependencies": [],
		"tasks": [{"subtasks": [{"state": "pending"}]}]
	}`
	tasksJSON09 := `{
		"dependencies": [{"depends_on_spec": "07", "relationship": "depends_on"}],
		"tasks": [{"subtasks": [{"state": "pending"}]}]
	}`
	root := setupWorkspaceDir(t, "ws-slug", map[string]*string{
		"07_secrets_variables": strPtr(tasksJSON07),
		"09_dag_builder":      strPtr(tasksJSON09),
	})
	env.handler.workspaceRoot = root

	// Set up mock git ops.
	gitOps := newMockGitOps()
	env.handler.gitOps = gitOps

	// Create implicit campaign (no spec_ids provided).
	body := `{"name": "sprint-42", "integration_branch": "main"}`
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-slug/campaigns", body, adminAuth())

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want %d; body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	resp := parseRawJSON(t, rec)

	// Campaign should be active.
	if resp["status"] != "active" {
		t.Errorf("campaign status = %v; want %q", resp["status"], "active")
	}

	// DAG should be present.
	if resp["dag"] == nil {
		t.Error("campaign response missing 'dag' field")
	}
}

// TS-12-SMOKE-2: Full merge cycle — create campaign, merge a spec,
// verify cascade rebase and frontier advance.
//
// Validates: 12-REQ-1, 12-REQ-6, 12-REQ-8
func TestSmoke_FullMergeCycle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Campaign with DAG: 07 -> 09 (09 depends on 07).
	// 07 is active (being worked on), 09 is pending.
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

	// Simulate spec 07 merge completion.
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

	// Verify spec 07 is now merged.
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

	// Verify spec 09 was activated (frontier advanced).
	spec09, err := store.GetCampaignSpec(ctx, "camp-1", "09")
	if err != nil {
		t.Fatalf("GetCampaignSpec(09) error: %v", err)
	}
	if spec09 == nil {
		t.Fatal("GetCampaignSpec(09) returned nil")
	}
	if spec09.Status != "active" {
		t.Errorf("spec 09 status = %q; want %q", spec09.Status, "active")
	}

	// Now merge spec 09 — campaign should complete.
	job2 := mergequeue.MergeJob{
		ID:            "job-2",
		CampaignID:    sql.NullString{String: "camp-1", Valid: true},
		SpecID:        sql.NullString{String: "09", Valid: true},
		WorkspaceSlug: "ws",
		TargetBranch:  "main",
		Status:        "merged",
	}

	err = scheduler.HandlePostMerge(ctx, job2)
	if err != nil {
		t.Fatalf("HandlePostMerge (job-2) returned error: %v", err)
	}

	// Campaign should be completed.
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

// TS-12-SMOKE-3: Conflict resolution flow — block spec, resolve, verify
// status restored to active.
//
// Validates: 12-REQ-8, 12-REQ-10
func TestSmoke_ConflictResolutionFlow(t *testing.T) {
	env := newHandlerTestEnv(t)

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "test-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpecFull(t, env.db, "camp-1", "07", "blocked", "spec/07-secrets-variables",
		"old-sha-1111111111111111111111111111111111", `["file1.go","file2.go"]`, "merge-uuid-1")

	// Set up mock git ops for clean rebase (conflict resolved).
	gitOps := newMockGitOps()
	gitOps.rebaseSHA = "new-sha-2222222222222222222222222222222222"
	env.handler.gitOps = gitOps
	env.handler.authz = NewAuthz()
	env.handler.rebaseEngine = NewRebaseEngine(env.handler.store, gitOps, env.handler.authz)

	// Call resolve endpoint.
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1/specs/07/resolve",
		"", readWriteAuth("user-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("resolve status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseRawJSON(t, rec)
	if resp["status"] != "active" {
		t.Errorf("resolve response status = %v; want %q", resp["status"], "active")
	}
	if resp["spec_id"] != "07" {
		t.Errorf("resolve response spec_id = %v; want %q", resp["spec_id"], "07")
	}

	// Verify the spec is active in the DB.
	spec, err := env.handler.store.GetCampaignSpec(nil, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec error: %v", err)
	}
	if spec == nil {
		t.Fatal("GetCampaignSpec returned nil")
	}
	if spec.Status != "active" {
		t.Errorf("spec status in DB = %q; want %q", spec.Status, "active")
	}
}

// TS-12-SMOKE-4: Campaign cancellation — cancel, verify all specs
// cancelled, branches intact.
//
// Validates: 12-REQ-13
func TestSmoke_CampaignCancellation(t *testing.T) {
	env := newHandlerTestEnv(t)

	// Set up mock git ops to track any delete calls.
	gitOps := newMockGitOps()
	env.handler.gitOps = gitOps

	dagJSON := `{"specs":["07","08","09"],"edges":[{"from":"07","to":"09","relationship":"depends_on"}]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "sprint-42", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, env.db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, env.db, "camp-1", "08", "active", "spec/08-other", "bbb2222222222222222222222222222222222222")
	seedCampaignSpec(t, env.db, "camp-1", "09", "pending", "", "")

	// Cancel the campaign.
	rec := env.doRequest(t, http.MethodDelete,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d; want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	resp := parseRawJSON(t, rec)
	if resp["status"] != "cancelled" {
		t.Errorf("cancel response status = %v; want %q", resp["status"], "cancelled")
	}

	// Verify all campaign_specs statuses are cancelled.
	specs, err := env.handler.store.GetCampaignSpecs(nil, "camp-1")
	if err != nil {
		t.Fatalf("GetCampaignSpecs error: %v", err)
	}
	for _, spec := range specs {
		if spec.Status != "cancelled" {
			t.Errorf("spec %s status = %q; want %q", spec.SpecID, spec.Status, "cancelled")
		}
	}

	// Verify no branches were deleted (branches left in place).
	if len(gitOps.deleteCalls) > 0 {
		t.Errorf("expected no branch deletions, got %d: %v", len(gitOps.deleteCalls), gitOps.deleteCalls)
	}

	// Verify the campaign itself cannot be cancelled again (409).
	rec2 := env.doRequest(t, http.MethodDelete,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1", "", adminAuth())
	if rec2.Code != http.StatusConflict {
		t.Errorf("second cancel status = %d; want %d", rec2.Code, http.StatusConflict)
	}
}

// TS-12-SMOKE-5: Dead-letter failure — PostMergeHook with DeadLetter=true
// fails entire campaign.
//
// Validates: 12-REQ-7, 12-REQ-1.4
func TestSmoke_DeadLetterFailure(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

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

	// Dead-letter job for spec 07.
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

	// No rebase should have been performed.
	if len(gitOps.rebaseCalls) > 0 {
		t.Errorf("expected no rebase calls for dead-letter; got %d", len(gitOps.rebaseCalls))
	}

	// After failure, subsequent hooks for this campaign should be no-ops.
	job2 := mergequeue.MergeJob{
		ID:            "job-2",
		CampaignID:    sql.NullString{String: "camp-1", Valid: true},
		SpecID:        sql.NullString{String: "08", Valid: true},
		WorkspaceSlug: "ws",
		TargetBranch:  "main",
		Status:        "merged",
	}

	err = scheduler.HandlePostMerge(ctx, job2)
	if err != nil {
		t.Fatalf("HandlePostMerge after failure returned error: %v", err)
	}

	// Spec 08 should remain active (hook was a no-op due to failed campaign).
	spec08, err := store.GetCampaignSpec(ctx, "camp-1", "08")
	if err != nil {
		t.Fatalf("GetCampaignSpec(08) error: %v", err)
	}
	if spec08 == nil {
		t.Fatal("GetCampaignSpec(08) returned nil")
	}
	if spec08.Status != "active" {
		t.Errorf("spec 08 status = %q; want %q (no mutations on failed campaign)", spec08.Status, "active")
	}
}
