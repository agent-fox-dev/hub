package carrypatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/gitserver"
)

// ===========================================================================
// Integration test infrastructure for reject-mode tests.
//
// These tests exercise the full git push path through the hub's git server
// with the carry-patch pre-receive hook registered, using a real git client.
// ===========================================================================

// rejectTestEnv holds the test infrastructure for reject-mode integration tests.
type rejectTestEnv struct {
	db            *sql.DB
	trunk         string // path to the workspace trunk repo
	remote        string // authenticated URL for git push
	workspaceRoot string
	srv           *httptest.Server
}

// newRejectTestEnv creates a full git server test environment with:
// - in-memory SQLite DB with all required tables (workspaces, patches, auth)
// - a carry_patch workspace "myws" in org "myorg"
// - the carry-patch pre-receive hook registered
// - a real httptest.Server for git push
func newRejectTestEnv(t *testing.T, gitURL string, getVar GetVariableFunc) *rejectTestEnv {
	t.Helper()

	db := openRejectTestDB(t)

	// Seed org and membership.
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO orgs (id, name, slug, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
		"org-1", "My Org", "myorg", now, now)
	mustExec(t, db, `INSERT INTO org_members (org_id, user_id, created_at) VALUES (?, ?, ?)`,
		"org-1", "user-1", now)

	// Seed carry_patch workspace.
	mustExec(t, db,
		`INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, clone_status, workspace_mode, integration_branch, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', 'ready', 'carry_patch', 'main-int', ?, ?)`,
		"myws", gitURL, "user-1", "org-1", now, now)

	wsRoot := t.TempDir()

	e := newRejectEcho(t, db, wsRoot)

	// Initialize the trunk repo.
	trunkPath := filepath.Join(wsRoot, "myws", "trunk")
	if err := os.MkdirAll(trunkPath, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	initRejectRepo(t, trunkPath)

	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("x-token-auth", "af_key_user1")
	u.Path = "/git/myorg/myws.git"

	// Save and restore global hooks.
	t.Cleanup(func() {
		gitserver.RegisterPreReceiveHook(nil)
		gitserver.RegisterPostPushHook(nil)
	})

	// Register the carry-patch pre-receive hook.
	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable: getVar,
	})
	gitserver.RegisterPreReceiveHook(hook)

	return &rejectTestEnv{
		db:            db,
		trunk:         trunkPath,
		remote:        u.String(),
		workspaceRoot: wsRoot,
		srv:           srv,
	}
}

// newRejectEcho creates an echo instance with git handlers mounted.
func newRejectEcho(t *testing.T, db *sql.DB, wsRoot string) *echo.Echo {
	t.Helper()
	e := echo.New()
	if err := gitserver.MountGitHandlers(e, db, wsRoot); err != nil {
		t.Fatalf("MountGitHandlers: %v", err)
	}
	return e
}

// openRejectTestDB creates an in-memory SQLite DB with all tables needed
// for the git server (workspaces with workspace_mode/integration_branch,
// patches, auth tables).
func openRejectTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)

	mustExec(t, db, `CREATE TABLE IF NOT EXISTS workspaces (
		slug              TEXT PRIMARY KEY,
		git_url           TEXT NOT NULL,
		branch            TEXT,
		owner_id          TEXT NOT NULL,
		org_id            TEXT,
		status            TEXT NOT NULL DEFAULT 'active',
		clone_status      TEXT NOT NULL DEFAULT 'ready',
		head_sha          TEXT,
		display_name      TEXT NOT NULL DEFAULT '',
		description       TEXT NOT NULL DEFAULT '',
		created_at        TEXT NOT NULL,
		updated_at        TEXT NOT NULL,
		workspace_mode    TEXT NOT NULL DEFAULT 'standard',
		integration_branch TEXT
	)`)

	createPatchesTable(t, db)

	// Auth tables needed by GitAuthMiddleware.
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS orgs (
		id TEXT NOT NULL PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		slug TEXT NOT NULL UNIQUE,
		url TEXT,
		status TEXT NOT NULL DEFAULT 'active',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS org_members (
		org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
		user_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (org_id, user_id)
	)`)
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS users (
		id TEXT NOT NULL PRIMARY KEY,
		username TEXT NOT NULL UNIQUE,
		email TEXT NOT NULL,
		full_name TEXT,
		role TEXT NOT NULL DEFAULT 'user',
		status TEXT NOT NULL DEFAULT 'active',
		provider TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS pats (
		token_id TEXT NOT NULL PRIMARY KEY,
		user_id TEXT NOT NULL,
		name TEXT NOT NULL,
		secret_hash TEXT NOT NULL,
		permissions TEXT NOT NULL,
		expires_days INTEGER NOT NULL,
		expires_at TEXT,
		revoked_at TEXT,
		created_at TEXT NOT NULL
	)`)
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS api_keys (
		key_id TEXT NOT NULL PRIMARY KEY,
		user_id TEXT NOT NULL,
		secret_hash TEXT NOT NULL,
		expires_days INTEGER NOT NULL,
		expires_at TEXT,
		revoked_at TEXT,
		created_at TEXT NOT NULL
	)`)
	mustExec(t, db, `CREATE TABLE IF NOT EXISTS admin_config (
		key TEXT NOT NULL PRIMARY KEY,
		value TEXT NOT NULL
	)`)

	// Seed test credentials matching gitserver's seedTestCredentials.
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db,
		`INSERT INTO users (id, username, email, role, status, provider, provider_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', 'github', ?, ?, ?)`,
		"user-1", "testuser1", "user1@test.com", "user", "gh-user-1", now, now)

	// API key: af_key_user1 → key_id "user1", secret_hash = sha256("user1")
	h := sha256.Sum256([]byte("user1"))
	hash := hex.EncodeToString(h[:])
	mustExec(t, db,
		`INSERT INTO api_keys (key_id, user_id, secret_hash, expires_days, created_at)
		 VALUES (?, ?, ?, 365, ?)`,
		"user1", "user-1", hash, now)

	return db
}

