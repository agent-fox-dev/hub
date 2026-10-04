package gitserver

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/txsvc/apikit"
)

// newProtectedRefsClientServer is like newGitClientServer but saves and
// restores the global pre-receive and post-push hooks around the test.
func newProtectedRefsClientServer(t *testing.T) (*gitTestEnv, string, string) {
	t.Helper()

	origPre := preReceiveHook
	origPost := postPushHook
	t.Cleanup(func() {
		preReceiveHook = origPre
		postPushHook = origPost
	})

	env, trunk, remote := newGitClientServer(t)
	return env, trunk, remote
}

// createBackupRef creates refs/hub/replaced/<branch> in the trunk at the given SHA.
func createBackupRef(t *testing.T, trunkPath, branch, sha string) {
	t.Helper()
	refName := "refs/hub/replaced/" + branch
	runGitCmd(t, trunkPath, "update-ref", refName, sha)
}

// refExists checks whether a ref exists in the given repo.
func refExists(t *testing.T, repoPath, refName string) bool {
	t.Helper()
	_, err := gitCmdOutput(repoPath, "rev-parse", "--verify", refName)
	return err == nil
}

// refValue returns the SHA a ref points to, or empty string if it doesn't exist.
func refValue(t *testing.T, repoPath, refName string) string {
	t.Helper()
	out, err := gitCmdOutput(repoPath, "rev-parse", "--verify", refName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// newGitClientServerForWorkspace starts a real HTTP server and returns the
// trunk path and an authenticated remote URL for the given workspace slug.
// It seeds the org, user, and workspace in the database.
func newGitClientServerForWorkspace(t *testing.T, slug string) (*gitTestEnv, string, string) {
	t.Helper()
	env := newGitTestEnv(t)
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, slug, "https://github.com/org/repo", "user-1", "org-1", "active")
	env.setCloneStatus(t, slug, "ready")
	trunk := env.initWorkspaceRepo(t, slug)

	srv := httptest.NewServer(env.echo)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("x-token-auth", "af_key_user1")
	u.Path = "/git/myorg/" + slug + ".git"
	return env, trunk, u.String()
}

// newGitClientServerWithCred starts a real HTTP server and returns the trunk
// path and an authenticated remote URL using the given credential.
func newGitClientServerWithCred(t *testing.T, slug, user, pass string) (*gitTestEnv, string, string) {
	t.Helper()
	env := newGitTestEnv(t)
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, slug, "https://github.com/org/repo", "user-1", "org-1", "active")
	env.setCloneStatus(t, slug, "ready")
	trunk := env.initWorkspaceRepo(t, slug)

	srv := httptest.NewServer(env.echo)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, pass)
	u.Path = "/git/myorg/" + slug + ".git"
	return env, trunk, u.String()
}

