package cli

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/forge"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// The two tests here run afc against the real hub: apikit's server with the
// workspace handlers, the secrets store, the forge routes and real
// credentials. They prove the wiring the stub hub cannot: the credential
// resolution behind a forge read, and a branch push through the trunk.

// realHub is the hub as cmd/af-hub wires it, on a database of its own, with
// one user whose API key afc uses.
type realHub struct {
	t      *testing.T
	url    string
	apiKey string
	db     *sql.DB
	store  *secrets.Store
	root   string // the clone queue's workspace root
}

// startHub starts the hub; forgeTransport, when not nil, is what the forge
// client sends its requests through.
func startHub(t *testing.T, forgeTransport http.RoundTripper) *realHub {
	t.Helper()
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "GITHUB_API_URL", "GITLAB_API_URL"} {
		t.Setenv(name, "")
	}
	server, database := testutil.Server(t)
	if err := workspace.MountWorkspaceHandlers(server, database, forge.Permissions()...); err != nil {
		t.Fatal(err)
	}
	db := database.SqlDB
	if err := secrets.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := forge.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	h := &realHub{t: t, db: db, store: secrets.NewStore(db), root: filepath.Join(t.TempDir(), "workspaces")}
	forge.RegisterRoutes(server.APIGroup(), forge.Config{
		DB: db,
		ResolveOriginAuth: func(slug string) (transport.AuthMethod, error) {
			return workspace.ResolveCloneAuth(h.store, slug)
		},
		ResolveUpstreamAuth: func(slug string) (transport.AuthMethod, error) {
			return workspace.ResolveUpstreamAuthMethod(h.store, slug)
		},
		GetVariable: workspace.SettingsReader(db),
		Transport:   forgeTransport,
		Secrets:     h.store,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	workspace.InitCloneQueue(ctx, db, h.root, 1)

	srv := httptest.NewServer(server.Echo())
	t.Cleanup(srv.Close)
	h.url = srv.URL
	h.apiKey = testutil.APIKey(t, db, testutil.SeedUser(t, db, "u1"))
	return h
}

// workspace seeds a ready workspace of the user.
func (h *realHub) workspace(ws testutil.Workspace) {
	h.t.Helper()
	ws.OwnerID, ws.Status = "u1", "active"
	testutil.SeedWorkspace(h.t, h.db, ws)
	testutil.Exec(h.t, h.db, `UPDATE workspaces SET clone_status = 'ready' WHERE slug = ?`, ws.Slug)
}

func (h *realHub) secret(slug, key, value string) {
	h.t.Helper()
	if _, err := h.store.CreateSecrets("workspace", slug, []secrets.EntryInput{{Key: key, Value: value}}); err != nil {
		h.t.Fatal(err)
	}
}

// setting sets one workspace setting.
func (h *realHub) setting(slug, key, value string) {
	h.t.Helper()
	testutil.Exec(h.t, h.db, `UPDATE workspaces SET settings = json_set(settings, '$.'||?, ?) WHERE slug = ?`, key, value, slug)
}

// afc runs afc against the hub as the user.
func (h *realHub) afc(args ...string) (stdout, stderr string, err error) {
	h.t.Helper()
	return afc(h.t, h.url, h.apiKey, "", args...)
}

// afc pr state --target upstream on a carry_patch workspace reaches the
// upstream repository on its forge with the upstream credential, which the
// hub resolves from the workspace's secrets, and never shows the credential.
func TestE2E_PRStateReadsTheUpstreamWithItsCredential(t *testing.T) {
	var mu sync.Mutex
	var seen []string // "METHOD path token"
	stub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"number":42,"title":"t","state":"open","merged":false,"head":{"ref":"f","sha":"abc123"},"base":{"ref":"main","sha":"def456"},"user":{"login":"a"},"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`)
	}))
	t.Cleanup(stub.Close)
	h := startHub(t, stub.Client().Transport)
	h.workspace(testutil.Workspace{Slug: "fork", GitURL: "https://github.com/me/widgets", WorkspaceMode: "carry_patch", UpstreamURL: "https://github.com/up/widgets"})
	h.secret("fork", "GIT_PAT", "origin-token-1")
	h.secret("fork", "UPSTREAM_GIT_PAT", "upstream-token-2")
	h.setting("fork", "forge_api_url", "https://origin-must-not-be-used.invalid")
	h.setting("fork", "upstream_forge_api_url", stub.URL)

	stdout, _, err := h.afc("pr", "state", "fork", "42", "--target", "upstream")
	if err != nil {
		t.Fatalf("afc pr state: %v\n%s", err, stdout)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "GET /repos/up/widgets/pulls/42 upstream-token-2" {
		t.Errorf("the forge saw %q; want one GET /repos/up/widgets/pulls/42 with the upstream token", seen)
	}
	doc, _ := decodeOneDocument(t, stdout).(map[string]any)
	if doc["number"] != 42.0 || doc["state"] != "open" || doc["head_sha"] != "abc123" || strings.Contains(stdout, "token-") {
		t.Errorf("stdout = %s; want the pull request's state without any credential", stdout)
	}
}

