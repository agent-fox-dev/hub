package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/forge"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// These smoke tests run the real afc command tree against a hub router with the real
// forge handlers, the real secrets store and the real credential resolvers, and a TLS
// stub for the forge. The packages stay apart in the binaries: only this test file of
// internal/cli imports internal/forge.

const smokeAPIKey = "smoke-api-key"

// smokeWorkspacesTable is the DDL of the workspaces table of internal/workspace
// (schema.go).
const smokeWorkspacesTable = `
CREATE TABLE IF NOT EXISTS workspaces (
	slug              TEXT PRIMARY KEY,
	git_url           TEXT NOT NULL,
	branch            TEXT,
	owner_id          TEXT NOT NULL,
	org_id            TEXT,
	status            TEXT NOT NULL DEFAULT 'active',
	display_name      TEXT NOT NULL DEFAULT '',
	description       TEXT NOT NULL DEFAULT '',
	clone_status      TEXT NOT NULL DEFAULT 'pending' CHECK(clone_status IN ('pending','cloning','ready','failed','archived')),
	head_sha          TEXT,
	clone_error       TEXT,
	created_at        TEXT NOT NULL,
	updated_at        TEXT NOT NULL,
	sync_mode         TEXT NOT NULL DEFAULT 'pull_only',
	sync_status       TEXT NOT NULL DEFAULT 'idle',
	upstream_head_sha TEXT,
	last_sync_at      TEXT,
	sync_error        TEXT,
	workspace_mode    TEXT NOT NULL DEFAULT 'standard',
	upstream_url      TEXT,
	integration_branch TEXT
)`

// forgeRequestSeen is a request the forge stub received.
type forgeRequestSeen struct {
	Method, Path, Query, Token, UserAgent string
}

// smokeEnv is a hub router with the forge routes, a forge stub, and a database.
type smokeEnv struct {
	t     *testing.T
	db    *sql.DB
	store *secrets.Store
	forge *httptest.Server // TLS stand-in for the forge
	hub   *httptest.Server // the hub, plain http

	mu       sync.Mutex
	seen     []forgeRequestSeen
	answer   http.HandlerFunc
	hubReply struct {
		status     int
		retryAfter string
		body       string
	}
}

// newSmokeEnv builds the hub. base replaces the forge client's transport, when it is not nil.
func newSmokeEnv(t *testing.T, base http.RoundTripper) *smokeEnv {
	t.Helper()
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "GITHUB_API_URL", "GITLAB_API_URL"} {
		t.Setenv(name, "")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(smokeWorkspacesTable); err != nil {
		t.Fatal(err)
	}
	if err := secrets.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := forge.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	e := &smokeEnv{t: t, db: db, store: secrets.NewStore(db)}
	e.answer = func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }

	e.forge = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		e.mu.Lock()
		e.seen = append(e.seen, forgeRequestSeen{r.Method, r.URL.Path, r.URL.RawQuery, token, r.Header.Get("User-Agent")})
		answer := e.answer
		e.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(e.forge.Close)
	if base == nil {
		base = e.forge.Client().Transport
	}

	router := echo.New()
	router.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := router.Group("/api/v1")
	api.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Header.Get("Authorization") == "Bearer "+smokeAPIKey {
				apikit.SetAuthInfo(c, &apikit.AuthInfo{UserID: "user-1", CredentialType: "api_key"})
			}
			return next(c)
		}
	})
	forge.RegisterRoutes(api, forge.Config{
		DB: db,
		ResolveOriginAuth: func(slug string) (transport.AuthMethod, error) {
			return workspace.ResolveCloneAuth(e.store, slug)
		},
		ResolveUpstreamAuth: func(slug string) (transport.AuthMethod, error) {
			return workspace.ResolveUpstreamAuthMethod(e.store, slug)
		},
		GetVariable: e.store.GetVariableValue,
		Transport:   base,
		Secrets:     e.store,
	})
	e.hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		e.mu.Lock()
		e.hubReply.status = rec.Code
		e.hubReply.retryAfter = rec.Header().Get("Retry-After")
		e.hubReply.body = rec.Body.String()
		e.mu.Unlock()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(e.hub.Close)
	return e
}