// mustExec executes a SQL statement and fails the test on error.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("mustExec(%q): %v", query, err)
	}
}

// initRejectRepo initializes a git repo with an initial commit.
func initRejectRepo(t *testing.T, path string) {
	t.Helper()
	runGitCmdR(t, "", "init", "-b", "main", path)
	runGitCmdR(t, path, "config", "user.name", "Test")
	runGitCmdR(t, path, "config", "user.email", "test@test.com")
	writeFileHelper(t, filepath.Join(path, "README.md"), "# test\n")
	runGitCmdR(t, path, "add", ".")
	runGitCmdR(t, path, "commit", "-m", "init")
}

// runGitCmdR runs a git command and fails the test on error.
func runGitCmdR(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmdOutR(dir, args...)
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return out
}

// gitCmdOutR runs a git command and returns combined output.
func gitCmdOutR(dir string, args ...string) (string, error) {
	full := append([]string{"-c", "user.name=test", "-c", "user.email=test@test.com"}, args...)
	cmd := exec.Command("git", full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// addCommitR creates a file and commits it.
func addCommitR(t *testing.T, repoPath, filename, message string) {
	t.Helper()
	filePath := filepath.Join(repoPath, filename)
	if err := os.WriteFile(filePath, []byte(message+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
	runGitCmdR(t, repoPath, "add", filename)
	runGitCmdR(t, repoPath, "commit", "-m", message)
}

// ===========================================================================
// TS-22-15 (integration): Reject mode refuses a create and an update of a
// registered patch branch with the origin message
// Verifies: 22-REQ-3.1
// ===========================================================================

func TestRejectMode_TS22_15_RefusesCreateAndUpdate(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		// PUSH_PATCHES_TO_ORIGIN unset → reject mode
		return "", fmt.Errorf("not found")
	}

	env := newRejectTestEnv(t, "https://example.com/fork.git", getVar)

	// Seed registered patch branches p1 and p2.
	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")
	seedPatch(t, env.db, "patch-2", "myws", "p2", 2, "active")

	// Create p1 on the trunk so we can test update.
	runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
	addCommitR(t, env.trunk, "p1.txt", "p1 initial")
	p1OldSHA := runGitCmdR(t, env.trunk, "rev-parse", "HEAD")
	runGitCmdR(t, env.trunk, "checkout", "main")

	// Clone and prepare pushes.
	clone := t.TempDir()
	runGitCmdR(t, "", "clone", env.remote, clone)

	// Test 1: Create p2 (ref absent on hub, row exists).
	runGitCmdR(t, clone, "checkout", "-b", "p2")
	addCommitR(t, clone, "p2.txt", "p2 commit")
	out, err := gitCmdOutR(clone, "push", "origin", "p2")
	if err == nil {
		t.Fatal("expected push of p2 to fail")
	}
	if !strings.Contains(out, "remote rejected") {
		t.Errorf("expected 'remote rejected' in output; got:\n%s", out)
	}
	if !strings.Contains(out, "branch is synced from origin; push to https://example.com/fork.git instead") {
		t.Errorf("expected rejection message in output; got:\n%s", out)
	}

	// Verify p2 does not exist on trunk.
	_, refErr := gitCmdOutR(env.trunk, "rev-parse", "--verify", "refs/heads/p2")
	if refErr == nil {
		t.Error("refs/heads/p2 should not exist on trunk after rejected create")
	}

	// Test 2: Update p1 to commit B.
	runGitCmdR(t, clone, "checkout", "-b", "p1", "origin/p1")
	addCommitR(t, clone, "p1-update.txt", "p1 update")
	out2, err2 := gitCmdOutR(clone, "push", "origin", "p1")
	if err2 == nil {
		t.Fatal("expected push of p1 to fail")
	}
	if !strings.Contains(out2, "branch is synced from origin; push to https://example.com/fork.git instead") {
		t.Errorf("expected rejection message for p1 update; got:\n%s", out2)
	}

	// Verify p1 still equals old SHA.
	p1CurrentSHA := runGitCmdR(t, env.trunk, "rev-parse", "refs/heads/p1")
	if p1CurrentSHA != p1OldSHA {
		t.Errorf("p1 changed after rejected update: got %s, want %s", p1CurrentSHA, p1OldSHA)
	}
}

// ===========================================================================
// TS-22-16 (unit): Userinfo in git_url never appears in the rejection message
// Verifies: 22-REQ-3.2
// ===========================================================================

func TestRejectMode_TS22_16_UserinfoStripped(t *testing.T) {
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	createPatchesTable(t, db)

	seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
		"carry_patch", "main-int", "https://user:s3cret@example.com/fork.git")
	seedPatch(t, db, "p1", "myws", "feature/a", 1, "active")

	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
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

	err := hook(ctx, db, "myws", actor, upd)
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	msg := err.Error()
	expected := "branch is synced from origin; push to https://example.com/fork.git instead"
	if msg != expected {
		t.Errorf("message = %q; want %q", msg, expected)
	}

	// Verify no credential text appears.
	if strings.Contains(msg, "s3cret") {
		t.Error("message contains password 's3cret'")
	}
	if strings.Contains(msg, "user:") {
		t.Error("message contains 'user:' (credential prefix)")
	}
	if strings.Contains(msg, "@example") {
		t.Error("message contains '@' before host (userinfo separator)")
	}
}

// ===========================================================================
// TS-22-17 (property): For any git_url with userinfo the rejection message
// is credential-free
// Verifies: 22-REQ-3.2
// ===========================================================================

func TestRejectMode_TS22_17_PropertyUserinfoStripped(t *testing.T) {
	// Property test: for various URLs with userinfo, the rejection message
	// never contains the username:password text and equals the template
	// with the URL rendered without userinfo.

	type testCase struct {
		name     string
		rawURL   string
		expected string // expected URL in message (without userinfo)
	}

	cases := []testCase{
		{
			name:     "simple https",
			rawURL:   "https://user:pass@example.com/fork.git",
			expected: "https://example.com/fork.git",
		},
		{
			name:     "password with special chars",
			rawURL:   "https://user:p%40ss!w0rd@example.com/fork.git",
			expected: "https://example.com/fork.git",
		},
		{
			name:     "username with encoded colon",
			rawURL:   "https://us%3Aer:pass@example.com/fork.git",
			expected: "https://example.com/fork.git",
		},
		{
			name:     "password with percent",
			rawURL:   "https://user:100%25done@example.com/fork.git",
			expected: "https://example.com/fork.git",
		},
		{
			name:     "user only (no password)",
			rawURL:   "https://user@example.com/fork.git",
			expected: "https://example.com/fork.git",
		},
		{
			name:     "http scheme with port",
			rawURL:   "http://admin:secret@internal.host:8080/repo.git",
			expected: "http://internal.host:8080/repo.git",
		},
		{
			name:     "no userinfo",
			rawURL:   "https://example.com/fork.git",
			expected: "https://example.com/fork.git",
		},
		{
			name:     "empty password",
			rawURL:   "https://user:@example.com/fork.git",
			expected: "https://example.com/fork.git",
		},
		{
			name:     "password with exclamation",
			rawURL:   "https://user:p!ss@example.com/fork.git",
			expected: "https://example.com/fork.git",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			createWorkspacesTable(t, db)
			createPatchesTable(t, db)

			seedWorkspaceWithGitURL(t, db, "ws", "user-1", "active", "ready",
				"carry_patch", "main-int", tc.rawURL)
			seedPatch(t, db, "p1", "ws", "feature/x", 1, "active")

			getVar := func(_, _, key string) (string, error) {
				if key == "PATCH_BRANCH_SOURCE" {
					return "origin", nil
				}
				return "", fmt.Errorf("not found")
			}

			hook := NewPreReceiveHook(PreReceiveHookDeps{
				GetVariable: getVar,
			})

			ctx := context.Background()
			upd := gitserver.RefUpdate{
				Name: plumbing.ReferenceName("refs/heads/feature/x"),
				Old:  plumbing.ZeroHash,
				New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			}

			err := hook(ctx, db, "ws", nil, upd)
			if err == nil {
				t.Fatal("expected non-nil error")
			}

			msg := err.Error()
			wantMsg := fmt.Sprintf("branch is synced from origin; push to %s instead", tc.expected)
			if msg != wantMsg {
				t.Errorf("message = %q; want %q", msg, wantMsg)
			}

			// Verify the message never contains the raw userinfo.
			u, parseErr := url.Parse(tc.rawURL)
			if parseErr == nil && u.User != nil {
				password, hasPass := u.User.Password()
				if hasPass && password != "" {
					if strings.Contains(msg, password) {
						t.Errorf("message contains password %q", password)
					}
				}
			}
		})
	}
}

// ===========================================================================
// TS-22-18 (integration): Reject mode refuses the deletion of a registered
// patch branch
// Verifies: 22-REQ-3.3
// ===========================================================================

func TestRejectMode_TS22_18_RefusesDeletion(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		return "", fmt.Errorf("not found")
	}

	// Unit test: verify the hook rejects a delete directly.
	t.Run("unit", func(t *testing.T) {
		db := openTestDB(t)
		createWorkspacesTable(t, db)
		createPatchesTable(t, db)

		seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
			"carry_patch", "main-int", "https://example.com/fork.git")
		seedPatch(t, db, "p1", "myws", "p1", 1, "active")

		hook := NewPreReceiveHook(PreReceiveHookDeps{
			GetVariable: getVar,
		})

		ctx := context.Background()
		actor := &apikit.AuthInfo{UserID: "user-1"}
		upd := gitserver.RefUpdate{
			Name: plumbing.ReferenceName("refs/heads/p1"),
			Old:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			New:  plumbing.ZeroHash, // delete
		}

		err := hook(ctx, db, "myws", actor, upd)
		if err == nil {
			t.Fatal("expected non-nil error for delete of registered patch branch")
		}
		if !strings.Contains(err.Error(), "branch is synced from origin") {
			t.Errorf("expected rejection message; got: %s", err.Error())
		}
		if !strings.Contains(err.Error(), "https://example.com/fork.git") {
			t.Errorf("expected git_url in message; got: %s", err.Error())
		}
	})

	// Integration test: delete mixed with a create (so git sends a packfile).
	t.Run("integration", func(t *testing.T) {
		env := newRejectTestEnv(t, "https://example.com/fork.git", getVar)

		// Seed registered patch branch p1.
		seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

		// Create p1 on the trunk.
		runGitCmdR(t, env.trunk, "checkout", "-b", "p1")
		addCommitR(t, env.trunk, "p1.txt", "p1 content")
		p1OldSHA := runGitCmdR(t, env.trunk, "rev-parse", "HEAD")
		runGitCmdR(t, env.trunk, "checkout", "main")

		// Clone.
		clone := t.TempDir()
		runGitCmdR(t, "", "clone", env.remote, clone)

		// Create a non-patch branch to push alongside the delete.
		runGitCmdR(t, clone, "checkout", "-b", "feature")
		addCommitR(t, clone, "feature.txt", "feature commit")

		// Push delete of p1 and create of feature in one push.
		out, _ := gitCmdOutR(clone, "push", "origin", ":refs/heads/p1", "feature")

		// The delete of p1 should be rejected.
		if !strings.Contains(out, "remote rejected") {
			t.Errorf("expected 'remote rejected' in output; got:\n%s", out)
		}
		if !strings.Contains(out, "branch is synced from origin") {
			t.Errorf("expected rejection message; got:\n%s", out)
		}

		// Verify p1 still exists at old SHA.
		p1CurrentSHA := runGitCmdR(t, env.trunk, "rev-parse", "refs/heads/p1")
		if p1CurrentSHA != p1OldSHA {
			t.Errorf("p1 changed after rejected delete: got %s, want %s", p1CurrentSHA, p1OldSHA)
		}

		// Verify feature was written (not affected by the rejection).
		featureSHA := runGitCmdR(t, clone, "rev-parse", "feature")
		trunkFeature := runGitCmdR(t, env.trunk, "rev-parse", "refs/heads/feature")
		if trunkFeature != featureSHA {
			t.Errorf("feature ref: trunk = %s; want %s", trunkFeature, featureSHA)
		}
	})
}

