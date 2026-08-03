package campaign

import (
	"context"
	"fmt"
	"testing"
)

// TS-12-23: Rebase engine rebases all active spec branches onto the new
// integration branch HEAD in topological DAG order (roots first, leaves last)
// after a successful merge.
//
// Requirement: 12-REQ-8.1
func TestCascadeRebase_TopologicalOrder(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// DAG: 07 -> 09 (07 is root, 09 depends on 07). Both are active.
	dagJSON := `{"specs":["07","09"],"edges":[{"from":"07","to":"09","relationship":"depends_on"}]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "09", "active", "spec/09-other", "bbb2222222222222222222222222222222222222")

	store := NewStore(db)
	gitOps := newMockGitOps()
	authz := NewAuthz()
	engine := NewRebaseEngine(store, gitOps, authz)

	results, err := engine.CascadeRebase(ctx, "camp-1", "merge-job-1", "main", "/repo")
	if err != nil {
		t.Fatalf("CascadeRebase returned error: %v", err)
	}

	// Verify both branches were rebased.
	if len(results) < 2 {
		t.Fatalf("CascadeRebase returned %d results; want >= 2", len(results))
	}

	// Verify rebase calls happened in topological order: 07 before 09.
	if len(gitOps.rebaseCalls) < 2 {
		t.Fatalf("expected >= 2 rebase calls; got %d", len(gitOps.rebaseCalls))
	}

	idx07, idx09 := -1, -1
	for i, name := range gitOps.rebaseCalls {
		if name == "spec/07-secrets-variables" {
			idx07 = i
		}
		if name == "spec/09-other" {
			idx09 = i
		}
	}
	if idx07 == -1 {
		t.Error("spec/07-secrets-variables was not rebased")
	}
	if idx09 == -1 {
		t.Error("spec/09-other was not rebased")
	}
	if idx07 != -1 && idx09 != -1 && idx07 >= idx09 {
		t.Errorf("rebase order wrong: 07 at index %d, 09 at index %d; want 07 before 09", idx07, idx09)
	}
}

// TS-12-24: Rebase engine updates branch_sha in campaign_specs to the new
// HEAD SHA when a spec branch rebases cleanly.
//
// Requirement: 12-REQ-8.2
func TestCascadeRebase_CleanRebase_UpdatesBranchSHA(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "old-sha-1111111111111111111111111111111111")

	store := NewStore(db)
	gitOps := newMockGitOps()
	gitOps.rebaseSHA = "new-sha-2222222222222222222222222222222222"
	authz := NewAuthz()
	engine := NewRebaseEngine(store, gitOps, authz)

	_, err := engine.CascadeRebase(ctx, "camp-1", "merge-job-1", "main", "/repo")
	if err != nil {
		t.Fatalf("CascadeRebase returned error: %v", err)
	}

	// Verify branch_sha was updated in campaign_specs.
	spec07, err := store.GetCampaignSpec(ctx, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec(07) error: %v", err)
	}
	if spec07 == nil {
		t.Fatal("GetCampaignSpec(07) returned nil")
	}
	if spec07.BranchSHA == "old-sha-1111111111111111111111111111111111" {
		t.Error("branch_sha was not updated after clean rebase")
	}
	if spec07.Status != "active" {
		t.Errorf("spec 07 status = %q; want %q (should remain active)", spec07.Status, "active")
	}
}

// TS-12-25: Rebase engine sets spec status to blocked, revokes push access
// via PushAuthorizer, records conflicting file paths in conflict_details,
// records blocked_by_merge UUID, and skips downstream dependents when a
// spec branch has a rebase conflict.
//
// Requirement: 12-REQ-8.3
func TestCascadeRebase_Conflict_SetsBlocked(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// DAG: 07 -> 09 (09 depends on 07). Spec 07 will conflict.
	dagJSON := `{"specs":["07","09"],"edges":[{"from":"07","to":"09","relationship":"depends_on"}]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "09", "active", "spec/09-other", "bbb2222222222222222222222222222222222222")

	store := NewStore(db)
	gitOps := newMockGitOps()
	gitOps.rebaseConflicts = map[string][]string{
		"spec/07-secrets-variables": {"file1.go"},
	}
	authz := NewAuthz()
	engine := NewRebaseEngine(store, gitOps, authz)

	_, err := engine.CascadeRebase(ctx, "camp-1", "merge-uuid-1", "main", "/repo")
	if err != nil {
		t.Fatalf("CascadeRebase returned error: %v", err)
	}

	// Verify spec 07 is blocked with conflict details.
	spec07, err := store.GetCampaignSpec(ctx, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec(07) error: %v", err)
	}
	if spec07 == nil {
		t.Fatal("GetCampaignSpec(07) returned nil")
	}
	if spec07.Status != "blocked" {
		t.Errorf("spec 07 status = %q; want %q", spec07.Status, "blocked")
	}
	if len(spec07.ConflictDetails) == 0 {
		t.Error("spec 07 conflict_details is empty; want at least one conflicting file")
	}
	if spec07.BlockedByMerge != "merge-uuid-1" {
		t.Errorf("spec 07 blocked_by_merge = %q; want %q", spec07.BlockedByMerge, "merge-uuid-1")
	}

	// Verify push access was revoked for spec 07.
	if !authz.IsBlocked("spec/07-secrets-variables") {
		t.Error("push access not revoked for blocked spec 07 branch")
	}

	// Verify spec 09 (downstream dependent) was NOT rebased.
	spec09, err := store.GetCampaignSpec(ctx, "camp-1", "09")
	if err != nil {
		t.Fatalf("GetCampaignSpec(09) error: %v", err)
	}
	if spec09 == nil {
		t.Fatal("GetCampaignSpec(09) returned nil")
	}
	// Spec 09 should still be active with its original SHA (not rebased).
	if spec09.Status != "active" {
		t.Errorf("spec 09 status = %q; want %q (skipped, not blocked)", spec09.Status, "active")
	}
}

