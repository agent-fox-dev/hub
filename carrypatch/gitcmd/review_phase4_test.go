package gitcmd

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Regression tests for the gitcmd findings of the 2026-09 codebase review.

func TestAssembleEnv_StripsRepoRedirectsAndDefaultsIdentity(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_WORK_TREE", "/elsewhere")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.hooksPath=/tmp/evil'")
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "ops@example.com")

	env := assembleEnv([]string{"GIT_AUTHOR_EMAIL=extra@example.com"})

	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_PARAMETERS"} {
		if _, found := lastEnvValue(env, key); found {
			t.Errorf("%s must be stripped from the git environment", key)
		}
	}
	checks := map[string]string{
		// Set (even if empty) in the process environment: kept as is.
		"GIT_AUTHOR_NAME": "",
		// Provided by extraEnv: kept.
		"GIT_AUTHOR_EMAIL": "extra@example.com",
		// Provided by the process environment: kept.
		"GIT_COMMITTER_EMAIL": "ops@example.com",
		// Absent everywhere: defaulted.
		"GIT_COMMITTER_NAME":  DefaultIdentityName,
		"GIT_EDITOR":          "true",
		"LC_ALL":              "C",
		"GIT_TERMINAL_PROMPT": "0",
	}
	for key, want := range checks {
		got, found := lastEnvValue(env, key)
		if !found {
			t.Errorf("%s missing from env", key)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// TestRun_CancellationKillsProcessGroup verifies that a cancelled context
// terminates git and the helper processes it spawned, so Run returns
// promptly instead of waiting for the orphaned child to release stdout.
func TestRun_CancellationKillsProcessGroup(t *testing.T) {
	requireGitMinVersion(t, 2, 38)
	runner, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	// The alias spawns a shell child that holds the pipes open for 30s.
	_, err = runner.Run(ctx, "-c", "alias.hang=!sleep 30", "hang")
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Run took %s after cancellation; the process group was not killed", elapsed)
	}
}

// TestRun_DefaultTimeoutApplies verifies that a context without a deadline
// is bounded by DefaultTimeout.
func TestRun_DefaultTimeoutApplies(t *testing.T) {
	requireGitMinVersion(t, 2, 38)
	runner, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	old := DefaultTimeout
	DefaultTimeout = 300 * time.Millisecond
	t.Cleanup(func() { DefaultTimeout = old })

	start := time.Now()
	_, err = runner.Run(context.Background(), "-c", "alias.hang=!sleep 30", "hang")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Run took %s; DefaultTimeout was not applied", elapsed)
	}
}
