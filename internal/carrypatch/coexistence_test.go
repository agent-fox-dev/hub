package carrypatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/gitserver"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// ===========================================================================
// Spec 01, task 9: the rebuild worktree coexists with pushes through the git
// server, clones, and git gc. Requirement: 01-REQ-9
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-01-63: the rebuild never runs gc --prune=now or reflog expire
// Requirement: 01-REQ-9.4
// ---------------------------------------------------------------------------

func assertNoGCCommands(t *testing.T, label string, calls [][]string) {
	t.Helper()
	for _, args := range calls {
		if len(args) == 0 {
			continue
		}
		switch args[0] {
		case "gc", "reflog", "prune", "repack":
			t.Errorf("%s: rebuild ran forbidden maintenance command %v", label, args)
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--prune=now") || strings.Contains(joined, "reflog expire") {
			t.Errorf("%s: rebuild ran %v", label, args)
		}
	}
}

func mockRunCalls(m *mockGitRunner) [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out [][]string
	for _, c := range m.RunCalls {
		out = append(out, append([]string(nil), c.Args...))
	}
	return out
}

func TestCoexistence_TS_01_63_NeverRunsGCOrReflogExpire(t *testing.T) {
	type mockScenario struct {
		name  string
		setup func(e *wtEnv)
		fails bool
	}
	scenarios := []mockScenario{
		{name: "success", setup: func(*wtEnv) {}},
		{name: "conflict", fails: true, setup: func(e *wtEnv) {
			e.wt.CherryPickFunc = func(context.Context, string) error { return &CherryPickConflictError{} }
			base := e.wt.RunFunc
			e.wt.RunFunc = func(ctx context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "diff" {
					return "conflict.txt", nil
				}
				return base(ctx, args...)
			}
		}},
		{name: "failure", fails: true, setup: func(e *wtEnv) {
			e.wt.CherryPickFunc = func(context.Context, string) error { return errors.New("boom") }
		}},
	}
	for _, sc := range scenarios {
		t.Run("mock_"+sc.name, func(t *testing.T) {
			e := newWtEnv(t, onePatch())
			sc.setup(e)
			_, _, err := e.h.HandleRebuildJob(context.Background(), e.payload(StrategyRebase, ""))
			if sc.fails && err == nil {
				t.Fatal("expected the scenario to fail")
			}
			if !sc.fails && err != nil {
				t.Fatalf("rebuild: %v", err)
			}
			assertNoGCCommands(t, "trunk", mockRunCalls(e.trunk))
			assertNoGCCommands(t, "worktree", mockRunCalls(e.wt))
		})
	}

	// The same through real runners for every core outcome.
	for _, sc := range coreScenarios {
		t.Run("real_"+strings.ReplaceAll(sc.name, " ", "_"), func(t *testing.T) {
			e := newRealEnv(t)
			_, _, _ = e.execute(sc)
			for label, r := range map[string]*hookRunner{"trunk": e.trunkRunner, "worktree": e.wtRunner} {
				if r == nil {
					continue
				}
				r.mu.Lock()
				calls := append([][]string(nil), r.runs...)
				r.mu.Unlock()
				assertNoGCCommands(t, label, calls)
			}
		})
	}

	// The executor source never spells these commands.
	src, err := os.ReadFile("rebuild_executor.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`gc --prune=now|reflog expire|"gc"|"reflog"|"prune"|"repack"`)
	if m := re.Find(src); m != nil {
		t.Errorf("rebuild_executor.go contains %q", m)
	}
}

// ---------------------------------------------------------------------------
// TS-01-64: objects reachable only from a worktree's HEAD survive git gc
// Requirement: 01-REQ-9.5
// ---------------------------------------------------------------------------

func TestCoexistence_TS_01_64_GCKeepsWorktreeOnlyCommits(t *testing.T) {
	gcVariants := [][]string{{"gc"}, {"gc", "--aggressive"}, {"gc", "--prune=now"}}
	rng := rand.New(rand.NewSource(0x6c))
	const iterations = 9
	for i := 0; i < iterations; i++ {
		chainLen := 1 + rng.Intn(5)
		gcArgs := gcVariants[i%len(gcVariants)]
		expireReflog := rng.Intn(2) == 0
		name := fmt.Sprintf("case%d_chain%d_%s_expire%v", i, chainLen, strings.Join(gcArgs, ""), expireReflog)
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			trunk, baseSHA := setupCarryPatchRepo(t, root, "ws-gc")
			wtPath := filepath.Join(root, "ws-gc", "rebuild", "job-gc")
			runGitCmd(t, trunk, "worktree", "add", "--detach", wtPath, baseSHA)

			var chain []string
			for c := 0; c < chainLen; c++ {
				writeFileHelper(t, filepath.Join(wtPath, fmt.Sprintf("chain-%d.txt", c)), fmt.Sprintf("chain %d", c))
				runGitCmd(t, wtPath, "add", ".")
				runGitCmd(t, wtPath, "commit", "-m", fmt.Sprintf("worktree-only %d", c))
				chain = append(chain, runGitCmd(t, wtPath, "rev-parse", "HEAD"))
			}
			// Control: an object nothing references.
			tree := runGitCmd(t, trunk, "rev-parse", "HEAD^{tree}")
			orphan := runGitCmd(t, trunk, "commit-tree", tree, "-m", "orphan")

			// None of the chain is on any branch.
			if out := runGitCmd(t, trunk, "branch", "--contains", chain[len(chain)-1]); out != "" {
				t.Fatalf("setup: chain commit is on branches %q", out)
			}
			if expireReflog {
				// Take the reflog out of the picture: only HEAD may keep the chain alive.
				runGitCmd(t, wtPath, "reflog", "expire", "--expire=now", "--all")
			}

			runGitCmd(t, trunk, gcArgs...)

			for _, c := range chain {
				if _, err := gitTry(trunk, "cat-file", "-e", c+"^{commit}"); err != nil {
					t.Errorf("commit %s reachable only from the worktree HEAD was removed by %v", c, gcArgs)
				}
			}
			if got := runGitCmd(t, wtPath, "rev-parse", "HEAD"); got != chain[len(chain)-1] {
				t.Errorf("worktree HEAD = %s, want %s", got, chain[len(chain)-1])
			}
			if _, err := gitTry(wtPath, "status"); err != nil {
				t.Errorf("worktree unusable after %v: %v", gcArgs, err)
			}
			if _, err := gitTry(wtPath, "log", "-1"); err != nil {
				t.Errorf("git log in worktree failed after %v: %v", gcArgs, err)
			}
			// The control proves gc really prunes when asked to: the orphan goes
			// with --prune=now, while the worktree chain stays.
			if len(gcArgs) == 2 && gcArgs[1] == "--prune=now" {
				if _, err := gitTry(trunk, "cat-file", "-e", orphan+"^{commit}"); err == nil {
					t.Errorf("control: unreferenced commit %s survived gc --prune=now", orphan)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Git server harness for TS-01-60 and TS-01-65
// ---------------------------------------------------------------------------

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

const (
	coHubOrgSlug = "myorg"
	coAdminToken = "af_admin_coexistence"
)

// hubServer is the hub's smart HTTP git server over the realEnv workspace.
type hubServer struct {
	srv *httptest.Server
	db  *sql.DB
	url string // clone URL with credentials
}

func newHubServer(t *testing.T, e *realEnv) *hubServer {
	t.Helper()
	db := openTestDB(t)
	createWorkspacesTable(t, db)
	for _, stmt := range []string{
		`CREATE TABLE orgs (id TEXT PRIMARY KEY, name TEXT, slug TEXT UNIQUE, url TEXT, status TEXT DEFAULT 'active', created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE admin_config (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO orgs (id, name, slug, created_at, updated_at) VALUES ('org-1', 'My Org', 'myorg', 'x', 'x')`,
		// sha256("af_admin_coexistence")
		fmt.Sprintf(`INSERT INTO admin_config (key, value) VALUES ('admin_token_hash', '%s')`, sha256Hex(coAdminToken)),
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed git server db: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, org_id, clone_status, created_at, updated_at)
		VALUES (?, 'https://example.invalid/repo', 'user-1', 'org-1', 'ready', 'x', 'x')`, e.slug); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	ec := echo.New()
	if err := gitserver.MountGitHandlers(ec, db, e.root); err != nil {
		t.Fatalf("MountGitHandlers: %v", err)
	}
	srv := httptest.NewServer(ec)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	return &hubServer{
		srv: srv,
		db:  db,
		url: fmt.Sprintf("http://x-token-auth:%s@%s/git/%s/%s.git", coAdminToken, host, coHubOrgSlug, e.slug),
	}
}

func (h *hubServer) headSHA(t *testing.T, slug string) string {
	t.Helper()
	var sha sql.NullString
	if err := h.db.QueryRow(`SELECT head_sha FROM workspaces WHERE slug = ?`, slug).Scan(&sha); err != nil {
		t.Fatalf("read head_sha: %v", err)
	}
	return sha.String
}

// blockedRebuild runs a rebuild of the realEnv workspace that blocks inside
// the first cherry-pick, with its worktree registered and the workspace lock
// free.
type blockedRebuild struct {
	e       *realEnv
	blocked chan struct{}
	release chan struct{}
	done    chan rebuildOutcome
	wtPath  string
}

type rebuildOutcome struct {
	res any
	err error
}

func startBlockedRebuild(t *testing.T, e *realEnv, jobID string) *blockedRebuild {
	t.Helper()
	b := &blockedRebuild{
		e:       e,
		blocked: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan rebuildOutcome, 1),
		wtPath:  filepath.Join(e.root, e.slug, "rebuild", jobID),
	}
	base := e.h.NewGitRunner
	e.h.NewGitRunner = func(path string) (GitRunner, error) {
		r, err := base(path)
		if err == nil && path != e.trunk {
			var once sync.Once
			r.(*hookRunner).onCherryPick = func(context.Context, string) error {
				once.Do(func() {
					close(b.blocked)
					<-b.release
				})
				return nil
			}
		}
		return r, err
	}
	go func() {
		res, _, err := e.run(context.Background(), jobID, StrategyRebase, "")
		b.done <- rebuildOutcome{res, err}
	}()
	<-b.blocked
	// Make sure a failing assertion never leaves the rebuild goroutine (and
	// the guard) behind.
	t.Cleanup(func() {
		select {
		case <-b.release:
		default:
			close(b.release)
		}
	})
	return b
}

func (b *blockedRebuild) finish(t *testing.T) *RebuildResult {
	t.Helper()
	select {
	case <-b.release:
	default:
		close(b.release)
	}
	out := <-b.done
	if out.err != nil {
		t.Fatalf("rebuild: %v", out.err)
	}
	return out.res.(*RebuildResult)
}

// ---------------------------------------------------------------------------
// TS-01-60: a push through the hub during patch application updates refs and
// resets the trunk working tree, without affecting the rebuild worktree
// Requirement: 01-REQ-9.1
// ---------------------------------------------------------------------------

type wtFingerprint struct{ head, status, index, base, entries string }

func fingerprintWorktree(t *testing.T, path string) wtFingerprint {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(path, "base.txt"))
	if err != nil {
		t.Fatalf("read worktree file: %v", err)
	}
	ents, _ := os.ReadDir(path)
	var names []string
	for _, en := range ents {
		if en.Name() != ".git" {
			names = append(names, en.Name())
		}
	}
	return wtFingerprint{
		head:    runGitCmd(t, path, "rev-parse", "HEAD"),
		status:  runGitCmd(t, path, "status", "--porcelain"),
		index:   runGitCmd(t, path, "ls-files", "-s"),
		base:    string(b),
		entries: strings.Join(names, ","),
	}
}

func TestCoexistence_TS_01_60_PushDuringApplication(t *testing.T) {
	e := newRealEnv(t)
	e.setPatches("feature/patch-a")
	hub := newHubServer(t, e)

	client := filepath.Join(t.TempDir(), "client")
	runGitCmd(t, "", "clone", hub.url, client)
	configGitUserCmd(t, client)

	patchTipBefore := runGitCmd(t, e.trunk, "rev-parse", "feature/patch-a")
	if got := runGitCmd(t, e.trunk, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Fatalf("setup: trunk is on %q, want main", got)
	}

	b := startBlockedRebuild(t, e, "job-60")
	if !wslock.RebuildActive(e.slug) {
		t.Fatal("setup: rebuild guard not set while blocked")
	}
	if got := runGitCmd(t, e.trunk, "worktree", "list", "--porcelain"); !strings.Contains(got, b.wtPath) {
		t.Fatalf("setup: rebuild worktree not registered:\n%s", got)
	}
	wtBefore := fingerprintWorktree(t, b.wtPath)

	// A developer pushes a new commit to the checked-out branch and to the
	// registered patch branch.
	writeFileHelper(t, filepath.Join(client, "pushed-main.txt"), "pushed to main")
	runGitCmd(t, client, "add", ".")
	runGitCmd(t, client, "commit", "-m", "push to main")
	pushedMain := runGitCmd(t, client, "rev-parse", "HEAD")
	runGitCmd(t, client, "checkout", "-b", "feature/patch-a", "origin/feature/patch-a")
	writeFileHelper(t, filepath.Join(client, "patch-a-more.txt"), "more patch a")
	runGitCmd(t, client, "add", ".")
	runGitCmd(t, client, "commit", "-m", "push to patch-a")
	pushedPatch := runGitCmd(t, client, "rev-parse", "HEAD")
	runGitCmd(t, client, "push", "origin", "main", "feature/patch-a")

	// The refs were updated.
	if got := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/feature/patch-a"); got != pushedPatch {
		t.Errorf("patch branch = %s, want pushed %s", got, pushedPatch)
	}
	if got := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/main"); got != pushedMain {
		t.Errorf("main = %s, want pushed %s", got, pushedMain)
	}
	// updateHeadSHA's TryLock succeeded (the workspace lock was free during
	// patch application), so the trunk working tree was hard-reset.
	if got := runGitCmd(t, e.trunk, "rev-parse", "HEAD"); got != pushedMain {
		t.Errorf("trunk HEAD = %s, want the pushed commit %s", got, pushedMain)
	}
	if st := runGitCmd(t, e.trunk, "status", "--porcelain"); st != "" {
		t.Errorf("trunk working tree not reset to the pushed HEAD: %q", st)
	}
	if _, err := os.Stat(filepath.Join(e.trunk, "pushed-main.txt")); err != nil {
		t.Errorf("trunk working tree lacks the pushed file: %v", err)
	}
	if got := hub.headSHA(t, e.slug); got != pushedMain {
		t.Errorf("head_sha = %q, want %s", got, pushedMain)
	}
	assertLockFree(t, e.slug)

	// The rebuild worktree is untouched.
	if wtAfter := fingerprintWorktree(t, b.wtPath); wtAfter != wtBefore {
		t.Errorf("rebuild worktree changed by the push:\nbefore=%+v\nafter=%+v", wtBefore, wtAfter)
	}

	// The run completes with the snapshot it took, not the pushed commit.
	res := b.finish(t)
	if len(res.PatchResults) != 1 || res.PatchResults[0].SourceSHA != patchTipBefore {
		t.Errorf("patch results = %+v, want source_sha %s", res.PatchResults, patchTipBefore)
	}
	if got := runGitCmd(t, e.trunk, "rev-parse", "HEAD"); got != pushedMain {
		t.Errorf("trunk HEAD after rebuild = %s, want %s", got, pushedMain)
	}
	if _, err := os.Stat(b.wtPath); !os.IsNotExist(err) {
		t.Errorf("rebuild worktree %s still exists (err=%v)", b.wtPath, err)
	}
	if n := e.worktreeCount(); n != 1 {
		t.Errorf("worktree list shows %d entries after the run, want only the trunk", n)
	}
}

// ---------------------------------------------------------------------------
// TS-01-65: clones and fetches see the pre-rebuild integration tip until
// phase two updates the ref. Requirement: 01-REQ-9.6
// ---------------------------------------------------------------------------

func TestCoexistence_TS_01_65_ServedTipIsPreRebuildUntilPhaseTwo(t *testing.T) {
	e := newRealEnv(t)
	e.setPatches("feature/patch-a", "feature/patch-b")
	hub := newHubServer(t, e)
	pre := runGitCmd(t, e.trunk, "rev-parse", "refs/heads/integration")

	b := startBlockedRebuild(t, e, "job-65")

	lsRemote := func() string {
		out := runGitCmd(t, "", "ls-remote", hub.url, "refs/heads/integration")
		f := strings.Fields(out)
		if len(f) == 0 {
			t.Fatalf("ls-remote returned no integration ref: %q", out)
		}
		return f[0]
	}

	// During patch application.
	if got := lsRemote(); got != pre {
		t.Errorf("served integration tip = %s during application, want pre-rebuild %s", got, pre)
	}
	client := filepath.Join(t.TempDir(), "client")
	runGitCmd(t, "", "clone", "--branch", "integration", hub.url, client)
	if got := runGitCmd(t, client, "rev-parse", "HEAD"); got != pre {
		t.Errorf("cloned integration tip = %s, want pre-rebuild %s", got, pre)
	}
	runGitCmd(t, client, "fetch", "origin")
	if got := runGitCmd(t, client, "rev-parse", "origin/integration"); got != pre {
		t.Errorf("fetched integration tip = %s during application, want %s", got, pre)
	}

	// After phase two.
	res := b.finish(t)
	if res.IntegrationHeadSHA == "" || res.IntegrationHeadSHA == pre {
		t.Fatalf("rebuild result SHA %q must differ from the pre-rebuild tip %s", res.IntegrationHeadSHA, pre)
	}
	if got := lsRemote(); got != res.IntegrationHeadSHA {
		t.Errorf("served integration tip = %s after completion, want %s", got, res.IntegrationHeadSHA)
	}
	runGitCmd(t, client, "fetch", "origin")
	if got := runGitCmd(t, client, "rev-parse", "origin/integration"); got != res.IntegrationHeadSHA {
		t.Errorf("fetched integration tip = %s after completion, want %s", got, res.IntegrationHeadSHA)
	}
}
