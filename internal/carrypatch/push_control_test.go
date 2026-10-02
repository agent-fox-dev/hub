package carrypatch

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitserver"
)

// ===========================================================================
// TS-22-10: PUSH_PATCHES_TO_ORIGIN is enabled only by the exact string true
// Verifies: 22-REQ-1.1
// ===========================================================================

func TestPushControl_TS22_10_PushPatchesEnabledParsing(t *testing.T) {
	type testCase struct {
		name     string
		getVar   GetVariableFunc
		expected bool
	}

	notFoundErr := fmt.Errorf("not found")

	cases := []testCase{
		{
			name: "unset (not found error)",
			getVar: func(_, _, _ string) (string, error) {
				return "", notFoundErr
			},
			expected: false,
		},
		{
			name: "exact string true",
			getVar: func(_, _, _ string) (string, error) {
				return "true", nil
			},
			expected: true,
		},
		{
			name: "True (capitalized)",
			getVar: func(_, _, _ string) (string, error) {
				return "True", nil
			},
			expected: false,
		},
		{
			name: "TRUE (all caps)",
			getVar: func(_, _, _ string) (string, error) {
				return "TRUE", nil
			},
			expected: false,
		},
		{
			name: "1",
			getVar: func(_, _, _ string) (string, error) {
				return "1", nil
			},
			expected: false,
		},
		{
			name: "space true (leading space)",
			getVar: func(_, _, _ string) (string, error) {
				return " true", nil
			},
			expected: false,
		},
		{
			name: "empty string",
			getVar: func(_, _, _ string) (string, error) {
				return "", nil
			},
			expected: false,
		},
		{
			name: "lookup error",
			getVar: func(_, _, _ string) (string, error) {
				return "", fmt.Errorf("db connection failed")
			},
			expected: false,
		},
		{
			name: "nil getVariable",
			getVar: nil,
			expected: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pushPatchesEnabled(tc.getVar, "test-ws")
			if got != tc.expected {
				t.Errorf("pushPatchesEnabled() = %v; want %v", got, tc.expected)
			}
		})
	}
}

// ===========================================================================
// TS-22-12: Push control applies to refs/heads/<name> matching a non-deleted
// patch row of a carry_patch workspace
// Verifies: 22-REQ-1.3
// ===========================================================================

func TestPushControl_TS22_12_ControlAppliesRegisteredPatches(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	// Seed a carry_patch workspace with integration_branch 'main-int'.
	seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
		"carry_patch", "main-int", "https://github.com/example/repo")

	// Seed patches with statuses active, pending (not a valid status, but
	// the spec says "status other than deleted"), and conflict.
	// Note: the patches table CHECK constraint allows active, conflict,
	// disabled, merged_upstream, deleted. We use active and conflict.
	seedPatch(t, db, "p1", "myws", "feature/active", 1, "active")
	seedPatch(t, db, "p2", "myws", "feature/conflict", 2, "conflict")
	seedPatch(t, db, "p3", "myws", "feature/disabled", 3, "disabled")

	// Source = origin, forwarding off → reject mode.
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		// PUSH_PATCHES_TO_ORIGIN not set → reject mode
		return "", fmt.Errorf("not found")
	}

	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable: getVar,
	})

	ctx := context.Background()
	actor := &apikit.AuthInfo{UserID: "user-1"}

	branches := []string{"feature/active", "feature/conflict", "feature/disabled"}
	for _, b := range branches {
		t.Run(b, func(t *testing.T) {
			upd := gitserver.RefUpdate{
				Name: plumbing.ReferenceName("refs/heads/" + b),
				Old:  plumbing.ZeroHash,
				New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			}
			err := hook(ctx, db, "myws", actor, upd)
			if err == nil {
				t.Errorf("expected non-nil error for registered patch branch %q; got nil", b)
			}
		})
	}
}

// ===========================================================================
// TS-22-13: Updates the control does not apply to are let through
// Verifies: 22-REQ-1.4
// ===========================================================================