// TS-23-37: A real git push that creates, updates or deletes
// refs/hub/replaced/ is rejected with the fixed message and keeps the old value.
// Verifies: 23-REQ-6.1
func TestProtectedRefs_TS23_37_RejectCreateUpdateDelete(t *testing.T) {
	_, trunk, remote := newProtectedRefsClientServer(t)

	// Register a pre-receive hook that accepts everything — the protected
	// namespace rule must fire before the hook is consulted.
	var hookCalls int
	var hookMu sync.Mutex
	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, upd RefUpdate) error {
		hookMu.Lock()
		defer hookMu.Unlock()
		// Only count calls for refs/hub/replaced/ refs.
		if strings.HasPrefix(string(upd.Name), "refs/hub/replaced/") {
			hookCalls++
		}
		return nil
	})

	// Get the initial commit SHA from the trunk.
	initialSHA := runGitCmd(t, trunk, "rev-parse", "HEAD")

	// Create an existing backup ref in the trunk.
	createBackupRef(t, trunk, "feature", initialSHA)

	// Clone the repo.
	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)

	// Add a commit so we have a different SHA to push.
	addCommit(t, clone, "new.txt", "new commit")
	newSHA := runGitCmd(t, clone, "rev-parse", "HEAD")

	// --- Test 1: Create refs/hub/replaced/new ---
	out, err := gitCmdOutput(clone, "push", "origin", newSHA+":refs/hub/replaced/new")
	if err == nil {
		t.Error("expected push to refs/hub/replaced/new to fail")
	}
	if !strings.Contains(out, "refs/hub/replaced/ is maintained by the hub and cannot be pushed to") {
		t.Errorf("expected protected-namespace message in output; got:\n%s", out)
	}
	if refExists(t, trunk, "refs/hub/replaced/new") {
		t.Error("refs/hub/replaced/new should not exist after rejected create")
	}

	// --- Test 2: Update refs/hub/replaced/feature ---
	out, err = gitCmdOutput(clone, "push", "origin", newSHA+":refs/hub/replaced/feature")
	if err == nil {
		t.Error("expected push to refs/hub/replaced/feature to fail")
	}
	if !strings.Contains(out, "refs/hub/replaced/ is maintained by the hub and cannot be pushed to") {
		t.Errorf("expected protected-namespace message in output; got:\n%s", out)
	}
	// The backup ref should keep its old value.
	trunkBackup := refValue(t, trunk, "refs/hub/replaced/feature")
	if trunkBackup != initialSHA {
		t.Errorf("refs/hub/replaced/feature changed; got %s, want %s", trunkBackup, initialSHA)
	}

	// --- Test 3: Delete refs/hub/replaced/feature ---
	// Push a delete alongside a normal branch update so the pack is not empty.
	// The delete of the protected ref should be rejected while the normal
	// branch is accepted.
	addCommit(t, clone, "del.txt", "del commit")
	delSHA := runGitCmd(t, clone, "rev-parse", "HEAD")
	out, _ = gitCmdOutput(clone, "push", "origin",
		delSHA+":refs/heads/delbranch",
		":refs/hub/replaced/feature")
	if !strings.Contains(out, "refs/hub/replaced/ is maintained by the hub and cannot be pushed to") {
		t.Errorf("expected protected-namespace message for delete; got:\n%s", out)
	}
	// The backup ref should still exist with its original value.
	trunkBackup = refValue(t, trunk, "refs/hub/replaced/feature")
	if trunkBackup != initialSHA {
		t.Errorf("refs/hub/replaced/feature was deleted or changed; got %s, want %s", trunkBackup, initialSHA)
	}
	// The normal branch should have been written.
	if !refExists(t, trunk, "refs/heads/delbranch") {
		t.Error("refs/heads/delbranch should exist after mixed push")
	}

	// --- Verify the hook was never consulted for protected refs ---
	hookMu.Lock()
	calls := hookCalls
	hookMu.Unlock()
	if calls != 0 {
		t.Errorf("pre-receive hook was called %d times for protected refs; want 0", calls)
	}
}