// ===========================================================================
// TS-22-19 (integration): A push mixing a registered branch, an unregistered
// branch and a tag rejects only the registered branch
// Verifies: 22-REQ-3.4
// ===========================================================================

func TestRejectMode_TS22_19_MixedPushRejectsOnlyRegistered(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		return "", fmt.Errorf("not found")
	}

	env := newRejectTestEnv(t, "https://example.com/fork.git", getVar)

	// Seed only p1 as registered.
	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Clone.
	clone := t.TempDir()
	runGitCmdR(t, "", "clone", env.remote, clone)

	// Create commits for p1 (registered), feature (unregistered), and tag v1.
	addCommitR(t, clone, "shared.txt", "shared commit")

	runGitCmdR(t, clone, "checkout", "-b", "p1")
	addCommitR(t, clone, "p1.txt", "p1 commit")

	runGitCmdR(t, clone, "checkout", "main")
	runGitCmdR(t, clone, "checkout", "-b", "feature")
	addCommitR(t, clone, "feature.txt", "feature commit")

	// Create a tag.
	runGitCmdR(t, clone, "tag", "v1")

	// Push all three in one push.
	out, _ := gitCmdOutR(clone, "push", "origin", "p1", "feature", "v1")

	// Only p1 should be rejected.
	if !strings.Contains(out, "remote rejected") {
		t.Errorf("expected 'remote rejected' for p1; got:\n%s", out)
	}

	// Verify p1 does NOT exist on trunk.
	_, p1Err := gitCmdOutR(env.trunk, "rev-parse", "--verify", "refs/heads/p1")
	if p1Err == nil {
		t.Error("refs/heads/p1 should not exist on trunk after rejection")
	}

	// Verify feature DOES exist on trunk.
	featureSHA := runGitCmdR(t, clone, "rev-parse", "feature")
	trunkFeature := runGitCmdR(t, env.trunk, "rev-parse", "refs/heads/feature")
	if trunkFeature != featureSHA {
		t.Errorf("feature ref: trunk = %s; want %s", trunkFeature, featureSHA)
	}

	// Verify tag v1 DOES exist on trunk.
	_, tagErr := gitCmdOutR(env.trunk, "rev-parse", "--verify", "refs/tags/v1")
	if tagErr != nil {
		t.Error("refs/tags/v1 should exist on trunk")
	}
}

