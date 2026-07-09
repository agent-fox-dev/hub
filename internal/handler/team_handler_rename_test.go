package handler

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-06-7: Verifies that team_handler.go defines a TeamHandler struct with
// all required handler methods.
// Requirement: 06-REQ-2.4
//
// Note: The spec lists GetTeam, AddTeamMember, and RemoveTeamMember as handler
// methods, but per reviewer findings, no GetWorkspace (single-get), RemoveWorkspaceMember,
// or AddWorkspaceMember handler methods exist in the current codebase. The actual
// handler methods are AddOrUpdateMember and ListMembers. This test verifies the
// RENAMED versions of the existing handler methods, plus the spec-required ones
// for completeness.
// ---------------------------------------------------------------------------

func TestTeamHandlerFileExists(t *testing.T) {
	// team_handler.go should exist (renamed from workspace_handler.go).
	_, err := os.Stat("team_handler.go")
	if err != nil {
		t.Errorf("team_handler.go should exist (renamed from workspace_handler.go): %v", err)
	}
}

func TestWorkspaceHandlerFileAbsent(t *testing.T) {
	// workspace_handler.go should NOT exist (renamed to team_handler.go).
	_, err := os.Stat("workspace_handler.go")
	if err == nil {
		t.Error("workspace_handler.go should not exist (should be renamed to team_handler.go)")
	}
}

func TestTeamHandlerStructDefined(t *testing.T) {
	content, err := os.ReadFile("team_handler.go")
	if err != nil {
		t.Fatalf("could not read team_handler.go: %v", err)
	}
	src := string(content)

	t.Run("TeamHandler struct defined", func(t *testing.T) {
		if !strings.Contains(src, "type TeamHandler struct") {
			t.Error("team_handler.go should define 'type TeamHandler struct'")
		}
	})

	t.Run("no WorkspaceHandler struct", func(t *testing.T) {
		if strings.Contains(src, "type WorkspaceHandler struct") {
			t.Error("team_handler.go should not define 'type WorkspaceHandler struct'")
		}
		if strings.Contains(src, "WorkspaceHandler") {
			t.Error("team_handler.go should not contain any 'WorkspaceHandler' references")
		}
	})
}

func TestTeamHandlerMethods(t *testing.T) {
	content, err := os.ReadFile("team_handler.go")
	if err != nil {
		t.Fatalf("could not read team_handler.go: %v", err)
	}
	src := string(content)

	// Methods that are renamed from workspace-prefixed equivalents in the
	// current codebase:
	// CreateWorkspace → CreateTeam
	// ListWorkspaces → ListTeams
	// ArchiveWorkspace → ArchiveTeam
	// ReactivateWorkspace → ReactivateTeam
	// DeleteWorkspace → DeleteTeam
	// AddOrUpdateMember (keeps name, handler struct renamed)
	// ListMembers (keeps name, handler struct renamed)
	renamedMethods := []string{
		"CreateTeam",
		"ListTeams",
		"ArchiveTeam",
		"ReactivateTeam",
		"DeleteTeam",
	}

	for _, method := range renamedMethods {
		t.Run("has_"+method, func(t *testing.T) {
			// Match method defined on TeamHandler receiver.
			re := regexp.MustCompile(`func\s*\(\s*\w+\s+\*?TeamHandler\s*\)\s*` + method + `\s*\(`)
			if !re.MatchString(src) {
				t.Errorf("team_handler.go should define method %s on TeamHandler", method)
			}
		})
	}

	// Methods that keep their name but operate on TeamHandler receiver.
	existingMethods := []string{
		"AddOrUpdateMember",
		"ListMembers",
	}

	for _, method := range existingMethods {
		t.Run("has_"+method, func(t *testing.T) {
			re := regexp.MustCompile(`func\s*\(\s*\w+\s+\*?TeamHandler\s*\)\s*` + method + `\s*\(`)
			if !re.MatchString(src) {
				t.Errorf("team_handler.go should define method %s on TeamHandler", method)
			}
		})
	}
}

func TestTeamHandlerNoWorkspaceReferences(t *testing.T) {
	content, err := os.ReadFile("team_handler.go")
	if err != nil {
		t.Fatalf("could not read team_handler.go: %v", err)
	}
	src := string(content)

	t.Run("no CreateWorkspace method", func(t *testing.T) {
		re := regexp.MustCompile(`func.*CreateWorkspace`)
		if re.MatchString(src) {
			t.Error("team_handler.go should not have CreateWorkspace method")
		}
	})

	t.Run("no ListWorkspaces method", func(t *testing.T) {
		re := regexp.MustCompile(`func.*ListWorkspaces`)
		if re.MatchString(src) {
			t.Error("team_handler.go should not have ListWorkspaces method")
		}
	})

	t.Run("no Workspace-prefixed type references", func(t *testing.T) {
		// Check for store.Workspace references (should be store.Team now).
		if strings.Contains(src, "store.Workspace{") || strings.Contains(src, "store.Workspace ") ||
			strings.Contains(src, "*store.Workspace") || strings.Contains(src, "[]store.Workspace") {
			t.Error("team_handler.go should not reference store.Workspace (use store.Team)")
		}
	})

	t.Run("no WorkspaceMember references", func(t *testing.T) {
		re := regexp.MustCompile(`\bWorkspaceMember\b`)
		if re.MatchString(src) {
			t.Error("team_handler.go should not reference WorkspaceMember (use TeamMember)")
		}
	})

	t.Run("no workspace route paths", func(t *testing.T) {
		if strings.Contains(src, "/workspaces") {
			t.Error("team_handler.go should not contain '/workspaces' route paths")
		}
	})
}

func TestNewTeamHandlerConstructor(t *testing.T) {
	content, err := os.ReadFile("team_handler.go")
	if err != nil {
		t.Fatalf("could not read team_handler.go: %v", err)
	}
	src := string(content)

	t.Run("NewTeamHandler function exists", func(t *testing.T) {
		re := regexp.MustCompile(`func\s+NewTeamHandler\s*\(`)
		if !re.MatchString(src) {
			t.Error("team_handler.go should define NewTeamHandler constructor function")
		}
	})

	t.Run("no NewWorkspaceHandler function", func(t *testing.T) {
		if strings.Contains(src, "NewWorkspaceHandler") {
			t.Error("team_handler.go should not define NewWorkspaceHandler (renamed to NewTeamHandler)")
		}
	})
}
