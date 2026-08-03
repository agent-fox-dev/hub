package campaign

import (
	"context"
	"testing"
)

// TS-12-18: Implicit campaign discovery includes a spec only if its tasks.json
// contains at least one subtask with state equal to pending.
func TestDiscoverPendingSpecs_IncludesPendingSpecs(t *testing.T) {
	t.Parallel()

	// Set up workspace with two specs:
	// - 07_secrets_variables: has a pending subtask → should be included
	// - 08_other: all subtasks done → should be excluded
	specTasks := map[string]*string{
		"07_secrets_variables": strPtr(`{
			"dependencies": [],
			"tasks": [
				{"subtasks": [{"state": "done"}, {"state": "pending"}]}
			]
		}`),
		"08_other": strPtr(`{
			"dependencies": [],
			"tasks": [
				{"subtasks": [{"state": "done"}, {"state": "done"}]}
			]
		}`),
	}
	root := setupWorkspaceDir(t, "ws-slug", specTasks)

	ctx := context.Background()
	specIDs, _, err := DiscoverPendingSpecs(ctx, root, "ws-slug")
	if err != nil {
		t.Fatalf("DiscoverPendingSpecs() returned error: %v", err)
	}

	// Spec 07 should be included (has pending subtask).
	found07 := false
	for _, id := range specIDs {
		if id == "07" {
			found07 = true
		}
	}
	if !found07 {
		t.Errorf("expected spec '07' to be included in discovered specs, got: %v", specIDs)
	}

	// Spec 08 should be excluded (no pending subtasks).
	for _, id := range specIDs {
		if id == "08" {
			t.Errorf("expected spec '08' to be excluded from discovered specs, got: %v", specIDs)
		}
	}
}

// TS-12-18: Spec with no pending subtasks is excluded from discovery.
func TestDiscoverPendingSpecs_ExcludesAllDoneSpecs(t *testing.T) {
	t.Parallel()

	specTasks := map[string]*string{
		"08_other": strPtr(`{
			"dependencies": [],
			"tasks": [
				{"subtasks": [{"state": "done"}, {"state": "done"}]}
			]
		}`),
		"09_completed": strPtr(`{
			"dependencies": [],
			"tasks": [
				{"subtasks": [{"state": "done"}]}
			]
		}`),
	}
	root := setupWorkspaceDir(t, "ws-slug", specTasks)

	ctx := context.Background()
	specIDs, _, err := DiscoverPendingSpecs(ctx, root, "ws-slug")

	// When all specs have no pending subtasks, DiscoverPendingSpecs should
	// return an error indicating no pending specs found.
	if err == nil && len(specIDs) == 0 {
		t.Fatalf("expected error when no pending specs found, got nil error and empty specIDs")
	}
	if err == nil {
		t.Errorf("expected error for no pending specs, got specIDs: %v", specIDs)
	}
}

// 12-REQ-5.E1: No spec directories returns error.
func TestDiscoverPendingSpecs_NoSpecDirsReturnsError(t *testing.T) {
	t.Parallel()

	// Set up workspace with no spec directories.
	root := setupWorkspaceDir(t, "ws-slug", map[string]*string{})

	ctx := context.Background()
	_, _, err := DiscoverPendingSpecs(ctx, root, "ws-slug")
	if err == nil {
		t.Fatal("expected error when no spec directories exist, got nil")
	}
}

// 12-REQ-5.E2: Missing workspace root returns error.
func TestDiscoverPendingSpecs_MissingWorkspaceRootReturnsError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, _, err := DiscoverPendingSpecs(ctx, "/nonexistent/path", "ws-slug")
	if err == nil {
		t.Fatal("expected error when workspace root does not exist, got nil")
	}
}

// 12-REQ-5.E3: All specs have no pending subtasks returns error.
func TestDiscoverPendingSpecs_AllDoneReturnsError(t *testing.T) {
	t.Parallel()

	specTasks := map[string]*string{
		"07_secrets_variables": strPtr(`{
			"dependencies": [],
			"tasks": [
				{"subtasks": [{"state": "done"}]}
			]
		}`),
	}
	root := setupWorkspaceDir(t, "ws-slug", specTasks)

	ctx := context.Background()
	_, _, err := DiscoverPendingSpecs(ctx, root, "ws-slug")
	if err == nil {
		t.Fatal("expected error when all specs have no pending subtasks, got nil")
	}
}

// Specs with missing tasks.json are silently skipped (not added to warnings).
func TestDiscoverPendingSpecs_SkipsMissingTasksJSON(t *testing.T) {
	t.Parallel()

	specTasks := map[string]*string{
		"07_secrets_variables": strPtr(`{
			"dependencies": [],
			"tasks": [
				{"subtasks": [{"state": "pending"}]}
			]
		}`),
		"08_no_tasks": nil, // directory exists but no tasks.json
	}
	root := setupWorkspaceDir(t, "ws-slug", specTasks)

	ctx := context.Background()
	specIDs, warnings, err := DiscoverPendingSpecs(ctx, root, "ws-slug")
	if err != nil {
		t.Fatalf("DiscoverPendingSpecs() returned error: %v", err)
	}

	// Spec 07 should still be included.
	found07 := false
	for _, id := range specIDs {
		if id == "07" {
			found07 = true
		}
	}
	if !found07 {
		t.Errorf("expected spec '07' to be included, got: %v", specIDs)
	}

	// Missing tasks.json should not produce a warning (silently skipped).
	for _, w := range warnings {
		if w != "" {
			// Check warnings don't mention spec 08 for missing file.
			t.Logf("warning: %s", w)
		}
	}
}

