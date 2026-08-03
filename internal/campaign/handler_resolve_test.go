package campaign

import (
	"net/http"
	"testing"
)

// TS-12-30: Resolve endpoint rebases spec branch onto current integration
// branch HEAD, restores push access, sets spec status to active, updates
// branch_sha, and returns HTTP 200 with spec_id, status=active, and
// branch_sha when spec is blocked and rebase is clean.
//
// Requirement: 12-REQ-10.1
func TestResolveEndpoint_BlockedSpec_CleanRebase_Returns200(t *testing.T) {
	env := newHandlerTestEnv(t)

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "test-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpecFull(t, env.db, "camp-1", "07", "blocked", "spec/07-secrets-variables",
		"old-sha-1111111111111111111111111111111111", `["file1.go"]`, "merge-uuid-1")

	// Set up mock git ops for clean rebase.
	gitOps := newMockGitOps()
	gitOps.rebaseSHA = "new-sha-22222222222222222222222222222222"
	env.handler.gitOps = gitOps
	env.handler.authz = NewAuthz()
	env.handler.rebaseEngine = NewRebaseEngine(env.handler.store, gitOps, env.handler.authz)

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1/specs/07/resolve",
		"", readWriteAuth("user-1"))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	body := parseRawJSON(t, rec)
	if body["spec_id"] != "07" {
		t.Errorf("spec_id = %v; want %q", body["spec_id"], "07")
	}
	if body["status"] != "active" {
		t.Errorf("status = %v; want %q", body["status"], "active")
	}
	sha, ok := body["branch_sha"].(string)
	if !ok || len(sha) != 40 {
		t.Errorf("branch_sha = %v; want 40-char hex SHA", body["branch_sha"])
	}
}

// TS-12-30 (continued): Verify that push access is restored after
// successful resolution.
func TestResolveEndpoint_BlockedSpec_CleanRebase_RestoresPushAccess(t *testing.T) {
	env := newHandlerTestEnv(t)

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "test-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpecFull(t, env.db, "camp-1", "07", "blocked", "spec/07-secrets-variables",
		"old-sha-1111111111111111111111111111111111", `["file1.go"]`, "merge-uuid-1")

	gitOps := newMockGitOps()
	authz := NewAuthz()
	authz.BlockBranch("spec/07-secrets-variables")
	env.handler.gitOps = gitOps
	env.handler.authz = authz
	env.handler.rebaseEngine = NewRebaseEngine(env.handler.store, gitOps, authz)

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1/specs/07/resolve",
		"", readWriteAuth("user-1"))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	// After successful resolution, the branch should no longer be blocked.
	if authz.IsBlocked("spec/07-secrets-variables") {
		t.Error("branch still blocked after successful resolve; want unblocked")
	}
}

// TS-12-31: Resolve endpoint returns HTTP 409 with updated conflict file
// list when spec is blocked and rebase still conflicts after agent's
// local fix.
//
// Requirement: 12-REQ-10.2
func TestResolveEndpoint_BlockedSpec_StillConflicting_Returns409(t *testing.T) {
	env := newHandlerTestEnv(t)

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "test-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpecFull(t, env.db, "camp-1", "07", "blocked", "spec/07-secrets-variables",
		"old-sha-1111111111111111111111111111111111", `["old_conflict.go"]`, "merge-uuid-1")

	// Set up mock git ops to return conflicts.
	gitOps := newMockGitOps()
	gitOps.rebaseConflicts = map[string][]string{
		"spec/07-secrets-variables": {"file1.go", "file2.go"},
	}
	env.handler.gitOps = gitOps
	env.handler.authz = NewAuthz()
	env.handler.rebaseEngine = NewRebaseEngine(env.handler.store, gitOps, env.handler.authz)

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1/specs/07/resolve",
		"", readWriteAuth("user-1"))

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusConflict)
	}

	body := parseRawJSON(t, rec)
	details, ok := body["conflict_details"]
	if !ok {
		t.Fatal("response missing conflict_details field")
	}

	// conflict_details should be a non-empty array.
	arr, ok := details.([]any)
	if !ok {
		t.Fatalf("conflict_details is %T; want []any", details)
	}
	if len(arr) == 0 {
		t.Error("conflict_details is empty; want at least one file")
	}
}

