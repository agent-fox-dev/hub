package campaign

import (
	"context"
	"testing"
)

// TS-12-27: Authz module registers a PushAuthorizer callback with
// internal/gitserver that rejects push attempts to a blocked spec branch.
//
// Requirement: 12-REQ-9.1
func TestAuthz_BlockedBranch_RejectsPush(t *testing.T) {
	ctx := context.Background()
	authz := NewAuthz()

	// Block the spec branch.
	authz.BlockBranch("spec/07-secrets-variables")

	// Attempt to push to the blocked branch.
	err := authz.AuthorizePush(ctx, "spec/07-secrets-variables")
	if err == nil {
		t.Fatal("AuthorizePush on blocked branch returned nil; want error")
	}

	// Verify the error message mentions "blocked".
	if !containsSubstring(err.Error(), "blocked") {
		t.Errorf("error = %q; want message containing %q", err.Error(), "blocked")
	}
}

// TS-12-27 (continued): IsBlocked reports that a blocked branch is blocked.
func TestAuthz_BlockedBranch_IsBlocked(t *testing.T) {
	authz := NewAuthz()
	authz.BlockBranch("spec/07-secrets-variables")

	if !authz.IsBlocked("spec/07-secrets-variables") {
		t.Error("IsBlocked returned false for blocked branch; want true")
	}
}

// TS-12-28: PushAuthorizer rejects direct pushes to the integration branch
// for any campaign, requiring all merges to go through the merge queue.
//
// Requirement: 12-REQ-9.2
func TestAuthz_IntegrationBranch_RejectsDirectPush(t *testing.T) {
	ctx := context.Background()
	authz := NewAuthz()

	// Register main as a protected integration branch.
	authz.ProtectIntegrationBranch("main")

	// Attempt to push directly to the integration branch.
	err := authz.AuthorizePush(ctx, "main")
	if err == nil {
		t.Fatal("AuthorizePush on integration branch returned nil; want error")
	}

	// Verify the error message mentions "integration branch".
	if !containsSubstring(err.Error(), "integration branch") {
		t.Errorf("error = %q; want message containing %q", err.Error(), "integration branch")
	}
}

// TS-12-29: Authz module restores push access to a spec branch via
// PushAuthorizer when a blocked spec is successfully resolved and set
// back to active.
//
// Requirement: 12-REQ-9.3
func TestAuthz_UnblockBranch_RestoresPushAccess(t *testing.T) {
	ctx := context.Background()
	authz := NewAuthz()

	// Block then unblock the branch.
	authz.BlockBranch("spec/07-secrets-variables")
	authz.UnblockBranch("spec/07-secrets-variables")

	// Push should now be allowed.
	err := authz.AuthorizePush(ctx, "spec/07-secrets-variables")
	if err != nil {
		t.Fatalf("AuthorizePush on unblocked branch returned error: %v", err)
	}

	// IsBlocked should return false.
	if authz.IsBlocked("spec/07-secrets-variables") {
		t.Error("IsBlocked returned true for unblocked branch; want false")
	}
}

// Edge case 12-REQ-9.E2: PushAuthorizer called for a branch not tracked in
// any active campaign should allow the push.
func TestAuthz_UntrackedBranch_AllowsPush(t *testing.T) {
	ctx := context.Background()
	authz := NewAuthz()

	// No branches blocked, no integration branches registered.
	err := authz.AuthorizePush(ctx, "feature/some-branch")
	if err != nil {
		t.Fatalf("AuthorizePush on untracked branch returned error: %v; want nil", err)
	}
}

// Edge case 12-REQ-9.E1: Push attempt on a cancelled campaign's spec branch
// should be rejected.
func TestAuthz_CancelledCampaignBranch_RejectsPush(t *testing.T) {
	ctx := context.Background()
	authz := NewAuthz()

	// Block the branch (as would happen when campaign is cancelled and branches
	// are marked blocked).
	authz.BlockBranch("spec/07-secrets-variables")

	err := authz.AuthorizePush(ctx, "spec/07-secrets-variables")
	if err == nil {
		t.Fatal("AuthorizePush on cancelled campaign branch returned nil; want error")
	}
}

// containsSubstring reports whether s contains substr, case-insensitive.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && contains(s, substr)
}

// contains does a simple substring search (case-sensitive).
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
