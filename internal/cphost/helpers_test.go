package cphost

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/aflib/jobqueue"
	"github.com/agent-fox-dev/carrypatch"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/gitserver"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/workspace"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// hubRecorder records the hub's audit events; err is what Emit returns.
type hubRecorder struct {
	events []audit.HubEvent
	err    error
}

func (r *hubRecorder) Emit(_ context.Context, ev audit.HubEvent) error {
	r.events = append(r.events, ev)
	return r.err
}

// nopEmitter is a module emitter that drops events.
type nopEmitter struct{}

func (nopEmitter) Emit(context.Context, carrypatch.Event) error { return nil }

// variables is the module's variable lookup; a missing key is an error.
type variables map[string]string

func (v variables) get(_, _, key string) (string, error) {
	if val, ok := v[key]; ok {
		return val, nil
	}
	return "", fmt.Errorf("variable %s not found", key)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := tryGit(dir, args...)
	if err != nil {
		t.Fatalf("git %v (dir=%s): %v\n%s", args, dir, err, out)
	}
	return out
}

func tryGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// branchWithCommit creates branch at HEAD with one commit and returns its SHA.
func branchWithCommit(t *testing.T, dir, branch string) string {
	t.Helper()
	runGit(t, dir, "checkout", "-q", "-b", branch)
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", branch)
	return runGit(t, dir, "rev-parse", "HEAD")
}

// hub is a database with the hub's schema holding the users owner and member of
// the org myorg and owner's carry_patch workspace myws (patch p1 registered),
// myws's trunk with a local bare origin, and a queue accepting rebuild jobs.
type hub struct {
	db            *sql.DB
	owner, member string
	queue         *jobqueue.Queue
	root          string // workspace root; the trunk is root/myws/trunk
	trunk, origin string
}

func newHub(t *testing.T) *hub {
	t.Helper()
	db := testutil.OpenDB(t)
	for _, init := range []func(*sql.DB) error{workspace.InitSchema, wsaccess.InitBindingSchema, jobqueue.InitSchema, jobqueue.MigrateGroupKey, jobqueue.MigrateProgress} {
		if err := init(db); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	queue, err := jobqueue.New(db, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Register("rebuild", func(context.Context, json.RawMessage) (any, bool, error) { return nil, false, nil }, nil); err != nil {
		t.Fatal(err)
	}

	owner := testutil.SeedUser(t, db, "owner")
	member := testutil.SeedUser(t, db, "member")
	org := testutil.SeedOrg(t, db, "org-1", "myorg", owner)
	testutil.SeedOrgMember(t, db, org, member)

	root := t.TempDir()
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "", "init", "-q", "--bare", "-b", "main", origin)
	trunk := filepath.Join(root, "myws", "trunk")
	runGit(t, "", "init", "-q", "-b", "main", trunk)
	runGit(t, trunk, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, trunk, "remote", "add", "origin", origin)
	runGit(t, trunk, "push", "-q", "origin", "main")

	testutil.SeedWorkspace(t, db, testutil.Workspace{Slug: "myws", OwnerID: owner, OrgID: org, GitURL: origin,
		WorkspaceMode: "carry_patch", IntegrationBranch: "main-int"})
	testutil.Exec(t, db, `UPDATE workspaces SET clone_status = 'ready' WHERE slug = 'myws'`)
	now := time.Now().UTC().Format(time.RFC3339)
	testutil.Exec(t, db, `INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at)
		VALUES ('p1-id', 'myws', 'p1', 1, 'active', ?, ?)`, now, now)
	return &hub{db: db, owner: owner, member: member, queue: queue, root: root, trunk: trunk, origin: origin}
}

// clone mounts the hub's git server with the module's hooks wired as cmd/af-hub
// wires them, under vars, and returns owner's clone of myws from the hub.
func (h *hub) clone(t *testing.T, vars variables) string {
	t.Helper()
	testutil.Exec(t, h.db, `INSERT INTO api_keys (key_id, user_id, secret_hash, expires_days, created_at) VALUES ('owner', ?, ?, 0, ?)`,
		h.owner, fmt.Sprintf("%x", sha256.Sum256([]byte("s3cret"))), time.Now().UTC().Format(time.RFC3339))

	noAuth := func(string) (transport.AuthMethod, error) { return nil, nil }
	gitserver.RegisterPreReceiveHook(PreReceive(carrypatch.NewPreReceiveHook(carrypatch.PreReceiveHookDeps{
		GetVariable: vars.get, ResolveAuth: noAuth, WorkspaceRoot: h.root, Logger: quiet,
	})))
	gitserver.RegisterPostPushHook(PostPush(carrypatch.NewPostPushRebuildHook(h.queue, vars.get, carrypatch.PostPushMirrorDeps{
		ResolveAuth: noAuth, WorkspaceRoot: h.root, Audit: nopEmitter{},
	})))
	t.Cleanup(func() {
		gitserver.RegisterPreReceiveHook(nil)
		gitserver.RegisterPostPushHook(nil)
	})

	e := echo.New()
	if err := gitserver.MountGitHandlers(e, h.db, h.root); err != nil {
		t.Fatalf("MountGitHandlers: %v", err)
	}
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	remote := strings.Replace(srv.URL, "http://", "http://x-token-auth:af_owner_s3cret@", 1) + "/git/myorg/myws.git"
	clone := t.TempDir()
	runGit(t, "", "clone", "-q", remote, clone)
	return clone
}

// waitFor polls cond until it holds or ten seconds pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
