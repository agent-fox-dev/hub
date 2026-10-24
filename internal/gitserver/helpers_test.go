package gitserver

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// The fixture every test starts from: org myorg (org-1) with users owner and
// member, user stranger outside it, and workspace ws of owner in myorg with a
// one-commit repository at <root>/ws/trunk.
const (
	orgID    = "org-1"
	wsPath   = "/git/myorg/ws.git"
	infoRefs = wsPath + "/info/refs?service="
)

// The pkt-line refusals the git server writes.
var (
	notFoundBody       = string(encodePktLine("ERR repository not found\n"))
	notAvailableBody   = string(encodePktLine("ERR repository not available\n"))
	readOnlyBody       = string(encodePktLine("ERR org members have read-only access to this workspace\n"))
	invalidServiceBody = string(encodePktLine("ERR invalid or missing service parameter\n"))
	internalErrorBody  = string(encodePktLine("ERR internal server error\n"))
)

// gitRoutes are the requests of the three git routes of ws, both info/refs
// services included; the second and fourth are pushes.
var gitRoutes = []struct{ method, path string }{
	{http.MethodGet, infoRefs + "git-upload-pack"},
	{http.MethodGet, infoRefs + "git-receive-pack"},
	{http.MethodPost, wsPath + "/git-upload-pack"},
	{http.MethodPost, wsPath + "/git-receive-pack"},
}

func isPush(i int) bool { return i == 1 || i == 3 }

// env is an Echo instance with the git routes mounted on a database with
// apikit's and the workspace schema, and the workspace root directory.
type env struct {
	e    *echo.Echo
	db   *sql.DB
	root string
	pats int
}

// newEnv mounts the git routes on a new Echo instance and seeds the fixture.
func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvOn(t, echo.New())
}

// newEnvOn mounts the git routes on e and seeds the fixture.
func newEnvOn(t *testing.T, e *echo.Echo) *env {
	t.Helper()
	db := testutil.OpenDB(t)
	if err := workspace.InitSchema(db); err != nil {
		t.Fatalf("workspace.InitSchema: %v", err)
	}
	v := &env{e: e, db: db, root: t.TempDir()}
	if err := MountGitHandlers(e, db, v.root); err != nil {
		t.Fatalf("MountGitHandlers: %v", err)
	}
	for _, u := range []string{"owner", "member", "stranger"} {
		testutil.SeedUser(t, db, u)
	}
	testutil.SeedOrg(t, db, orgID, "myorg", "owner")
	testutil.SeedOrgMember(t, db, orgID, "owner")
	testutil.SeedOrgMember(t, db, orgID, "member")
	v.workspace(t, "ws", "owner", orgID, "ready")
	initRepo(t, v.root, "ws")
	return v
}

// workspace seeds a workspace row of owner in org with the given status.
func (v *env) workspace(t *testing.T, slug, owner, org, status string) {
	t.Helper()
	testutil.SeedWorkspace(t, v.db, testutil.Workspace{Slug: slug, OwnerID: owner, OrgID: org, Status: status})
}

// key is an API key of user in the simple af_key_<id> format: the id is the
// key id and its hash the secret hash. It returns the credential.
func (v *env) key(t *testing.T, user string) string {
	t.Helper()
	v.pats++
	id := fmt.Sprintf("key%d%s", v.pats, user)
	seedKey(t, v.db, id, id, user)
	return "af_key_" + id
}

// seedKey inserts an API key row with the hash of secret.
func seedKey(t *testing.T, db *sql.DB, keyID, secret, user string) {
	t.Helper()
	testutil.Exec(t, db, `INSERT INTO api_keys (key_id, user_id, secret_hash, expires_days, created_at) VALUES (?, ?, ?, 365, ?)`,
		keyID, user, hashCredential(secret), apikit.NowUTC())
}

// pat is a PAT of user with scopes, in the simple af_pat_<id> format: the id
// is the token id and its hash the secret hash. It returns the credential.
func (v *env) pat(t *testing.T, user string, scopes ...string) string {
	t.Helper()
	v.pats++
	id := fmt.Sprintf("tok%d%s", v.pats, user)
	seedPAT(t, v.db, id, id, user, scopes...)
	return "af_pat_" + id
}

// seedPAT inserts a PAT row with the hash of secret.
func seedPAT(t *testing.T, db *sql.DB, tokenID, secret, user string, scopes ...string) {
	t.Helper()
	perms, _ := json.Marshal(append([]string{}, scopes...))
	testutil.Exec(t, db, `INSERT INTO pats (token_id, user_id, name, secret_hash, permissions, expires_days, created_at)
		VALUES (?, ?, ?, ?, ?, 365, ?)`, tokenID, user, tokenID, hashCredential(secret), string(perms), apikit.NowUTC())
}

