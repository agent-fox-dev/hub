package carrypatch

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/aflib/jobqueue"
	_ "modernc.org/sqlite"
)

// The module's tests run against doubles of its host: in-process locks with
// the semantics of the hub's wslock and the hub's workspace access rule read
// from the test database. TestMain binds them and pins git's default branch
// so that fixtures which init and clone without naming a branch get main.

func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_COUNT", "1")
	os.Setenv("GIT_CONFIG_KEY_0", "init.defaultBranch")
	os.Setenv("GIT_CONFIG_VALUE_0", "main")
	testLocks = newLockSet()
	SetHost(Host{
		Lock:         testLocks.Lock,
		TryLock:      testLocks.TryLock,
		BeginRebuild: testLocks.BeginRebuild,
		Authorize:    testAuthorize,
	})
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Host doubles
// ---------------------------------------------------------------------------

var testLocks *lockSet

// lockSet is an in-process per-workspace lock with a rebuild guard.
type lockSet struct {
	mu     sync.Mutex
	locks  map[string]*sync.Mutex
	guards map[string]bool
}

func newLockSet() *lockSet {
	return &lockSet{locks: map[string]*sync.Mutex{}, guards: map[string]bool{}}
}

func (s *lockSet) get(slug string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[slug]
	if !ok {
		l = &sync.Mutex{}
		s.locks[slug] = l
	}
	return l
}

func (s *lockSet) Lock(slug string) func() {
	l := s.get(slug)
	l.Lock()
	return l.Unlock
}

func (s *lockSet) TryLock(slug string) (func(), bool) {
	l := s.get(slug)
	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
}

func (s *lockSet) BeginRebuild(slug string) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.guards[slug] {
		return func() {}, false
	}
	s.guards[slug] = true
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.guards, slug)
	}, true
}

// RebuildActive reports whether a rebuild is marked active on the workspace.
func (s *lockSet) RebuildActive(slug string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.guards[slug]
}

// lockHeld reports whether the workspace lock is currently taken.
func lockHeld(slug string) bool {
	unlock, ok := testLocks.TryLock(slug)
	if ok {
		unlock()
	}
	return !ok
}

// holdLock takes the workspace lock for the test and returns its release.
func holdLock(t *testing.T, slug string) func() {
	t.Helper()
	unlock, ok := testLocks.TryLock(slug)
	if !ok {
		t.Fatalf("workspace %q is already locked", slug)
	}
	return unlock
}

// testAuthorize is the hub's access rule over the test database: an admin
// token and the owner read and write; a member of the workspace's org reads
// and is refused a write with 403; everyone else, and an unknown slug, is 404.
func testAuthorize(db *sql.DB, auth *apikit.AuthInfo, slug string, need Need) (int, string) {
	if auth == nil {
		return http.StatusUnauthorized, "authentication required"
	}
	var ownerID string
	var orgID sql.NullString
	err := db.QueryRow(`SELECT owner_id, org_id FROM workspaces WHERE slug = ?`, slug).Scan(&ownerID, &orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound, "workspace not found"
	}
	if err != nil {
		return http.StatusInternalServerError, "internal server error"
	}
	if auth.CredentialType == "admin_token" || (auth.UserID != "" && auth.UserID == ownerID) {
		return 0, ""
	}
	var n int
	if auth.UserID == "" || !orgID.Valid ||
		db.QueryRow(`SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?`, orgID.String, auth.UserID).Scan(&n) != nil || n == 0 {
		return http.StatusNotFound, "workspace not found"
	}
	if need == NeedWrite {
		return http.StatusForbidden, "org members have read-only access to this workspace"
	}
	return 0, ""
}

// ---------------------------------------------------------------------------
// Database, seeding, credentials
// ---------------------------------------------------------------------------

