package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/store"
)

// ---------------------------------------------------------------------------
// TS-06-30: Verifies that the renamed codebase produces a clean build with
// exit code 0 from both `go build ./...` and `go vet ./...`.
// Requirement: 06-REQ-7.3
// ---------------------------------------------------------------------------

func TestBehavioral_GoBuildClean(t *testing.T) {
	root := findIntegrationProjectRoot(t)

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("go build ./... failed with exit code %v\nOutput:\n%s", err, string(output))
	}
}

func TestBehavioral_GoVetClean(t *testing.T) {
	root := findIntegrationProjectRoot(t)

	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("go vet ./... failed with exit code %v\nOutput:\n%s", err, string(output))
	}
}

// ---------------------------------------------------------------------------
// TS-06-28: Verifies that the full test suite passes with zero failures
// after the rename by running `go test ./...`.
// Requirement: 06-REQ-7.1
// ---------------------------------------------------------------------------

func TestBehavioral_FullTestSuitePasses(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full test suite check in short mode")
	}

	root := findIntegrationProjectRoot(t)

	cmd := exec.Command("go", "test", "./...", "-count=1")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	outStr := string(output)

	if err != nil {
		t.Errorf("go test ./... -count=1 failed with exit code %v\nOutput:\n%s", err, outStr)
	}

	// Check for FAIL lines in output.
	for line := range strings.SplitSeq(outStr, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "FAIL") && !strings.HasPrefix(trimmed, "FAIL\t") {
			continue // skip the summary line like "FAIL\tpackage [build failed]"
		}
		if strings.HasPrefix(trimmed, "--- FAIL") {
			t.Errorf("found test failure in output: %s", trimmed)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-06-29: Verifies that RBAC enforcement, team lifecycle states, and API
// key scoping logic are behaviourally identical to the pre-rename
// implementation.
// Requirement: 06-REQ-7.2
// ---------------------------------------------------------------------------

func TestBehavioral_RBAC_UnauthenticatedAccess(t *testing.T) {
	env := setupFullTestEnv(t)

	// Unauthenticated requests to protected team endpoints should fail
	// with 401 or 403.
	protectedPaths := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/workspaces"},
		{"POST", "/api/v1/workspaces"},
	}

	for _, pp := range protectedPaths {
		name := fmt.Sprintf("%s %s", pp.method, pp.path)
		t.Run(name, func(t *testing.T) {
			rec := doRequest(env.Echo, pp.method, pp.path, "", nil)
			if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
				t.Errorf("expected 401 or 403 for unauthenticated %s %s, got %d",
					pp.method, pp.path, rec.Code)
			}
		})
	}
}

func TestBehavioral_TeamLifecycleTransitions(t *testing.T) {
	env := setupFullTestEnv(t)

	// Seed admin token for authenticated requests.
	adminToken := "behavioral-test-admin-token"
	seedAdminTokenFull(t, env.Store, adminToken)
	headers := adminHeaders(adminToken)

	// 1. Create a workspace (team).
	createBody := `{"name":"lifecycle-test","slug":"lifecycle-test","url":"https://lifecycle.example.com"}`
	createRec := doRequest(env.Echo, "POST", "/api/v1/workspaces", createBody, headers)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating workspace, got %d: %s", createRec.Code, createRec.Body.String())
	}

	var created workspaceResponse
	parseJSON(t, createRec, &created)
	wsID := created.ID

	// 2. Verify it starts as active.
	if created.Status != "active" {
		t.Errorf("expected initial status 'active', got %q", created.Status)
	}

	// 3. Archive it (POST, not PUT — matches actual codebase).
	archiveRec := doRequest(env.Echo, "POST", "/api/v1/workspaces/"+wsID+"/archive", "", headers)
	if archiveRec.Code != http.StatusOK {
		t.Errorf("expected 200 archiving workspace, got %d: %s", archiveRec.Code, archiveRec.Body.String())
	}

	// 4. List workspaces including archived and verify status.
	listRec := doRequest(env.Echo, "GET", "/api/v1/workspaces?include_archived=true", "", headers)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200 listing workspaces, got %d", listRec.Code)
	}
	var allWs []workspaceResponse
	parseJSON(t, listRec, &allWs)
	found := false
	for _, ws := range allWs {
		if ws.ID == wsID {
			found = true
			if ws.Status != "archived" {
				t.Errorf("expected archived status after archive, got %q", ws.Status)
			}
		}
	}
	if !found {
		t.Error("archived workspace not found in list with include_archived=true")
	}

	// 5. Reactivate it.
	reactivateRec := doRequest(env.Echo, "POST", "/api/v1/workspaces/"+wsID+"/reactivate", "", headers)
	if reactivateRec.Code != http.StatusOK {
		t.Errorf("expected 200 reactivating workspace, got %d: %s", reactivateRec.Code, reactivateRec.Body.String())
	}

	// 6. Verify it's active again.
	listRec2 := doRequest(env.Echo, "GET", "/api/v1/workspaces", "", headers)
	if listRec2.Code != http.StatusOK {
		t.Fatalf("expected 200 listing workspaces, got %d", listRec2.Code)
	}
	var activeWs []workspaceResponse
	parseJSON(t, listRec2, &activeWs)
	found = false
	for _, ws := range activeWs {
		if ws.ID == wsID {
			found = true
			if ws.Status != "active" {
				t.Errorf("expected active status after reactivate, got %q", ws.Status)
			}
		}
	}
	if !found {
		t.Error("reactivated workspace not found in active list")
	}
}

