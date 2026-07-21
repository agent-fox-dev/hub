package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projectRoot walks up from the current directory until it finds go.mod,
// returning the directory that contains it (the project root). This lets
// tests reference docs/ relative to the repository root regardless of which
// directory `go test` runs in.
func projectRoot(t *testing.T) string {
	t.Helper()

	// Start from the directory of this test file.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd failed: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (no go.mod found in any parent directory)")
		}
		dir = parent
	}
}

// readDocFile reads a documentation file relative to the project root and
// returns its content as a string. It fails the test if the file does not exist.
func readDocFile(t *testing.T, relPath string) string {
	t.Helper()
	root := projectRoot(t)
	fullPath := filepath.Join(root, relPath)

	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", relPath, err)
	}
	return string(data)
}

// =============================================================================
// TS-03-35: docs/api.md covers all workspace endpoints (create, list, get,
// update, archive, reactivate, delete) with auth, request/response schemas,
// permission scopes, and error codes
// Requirement: 03-REQ-9.1
// =============================================================================

func TestSpec03_Group6_APIDocCoversAllWorkspaceEndpoints(t *testing.T) {
	content := readDocFile(t, "docs/api.md")

	// Each workspace endpoint must be mentioned.
	endpoints := []struct {
		name  string
		terms []string // at least one of these must appear
	}{
		{"create", []string{"POST /api/v1/workspaces"}},
		{"list", []string{"GET /api/v1/workspaces"}},
		{"get", []string{"GET /api/v1/workspaces/:slug", "GET /api/v1/workspaces/{slug}"}},
		{"update", []string{"PATCH /api/v1/workspaces/:slug", "PATCH /api/v1/workspaces/{slug}"}},
		{"archive", []string{"archive", "/archive"}},
		{"reactivate", []string{"reactivate", "/reactivate"}},
		{"delete", []string{"DELETE /api/v1/workspaces/:slug", "DELETE /api/v1/workspaces/{slug}"}},
	}

	for _, ep := range endpoints {
		found := false
		for _, term := range ep.terms {
			if strings.Contains(content, term) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("docs/api.md missing endpoint %q: expected one of %v", ep.name, ep.terms)
		}
	}
}

// =============================================================================
// TS-03-36: docs/api.md documents PATCH /api/v1/workspaces/:slug with
// partial-update semantics, field constraints, and all error conditions
// Requirement: 03-REQ-9.2
// =============================================================================

func TestSpec03_Group6_APIDocPATCHEndpointDetails(t *testing.T) {
	content := readDocFile(t, "docs/api.md")

	// PATCH endpoint must be documented.
	if !strings.Contains(content, "PATCH /api/v1/workspaces") {
		t.Error("docs/api.md does not contain 'PATCH /api/v1/workspaces'")
	}

	// Field constraints.
	if !strings.Contains(content, "128") {
		t.Error("docs/api.md does not mention display_name max length '128'")
	}
	if !strings.Contains(content, "1024") {
		t.Error("docs/api.md does not mention description max length '1024'")
	}

	// Archived workspace behavior.
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "archived") {
		t.Error("docs/api.md does not mention 'archived' workspace behavior")
	}

	// Partial update semantics.
	if !strings.Contains(lower, "partial") && !strings.Contains(lower, "omitted fields") && !strings.Contains(lower, "omitted field") {
		t.Error("docs/api.md does not describe partial update semantics ('partial' or 'omitted fields')")
	}

	// Error status codes.
	errorCodes := []string{"400", "403", "404"}
	for _, code := range errorCodes {
		if !strings.Contains(content, code) {
			t.Errorf("docs/api.md does not mention HTTP error code %s", code)
		}
	}
}

// =============================================================================
// TS-03-37: docs/api.md documents workspaces:write and workspaces:delete scopes,
// their implied permissions, and which endpoints they authorize
// Requirement: 03-REQ-9.3
// =============================================================================