// openDB opens an in-memory database with the hub's workspaces and patches
// tables, the apikit org tables the access rule reads, and the job queue.
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE workspaces (
			slug TEXT PRIMARY KEY, git_url TEXT NOT NULL, branch TEXT, owner_id TEXT NOT NULL, org_id TEXT,
			status TEXT NOT NULL DEFAULT 'active', display_name TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
			clone_status TEXT NOT NULL DEFAULT 'pending', head_sha TEXT, clone_error TEXT,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			sync_mode TEXT NOT NULL DEFAULT 'pull_only', sync_status TEXT NOT NULL DEFAULT 'idle',
			upstream_head_sha TEXT, last_sync_at TEXT, sync_error TEXT,
			workspace_mode TEXT NOT NULL DEFAULT 'standard', upstream_url TEXT, integration_branch TEXT)`,
		`CREATE TABLE patches (
			id TEXT PRIMARY KEY, workspace_slug TEXT NOT NULL, branch_name TEXT NOT NULL, position INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','conflict','disabled','merged_upstream','deleted')),
			conflict_files TEXT, upstream_pr_url TEXT, description TEXT, deleted_at TEXT,
			added_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			origin_sync_state TEXT, origin_sha TEXT, origin_synced_at TEXT,
			UNIQUE(workspace_slug, branch_name), UNIQUE(workspace_slug, position))`,
		`CREATE TABLE org_members (org_id TEXT NOT NULL, user_id TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT '', PRIMARY KEY (org_id, user_id))`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("jobqueue schema: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("jobqueue migrate: %v", err)
	}
	if err := jobqueue.MigrateProgress(db); err != nil {
		t.Fatalf("jobqueue migrate: %v", err)
	}
	return db
}

// newQueue returns a job queue over db with the rebuild job type registered
// on h (a bare handler when h is nil). The queue is never started: tests
// enqueue and inspect jobs, and run the executor directly.
func newQueue(t *testing.T, db *sql.DB, h *RebuildHandler) *jobqueue.Queue {
	t.Helper()
	q, err := jobqueue.New(db, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("jobqueue.New: %v", err)
	}
	if h == nil {
		h = &RebuildHandler{}
	}
	if err := RegisterRebuildJob(q, h); err != nil {
		t.Fatalf("RegisterRebuildJob: %v", err)
	}
	return q
}

// ws describes a workspace row; empty fields take the defaults of seedWS.
type ws struct {
	slug, owner, status, clone, mode, integration, gitURL, upstreamSHA, branch string
}

// seedWS inserts a workspace: by default an active carry_patch workspace owned
// by alice with a ready clone and integration branch "integration".
func seedWS(t *testing.T, db *sql.DB, w ws) {
	t.Helper()
	def := func(v *string, d string) {
		if *v == "" {
			*v = d
		}
	}
	def(&w.owner, "alice")
	def(&w.status, "active")
	def(&w.clone, "ready")
	def(&w.mode, "carry_patch")
	def(&w.integration, "integration")
	def(&w.gitURL, "https://github.com/example/fork")
	var upstream, branch *string
	if w.upstreamSHA != "" {
		upstream = &w.upstreamSHA
	}
	if w.branch != "" {
		branch = &w.branch
	}
	now := apikit.NowUTC()
	_, err := db.Exec(`INSERT INTO workspaces (slug, git_url, branch, owner_id, status, clone_status, workspace_mode,
		integration_branch, upstream_url, upstream_head_sha, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'https://github.com/example/upstream', ?, ?, ?)`,
		w.slug, w.gitURL, branch, w.owner, w.status, w.clone, w.mode, w.integration, upstream, now, now)
	if err != nil {
		t.Fatalf("seed workspace %q: %v", w.slug, err)
	}
}

// seedPatch inserts a patch row and returns its id.
func seedPatch(t *testing.T, db *sql.DB, slug, id, branch string, position int, status string) string {
	t.Helper()
	now := apikit.NowUTC()
	_, err := db.Exec(`INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, id, slug, branch, position, status, now, now)
	if err != nil {
		t.Fatalf("seed patch %q: %v", id, err)
	}
	return id
}

// seedMember puts the workspace in org and makes user a member of it.
func seedMember(t *testing.T, db *sql.DB, slug, org, user string) {
	t.Helper()
	mustExec(t, db, `UPDATE workspaces SET org_id = ? WHERE slug = ?`, org, slug)
	mustExec(t, db, `INSERT OR IGNORE INTO org_members (org_id, user_id) VALUES (?, ?)`, org, user)
}

