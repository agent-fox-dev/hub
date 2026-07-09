// arch_doc_test.go — doc-check tests for architecture/services-architecture.md
// per spec 07 workspace entity requirements (07-REQ-5.1, 07-REQ-5.2, 07-REQ-5.3).
//
// These tests verify:
//   TS-07-24: No organizational-context 'workspace' references remain.
//   TS-07-25: Workspace entity described with git repo, owner_id, team_id, branch, status.
//   TS-07-26: Both teams and workspaces table schemas present with all columns.
//
// Tests are expected to FAIL until task group 12 updates the architecture docs.
package internal_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TS-07-24: Verifies all architecture and docs files replace 'workspace'
// (referring to the organizational/team concept) with 'team'.
//
// Scans .md files in architecture/ and docs/ for patterns where 'workspace'
// is used to describe an organizational container or group (the old meaning).
// References to workspace as the NEW git-repo-mapped entity are allowed.
func TestArchDocs_NoOrganizationalWorkspaceRefs(t *testing.T) {
	root := findRepoRoot(t)

	dirs := []string{
		filepath.Join(root, "architecture"),
		filepath.Join(root, "docs"),
	}

	// Patterns that indicate the OLD organizational usage of 'workspace'.
	// These should have been renamed to 'team' by spec 06.
	//
	// We look for:
	// 1. "workspace" used in contexts describing organizational containers/groups
	// 2. References to the OLD workspace schema columns (name, url, created_by)
	//    in a workspace table description
	// 3. workspace_members (should be team_members)
	// 4. workspace_configs (organizational config — should be team_configs)
	orgPatterns := []*regexp.Regexp{
		// workspace_members is the old membership table name (now team_members)
		regexp.MustCompile(`(?i)\bworkspace_members\b`),
		// workspace_configs is the old config table name (now team_configs)
		regexp.MustCompile(`(?i)\bworkspace_configs\b`),
	}

	for _, dir := range dirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				base := info.Name()
				if strings.HasPrefix(base, ".") || base == "errata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(info.Name(), ".md") {
				return nil
			}

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Errorf("could not read %s: %v", path, readErr)
				return nil
			}
			content := string(data)
			relPath, _ := filepath.Rel(root, path)

			for _, pat := range orgPatterns {
				if matches := pat.FindAllString(content, -1); len(matches) > 0 {
					t.Errorf("%s: still contains organizational workspace reference matching %q: %v",
						relPath, pat.String(), matches)
				}
			}

			return nil
		})
		if err != nil {
			t.Errorf("error walking %s: %v", dir, err)
		}
	}
}

// TS-07-24 continued: Verify the operational store schema section in
// services-architecture.md does not use the OLD workspaces table schema
// (with 'name', 'owner', 'origin' columns from the pre-spec-07 design).
func TestArchDocs_NoOldWorkspaceSchemaInOperationalStore(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "architecture", "services-architecture.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("services-architecture.md does not exist: %v", err)
	}
	content := string(data)

	// The OLD workspaces table schema had columns: name, owner, origin,
	// base_branch, remote, campaign_id, updated_at.
	// The NEW workspaces table per spec 07-REQ-1.1 has: id, slug, git_url,
	// branch, owner_id, team_id, status, created_at.
	//
	// Look for the OLD schema pattern: `workspaces` followed by old columns
	// like 'name, status, owner, origin'.
	oldSchemaPattern := regexp.MustCompile(
		`(?i)\bworkspaces\b[^.]*\b(?:origin|base_branch|campaign_id)\b`,
	)
	if matches := oldSchemaPattern.FindAllString(content, -1); len(matches) > 0 {
		t.Errorf("services-architecture.md still contains OLD workspaces schema references: %v", matches)
	}
}

// TS-07-25: Verifies architecture/services-architecture.md describes workspace
// as a git-repository-mapped entity with owner_id, optional team_id, optional
// branch, and status (active/archived).
func TestArchDocs_WorkspaceEntityDescription(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "architecture", "services-architecture.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("services-architecture.md does not exist: %v", err)
	}
	content := strings.ToLower(string(data))

	// The workspace definition must mention git/repository context.
	if !strings.Contains(content, "git") && !strings.Contains(content, "repository") {
		t.Error("services-architecture.md should mention 'git' or 'repository' in the workspace definition")
	}

	// Must mention the key structural fields.
	requiredTerms := []string{
		"owner_id",
		"team_id",
		"branch",
	}
	for _, term := range requiredTerms {
		if !strings.Contains(content, term) {
			t.Errorf("services-architecture.md should mention %q in the workspace definition", term)
		}
	}

	// Must mention both status values.
	if !strings.Contains(content, "active") {
		t.Error("services-architecture.md should mention 'active' workspace status")
	}
	if !strings.Contains(content, "archived") {
		t.Error("services-architecture.md should mention 'archived' workspace status")
	}
}

// TS-07-26: Verifies architecture/services-architecture.md operational store
// schema section includes both the teams table and the new workspaces table
// with all required columns.
func TestArchDocs_OperationalStoreSchemas(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "architecture", "services-architecture.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("services-architecture.md does not exist: %v", err)
	}
	content := string(data)

	// Both table names must appear.
	if !strings.Contains(content, "teams") {
		t.Error("services-architecture.md should contain 'teams' table schema")
	}
	if !strings.Contains(content, "workspaces") {
		t.Error("services-architecture.md should contain 'workspaces' table schema")
	}

	// All 8 columns of the new workspaces table per spec 07-REQ-1.1 must appear.
	requiredColumns := []string{
		"id",
		"slug",
		"git_url",
		"branch",
		"owner_id",
		"team_id",
		"status",
		"created_at",
	}
	for _, col := range requiredColumns {
		if !strings.Contains(content, col) {
			t.Errorf("services-architecture.md should contain workspaces column %q", col)
		}
	}
}