func TestSpec03_Group6_APIDocScopeDocumentation(t *testing.T) {
	content := readDocFile(t, "docs/api.md")

	// Both new scopes must be mentioned.
	if !strings.Contains(content, "workspaces:write") {
		t.Error("docs/api.md does not contain 'workspaces:write'")
	}
	if !strings.Contains(content, "workspaces:delete") {
		t.Error("docs/api.md does not contain 'workspaces:delete'")
	}

	// Implied permissions must be described.
	lower := strings.ToLower(content)
	hasImplied := strings.Contains(lower, "implied") ||
		strings.Contains(lower, "implies") ||
		strings.Contains(lower, "read access")
	if !hasImplied {
		t.Error("docs/api.md does not describe implied permissions ('implied', 'implies', or 'read access')")
	}
}

// =============================================================================
// TS-03-38: docs/api.md covers non-workspace apikit-provided endpoints
// (login, user, keys, tokens, orgs, admin)
// Requirement: 03-REQ-9.4
// =============================================================================

func TestSpec03_Group6_APIDocNonWorkspaceEndpoints(t *testing.T) {
	content := readDocFile(t, "docs/api.md")
	lower := strings.ToLower(content)

	sections := []string{"login", "user", "keys", "tokens", "orgs", "admin"}
	for _, section := range sections {
		if !strings.Contains(lower, section) {
			t.Errorf("docs/api.md does not contain reference to '%s' endpoints", section)
		}
	}
}

// =============================================================================
// TS-03-39: docs/cli.md covers all workspace subcommands (create, list, get,
// update, archive, reactivate, delete) with flags, argument formats, exit codes
// Requirement: 03-REQ-10.1
// =============================================================================

func TestSpec03_Group6_CLIDocCoversAllWorkspaceSubcommands(t *testing.T) {
	content := readDocFile(t, "docs/cli.md")
	lower := strings.ToLower(content)

	cmds := []string{"create", "list", "get", "update", "archive", "reactivate", "delete"}
	for _, cmd := range cmds {
		// Accept either "workspace create" or "afc workspace create".
		hasCmd := strings.Contains(lower, "workspace "+cmd) ||
			strings.Contains(lower, "afc workspace "+cmd)
		if !hasCmd {
			t.Errorf("docs/cli.md does not document workspace subcommand %q", cmd)
		}
	}
}

// =============================================================================
// TS-03-40: docs/cli.md documents afc workspace update with all flags and
// exit-1 behavior when no flags are provided
// Requirement: 03-REQ-10.2
// =============================================================================

func TestSpec03_Group6_CLIDocUpdateFlags(t *testing.T) {
	content := readDocFile(t, "docs/cli.md")

	// All flags must be documented.
	flags := []string{
		"--display-name",
		"--description",
		"--org",
		"--clear-display-name",
		"--clear-description",
		"--clear-org",
	}
	for _, flag := range flags {
		if !strings.Contains(content, flag) {
			t.Errorf("docs/cli.md does not document flag %q", flag)
		}
	}

	// Exit code 1 behavior when no flags provided.
	lower := strings.ToLower(content)
	hasExit := strings.Contains(lower, "exit") && strings.Contains(content, "1")
	if !hasExit {
		t.Error("docs/cli.md does not document exit code 1 behavior for no-flags invocation")
	}
}

// =============================================================================
// TS-03-41: docs/cli.md covers all apikit-provided afc commands
// (login, user, keys, tokens, orgs, admin)
// Requirement: 03-REQ-10.3
// =============================================================================

func TestSpec03_Group6_CLIDocApikitCommands(t *testing.T) {
	content := readDocFile(t, "docs/cli.md")
	lower := strings.ToLower(content)

	cmds := []string{"login", "user", "keys", "tokens", "orgs", "admin"}
	for _, cmd := range cmds {
		if !strings.Contains(lower, cmd) {
			t.Errorf("docs/cli.md does not contain reference to '%s' command", cmd)
		}
	}
}