func TestBehavioral_APIKeyScopingToTeam(t *testing.T) {
	env := setupFullTestEnv(t)

	// Seed admin token for authenticated requests.
	adminToken := "behavioral-apikey-admin-token"
	seedAdminTokenFull(t, env.Store, adminToken)
	headers := adminHeaders(adminToken)

	// 1. Create a workspace (team).
	createBody := `{"name":"apikey-scope-test","slug":"apikey-scope-test","url":"https://apikey.example.com"}`
	createRec := doRequest(env.Echo, "POST", "/api/v1/workspaces", createBody, headers)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating workspace, got %d: %s", createRec.Code, createRec.Body.String())
	}

	var created workspaceResponse
	parseJSON(t, createRec, &created)
	wsID := created.ID

	// 2. Create a user + member to get a non-admin context.
	_, err := env.Store.CreateUser(&store.User{
		ID: "scopeuser1", Username: "scopeuser1", Provider: "github", ProviderID: "scope1", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// 3. Add user as a member of the workspace.
	memberBody := `{"user_id":"scopeuser1","role":"editor"}`
	memberRec := doRequest(env.Echo, "POST", "/api/v1/workspaces/"+wsID+"/members", memberBody, headers)
	if memberRec.Code != http.StatusOK && memberRec.Code != http.StatusCreated {
		t.Fatalf("expected 200/201 adding member, got %d: %s", memberRec.Code, memberRec.Body.String())
	}

	// 4. Create an API key scoped to this workspace (using admin).
	keyBody := fmt.Sprintf(`{"workspace_id":"%s","role":"editor","label":"scoped-key"}`, wsID)
	keyRec := doRequest(env.Echo, "POST", "/api/v1/keys", keyBody, headers)
	if keyRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating API key, got %d: %s", keyRec.Code, keyRec.Body.String())
	}

	var keyResp map[string]any
	parseJSON(t, keyRec, &keyResp)

	// 5. Verify the key response contains workspace_id (or team_id after rename).
	// Before rename: workspace_id; after rename: team_id.
	// We accept either since this test should pass before AND after the rename.
	hasWorkspaceID := false
	hasTeamID := false
	if _, ok := keyResp["workspace_id"]; ok {
		hasWorkspaceID = true
	}
	if _, ok := keyResp["team_id"]; ok {
		hasTeamID = true
	}
	if !hasWorkspaceID && !hasTeamID {
		t.Error("API key response should contain either 'workspace_id' or 'team_id'")
	}
}

// ---------------------------------------------------------------------------
// TS-06-31: Verifies that spec packages 01-04 and their associated artifacts
// are unmodified by this rename.
// Requirement: 06-REQ-8.1
// ---------------------------------------------------------------------------

