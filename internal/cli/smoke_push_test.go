package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// pushSmokeHub is a real hub, as cmd/af-hub wires it for the branch push: the
// apikit server with the workspace handlers, the secrets schema, the clone
// queue's workspace root, and a DuckDB-backed audit emitter. It records the
// body of every branch push request.
type pushSmokeHub struct {
	url, apiKey string
	root        string
	duck        *sql.DB

	mu     sync.Mutex
	bodies []string
}

func startPushSmokeHub(t *testing.T) *pushSmokeHub {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	config := "[server]\nport = 0\n\n[database]\npath = \"" + filepath.Join(dir, "hub.db") + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := apikit.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	database, err := apikit.OpenDatabase(cfg.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	server := apikit.NewServer(cfg, func() error { return nil })
	if err := workspace.MountWorkspaceHandlers(server, database); err != nil {
		t.Fatal(err)
	}
	if err := secrets.InitSchema(database.SqlDB); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &pushSmokeHub{root: filepath.Join(dir, "workspaces")}
	workspace.InitCloneQueue(ctx, database.SqlDB, h.root, 1)
	h.duck, err = audit.OpenDB(filepath.Join(dir, "audit.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.duck.Close() })
	if err := audit.InitSchema(h.duck); err != nil {
		t.Fatal(err)
	}
	store := audit.NewStore(h.duck)
	workspace.SetAuditDependencies(store, audit.NewEmitter(store, nil), nil)
	t.Cleanup(func() { workspace.SetAuditDependencies(nil, nil, nil) })

	now := apikit.NowUTC()
	if _, err := database.SqlDB.Exec(`INSERT INTO users (id, username, email, full_name, role, status, provider, provider_id, created_at, updated_at)
		VALUES ('u1', 'u1', 'u1@example.com', '', 'user', 'active', 'github', 'p1', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	key, err := apikit.GenerateAPIKey(database.SqlDB, "u1", 0, echo.New().Logger)
	if err != nil {
		t.Fatal(err)
	}
	h.apiKey = key.FullKey

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/push") {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			h.mu.Lock()
			h.bodies = append(h.bodies, string(body))
			h.mu.Unlock()
		}
		server.Echo().ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	h.url = srv.URL

	// The workspace: a trunk with main and the given branches, origin a bare
	// repository that has main.
	if _, err := database.SqlDB.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, status, created_at, updated_at)
		VALUES ('smoke', ?, 'u1', 'ready', ?, ?)`, h.bare(), now, now); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *pushSmokeHub) trunk() string { return filepath.Join(h.root, "smoke", "trunk") }
func (h *pushSmokeHub) bare() string  { return filepath.Join(h.root, "origin.git") }

// git runs git in dir with a fixed identity.
func smokeGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedRepos makes the trunk, with a commit on main and one on each branch, and
// the bare origin holding main.
func (h *pushSmokeHub) seedRepos(t *testing.T, branches ...string) {
	t.Helper()
	if err := os.MkdirAll(h.trunk(), 0o755); err != nil {
		t.Fatal(err)
	}
	smokeGitCmd(t, h.root, "init", "-q", "--bare", "-b", "main", h.bare())
	smokeGitCmd(t, h.trunk(), "init", "-q", "-b", "main")
	smokeGitCmd(t, h.trunk(), "commit", "-q", "--allow-empty", "-m", "main")
	for _, b := range branches {
		smokeGitCmd(t, h.trunk(), "checkout", "-q", "-b", b)
		smokeGitCmd(t, h.trunk(), "commit", "-q", "--allow-empty", "-m", b)
		smokeGitCmd(t, h.trunk(), "checkout", "-q", "main")
	}
	smokeGitCmd(t, h.trunk(), "remote", "add", "origin", h.bare())
	smokeGitCmd(t, h.trunk(), "push", "-q", "origin", "main")
}

// events are the metadata of the hub.branch.push events in the audit store, in
// the order they were recorded.
func (h *pushSmokeHub) events(t *testing.T) []map[string]any {
	t.Helper()
	rows, err := h.duck.Query(`SELECT metadata FROM hub_audit_events WHERE event_type = 'hub.branch.push' ORDER BY timestamp`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(raw), &m)
		out = append(out, m)
	}
	return out
}

// afc runs the real afc command tree against the hub.
func (h *pushSmokeHub) afc(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setupTestEnv(t)
	return runRootCmd(t, append([]string{"--endpoint-url", h.url, "--api-key", h.apiKey}, args...)...)
}

// objectCount is the number of objects in the bare repository.
func objectCount(t *testing.T, bare string) string {
	t.Helper()
	return smokeGitCmd(t, bare, "count-objects", "-v")
}

// TS-38-54 (smoke, 38-PATH-2): An agent pushes with afc and repeats the call
// to get up_to_date. Real: afc and apikit's client, the hub's HTTP handlers and
// router, wslock, the trunk and go-git's push, the audit emitter.
func TestSmoke_AfcPushTwice_TS_38_54(t *testing.T) {
	h := startPushSmokeHub(t)
	h.seedRepos(t, "fix/42-nil-map")
	tip := smokeGitCmd(t, h.trunk(), "rev-parse", "refs/heads/fix/42-nil-map")

	stdout, _, err := h.afc(t, "workspace", "push", "smoke", "fix/42-nil-map")
	if code := ExitCode(err); code != 0 {
		t.Fatalf("first push: exit %d (%v): %s", code, err, stdout)
	}
	first, _ := decodeOneDocument(t, stdout).(map[string]any)
	if first["result"] != "pushed" || first["sha"] != tip || first["branch"] != "fix/42-nil-map" {
		t.Errorf("first push printed %v", first)
	}
	objects := objectCount(t, h.bare())

	stdout, _, err = h.afc(t, "workspace", "push", "smoke", "fix/42-nil-map")
	if code := ExitCode(err); code != 0 {
		t.Fatalf("second push: exit %d (%v): %s", code, err, stdout)
	}
	if second, _ := decodeOneDocument(t, stdout).(map[string]any); second["result"] != "up_to_date" {
		t.Errorf("second push printed %v; want up_to_date", second)
	}
	if got := objectCount(t, h.bare()); got != objects {
		t.Errorf("the second push sent objects:\n%s\n%s", objects, got)
	}
	if got := smokeGitCmd(t, h.bare(), "rev-parse", "refs/heads/fix/42-nil-map"); got != tip {
		t.Errorf("origin's fix/42-nil-map = %s; want %s", got, tip)
	}
	evs := h.events(t)
	if len(evs) != 2 || evs[0]["result"] != "pushed" || evs[1]["result"] != "up_to_date" {
		t.Errorf("events %v; want pushed then up_to_date", evs)
	}
}

// TS-38-55 (smoke, 38-PATH-3): A diverged branch is refused with
// non_fast_forward through afc, then force-pushed.
func TestSmoke_AfcDivergedThenForced_TS_38_55(t *testing.T) {
	h := startPushSmokeHub(t)
	h.seedRepos(t, "feature")
	tip := smokeGitCmd(t, h.trunk(), "rev-parse", "refs/heads/feature")
	smokeGitCmd(t, h.trunk(), "push", "-q", "origin", "feature")
	work := filepath.Join(t.TempDir(), "w")
	smokeGitCmd(t, filepath.Dir(work), "clone", "-q", "-b", "feature", h.bare(), work)
	smokeGitCmd(t, work, "commit", "-q", "--allow-empty", "-m", "made on the forge")
	smokeGitCmd(t, work, "push", "-q", "origin", "feature")
	originTip := smokeGitCmd(t, h.bare(), "rev-parse", "refs/heads/feature")

	stdout, stderr, err := h.afc(t, "workspace", "push", "smoke", "feature")
	if code := ExitCode(err); code != 1 {
		t.Errorf("diverged push: exit %d; want 1 (%v)", code, err)
	}
	if !strings.Contains(stdout, "non_fast_forward:") || !strings.Contains(stdout, originTip) || !strings.Contains(stdout, "push with force to overwrite it") {
		t.Errorf("diverged push printed %q; want non_fast_forward: with origin's commit and the force hint", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr %q without --force", stderr)
	}

	stdout, stderr, err = h.afc(t, "workspace", "push", "smoke", "feature", "--force")
	if code := ExitCode(err); code != 0 {
		t.Fatalf("forced push: exit %d (%v): %s", code, err, stdout)
	}
	if lines := strings.Split(strings.TrimSpace(stderr), "\n"); len(lines) != 1 || !strings.Contains(stderr, "may be overwritten") {
		t.Errorf("stderr %q; want one warning line", stderr)
	}
	if out, _ := decodeOneDocument(t, stdout).(map[string]any); out["result"] != "forced" {
		t.Errorf("forced push printed %v; want forced", out)
	}
	h.mu.Lock()
	bodies := append([]string(nil), h.bodies...)
	h.mu.Unlock()
	if len(bodies) != 2 || bodies[0] != `{"force":false}` || bodies[1] != `{"force":true}` {
		t.Errorf("request bodies %q; want force false then true", bodies)
	}
	if got := smokeGitCmd(t, h.bare(), "rev-parse", "refs/heads/feature"); got != tip {
		t.Errorf("origin's feature = %s; want the trunk's %s", got, tip)
	}
	evs := h.events(t)
	if len(evs) != 2 || evs[0]["ok"] != false || evs[0]["error_type"] != "non_fast_forward" || evs[1]["result"] != "forced" {
		t.Errorf("events %v; want non_fast_forward then forced", evs)
	}
}