// mustExec runs a statement and fails the test on error.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

// patchStatuses returns branch -> status of every patch row of the workspace,
// soft-deleted ones included.
func patchStatuses(t *testing.T, db *sql.DB, slug string) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT branch_name, status FROM patches WHERE workspace_slug = ?`, slug)
	if err != nil {
		t.Fatalf("query patches: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var b, s string
		if err := rows.Scan(&b, &s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[b] = s
	}
	return out
}

// positions returns the branches of the visible patches in position order.
func positions(t *testing.T, db *sql.DB, slug string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT branch_name FROM patches WHERE workspace_slug = ? AND status != 'deleted' ORDER BY position`, slug)
	if err != nil {
		t.Fatalf("query positions: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, b)
	}
	return out
}

func admin() *apikit.AuthInfo { return &apikit.AuthInfo{CredentialType: "admin_token"} }

func user(id string) *apikit.AuthInfo { return &apikit.AuthInfo{CredentialType: "api_key", UserID: id} }

func pat(id string, scopes ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "pat", UserID: id, Permissions: scopes}
}

var alice = user("alice")

// ---------------------------------------------------------------------------
// Audit, recovery and variable doubles
// ---------------------------------------------------------------------------

// recordingEmitter records emitted events; err, when set, is returned by Emit.
type recordingEmitter struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (r *recordingEmitter) Emit(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return r.err
}

// ofType returns the recorded events of one type.
func (r *recordingEmitter) ofType(eventType string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if e.EventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

// stubRecovery is a Recoverer with canned answers that records its calls.
type stubRecovery struct {
	sha     string
	found   bool
	readErr error

	removeErr error
	removed   []string

	result   ResetResult
	resetErr error
	resets   []ResetPatchInfo
}

func (s *stubRecovery) ReadReplacedSHA(context.Context, string, string) (string, bool, error) {
	return s.sha, s.found, s.readErr
}

func (s *stubRecovery) RemoveBackup(_ context.Context, _, branch string) error {
	s.removed = append(s.removed, branch)
	return s.removeErr
}

func (s *stubRecovery) RunReset(_ context.Context, _ string, p ResetPatchInfo, _ *apikit.AuthInfo) (ResetResult, error) {
	s.resets = append(s.resets, p)
	return s.result, s.resetErr
}

// varsOf returns a GetVariableFunc over a map; a missing key is an error, as
// the hub answers for an unset setting. reads counts the lookups by key.
func varsOf(vars map[string]string, reads map[string]int) GetVariableFunc {
	var mu sync.Mutex
	return func(_, _, key string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if reads != nil {
			reads[key]++
		}
		if v, ok := vars[key]; ok {
			return v, nil
		}
		return "", errors.New("not set")
	}
}

var noFetch = func(context.Context, string, transport.AuthMethod) error { return nil }

var noAuth = func(string) (transport.AuthMethod, error) { return nil, nil }

// logSink collects slog output for assertions.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *logSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func newLogger() (*slog.Logger, *logSink) {
	sink := &logSink{}
	return slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug})), sink
}

// captureDefaultLog routes slog.Default() into a sink for the test.
func captureDefaultLog(t *testing.T) *logSink {
	t.Helper()
	logger, sink := newLogger()
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })
	return sink
}

// ---------------------------------------------------------------------------
// Git runner doubles
// ---------------------------------------------------------------------------

const fakeSHA = "aaaa000000000000000000000000000000000001"

// fakeGit is a GitRunner whose answers are functions; it records every call.
type fakeGit struct {
	mu    sync.Mutex
	calls [][]string

	run        func(args ...string) (string, error)
	cherryPick func(sha string) error
	mergeNoFF  func(ref, message string) error
	mergeTree  func(base, head string) (string, error)
	isAncestor func(ancestor, descendant string) (bool, error)
	cherry     func(upstream, head string) ([]string, []string, error)
	hardReset  func(ref string) error
	wtAdd      func(path, commit string) error
	wtRemove   func(path string) error
	wtPrune    func() error
	updateRef  func(ref, sha string) error
}

