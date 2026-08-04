package gitcmd

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// ========================================================================
// Spec 11 Task 3.2: Goroutine safety and race detection tests
// (TS-11-30, TS-11-31, TS-11-38)
// Requirements: 11-REQ-11.1, 11-REQ-11.2, 11-REQ-13.4
// ========================================================================

// TestGitRunner_ImmutableState verifies that GitRunner holds only immutable
// state after construction: its fields should be limited to simple types
// like string and []string, with no synchronization primitives (sync.Mutex,
// sync.WaitGroup, etc.) or shared mutable buffers.
//
// This test uses reflection to inspect the struct definition at runtime.
//
// TS-11-30
// Requirement: 11-REQ-11.1
func TestGitRunner_ImmutableState(t *testing.T) {
	rt := reflect.TypeOf(GitRunner{})

	// Forbidden type patterns that indicate mutable shared state.
	forbiddenPatterns := []string{
		"sync.Mutex",
		"sync.RWMutex",
		"sync.WaitGroup",
		"sync.Once",
		"sync.Cond",
		"sync.Map",
		"sync.Pool",
		"bytes.Buffer",
	}

	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		typeName := field.Type.String()

		for _, forbidden := range forbiddenPatterns {
			if strings.Contains(typeName, forbidden) {
				t.Errorf("GitRunner field %q has type %q which indicates mutable "+
					"shared state; GitRunner should hold only immutable state "+
					"(workDir string and env []string)",
					field.Name, typeName)
			}
		}

		// Also check for channel types which indicate mutable shared state.
		if field.Type.Kind() == reflect.Chan {
			t.Errorf("GitRunner field %q is a channel (%s); GitRunner should "+
				"hold only immutable state", field.Name, typeName)
		}
	}
}

// TestConcurrentRun verifies that multiple goroutines can call Run
// concurrently on the same *GitRunner instance without data races. Each
// invocation must create an independent exec.Cmd with its own stdout and
// stderr buffers, producing independent results.
//
// This test must be run with the -race flag to detect data races:
//
//	go test -race ./internal/gitcmd/... -run TestConcurrentRun
//
// TS-11-31
// Requirement: 11-REQ-11.2
//
// Also satisfies TS-11-38 (11-REQ-13.4): the integration test suite includes
// a goroutine-safety test that launches concurrent goroutines calling Run on
// the same *GitRunner and verifies no data races under go test -race.
func TestConcurrentRun(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepoWithCommit(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	const numGoroutines = 20

	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([]string, numGoroutines)
	errs := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = runner.Run(ctx, "rev-parse", "HEAD")
		}(i)
	}

	wg.Wait()

	// Verify all goroutines completed successfully with valid results.
	for i := 0; i < numGoroutines; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d returned error: %v", i, errs[i])
			continue
		}
		if len(results[i]) != 40 {
			t.Errorf("goroutine %d: SHA length = %d, want 40: %q",
				i, len(results[i]), results[i])
		}
		if !shaRegexp.MatchString(results[i]) {
			t.Errorf("goroutine %d: SHA %q does not match /^[0-9a-f]{40}$/",
				i, results[i])
		}
	}

	// All goroutines should return the same HEAD SHA.
	if numGoroutines > 1 && errs[0] == nil {
		expected := results[0]
		for i := 1; i < numGoroutines; i++ {
			if errs[i] == nil && results[i] != expected {
				t.Errorf("goroutine %d returned %q, want %q (same HEAD as goroutine 0)",
					i, results[i], expected)
			}
		}
	}
}
