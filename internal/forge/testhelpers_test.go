package forge

import (
	"bytes"
	"context"
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

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

const (
	ownerID = "user-owner"
	otherID = "user-other"
)

// ---------------------------------------------------------------------------
// Credentials
// ---------------------------------------------------------------------------

func apiKeyAuth() *apikit.AuthInfo {
	return &apikit.AuthInfo{UserID: ownerID, CredentialType: "api_key"}
}

func adminAuth() *apikit.AuthInfo {
	return &apikit.AuthInfo{UserID: otherID, CredentialType: "admin_token"}
}

func patAuth(perms ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{UserID: ownerID, CredentialType: "pat", Permissions: perms}
}

// ---------------------------------------------------------------------------
// Database
// ---------------------------------------------------------------------------

// openTestDB opens an in-memory SQLite database.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// createWorkspacesTable creates the workspaces table with the columns the forge
// routes and wsaccess read. workspace_mode is left nullable on purpose: the routes
// treat a NULL mode as a standard workspace.
func createWorkspacesTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS workspaces (
			slug           TEXT PRIMARY KEY,
			git_url        TEXT NOT NULL,
			owner_id       TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'active',
			clone_status   TEXT NOT NULL DEFAULT 'pending',
			workspace_mode TEXT,
			upstream_url   TEXT
		)`)
	if err != nil {
		t.Fatalf("create workspaces table: %v", err)
	}
}

// workspaceRow is a row of the workspaces table; a nil mode or upstream is NULL.
type workspaceRow struct {
	slug        string
	owner       string
	gitURL      string
	mode        *string
	upstream    *string
	status      string
	cloneStatus string
}

func ptr(s string) *string { return &s }

// addWorkspace inserts a workspace: owned by ownerID, active and cloned, a standard
// workspace on github.com/acme/widgets unless a modifier says otherwise.
func addWorkspace(t *testing.T, db *sql.DB, slug string, mods ...func(*workspaceRow)) {
	t.Helper()
	row := workspaceRow{
		slug: slug, owner: ownerID, gitURL: "https://github.com/acme/widgets",
		mode: ptr("standard"), status: "active", cloneStatus: "ready",
	}
	for _, m := range mods {
		m(&row)
	}
	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, upstream_url)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		row.slug, row.gitURL, row.owner, row.status, row.cloneStatus, row.mode, row.upstream)
	if err != nil {
		t.Fatalf("insert workspace %s: %v", slug, err)
	}
}

// carryPatch makes the workspace a carry_patch fork of upstream.
func carryPatch(upstream string) func(*workspaceRow) {
	return func(r *workspaceRow) {
		r.mode = ptr("carry_patch")
		r.upstream = ptr(upstream)
	}
}

// ---------------------------------------------------------------------------
// The forge stub
// ---------------------------------------------------------------------------

// recordedRequest is a request the forge stub received.
type recordedRequest struct {
	Method string
	Path   string
	// EscapedPath is the path as it was sent, with %2F in a segment kept.
	EscapedPath string
	Query       string
	Header      http.Header
	Body        string
}

// forgeStub is a TLS server standing in for GitHub or GitLab. It records every
// request it receives and answers with its handler, 200 and an empty JSON object
// by default.
type forgeStub struct {
	*httptest.Server

	mu       sync.Mutex
	requests []recordedRequest
	handler  http.HandlerFunc
}

func newForgeStub(t *testing.T) *forgeStub {
	t.Helper()
	s := &forgeStub{}
	s.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body)) // for a handler that reads it too
		s.mu.Lock()
		s.requests = append(s.requests, recordedRequest{
			Method: r.Method, Path: r.URL.Path, EscapedPath: r.URL.EscapedPath(), Query: r.URL.RawQuery,
			Header: r.Header.Clone(), Body: string(body),
		})
		h := s.handler
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// setHandler replaces what the stub answers.
func (s *forgeStub) setHandler(h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

// received returns the requests recorded so far.
func (s *forgeStub) received() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedRequest(nil), s.requests...)
}

func (s *forgeStub) count() int { return len(s.received()) }

// ---------------------------------------------------------------------------
// The test environment
// ---------------------------------------------------------------------------

// env is the forge routes mounted on a router, with a workspaces table, a stub for
// the forge and resolvers and a variable lookup that count their calls.
type env struct {
	t    *testing.T
	db   *sql.DB
	echo *echo.Echo
	stub *forgeStub

	originAuth   transport.AuthMethod
	upstreamAuth transport.AuthMethod
	originErr    error
	upstreamErr  error

	mu     sync.Mutex
	vars   map[string]string // "slug/KEY" -> value
	varErr error

	originCalls   atomic.Int64
	upstreamCalls atomic.Int64
	varCalls      atomic.Int64
}

// testAuthMiddleware injects the AuthInfo carried as JSON in the X-Test-Auth header.
func testAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if h := c.Request().Header.Get("X-Test-Auth"); h != "" {
				var info apikit.AuthInfo
				if err := json.Unmarshal([]byte(h), &info); err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Test-Auth header")
				}
				apikit.SetAuthInfo(c, &info)
			}
			return next(c)
		}
	}
}

// newEnv mounts the forge routes. The modifiers change the Config before it is used.
func newEnv(t *testing.T, mods ...func(*Config)) *env {
	t.Helper()
	isolateForgeEnv(t)
	e := &env{t: t, db: openTestDB(t), stub: newForgeStub(t), vars: map[string]string{}}
	createWorkspacesTable(t, e.db)
	e.originAuth = &githttp.BasicAuth{Username: "x-token-auth", Password: "origin-token"}

	cfg := Config{
		DB: e.db,
		ResolveOriginAuth: func(string) (transport.AuthMethod, error) {
			e.originCalls.Add(1)
			return e.originAuth, e.originErr
		},
		ResolveUpstreamAuth: func(string) (transport.AuthMethod, error) {
			e.upstreamCalls.Add(1)
			return e.upstreamAuth, e.upstreamErr
		},
		GetVariable: func(ownerType, owner, key string) (string, error) {
			e.varCalls.Add(1)
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.varErr != nil {
				return "", e.varErr
			}
			if v, ok := e.vars[owner+"/"+key]; ok {
				return v, nil
			}
			return "", &secrets.NotFoundError{Key: key}
		},
		Transport: e.stub.Client().Transport,
	}
	for _, m := range mods {
		m(&cfg)
	}

	e.echo = echo.New()
	e.echo.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.echo.Group("/api/v1")
	api.Use(testAuthMiddleware())
	RegisterRoutes(api, cfg)
	return e
}

// setVar sets a workspace variable the Config's GetVariable will find.
func (e *env) setVar(slug, key, value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.vars[slug+"/"+key] = value
}

// get calls GET /api/v1<path> with the credential, or without one when auth is nil.
func (e *env) get(path string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.request(path, auth, nil, "")
}

// request is get with extra request headers and a body.
func (e *env) request(path string, auth *apikit.AuthInfo, header http.Header, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1"+path, strings.NewReader(body))
	for name, values := range header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	if auth != nil {
		b, err := json.Marshal(auth)
		if err != nil {
			e.t.Fatalf("marshal auth: %v", err)
		}
		req.Header.Set("X-Test-Auth", string(b))
	}
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	return rec
}

// apiError is the error envelope of the hub's API.
type apiError struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	ErrorType string `json:"error_type"`
}

// decodeError decodes the error envelope of a response; a body that is none stops
// the test.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder) apiError {
	t.Helper()
	var env struct {
		Error *apiError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error == nil {
		t.Fatalf("status %d: the body is not an error envelope: %v\n%s", rec.Code, err, rec.Body.String())
	}
	return *env.Error
}

// forgeRoute is one of the ten forge routes; path holds %s where the slug goes and
// a sample number where a number is taken.
type forgeRoute struct {
	name string
	path string
	// takesTarget is false for the diagnostic, which does not take ?target=.
	takesTarget bool
}

var forgeRoutes = []forgeRoute{
	{"forge", "/workspaces/%s/forge", false},
	{"repository", "/workspaces/%s/forge/repository", true},
	{"issues", "/workspaces/%s/issues", true},
	{"issue", "/workspaces/%s/issues/1", true},
	{"comments", "/workspaces/%s/issues/1/comments", true},
	{"pull", "/workspaces/%s/pulls/1", true},
	{"state", "/workspaces/%s/pulls/1/state", true},
	{"files", "/workspaces/%s/pulls/1/files", true},
	{"checks", "/workspaces/%s/pulls/1/checks", true},
	{"reviews", "/workspaces/%s/pulls/1/reviews", true},
}

func (r forgeRoute) url(slug string) string {
	return strings.Replace(r.path, "%s", slug, 1)
}

// isolateForgeEnv clears the environment variables issuex reads for a token and for
// the API URL of a forge, so that the machine a test runs on does not change what
// the client sends or which forge it picks.
func isolateForgeEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "GITHUB_API_URL", "GITLAB_API_URL"} {
		t.Setenv(name, "")
	}
}

// bearer returns the token a request carried: the GitHub bearer token or the GitLab
// private token.
func bearer(r recordedRequest) string {
	if v := r.Header.Get("Authorization"); v != "" {
		return strings.TrimPrefix(v, "Bearer ")
	}
	return r.Header.Get("PRIVATE-TOKEN")
}

// githubRepositoryBody is what GitHub answers to a repository request.
const githubRepositoryBody = `{"full_name":"acme/widgets","default_branch":"main","private":false,"archived":false,"permissions":{"push":true,"pull":true,"admin":false}}`

// answerRepository makes the stub answer every request with a GitHub repository.
func (s *forgeStub) answerRepository() {
	s.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, githubRepositoryBody)
	})
}

// fakeClient is an issuex client that answers GetRepository with a fixed value and
// panics on every method the test did not give it.
type fakeClient struct {
	issuex.Client
	repository func(ctx context.Context, repo issuex.Repo) (issuex.Repository, error)
}

func (f *fakeClient) GetRepository(ctx context.Context, repo issuex.Repo) (issuex.Repository, error) {
	return f.repository(ctx, repo)
}

// optionsRecorder returns a Config modifier that makes NewClient record the Options
// it is called with and build a fake client that answers GetRepository.
func optionsRecorder(got *[]issuex.Options) func(*Config) {
	return func(cfg *Config) {
		var mu sync.Mutex
		cfg.NewClient = func(o issuex.Options) (issuex.Client, error) {
			mu.Lock()
			*got = append(*got, o)
			mu.Unlock()
			return &fakeClient{repository: func(context.Context, issuex.Repo) (issuex.Repository, error) {
				return issuex.Repository{FullName: "acme/widgets"}, nil
			}}, nil
		}
	}
}

// captureLog sends everything logged through slog and the log package to the
// returned buffer for the rest of the test.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// syncBuffer is a bytes.Buffer that can be written to from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// countingTransport counts the requests that pass through it and fails them.
type countingTransport struct {
	n atomic.Int64
}

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return nil, io.ErrUnexpectedEOF
}

// ---------------------------------------------------------------------------
// A GitHub fixture and a recording client
// ---------------------------------------------------------------------------

// githubFixture answers the requests of the nine read routes with minimal GitHub
// JSON for acme/widgets: two issues and a pull request in the issue list, one issue,
// two comments, one pull request whose head is abc123, one changed file, one check
// run and one review.
func githubFixture(w http.ResponseWriter, r *http.Request) {
	const (
		user = `{"login":"alice"}`
		when = `"2026-01-02T03:04:05Z"`
		pr   = `{"number":1,"title":"Add widgets","body":"body","state":"open","html_url":"https://github.com/acme/widgets/pull/1","draft":false,"merged":false,"user":` + user +
			`,"head":{"ref":"feature","sha":"abc123"},"base":{"ref":"main","sha":"def456"},"created_at":` + when + `,"updated_at":` + when + `}`
		issue = `{"number":1,"title":"Widgets break","body":"it breaks","state":"open","html_url":"https://github.com/acme/widgets/issues/1","user":` + user +
			`,"labels":[{"name":"af:triage"}],"created_at":` + when + `,"updated_at":` + when + `}`
		other = `{"number":2,"title":"Second","body":"","state":"open","html_url":"https://github.com/acme/widgets/issues/2","user":` + user +
			`,"labels":[],"created_at":` + when + `,"updated_at":` + when + `}`
		asPR = `{"number":3,"title":"A pull request","state":"open","user":` + user + `,"pull_request":{"url":"x"},"created_at":` + when + `,"updated_at":` + when + `}`
	)
	body := map[string]string{
		"/repos/acme/widgets":                           githubRepositoryBody,
		"/repos/acme/widgets/issues":                    `[` + issue + `,` + other + `,` + asPR + `]`,
		"/repos/acme/widgets/issues/1":                  issue,
		"/repos/acme/widgets/issues/1/comments":         `[{"id":10,"body":"first","user":` + user + `,"created_at":` + when + `,"updated_at":` + when + `},{"id":11,"body":"second","user":` + user + `,"created_at":` + when + `,"updated_at":` + when + `}]`,
		"/repos/acme/widgets/pulls/1":                   pr,
		"/repos/acme/widgets/pulls/1/files":             `[{"filename":"a.go","status":"modified","additions":3,"deletions":1}]`,
		"/repos/acme/widgets/commits/abc123/check-runs": `{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"success","html_url":"https://ci.example/1","output":{"title":"ok","summary":"all green"}}]}`,
		"/repos/acme/widgets/pulls/1/reviews":           `[{"user":` + user + `,"state":"APPROVED","body":"lgtm","submitted_at":` + when + `}]`,
	}[r.URL.Path]
	if body == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, body)
}

// recordingClient is an issuex client that records the arguments of the read methods
// and answers each with an empty result: nil slices, so that a route's handling of an
// empty list is seen. The methods the routes do not use panic.
type recordingClient struct {
	issuex.Client

	// err, when set, is what every method returns with its empty result.
	err error

	mu     sync.Mutex
	calls  []string
	repo   issuex.Repo
	ref    issuex.IssueRef
	filter issuex.IssueFilter
}

func (r *recordingClient) note(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *recordingClient) called() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *recordingClient) GetRepository(_ context.Context, repo issuex.Repo) (issuex.Repository, error) {
	r.note("GetRepository")
	r.mu.Lock()
	r.repo = repo
	r.mu.Unlock()
	return issuex.Repository{}, r.err
}

func (r *recordingClient) ListIssues(_ context.Context, repo issuex.Repo, f issuex.IssueFilter) (issuex.IssueList, error) {
	r.note("ListIssues")
	r.mu.Lock()
	r.repo, r.filter = repo, f
	r.mu.Unlock()
	return issuex.IssueList{}, r.err
}

func (r *recordingClient) ReadIssue(_ context.Context, ref issuex.IssueRef) (issuex.IssueThread, error) {
	r.note("ReadIssue")
	r.mu.Lock()
	r.ref = ref
	r.mu.Unlock()
	return issuex.IssueThread{}, r.err
}

func (r *recordingClient) ListComments(_ context.Context, ref issuex.IssueRef) (issuex.CommentList, error) {
	r.note("ListComments")
	r.mu.Lock()
	r.ref = ref
	r.mu.Unlock()
	return issuex.CommentList{}, r.err
}

func (r *recordingClient) ReadPullRequest(_ context.Context, ref issuex.IssueRef) (issuex.PullRequest, error) {
	r.note("ReadPullRequest")
	r.mu.Lock()
	r.ref = ref
	r.mu.Unlock()
	return issuex.PullRequest{}, r.err
}

func (r *recordingClient) GetPRState(_ context.Context, ref issuex.IssueRef) (issuex.PRState, error) {
	r.note("GetPRState")
	r.mu.Lock()
	r.ref = ref
	r.mu.Unlock()
	return issuex.PRState{}, r.err
}

func (r *recordingClient) ReadChangedFiles(_ context.Context, ref issuex.IssueRef) ([]issuex.ChangedFile, error) {
	r.note("ReadChangedFiles")
	r.mu.Lock()
	r.ref = ref
	r.mu.Unlock()
	return nil, r.err
}

func (r *recordingClient) GetCIChecks(_ context.Context, ref issuex.IssueRef) ([]issuex.CheckRun, error) {
	r.note("GetCIChecks")
	r.mu.Lock()
	r.ref = ref
	r.mu.Unlock()
	return nil, r.err
}

func (r *recordingClient) GetPRReviews(_ context.Context, ref issuex.IssueRef) ([]issuex.Review, error) {
	r.note("GetPRReviews")
	r.mu.Lock()
	r.ref = ref
	r.mu.Unlock()
	return nil, r.err
}

// useClient makes the routes use rc, and counts the clients built in *built.
func useClient(rc *recordingClient, built *atomic.Int64) func(*Config) {
	return func(cfg *Config) {
		cfg.NewClient = func(issuex.Options) (issuex.Client, error) {
			if built != nil {
				built.Add(1)
			}
			return rc, nil
		}
	}
}
