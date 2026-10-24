package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/carrypatch"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/cphost"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// TestMain pins git's default branch for every fixture of this package: the
// fixtures init repositories and clone bare ones without naming the branch,
// which only gives `main` when init.defaultBranch says so.
func TestMain(m *testing.M) {
	os.Setenv("GIT_CONFIG_COUNT", "1")
	os.Setenv("GIT_CONFIG_KEY_0", "init.defaultBranch")
	os.Setenv("GIT_CONFIG_VALUE_0", "main")
	os.Exit(m.Run())
}

// The users of the fixtures: owner owns the workspaces, member belongs to
// owner's org, stranger belongs to nothing. Every one has a personal org.
const (
	owner    = "owner"
	member   = "member"
	stranger = "stranger"
	headSHA  = "abcdef1234567890abcdef1234567890abcdef12"
)

func strPtr(s string) *string { return &s }

// swap sets *p to v for the rest of the test.
func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// env is the workspace routes, and the module's patch routes, on an Echo
// instance behind the test auth middleware, over a database with apikit's,
// the workspace's and the secrets' schema. The workspace root is a temporary
// directory, the audit emitter a recording fake, the clone queue off, and the
// git seams stubbed: archive pushes succeed, HEAD reads give headSHA,
// credential validation and the clone are off.
type env struct {
	e      *echo.Echo
	db     *sql.DB
	root   string
	events *fakeEmitter
	pushes *[]pushCall
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := testutil.OpenDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := secrets.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	e, api := testutil.API()
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatal(err)
	}
	mountPatchRoutes(api, db, nil)
	for _, u := range []string{owner, member, stranger} {
		testutil.SeedUser(t, db, u)
		org := testutil.SeedPersonalOrg(t, db, u)
		testutil.SeedOrgMember(t, db, org, u)
	}
	events := &fakeEmitter{}
	swap(t, &defaultWorkspaceRoot, t.TempDir())
	swap(t, &defaultExternalURL, "")
	swap(t, &defaultAuditEmitter, audit.Emitter(events))
	swap(t, &defaultAuditStore, nil)
	swap(t, &defaultQueue, nil)
	swap(t, &archiveHeadFn, func(string) (string, error) { return headSHA, nil })
	swap(t, &archiveOpenAndPushFn, func(string, string, transport.AuthMethod) error { return nil })
	swap(t, &validateCredentialsFn, nil)
	swap(t, &cloneFn, nil)
	swap(t, &upstreamFetchFn, nil)
	swap(t, &syncFetchAndCompareFn, nil)
	swap(t, &syncUpdateLocalRefFn, nil)
	swap(t, &syncExtension, nil)
	return &env{e: e, db: db, root: defaultWorkspaceRoot, events: events, pushes: recordPushes(t)}
}

var bindCarryPatchOnce sync.Once

// mountPatchRoutes mounts the carry-patch module's patch routes over the
// hub's locks and access rule, as cmd/af-hub does, for the behaviours of the
// hub that reach across (the cascade delete, the access of members).
func mountPatchRoutes(api *echo.Group, db *sql.DB, emitter audit.Emitter) {
	bindCarryPatchOnce.Do(cphost.Bind)
	carrypatch.RegisterPatchRoutes(api, carrypatch.APIConfig{
		DB: db, PatchStore: carrypatch.NewSQLPatchStore(db), Audit: cphost.Emitter(emitter),
	})
}

// do serves one request as auth (nil for unauthenticated).
func (v *env) do(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Request(t, v.e, method, path, body, auth)
}

// seed inserts ws and returns it; Status defaults to pending, GitURL to a
// GitHub URL of the slug.
func (v *env) seed(t *testing.T, ws *Workspace) *Workspace {
	t.Helper()
	if ws.Status == "" {
		ws.Status = "pending"
	}
	if ws.GitURL == "" {
		ws.GitURL = "https://github.com/org/" + ws.Slug
	}
	if err := insertWorkspace(v.db, ws); err != nil {
		t.Fatalf("seeding %s: %v", ws.Slug, err)
	}
	return ws
}

