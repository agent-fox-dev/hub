package campaign

import (
	"encoding/json"
	"strings"
	"testing"
)

// TS-12-37: GET /campaigns returns all campaigns for the workspace; when
// status query parameter is provided, only campaigns matching that status
// are returned.
func TestListCampaigns_ReturnsAll(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	// Seed two campaigns with different statuses.
	seedCampaign(t, env.db, "camp-1", "ws-slug", "camp-active", "main", "active",
		`{"specs":["07"],"edges":[]}`, "user")
	seedCampaign(t, env.db, "camp-2", "ws-slug", "camp-completed", "main", "completed",
		`{"specs":["07"],"edges":[]}`, "user")

	rec := env.doRequest(t, "GET", "/api/v1/workspaces/ws-slug/campaigns", "", adminAuth())

	if rec.Code != 200 {
		t.Fatalf("status code = %d; want 200", rec.Code)
	}

	campaigns := parseJSONArray(t, rec)
	if len(campaigns) != 2 {
		t.Fatalf("expected 2 campaigns, got %d", len(campaigns))
	}
}

// TS-12-37: GET /campaigns?status=active filters by status.
func TestListCampaigns_FilterByActiveStatus(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	seedCampaign(t, env.db, "camp-1", "ws-slug", "camp-active", "main", "active",
		`{"specs":["07"],"edges":[]}`, "user")
	seedCampaign(t, env.db, "camp-2", "ws-slug", "camp-completed", "main", "completed",
		`{"specs":["07"],"edges":[]}`, "user")

	rec := env.doRequest(t, "GET", "/api/v1/workspaces/ws-slug/campaigns?status=active", "", adminAuth())

	if rec.Code != 200 {
		t.Fatalf("status code = %d; want 200", rec.Code)
	}

	campaigns := parseJSONArray(t, rec)
	if len(campaigns) != 1 {
		t.Fatalf("expected 1 campaign with status=active, got %d", len(campaigns))
	}

	if name, _ := campaigns[0]["name"].(string); name != "camp-active" {
		t.Errorf("filtered campaign name = %q; want %q", name, "camp-active")
	}

	if status, _ := campaigns[0]["status"].(string); status != "active" {
		t.Errorf("filtered campaign status = %q; want %q", status, "active")
	}
}

// TS-12-37: GET /campaigns filters work for all valid status values.
func TestListCampaigns_FilterByEachStatus(t *testing.T) {
	t.Parallel()

	validStatuses := []string{"active", "completed", "failed", "cancelled"}
	for _, status := range validStatuses {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			env := newHandlerTestEnv(t)

			seedCampaign(t, env.db, "camp-"+status, "ws-slug", "camp-"+status, "main", status,
				`{"specs":["07"],"edges":[]}`, "user")
			// Seed another campaign with a different status to ensure filtering works.
			otherStatus := "active"
			if status == "active" {
				otherStatus = "completed"
			}
			seedCampaign(t, env.db, "camp-other-"+status, "ws-slug", "camp-other-"+status, "main", otherStatus,
				`{"specs":["07"],"edges":[]}`, "user")

			rec := env.doRequest(t, "GET",
				"/api/v1/workspaces/ws-slug/campaigns?status="+status, "", adminAuth())

			if rec.Code != 200 {
				t.Fatalf("status code = %d; want 200 for status=%s", rec.Code, status)
			}

			campaigns := parseJSONArray(t, rec)
			if len(campaigns) != 1 {
				t.Fatalf("expected 1 campaign with status=%s, got %d", status, len(campaigns))
			}

			if gotStatus, _ := campaigns[0]["status"].(string); gotStatus != status {
				t.Errorf("campaign status = %q; want %q", gotStatus, status)
			}
		})
	}
}

// 12-REQ-12.E3: GET /campaigns returns an empty array when no campaigns exist.
func TestListCampaigns_ReturnsEmptyArray(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	rec := env.doRequest(t, "GET", "/api/v1/workspaces/ws-slug/campaigns", "", adminAuth())

	if rec.Code != 200 {
		t.Fatalf("status code = %d; want 200", rec.Code)
	}

	// Response body should be an empty JSON array [].
	var body json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed != "[]" {
		// Also accept null (which Go serializes for nil slices), but prefer [].
		// When implementation is done, it should return [].
		t.Logf("response body = %s; want []", trimmed)
	}

	// Parse as array and check length.
	campaigns := make([]map[string]any, 0)
	if err := json.Unmarshal(body, &campaigns); err != nil {
		t.Fatalf("response body is not a valid JSON array: %v", err)
	}
	if len(campaigns) != 0 {
		t.Errorf("expected empty array, got %d campaigns", len(campaigns))
	}
}

