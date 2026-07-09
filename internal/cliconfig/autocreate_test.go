package cliconfig_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/cliconfig"
)

// ---------------------------------------------------------------------------
// TS-05-1: Config file auto-creation on startup — fresh directory
// REQ: 05-REQ-1.1
// ---------------------------------------------------------------------------

func TestEnsureConfigExists_CreatesDirectoryAndFile(t *testing.T) {
	// TS-05-1: When $HOME/.af/config.toml does not exist, the CLI creates
	// $HOME/.af/ with 0700 and $HOME/.af/config.toml with 0600 containing
	// a comment header and hub_url = "".

	homeDir := t.TempDir()

	err := cliconfig.EnsureConfigExists(homeDir)
	if err != nil {
		t.Fatalf("EnsureConfigExists returned unexpected error: %v", err)
	}

	// Assert $HOME/.af/ directory exists.
	afDir := filepath.Join(homeDir, ".af")
	info, err := os.Stat(afDir)
	if err != nil {
		t.Fatalf("expected %s to exist, got error: %v", afDir, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be a directory", afDir)
	}

	// Assert directory permissions are 0700.
	if runtime.GOOS != "windows" {
		perm := info.Mode().Perm()
		if perm != 0700 {
			t.Errorf("expected directory permissions 0700, got %04o", perm)
		}
	}

	// Assert $HOME/.af/config.toml exists.
	configPath := filepath.Join(afDir, "config.toml")
	fileInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("expected %s to exist, got error: %v", configPath, err)
	}

	// Assert file permissions are 0600.
	if runtime.GOOS != "windows" {
		perm := fileInfo.Mode().Perm()
		if perm != 0600 {
			t.Errorf("expected file permissions 0600, got %04o", perm)
		}
	}

	// Assert file content includes a comment header and hub_url = "".
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	contentStr := string(content)

	if !strings.Contains(contentStr, "#") {
		t.Error("expected config file to contain a comment header (line starting with #)")
	}
	if !strings.Contains(contentStr, `hub_url = ""`) {
		t.Errorf("expected config file to contain 'hub_url = \"\"', got:\n%s", contentStr)
	}
}

// ---------------------------------------------------------------------------
// TS-05-2: Config file auto-creation — existing file is not modified
// REQ: 05-REQ-1.2
// ---------------------------------------------------------------------------

func TestEnsureConfigExists_ExistingFileNotModified(t *testing.T) {
	// TS-05-2: When $HOME/.af/config.toml already exists with custom content,
	// the CLI startup routine makes no changes.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(afDir, "config.toml")
	existingContent := []byte(`hub_url = "https://existing.example.com"` + "\n")
	if err := os.WriteFile(configPath, existingContent, 0600); err != nil {
		t.Fatal(err)
	}

	// Record the modification time before calling EnsureConfigExists.
	beforeInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeModTime := beforeInfo.ModTime()

	err = cliconfig.EnsureConfigExists(homeDir)
	if err != nil {
		t.Fatalf("EnsureConfigExists returned unexpected error: %v", err)
	}

	// Assert content is unchanged.
	afterContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterContent) != string(existingContent) {
		t.Errorf("expected config content to be unchanged.\nBefore: %q\nAfter:  %q",
			string(existingContent), string(afterContent))
	}

	// Assert modification time is unchanged.
	afterInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !afterInfo.ModTime().Equal(beforeModTime) {
		t.Errorf("expected modification time to be unchanged, was %v, now %v",
			beforeModTime, afterInfo.ModTime())
	}
}

// ---------------------------------------------------------------------------
// TS-05-3: Permissions on pre-existing paths are not modified
// REQ: 05-REQ-1.3
// ---------------------------------------------------------------------------

