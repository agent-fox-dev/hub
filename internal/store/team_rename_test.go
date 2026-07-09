package store

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// findStoreProjectRoot walks up from the current directory (internal/store/)
// to find the directory containing go.mod.
func findStoreProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// ---------------------------------------------------------------------------
// TS-06-4: Verifies that the store package exports store.Team and
// store.TeamMember structs with correct fields, and that APIKey.TeamID
// replaces the old WorkspaceID field.
// Requirement: 06-REQ-2.1
// ---------------------------------------------------------------------------

func TestAPIKey_HasTeamIDField(t *testing.T) {
	apiKeyType := reflect.TypeFor[APIKey]()

	t.Run("TeamID field exists", func(t *testing.T) {
		_, ok := apiKeyType.FieldByName("TeamID")
		if !ok {
			t.Error("store.APIKey should have TeamID field")
		}
	})

	t.Run("no WorkspaceID field", func(t *testing.T) {
		_, ok := apiKeyType.FieldByName("WorkspaceID")
		if ok {
			t.Error("store.APIKey should not have WorkspaceID field (renamed to TeamID)")
		}
	})
}

func TestAPIKey_TeamIDJsonTag(t *testing.T) {
	apiKeyType := reflect.TypeFor[APIKey]()

	field, ok := apiKeyType.FieldByName("TeamID")
	if !ok {
		t.Fatal("store.APIKey should have TeamID field; cannot check json tag")
	}

	tag := field.Tag.Get("json")
	if !strings.HasPrefix(tag, "team_id") {
		t.Errorf("APIKey.TeamID json tag should start with 'team_id', got %q", tag)
	}
}

func TestTeamStructDefined(t *testing.T) {
	// Read teams.go from the store package directory (test CWD = internal/store/).
	content, err := os.ReadFile("teams.go")
	if err != nil {
		t.Fatalf("teams.go not found (expected renamed from workspaces.go): %v", err)
	}

	src := string(content)

	t.Run("Team struct defined", func(t *testing.T) {
		if !strings.Contains(src, "type Team struct") {
			t.Error("teams.go should define 'type Team struct'")
		}
	})

	t.Run("no Workspace struct", func(t *testing.T) {
		if strings.Contains(src, "type Workspace struct") {
			t.Error("teams.go should not define 'type Workspace struct' (renamed to Team)")
		}
	})

	t.Run("Team has standard fields", func(t *testing.T) {
		// Verify the Team struct has the expected fields.
		for _, field := range []string{"ID", "Name", "Slug", "URL", "Status", "CreatedAt"} {
			re := regexp.MustCompile(field + `\s+string`)
			if !re.MatchString(src) {
				t.Errorf("Team struct should have field %s of type string", field)
			}
		}
	})
}

func TestTeamMemberStructDefined(t *testing.T) {
	content, err := os.ReadFile("team_members.go")
	if err != nil {
		t.Fatalf("team_members.go not found (expected renamed from workspace_members.go): %v", err)
	}

	src := string(content)

	t.Run("TeamMember struct defined", func(t *testing.T) {
		if !strings.Contains(src, "type TeamMember struct") {
			t.Error("team_members.go should define 'type TeamMember struct'")
		}
	})

	t.Run("no WorkspaceMember struct", func(t *testing.T) {
		if strings.Contains(src, "type WorkspaceMember struct") {
			t.Error("team_members.go should not define 'type WorkspaceMember struct'")
		}
	})

	t.Run("TeamMember has TeamID field", func(t *testing.T) {
		re := regexp.MustCompile(`TeamID\s+string`)
		if !re.MatchString(src) {
			t.Error("TeamMember struct should have TeamID field of type string")
		}
	})

	t.Run("TeamMember has no WorkspaceID field", func(t *testing.T) {
		re := regexp.MustCompile(`WorkspaceID\s+string`)
		if re.MatchString(src) {
			t.Error("TeamMember struct should not have WorkspaceID field")
		}
	})

	t.Run("TeamMember has team_id json tag", func(t *testing.T) {
		if !strings.Contains(src, `"team_id"`) {
			t.Error("TeamMember should have json tag 'team_id'")
		}
		if strings.Contains(src, `"workspace_id"`) {
			t.Error("TeamMember should not have json tag 'workspace_id'")
		}
	})
}