// ===========================================================================
// TS-22-20 (integration): A rejection is logged at info level with slug,
// branch and user, and the HTTP status is 200
// Verifies: 22-REQ-3.5
// ===========================================================================

func TestRejectMode_TS22_20_LoggedAndHTTP200(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		return "", fmt.Errorf("not found")
	}

	// Test logging with authenticated user.
	t.Run("with auth user u-42", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		hook := newPreReceiveHookWithLogger(PreReceiveHookDeps{
			GetVariable: getVar,
		}, logger)

		db := openTestDB(t)
		createWorkspacesTable(t, db)
		createPatchesTable(t, db)

		seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
			"carry_patch", "main-int", "https://example.com/fork.git")
		seedPatch(t, db, "p1", "myws", "feature/a", 1, "active")

		ctx := context.Background()
		actor := &apikit.AuthInfo{UserID: "u-42"}
		upd := gitserver.RefUpdate{
			Name: plumbing.ReferenceName("refs/heads/feature/a"),
			Old:  plumbing.ZeroHash,
			New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		}

		err := hook(ctx, db, "myws", actor, upd)
		if err == nil {
			t.Fatal("expected non-nil error")
		}

		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "INFO") {
			t.Errorf("expected INFO level log; got:\n%s", logOutput)
		}
		if !strings.Contains(logOutput, "myws") {
			t.Errorf("expected slug 'myws' in log; got:\n%s", logOutput)
		}
		if !strings.Contains(logOutput, "feature/a") {
			t.Errorf("expected branch 'feature/a' in log; got:\n%s", logOutput)
		}
		if !strings.Contains(logOutput, "u-42") {
			t.Errorf("expected user 'u-42' in log; got:\n%s", logOutput)
		}
	})

	// Test without auth info → user field is 'unknown'.
	t.Run("without auth", func(t *testing.T) {
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

		hook := newPreReceiveHookWithLogger(PreReceiveHookDeps{
			GetVariable: getVar,
		}, logger)

		db := openTestDB(t)
		createWorkspacesTable(t, db)
		createPatchesTable(t, db)

		seedWorkspaceWithGitURL(t, db, "myws", "user-1", "active", "ready",
			"carry_patch", "main-int", "https://example.com/fork.git")
		seedPatch(t, db, "p1", "myws", "feature/a", 1, "active")

		ctx := context.Background()
		upd := gitserver.RefUpdate{
			Name: plumbing.ReferenceName("refs/heads/feature/a"),
			Old:  plumbing.ZeroHash,
			New:  plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		}

		err := hook(ctx, db, "myws", nil, upd)
		if err == nil {
			t.Fatal("expected non-nil error")
		}

		logOutput := logBuf.String()
		if !strings.Contains(logOutput, "unknown") {
			t.Errorf("expected 'unknown' user in log when no auth; got:\n%s", logOutput)
		}
	})

	// Test HTTP 200 via integration test.
	t.Run("HTTP 200", func(t *testing.T) {
		env := newRejectTestEnv(t, "https://example.com/fork.git", getVar)
		seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

		clone := t.TempDir()
		runGitCmdR(t, "", "clone", env.remote, clone)
		runGitCmdR(t, clone, "checkout", "-b", "p1")
		addCommitR(t, clone, "p1.txt", "p1 commit")

		// The push will fail from git's perspective (remote rejected),
		// but the HTTP response is 200. The fact that git push received
		// the report status (with ng lines) proves the HTTP response was
		// 200 — a non-200 response would not contain pkt-line data.
		out, err := gitCmdOutR(clone, "push", "origin", "p1")
		if err == nil {
			t.Fatal("expected push to fail")
		}
		// The presence of "remote rejected" in the output proves the
		// client received a valid report-status response (HTTP 200).
		if !strings.Contains(out, "remote rejected") {
			t.Errorf("expected 'remote rejected' (proving HTTP 200 with report-status); got:\n%s", out)
		}
	})
}

