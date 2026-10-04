package carrypatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/gitserver"
	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// Smoke test infrastructure for push-control end-to-end tests.
//
// Every test uses real components: gitserver HTTP handler, thinPackSafeStorer,
// carry-patch pre-receive hook, database, trunk repository, git binary.
// Tests that need origin use a local bare repository.
// ===========================================================================

// smokeEnv holds the full end-to-end test environment.
type smokeEnv struct {
	db            *sql.DB
	queue         *jobqueue.Queue
	trunk         string // path to the workspace trunk repo
	bareOrigin    string // path to the local bare origin repo (empty if none)
	remote        string // authenticated URL for git push
	workspaceRoot string
	srv           *httptest.Server
	emitter       *smokeAuditEmitter
	hookBranches  []string // branches received by the post-push hook
	hookMu        sync.Mutex
}

// smokeAuditEmitter records emitted audit events for assertions.
type smokeAuditEmitter struct {
	mu     sync.Mutex
	events []audit.HubEvent
}

func (e *smokeAuditEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	return nil
}

func (e *smokeAuditEmitter) Events() []audit.HubEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	cp := make([]audit.HubEvent, len(e.events))
	copy(cp, e.events)
	return cp
}

// smokeOpts configures the smoke test environment.
type smokeOpts struct {
	gitURL         string
	getVar         GetVariableFunc
	withOrigin     bool
	withMirror     bool
	withQueue      bool
	originHookPath string // if set, install a server-side hook on the bare origin
}

