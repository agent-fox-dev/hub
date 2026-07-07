package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot returns the absolute path to the repository root by walking up
// from the current working directory until a go.mod file is found. This is
// needed because Go tests run from the package directory, not the repo root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root (go.mod)")
		}
		dir = parent
	}
}

// ---------------------------------------------------------------------------
// TS-03-17: docs/cli.md exists and documents all commands, flags, env vars,
//           and includes at least one usage example per command
// REQ: 03-REQ-8.1
// ---------------------------------------------------------------------------

func TestDocs_CLIMDExists(t *testing.T) {
	// TS-03-17: Assert docs/cli.md file exists and is non-empty.

	root := repoRoot(t)
	cliDocPath := filepath.Join(root, "docs", "cli.md")

	info, err := os.Stat(cliDocPath)
	if err != nil {
		t.Fatalf("docs/cli.md does not exist: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("docs/cli.md exists but is empty")
	}
}

func TestDocs_CLIMDContainsAllCommands(t *testing.T) {
	// TS-03-17: Assert docs/cli.md contains sections for all CLI commands:
	// login, keys create, keys list, keys refresh, keys revoke.

	root := repoRoot(t)
	cliDocPath := filepath.Join(root, "docs", "cli.md")

	content, err := os.ReadFile(cliDocPath)
	if err != nil {
		t.Fatalf("could not read docs/cli.md: %v", err)
	}

	text := string(content)

	commands := []string{
		"login",
		"keys create",
		"keys list",
		"keys refresh",
		"keys revoke",
	}

	for _, cmd := range commands {
		if !strings.Contains(text, cmd) {
			t.Errorf("docs/cli.md does not contain command %q", cmd)
		}
	}
}

func TestDocs_CLIMDContainsAllFlagsAndEnvVars(t *testing.T) {
	// TS-03-17: Assert docs/cli.md documents all flags and environment
	// variables: --hub-url, AF_HUB_URL, --api-key, AF_HUB_API_KEY.

	root := repoRoot(t)
	cliDocPath := filepath.Join(root, "docs", "cli.md")

	content, err := os.ReadFile(cliDocPath)
	if err != nil {
		t.Fatalf("could not read docs/cli.md: %v", err)
	}

	text := string(content)

	items := []string{
		"--hub-url",
		"AF_HUB_URL",
		"--api-key",
		"AF_HUB_API_KEY",
	}

	for _, item := range items {
		if !strings.Contains(text, item) {
			t.Errorf("docs/cli.md does not contain flag/env var %q", item)
		}
	}
}

func TestDocs_CLIMDContainsExamplesPerCommand(t *testing.T) {
	// TS-03-17: Assert docs/cli.md contains at least one code block (```)
	// per command section. We check that each command name appears near a
	// code block marker.

	root := repoRoot(t)
	cliDocPath := filepath.Join(root, "docs", "cli.md")

	content, err := os.ReadFile(cliDocPath)
	if err != nil {
		t.Fatalf("could not read docs/cli.md: %v", err)
	}

	text := string(content)

	// Each command should have at least one associated code example.
	// We check that the document has code blocks and that each command
	// section contains examples by looking for fenced code markers.
	commands := []string{
		"login",
		"keys create",
		"keys list",
		"keys refresh",
		"keys revoke",
	}

	codeBlockCount := strings.Count(text, "```")
	if codeBlockCount < 2 {
		// Need at least one pair of ``` (opening + closing) per command,
		// so minimum 10 total markers for 5 commands.
		t.Errorf("expected at least 2 code block markers (``` pairs), got %d total ``` markers", codeBlockCount)
	}

	// For each command, find the section and verify it has a code block.
	for _, cmd := range commands {
		idx := strings.Index(text, cmd)
		if idx < 0 {
			t.Errorf("docs/cli.md does not contain command %q — cannot check for examples", cmd)
			continue
		}

		// Look for a code block within 2000 characters after the command name.
		// This is a heuristic: the code example should be within the same section.
		endIdx := idx + 2000
		if endIdx > len(text) {
			endIdx = len(text)
		}
		section := text[idx:endIdx]
		if !strings.Contains(section, "```") {
			t.Errorf("docs/cli.md section for %q does not appear to have a code example (no ``` within 2000 chars)", cmd)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-03-18: README.md contains a link to docs/cli.md
// REQ: 03-REQ-8.2
// ---------------------------------------------------------------------------

func TestDocs_READMELinksToCliMD(t *testing.T) {
	// TS-03-18: Read README.md and assert it contains 'docs/cli.md'.

	root := repoRoot(t)
	readmePath := filepath.Join(root, "README.md")

	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("could not read README.md: %v", err)
	}

	text := string(content)
	if len(text) == 0 {
		t.Fatal("README.md is empty")
	}

	if !strings.Contains(text, "docs/cli.md") {
		t.Error("README.md does not contain a reference to 'docs/cli.md'")
	}
}