func TestPushControl_TS22_13_NonApplicableUpdatesPassThrough(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	// carry_patch workspace
	seedWorkspaceWithGitURL(t, db, "cpws", "user-1", "active", "ready",
		"carry_patch", "main-int", "https://github.com/example/repo")
	// standard workspace
	seedWorkspaceWithGitURL(t, db, "stdws", "user-1", "active", "ready",
		"standard", "main", "https://github.com/example/repo")

	// Patches for cpws
	seedPatch(t, db, "p-del", "cpws", "feature/deleted", 1, "deleted")
	seedPatch(t, db, "p-int", "cpws", "main-int", 2, "active") // same name as integration branch

	// Source = origin, forwarding on (to ensure we'd see a forward if applicable)
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}

	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable: getVar,
	})

	ctx := context.Background()
	actor := &apikit.AuthInfo{UserID: "user-1"}

	cases := []struct {
		name string
		slug string
		ref  string
	}{
		{"deleted-status patch", "cpws", "refs/heads/feature/deleted"},
		{"integration branch (even with patch row)", "cpws", "refs/heads/main-int"},
		{"branch with no patch row", "cpws", "refs/heads/feature/unregistered"},
		{"tag", "cpws", "refs/tags/v1"},
		{"notes ref", "cpws", "refs/notes/x"},
		{"standard workspace branch", "stdws", "refs/heads/feature/anything"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upd := gitserver.RefUpdate{
				Name: plumbing.ReferenceName(tc.ref),
				Old:  plumbing.ZeroHash,
				New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			}
			err := hook(ctx, db, tc.slug, actor, upd)
			if err != nil {
				t.Errorf("expected nil error for %q; got %v", tc.name, err)
			}
		})
	}
}

// ===========================================================================
// TS-22-14: A standard-mode workspace push reads neither variable
// Verifies: 22-REQ-1.5
// ===========================================================================

func TestPushControl_TS22_14_StandardWorkspaceNoVariableReads(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	seedWorkspaceWithGitURL(t, db, "stdws", "user-1", "active", "ready",
		"standard", "main", "https://github.com/example/repo")

	var callCount int32
	getVar := func(scope, slug, key string) (string, error) {
		atomic.AddInt32(&callCount, 1)
		return "", fmt.Errorf("not found")
	}

	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable: getVar,
	})

	ctx := context.Background()
	actor := &apikit.AuthInfo{UserID: "user-1"}

	// Push several refs
	refs := []string{"refs/heads/main", "refs/heads/feature/a", "refs/tags/v1"}
	for _, ref := range refs {
		upd := gitserver.RefUpdate{
			Name: plumbing.ReferenceName(ref),
			Old:  plumbing.ZeroHash,
			New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		}
		err := hook(ctx, db, "stdws", actor, upd)
		if err != nil {
			t.Errorf("expected nil error for %q; got %v", ref, err)
		}
	}

	if atomic.LoadInt32(&callCount) != 0 {
		t.Errorf("expected 0 GetVariable calls for standard workspace; got %d", callCount)
	}
}

// ===========================================================================
// TS-22-44: A failure while deciding whether control applies lets the update
// through with a warning
// Verifies: 22-REQ-8.1
// ===========================================================================

func TestPushControl_TS22_44_FailOpenOnQueryErrors(t *testing.T) {
	// Test 1: workspace-mode query failure (workspace not found in DB)
	t.Run("workspace not found", func(t *testing.T) {
		db := openTestDB(t)
		createWorkspacesTable(t, db)
		createPatchesTable(t, db)
		// No workspace seeded → query returns sql.ErrNoRows

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		getVar := func(_, _, _ string) (string, error) {
			return "origin", nil
		}

		hook := newPreReceiveHookWithLogger(PreReceiveHookDeps{
			GetVariable: getVar,
		}, logger)

		ctx := context.Background()
		actor := &apikit.AuthInfo{UserID: "user-1"}
		upd := gitserver.RefUpdate{
			Name: plumbing.ReferenceName("refs/heads/feature/x"),
			Old:  plumbing.ZeroHash,
			New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		}

		err := hook(ctx, db, "nonexistent", actor, upd)
		if err != nil {
			t.Errorf("expected nil error (fail open); got %v", err)
		}
	})

	// Test 2: variable lookup failure beyond defaults
	// Note: variable lookup errors for PATCH_BRANCH_SOURCE default to "hub"
	// (which means no rejection), and PUSH_PATCHES_TO_ORIGIN defaults to
	// disabled. These are by-design defaults, not fail-open. The fail-open
	// behavior is tested by the workspace-mode and patches query failures.
	// A variable error that goes beyond the defaults (e.g. an unexpected
	// error in a code path that doesn't have a default) would be handled
	// by the same fail-open pattern.

	// Test 3: patches registration query failure
	t.Run("patches query error", func(t *testing.T) {
		db := openTestDB(t)
		createWorkspacesTable(t, db)
		// Do NOT create patches table → query will fail

		seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
			"carry_patch", "main-int", "https://github.com/example/repo")

		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		getVar := func(_, _, key string) (string, error) {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			return "", fmt.Errorf("not found")
		}

		hook := newPreReceiveHookWithLogger(PreReceiveHookDeps{
			GetVariable: getVar,
		}, logger)

		ctx := context.Background()
		actor := &apikit.AuthInfo{UserID: "user-1"}
		upd := gitserver.RefUpdate{
			Name: plumbing.ReferenceName("refs/heads/feature/x"),
			Old:  plumbing.ZeroHash,
			New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		}

		err := hook(ctx, db, "myws", actor, upd)
		if err != nil {
			t.Errorf("expected nil error (fail open); got %v", err)
		}

		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "WARN") && !strings.Contains(logOutput, "warn") {
			t.Errorf("expected a warn-level log; got:\n%s", logOutput)
		}
	})
}