func newFakeGit() *fakeGit {
	return &fakeGit{
		run: func(args ...string) (string, error) {
			if args[0] == "rev-parse" {
				return fakeSHA, nil
			}
			return "", nil
		},
	}
}

func (f *fakeGit) record(args ...string) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.mu.Unlock()
}

// has reports whether a recorded call starts with the given words.
func (f *fakeGit) has(prefix ...string) bool { return f.count(prefix...) > 0 }

func (f *fakeGit) count(prefix ...string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], " ") == strings.Join(prefix, " ") {
			n++
		}
	}
	return n
}

func (f *fakeGit) Run(_ context.Context, args ...string) (string, error) {
	f.record(args...)
	return f.run(args...)
}

func (f *fakeGit) CherryPick(_ context.Context, sha string) error {
	f.record("CherryPick", sha)
	if f.cherryPick != nil {
		return f.cherryPick(sha)
	}
	return nil
}

func (f *fakeGit) MergeNoFF(_ context.Context, ref, message string) error {
	f.record("MergeNoFF", ref, message)
	if f.mergeNoFF != nil {
		return f.mergeNoFF(ref, message)
	}
	return nil
}

func (f *fakeGit) MergeTree(_ context.Context, base, head string) (string, error) {
	f.record("MergeTree", base, head)
	if f.mergeTree != nil {
		return f.mergeTree(base, head)
	}
	return "tree", nil
}

func (f *fakeGit) IsAncestor(_ context.Context, a, d string) (bool, error) {
	f.record("IsAncestor", a, d)
	if f.isAncestor != nil {
		return f.isAncestor(a, d)
	}
	return false, nil
}

func (f *fakeGit) Cherry(_ context.Context, up, head string) ([]string, []string, error) {
	f.record("Cherry", up, head)
	if f.cherry != nil {
		return f.cherry(up, head)
	}
	return nil, nil, nil
}

func (f *fakeGit) HardReset(_ context.Context, ref string) error {
	f.record("HardReset", ref)
	if f.hardReset != nil {
		return f.hardReset(ref)
	}
	return nil
}

func (f *fakeGit) WorktreeAdd(_ context.Context, path, commit string) error {
	f.record("WorktreeAdd", path, commit)
	if f.wtAdd != nil {
		return f.wtAdd(path, commit)
	}
	return os.MkdirAll(path, 0o755)
}

func (f *fakeGit) WorktreeRemove(_ context.Context, path string) error {
	f.record("WorktreeRemove", path)
	if f.wtRemove != nil {
		return f.wtRemove(path)
	}
	return os.RemoveAll(path)
}

func (f *fakeGit) WorktreePrune(context.Context) error {
	f.record("WorktreePrune")
	if f.wtPrune != nil {
		return f.wtPrune()
	}
	return nil
}

func (f *fakeGit) UpdateRef(_ context.Context, ref, sha string) error {
	f.record("UpdateRef", ref, sha)
	if f.updateRef != nil {
		return f.updateRef(ref, sha)
	}
	return nil
}

// spyGit wraps a real GitRunner and lets a test intercept single operations.
// A hook that returns handled=true answers the call itself.
type spyGit struct {
	GitRunner
	mu    sync.Mutex
	runs  [][]string
	onRun func(args []string) (out string, err error, handled bool)

	onCherryPick func(sha string) error
	onHardReset  func(ref string) error
	onUpdateRef  func(ref, sha string) error
	onIsAncestor func(a, d string) error
}

func (s *spyGit) IsAncestor(ctx context.Context, a, d string) (bool, error) {
	if s.onIsAncestor != nil {
		if err := s.onIsAncestor(a, d); err != nil {
			return false, err
		}
	}
	return s.GitRunner.IsAncestor(ctx, a, d)
}

func (s *spyGit) Run(ctx context.Context, args ...string) (string, error) {
	s.mu.Lock()
	s.runs = append(s.runs, args)
	s.mu.Unlock()
	if s.onRun != nil {
		if out, err, ok := s.onRun(args); ok {
			return out, err
		}
	}
	return s.GitRunner.Run(ctx, args...)
}