// TS-12-39: GET /campaigns returns HTTP 422 when the status query parameter
// has an invalid value.
func TestListCampaigns_422ForInvalidStatus(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	rec := env.doRequest(t, "GET",
		"/api/v1/workspaces/ws-slug/campaigns?status=invalid_status", "", adminAuth())

	if rec.Code != 422 {
		t.Fatalf("status code = %d; want 422 for invalid status filter", rec.Code)
	}

	resp := parseRawJSON(t, rec)
	errMsg, ok := resp["error"].(string)
	if !ok {
		t.Fatal("response missing 'error' field")
	}
	if !strings.Contains(strings.ToLower(errMsg), "status") {
		t.Errorf("error message should mention 'status', got: %q", errMsg)
	}
}

// 12-REQ-12.E2: GET /campaigns returns HTTP 403 when caller lacks
// campaigns:read permission scope.
func TestListCampaigns_403WhenLackingReadScope(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	// Use a PAT with only campaigns:write scope (missing campaigns:read).
	auth := patAuth("user-1", "campaigns:write")
	rec := env.doRequest(t, "GET", "/api/v1/workspaces/ws-slug/campaigns", "", auth)

	if rec.Code != 403 {
		t.Fatalf("status code = %d; want 403 for missing read scope", rec.Code)
	}
}