// addWorkspace inserts a workspace owned by the caller.
func (e *smokeEnv) addWorkspace(slug, gitURL, mode string, upstream *string) {
	e.t.Helper()
	_, err := e.db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, upstream_url, created_at, updated_at)
		VALUES (?, ?, 'user-1', 'active', 'ready', ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, slug, gitURL, mode, upstream)
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *smokeEnv) secret(slug, key, value string) {
	e.t.Helper()
	if _, err := e.store.CreateSecrets("workspace", slug, []secrets.EntryInput{{Key: key, Value: value}}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *smokeEnv) variable(slug, key, value string) {
	e.t.Helper()
	if _, err := e.store.CreateVariables("workspace", slug, []secrets.EntryInput{{Key: key, Value: value}}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *smokeEnv) forgeRequests() []forgeRequestSeen {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]forgeRequestSeen(nil), e.seen...)
}

// afc runs the real afc command tree against the hub.
func (e *smokeEnv) afc(args ...string) (stdout string, err error) {
	e.t.Helper()
	setupTestEnv(e.t)
	stdout, _, err = runRootCmd(e.t, append([]string{"--endpoint-url", e.hub.URL, "--api-key", smokeAPIKey}, args...)...)
	return stdout, err
}

// TS-30-54: afc pr state --target upstream on a carry_patch workspace reads the upstream
// repository with the upstream credential.
// Requirement: 30-PATH-2, 30-REQ-3.2
func TestSmoke_AfcPRStateReadsTheUpstream_TS_30_54(t *testing.T) {
	e := newSmokeEnv(t, nil)
	upstream := "https://github.com/up/widgets"
	e.addWorkspace("fork", "https://github.com/me/widgets", "carry_patch", &upstream)
	e.secret("fork", "GIT_PAT", "origin-token-1")
	e.secret("fork", "UPSTREAM_GIT_PAT", "upstream-token-2")
	e.variable("fork", "FORGE_API_URL", "https://origin-must-not-be-used.invalid")
	e.variable("fork", "UPSTREAM_FORGE_API_URL", e.forge.URL)
	e.answer = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"number":42,"title":"t","state":"open","merged":false,"head":{"ref":"f","sha":"abc123"},"base":{"ref":"main","sha":"def456"},"user":{"login":"a"},"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`)
	}

	stdout, err := e.afc("pr", "state", "fork", "42", "--target", "upstream")
	if err != nil || ExitCode(err) != 0 {
		t.Fatalf("afc pr state: %v (exit %d)\n%s", err, ExitCode(err), stdout)
	}
	reqs := e.forgeRequests()
	if len(reqs) != 1 {
		t.Fatalf("the forge received %d requests; want 1: %+v", len(reqs), reqs)
	}
	if r := reqs[0]; r.Method != http.MethodGet || r.Path != "/repos/up/widgets/pulls/42" || r.Token != "upstream-token-2" {
		t.Errorf("the forge saw %+v; want GET /repos/up/widgets/pulls/42 with the upstream token", r)
	}
	doc, ok := decodeOneDocument(t, stdout).(map[string]any)
	if !ok || doc["number"] != float64(42) || doc["state"] != "open" || doc["head_sha"] != "abc123" {
		t.Errorf("stdout = %s; want the pull request's state as one document", stdout)
	}
}