func (s *spyGit) CherryPick(ctx context.Context, sha string) error {
	if s.onCherryPick != nil {
		if err := s.onCherryPick(sha); err != nil {
			return err
		}
	}
	return s.GitRunner.CherryPick(ctx, sha)
}

func (s *spyGit) HardReset(ctx context.Context, ref string) error {
	if s.onHardReset != nil {
		return s.onHardReset(ref)
	}
	return s.GitRunner.HardReset(ctx, ref)
}

func (s *spyGit) UpdateRef(ctx context.Context, ref, sha string) error {
	if s.onUpdateRef != nil {
		return s.onUpdateRef(ref, sha)
	}
	return s.GitRunner.UpdateRef(ctx, ref, sha)
}

// ran reports whether a recorded Run started with the given words.
func (s *spyGit) ran(prefix ...string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.runs {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], " ") == strings.Join(prefix, " ") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Git fixtures
// ---------------------------------------------------------------------------

// runGit runs a git command with a test identity and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitErr(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return out
}

// gitErr runs a git command and returns its trimmed combined output.
func gitErr(dir string, args ...string) (string, error) {
	full := append([]string{"-c", "user.name=test", "-c", "user.email=test@test.com"}, args...)
	cmd := exec.Command("git", full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// refOf returns the SHA a ref resolves to in dir, or "" when it does not.
func refOf(dir, ref string) string {
	out, err := gitErr(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// commitFile writes a file, commits it and returns the commit SHA.
func commitFile(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), content)
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", message)
	return runGit(t, dir, "rev-parse", "HEAD")
}

// realGit returns the production GitRunner for a repository.
func realGit(t *testing.T, path string) GitRunner {
	t.Helper()
	r, err := NewGitRunnerFactory()(path)
	if err != nil {
		t.Fatalf("git runner for %s: %v", path, err)
	}
	return r
}

// newRebuildRepo creates <root>/<slug>/trunk, the shape the rebuild works on:
// main with a base commit and an upstream change, feature/patch-a and
// feature/patch-b that each add a file, feature/conflict that changes base.txt
// against main, an integration branch at main, and refs/remotes/upstream/HEAD
// at main. It returns the trunk path and the upstream base SHA.
func newRebuildRepo(t *testing.T, root, slug string) (trunk, base string) {
	t.Helper()
	trunk = filepath.Join(root, slug, "trunk")
	if err := os.MkdirAll(trunk, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, "", "init", "-q", "-b", "main", trunk)
	commitFile(t, trunk, "base.txt", "base content\n", "base commit")
	for _, b := range []string{"patch-a", "patch-b"} {
		runGit(t, trunk, "checkout", "-q", "-b", "feature/"+b, "main")
		commitFile(t, trunk, b+".txt", b+" content\n", b+" change")
	}
	runGit(t, trunk, "checkout", "-q", "-b", "feature/conflict", "main")
	commitFile(t, trunk, "base.txt", "feature version\n", "conflicting change")
	runGit(t, trunk, "checkout", "-q", "main")
	base = commitFile(t, trunk, "base.txt", "upstream version\n", "upstream change")
	runGit(t, trunk, "branch", "integration")
	runGit(t, trunk, "update-ref", UpstreamHeadRef, base)
	return trunk, base
}

// forkTrunk is a bare fork with main and a trunk cloned from it (remote
// origin), with refs/remotes/upstream/HEAD at the initial commit.
type forkTrunk struct {
	fork, trunk, base string
}

func newForkTrunk(t *testing.T, root, slug string) *forkTrunk {
	t.Helper()
	f := &forkTrunk{fork: filepath.Join(t.TempDir(), "fork.git")}
	runGit(t, "", "init", "-q", "--bare", "-b", "main", f.fork)
	seed := filepath.Join(t.TempDir(), "seed")
	runGit(t, "", "clone", "-q", f.fork, seed)
	f.base = commitFile(t, seed, "README.md", "initial\n", "initial commit")
	runGit(t, seed, "push", "-q", "origin", "main")
	f.trunk = filepath.Join(root, slug, "trunk")
	if err := os.MkdirAll(filepath.Dir(f.trunk), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runGit(t, "", "clone", "-q", f.fork, f.trunk)
	runGit(t, f.trunk, "update-ref", UpstreamHeadRef, f.base)
	return f
}

// pushBranch creates branch on the fork from base with one commit (through a
// temporary clone) and returns the commit SHA. The trunk does not see it
// until something fetches.
func (f *forkTrunk) pushBranch(t *testing.T, branch, file, content string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	runGit(t, "", "clone", "-q", f.fork, work)
	runGit(t, work, "checkout", "-q", "-b", branch, f.base)
	sha := commitFile(t, work, file, content, "fork: "+branch)
	runGit(t, work, "push", "-q", "--force", "origin", branch)
	return sha
}

// localBranch creates branch in the trunk at base with one commit and
// returns its SHA; the trunk's checkout is left on main.
func (f *forkTrunk) localBranch(t *testing.T, branch, file, content string) string {
	t.Helper()
	runGit(t, f.trunk, "checkout", "-q", "-b", branch, f.base)
	sha := commitFile(t, f.trunk, file, content, "local: "+branch)
	runGit(t, f.trunk, "checkout", "-q", "main")
	return sha
}

// ---------------------------------------------------------------------------
// HTTP environment
// ---------------------------------------------------------------------------

// env serves the module's routes over a database, a queue and doubles a test
// adjusts before the request: each request is served by a server built from
// the fields as they are at that moment.
type env struct {
	t     *testing.T
	db    *sql.DB
	q     *jobqueue.Queue
	root  string
	fake  *fakeGit
	audit *recordingEmitter
	vars  map[string]string
	reads map[string]int

	useRealGit bool
	wrap       func(GitRunner) GitRunner // wraps the real runner when set
	gitErr     error
	audited    bool // false mounts the routes with a nil emitter
	recovery   Recoverer
	resolve    BranchResolverFunc

	fetch, fetchOrigin             FetchFunc
	resolveAuth, resolveOriginAuth ResolveAuthFunc
	syncNilFetchOrigin             bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := openDB(t)
	return &env{
		t: t, db: db, q: newQueue(t, db, nil), root: t.TempDir(), fake: newFakeGit(),
		audit: &recordingEmitter{}, vars: map[string]string{}, reads: map[string]int{}, audited: true,
		fetch: noFetch, fetchOrigin: noFetch, resolveAuth: noAuth, resolveOriginAuth: noAuth,
	}
}

func (e *env) newGit(path string) (GitRunner, error) {
	if e.gitErr != nil {
		return nil, e.gitErr
	}
	if e.useRealGit {
		g, err := NewGitRunnerFactory()(path)
		if err == nil && e.wrap != nil {
			g = e.wrap(g)
		}
		return g, err
	}
	return e.fake, nil
}

func (e *env) emitter() Emitter {
	if e.audited {
		return e.audit
	}
	return nil
}

func (e *env) cfg() APIConfig {
	return APIConfig{
		DB: e.db, Queue: e.q, WorkspaceRoot: e.root, NewGitRunner: e.newGit,
		PatchStore: NewSQLPatchStore(e.db), GetVariable: varsOf(e.vars, e.reads),
		ResolveBranch: e.resolve, Recovery: e.recovery, Audit: e.emitter(),
	}
}

func (e *env) syncCfg() SyncAPIConfig {
	cfg := SyncAPIConfig{
		DB: e.db, Queue: e.q, WorkspaceRoot: e.root, NewGitRunner: e.newGit,
		Fetch: e.fetch, ResolveAuth: e.resolveAuth, GetVariable: varsOf(e.vars, e.reads),
		PatchStore: NewSQLPatchStore(e.db), FetchOrigin: e.fetchOrigin,
		ResolveOriginAuth: e.resolveOriginAuth, Audit: e.emitter(),
	}
	if e.syncNilFetchOrigin {
		cfg.FetchOrigin = nil
	}
	return cfg
}

// server mounts every route plus the sync extension through the hook the hub
// uses; a sync the hook does not handle answers {"handled": false}.
func (e *env) server() *echo.Echo {
	srv := echo.New()
	api := srv.Group("/api/v1")
	api.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if h := c.Request().Header.Get("X-Test-Auth"); h != "" {
				var info apikit.AuthInfo
				if err := json.Unmarshal([]byte(h), &info); err != nil {
					return err
				}
				apikit.SetAuthInfo(c, &info)
			}
			return next(c)
		}
	})
	RegisterRoutes(api, e.cfg())
	hook := NewCarryPatchSyncHook(e.syncCfg())
	api.POST("/workspaces/:slug/sync", func(c echo.Context) error {
		slug := c.Param("slug")
		extras, handled, err := hook(c, slug, filepath.Join(e.root, slug, "trunk"))
		if err != nil {
			return err
		}
		if !handled {
			// The hook also reports "not handled" after it wrote an error
			// response itself; the host must not write a second body then.
			if c.Response().Committed {
				return nil
			}
			return c.JSON(http.StatusOK, map[string]any{"handled": false})
		}
		return c.JSON(http.StatusOK, extras)
	})
	return srv
}