// ===========================================================================
// TS-22-31 (integration): A push where nothing was accepted emits no audit
// event and runs no post-push hook or rebuild
// Verifies: 22-REQ-5.3
// ===========================================================================

func TestRejectMode_TS22_31_RejectedOnlyNoSideEffects(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		if key == "AUTO_REBUILD_AFTER_PUSH" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}

	env := newRejectTestEnv(t, "https://example.com/fork.git", getVar)

	// Seed registered patch branch.
	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Set up a recording audit emitter.
	auditMock := &mockAuditEmitter{}
	gitserver.SetAuditEmitter(auditMock)
	t.Cleanup(func() { gitserver.SetAuditEmitter(nil) })

	// Set up a recording post-push hook.
	var mu sync.Mutex
	var hookCalled bool
	gitserver.RegisterPostPushHook(func(_ *sql.DB, _ string, branches []string) {
		mu.Lock()
		defer mu.Unlock()
		hookCalled = true
	})

	// Clone and push only a registered patch branch.
	clone := t.TempDir()
	runGitCmdR(t, "", "clone", env.remote, clone)
	runGitCmdR(t, clone, "checkout", "-b", "p1")
	addCommitR(t, clone, "p1.txt", "p1 commit")

	_, _ = gitCmdOutR(clone, "push", "origin", "p1")

	// Wait for any async hook.
	time.Sleep(300 * time.Millisecond)

	// No audit event should have been emitted.
	events := auditMock.Events()
	if len(events) != 0 {
		t.Errorf("expected 0 audit events for rejected-only push, got %d", len(events))
	}

	// No post-push hook should have been called.
	mu.Lock()
	called := hookCalled
	mu.Unlock()
	if called {
		t.Error("post-push hook should not be called for rejected-only push")
	}
}

// ===========================================================================
// Mock audit emitter for tests
// ===========================================================================

type mockAuditEmitter struct {
	mu     sync.Mutex
	events []audit.HubEvent
}

func (m *mockAuditEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockAuditEmitter) Events() []audit.HubEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]audit.HubEvent, len(m.events))
	copy(result, m.events)
	return result
}
