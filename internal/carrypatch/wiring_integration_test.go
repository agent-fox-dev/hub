package carrypatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/gitserver"
)

// ===========================================================================
// TS-22-9 (integration): info/refs, clone, fetch and pushes to unregistered
// branches, integration branch, tags, other refs and standard workspaces
// are unchanged with the carry-patch hook wired
// Verifies: 22-REQ-8.5
// ===========================================================================

func TestWiringIntegration_TS22_9_UnchangedBehaviourWithHookWired(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		// PUSH_PATCHES_TO_ORIGIN unset → reject mode
		return "", fmt.Errorf("not found")
	}

	// Create a carry_patch workspace with the hook registered.
	env := newRejectTestEnv(t, "https://example.com/fork.git", getVar)

	// Register a registered patch branch so we can verify it's the only
	// one affected.
	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Also seed a standard workspace.
	now := "2024-01-01T00:00:00Z"
	mustExec(t, env.db, `INSERT INTO orgs (id, name, slug, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
		"org-std", "Std Org", "stdorg", now, now)
	mustExec(t, env.db, `INSERT INTO org_members (org_id, user_id, created_at) VALUES (?, ?, ?)`,
		"org-std", "user-1", now)
	mustExec(t, env.db,
		`INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, clone_status, workspace_mode, integration_branch, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', 'ready', 'standard', '', ?, ?)`,
		"stdws", "https://example.com/std.git", "user-1", "org-std", now, now)

	// Initialize the standard workspace trunk.
	stdTrunk := filepath.Join(env.workspaceRoot, "stdws", "trunk")
	if err := os.MkdirAll(stdTrunk, 0o755); err != nil {
		t.Fatalf("mkdir stdws trunk: %v", err)
	}
	initRejectRepo(t, stdTrunk)

	// Build the standard workspace remote URL.
	stdRemote := strings.Replace(env.remote, "/myorg/myws.git", "/stdorg/stdws.git", 1)

	// --- Test 1: info/refs advertisement ---
	clone := t.TempDir()
	runGitCmdR(t, "", "clone", env.remote, clone)
	// If clone succeeded, info/refs worked.

	// --- Test 2: fetch ---
	// Add a commit on the trunk so there's something new to fetch.
	addCommitR(t, env.trunk, "fetch-test.txt", "fetch test commit")
	runGitCmdR(t, clone, "fetch", "origin")

	// --- Test 3: Push to an unregistered branch ---
	runGitCmdR(t, clone, "checkout", "-b", "feature")
	addCommitR(t, clone, "feature.txt", "feature commit")
	runGitCmdR(t, clone, "push", "origin", "feature")

	// Verify feature was written.
	featureSHA := runGitCmdR(t, clone, "rev-parse", "feature")
	trunkFeature := runGitCmdR(t, env.trunk, "rev-parse", "refs/heads/feature")
	if trunkFeature != featureSHA {
		t.Errorf("feature ref: trunk = %s; want %s", trunkFeature, featureSHA)
	}

	// --- Test 4: Push to the integration branch ---
	// The workspace's integration_branch is "main-int".
	runGitCmdR(t, clone, "checkout", "-b", "main-int")
	addCommitR(t, clone, "int.txt", "integration commit")
	runGitCmdR(t, clone, "push", "origin", "main-int")

	intSHA := runGitCmdR(t, clone, "rev-parse", "main-int")
	trunkInt := runGitCmdR(t, env.trunk, "rev-parse", "refs/heads/main-int")
	if trunkInt != intSHA {
		t.Errorf("integration ref: trunk = %s; want %s", trunkInt, intSHA)
	}

	// --- Test 5: Push a tag ---
	runGitCmdR(t, clone, "tag", "v1")
	runGitCmdR(t, clone, "push", "origin", "v1")

	_, tagErr := gitCmdOutR(env.trunk, "rev-parse", "--verify", "refs/tags/v1")
	if tagErr != nil {
		t.Error("refs/tags/v1 should exist on trunk after push")
	}

	// --- Test 6: Push to refs/notes/x ---
	// Create a note ref. We need to push a commit to a non-standard ref.
	addCommitR(t, clone, "note.txt", "note commit")
	noteSHA := runGitCmdR(t, clone, "rev-parse", "HEAD")
	runGitCmdR(t, clone, "push", "origin", noteSHA+":refs/notes/x")

	_, noteErr := gitCmdOutR(env.trunk, "rev-parse", "--verify", "refs/notes/x")
	if noteErr != nil {
		t.Error("refs/notes/x should exist on trunk after push")
	}

	// --- Test 7: Push to a branch in the standard workspace ---
	stdClone := t.TempDir()
	runGitCmdR(t, "", "clone", stdRemote, stdClone)
	runGitCmdR(t, stdClone, "checkout", "-b", "std-feature")
	addCommitR(t, stdClone, "std.txt", "std commit")
	runGitCmdR(t, stdClone, "push", "origin", "std-feature")

	stdFeatureSHA := runGitCmdR(t, stdClone, "rev-parse", "std-feature")
	trunkStdFeature := runGitCmdR(t, stdTrunk, "rev-parse", "refs/heads/std-feature")
	if trunkStdFeature != stdFeatureSHA {
		t.Errorf("std-feature ref: trunk = %s; want %s", trunkStdFeature, stdFeatureSHA)
	}

	// --- Verify: no origin push occurred ---
	// The carry_patch workspace has no origin remote configured, so any
	// attempt to push to origin would fail. The fact that all pushes
	// succeeded proves no origin push was attempted.
	// (The standard workspace also has no origin remote.)
}

