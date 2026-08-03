package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers for CheckGitVersion
// ---------------------------------------------------------------------------

// fakeGitVersionBin creates a temporary directory containing a shell script
// named "git" that outputs the specified version string to stdout and exits 0.
// This allows tests to control what CheckGitVersion sees without needing the
// real git binary.
//
// Returns the directory path containing the fake binary.
func fakeGitVersionBin(t *testing.T, versionOutput string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "git")
	content := fmt.Sprintf("#!/bin/sh\necho '%s'\n", versionOutput)
	err := os.WriteFile(script, []byte(content), 0o755)
	if err != nil {
		t.Fatalf("failed to create fake git version script: %v", err)
	}
	return dir
}

// ---------------------------------------------------------------------------
// Task 5.1: Table-driven tests for version string parsing
// ---------------------------------------------------------------------------

// TS-10-23: CheckGitVersion correctly parses standard, Apple Git vendor, and
// release candidate version strings by extracting only the first three
// dot-separated numeric components.
// Requirement: 10-REQ-6.4
// Property: PROP-9
func TestVersionParsing_TableDriven(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "standard version",
			input:    "git version 2.39.1",
			expected: "2.39.1",
		},
		{
			name:     "Apple Git vendor suffix",
			input:    "git version 2.39.3 (Apple Git-145)",
			expected: "2.39.3",
		},
		{
			name:     "release candidate suffix",
			input:    "git version 2.38.0.rc1",
			expected: "2.38.0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parsed, err := parseGitVersion(tc.input)
			if err != nil {
				t.Fatalf("parseGitVersion(%q) returned error: %v", tc.input, err)
			}
			if parsed != tc.expected {
				t.Errorf("parseGitVersion(%q) = %q; want %q", tc.input, parsed, tc.expected)
			}
		})
	}
}

// TS-10-22: CheckGitVersion returns a parse error when the git --version
// output cannot be parsed into three numeric components.
// Requirement: 10-REQ-6.3
func TestVersionParsing_Unparseable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "completely non-numeric",
			input: "git version banana",
		},
		{
			name:  "missing version prefix entirely",
			input: "not a git version string at all",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseGitVersion(tc.input)
			if err == nil {
				t.Errorf("parseGitVersion(%q) returned nil error; expected parse error", tc.input)
			}
		})
	}
}

// 10-REQ-6.E5: If the version string contains fewer than three dot-separated
// numeric components, parseGitVersion returns a parse error; do not attempt
// to compare a partial version.
// Property: PROP-9
func TestVersionParsing_FewerThanThreeComponents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "only major version",
			input: "git version 2",
		},
		{
			name:  "only major.minor",
			input: "git version 2.39",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseGitVersion(tc.input)
			if err == nil {
				t.Errorf("parseGitVersion(%q) returned nil error; expected parse error for fewer than 3 components", tc.input)
			}
		})
	}
}

// TS-10-20: CheckGitVersion returns nil when the installed git version is
// >= 2.38. This is an integration test that runs against the real git binary
// on the host (CI has git >= 2.38).
// Requirement: 10-REQ-6.1
func TestCheckGitVersion_RealGit(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := CheckGitVersion(ctx)
	if err != nil {
		t.Errorf("CheckGitVersion returned error: %v; expected nil (git >= 2.38 should be installed)", err)
	}
}

// ---------------------------------------------------------------------------
// Task 5.2: CheckGitVersion below-minimum error message format
// ---------------------------------------------------------------------------

// TS-10-21: CheckGitVersion returns an error with the exact format
// 'requires git >= 2.38, found <installed>' when the installed git version
// is below 2.38.
// Requirement: 10-REQ-6.2
// Property: PROP-6
func TestCheckGitVersion_BelowMinimum_ExactFormat(t *testing.T) {
	// Cannot use t.Parallel() — t.Setenv modifies process-level env.
	fakeDir := fakeGitVersionBin(t, "git version 2.35.1")
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := CheckGitVersion(ctx)
	if err == nil {
		t.Fatal("CheckGitVersion returned nil error; expected error for version 2.35.1")
	}

	expected := "requires git >= 2.38, found 2.35.1"
	if err.Error() != expected {
		t.Errorf("CheckGitVersion error = %q; want exactly %q", err.Error(), expected)
	}
}