// TS-12-31 (continued): Verify that spec status remains blocked when
// rebase still conflicts.
func TestResolveEndpoint_StillConflicting_SpecRemainsBlocked(t *testing.T) {
	env := newHandlerTestEnv(t)

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "test-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpecFull(t, env.db, "camp-1", "07", "blocked", "spec/07-secrets-variables",
		"old-sha-1111111111111111111111111111111111", `["file1.go"]`, "merge-uuid-1")

	gitOps := newMockGitOps()
	gitOps.rebaseConflicts = map[string][]string{
		"spec/07-secrets-variables": {"new_conflict.go"},
	}
	env.handler.gitOps = gitOps
	env.handler.authz = NewAuthz()
	env.handler.rebaseEngine = NewRebaseEngine(env.handler.store, gitOps, env.handler.authz)

	_ = env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1/specs/07/resolve",
		"", readWriteAuth("user-1"))

	// Verify spec remains blocked in DB.
	spec, err := env.handler.store.GetCampaignSpec(nil, "camp-1", "07")
	if err != nil {
		t.Fatalf("GetCampaignSpec error: %v", err)
	}
	if spec == nil {
		t.Fatal("GetCampaignSpec returned nil")
	}
	if spec.Status != "blocked" {
		t.Errorf("spec status = %q; want %q (still conflicting)", spec.Status, "blocked")
	}
}

// TS-12-32: Resolve endpoint returns HTTP 200 with current status and
// branch_sha without performing any rebase when spec is already in
// active status.
//
// Requirement: 12-REQ-10.3
func TestResolveEndpoint_ActiveSpec_Returns200_NoRebase(t *testing.T) {
	env := newHandlerTestEnv(t)

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "test-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, env.db, "camp-1", "07", "active", "spec/07-secrets-variables",
		"abc12345678901234567890123456789012345678")

	gitOps := newMockGitOps()
	env.handler.gitOps = gitOps

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1/specs/07/resolve",
		"", readWriteAuth("user-1"))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	body := parseRawJSON(t, rec)
	if body["spec_id"] != "07" {
		t.Errorf("spec_id = %v; want %q", body["spec_id"], "07")
	}
	if body["status"] != "active" {
		t.Errorf("status = %v; want %q", body["status"], "active")
	}
	if body["branch_sha"] != "abc12345678901234567890123456789012345678" {
		t.Errorf("branch_sha = %v; want %q", body["branch_sha"], "abc12345678901234567890123456789012345678")
	}

	// No rebase should have been performed.
	if len(gitOps.rebaseCalls) != 0 {
		t.Errorf("expected 0 rebase calls for active spec; got %d", len(gitOps.rebaseCalls))
	}
}

// TS-12-33: Resolve endpoint returns HTTP 200 with current status=merged
// and branch_sha without performing any rebase when spec is already in
// merged status.
//
// Requirement: 12-REQ-10.4
func TestResolveEndpoint_MergedSpec_Returns200_NoRebase(t *testing.T) {
	env := newHandlerTestEnv(t)

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "test-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpec(t, env.db, "camp-1", "07", "merged", "spec/07-secrets-variables",
		"def45678901234567890123456789012345678ab")

	gitOps := newMockGitOps()
	env.handler.gitOps = gitOps

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1/specs/07/resolve",
		"", readWriteAuth("user-1"))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusOK)
	}

	body := parseRawJSON(t, rec)
	if body["spec_id"] != "07" {
		t.Errorf("spec_id = %v; want %q", body["spec_id"], "07")
	}
	if body["status"] != "merged" {
		t.Errorf("status = %v; want %q", body["status"], "merged")
	}
	if body["branch_sha"] != "def45678901234567890123456789012345678ab" {
		t.Errorf("branch_sha = %v; want %q", body["branch_sha"], "def45678901234567890123456789012345678ab")
	}

	// No rebase should have been performed.
	if len(gitOps.rebaseCalls) != 0 {
		t.Errorf("expected 0 rebase calls for merged spec; got %d", len(gitOps.rebaseCalls))
	}
}

