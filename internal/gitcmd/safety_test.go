package gitcmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ========================================================================
// Spec 14 Task 5.3: Safety environment variables on all new methods
// (TS-14-32)
// Requirements: 14-REQ-15.1, 14-REQ-15.E1
// ========================================================================

// TS-14-32: All new GitRunner methods inject GIT_TERMINAL_PROMPT,
// GIT_CONFIG_NOSYSTEM, and GIT_ALLOW_PROTOCOL into every git subprocess.
//
// This test uses a git wrapper script that captures subprocess environment
// variables to a file before delegating to the real git binary. Each new
// method is called, and the captured env is verified to contain the three
// safety variables with correct values.
//
// Preconditions:
//   - GitRunner is constructed with workDir pointing to a real temp git repository
//   - A git wrapper script captures subprocess environment variables
//
// Requirement: 14-REQ-15.1
// Edge case: 14-REQ-15.E1 (GitRunner's extraEnv takes precedence)
func TestSafetyEnvVars_NewMethodsInheritExtraEnv(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Locate the real git binary BEFORE modifying PATH.
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git not found on PATH: %v", err)
	}

	// Create a wrapper script that captures env vars, then delegates to real git.
	wrapperDir := t.TempDir()
	captureFile := filepath.Join(t.TempDir(), "captured-env.log")

	wrapperScript := fmt.Sprintf("#!/bin/sh\nenv > '%s'\nexec '%s' \"$@\"\n",
		captureFile, realGit)
	if err := os.WriteFile(filepath.Join(wrapperDir, "git"), []byte(wrapperScript), 0o755); err != nil {
		t.Fatalf("write wrapper script: %v", err)
	}

	// Set up a real git repo BEFORE modifying PATH (uses real git for setup).
	repoDir := initTestRepoWithCommit(t)

	// Modify PATH so the wrapper git is found first by exec.Command.
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", wrapperDir+":"+origPath)

	// Construct runner (constructor's version check uses wrapper, which
	// delegates to real git — version check passes).
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()

	// All new methods that must inherit safety environment variables (14-REQ-15.1).
	// Each method is called with syntactically valid arguments. The test does not
	// check method success/failure — only that a git subprocess was spawned with
	// the correct safety environment variables.
	methods := []struct {
		name string
		call func()
	}{
		{"Checkout", func() { runner.Checkout(ctx, "HEAD") }},
		{"CreateBranch", func() { runner.CreateBranch(ctx, "safety-br", "HEAD") }},
		{"DeleteBranch", func() { runner.DeleteBranch(ctx, "nonexistent-br") }},
		{"CherryPick", func() { runner.CherryPick(ctx, "HEAD") }},
		{"ConfigSet", func() { runner.ConfigSet(ctx, "test.safety", "true") }},
		{"RemoteAdd", func() { runner.RemoteAdd(ctx, "safety-remote", "https://example.com/r.git") }},
		{"Log", func() { runner.Log(ctx, "--oneline", "-1") }},
		{"Diff", func() { runner.Diff(ctx) }},
		{"MergeNoFF", func() { runner.MergeNoFF(ctx, "HEAD") }},
		{"MergeAbort", func() { runner.MergeAbort(ctx) }},
		{"RebaseContinue", func() { runner.RebaseContinue(ctx) }},
		{"IsAncestor", func() { runner.IsAncestor(ctx, "HEAD", "HEAD") }},
	}

	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			// Clear the capture file before each method call.
			if err := os.WriteFile(captureFile, nil, 0o644); err != nil {
				t.Fatalf("clear capture file: %v", err)
			}

			// Call the method (return values are ignored — we only care about
			// whether a git subprocess was spawned with the right env).
			m.call()

			// Read the captured environment.
			content, err := os.ReadFile(captureFile)
			if err != nil {
				t.Fatalf("read capture file: %v", err)
			}
			envStr := strings.TrimSpace(string(content))

			if envStr == "" {
				t.Fatalf("%s did not spawn a git subprocess; "+
					"all new methods must use GitRunner.Run or GitRunner.runWithExitCode "+
					"to inherit safety environment variables (14-REQ-15.1)", m.name)
			}

			// Verify safety environment variables are present in the subprocess.
			if !strings.Contains(envStr, "GIT_TERMINAL_PROMPT=0") {
				t.Errorf("%s: subprocess env missing GIT_TERMINAL_PROMPT=0", m.name)
			}
			if !strings.Contains(envStr, "GIT_CONFIG_NOSYSTEM=1") {
				t.Errorf("%s: subprocess env missing GIT_CONFIG_NOSYSTEM=1", m.name)
			}
			if !strings.Contains(envStr, "GIT_ALLOW_PROTOCOL=") {
				t.Errorf("%s: subprocess env missing GIT_ALLOW_PROTOCOL", m.name)
			}
		})
	}
}