// PROP-6: CheckGitVersion error message format is exact for any version below
// 2.38. Multiple versions are tested to confirm the format is consistent.
// Requirement: 10-REQ-6.2
func TestCheckGitVersion_BelowMinimum_MultipleVersions(t *testing.T) {
	// Cannot use t.Parallel() — subtests use t.Setenv.
	tests := []struct {
		name          string
		versionOutput string
		expectedError string
	}{
		{
			name:          "version 2.35.1",
			versionOutput: "git version 2.35.1",
			expectedError: "requires git >= 2.38, found 2.35.1",
		},
		{
			name:          "version 2.37.0",
			versionOutput: "git version 2.37.0",
			expectedError: "requires git >= 2.38, found 2.37.0",
		},
		{
			name:          "version 1.0.0",
			versionOutput: "git version 1.0.0",
			expectedError: "requires git >= 2.38, found 1.0.0",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Cannot use t.Parallel() — t.Setenv modifies process-level env.
			fakeDir := fakeGitVersionBin(t, tc.versionOutput)
			t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := CheckGitVersion(ctx)
			if err == nil {
				t.Fatalf("CheckGitVersion returned nil for %s; expected error", tc.versionOutput)
			}
			if err.Error() != tc.expectedError {
				t.Errorf("error = %q; want exactly %q", err.Error(), tc.expectedError)
			}
		})
	}
}

// TestCheckGitVersion_MinimumFloor verifies the minimum version floor is
// enforced correctly: version 2.37.x returns error, version 2.38.0 returns
// nil.
// Requirement: 10-REQ-6.2 (boundary)
func TestCheckGitVersion_MinimumFloor(t *testing.T) {
	// Cannot use t.Parallel() — subtests use t.Setenv.
	tests := []struct {
		name          string
		versionOutput string
		expectError   bool
	}{
		{
			name:          "below floor: 2.37.9",
			versionOutput: "git version 2.37.9",
			expectError:   true,
		},
		{
			name:          "at floor: 2.38.0",
			versionOutput: "git version 2.38.0",
			expectError:   false,
		},
		{
			name:          "above floor: 2.39.0",
			versionOutput: "git version 2.39.0",
			expectError:   false,
		},
		{
			name:          "major version below: 1.99.99",
			versionOutput: "git version 1.99.99",
			expectError:   true,
		},
		{
			name:          "major version above: 3.0.0",
			versionOutput: "git version 3.0.0",
			expectError:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Cannot use t.Parallel() — t.Setenv modifies process-level env.
			fakeDir := fakeGitVersionBin(t, tc.versionOutput)
			t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := CheckGitVersion(ctx)
			if tc.expectError && err == nil {
				t.Errorf("CheckGitVersion returned nil for %s; expected error", tc.versionOutput)
			}
			if !tc.expectError && err != nil {
				t.Errorf("CheckGitVersion returned error for %s: %v; expected nil", tc.versionOutput, err)
			}
		})
	}
}

// TestCheckGitVersion_MinVersionNotExported verifies that the minimum version
// constants (minMajor, minMinor) are package-private. This test compiles only
// because it is in the same package (gitcmd); an external package would get a
// compilation error if it tried to reference these unexported symbols.
// If someone exports them by capitalizing, this test's references would need
// updating, serving as a code-review signal.
func TestCheckGitVersion_MinVersionNotExported(t *testing.T) {
	t.Parallel()
	// Access unexported constants to verify they exist and have expected values.
	if minMajor != 2 {
		t.Errorf("minMajor = %d; want 2", minMajor)
	}
	if minMinor != 38 {
		t.Errorf("minMinor = %d; want 38", minMinor)
	}
}

// ---------------------------------------------------------------------------
// Edge cases for CheckGitVersion
// ---------------------------------------------------------------------------