// TS-23-38: Any ref under refs/hub/replaced/ is rejected for any workspace
// mode, credential and hook state.
// Verifies: 23-REQ-6.2
func TestProtectedRefs_TS23_38_PropertyAllModesCredsHooks(t *testing.T) {
	refNames := []string{
		"refs/hub/replaced/feature",
		"refs/hub/replaced/main",
		"refs/hub/replaced/deeply/nested/branch",
		"refs/hub/replaced/with-dashes",
		"refs/hub/replaced/with.dots",
	}

	hookStates := []struct {
		name string
		hook PreReceiveHookFunc
	}{
		{"no_hook", nil},
		{"accept_all_hook", func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, _ RefUpdate) error {
			return nil
		}},
	}

	for _, hookState := range hookStates {
		for _, refName := range refNames {
			name := hookState.name + "/" + strings.ReplaceAll(refName, "/", "_")
			t.Run(name, func(t *testing.T) {
				origPre := preReceiveHook
				origPost := postPushHook
				t.Cleanup(func() {
					preReceiveHook = origPre
					postPushHook = origPost
				})

				_, trunk, remote := newGitClientServer(t)
				RegisterPreReceiveHook(hookState.hook)

				clone := t.TempDir()
				runGitCmd(t, "", "clone", remote, clone)
				addCommit(t, clone, "x.txt", "commit")
				sha := runGitCmd(t, clone, "rev-parse", "HEAD")

				out, err := gitCmdOutput(clone, "push", "origin", sha+":"+refName)
				if err == nil {
					t.Errorf("expected push to %s to fail", refName)
				}
				if !strings.Contains(out, "refs/hub/replaced/ is maintained by the hub and cannot be pushed to") {
					t.Errorf("expected protected-namespace message for %s; got:\n%s", refName, out)
				}
				if refExists(t, trunk, refName) {
					t.Errorf("%s should not exist in trunk after rejected push", refName)
				}
			})
		}
	}

	// Additional sub-tests for different credential types.

	// Standard-mode workspace with api_key.
	t.Run("standard_mode_workspace", func(t *testing.T) {
		origPre := preReceiveHook
		origPost := postPushHook
		t.Cleanup(func() {
			preReceiveHook = origPre
			postPushHook = origPost
		})

		_, trunk, remote := newGitClientServerForWorkspace(t, "stdws")
		RegisterPreReceiveHook(nil)

		clone := t.TempDir()
		runGitCmd(t, "", "clone", remote, clone)
		addCommit(t, clone, "std.txt", "std commit")
		sha := runGitCmd(t, clone, "rev-parse", "HEAD")

		out, err := gitCmdOutput(clone, "push", "origin", sha+":refs/hub/replaced/feature")
		if err == nil {
			t.Error("expected push to refs/hub/replaced/feature to fail on standard workspace")
		}
		if !strings.Contains(out, "refs/hub/replaced/ is maintained by the hub and cannot be pushed to") {
			t.Errorf("expected protected-namespace message; got:\n%s", out)
		}
		if refExists(t, trunk, "refs/hub/replaced/feature") {
			t.Error("refs/hub/replaced/feature should not exist in trunk")
		}
	})

	// PAT credential with git:write.
	t.Run("pat_credential", func(t *testing.T) {
		origPre := preReceiveHook
		origPost := postPushHook
		t.Cleanup(func() {
			preReceiveHook = origPre
			postPushHook = origPost
		})

		_, _, remote := newGitClientServerWithCred(t, "patws", "x-token-auth", "af_pat_abc123")
		RegisterPreReceiveHook(nil)

		clone := t.TempDir()
		runGitCmd(t, "", "clone", remote, clone)
		addCommit(t, clone, "pat.txt", "pat commit")
		sha := runGitCmd(t, clone, "rev-parse", "HEAD")

		out, err := gitCmdOutput(clone, "push", "origin", sha+":refs/hub/replaced/feature")
		if err == nil {
			t.Error("expected push to refs/hub/replaced/feature to fail with PAT credential")
		}
		if !strings.Contains(out, "refs/hub/replaced/ is maintained by the hub and cannot be pushed to") {
			t.Errorf("expected protected-namespace message; got:\n%s", out)
		}
	})

	// Admin credential.
	t.Run("admin_credential", func(t *testing.T) {
		origPre := preReceiveHook
		origPost := postPushHook
		t.Cleanup(func() {
			preReceiveHook = origPre
			postPushHook = origPost
		})

		_, _, remote := newGitClientServerWithCred(t, "adminws", "x-admin-auth", "af_admin_test789")
		RegisterPreReceiveHook(nil)

		clone := t.TempDir()
		runGitCmd(t, "", "clone", remote, clone)
		addCommit(t, clone, "admin.txt", "admin commit")
		sha := runGitCmd(t, clone, "rev-parse", "HEAD")

		out, err := gitCmdOutput(clone, "push", "origin", sha+":refs/hub/replaced/feature")
		if err == nil {
			t.Error("expected push to refs/hub/replaced/feature to fail with admin credential")
		}
		if !strings.Contains(out, "refs/hub/replaced/ is maintained by the hub and cannot be pushed to") {
			t.Errorf("expected protected-namespace message; got:\n%s", out)
		}
	})
}

// TS-23-39: A push mixing a protected ref and a normal branch writes the
// branch and rejects only the protected ref.
// Verifies: 23-REQ-6.3
func TestProtectedRefs_TS23_39_MixedPushWritesBranchRejectsProtected(t *testing.T) {
	_, trunk, remote := newProtectedRefsClientServer(t)
	RegisterPreReceiveHook(nil) // No hook — protection is built-in.

	initialSHA := runGitCmd(t, trunk, "rev-parse", "HEAD")
	createBackupRef(t, trunk, "feature", initialSHA)

	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)
	addCommit(t, clone, "mixed.txt", "mixed commit")
	newSHA := runGitCmd(t, clone, "rev-parse", "HEAD")

	// Push both a normal branch and a protected ref in one push.
	out, _ := gitCmdOutput(clone, "push", "origin",
		newSHA+":refs/heads/newbranch",
		newSHA+":refs/hub/replaced/feature")

	// The normal branch should be written.
	trunkNewBranch := refValue(t, trunk, "refs/heads/newbranch")
	if trunkNewBranch != newSHA {
		t.Errorf("refs/heads/newbranch: got %s, want %s", trunkNewBranch, newSHA)
	}

	// The protected ref should keep its old value.
	trunkBackup := refValue(t, trunk, "refs/hub/replaced/feature")
	if trunkBackup != initialSHA {
		t.Errorf("refs/hub/replaced/feature changed; got %s, want %s", trunkBackup, initialSHA)
	}

	// Output should contain ng for the protected ref.
	if !strings.Contains(out, "refs/hub/replaced/ is maintained by the hub and cannot be pushed to") {
		t.Errorf("expected protected-namespace message in output; got:\n%s", out)
	}
}