// TS-30-55: afc forge status diagnoses origin and upstream without any forge request.
// Requirement: 30-PATH-3, 30-REQ-6.1
func TestSmoke_AfcForgeStatusSendsNothing_TS_30_55(t *testing.T) {
	counting := &countingRoundTripper{}
	e := newSmokeEnv(t, counting)
	upstream := "https://git.other.example/up/widgets"
	e.addWorkspace("widgets", "https://github.com/acme/widgets", "carry_patch", &upstream)
	e.secret("widgets", "GIT_PAT", "secret-pat-aaaa1111")
	e.secret("widgets", "UPSTREAM_GIT_USERNAME", "bob-secret-name")
	e.secret("widgets", "UPSTREAM_GIT_PASSWORD", "secret-password-bbbb2222")

	stdout, err := e.afc("forge", "status", "widgets")
	if err != nil || ExitCode(err) != 0 {
		t.Fatalf("afc forge status: %v (exit %d)\n%s", err, ExitCode(err), stdout)
	}
	doc, ok := decodeOneDocument(t, stdout).(map[string]any)
	if !ok {
		t.Fatalf("stdout = %s", stdout)
	}
	if doc["forge"] != "github" || doc["api_url"] != "https://api.github.com" || doc["credential"] != "pat" {
		t.Errorf("origin block = %v; want github, api.github.com and pat", doc)
	}
	if repo, _ := doc["repository"].(map[string]any); repo["owner"] != "acme" || repo["name"] != "widgets" {
		t.Errorf("repository = %v", doc["repository"])
	}
	up, _ := doc["upstream"].(map[string]any)
	if up == nil || up["forge"] != "unknown" || up["api_url"] != nil || up["credential"] != "basic" {
		t.Errorf("upstream block = %v; want an unknown forge reached with a basic credential", doc["upstream"])
	}
	for _, secret := range []string{"secret-pat-aaaa1111", "bob-secret-name", "secret-password-bbbb2222"} {
		if strings.Contains(stdout, secret) {
			t.Errorf("stdout holds %q: %s", secret, stdout)
		}
	}
	if n := counting.n.Load(); n != 0 {
		t.Errorf("the forge transport recorded %d requests; want none", n)
	}
	if n := len(e.forgeRequests()); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
}

// TS-30-56: A rate-limited forge read becomes 429 forge_rate_limited and a non-zero afc
// exit code.
// Requirement: 30-PATH-4, 30-REQ-8.4, 30-REQ-9.2
func TestSmoke_RateLimitedReadIsA429AndANonZeroExit_TS_30_56(t *testing.T) {
	const token = "smoke-pat-limited-7c1d"
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	e := newSmokeEnv(t, nil)
	e.addWorkspace("widgets", "https://github.com/acme/widgets", "standard", nil)
	e.secret("widgets", "GIT_PAT", token)
	e.variable("widgets", "FORGE_API_URL", e.forge.URL)
	e.answer = func(w http.ResponseWriter, r *http.Request) {
		// Retry-After: 1 makes issuex wait two seconds (it adds one) and try once more;
		// the forge limits again, and the answer carries the two seconds.
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"message":"API rate limit exceeded for token `+token+`"}`)
	}

	stdout, err := e.afc("issue", "get", "widgets", "7")
	if err == nil || ExitCode(err) == 0 {
		t.Fatalf("afc issue get succeeded on a rate limit (exit %d)\n%s", ExitCode(err), stdout)
	}
	e.mu.Lock()
	status, retryAfter, hubBody := e.hubReply.status, e.hubReply.retryAfter, e.hubReply.body
	e.mu.Unlock()
	if status != http.StatusTooManyRequests || retryAfter != "2" {
		t.Errorf("hub answered %d with Retry-After %q; want 429 and 2", status, retryAfter)
	}
	var hubError struct {
		Error struct {
			Message   string `json:"message"`
			ErrorType string `json:"error_type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(hubBody), &hubError); err != nil {
		t.Fatalf("the hub's answer is not an error envelope: %v\n%s", err, hubBody)
	}
	if hubError.Error.ErrorType != "forge_rate_limited" || !strings.Contains(hubError.Error.Message, "[redacted]") || strings.Contains(hubBody, token) {
		t.Errorf("the hub's error = %+v; want forge_rate_limited with [redacted] in place of the token", hubError.Error)
	}
	// afc prints the hub's message in its own error envelope.
	if !hasErrorEnvelope(stdout) || !strings.Contains(stdout, "[redacted]") || strings.Contains(stdout, token) {
		t.Errorf("stdout = %q; want afc's error envelope with the token redacted", stdout)
	}

	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(line, "widgets") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("%d log lines name the workspace; want one:\n%s", len(lines), buf)
	}
	for _, want := range []string{"target=origin", "operation=ReadIssue", "forge_status=429"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("the log line lacks %q: %s", want, lines[0])
		}
	}
	if strings.Contains(buf.String(), token) {
		t.Errorf("the log holds the token:\n%s", buf)
	}
}

// countingRoundTripper counts the requests it is given and fails them.
type countingRoundTripper struct{ n atomic.Int64 }

func (c *countingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return nil, io.ErrUnexpectedEOF
}

// lockedBuffer is a bytes.Buffer that can be written to from several goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
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