func TestEnsureConfigExists_DoesNotChangeExistingPermissions(t *testing.T) {
	// TS-05-3: Pre-create $HOME/.af/ (0755) and config.toml (0644). After
	// calling EnsureConfigExists, permissions must remain 0755/0644 — NOT
	// changed to 0700/0600.

	if runtime.GOOS == "windows" {
		t.Skip("permission tests not supported on Windows")
	}

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0755); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(`hub_url = ""`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := cliconfig.EnsureConfigExists(homeDir)
	if err != nil {
		t.Fatalf("EnsureConfigExists returned unexpected error: %v", err)
	}

	// Assert directory permissions remain 0755.
	dirInfo, err := os.Stat(afDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0755 {
		t.Errorf("expected directory permissions to remain 0755, got %04o", perm)
	}

	// Assert file permissions remain 0644.
	fileInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0644 {
		t.Errorf("expected file permissions to remain 0644, got %04o", perm)
	}
}

// ---------------------------------------------------------------------------
// TS-05-E1: Directory creation error — permission denied
// REQ: 05-REQ-1.E1
// ---------------------------------------------------------------------------

func TestEnsureConfigExists_DirectoryCreationError(t *testing.T) {
	// TS-05-E1: When $HOME is read-only and .af/ cannot be created,
	// EnsureConfigExists returns a non-nil error whose message references
	// the .af path.

	if runtime.GOOS == "windows" {
		t.Skip("permission tests not supported on Windows")
	}

	homeDir := t.TempDir()
	// Make homeDir read-only so mkdir .af will fail.
	if err := os.Chmod(homeDir, 0500); err != nil {
		t.Fatal(err)
	}
	// Restore permissions for cleanup.
	t.Cleanup(func() { os.Chmod(homeDir, 0700) })

	err := cliconfig.EnsureConfigExists(homeDir)
	if err == nil {
		t.Fatal("expected non-nil error when directory cannot be created, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, ".af") {
		t.Errorf("expected error message to contain '.af', got: %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// TS-05-E2: File creation error — directory not writable
// REQ: 05-REQ-1.E2
// ---------------------------------------------------------------------------

func TestEnsureConfigExists_FileCreationError(t *testing.T) {
	// TS-05-E2: When $HOME/.af/ exists but has permissions 0500 (no write),
	// EnsureConfigExists returns a non-nil error referencing config.toml.

	if runtime.GOOS == "windows" {
		t.Skip("permission tests not supported on Windows")
	}

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	// Create .af/ with no-write permission.
	if err := os.MkdirAll(afDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(afDir, 0700) })

	err := cliconfig.EnsureConfigExists(homeDir)
	if err == nil {
		t.Fatal("expected non-nil error when config file cannot be created, got nil")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "config.toml") {
		t.Errorf("expected error message to reference 'config.toml', got: %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// TS-05-E3: Partial file cleanup on write failure
// REQ: 05-REQ-1.E3
// ---------------------------------------------------------------------------

func TestEnsureConfigExists_PartialFileCleanedUp(t *testing.T) {
	// TS-05-E3: Inject a write-failing function; assert
	// EnsureConfigExistsWithWriter returns error and config.toml does not
	// exist (partial file removed).

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	// Pre-create .af/ directory so the function can reach the file-write step.
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Inject a writer that always fails.
	failingWriter := func(path string, content []byte, perm os.FileMode) error {
		// Simulate a partial write by creating the file first, then failing.
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, perm)
		if err != nil {
			return err
		}
		// Write a partial byte to simulate interrupted write.
		f.Write(content[:1])
		f.Close()
		return fmt.Errorf("simulated write failure")
	}

	err := cliconfig.EnsureConfigExistsWithWriter(homeDir, failingWriter)
	if err == nil {
		t.Fatal("expected non-nil error when write fails, got nil")
	}

	// Assert that no partial config.toml file remains.
	configPath := filepath.Join(afDir, "config.toml")
	if _, statErr := os.Stat(configPath); statErr == nil {
		t.Errorf("expected config.toml to be cleaned up after write failure, but file exists")
	}
}