// TS-23-40: A rejected protected ref triggers no head_sha update, audit
// entry or post-push hook.
// Verifies: 23-REQ-6.4
func TestProtectedRefs_TS23_40_RejectedRefNoSideEffects(t *testing.T) {
	env, trunk, remote := newProtectedRefsClientServer(t)

	// Set up a recording audit emitter.
	auditMock := newGitAuditEmitter()
	origEmitter := defaultAuditEmitter
	defaultAuditEmitter = auditMock
	t.Cleanup(func() { defaultAuditEmitter = origEmitter })

	// Set up a recording post-push hook.
	var mu sync.Mutex
	var hookCalled bool
	RegisterPostPushHook(func(_ *sql.DB, _ string, branches []string) {
		mu.Lock()
		defer mu.Unlock()
		hookCalled = true
	})

	RegisterPreReceiveHook(nil) // No pre-receive hook.

	initialSHA := runGitCmd(t, trunk, "rev-parse", "HEAD")
	setHeadSHAInDB(t, env, "myws", initialSHA)

	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)
	addCommit(t, clone, "prot.txt", "protected commit")
	newSHA := runGitCmd(t, clone, "rev-parse", "HEAD")

	// Push only a protected ref.
	_, _ = gitCmdOutput(clone, "push", "origin", newSHA+":refs/hub/replaced/f")

	// Wait briefly for any async hook.
	time.Sleep(200 * time.Millisecond)

	// head_sha should be unchanged (the trunk HEAD didn't change).
	var headSHA string
	err := env.db.QueryRow(
		`SELECT COALESCE(head_sha, '') FROM workspaces WHERE slug = ?`, "myws",
	).Scan(&headSHA)
	if err != nil {
		t.Fatalf("failed to query head_sha: %v", err)
	}
	if headSHA != initialSHA {
		t.Errorf("head_sha changed after rejected push; got %q, want %q", headSHA, initialSHA)
	}

	// No audit event should have been emitted.
	events := auditMock.Events()
	if len(events) != 0 {
		t.Errorf("expected 0 audit events for rejected-only push, got %d", len(events))
	}

	// Post-push hook should not have been called.
	mu.Lock()
	called := hookCalled
	mu.Unlock()
	if called {
		t.Error("post-push hook should not be called for rejected-only push")
	}
}

// TS-23-41: Ref names outside refs/hub/replaced/ are never rejected by the
// protected-namespace rule.
// Verifies: 23-REQ-6.5
func TestProtectedRefs_TS23_41_PropertyNonProtectedRefsAccepted(t *testing.T) {
	// Table of ref names that should NOT be rejected.
	refNames := []string{
		"refs/hub/forward/x",
		"refs/heads/hub/replaced/x",
		"refs/heads/feature",
		"refs/tags/v1.0",
		"refs/heads/main",
	}

	for _, refName := range refNames {
		t.Run(strings.ReplaceAll(refName, "/", "_"), func(t *testing.T) {
			origPre := preReceiveHook
			origPost := postPushHook
			t.Cleanup(func() {
				preReceiveHook = origPre
				postPushHook = origPost
			})

			_, trunk, remote := newGitClientServer(t)
			RegisterPreReceiveHook(nil)

			clone := t.TempDir()
			runGitCmd(t, "", "clone", remote, clone)
			addCommit(t, clone, "ok.txt", "ok commit")
			sha := runGitCmd(t, clone, "rev-parse", "HEAD")

			out, err := gitCmdOutput(clone, "push", "origin", sha+":"+refName)
			if err != nil {
				t.Errorf("push to %s should succeed; got error: %v\n%s", refName, err, out)
			}
			if strings.Contains(out, "maintained by the hub") {
				t.Errorf("protected-namespace message should not appear for %s; got:\n%s", refName, out)
			}
			// The ref should exist in the trunk.
			if !refExists(t, trunk, refName) {
				t.Errorf("%s should exist in trunk after accepted push", refName)
			}
		})
	}
}