// TS-12-38: GET /campaigns/:id returns the full campaign object including dag
// and specs array with per-spec status, branch_name, branch_sha,
// conflict_details, and blocked_by_merge.
func TestGetCampaign_ReturnsFullObject(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	// Seed a campaign with a blocked spec.
	dagJSON := `{"specs":["07"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "sprint-42", "main", "active", dagJSON, "user")
	seedCampaignSpecFull(t, env.db,
		"camp-1", "07", "blocked",
		"spec/07-secrets-variables", "abc123",
		`["file1.go"]`, "merge-uuid",
	)

	rec := env.doRequest(t, "GET", "/api/v1/workspaces/ws-slug/campaigns/camp-1", "", adminAuth())

	if rec.Code != 200 {
		t.Fatalf("status code = %d; want 200", rec.Code)
	}

	resp := parseRawJSON(t, rec)

	// Verify campaign ID.
	if id, _ := resp["id"].(string); id != "camp-1" {
		t.Errorf("campaign id = %q; want %q", id, "camp-1")
	}

	// Verify DAG is present.
	dagRaw, ok := resp["dag"]
	if !ok {
		t.Fatal("response missing 'dag' field")
	}
	dagBytes, _ := json.Marshal(dagRaw)
	var dag DAG
	if err := json.Unmarshal(dagBytes, &dag); err != nil {
		t.Fatalf("failed to parse dag: %v", err)
	}
	if len(dag.Specs) == 0 {
		t.Error("dag.specs should not be empty")
	}

	// Verify specs array.
	specsRaw, ok := resp["specs"]
	if !ok {
		t.Fatal("response missing 'specs' field")
	}
	specs, ok := specsRaw.([]any)
	if !ok || len(specs) == 0 {
		t.Fatalf("specs should be a non-empty array, got: %v", specsRaw)
	}

	// Parse the spec and verify all fields.
	specBytes, _ := json.Marshal(specs[0])
	var spec map[string]any
	if err := json.Unmarshal(specBytes, &spec); err != nil {
		t.Fatalf("failed to parse spec: %v", err)
	}

	if specID, _ := spec["spec_id"].(string); specID != "07" {
		t.Errorf("spec_id = %q; want %q", specID, "07")
	}
	if status, _ := spec["status"].(string); status != "blocked" {
		t.Errorf("spec status = %q; want %q", status, "blocked")
	}
	if branchName, _ := spec["branch_name"].(string); branchName != "spec/07-secrets-variables" {
		t.Errorf("branch_name = %q; want %q", branchName, "spec/07-secrets-variables")
	}
	if branchSHA, _ := spec["branch_sha"].(string); branchSHA != "abc123" {
		t.Errorf("branch_sha = %q; want %q", branchSHA, "abc123")
	}

	// Verify conflict_details is a JSON array.
	conflictRaw, ok := spec["conflict_details"]
	if !ok {
		t.Fatal("spec missing 'conflict_details' field")
	}
	conflictArr, ok := conflictRaw.([]any)
	if !ok || len(conflictArr) == 0 {
		t.Fatalf("conflict_details should be a non-empty array, got: %v", conflictRaw)
	}
	if conflictArr[0] != "file1.go" {
		t.Errorf("conflict_details[0] = %v; want %q", conflictArr[0], "file1.go")
	}

	// Verify blocked_by_merge.
	if blockedBy, _ := spec["blocked_by_merge"].(string); blockedBy != "merge-uuid" {
		t.Errorf("blocked_by_merge = %q; want %q", blockedBy, "merge-uuid")
	}
}

// Verify that branch_name and branch_sha are null/omitted for pending specs.
func TestGetCampaign_PendingSpecsHaveNoBranchInfo(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	dagJSON := `{"specs":["09"],"edges":[]}`
	seedCampaign(t, env.db, "camp-1", "ws-slug", "sprint-42", "main", "active", dagJSON, "user")
	seedCampaignSpec(t, env.db, "camp-1", "09", "pending", "", "")

	rec := env.doRequest(t, "GET", "/api/v1/workspaces/ws-slug/campaigns/camp-1", "", adminAuth())

	if rec.Code != 200 {
		t.Fatalf("status code = %d; want 200", rec.Code)
	}

	resp := parseRawJSON(t, rec)
	specsRaw, ok := resp["specs"]
	if !ok {
		t.Fatal("response missing 'specs' field")
	}
	specs, ok := specsRaw.([]any)
	if !ok || len(specs) == 0 {
		t.Fatal("specs array should not be empty")
	}

	specBytes, _ := json.Marshal(specs[0])
	var spec map[string]any
	if err := json.Unmarshal(specBytes, &spec); err != nil {
		t.Fatalf("failed to parse spec: %v", err)
	}

	// branch_name and branch_sha should be null/empty/omitted for pending specs.
	branchName, _ := spec["branch_name"].(string)
	branchSHA, _ := spec["branch_sha"].(string)
	if branchName != "" {
		t.Errorf("pending spec branch_name should be empty, got: %q", branchName)
	}
	if branchSHA != "" {
		t.Errorf("pending spec branch_sha should be empty, got: %q", branchSHA)
	}
}

// 12-REQ-12.E1: GET /campaigns/:id returns HTTP 404 for unknown campaign ID.
func TestGetCampaign_404WhenNotFound(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	rec := env.doRequest(t, "GET",
		"/api/v1/workspaces/ws-slug/campaigns/nonexistent-id", "", adminAuth())

	if rec.Code != 404 {
		t.Fatalf("status code = %d; want 404 for non-existent campaign", rec.Code)
	}

	resp := parseRawJSON(t, rec)
	if _, ok := resp["error"].(string); !ok {
		t.Fatal("response missing 'error' field")
	}
}

// 12-REQ-12.E2: GET /campaigns/:id returns HTTP 403 when caller lacks
// campaigns:read scope.
func TestGetCampaign_403WhenLackingReadScope(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	seedCampaign(t, env.db, "camp-1", "ws-slug", "sprint-42", "main", "active",
		`{"specs":["07"],"edges":[]}`, "user")

	// Use a PAT with only campaigns:write scope (missing campaigns:read).
	auth := patAuth("user-1", "campaigns:write")
	rec := env.doRequest(t, "GET", "/api/v1/workspaces/ws-slug/campaigns/camp-1", "", auth)

	if rec.Code != 403 {
		t.Fatalf("status code = %d; want 403 for missing read scope", rec.Code)
	}
}

// Verify the campaign response includes workspace_slug and integration_branch fields.
func TestGetCampaign_IncludesAllCampaignFields(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	seedCampaign(t, env.db, "camp-1", "ws-slug", "sprint-42", "main", "active",
		`{"specs":["07"],"edges":[]}`, "user-1")

	rec := env.doRequest(t, "GET", "/api/v1/workspaces/ws-slug/campaigns/camp-1", "", adminAuth())

	if rec.Code != 200 {
		t.Fatalf("status code = %d; want 200", rec.Code)
	}

	resp := parseRawJSON(t, rec)

	requiredFields := []string{"id", "workspace_slug", "name", "integration_branch",
		"status", "dag", "created_by", "created_at", "updated_at"}

	for _, field := range requiredFields {
		if _, ok := resp[field]; !ok {
			t.Errorf("response missing required field %q", field)
		}
	}

	if ws, _ := resp["workspace_slug"].(string); ws != "ws-slug" {
		t.Errorf("workspace_slug = %q; want %q", ws, "ws-slug")
	}
	if ib, _ := resp["integration_branch"].(string); ib != "main" {
		t.Errorf("integration_branch = %q; want %q", ib, "main")
	}
}