// ready seeds a ready workspace of ownerID.
func (v *env) ready(t *testing.T, slug, ownerID string) *Workspace {
	t.Helper()
	return v.seed(t, &Workspace{Slug: slug, OwnerID: ownerID, Status: "ready"})
}

// orgWorkspace seeds org "acme" with owner and member in it, and a ready
// carry_patch workspace of owner in it, with one registered patch.
func (v *env) orgWorkspace(t *testing.T, slug string) *Workspace {
	t.Helper()
	testutil.SeedOrg(t, v.db, "org-acme", "acme", "")
	testutil.SeedOrgMember(t, v.db, "org-acme", owner)
	testutil.SeedOrgMember(t, v.db, "org-acme", member)
	ws := v.seed(t, &Workspace{Slug: slug, OwnerID: owner, OrgID: strPtr("org-acme"), Status: "ready",
		WorkspaceMode: "carry_patch", UpstreamURL: strPtr("https://github.com/up/repo"), IntegrationBranch: strPtr("deploy")})
	v.seedPatch(t, slug, "feature/a", 1)
	return ws
}

// seedPatch inserts an active patch row of the carry-patch module's.
func (v *env) seedPatch(t *testing.T, slug, branch string, position int) {
	t.Helper()
	now := apikit.NowUTC()
	testutil.Exec(t, v.db, `INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, ?)`, slug+"-"+fmt.Sprint(position), slug, branch, position, now, now)
}

// column reads one column of the workspace row as a string ("" for NULL).
func (v *env) column(t *testing.T, slug, column string) string {
	t.Helper()
	var s sql.NullString
	if err := v.db.QueryRow(`SELECT `+column+` FROM workspaces WHERE slug = ?`, slug).Scan(&s); err != nil {
		t.Fatalf("%s of %s: %v", column, slug, err)
	}
	return s.String
}

// trunk is the trunk directory of slug under the workspace root.
func (v *env) trunk(slug string) string { return filepath.Join(v.root, slug, "trunk") }

// seedPushWorkspace seeds a ready workspace whose trunk is a repository under
// the root with main and the given branches, and whose git_url is a bare
// origin, and returns the bare repository's path.
func (v *env) seedPushWorkspace(t *testing.T, ws *Workspace, branches ...string) string {
	t.Helper()
	bare := newTrunk(t, v.trunk(ws.Slug), branches...)
	ws.GitURL, ws.Status = bare, "ready"
	v.seed(t, ws)
	return bare
}

// captureQueue makes the handlers enqueue into a queue without workers and
// returns the jobs they enqueue.
func captureQueue(t *testing.T) func() []CloneJob {
	t.Helper()
	q := &JobQueue{jobs: make(chan CloneJob, 16)}
	swap(t, &defaultQueue, q)
	return func() []CloneJob {
		var jobs []CloneJob
		for {
			select {
			case j := <-q.jobs:
				jobs = append(jobs, j)
			default:
				return jobs
			}
		}
	}
}

// storeSecrets stores key/value pairs as secrets of the workspace slug.
func storeSecrets(t *testing.T, db *sql.DB, slug string, kv ...string) {
	t.Helper()
	var entries []secrets.EntryInput
	for i := 0; i+1 < len(kv); i += 2 {
		entries = append(entries, secrets.EntryInput{Key: kv[i], Value: kv[i+1]})
	}
	if _, err := secrets.NewStore(db).CreateSecrets("workspace", slug, entries); err != nil {
		t.Fatalf("storing secrets of %s: %v", slug, err)
	}
}

// fakeEmitter records the hub audit events emitted; fail makes Emit fail.
type fakeEmitter struct {
	mu     sync.Mutex
	events []audit.HubEvent
	fail   error
}

func (f *fakeEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return f.fail
}

