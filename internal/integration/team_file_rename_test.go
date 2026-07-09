package integration_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-06-21: Verifies that the Go source tree contains the renamed files:
// team_handler.go, teams.go, and team_members.go at their expected package
// paths.
// Requirement: 06-REQ-5.1
// ---------------------------------------------------------------------------

func TestFileExists_TeamHandler(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	path := filepath.Join(root, "internal", "handler", "team_handler.go")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("team_handler.go should exist at internal/handler/team_handler.go: %v", err)
	}
}

func TestFileExists_TeamsGo(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	path := filepath.Join(root, "internal", "store", "teams.go")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("teams.go should exist at internal/store/teams.go: %v", err)
	}
}

func TestFileExists_TeamMembersGo(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	path := filepath.Join(root, "internal", "store", "team_members.go")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("team_members.go should exist at internal/store/team_members.go: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TS-06-22: Verifies that the Go source tree contains no files named
// workspace_handler.go, workspaces.go, or workspace_members.go.
// Requirement: 06-REQ-5.2
// ---------------------------------------------------------------------------

func TestFileAbsent_WorkspaceHandler(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	path := filepath.Join(root, "internal", "handler", "workspace_handler.go")
	if _, err := os.Stat(path); err == nil {
		t.Error("workspace_handler.go should not exist (should be renamed to team_handler.go)")
	}
}

func TestFileAbsent_WorkspacesGo(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	path := filepath.Join(root, "internal", "store", "workspaces.go")
	if _, err := os.Stat(path); err == nil {
		t.Error("workspaces.go should not exist (should be renamed to teams.go)")
	}
}

func TestFileAbsent_WorkspaceMembersGo(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	path := filepath.Join(root, "internal", "store", "workspace_members.go")
	if _, err := os.Stat(path); err == nil {
		t.Error("workspace_members.go should not exist (should be renamed to team_members.go)")
	}
}

// ---------------------------------------------------------------------------
// TS-06-23: Verifies that the integration test directory contains
// team_handler_test.go and team_edge_test.go, and that all test assertions
// within reference team types and endpoints.
// Requirement: 06-REQ-5.3
// ---------------------------------------------------------------------------

func TestFileExists_TeamHandlerTest(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	path := filepath.Join(root, "internal", "integration", "team_handler_test.go")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("team_handler_test.go should exist at internal/integration/: %v", err)
	}
}

func TestFileExists_TeamEdgeTest(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	path := filepath.Join(root, "internal", "integration", "team_edge_test.go")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("team_edge_test.go should exist at internal/integration/: %v", err)
	}
}

func TestFileAbsent_WorkspaceHandlerTest(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	path := filepath.Join(root, "internal", "integration", "workspace_handler_test.go")
	if _, err := os.Stat(path); err == nil {
		t.Error("workspace_handler_test.go should not exist (should be renamed to team_handler_test.go)")
	}
}

func TestFileAbsent_WorkspaceEdgeTest(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	path := filepath.Join(root, "internal", "integration", "workspace_edge_test.go")
	if _, err := os.Stat(path); err == nil {
		t.Error("workspace_edge_test.go should not exist (should be renamed to team_edge_test.go)")
	}
}

func TestRenamedTestFiles_NoWorkspaceReferences(t *testing.T) {
	root := findIntegrationProjectRoot(t)

	testFiles := []string{
		filepath.Join(root, "internal", "integration", "team_handler_test.go"),
		filepath.Join(root, "internal", "integration", "team_edge_test.go"),
	}

	workspacePatterns := []*regexp.Regexp{
		regexp.MustCompile(`/api/v1/workspaces`),
		regexp.MustCompile(`\bWorkspace[A-Z]`),
	}

	for _, filePath := range testFiles {
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Errorf("cannot read %s for content check: %v", filepath.Base(filePath), err)
			continue
		}

		src := string(content)
		base := filepath.Base(filePath)

		for _, pat := range workspacePatterns {
			matches := pat.FindAllString(src, -1)
			if len(matches) > 0 {
				t.Errorf("%s should not contain workspace references matching %s, found: %v",
					base, pat.String(), matches)
			}
		}

		// Also check for workspace_id references (snake_case).
		if strings.Contains(src, "workspace_id") {
			t.Errorf("%s should not contain 'workspace_id' references", base)
		}
	}
}