// newSmokeEnv creates a full end-to-end test environment.
func newSmokeEnv(t *testing.T, opts smokeOpts) *smokeEnv {
	t.Helper()

	db := openSmokeTestDB(t)

	// Seed org and membership.
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(t, db, `INSERT INTO orgs (id, name, slug, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
		"org-1", "My Org", "myorg", now, now)
	mustExec(t, db, `INSERT INTO org_members (org_id, user_id, created_at) VALUES (?, ?, ?)`,
		"org-1", "user-1", now)

	wsRoot := t.TempDir()
	var bareOrigin string

	gitURL := opts.gitURL
	if opts.withOrigin {
		bareOrigin = t.TempDir()
		runGitCmdS(t, "", "init", "--bare", "-b", "main", bareOrigin)
		if gitURL == "" {
			gitURL = bareOrigin
		}
	}
	if gitURL == "" {
		gitURL = "https://example.com/fork.git"
	}

	// Seed carry_patch workspace.
	mustExec(t, db,
		`INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, clone_status, workspace_mode, integration_branch, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', 'ready', 'carry_patch', 'main-int', ?, ?)`,
		"myws", gitURL, "user-1", "org-1", now, now)

	// Initialize the trunk repo.
	trunkPath := filepath.Join(wsRoot, "myws", "trunk")
	if err := os.MkdirAll(trunkPath, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	initSmokeRepo(t, trunkPath)

	if opts.withOrigin {
		runGitCmdS(t, trunkPath, "remote", "add", "origin", bareOrigin)
		runGitCmdS(t, trunkPath, "push", "origin", "main")
	}

	// Install origin hook if requested.
	if opts.originHookPath != "" && bareOrigin != "" {
		hookDir := filepath.Join(bareOrigin, "hooks")
		if err := os.MkdirAll(hookDir, 0o755); err != nil {
			t.Fatalf("mkdir hooks: %v", err)
		}
		hookPath := filepath.Join(hookDir, "pre-receive")
		if err := os.WriteFile(hookPath, []byte(opts.originHookPath), 0o755); err != nil {
			t.Fatalf("write hook: %v", err)
		}
	}

	// Set up job queue if requested.
	var queue *jobqueue.Queue
	if opts.withQueue {
		if err := jobqueue.InitSchema(db); err != nil {
			t.Fatalf("InitSchema: %v", err)
		}
		if err := jobqueue.MigrateGroupKey(db); err != nil {
			t.Fatalf("MigrateGroupKey: %v", err)
		}
		if err := jobqueue.MigrateProgress(db); err != nil {
			t.Fatalf("MigrateProgress: %v", err)
		}
		q, err := jobqueue.New(db, nopLogger())
		if err != nil {
			t.Fatalf("jobqueue.New: %v", err)
		}
		if err := q.Register("rebuild", func(_ context.Context, _ json.RawMessage) (any, bool, error) {
			return nil, false, nil
		}, nil); err != nil {
			t.Fatalf("register rebuild: %v", err)
		}
		queue = q
	}

	emitter := &smokeAuditEmitter{}

	e := echo.New()
	if err := gitserver.MountGitHandlers(e, db, wsRoot); err != nil {
		t.Fatalf("MountGitHandlers: %v", err)
	}

	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("x-token-auth", "af_key_user1")
	u.Path = "/git/myorg/myws.git"

	env := &smokeEnv{
		db:            db,
		queue:         queue,
		trunk:         trunkPath,
		bareOrigin:    bareOrigin,
		remote:        u.String(),
		workspaceRoot: wsRoot,
		srv:           srv,
		emitter:       emitter,
	}

	// Save and restore global hooks.
	t.Cleanup(func() {
		gitserver.RegisterPreReceiveHook(nil)
		gitserver.RegisterPostPushHook(nil)
		gitserver.SetAuditEmitter(nil)
	})

	// Register the carry-patch pre-receive hook.
	hook := NewPreReceiveHook(PreReceiveHookDeps{
		GetVariable:   opts.getVar,
		ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
		WorkspaceRoot: wsRoot,
	})
	gitserver.RegisterPreReceiveHook(hook)

	// Set audit emitter.
	gitserver.SetAuditEmitter(emitter)

	// Register post-push hook.
	if opts.withMirror && queue != nil {
		postHook := NewPostPushRebuildHook(
			queue,
			opts.getVar,
			PostPushMirrorDeps{
				ResolveAuth:   func(slug string) (transport.AuthMethod, error) { return nil, nil },
				WorkspaceRoot: wsRoot,
				Audit:         emitter,
			},
		)
		gitserver.RegisterPostPushHook(postHook)
	} else if queue != nil {
		postHook := NewPostPushRebuildHook(
			queue,
			opts.getVar,
			PostPushMirrorDeps{},
		)
		gitserver.RegisterPostPushHook(postHook)
	} else {
		// Record branches for assertions.
		gitserver.RegisterPostPushHook(func(_ *sql.DB, _ string, branches []string) {
			env.hookMu.Lock()
			defer env.hookMu.Unlock()
			env.hookBranches = append(env.hookBranches, branches...)
		})
	}

	return env
}

// openSmokeTestDB creates an in-memory SQLite DB with all tables needed
// for the git server and carry-patch hooks.
func openSmokeTestDB(t *testing.T) *sql.DB {
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
	// Use the same hash function as gitserver.
	mustExec(t, db,
		`INSERT INTO api_keys (key_id, user_id, secret_hash, expires_days, created_at)
		 VALUES (?, ?, ?, 365, ?)`,
		"user1", "user-1", smokeHashCredential("user1"), now)

	return db
}

// smokeHashCredential computes the SHA-256 hash of a credential string,
// matching gitserver's hashCredential.
func smokeHashCredential(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// initSmokeRepo initializes a git repo with an initial commit.
func initSmokeRepo(t *testing.T, path string) {
	t.Helper()
	runGitCmdS(t, "", "init", "-b", "main", path)
	runGitCmdS(t, path, "config", "user.name", "Test")
	runGitCmdS(t, path, "config", "user.email", "test@test.com")
	writeFileHelper(t, filepath.Join(path, "README.md"), "# test\n")
	runGitCmdS(t, path, "add", ".")
	runGitCmdS(t, path, "commit", "-m", "init")
}

// runGitCmdS runs a git command and fails the test on error.
func runGitCmdS(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmdOutS(dir, args...)
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return out
}

// gitCmdOutS runs a git command and returns combined output.
func gitCmdOutS(dir string, args ...string) (string, error) {
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

// addCommitS creates a file and commits it.
func addCommitS(t *testing.T, repoPath, filename, message string) {
	t.Helper()
	filePath := filepath.Join(repoPath, filename)
	if err := os.WriteFile(filePath, []byte(message+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
	runGitCmdS(t, repoPath, "add", filename)
	runGitCmdS(t, repoPath, "commit", "-m", message)
}

// refExistsS checks if a ref exists in a git repo.
func refExistsS(t *testing.T, repoPath, ref string) bool {
	t.Helper()
	_, err := gitCmdOutS(repoPath, "rev-parse", "--verify", ref)
	return err == nil
}

// refSHAS returns the SHA of a ref in a git repo.
func refSHAS(t *testing.T, repoPath, ref string) string {
	t.Helper()
	return runGitCmdS(t, repoPath, "rev-parse", ref)
}

// ===========================================================================
// TS-22-49 (smoke): A developer's push to a registered patch branch in
// origin mode is rejected end to end
// Verifies: 22-PATH-1, 22-REQ-3.1, 22-REQ-5.3
// ===========================================================================

func TestSmoke_TS22_49_OriginModeRejectEndToEnd(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		// PUSH_PATCHES_TO_ORIGIN unset → reject mode
		return "", fmt.Errorf("not found")
	}

	env := newSmokeEnv(t, smokeOpts{
		gitURL:     "https://user:s3cret@example.com/fork.git",
		getVar:     getVar,
		withOrigin: false,
	})

	// Seed registered patch branch p1.
	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Clone.
	clone := t.TempDir()
	runGitCmdS(t, "", "clone", env.remote, clone)

	// Push p1.
	runGitCmdS(t, clone, "checkout", "-b", "p1")
	addCommitS(t, clone, "p1.txt", "p1 commit")

	out, err := gitCmdOutS(clone, "push", "origin", "p1")
	if err == nil {
		t.Fatal("expected push to fail")
	}

	// Assert: git shows 'remote rejected' with the message.
	if !strings.Contains(out, "remote rejected") {
		t.Errorf("expected 'remote rejected' in output; got:\n%s", out)
	}
	// The message should contain the git_url WITHOUT userinfo.
	if !strings.Contains(out, "branch is synced from origin; push to https://example.com/fork.git instead") {
		t.Errorf("expected rejection message with clean URL; got:\n%s", out)
	}
	// Userinfo must not appear.
	if strings.Contains(out, "s3cret") {
		t.Error("output contains password 's3cret'")
	}

	// Assert: hub p1 is unchanged (should not exist).
	if refExistsS(t, env.trunk, "refs/heads/p1") {
		t.Error("refs/heads/p1 should not exist on trunk after rejection")
	}

	// Assert: no hub.git.push event is emitted.
	time.Sleep(300 * time.Millisecond) // wait for async hooks
	events := env.emitter.Events()
	for _, ev := range events {
		if ev.EventType == "hub.git.push" {
			t.Error("hub.git.push event should not be emitted for rejected-only push")
		}
	}

	// Assert: no post-push hook ran.
	env.hookMu.Lock()
	branches := env.hookBranches
	env.hookMu.Unlock()
	if len(branches) != 0 {
		t.Errorf("post-push hook should not be called; got branches: %v", branches)
	}
}

// ===========================================================================
// TS-22-50 (smoke): A push to a registered patch branch in origin mode with
// forwarding on reaches both fork and hub
// Verifies: 22-PATH-2, 22-REQ-4.1, 22-REQ-5.2
// ===========================================================================

func TestSmoke_TS22_50_ForwardReachesBothSides(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}

	env := newSmokeEnv(t, smokeOpts{
		getVar:     getVar,
		withOrigin: true,
	})

	// Seed registered patch branch p1.
	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Clone.
	clone := t.TempDir()
	runGitCmdS(t, "", "clone", env.remote, clone)

	// Push p1.
	runGitCmdS(t, clone, "checkout", "-b", "p1")
	addCommitS(t, clone, "p1.txt", "p1 commit")
	commitSHA := refSHAS(t, clone, "HEAD")

	out, err := gitCmdOutS(clone, "push", "origin", "p1")
	if err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	// Assert: p1 is at the same commit on both hub trunk and bare origin.
	trunkP1 := refSHAS(t, env.trunk, "refs/heads/p1")
	if trunkP1 != commitSHA {
		t.Errorf("trunk p1 = %s; want %s", trunkP1, commitSHA)
	}
	originP1 := refSHAS(t, env.bareOrigin, "refs/heads/p1")
	if originP1 != commitSHA {
		t.Errorf("origin p1 = %s; want %s", originP1, commitSHA)
	}

	// Assert: no refs/hub/forward/* remains.
	forwardRefs, _ := gitCmdOutS(env.trunk, "for-each-ref", "--format=%(refname)", "refs/hub/forward/")
	if strings.TrimSpace(forwardRefs) != "" {
		t.Errorf("expected no refs/hub/forward/ refs; got: %s", forwardRefs)
	}

	// Assert: hub.git.push is emitted listing refs/heads/p1.
	time.Sleep(300 * time.Millisecond)
	events := env.emitter.Events()
	foundPush := false
	for _, ev := range events {
		if ev.EventType == "hub.git.push" {
			foundPush = true
			refs, ok := ev.Metadata["refs_updated"].([]string)
			if !ok {
				// Try []any.
				refsAny, ok2 := ev.Metadata["refs_updated"].([]any)
				if ok2 {
					for _, r := range refsAny {
						if s, ok3 := r.(string); ok3 {
							refs = append(refs, s)
						}
					}
				}
			}
			found := false
			for _, r := range refs {
				if r == "refs/heads/p1" {
					found = true
				}
			}
			if !found {
				t.Errorf("hub.git.push event should list refs/heads/p1; got: %v", ev.Metadata["refs_updated"])
			}
		}
	}
	if !foundPush {
		t.Error("expected hub.git.push event to be emitted")
	}

	// Assert: post-push hook ran with p1.
	env.hookMu.Lock()
	hookBranches := env.hookBranches
	env.hookMu.Unlock()
	found := false
	for _, b := range hookBranches {
		if b == "p1" {
			found = true
		}
	}
	if !found {
		t.Errorf("post-push hook should receive 'p1'; got: %v", hookBranches)
	}
}

// ===========================================================================
// TS-22-51 (smoke): A forward refused by the fork as non-fast-forward leaves
// the hub ref unchanged
// Verifies: 22-PATH-3, 22-REQ-4.5, 22-REQ-4.6
// ===========================================================================

func TestSmoke_TS22_51_NonFastForwardLeavesHubUnchanged(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		return "", fmt.Errorf("not found")
	}

	env := newSmokeEnv(t, smokeOpts{
		getVar:     getVar,
		withOrigin: true,
	})

	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Clone and create p1.
	clone := t.TempDir()
	runGitCmdS(t, "", "clone", env.remote, clone)
	runGitCmdS(t, clone, "checkout", "-b", "p1")
	addCommitS(t, clone, "p1.txt", "p1 initial")

	// Push p1 to hub (and origin via forward).
	out, err := gitCmdOutS(clone, "push", "origin", "p1")
	if err != nil {
		t.Fatalf("initial push failed: %v\n%s", err, out)
	}

	hubP1Before := refSHAS(t, env.trunk, "refs/heads/p1")

	// Advance origin's p1 ahead with a divergent commit.
	tmpClone := t.TempDir()
	runGitCmdS(t, "", "clone", env.bareOrigin, tmpClone)
	runGitCmdS(t, tmpClone, "checkout", "p1")
	addCommitS(t, tmpClone, "diverge.txt", "origin moved ahead")
	runGitCmdS(t, tmpClone, "push", "origin", "p1")
	originP1Before := refSHAS(t, env.bareOrigin, "refs/heads/p1")

	// Push a different commit from the hub client — should fail (non-ff).
	addCommitS(t, clone, "hub-diverge.txt", "hub diverge")
	out2, err2 := gitCmdOutS(clone, "push", "origin", "p1")
	if err2 == nil {
		t.Fatal("expected push to fail due to non-fast-forward")
	}

	// Assert: git shows 'remote rejected' with 'origin rejected push: '.
	if !strings.Contains(out2, "remote rejected") {
		t.Errorf("expected 'remote rejected' in output; got:\n%s", out2)
	}
	if !strings.Contains(out2, "origin rejected push: ") {
		t.Errorf("expected 'origin rejected push: ' in output; got:\n%s", out2)
	}

	// Assert: hub p1 and origin p1 are unchanged.
	hubP1After := refSHAS(t, env.trunk, "refs/heads/p1")
	if hubP1After != hubP1Before {
		t.Errorf("hub p1 changed: %s → %s", hubP1Before, hubP1After)
	}
	originP1After := refSHAS(t, env.bareOrigin, "refs/heads/p1")
	if originP1After != originP1Before {
		t.Errorf("origin p1 changed: %s → %s", originP1Before, originP1After)
	}

	// Assert: the temporary forward ref is gone.
	forwardRefs, _ := gitCmdOutS(env.trunk, "for-each-ref", "--format=%(refname)", "refs/hub/forward/")
	if strings.TrimSpace(forwardRefs) != "" {
		t.Errorf("expected no refs/hub/forward/ refs; got: %s", forwardRefs)
	}
}

// ===========================================================================
// TS-22-52 (smoke): A hub-mode push of a registered patch branch is accepted,
// rebuilt and mirrored to the fork
// Verifies: 22-PATH-4, 22-REQ-6.1, 22-REQ-6.2
// ===========================================================================

func TestSmoke_TS22_52_HubModeMirrorEndToEnd(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		// PATCH_BRANCH_SOURCE unset → hub mode (default)
		return "", fmt.Errorf("not found")
	}

	env := newSmokeEnv(t, smokeOpts{
		getVar:     getVar,
		withOrigin: true,
		withMirror: true,
		withQueue:  true,
	})

	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Clone.
	clone := t.TempDir()
	runGitCmdS(t, "", "clone", env.remote, clone)

	// Push p1.
	runGitCmdS(t, clone, "checkout", "-b", "p1")
	addCommitS(t, clone, "p1.txt", "p1 commit")
	commitSHA := refSHAS(t, clone, "HEAD")

	out, err := gitCmdOutS(clone, "push", "origin", "p1")
	if err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	// Assert: push succeeds and hub p1 holds the commit.
	trunkP1 := refSHAS(t, env.trunk, "refs/heads/p1")
	if trunkP1 != commitSHA {
		t.Errorf("trunk p1 = %s; want %s", trunkP1, commitSHA)
	}

	// Wait for the async post-push hook (mirror runs in goroutine).
	time.Sleep(1 * time.Second)

	// Assert: a rebuild job with SubmittedBy system:push-hook is enqueued.
	var jobCount int
	err = env.db.QueryRow(
		`SELECT COUNT(*) FROM jobs WHERE type = 'rebuild' AND submitted_by = 'system:push-hook'`,
	).Scan(&jobCount)
	if err != nil {
		t.Fatalf("query jobs: %v", err)
	}
	if jobCount == 0 {
		t.Error("expected a rebuild job with submitted_by 'system:push-hook'")
	}

	// Assert: the bare origin holds p1 at the hub tip.
	originP1 := refSHAS(t, env.bareOrigin, "refs/heads/p1")
	if originP1 != commitSHA {
		t.Errorf("origin p1 = %s; want %s (hub tip)", originP1, commitSHA)
	}
}

// ===========================================================================
// TS-22-53 (smoke): A failed mirror is reported through hub.patch.mirror_failed
// while the push, rebuild and other branch succeed
// Verifies: 22-PATH-5, 22-REQ-7.1, 22-REQ-6.3
// ===========================================================================

func TestSmoke_TS22_53_FailedMirrorReported(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PUSH_PATCHES_TO_ORIGIN" {
			return "true", nil
		}
		// PATCH_BRANCH_SOURCE unset → hub mode (default)
		return "", fmt.Errorf("not found")
	}

	// Create a pre-receive hook on the bare origin that rejects p2.
	hookScript := `#!/bin/sh
while read oldrev newrev refname; do
  if [ "$refname" = "refs/heads/p2" ]; then
    echo "refusing p2" >&2
    exit 1
  fi
done
exit 0
`

	env := newSmokeEnv(t, smokeOpts{
		getVar:         getVar,
		withOrigin:     true,
		withMirror:     true,
		withQueue:      true,
		originHookPath: hookScript,
	})

	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")
	seedPatch(t, env.db, "patch-2", "myws", "p2", 2, "active")

	// Clone.
	clone := t.TempDir()
	runGitCmdS(t, "", "clone", env.remote, clone)

	// Create p1 and p2.
	runGitCmdS(t, clone, "checkout", "-b", "p1")
	addCommitS(t, clone, "p1.txt", "p1 commit")

	runGitCmdS(t, clone, "checkout", "main")
	runGitCmdS(t, clone, "checkout", "-b", "p2")
	addCommitS(t, clone, "p2.txt", "p2 commit")

	// Push both.
	out, err := gitCmdOutS(clone, "push", "origin", "p1", "p2")
	if err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	// Wait for the async post-push hook.
	time.Sleep(2 * time.Second)

	// Assert: a rebuild job is enqueued.
	var jobCount int
	err = env.db.QueryRow(
		`SELECT COUNT(*) FROM jobs WHERE type = 'rebuild' AND submitted_by = 'system:push-hook'`,
	).Scan(&jobCount)
	if err != nil {
		t.Fatalf("query jobs: %v", err)
	}
	if jobCount == 0 {
		t.Error("expected a rebuild job")
	}

	// Assert: p1 exists on the origin and p2 does not.
	if !refExistsS(t, env.bareOrigin, "refs/heads/p1") {
		t.Error("origin should have p1 after mirror")
	}
	if refExistsS(t, env.bareOrigin, "refs/heads/p2") {
		t.Error("origin should NOT have p2 (hook rejects it)")
	}

	// Assert: one hub.patch.mirror_failed event.
	events := env.emitter.Events()
	var mirrorFailed []audit.HubEvent
	for _, ev := range events {
		if ev.EventType == audit.EventPatchMirrorFailed {
			mirrorFailed = append(mirrorFailed, ev)
		}
	}
	if len(mirrorFailed) == 0 {
		t.Fatal("expected at least one hub.patch.mirror_failed event")
	}

	// Check the event metadata.
	ev := mirrorFailed[0]
	if ev.ResourceType != "patch" {
		t.Errorf("resource_type = %q; want %q", ev.ResourceType, "patch")
	}
	if ev.Action != "mirror" {
		t.Errorf("action = %q; want %q", ev.Action, "mirror")
	}
	if ev.ActorType != "system" {
		t.Errorf("actor_type = %q; want %q", ev.ActorType, "system")
	}
	if ev.Workspace != "myws" {
		t.Errorf("workspace = %q; want %q", ev.Workspace, "myws")
	}
	branchName, _ := ev.Metadata["branch_name"].(string)
	if branchName != "p2" {
		t.Errorf("metadata.branch_name = %q; want %q", branchName, "p2")
	}
	errorStr, _ := ev.Metadata["error"].(string)
	if errorStr == "" {
		t.Error("metadata.error should be non-empty")
	}
}

