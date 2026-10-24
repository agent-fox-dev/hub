package merge

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/aflib/jobqueue"
	"github.com/agent-fox-dev/carrypatch/gitcmd"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// The team fixture: the active, cloned workspace ws of owner in the org
// acme, of which member is a member; stranger belongs to neither.
const (
	owner    = "owner"
	member   = "member"
	stranger = "stranger"
	orgID    = "org-acme"
	ws       = "ws"
	merges   = "/api/v1/workspaces/ws/merges"
)

// recorder is an audit emitter that keeps the events it is given and fails
// with err when set.
type recorder struct {
	mu     sync.Mutex
	events []audit.HubEvent
	err    error
}

func (r *recorder) Emit(_ context.Context, e audit.HubEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return r.err
}

func (r *recorder) all() []audit.HubEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.HubEvent(nil), r.events...)
}

// env is the merge API behind the test auth middleware, on apikit's schema
// plus the workspace and job queue tables, with a branch registry in place
// of the repository and a fake batch rebase.
type env struct {
	e        *echo.Echo
	db       *sql.DB
	queue    *jobqueue.Queue
	audit    *recorder
	branches map[string]bool // "slug:branch" -> exists
	rebase   BatchRebaseFunc
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := testutil.OpenDB(t)
	if err := workspace.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatal(err)
	}
	q, err := jobqueue.New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	v := &env{db: db, queue: q, audit: &recorder{}, branches: map[string]bool{ws + ":main": true, ws + ":feature/a": true}}
	if err := RegisterHandler(q, &Handler{Audit: v.audit}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{owner, member, stranger} {
		testutil.SeedUser(t, db, u)
	}
	testutil.SeedOrg(t, db, orgID, "acme", owner)
	testutil.SeedOrgMember(t, db, orgID, owner)
	testutil.SeedOrgMember(t, db, orgID, member)
	testutil.Exec(t, db, `INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, clone_status, created_at, updated_at)
		VALUES (?, 'https://github.com/org/ws', ?, ?, 'active', 'ready', ?, ?)`, ws, owner, orgID, apikit.NowUTC(), apikit.NowUTC())
	v.e, _ = testutil.API()
	_, api := v.e, v.e.Group("/api/v1", testutil.AuthMiddleware())
	RegisterMergeRoutes(api, MergeAPIConfig{
		DB:    db,
		Queue: q,
		Audit: v.audit,
		BranchExists: func(slug, branch string) (bool, error) {
			if branch == "broken" {
				return false, context.DeadlineExceeded
			}
			return v.branches[slug+":"+branch], nil
		},
		BatchRebase: func(ctx context.Context, slug, target string, branches []string) ([]RebaseResult, error) {
			return v.rebase(ctx, slug, target, branches)
		},
	})
	return v
}

// seedJob inserts a merge job of the workspace with the given status; result
// and jobError are stored when not empty.
func (v *env) seedJob(t *testing.T, id, status, slug, target, source, result, jobError string) {
	t.Helper()
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	now := apikit.NowUTC()
	testutil.Exec(t, v.db, `INSERT INTO jobs (id, type, key, group_key, nonce, status, payload, result, error, retry_count, available_at, submitted_by, created_at, updated_at)
		VALUES (?, 'merge', ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		id, slug+":"+target+":"+source, slug+":"+target, id, status,
		`{"workspace_slug":"`+slug+`","target_branch":"`+target+`","source_ref":"`+source+`","submitted_by":"owner"}`,
		null(result), null(jobError), now, owner, now, now)
}

// jobStatus is the status of a job row.
func (v *env) jobStatus(t *testing.T, id string) string {
	t.Helper()
	var s string
	if err := v.db.QueryRow(`SELECT status FROM jobs WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (v *env) do(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Request(t, v.e, method, path, body, auth)
}

// wantError fails the test unless rec is the error status with the message
// and error_type ("" for none).
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, message, errorType string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if e := testutil.Error(t, rec); e.Message != message || e.ErrorType != errorType {
		t.Fatalf("error %+v, want %q error_type %q", e, message, errorType)
	}
}

// --- git fixtures ---

// gitIn runs git in dir and returns its trimmed output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes file with content on the current branch and commits it.
func commit(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", file)
}

// setupTrunk creates the repository <root>/<slug>/trunk, checked out on
// main, with the branches: feature/conflict (edits base.txt against main),
// feature/already-merged (merged into main), feature/not-ready (at main),
// feature/clean and feature/a (each adds a file). It returns the trunk dir.
func setupTrunk(t *testing.T, root, slug string) string {
	t.Helper()
	trunk := filepath.Join(root, slug, "trunk")
	if err := os.MkdirAll(trunk, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, trunk, "init", "-q", "-b", "main")
	commit(t, trunk, "base.txt", "base content")
	gitIn(t, trunk, "checkout", "-q", "-b", "feature/conflict")
	commit(t, trunk, "base.txt", "feature content")
	gitIn(t, trunk, "checkout", "-q", "main")
	commit(t, trunk, "base.txt", "main content")
	gitIn(t, trunk, "checkout", "-q", "-b", "feature/already-merged")
	commit(t, trunk, "merged.txt", "merged")
	gitIn(t, trunk, "checkout", "-q", "main")
	gitIn(t, trunk, "merge", "-q", "--no-ff", "feature/already-merged", "-m", "merge")
	for _, b := range []string{"feature/clean", "feature/a"} {
		gitIn(t, trunk, "checkout", "-q", "-b", b, "main")
		commit(t, trunk, strings.ReplaceAll(b, "/", "-")+".txt", b)
	}
	gitIn(t, trunk, "checkout", "-q", "-b", "feature/not-ready", "main")
	gitIn(t, trunk, "checkout", "-q", "main")
	return trunk
}

// newHandler is a Handler over root whose fetch succeeds without a remote
// and records its calls in fetched.
func newHandler(root string, fetched *[]string) *Handler {
	return &Handler{
		WorkspaceRoot: root,
		Fetch: func(_ context.Context, trunkDir, target string, auth transport.AuthMethod) error {
			if fetched != nil {
				*fetched = append(*fetched, trunkDir+" "+target)
			}
			return nil
		},
		ResolveAuth: func(string) (transport.AuthMethod, error) { return nil, nil },
	}
}

// fakeRunner is a RebaseRunner that answers from fields and records calls.
type fakeRunner struct {
	calls       []string
	rebaseSHA   string
	rebaseErr   error
	revParseErr error
	head        string // the answer of symbolic-ref HEAD
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if args[0] == "symbolic-ref" {
		return f.head, nil
	}
	return "", nil
}

func (f *fakeRunner) Rebase(_ context.Context, onto string) (string, error) {
	f.calls = append(f.calls, "rebase "+onto)
	return f.rebaseSHA, f.rebaseErr
}

func (f *fakeRunner) RevParse(_ context.Context, ref string) (string, error) {
	f.calls = append(f.calls, "rev-parse "+ref)
	return "0123456789abcdef0123456789abcdef01234567", f.revParseErr
}

var _ RebaseRunner = (*gitcmd.GitRunner)(nil)