// ofType returns the recorded events of eventType.
func (f *fakeEmitter) ofType(eventType string) []audit.HubEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []audit.HubEvent
	for _, ev := range f.events {
		if ev.EventType == eventType {
			out = append(out, ev)
		}
	}
	return out
}

// one returns the one event of eventType, failing on any other count.
func (f *fakeEmitter) one(t *testing.T, eventType string) audit.HubEvent {
	t.Helper()
	evs := f.ofType(eventType)
	if len(evs) != 1 {
		t.Fatalf("%d %s events; want 1", len(evs), eventType)
	}
	return evs[0]
}

// fakeAuditStore answers ForceCloseSessions with its results; the rest of
// audit.Store is never called by this package.
type fakeAuditStore struct {
	audit.Store
	results []audit.ForceCloseResult
	calls   []string // "slug|reason"
}

func (s *fakeAuditStore) ForceCloseSessions(_ context.Context, slug, reason, _ string) ([]audit.ForceCloseResult, error) {
	s.calls = append(s.calls, slug+"|"+reason)
	return s.results, nil
}

// pushCall is one call of the shared push to origin.
type pushCall struct {
	refSpecs []config.RefSpec
	force    bool
	auth     transport.AuthMethod
	deadline bool
}

// recordPushes records every call of the shared push, which still runs.
func recordPushes(t *testing.T) *[]pushCall {
	t.Helper()
	var calls []pushCall
	orig := pushToOriginFn
	swap(t, &pushToOriginFn, func(ctx context.Context, repoPath, gitURL string, auth transport.AuthMethod, refSpecs []config.RefSpec, force bool, progress io.Writer) error {
		_, hasDeadline := ctx.Deadline()
		calls = append(calls, pushCall{append([]config.RefSpec(nil), refSpecs...), force, auth, hasDeadline})
		return orig(ctx, repoPath, gitURL, auth, refSpecs, force, progress)
	})
	return &calls
}

// stubPush makes the shared push return fn's error without pushing.
func stubPush(t *testing.T, fn func(ctx context.Context, auth transport.AuthMethod) error) {
	t.Helper()
	swap(t, &pushToOriginFn, func(ctx context.Context, _, _ string, auth transport.AuthMethod, _ []config.RefSpec, _ bool, _ io.Writer) error {
		return fn(ctx, auth)
	})
}

// stubSync makes the sync's fetch answer (sha, outcome, err) and its ref
// update answer refErr; it returns the branches the ref update was given.
func stubSync(t *testing.T, sha, outcome string, err, refErr error) *[]string {
	t.Helper()
	var updated []string
	swap(t, &syncFetchAndCompareFn, func(context.Context, string, transport.AuthMethod, *string, string) (string, string, error) {
		return sha, outcome, err
	})
	swap(t, &syncUpdateLocalRefFn, func(_ string, branch *string, newSHA string) error {
		updated = append(updated, newSHA)
		return refErr
	})
	return &updated
}

// request is a request on path as auth, on ctx, for the tests that cancel it.
func request(ctx context.Context, method, path string, auth *apikit.AuthInfo) *http.Request {
	req := httptest.NewRequest(method, path, nil).WithContext(ctx)
	raw, _ := json.Marshal(auth)
	req.Header.Set(testutil.AuthHeader, string(raw))
	return req
}

// gitTry runs git in dir and returns its output and error.
func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// gitRun runs git in dir with a fixed identity and returns its trimmed output.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes name with content in dir, commits it and returns the commit.
func commit(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", name)
	gitRun(t, dir, "commit", "-q", "-m", "add "+name)
	return gitRun(t, dir, "rev-parse", "HEAD")
}