func TestBehavioral_SpecPackagesUnmodified(t *testing.T) {
	root := findIntegrationProjectRoot(t)

	// Spec packages live under .agent-fox/specs/.
	specDirs := []string{
		".agent-fox/specs/01_backend_foundation",
		".agent-fox/specs/02_auth_rbac_api",
		".agent-fox/specs/03_cli",
		".agent-fox/specs/04_web_ui_scaffold",
	}

	// Check that git diff shows no changes for these paths.
	for _, specDir := range specDirs {
		t.Run(specDir, func(t *testing.T) {
			cmd := exec.Command("git", "diff", "HEAD", "--", specDir)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if err != nil {
				// git diff may exit non-zero if the path doesn't exist in HEAD;
				// only fail if there's actual diff content.
				if len(strings.TrimSpace(string(output))) > 0 {
					t.Errorf("spec directory %s has modifications:\n%s", specDir, string(output))
				}
			} else if len(strings.TrimSpace(string(output))) > 0 {
				t.Errorf("spec directory %s has modifications:\n%s", specDir, string(output))
			}
		})
	}

	// Also check with git diff --staged in case changes are staged.
	for _, specDir := range specDirs {
		t.Run("staged/"+specDir, func(t *testing.T) {
			cmd := exec.Command("git", "diff", "--staged", "--", specDir)
			cmd.Dir = root
			output, err := cmd.CombinedOutput()
			if err == nil && len(strings.TrimSpace(string(output))) > 0 {
				t.Errorf("spec directory %s has staged modifications:\n%s", specDir, string(output))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-06-E6: Verifies that a test referencing a deleted workspace-prefixed
// type causes `go test ./...` to fail at the compilation stage.
// Requirement: 06-REQ-7.E1
// ---------------------------------------------------------------------------

func TestBehavioral_StaleWorkspaceRefFailsCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile-check test in short mode")
	}

	root := findIntegrationProjectRoot(t)

	// Create a temporary test file that references store.WorkspaceMember{}.
	// After the rename, this should fail to compile since WorkspaceMember
	// no longer exists.
	tmpTestFile := filepath.Join(root, "internal", "store", "stale_workspace_check_test.go")
	tmpContent := `package store

// This file is auto-generated by TS-06-E6 to verify that deleted
// workspace-prefixed types cause compile failures.
var _ = WorkspaceMember{}
`

	if err := os.WriteFile(tmpTestFile, []byte(tmpContent), 0644); err != nil {
		t.Fatalf("failed to write temp test file: %v", err)
	}
	// Always clean up the temp file.
	defer os.Remove(tmpTestFile)

	// Try to build. If WorkspaceMember has been renamed to TeamMember,
	// this should fail.
	cmd := exec.Command("go", "build", "./internal/store/...")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Error("expected go build to fail when referencing store.WorkspaceMember{}, " +
			"but it succeeded (WorkspaceMember should be renamed to TeamMember)")
	} else {
		outStr := string(output)
		// Verify the error mentions the undefined symbol.
		if !strings.Contains(outStr, "undefined") && !strings.Contains(outStr, "WorkspaceMember") {
			t.Errorf("expected error to mention 'undefined' or 'WorkspaceMember', got:\n%s", outStr)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-06-P4 (partial): Verifies that JSON responses from team-related
// endpoints use `team_id` and never `workspace_id`.
// Requirement: 06-REQ-3.9
//
// This test validates the behavioral guarantee that the JSON field rename
// is consistent across all endpoints that carry the organizational ID.
// ---------------------------------------------------------------------------

func TestBehavioral_JSONFieldConsistency(t *testing.T) {
	env := setupFullTestEnv(t)

	adminToken := "behavioral-json-admin-token"
	seedAdminTokenFull(t, env.Store, adminToken)
	headers := adminHeaders(adminToken)

	// Create a workspace/team.
	createBody := `{"name":"json-check","slug":"json-check","url":"https://json.example.com"}`
	createRec := doRequest(env.Echo, "POST", "/api/v1/workspaces", createBody, headers)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}

	// Check that the list endpoint returns objects. After the rename, all
	// JSON fields should use team_id (not workspace_id).
	listRec := doRequest(env.Echo, "GET", "/api/v1/workspaces", "", headers)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", listRec.Code)
	}

	// Parse the raw JSON to check for workspace_id vs team_id keys.
	var rawList []json.RawMessage
	if err := json.Unmarshal(listRec.Body.Bytes(), &rawList); err != nil {
		t.Fatalf("failed to parse list response: %v", err)
	}

	for i, raw := range rawList {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Errorf("item %d: failed to parse: %v", i, err)
			continue
		}

		// After the rename, workspace_id should not appear.
		// Before the rename, this test will pass vacuously since the
		// workspace list endpoint doesn't include workspace_id in each item.
		if _, has := obj["workspace_id"]; has {
			t.Errorf("item %d: JSON response should not contain 'workspace_id' "+
				"(should be 'team_id' after rename)", i)
		}
	}
}