// git runs git in dir with a fixed identity and returns its trimmed output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// afc workspace push of a branch origin has moved on is refused as
// non_fast_forward, exit 1, and goes through with --force, which warns on
// stderr and sends force true; the hub, the trunk and go-git's push are real.
func TestE2E_PushRefusesDivergedThenForces(t *testing.T) {
	h := startHub(t, nil)
	bare, trunk := filepath.Join(h.root, "origin.git"), filepath.Join(h.root, "ws", "trunk")
	if err := os.MkdirAll(trunk, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, h.root, "init", "-q", "--bare", "-b", "main", bare)
	git(t, trunk, "init", "-q", "-b", "main")
	git(t, trunk, "commit", "-q", "--allow-empty", "-m", "main")
	git(t, trunk, "checkout", "-q", "-b", "feature")
	git(t, trunk, "commit", "-q", "--allow-empty", "-m", "feature")
	git(t, trunk, "checkout", "-q", "main")
	git(t, trunk, "remote", "add", "origin", bare)
	git(t, trunk, "push", "-q", "origin", "main", "feature")
	h.workspace(testutil.Workspace{Slug: "ws", GitURL: bare})
	tip := git(t, trunk, "rev-parse", "refs/heads/feature")

	// Origin's feature moves on without the trunk.
	work := filepath.Join(t.TempDir(), "w")
	git(t, filepath.Dir(work), "clone", "-q", "-b", "feature", bare, work)
	git(t, work, "commit", "-q", "--allow-empty", "-m", "made on the forge")
	git(t, work, "push", "-q", "origin", "feature")
	originTip := git(t, bare, "rev-parse", "refs/heads/feature")

	stdout, stderr, err := h.afc("workspace", "push", "ws", "feature")
	if code := ExitCode(err); code != 1 || stderr != "" {
		t.Errorf("diverged push: exit %d (%v), stderr %q; want 1 and nothing on stderr", code, err, stderr)
	}
	if msg := errorMessage(t, stdout); !strings.Contains(msg, "non_fast_forward:") || !strings.Contains(msg, originTip) || !strings.Contains(msg, "force") {
		t.Errorf("diverged push printed %q; want non_fast_forward with origin's commit and the force hint", stdout)
	}
	if got := git(t, bare, "rev-parse", "refs/heads/feature"); got != originTip {
		t.Errorf("origin's feature = %s after the refusal; want %s untouched", got, originTip)
	}

	stdout, stderr, err = h.afc("workspace", "push", "ws", "feature", "--force")
	if code := ExitCode(err); code != 0 {
		t.Fatalf("forced push: exit %d (%v): %s", code, err, stdout)
	}
	if lines := strings.Split(strings.TrimSpace(stderr), "\n"); len(lines) != 1 || !strings.Contains(stderr, "may be overwritten") {
		t.Errorf("stderr %q; want one warning line", stderr)
	}
	if out, _ := decodeOneDocument(t, stdout).(map[string]any); out["result"] != "forced" || out["sha"] != tip {
		t.Errorf("forced push printed %v; want forced at %s", out, tip)
	}
	if got := git(t, bare, "rev-parse", "refs/heads/feature"); got != tip {
		t.Errorf("origin's feature = %s; want the trunk's %s", got, tip)
	}
}