// newTrunk makes a repository at dir whose HEAD is main, with one commit on
// main and one of its own on each of branches, and an empty bare origin, and
// returns the bare repository's path, which is also origin's URL.
func newTrunk(t *testing.T, dir string, branches ...string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	gitRun(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-q", "-b", "main")
	commit(t, dir, "README.md", "# trunk")
	for _, b := range branches {
		gitRun(t, dir, "checkout", "-q", "-b", b)
		commit(t, dir, strings.ReplaceAll(b, "/", "_")+".txt", b)
		gitRun(t, dir, "checkout", "-q", "main")
	}
	gitRun(t, dir, "remote", "add", "origin", bare)
	return bare
}

// bareRepo is a bare repository holding main and the given branches, each
// with a commit: a clone source.
func bareRepo(t *testing.T, branches ...string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	bare := newTrunk(t, work, branches...)
	gitRun(t, work, "push", "-q", "origin", "--all")
	return bare
}

// bareRef is the commit ref points at in the bare repository, "" when none.
func bareRef(t *testing.T, bare, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "rev-parse", "--verify", "--quiet", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// hub is the real stack: an apikit server with its auth middleware, both
// binding guards and the workspace and patch routes, for what only that
// layer proves. apiKey is an API key of user u1.
type hub struct {
	server *apikit.Server
	db     *sql.DB
	apiKey string
}

func newHub(t *testing.T) *hub {
	t.Helper()
	server, database := testutil.Server(t)
	perms := append(secrets.Permissions(), carrypatch.CarryPatchPermissions()...)
	if err := MountWorkspaceHandlers(server, database, perms...); err != nil {
		t.Fatalf("MountWorkspaceHandlers: %v", err)
	}
	if err := secrets.InitSchema(database.SqlDB); err != nil {
		t.Fatal(err)
	}
	mountPatchRoutes(server.APIGroup(), database.SqlDB, nil)
	testutil.SeedUser(t, database.SqlDB, "u1")
	return &hub{server: server, db: database.SqlDB, apiKey: testutil.APIKey(t, database.SqlDB, "u1")}
}

// do sends one request with token as the Bearer credential.
func (h *hub) do(method, path, token, body string) *httptest.ResponseRecorder {
	return testutil.Bearer(h.server.Echo(), method, path, body, token)
}

// boundPAT mints a PAT of u1 with scopes and binds it to slug.
func (h *hub) boundPAT(t *testing.T, slug string, scopes ...string) string {
	t.Helper()
	token, id := testutil.MintPAT(t, h.server.Echo(), h.apiKey, scopes...)
	testutil.BindToken(t, h.db, id, slug)
	return token
}

// waitFor polls cond until it holds or two seconds pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// expectStatus fails unless the response has code; it returns the error
// envelope's message for a refusal.
func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, code int) string {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status %d; want %d: %s", rec.Code, code, rec.Body)
	}
	if code >= 400 {
		return testutil.Error(t, rec).Message
	}
	return ""
}

// expectError fails unless the response is a refusal with code whose message
// contains message and whose error_type is errorType.
func expectError(t *testing.T, rec *httptest.ResponseRecorder, code int, message, errorType string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status %d; want %d: %s", rec.Code, code, rec.Body)
	}
	e := testutil.Error(t, rec)
	if !strings.Contains(e.Message, message) || e.ErrorType != errorType {
		t.Errorf("error %q (type %q); want %q (type %q)", e.Message, e.ErrorType, message, errorType)
	}
}

// contextOf is an Echo context of a request on path (with template route),
// for calling a handler helper directly.
func contextOf(method, path, route string) (echo.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(method, path, nil), rec)
	c.SetPath(route)
	return c, rec
}

// workspaceBody is the JSON of a create request.
func workspaceBody(slug string, extra string) string {
	body := fmt.Sprintf(`{"slug":%q,"git_url":"https://github.com/org/%s"`, slug, slug)
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

// captureLog sends the standard logger's and slog's output to the returned
// buffer for the rest of the test.
func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prevOut := log.Writer()
	log.SetOutput(buf)
	prevSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		slog.SetDefault(prevSlog)
	})
	return buf
}

// lockedBuffer is a buffer safe for the loggers and the test.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