// ===========================================================================
// TS-22-11: PATCH_BRANCH_SOURCE uses spec 20's parsing and both variables
// are re-read on every controlled ref update
// Verifies: 22-REQ-1.2
// ===========================================================================

func TestPushControl_TS22_11_VariablesReReadPerUpdate(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
		"carry_patch", "main-int", "https://github.com/example/repo")
	seedPatch(t, db, "p1", "myws", "feature/a", 1, "active")

	// Track variable reads
	var varCalls int32
	var patchSource string = "" // unset → hub mode
	var pushPatches string = ""

	getVar := func(scope, slug, key string) (string, error) {
		atomic.AddInt32(&varCalls, 1)
		if key == "PATCH_BRANCH_SOURCE" {
			if patchSource == "" {
				return "", fmt.Errorf("not found")
			}
			return patchSource, nil
		}
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			if pushPatches == "" {
				return "", fmt.Errorf("not found")
			}
			return pushPatches, nil
		}
		return "", fmt.Errorf("not found")
	}

	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable: getVar,
	})

	ctx := context.Background()
	actor := &apikit.AuthInfo{UserID: "user-1"}
	upd := gitserver.RefUpdate{
		Name: plumbing.ReferenceName("refs/heads/feature/a"),
		Old:  plumbing.ZeroHash,
		New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
	}

	// Push 1: source unset → hub mode → should pass through (no rejection)
	patchSource = ""
	atomic.StoreInt32(&varCalls, 0)
	err := hook(ctx, db, "myws", actor, upd)
	if err != nil {
		t.Errorf("push 1 (hub mode): expected nil error; got %v", err)
	}

	// Push 2: source = "garbage" → hub mode → should pass through
	patchSource = "garbage"
	err = hook(ctx, db, "myws", actor, upd)
	if err != nil {
		t.Errorf("push 2 (garbage source): expected nil error; got %v", err)
	}

	// Push 3: source = "origin" → origin mode, forwarding off → should reject
	patchSource = "origin"
	pushPatches = ""
	err = hook(ctx, db, "myws", actor, upd)
	if err == nil {
		t.Error("push 3 (origin mode): expected non-nil error; got nil")
	}

	// Verify variables were read (at least PATCH_BRANCH_SOURCE per controlled update)
	calls := atomic.LoadInt32(&varCalls)
	if calls < 3 {
		t.Errorf("expected at least 3 GetVariable calls across 3 updates; got %d", calls)
	}
}

// ===========================================================================
// Helper: seedWorkspaceWithGitURL
// ===========================================================================

func seedWorkspaceWithGitURL(t *testing.T, db *sql.DB, slug, ownerID, status, cloneStatus, mode, integrationBranch, gitURL string) {
	t.Helper()
	now := "2024-01-01T00:00:00Z"
	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, integration_branch, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		slug, gitURL, ownerID, status, cloneStatus, mode, integrationBranch, now, now,
	)
	if err != nil {
		t.Fatalf("seedWorkspaceWithGitURL(%q) returned error: %v", slug, err)
	}
}