// TS-12-34: Resolve endpoint returns HTTP 409 with an error message when
// spec is in pending, failed, or cancelled status.
//
// Requirement: 12-REQ-10.5
func TestResolveEndpoint_InvalidStatus_Returns409(t *testing.T) {
	for _, status := range []string{"pending", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			env := newHandlerTestEnv(t)

			dagJSON := `{"specs":["07"],"edges":[]}`
			seedCampaign(t, env.db, "camp-1", "ws-slug", "test-campaign", "main", "active", dagJSON, "user-1")
			seedCampaignSpec(t, env.db, "camp-1", "07", status, "spec/07-secrets-variables", "abc123")

			rec := env.doRequest(t, http.MethodPost,
				"/api/v1/workspaces/ws-slug/campaigns/camp-1/specs/07/resolve",
				"", readWriteAuth("user-1"))

			if rec.Code != http.StatusConflict {
				t.Errorf("status = %d; want %d for spec in %q status", rec.Code, http.StatusConflict, status)
			}

			body := parseRawJSON(t, rec)
			errMsg, ok := body["error"].(string)
			if !ok || errMsg == "" {
				t.Error("expected non-empty error message in response body")
			}
		})
	}
}

// Edge case 12-REQ-10.E1: Resolve endpoint called for a spec_id that does
// not exist in the campaign should return HTTP 404.
func TestResolveEndpoint_SpecNotFound_Returns404(t *testing.T) {
	env := newHandlerTestEnv(t)

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "test-campaign", "main", "active", dagJSON, "user-1")
	// Note: no campaign_spec for "99".

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1/specs/99/resolve",
		"", readWriteAuth("user-1"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusNotFound)
	}

	body := parseRawJSON(t, rec)
	errMsg, ok := body["error"].(string)
	if !ok || errMsg == "" {
		t.Error("expected non-empty error message in response body")
	}
}

// Edge case 12-REQ-10.E3: Resolve endpoint called without campaigns:write
// permission should return HTTP 403.
func TestResolveEndpoint_NoWritePermission_Returns403(t *testing.T) {
	env := newHandlerTestEnv(t)

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "test-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpecFull(t, env.db, "camp-1", "07", "blocked", "spec/07-secrets-variables",
		"old-sha-1111111111111111111111111111111111", `["file1.go"]`, "merge-uuid-1")

	// Use read-only auth (no campaigns:write scope).
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1/specs/07/resolve",
		"", readAuth("user-1"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusForbidden)
	}

	body := parseRawJSON(t, rec)
	errMsg, ok := body["error"].(string)
	if !ok || errMsg == "" {
		t.Error("expected non-empty error message in response body")
	}
}

// Edge case 12-REQ-10.E4: Newer merges have occurred since the agent's
// local fix, introducing new conflicts during resolve verification.
func TestResolveEndpoint_NewConflictsAfterLocalFix_Returns409(t *testing.T) {
	env := newHandlerTestEnv(t)

	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "test-campaign", "main", "active", dagJSON, "user-1")
	seedCampaignSpecFull(t, env.db, "camp-1", "07", "blocked", "spec/07-secrets-variables",
		"old-sha-1111111111111111111111111111111111", `["original_file.go"]`, "merge-uuid-1")

	// Set up mock git ops to return NEW conflicts (different from original).
	gitOps := newMockGitOps()
	gitOps.rebaseConflicts = map[string][]string{
		"spec/07-secrets-variables": {"new_conflict.go", "another_new.go"},
	}
	env.handler.gitOps = gitOps
	env.handler.authz = NewAuthz()
	env.handler.rebaseEngine = NewRebaseEngine(env.handler.store, gitOps, env.handler.authz)

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/ws-slug/campaigns/camp-1/specs/07/resolve",
		"", readWriteAuth("user-1"))

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d; want %d", rec.Code, http.StatusConflict)
	}

	body := parseRawJSON(t, rec)
	details, ok := body["conflict_details"]
	if !ok {
		t.Fatal("response missing conflict_details field")
	}
	arr, ok := details.([]any)
	if !ok {
		t.Fatalf("conflict_details is %T; want []any", details)
	}
	if len(arr) == 0 {
		t.Error("conflict_details is empty; want updated conflict file list")
	}
}