// 10-REQ-6.E1: If the git binary is not found on PATH, CheckGitVersion
// returns the raw os/exec error without wrapping it in a GitError.
func TestCheckGitVersion_BinaryNotFound(t *testing.T) {
	// Cannot use t.Parallel() — t.Setenv modifies process-level env.
	emptyDir := t.TempDir() // empty directory with no git binary
	t.Setenv("PATH", emptyDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := CheckGitVersion(ctx)
	if err == nil {
		t.Fatal("CheckGitVersion returned nil when git binary not found; expected error")
	}

	// Must NOT be a *GitError.
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		t.Error("CheckGitVersion returned *GitError when git not found; expected raw os/exec error")
	}

	// Must be an *exec.Error.
	var execErr *exec.Error
	if !errors.As(err, &execErr) {
		t.Errorf("error is not *exec.Error: %T — %v", err, err)
	}
}

// 10-REQ-6.E2: If the context deadline expires or is cancelled while
// 'git --version' is running, CheckGitVersion returns the context error.
func TestCheckGitVersion_ContextCancelled(t *testing.T) {
	t.Parallel()
	// Pre-cancel the context so the subprocess never starts (or is killed).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := CheckGitVersion(ctx)
	if err == nil {
		t.Error("CheckGitVersion with cancelled context returned nil; expected error")
	}
}

// 10-REQ-6.E2 (timeout variant): A very short deadline triggers the context
// deadline exceeded error.
func TestCheckGitVersion_ContextTimeout(t *testing.T) {
	t.Parallel()
	// Use an extremely short timeout that should expire before git can respond.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// Allow a tiny moment for the deadline to lapse.
	time.Sleep(1 * time.Millisecond)

	err := CheckGitVersion(ctx)
	if err == nil {
		t.Error("CheckGitVersion with expired deadline returned nil; expected error")
	}
}

// 10-REQ-6.E3: When the version string contains an Apple Git vendor suffix,
// CheckGitVersion extracts the version correctly and returns nil (>= 2.38).
// 10-REQ-6.E4: When the version string contains a release candidate suffix,
// CheckGitVersion extracts the version correctly and returns nil (>= 2.38).
// These tests exercise the full CheckGitVersion stack (subprocess → parse →
// compare), not just parseGitVersion directly.
func TestCheckGitVersion_AppleGitAndRC(t *testing.T) {
	// Cannot use t.Parallel() — subtests use t.Setenv.
	tests := []struct {
		name          string
		versionOutput string
	}{
		{
			name:          "Apple Git format (E3)",
			versionOutput: "git version 2.39.3 (Apple Git-145)",
		},
		{
			name:          "release candidate format (E4)",
			versionOutput: "git version 2.38.0.rc1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Cannot use t.Parallel() — t.Setenv modifies process-level env.
			fakeDir := fakeGitVersionBin(t, tc.versionOutput)
			t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := CheckGitVersion(ctx)
			if err != nil {
				t.Errorf("CheckGitVersion returned error for %q: %v; expected nil", tc.versionOutput, err)
			}
		})
	}
}

// TS-10-22 via CheckGitVersion: When the git binary outputs an unparseable
// version string, CheckGitVersion returns a parse error (not a *GitError).
// Requirement: 10-REQ-6.3
func TestCheckGitVersion_UnparseableVersion(t *testing.T) {
	// Cannot use t.Parallel() — t.Setenv modifies process-level env.
	fakeDir := fakeGitVersionBin(t, "git version banana")
	t.Setenv("PATH", fakeDir+":"+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := CheckGitVersion(ctx)
	if err == nil {
		t.Fatal("CheckGitVersion returned nil for unparseable version; expected parse error")
	}

	// The error should NOT be a *GitError (git exited 0, but output is bad).
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		t.Error("CheckGitVersion returned *GitError for parse failure; expected plain parse error")
	}

	// The error message should describe the parse failure.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "parse") && !strings.Contains(errMsg, "version") {
		t.Errorf("error message should describe the parse failure; got %q", errMsg)
	}
}
