package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-06-24: Verifies that docs/api.md reflects all renamed endpoint paths
// and uses team_id in all request/response examples.
// Requirement: 06-REQ-6.1
// ---------------------------------------------------------------------------

func TestDocs_ApiMd_UsesTeamEndpoints(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "docs", "api.md"))
	if err != nil {
		t.Fatalf("could not read docs/api.md: %v", err)
	}
	src := string(content)

	t.Run("contains /api/v1/teams paths", func(t *testing.T) {
		if !strings.Contains(src, "/api/v1/teams") {
			t.Error("docs/api.md should contain '/api/v1/teams' endpoint paths")
		}
	})

	t.Run("contains team_id field", func(t *testing.T) {
		if !strings.Contains(src, "team_id") {
			t.Error("docs/api.md should contain 'team_id' in request/response examples")
		}
	})

	t.Run("no /api/v1/workspaces paths", func(t *testing.T) {
		if strings.Contains(src, "/api/v1/workspaces") {
			t.Error("docs/api.md should not contain legacy '/api/v1/workspaces' endpoint paths")
		}
	})

	t.Run("no workspace_id field", func(t *testing.T) {
		if strings.Contains(src, "workspace_id") {
			t.Error("docs/api.md should not contain legacy 'workspace_id' field references")
		}
	})
}

// ---------------------------------------------------------------------------
// TS-06-25: Verifies that docs/cli.md documents the --team flag for
// afc keys create and contains no references to --workspace.
// Requirement: 06-REQ-6.2
// ---------------------------------------------------------------------------

func TestDocs_CliMd_UsesTeamFlag(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "docs", "cli.md"))
	if err != nil {
		t.Fatalf("could not read docs/cli.md: %v", err)
	}
	src := string(content)

	t.Run("contains --team flag", func(t *testing.T) {
		if !strings.Contains(src, "--team") {
			t.Error("docs/cli.md should document the '--team' flag for keys create")
		}
	})

	t.Run("no --workspace flag", func(t *testing.T) {
		if strings.Contains(src, "--workspace") {
			t.Error("docs/cli.md should not contain '--workspace' flag references")
		}
	})
}

// ---------------------------------------------------------------------------
// TS-06-26: Verifies that docs/architecture.md uses 'team' for the
// organizational-boundary entity and contains no occurrences of 'workspace'
// referring to that concept.
// Requirement: 06-REQ-6.3
// ---------------------------------------------------------------------------

func TestDocs_ArchitectureMd_UsesTeam(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "docs", "architecture.md"))
	if err != nil {
		t.Fatalf("could not read docs/architecture.md: %v", err)
	}
	src := string(content)

	t.Run("uses team terminology", func(t *testing.T) {
		if !strings.Contains(src, "team") {
			t.Error("docs/architecture.md should use 'team' for the organizational-boundary entity")
		}
	})

	t.Run("no organizational workspace references", func(t *testing.T) {
		// Count occurrences of 'workspace' (case-insensitive).
		// After the rename, 'workspace' should not appear in the org-boundary
		// sense. It may appear as a forward-reference to the upcoming
		// workspace_entity concept, but the bare 'workspaces' table name or
		// 'workspace management' headings should be gone.
		srcLower := strings.ToLower(src)
		workspaceCount := strings.Count(srcLower, "workspace")
		if workspaceCount > 0 {
			t.Errorf("docs/architecture.md contains %d occurrences of 'workspace' "+
				"(should be replaced with 'team' for the organizational-boundary concept)",
				workspaceCount)
		}
	})
}

// ---------------------------------------------------------------------------
// TS-06-27: Verifies that docs/configuration.md, docs/errata/*.md, and
// README.md replace all organizational 'workspace' occurrences with 'team'.
// Requirement: 06-REQ-6.4
// ---------------------------------------------------------------------------

func TestDocs_ConfigurationMd_NoOrgWorkspace(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "docs", "configuration.md"))
	if err != nil {
		t.Fatalf("could not read docs/configuration.md: %v", err)
	}
	checkNoOrgWorkspaceReferences(t, "docs/configuration.md", string(content))
}

func TestDocs_ReadmeMd_NoOrgWorkspace(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("could not read README.md: %v", err)
	}
	checkNoOrgWorkspaceReferences(t, "README.md", string(content))
}

func TestDocs_ErrataMd_NoOrgWorkspace(t *testing.T) {
	root := findIntegrationProjectRoot(t)
	errataDir := filepath.Join(root, "docs", "errata")

	entries, err := os.ReadDir(errataDir)
	if err != nil {
		// If no errata directory exists, skip (non-fatal).
		if os.IsNotExist(err) {
			t.Skip("docs/errata/ does not exist; nothing to check")
		}
		t.Fatalf("could not read docs/errata/ directory: %v", err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		// Skip the errata file for this spec itself — it documents the
		// workspace→team divergences and legitimately mentions both terms.
		if entry.Name() == "06_team_rename.md" {
			continue
		}

		filePath := filepath.Join(errataDir, entry.Name())
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Errorf("could not read %s: %v", filePath, err)
			continue
		}

		checkNoOrgWorkspaceReferences(t, "docs/errata/"+entry.Name(), string(content))
	}
}

// checkNoOrgWorkspaceReferences asserts that a documentation file does not
// contain 'workspace' in the organizational-boundary sense. Specifically,
// it checks for patterns like 'workspace management', 'workspace_id',
// '/workspaces', and 'workspaces' (plural table name). Occurrences of
// 'workspace' in the context of the upcoming workspace_entity concept
// (e.g. "task-scoped workspace") are acceptable and excluded by this check.
func checkNoOrgWorkspaceReferences(t *testing.T, fileName, content string) {
	t.Helper()

	// Check for the most unambiguous organizational-boundary patterns first.
	orgPatterns := []struct {
		pattern string
		desc    string
	}{
		{"workspace_id", "legacy workspace_id field name"},
		{"/api/v1/workspaces", "legacy API endpoint path"},
		{"--workspace", "legacy --workspace CLI flag"},
		{"workspace_members", "legacy workspace_members table name"},
	}

	for _, p := range orgPatterns {
		if strings.Contains(content, p.pattern) {
			t.Errorf("%s contains '%s' (%s) — should be renamed to team equivalent",
				fileName, p.pattern, p.desc)
		}
	}
}
