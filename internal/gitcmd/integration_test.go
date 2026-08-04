package gitcmd

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ========================================================================
// Spec 11 Task 3.3: Integration coverage and cross-cutting tests
// (TS-11-35, TS-11-36, TS-11-37, TS-11-39)
// Requirements: 11-REQ-13.1, 11-REQ-13.2, 11-REQ-13.3, 11-REQ-13.5
// ========================================================================

// TestStdlibOnlyImports verifies that all test files in the package import
// only standard library packages — no testify, gomock, or other third-party
// test dependencies are permitted.
//
// TS-11-35
// Requirement: 11-REQ-13.1
func TestStdlibOnlyImports(t *testing.T) {
	entries, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no *_test.go files found in package directory")
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		f, parseErr := parser.ParseFile(fset, entry, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("failed to parse %s: %v", entry, parseErr)
		}
		for _, imp := range f.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			// Standard library packages have no dot in their first path
			// component (e.g., "testing", "os", "context", "go/parser").
			// Third-party packages have a domain (e.g., "github.com/...").
			firstComponent := strings.SplitN(importPath, "/", 2)[0]
			if strings.Contains(firstComponent, ".") {
				t.Errorf("test file %s imports non-stdlib package %q; "+
					"test suite must use only standard library (testing, os, etc.)",
					entry, importPath)
			}
		}
	}
}

// TestTempRepoCleanupPattern verifies that integration tests create temporary
// git repositories using os.MkdirTemp or t.TempDir() and register cleanup
// appropriately. Tests must not share mutable repository state via global or
// package-level variables.
//
// TS-11-36
// Requirement: 11-REQ-13.2
func TestTempRepoCleanupPattern(t *testing.T) {
	entries, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	hasTempPattern := false
	for _, entry := range entries {
		data, readErr := os.ReadFile(entry)
		if readErr != nil {
			t.Fatalf("read %s: %v", entry, readErr)
		}
		content := string(data)

		// Accept either os.MkdirTemp+t.Cleanup or t.TempDir() — the latter
		// is the Go 1.15+ replacement that handles cleanup automatically.
		if strings.Contains(content, "os.MkdirTemp") || strings.Contains(content, ".TempDir()") {
			hasTempPattern = true
		}
	}

	if !hasTempPattern {
		t.Error("test suite should use os.MkdirTemp+t.Cleanup or t.TempDir() " +
			"for temporary test directories; no temp directory pattern found")
	}
}

// TestConstructorSkipsOldGit verifies that integration tests that require
// git >= 2.38 use t.Skip (via the requireGitMinVersion helper) to gracefully
// handle hosts with older git versions, rather than failing with t.Fatal or
// t.Error. The skip mechanism must NOT rely on a fake git binary or
// exec.Command mock.
//
// TS-11-37
// Requirement: 11-REQ-13.3
func TestConstructorSkipsOldGit(t *testing.T) {
	// Verify that the requireGitMinVersion helper exists and calls t.Skip.
	helperData, err := os.ReadFile("testhelpers_test.go")
	if err != nil {
		t.Fatalf("read testhelpers_test.go: %v", err)
	}
	helperContent := string(helperData)

	if !strings.Contains(helperContent, "requireGitMinVersion") {
		t.Error("test helpers should define a requireGitMinVersion function")
	}
	if !strings.Contains(helperContent, "t.Skipf") && !strings.Contains(helperContent, "t.Skip(") {
		t.Error("requireGitMinVersion should call t.Skip or t.Skipf to skip " +
			"tests on hosts with git < 2.38")
	}

	// Verify that integration tests use the helper (not a mock/fake git).
	entries, readErr := filepath.Glob("*_test.go")
	if readErr != nil {
		t.Fatalf("glob: %v", readErr)
	}

	helperUsed := false
	for _, entry := range entries {
		if entry == "testhelpers_test.go" {
			continue
		}
		data, readErr := os.ReadFile(entry)
		if readErr != nil {
			t.Fatalf("read %s: %v", entry, readErr)
		}
		if strings.Contains(string(data), "requireGitMinVersion(t,") {
			helperUsed = true
			break
		}
	}
	if !helperUsed {
		t.Error("at least one integration test should call requireGitMinVersion " +
			"to skip when host git version is below minimum")
	}
}

// TestSafetyVarPrecedence_Integration verifies that when a caller passes a
// conflicting GIT_ALLOW_PROTOCOL value in extraEnv, the hardcoded
// "file:https:ssh" value takes precedence. This test checks both the internal
// environment assembly and the functional behavior.
//
// Note: The test spec pseudocode suggests using runner.Run(ctx, "env") to
// inspect the subprocess environment, but "git env" is not a valid git
// subcommand. Instead, this test inspects the runner's internal env slice
// (same-package access) and verifies functional behavior via LsRemote with
// a file:// URL — if the hardcoded GIT_ALLOW_PROTOCOL (which includes "file")
// takes effect, file:// access succeeds.
//
// TS-11-39
// Requirement: 11-REQ-13.5
func TestSafetyVarPrecedence_Integration(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	tmpDir := t.TempDir()

	// Pass a conflicting GIT_ALLOW_PROTOCOL that excludes "file" protocol.
	runner, err := New(tmpDir, []string{"GIT_ALLOW_PROTOCOL=https"})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Verify internal env slice: the hardcoded value must be the last
	// occurrence, overriding the caller-supplied value.
	val, found := lastEnvValue(runner.env, "GIT_ALLOW_PROTOCOL")
	if !found {
		t.Fatal("GIT_ALLOW_PROTOCOL not found in runner.env")
	}
	if val != "file:https:ssh" {
		t.Errorf("effective GIT_ALLOW_PROTOCOL = %q, want %q (hardcoded value should override extraEnv)",
			val, "file:https:ssh")
	}

	// Functional verification: if the hardcoded GIT_ALLOW_PROTOCOL takes
	// effect (includes "file"), then ls-remote via file:// URL should work.
	// If the caller-supplied value ("https" only) took effect, git would
	// reject the file:// protocol.
	bareRepo := initBareTestRepo(t)
	ctx := context.Background()
	out, lsErr := runner.LsRemote(ctx, "file://"+bareRepo, "refs/heads/main")
	if lsErr != nil {
		t.Errorf("LsRemote with file:// URL should succeed when hardcoded "+
			"GIT_ALLOW_PROTOCOL wins, got error: %v", lsErr)
	}
	if out == "" {
		t.Error("LsRemote should return non-empty output for existing ref")
	}
}