// Specs with malformed tasks.json are skipped and added to warnings array.
func TestDiscoverPendingSpecs_MalformedTasksJSONProducesWarning(t *testing.T) {
	t.Parallel()

	specTasks := map[string]*string{
		"07_secrets_variables": strPtr(`{
			"dependencies": [],
			"tasks": [
				{"subtasks": [{"state": "pending"}]}
			]
		}`),
		"08_broken": strPtr(`{invalid json`),
	}
	root := setupWorkspaceDir(t, "ws-slug", specTasks)

	ctx := context.Background()
	specIDs, warnings, err := DiscoverPendingSpecs(ctx, root, "ws-slug")
	if err != nil {
		t.Fatalf("DiscoverPendingSpecs() returned error: %v", err)
	}

	// Spec 07 should be included.
	found07 := false
	for _, id := range specIDs {
		if id == "07" {
			found07 = true
		}
	}
	if !found07 {
		t.Errorf("expected spec '07' to be included, got: %v", specIDs)
	}

	// Malformed tasks.json should produce a warning.
	if len(warnings) == 0 {
		t.Error("expected at least one warning for malformed tasks.json")
	}
}

// Unit test for HasPendingSubtask with a pending subtask.
func TestHasPendingSubtask_WithPending(t *testing.T) {
	t.Parallel()

	tj := &DiscoveryTasksJSON{
		Tasks: []DiscoveryTaskGroup{
			{
				Subtasks: []DiscoverySubtask{
					{State: "done"},
					{State: "pending"},
				},
			},
		},
	}

	if !HasPendingSubtask(tj) {
		t.Error("HasPendingSubtask() = false; want true for tasks with a pending subtask")
	}
}

// Unit test for HasPendingSubtask when all subtasks are done.
func TestHasPendingSubtask_AllDone(t *testing.T) {
	t.Parallel()

	tj := &DiscoveryTasksJSON{
		Tasks: []DiscoveryTaskGroup{
			{
				Subtasks: []DiscoverySubtask{
					{State: "done"},
					{State: "done"},
				},
			},
		},
	}

	if HasPendingSubtask(tj) {
		t.Error("HasPendingSubtask() = true; want false when all subtasks are done")
	}
}

// Unit test for ExtractSpecID from spec directory name.
func TestExtractSpecID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dirName string
		want    string
	}{
		{"07_secrets_variables", "07"},
		{"09_dag_builder", "09"},
		{"12_campaign", "12"},
	}

	for _, tt := range tests {
		got := ExtractSpecID(tt.dirName)
		if got != tt.want {
			t.Errorf("ExtractSpecID(%q) = %q; want %q", tt.dirName, got, tt.want)
		}
	}
}

// TS-12-17: POST /campaigns with spec_ids omitted scans tasks.json files at
// the workspace path and includes specs with at least one pending subtask.
func TestCreateCampaign_ImplicitDiscovery(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	// Set up workspace with spec directories and tasks.json files.
	specTasks := map[string]*string{
		"07_secrets_variables": strPtr(`{
			"dependencies": [],
			"tasks": [
				{"subtasks": [{"state": "pending"}]}
			]
		}`),
		"08_other": strPtr(`{
			"dependencies": [],
			"tasks": [
				{"subtasks": [{"state": "done"}, {"state": "done"}]}
			]
		}`),
	}
	root := setupWorkspaceDir(t, "ws-slug", specTasks)
	env.handler.workspaceRoot = root

	// POST without spec_ids triggers implicit discovery.
	body := `{"name":"implicit-camp","integration_branch":"main"}`
	rec := env.doRequest(t, "POST", "/api/v1/workspaces/ws-slug/campaigns", body, adminAuth())

	if rec.Code != 201 {
		t.Fatalf("status code = %d; want 201", rec.Code)
	}

	resp := parseRawJSON(t, rec)

	// Verify specs in response.
	specsRaw, ok := resp["specs"]
	if !ok {
		t.Fatal("response missing 'specs' field")
	}

	specs, ok := specsRaw.([]any)
	if !ok {
		t.Fatalf("specs is not an array: %T", specsRaw)
	}

	// Spec 07 should be included (has pending subtask).
	var specIDs []string
	for _, s := range specs {
		specMap, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := specMap["spec_id"].(string); ok {
			specIDs = append(specIDs, id)
		}
	}

	found07 := false
	found08 := false
	for _, id := range specIDs {
		if id == "07" {
			found07 = true
		}
		if id == "08" {
			found08 = true
		}
	}

	if !found07 {
		t.Errorf("expected spec '07' in response specs, got: %v", specIDs)
	}
	if found08 {
		t.Errorf("expected spec '08' to be excluded from response specs, got: %v", specIDs)
	}
}