// ===========================================================================
// TS-22-45 (integration): cmd/af-hub wiring registers the carry-patch
// pre-receive hook and builds the post-push hook with credentials,
// workspace root and emitter
// Verifies: 22-REQ-9.1
// ===========================================================================

func TestWiringIntegration_TS22_45_PreReceiveAndMirrorWired(t *testing.T) {
	// This test simulates what main.go does: it registers the pre-receive
	// hook and builds the post-push hook with mirror dependencies, then
	// verifies both work end-to-end.

	// --- Part 1: Origin-mode push is rejected (pre-receive hook registered) ---
	t.Run("origin_mode_rejected", func(t *testing.T) {
		getVar := func(scope, slug, key string) (string, error) {
			if key == "PATCH_BRANCH_SOURCE" {
				return "origin", nil
			}
			// PUSH_PATCHES_TO_ORIGIN unset → reject mode
			return "", fmt.Errorf("not found")
		}

		env := newRejectTestEnv(t, "https://example.com/fork.git", getVar)
		seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

		// Register the pre-receive hook as main.go would.
		hook := NewPreReceiveHook(PreReceiveHookDeps{
			GetVariable:   getVar,
			ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
			WorkspaceRoot: env.workspaceRoot,
		})
		gitserver.RegisterPreReceiveHook(hook)

		clone := t.TempDir()
		runGitCmdR(t, "", "clone", env.remote, clone)
		runGitCmdR(t, clone, "checkout", "-b", "p1")
		addCommitR(t, clone, "p1.txt", "p1 commit")

		out, err := gitCmdOutR(clone, "push", "origin", "p1")
		if err == nil {
			t.Fatal("expected push to fail")
		}
		if !strings.Contains(out, "branch is synced from origin") {
			t.Errorf("expected origin rejection message; got:\n%s", out)
		}
	})

	// --- Part 2: Hub-mode push with forwarding on reaches the local bare origin ---
	t.Run("hub_mode_mirror", func(t *testing.T) {
		getVar := func(scope, slug, key string) (string, error) {
			if key == "PUSH_PATCHES_TO_ORIGIN" {
				return "true", nil
			}
			// PATCH_BRANCH_SOURCE unset → hub mode (default)
			return "", fmt.Errorf("not found")
		}

		env := newMirrorTestEnv(t)
		seedPatch(t, env.db, "p1-id", "myws", "p1", 1, "active")

		// Create p1 on the trunk.
		runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
		addCommitR(t, env.trunk, "p1.txt", "p1 initial")
		runGitCmdR(t, env.trunk, "checkout", "main")

		emitter := &recordingEmitter{}

		// Build the post-push hook as main.go would, with all mirror deps.
		hook := NewPostPushRebuildHook(
			env.queue,
			getVar,
			PostPushMirrorDeps{
				ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
				WorkspaceRoot: env.workspaceRoot,
				Audit:         emitter,
			},
		)

		// Run the hook.
		hook(env.db, "myws", []string{"p1"})

		// Verify p1 reached the bare origin.
		if !mirrorRefExists(t, env.bareOrigin, "refs/heads/p1") {
			t.Fatal("origin should have p1 after hub-mode mirror")
		}
	})

	// --- Part 3: Verify main.go source uses the right functions ---
	t.Run("source_verification", func(t *testing.T) {
		mainSrc, err := os.ReadFile("../../cmd/af-hub/main.go")
		if err != nil {
			t.Fatalf("failed to read main.go: %v", err)
		}
		src := string(mainSrc)

		// Verify store.GetVariableValue is used.
		if !strings.Contains(src, "store.GetVariableValue") {
			t.Error("main.go should use store.GetVariableValue")
		}

		// Verify workspace.ResolveCloneAuth is used.
		if !strings.Contains(src, "workspace.ResolveCloneAuth") {
			t.Error("main.go should use workspace.ResolveCloneAuth")
		}

		// Verify no local auth struct is defined.
		if strings.Contains(src, "type authInfo struct") ||
			strings.Contains(src, "type AuthInfo struct") ||
			strings.Contains(src, "type credential struct") {
			t.Error("main.go should not define local auth structs")
		}

		// Verify RegisterPreReceiveHook is called.
		if !strings.Contains(src, "gitserver.RegisterPreReceiveHook") {
			t.Error("main.go should call gitserver.RegisterPreReceiveHook")
		}

		// Verify PostPushMirrorDeps is populated with real dependencies
		// (not the empty struct from task 6).
		if !strings.Contains(src, "PostPushMirrorDeps{") {
			t.Error("main.go should construct PostPushMirrorDeps")
		}
		// The PostPushMirrorDeps should have ResolveAuth, WorkspaceRoot, and Audit set.
		if strings.Contains(src, "PostPushMirrorDeps{}") {
			t.Error("main.go should not use empty PostPushMirrorDeps{}; wire real dependencies")
		}

		// Verify PreReceiveHookDeps is constructed with GetVariable, ResolveAuth, WorkspaceRoot.
		if !strings.Contains(src, "PreReceiveHookDeps{") {
			t.Error("main.go should construct PreReceiveHookDeps")
		}
	})
}
