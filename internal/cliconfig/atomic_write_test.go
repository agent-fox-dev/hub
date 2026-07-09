package cliconfig_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/agent-fox/af-hub/internal/cliconfig"
)

// ---------------------------------------------------------------------------
// TS-05-10: Atomic config write uses temp file in $HOME/.af/ and os.Rename
// REQ: 05-REQ-4.1
// ---------------------------------------------------------------------------

func TestAtomicWrite_UsesTempFileAndRename(t *testing.T) {
	// TS-05-10: Trigger a config mutation (e.g. SetDefaultKey) and verify that:
	// 1. A temp file was created in $HOME/.af/
	// 2. config.toml was atomically replaced via rename
	// 3. No temp file remains after the operation

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Write initial config.
	initialContent := `hub_url = "https://hub.example.com"
api_key = "_login"

[keys._login]
key_id = "0011aabb"
token = "af_0011aabb_secret"
label = "login"

[keys.my-project]
key_id = "a1b2c3"
token = "af_a1b2c3_secret"
label = "dev"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initialContent), 0600); err != nil {
		t.Fatal(err)
	}

	// Track spy state for CreateTemp and Rename calls.
	var createTempCalled bool
	var createTempDir string
	var tempFilePath string
	var renameCalled bool
	var renameDestination string

	spyCreateTemp := func(dir, pattern string) (*os.File, error) {
		createTempCalled = true
		createTempDir = dir
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		tempFilePath = f.Name()
		return f, nil
	}

	spyRename := func(oldpath, newpath string) error {
		renameCalled = true
		renameDestination = newpath
		return os.Rename(oldpath, newpath)
	}

	// Build a config struct to write.
	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "my-project",
		Keys: map[string]cliconfig.KeyEntry{
			"_login": {
				KeyID: "0011aabb",
				Token: "af_0011aabb_secret",
				Label: "login",
			},
			"my-project": {
				KeyID: "a1b2c3",
				Token: "af_a1b2c3_secret",
				Label: "dev",
			},
		},
	}

	err := cliconfig.WriteConfigAtomicWith(homeDir, cfg, spyCreateTemp, spyRename)
	if err != nil {
		t.Fatalf("WriteConfigAtomicWith returned unexpected error: %v", err)
	}

	// Assert CreateTemp was called in $HOME/.af/.
	if !createTempCalled {
		t.Error("expected CreateTemp to be called, but it was not")
	}
	if createTempDir != afDir {
		t.Errorf("expected CreateTemp dir = %q, got %q", afDir, createTempDir)
	}

	// Assert Rename was called with config.toml as destination.
	if !renameCalled {
		t.Error("expected Rename to be called, but it was not")
	}
	if renameDestination != configPath {
		t.Errorf("expected Rename destination = %q, got %q", configPath, renameDestination)
	}

	// Assert temp file is gone after mutation.
	if tempFilePath != "" {
		if _, err := os.Stat(tempFilePath); err == nil {
			t.Errorf("expected temp file %q to be removed after mutation, but it still exists", tempFilePath)
		}
	}

	// Assert config.toml was actually updated.
	updatedContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read updated config: %v", err)
	}
	if !strings.Contains(string(updatedContent), `api_key`) {
		t.Errorf("expected updated config to contain api_key, got:\n%s", updatedContent)
	}
}

func TestAtomicWrite_ConfigContentIsValidTOML(t *testing.T) {
	// Supplemental: After WriteConfigAtomic, the resulting file is valid TOML
	// that round-trips correctly.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`hub_url = ""`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "my-project",
		Keys: map[string]cliconfig.KeyEntry{
			"my-project": {
				KeyID: "a1b2c3",
				Token: "af_a1b2c3_secret",
				Label: "dev",
			},
		},
	}

	err := cliconfig.WriteConfigAtomic(homeDir, cfg)
	if err != nil {
		t.Fatalf("WriteConfigAtomic returned unexpected error: %v", err)
	}

	// Read the file and verify it's valid TOML.
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read updated config: %v", err)
	}

	var parsed cliconfig.Config
	if _, decodeErr := toml.Decode(string(content), &parsed); decodeErr != nil {
		t.Fatalf("expected valid TOML after atomic write, got decode error: %v\nContent:\n%s", decodeErr, content)
	}

	if parsed.HubURL != "https://hub.example.com" {
		t.Errorf("expected HubURL = %q, got %q", "https://hub.example.com", parsed.HubURL)
	}
	if parsed.APIKey != "my-project" {
		t.Errorf("expected APIKey = %q, got %q", "my-project", parsed.APIKey)
	}
	entry, ok := parsed.Keys["my-project"]
	if !ok {
		t.Fatal("expected keys.my-project in decoded config")
	}
	if entry.Token != "af_a1b2c3_secret" {
		t.Errorf("expected Token = %q, got %q", "af_a1b2c3_secret", entry.Token)
	}
}

// ---------------------------------------------------------------------------
// TS-05-11: Temp file created with 0600 permissions
// REQ: 05-REQ-4.2
// ---------------------------------------------------------------------------

func TestAtomicWrite_TempFilePermissions(t *testing.T) {
	// TS-05-11: Trigger a config mutation and capture the temp file's
	// permissions before rename. Assert the temp file has 0600.

	if runtime.GOOS == "windows" {
		t.Skip("permission tests not supported on Windows")
	}

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`hub_url = ""`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var capturedTempPerms os.FileMode

	spyCreateTemp := func(dir, pattern string) (*os.File, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		return f, nil
	}

	// Use a spy rename that captures the temp file's permissions before renaming.
	spyRename := func(oldpath, newpath string) error {
		info, err := os.Stat(oldpath)
		if err != nil {
			return fmt.Errorf("spy: stat temp file: %w", err)
		}
		capturedTempPerms = info.Mode().Perm()
		return os.Rename(oldpath, newpath)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "",
		Keys:   map[string]cliconfig.KeyEntry{},
	}

	err := cliconfig.WriteConfigAtomicWith(homeDir, cfg, spyCreateTemp, spyRename)
	if err != nil {
		t.Fatalf("WriteConfigAtomicWith returned unexpected error: %v", err)
	}

	if capturedTempPerms != 0600 {
		t.Errorf("expected temp file permissions 0600, got %04o", capturedTempPerms)
	}
}

// ---------------------------------------------------------------------------
// TS-05-E6: os.Rename failure — temp file cleaned up, original intact
// REQ: 05-REQ-4.E1
// ---------------------------------------------------------------------------

func TestAtomicWrite_RenameFailure_OriginalUnchanged(t *testing.T) {
	// TS-05-E6: Stub os.Rename to always fail. Assert original config.toml
	// is unchanged, temp file is removed, and the function returns a non-nil error.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	originalContent := `hub_url = "https://original.example.com"
api_key = "_login"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(originalContent), 0600); err != nil {
		t.Fatal(err)
	}

	var tempFileCreated string

	spyCreateTemp := func(dir, pattern string) (*os.File, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		tempFileCreated = f.Name()
		return f, nil
	}

	// Always-fail rename.
	alwaysFailRename := func(oldpath, newpath string) error {
		return fmt.Errorf("simulated rename failure")
	}

	cfg := &cliconfig.Config{
		HubURL: "https://new.example.com",
		APIKey: "my-project",
		Keys:   map[string]cliconfig.KeyEntry{},
	}

	err := cliconfig.WriteConfigAtomicWith(homeDir, cfg, spyCreateTemp, alwaysFailRename)
	if err == nil {
		t.Fatal("expected non-nil error when Rename fails, got nil")
	}

	// Assert original config.toml content is unchanged.
	afterContent, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("failed to read config after failed mutation: %v", readErr)
	}
	if string(afterContent) != originalContent {
		t.Errorf("expected original config to be unchanged.\nExpected: %q\nGot:     %q",
			originalContent, string(afterContent))
	}

	// Assert temp file was cleaned up.
	if tempFileCreated != "" {
		if _, statErr := os.Stat(tempFileCreated); statErr == nil {
			t.Errorf("expected temp file %q to be removed after rename failure, but it still exists", tempFileCreated)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-05-E7: os.CreateTemp failure — original config unchanged
// REQ: 05-REQ-4.E2
// ---------------------------------------------------------------------------

func TestAtomicWrite_CreateTempFailure_OriginalUnchanged(t *testing.T) {
	// TS-05-E7: Stub os.CreateTemp to always fail. Assert original config.toml
	// is unchanged and the function returns a non-nil error.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	originalContent := `hub_url = "https://original.example.com"
api_key = "_login"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(originalContent), 0600); err != nil {
		t.Fatal(err)
	}

	// Always-fail CreateTemp.
	alwaysFailCreateTemp := func(dir, pattern string) (*os.File, error) {
		return nil, fmt.Errorf("simulated CreateTemp failure: disk full")
	}

	// Rename should never be called, but provide a real implementation.
	realRename := func(oldpath, newpath string) error {
		return os.Rename(oldpath, newpath)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://new.example.com",
		APIKey: "my-project",
		Keys:   map[string]cliconfig.KeyEntry{},
	}

	err := cliconfig.WriteConfigAtomicWith(homeDir, cfg, alwaysFailCreateTemp, realRename)
	if err == nil {
		t.Fatal("expected non-nil error when CreateTemp fails, got nil")
	}

	// Assert original config.toml content is unchanged.
	afterContent, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("failed to read config after failed mutation: %v", readErr)
	}
	if string(afterContent) != originalContent {
		t.Errorf("expected original config to be unchanged.\nExpected: %q\nGot:     %q",
			originalContent, string(afterContent))
	}
}

func TestAtomicWrite_RenameFailure_NoTempFilesRemain(t *testing.T) {
	// Supplemental: After a rename failure, verify no temp files matching
	// the config-* pattern remain in $HOME/.af/.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`hub_url = ""`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	alwaysFailRename := func(oldpath, newpath string) error {
		return fmt.Errorf("simulated rename failure")
	}

	cfg := &cliconfig.Config{
		HubURL: "",
		APIKey: "",
		Keys:   map[string]cliconfig.KeyEntry{},
	}

	_ = cliconfig.WriteConfigAtomicWith(homeDir, cfg, os.CreateTemp, alwaysFailRename)

	// Check that no temp files remain.
	entries, err := os.ReadDir(afDir)
	if err != nil {
		t.Fatalf("failed to list $HOME/.af/: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "config.toml" {
			t.Errorf("unexpected file remaining in $HOME/.af/ after failed rename: %q", name)
		}
	}
}