// do performs a request with an optional JSON body and credential.
func (e *env) do(method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if auth != nil {
		b, _ := json.Marshal(auth)
		req.Header.Set("X-Test-Auth", string(b))
	}
	rec := httptest.NewRecorder()
	e.server().ServeHTTP(rec, req)
	return rec
}

// want asserts the status and returns the decoded JSON object body (nil for
// an empty body).
func (e *env) want(rec *httptest.ResponseRecorder, status int) map[string]any {
	e.t.Helper()
	if rec.Code != status {
		e.t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		e.t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return m
}

// wantList asserts the status and returns the decoded JSON array body.
func (e *env) wantList(rec *httptest.ResponseRecorder, status int) []map[string]any {
	e.t.Helper()
	if rec.Code != status {
		e.t.Fatalf("status = %d, want %d; body: %s", rec.Code, status, rec.Body.String())
	}
	var l []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		e.t.Fatalf("decode list %q: %v", rec.Body.String(), err)
	}
	return l
}

// wantErr asserts the status and returns the error message and error_type.
func (e *env) wantErr(rec *httptest.ResponseRecorder, status int) (message, errorType string) {
	e.t.Helper()
	m := e.want(rec, status)
	detail, _ := m["error"].(map[string]any)
	if detail == nil {
		e.t.Fatalf("no error envelope in %s", rec.Body.String())
	}
	msg, _ := detail["message"].(string)
	typ, _ := detail["error_type"].(string)
	return msg, typ
}