func TestUserWithMemberships_UsesTeamMember(t *testing.T) {
	// Read store.go to check that UserWithMemberships references TeamMember,
	// not WorkspaceMember.
	content, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("failed to read store.go: %v", err)
	}
	src := string(content)

	t.Run("references TeamMember", func(t *testing.T) {
		if !strings.Contains(src, "TeamMember") {
			t.Error("UserWithMemberships should reference TeamMember")
		}
	})

	t.Run("no WorkspaceMember reference", func(t *testing.T) {
		if strings.Contains(src, "WorkspaceMember") {
			t.Error("UserWithMemberships should not reference WorkspaceMember")
		}
	})
}

// ---------------------------------------------------------------------------
// TS-06-5: Verifies that the store package exports all required CRUD methods
// with Team-prefixed names and identical behaviour to their
// Workspace-prefixed predecessors.
// Requirement: 06-REQ-2.2
//
// Note: The spec lists ArchiveTeam and ReactivateTeam as store methods, but
// per reviewer finding, these do not exist as store-level methods. The handler
// uses GetTeamByID + UpdateTeam instead. We test for UpdateTeam (the actual
// renamed method) rather than ArchiveTeam/ReactivateTeam.
// See also: docs/errata for spec divergences.
// ---------------------------------------------------------------------------

func TestStoreInterface_HasTeamMethods(t *testing.T) {
	storeType := reflect.TypeFor[Store]()

	// All workspace-prefixed methods from the current Store interface,
	// expected to be renamed to team-prefixed equivalents.
	expectedMethods := []string{
		"CreateTeam",
		"GetTeamByID",
		"GetTeamBySlug",
		"UpdateTeam",
		"DeleteTeam",
		"ListTeams",
		"DeleteTeamWithCascade",
		"CreateTeamMember",
		"GetTeamMember",
		"ListTeamMembers",
		"DeleteTeamMember",
		"UpsertTeamMember",
		"CountAPIKeysByTeamID",
	}

	for _, name := range expectedMethods {
		t.Run(name, func(t *testing.T) {
			if _, ok := storeType.MethodByName(name); !ok {
				t.Errorf("Store interface should have method %s", name)
			}
		})
	}
}

func TestStoreInterface_NoWorkspaceMethods(t *testing.T) {
	storeType := reflect.TypeFor[Store]()

	// All legacy workspace-prefixed methods that should be removed after rename.
	legacyMethods := []string{
		"CreateWorkspace",
		"GetWorkspaceByID",
		"GetWorkspaceBySlug",
		"UpdateWorkspace",
		"DeleteWorkspace",
		"ListWorkspaces",
		"DeleteWorkspaceWithCascade",
		"CreateWorkspaceMember",
		"GetWorkspaceMember",
		"ListWorkspaceMembers",
		"DeleteWorkspaceMember",
		"UpsertWorkspaceMember",
		"CountAPIKeysByWorkspaceID",
	}

	for _, name := range legacyMethods {
		t.Run(name, func(t *testing.T) {
			if _, ok := storeType.MethodByName(name); ok {
				t.Errorf("Store interface should not have legacy method %s (should be renamed)", name)
			}
		})
	}
}