// admin registers an admin token and returns it.
func (v *env) admin(t *testing.T) string {
	t.Helper()
	testutil.Exec(t, v.db, `INSERT INTO admin_config (key, value) VALUES ('admin_token_hash', ?)`, hashCredential("af_admin_root"))
	return "af_admin_root"
}

// basic is the Authorization header of credential cred (username ignored).
func basic(cred string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x:"+cred))
}

// do serves one request as cred (empty for no Authorization header).
func (v *env) do(t *testing.T, method, path, body, cred string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if cred != "" {
		req.Header.Set("Authorization", basic(cred))
	}
	return v.serve(req)
}

func (v *env) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	v.e.ServeHTTP(rec, req)
	return rec
}

// headSHA is the recorded head_sha of slug.
func (v *env) headSHA(t *testing.T, slug string) string {
	t.Helper()
	var sha sql.NullString
	if err := v.db.QueryRow(`SELECT head_sha FROM workspaces WHERE slug = ?`, slug).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	return sha.String
}

// trunk is the repository path of slug.
func (v *env) trunk(slug string) string { return filepath.Join(v.root, slug, "trunk") }

// remote starts a real HTTP server on the routes and returns the URL of ws
// with cred embedded, for the git CLI.
func (v *env) remote(t *testing.T, cred string) string {
	t.Helper()
	srv := httptest.NewServer(v.e)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("x", cred)
	u.Path = wsPath
	return u.String()
}

// gitRun runs git in dir with an isolated configuration and returns its trimmed
// output, or fails the test.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

// gitTry is gitRun for commands expected to fail; it returns the combined output.
func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// initRepo creates <root>/<slug>/trunk with one commit and returns its path.
func initRepo(t *testing.T, root, slug string) string {
	t.Helper()
	trunk := filepath.Join(root, slug, "trunk")
	if err := os.MkdirAll(trunk, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, trunk, "init", "-q")
	addCommit(t, trunk, "README.md", "init")
	return trunk
}

// addCommit commits a new file in dir and returns the new HEAD.
func addCommit(t *testing.T, dir, file, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", file)
	gitRun(t, dir, "commit", "-q", "-m", msg)
	return gitRun(t, dir, "rev-parse", "HEAD")
}

// clone clones remote into a new directory and returns it.
func clone(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, "", "clone", "-q", remote, dir)
	return dir
}

// refValue is the hash ref points to in dir, empty when it does not exist.
func refValue(dir, ref string) string {
	out, err := gitTry(dir, "rev-parse", "--verify", "-q", ref)
	if err != nil {
		return ""
	}
	return out
}

// pushSpy records the audit events and post-push hook calls of pushes.
type pushSpy struct {
	mu     sync.Mutex
	events []audit.HubEvent
	pushed chan []string
}

// spyPushes installs pre as the pre-receive hook, a recording post-push
// hook and a recording audit emitter for the rest of the test.
func spyPushes(t *testing.T, pre PreReceiveHookFunc) *pushSpy {
	t.Helper()
	origPre, origPost, origAudit := preReceiveHook, postPushHook, defaultAuditEmitter
	t.Cleanup(func() { preReceiveHook, postPushHook, defaultAuditEmitter = origPre, origPost, origAudit })
	s := &pushSpy{pushed: make(chan []string, 8)}
	RegisterPreReceiveHook(pre)
	RegisterPostPushHook(func(_ *sql.DB, _ string, branches []string) { s.pushed <- branches })
	SetAuditEmitter(s)
	return s
}

func (s *pushSpy) Emit(_ context.Context, e audit.HubEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

// Events are the audit events emitted so far.
func (s *pushSpy) Events() []audit.HubEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]audit.HubEvent(nil), s.events...)
}

// Pushed waits for the next post-push hook call and returns its branches.
func (s *pushSpy) Pushed(t *testing.T) []string {
	t.Helper()
	select {
	case b := <-s.pushed:
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("post-push hook was not called")
		return nil
	}
}

// refsUpdated is the refs_updated metadata of an audit event.
func refsUpdated(t *testing.T, e audit.HubEvent) []string {
	t.Helper()
	refs, ok := e.Metadata["refs_updated"].([]string)
	if !ok {
		t.Fatalf("refs_updated is %T; want []string", e.Metadata["refs_updated"])
	}
	return refs
}

// receivePackRequest is a receive-pack body: the commands, the capabilities
// after the first one, a flush and the packfile.
func receivePackRequest(commands []string, caps string, pack []byte) string {
	var b strings.Builder
	for i, c := range commands {
		if i == 0 {
			c += "\x00" + caps
		}
		b.Write(encodePktLine(c))
	}
	b.Write(encodePktFlush())
	b.Write(pack)
	return b.String()
}

// uploadPackWant is an upload-pack body wanting the head of trunk.
func uploadPackWant(t *testing.T, trunk string) string {
	t.Helper()
	return string(encodePktLine("want "+gitRun(t, trunk, "rev-parse", "HEAD")+"\n")) + "0000" + string(encodePktLine("done\n"))
}