// branches returns the branch_name of each patch object in a list.
func branches(list []map[string]any) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p["branch_name"].(string))
	}
	return out
}

// enqueueJob enqueues a rebuild job for slug with the given status.
func (e *env) enqueueJob(slug, status string) string {
	e.t.Helper()
	payload, _ := json.Marshal(RebuildPayload{WorkspaceSlug: slug, Strategy: StrategyRebase, SubmittedBy: "operator", IntegrationBranch: "integration"})
	id, dup, err := e.q.Enqueue(jobqueue.EnqueueParams{Type: "rebuild", Key: slug, Nonce: fmt.Sprintf("n-%d", time.Now().UnixNano()),
		Payload: payload, SubmittedBy: "operator", Group: slug + ":integration"})
	if err != nil || dup {
		e.t.Fatalf("enqueue: dup=%v err=%v", dup, err)
	}
	if status != jobqueue.StatusQueued {
		mustExec(e.t, e.db, `UPDATE jobs SET status = ? WHERE id = ?`, status, id)
	}
	return id
}

// jobs returns the rebuild jobs of the workspace, newest first.
func (e *env) jobs(slug string) []*jobqueue.Job {
	e.t.Helper()
	jobs, err := e.q.ListByKey("rebuild", slug)
	if err != nil {
		e.t.Fatalf("ListByKey: %v", err)
	}
	return jobs
}

// equalStrings compares two string slices in order.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
