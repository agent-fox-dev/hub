package internal_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// containsCaseInsensitive checks whether s contains substr (case-insensitive).
func containsCaseInsensitive(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// TS-01-37: Verify that README.md exists at the project root and contains
// the required sections.
func TestREADME_ContainsRequiredSections(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "README.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("README.md does not exist at project root: %v", err)
	}
	content := string(data)

	required := []string{
		"Go 1.22",
		"make build",
		"docs/architecture.md",
		"docs/configuration.md",
	}
	for _, keyword := range required {
		if !strings.Contains(content, keyword) {
			t.Errorf("README.md should contain %q", keyword)
		}
	}
}

// TS-01-38: Verify that docs/architecture.md exists and covers the required
// architecture topics.
func TestArchitectureDoc_ContainsRequiredTopics(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "docs", "architecture.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("docs/architecture.md does not exist: %v", err)
	}
	content := string(data)

	required := []string{
		"af-hub",
		"afc",
		"internal",
		"SQLite",
		"config",
	}
	for _, keyword := range required {
		if !containsCaseInsensitive(content, keyword) {
			t.Errorf("docs/architecture.md should mention %q", keyword)
		}
	}
}

// TS-01-39: Verify that docs/configuration.md exists and documents all
// config.toml fields with types, defaults, and validation rules.
func TestConfigurationDoc_ContainsRequiredFields(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "docs", "configuration.md")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("docs/configuration.md does not exist: %v", err)
	}
	content := string(data)

	required := []string{
		"port",
		"bind_address",
		"database",
		"logging",
		"AF_HUB_ADMIN_TOKEN",
	}
	for _, keyword := range required {
		if !containsCaseInsensitive(content, keyword) {
			t.Errorf("docs/configuration.md should mention %q", keyword)
		}
	}
}