// TS-23-42: ls-remote lists the backup ref and a client can fetch it by name.
// Verifies: 23-REQ-6.6
func TestProtectedRefs_TS23_42_LsRemoteAndFetchBackupRef(t *testing.T) {
	_, trunk, remote := newProtectedRefsClientServer(t)
	RegisterPreReceiveHook(nil)

	// Create a commit that is only reachable from the backup ref.
	addCommit(t, trunk, "backup-only.txt", "backup only commit")
	backupSHA := runGitCmd(t, trunk, "rev-parse", "HEAD")

	// Create the backup ref.
	createBackupRef(t, trunk, "feature", backupSHA)

	// Reset trunk HEAD back so the backup commit is only reachable via the backup ref.
	runGitCmd(t, trunk, "reset", "--hard", "HEAD~1")

	// --- ls-remote should list the backup ref ---
	lsOut := runGitCmd(t, "", "ls-remote", remote)
	if !strings.Contains(lsOut, "refs/hub/replaced/feature") {
		t.Errorf("ls-remote should list refs/hub/replaced/feature; got:\n%s", lsOut)
	}
	if !strings.Contains(lsOut, backupSHA) {
		t.Errorf("ls-remote should show backup SHA %s; got:\n%s", backupSHA, lsOut)
	}

	// --- A client can fetch the backup ref by name ---
	fetchDir := t.TempDir()
	runGitCmd(t, "", "init", fetchDir)
	runGitCmd(t, fetchDir, "remote", "add", "hub", remote)
	runGitCmd(t, fetchDir, "fetch", "hub", "refs/hub/replaced/feature")

	// FETCH_HEAD should be the backup SHA.
	fetchHead := runGitCmd(t, fetchDir, "rev-parse", "FETCH_HEAD")
	if fetchHead != backupSHA {
		t.Errorf("FETCH_HEAD = %s; want %s", fetchHead, backupSHA)
	}
}

// TS-23-43: Hub-internal writes to refs/hub/replaced/ are unaffected by the
// git server rule.
// Verifies: 23-REQ-6.7
func TestProtectedRefs_TS23_43_InternalWritesUnaffected(t *testing.T) {
	_, trunk, remote := newProtectedRefsClientServer(t)
	RegisterPreReceiveHook(nil)

	initialSHA := runGitCmd(t, trunk, "rev-parse", "HEAD")

	// Add a second commit for a different SHA.
	addCommit(t, trunk, "second.txt", "second commit")
	secondSHA := runGitCmd(t, trunk, "rev-parse", "HEAD")

	// --- Internal write: create backup ref ---
	runGitCmd(t, trunk, "update-ref", "refs/hub/replaced/feature", initialSHA)
	if !refExists(t, trunk, "refs/hub/replaced/feature") {
		t.Fatal("internal create of refs/hub/replaced/feature failed")
	}

	// A client fetch should see the value.
	fetchDir := t.TempDir()
	runGitCmd(t, "", "init", fetchDir)
	runGitCmd(t, fetchDir, "remote", "add", "hub", remote)
	runGitCmd(t, fetchDir, "fetch", "hub", "refs/hub/replaced/feature")
	fetchHead := runGitCmd(t, fetchDir, "rev-parse", "FETCH_HEAD")
	if fetchHead != initialSHA {
		t.Errorf("after create: FETCH_HEAD = %s; want %s", fetchHead, initialSHA)
	}

	// --- Internal write: overwrite backup ref ---
	runGitCmd(t, trunk, "update-ref", "refs/hub/replaced/feature", secondSHA)
	val := refValue(t, trunk, "refs/hub/replaced/feature")
	if val != secondSHA {
		t.Errorf("after overwrite: ref = %s; want %s", val, secondSHA)
	}

	// A client fetch should see the new value.
	runGitCmd(t, fetchDir, "fetch", "hub", "refs/hub/replaced/feature")
	fetchHead = runGitCmd(t, fetchDir, "rev-parse", "FETCH_HEAD")
	if fetchHead != secondSHA {
		t.Errorf("after overwrite fetch: FETCH_HEAD = %s; want %s", fetchHead, secondSHA)
	}

	// --- Internal write: delete backup ref ---
	runGitCmd(t, trunk, "update-ref", "-d", "refs/hub/replaced/feature")
	if refExists(t, trunk, "refs/hub/replaced/feature") {
		t.Error("internal delete of refs/hub/replaced/feature failed")
	}

	// ls-remote should no longer list it.
	lsOut := runGitCmd(t, "", "ls-remote", remote)
	if strings.Contains(lsOut, "refs/hub/replaced/feature") {
		t.Error("ls-remote should not list deleted refs/hub/replaced/feature")
	}
}