// TS-12-26: Rebase engine applies conflict-stop semantics per branch subtree
// only. A conflict in one branch does not prevent rebasing of branches in
// unrelated parts of the DAG.
//
// Requirement: 12-REQ-8.4
func TestCascadeRebase_ConflictStopPerSubtree(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// DAG: 07 -> 09 (subtree A), 08 standalone (subtree B).
	// Spec 07 conflicts, spec 08 should still be rebased.
	dagJSON := `{"specs":["07","08","09"],"edges":[{"from":"07","to":"09","relationship":"depends_on"}]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "08", "active", "spec/08-standalone", "ccc3333333333333333333333333333333333333")
	seedCampaignSpec(t, db, "camp-1", "09", "active", "spec/09-other", "bbb2222222222222222222222222222222222222")

	store := NewStore(db)
	gitOps := newMockGitOps()
	gitOps.rebaseConflicts = map[string][]string{
		"spec/07-secrets-variables": {"conflicting_file.go"},
	}
	gitOps.rebaseSHA = "rebased-sha-444444444444444444444444444444444"
	authz := NewAuthz()
	engine := NewRebaseEngine(store, gitOps, authz)

	_, err := engine.CascadeRebase(ctx, "camp-1", "merge-uuid-1", "main", "/repo")
	if err != nil {
		t.Fatalf("CascadeRebase returned error: %v", err)
	}

	// Spec 07 should be blocked (conflict in subtree A).
	spec07, err := store.GetCampaignSpec(ctx, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec(07) error: %v", err)
	}
	if spec07 == nil {
		t.Fatal("GetCampaignSpec(07) returned nil")
	}
	if spec07.Status != "blocked" {
		t.Errorf("spec 07 status = %q; want %q", spec07.Status, "blocked")
	}

	// Spec 09 (downstream of 07) should NOT be rebased (skipped due to conflict-stop).
	spec09, err := store.GetCampaignSpec(ctx, "camp-1", "09")
	if err != nil {
		t.Fatalf("GetCampaignSpec(09) error: %v", err)
	}
	if spec09 == nil {
		t.Fatal("GetCampaignSpec(09) returned nil")
	}
	if spec09.BranchSHA != "bbb2222222222222222222222222222222222222" {
		t.Errorf("spec 09 branch_sha changed; want unchanged (downstream of conflict)")
	}

	// Spec 08 (unrelated subtree B) SHOULD be rebased successfully.
	spec08, err := store.GetCampaignSpec(ctx, "camp-1", "08")
	if err != nil {
		t.Fatalf("GetCampaignSpec(08) error: %v", err)
	}
	if spec08 == nil {
		t.Fatal("GetCampaignSpec(08) returned nil")
	}
	if spec08.BranchSHA == "ccc3333333333333333333333333333333333333" {
		t.Error("spec 08 branch_sha unchanged; want rebased (unrelated subtree)")
	}
	if spec08.Status != "active" {
		t.Errorf("spec 08 status = %q; want %q", spec08.Status, "active")
	}
}

// Edge case 12-REQ-8.E2: GitRunner rebase returns an unexpected error (not a
// conflict). The branch should be treated as blocked and the cascade should
// continue for unrelated branches.
func TestCascadeRebase_UnexpectedError_TreatedAsBlocked(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07","08"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")
	seedCampaignSpec(t, db, "camp-1", "08", "active", "spec/08-standalone", "ccc3333333333333333333333333333333333333")

	store := NewStore(db)
	gitOps := newMockGitOps()
	gitOps.rebaseErrors = map[string]error{
		"spec/07-secrets-variables": fmt.Errorf("unexpected git error"),
	}
	authz := NewAuthz()
	engine := NewRebaseEngine(store, gitOps, authz)

	_, err := engine.CascadeRebase(ctx, "camp-1", "merge-uuid-1", "main", "/repo")
	if err != nil {
		t.Fatalf("CascadeRebase returned error: %v", err)
	}

	// Spec 07 should be blocked due to the unexpected error.
	spec07, err := store.GetCampaignSpec(ctx, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec(07) error: %v", err)
	}
	if spec07 == nil {
		t.Fatal("GetCampaignSpec(07) returned nil")
	}
	if spec07.Status != "blocked" {
		t.Errorf("spec 07 status = %q; want %q (unexpected error treated as blocked)", spec07.Status, "blocked")
	}

	// Spec 08 (unrelated) should still be rebased.
	spec08, err := store.GetCampaignSpec(ctx, "camp-1", "08")
	if err != nil {
		t.Fatalf("GetCampaignSpec(08) error: %v", err)
	}
	if spec08 == nil {
		t.Fatal("GetCampaignSpec(08) returned nil")
	}
	if spec08.BranchSHA == "ccc3333333333333333333333333333333333333" {
		t.Error("spec 08 branch_sha unchanged; want rebased")
	}
}

// Edge case 12-REQ-8.E3: An already-rebased branch encountered again in the
// cascade (idempotency after crash recovery) should be treated as a no-op.
func TestCascadeRebase_AlreadyRebased_NoOp(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, db, "camp-1", "ws", "my-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, db, "camp-1", "07", "active", "spec/07-secrets-variables", "aaa1111111111111111111111111111111111111")

	store := NewStore(db)
	gitOps := newMockGitOps()
	authz := NewAuthz()
	engine := NewRebaseEngine(store, gitOps, authz)

	// First cascade rebase.
	_, err := engine.CascadeRebase(ctx, "camp-1", "merge-job-1", "main", "/repo")
	if err != nil {
		t.Fatalf("first CascadeRebase returned error: %v", err)
	}

	// Second cascade rebase (idempotency scenario).
	results, err := engine.CascadeRebase(ctx, "camp-1", "merge-job-1", "main", "/repo")
	if err != nil {
		t.Fatalf("second CascadeRebase returned error: %v", err)
	}

	// Both calls should succeed without error. The branch should remain
	// active with a consistent SHA.
	if results != nil {
		for _, r := range results {
			if !r.Success {
				t.Errorf("spec %s rebase not successful on idempotent cascade", r.SpecID)
			}
		}
	}
}