// ===========================================================================
// TS-22-54 (smoke): After registration and sync of a fork-only branch, a
// mixed push rejects only the registered branch
// Verifies: 22-PATH-6, 22-REQ-3.4, 22-REQ-5.2
// ===========================================================================

func TestSmoke_TS22_54_MixedPushRejectsOnlyRegistered(t *testing.T) {
	getVar := func(scope, slug, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		// PUSH_PATCHES_TO_ORIGIN unset → reject mode
		return "", fmt.Errorf("not found")
	}

	env := newSmokeEnv(t, smokeOpts{
		getVar:     getVar,
		withOrigin: true,
	})

	// Simulate registration: insert a patch row for p1.
	seedPatch(t, env.db, "patch-1", "myws", "p1", 1, "active")

	// Simulate sync: create p1 on the trunk from the fork.
	// First create p1 on the bare origin.
	tmpClone := t.TempDir()
	runGitCmdS(t, "", "clone", env.bareOrigin, tmpClone)
	runGitCmdS(t, tmpClone, "checkout", "-b", "p1")
	addCommitS(t, tmpClone, "p1-fork.txt", "p1 from fork")
	runGitCmdS(t, tmpClone, "push", "origin", "p1")

	// Create p1 on the trunk (simulating sync).
	runGitCmdS(t, env.trunk, "fetch", "origin")
	p1SHA := refSHAS(t, env.bareOrigin, "refs/heads/p1")
	runGitCmdS(t, env.trunk, "branch", "p1", p1SHA)

	// Clone the hub.
	clone := t.TempDir()
	runGitCmdS(t, "", "clone", env.remote, clone)

	// Create commits for the registered branch (p1), an unregistered branch
	// (feature), and a tag (v1).
	runGitCmdS(t, clone, "checkout", "-b", "p1", "origin/p1")
	addCommitS(t, clone, "p1-hub.txt", "p1 hub commit")

	runGitCmdS(t, clone, "checkout", "main")
	runGitCmdS(t, clone, "checkout", "-b", "feature")
	addCommitS(t, clone, "feature.txt", "feature commit")

	runGitCmdS(t, clone, "tag", "v1")

	// Push all three.
	out, _ := gitCmdOutS(clone, "push", "origin", "p1", "feature", "v1")

	// Assert: the registered branch shows remote rejected.
	if !strings.Contains(out, "remote rejected") {
		t.Errorf("expected 'remote rejected' for p1; got:\n%s", out)
	}

	// Assert: p1 on the hub is unchanged (still at the sync SHA).
	trunkP1 := refSHAS(t, env.trunk, "refs/heads/p1")
	if trunkP1 != p1SHA {
		t.Errorf("trunk p1 changed: got %s, want %s", trunkP1, p1SHA)
	}

	// Assert: the unregistered branch and tag are written on the hub.
	featureSHA := refSHAS(t, clone, "feature")
	trunkFeature := refSHAS(t, env.trunk, "refs/heads/feature")
	if trunkFeature != featureSHA {
		t.Errorf("feature ref: trunk = %s; want %s", trunkFeature, featureSHA)
	}

	if !refExistsS(t, env.trunk, "refs/tags/v1") {
		t.Error("refs/tags/v1 should exist on trunk")
	}

	// Assert: hub.git.push lists only the accepted refs.
	time.Sleep(300 * time.Millisecond)
	events := env.emitter.Events()
	for _, ev := range events {
		if ev.EventType == "hub.git.push" {
			refsUpdated := extractRefsFromEvent(ev)
			for _, r := range refsUpdated {
				if r == "refs/heads/p1" {
					t.Error("hub.git.push should not list refs/heads/p1 (rejected)")
				}
			}
			// Should contain feature and v1.
			foundFeature := false
			foundTag := false
			for _, r := range refsUpdated {
				if r == "refs/heads/feature" {
					foundFeature = true
				}
				if r == "refs/tags/v1" {
					foundTag = true
				}
			}
			if !foundFeature {
				t.Errorf("hub.git.push should list refs/heads/feature; got: %v", refsUpdated)
			}
			if !foundTag {
				t.Errorf("hub.git.push should list refs/tags/v1; got: %v", refsUpdated)
			}
		}
	}

	// Assert: the post-push hook receives only the unregistered branch.
	env.hookMu.Lock()
	hookBranches := env.hookBranches
	env.hookMu.Unlock()
	for _, b := range hookBranches {
		if b == "p1" {
			t.Error("post-push hook should not receive 'p1' (rejected)")
		}
	}
	foundFeatureHook := false
	for _, b := range hookBranches {
		if b == "feature" {
			foundFeatureHook = true
		}
	}
	if !foundFeatureHook {
		t.Errorf("post-push hook should receive 'feature'; got: %v", hookBranches)
	}
}

// extractRefsFromEvent extracts the refs_updated list from an audit event.
func extractRefsFromEvent(ev audit.HubEvent) []string {
	var refs []string
	switch v := ev.Metadata["refs_updated"].(type) {
	case []string:
		refs = v
	case []any:
		for _, r := range v {
			if s, ok := r.(string); ok {
				refs = append(refs, s)
			}
		}
	}
	return refs
}