func TestStoreInterface_NonWorkspaceMethodsUnchanged(t *testing.T) {
	// Verify that non-workspace methods on the Store interface are still present
	// (they should not have been affected by the rename).
	storeType := reflect.TypeFor[Store]()

	unchangedMethods := []string{
		"CreateUser",
		"GetUserByID",
		"GetUserByUsername",
		"GetUserByProviderID",
		"UpdateUser",
		"DeleteUser",
		"ListUsers",
		"CountUsers",
		"CreateAPIKey",
		"GetAPIKeyByID",
		"GetAPIKeyByKeyID",
		"RevokeAPIKey",
		"DeleteAPIKey",
		"ListAPIKeys",
		"ListAPIKeysByUserID",
		"UpdateAPIKeyHash",
		"CreateAdminToken",
		"GetAdminToken",
		"GetAdminTokenByHash",
		"UpdateAdminToken",
		"DeleteAdminToken",
	}

	for _, name := range unchangedMethods {
		t.Run(name, func(t *testing.T) {
			if _, ok := storeType.MethodByName(name); !ok {
				t.Errorf("Store interface should still have non-workspace method %s", name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-06-6: Verifies that the Go codebase contains no exported or unexported
// identifiers using the Workspace or workspace prefix for the
// organizational-boundary concept.
// Requirement: 06-REQ-2.3
//
// TS-06-E3: Verifies that a grep-based CI check reports a violation when
// any residual workspace-prefixed identifier exists.
// Requirement: 06-REQ-2.E2
// ---------------------------------------------------------------------------

func TestNoWorkspacePrefixedIdentifiers(t *testing.T) {
	root, err := findStoreProjectRoot()
	if err != nil {
		t.Fatalf("could not find project root: %v", err)
	}

	// Patterns for workspace-prefixed identifiers (organizational-boundary concept).
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bWorkspace[A-Z]`), // Exported: WorkspaceHandler, WorkspaceMember, etc.
		regexp.MustCompile(`\bworkspace_id\b`), // Snake-case field/column name
		regexp.MustCompile(`\bworkspace[A-Z]`), // Unexported camelCase: workspaceCount, etc.
	}

	// Directories to exclude from the scan (spec packages, agent-fox metadata).
	excludeDirs := map[string]bool{
		".specs":     true,
		".agent-fox": true,
		"vendor":     true,
	}

	var violations []string

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip excluded directories.
		if info.IsDir() {
			relDir, _ := filepath.Rel(root, path)
			for part := range strings.SplitSeq(relDir, string(filepath.Separator)) {
				if excludeDirs[part] {
					return filepath.SkipDir
				}
			}
			return nil
		}

		// Only scan .go files.
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		// Skip test files for the rename tests themselves (they legitimately
		// reference workspace names in assertion strings).
		base := filepath.Base(path)
		if base == "team_rename_test.go" || base == "schema_rename_test.go" ||
			base == "team_handler_rename_test.go" || base == "team_context_rename_test.go" {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		src := string(content)

		relPath, _ := filepath.Rel(root, path)
		for _, pat := range patterns {
			matches := pat.FindAllString(src, -1)
			for _, m := range matches {
				violations = append(violations, relPath+": "+m)
			}
		}

		return nil
	})

	if err != nil {
		t.Fatalf("error walking source tree: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("found %d workspace-prefixed identifiers that should be renamed to team:\n%s",
			len(violations), strings.Join(violations, "\n"))
	}
}

func TestGrepDetectsWorkspaceViolation(t *testing.T) {
	// Negative test: verify the grep pattern correctly catches workspace-
	// prefixed identifiers when they are present.
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bWorkspace[A-Z]`),
		regexp.MustCompile(`\bworkspace_id\b`),
		regexp.MustCompile(`\bworkspace[A-Z]`),
	}

	testCases := []struct {
		name    string
		content string
		want    bool // true = should be detected
	}{
		{"exported WorkspaceHandler", "type WorkspaceHandler struct", true},
		{"exported WorkspaceMember", "type WorkspaceMember struct", true},
		{"snake_case workspace_id", `workspace_id TEXT`, true},
		{"unexported workspaceCount", "var workspaceCount = 0", true},
		{"team is OK", "type TeamHandler struct", false},
		{"team_id is OK", `team_id TEXT`, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			found := false
			for _, pat := range patterns {
				if pat.MatchString(tc.content) {
					found = true
					break
				}
			}
			if found != tc.want {
				if tc.want {
					t.Errorf("patterns should detect workspace identifier in %q", tc.content)
				} else {
					t.Errorf("patterns should NOT detect workspace identifier in %q", tc.content)
				}
			}
		})
	}
}
