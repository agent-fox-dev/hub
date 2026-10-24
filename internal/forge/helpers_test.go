package forge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

const (
	ownerID  = "user-owner"
	otherID  = "user-other"
	memberID = "user-member"

	// ingressSecret is the webhook secret webhookWorkspace gives a workspace.
	ingressSecret = "ingress-secret-of-16-chars"
	webhookPath   = "/workspaces/widgets/forge/webhook"
)

func apiKeyAuth() *apikit.AuthInfo             { return testutil.User(ownerID) }
func adminAuth() *apikit.AuthInfo              { return testutil.Admin() }
func patAuth(perms ...string) *apikit.AuthInfo { return testutil.PAT(ownerID, perms...) }

// ---------------------------------------------------------------------------
// Workspaces
// ---------------------------------------------------------------------------

// addWorkspace inserts a workspace: owned by ownerID, active, a standard workspace on
// github.com/acme/widgets unless a modifier says otherwise.
func addWorkspace(t *testing.T, db *sql.DB, slug string, mods ...func(*testutil.Workspace)) {
	t.Helper()
	ws := testutil.Workspace{Slug: slug, OwnerID: ownerID, Status: "active", GitURL: "https://github.com/acme/widgets"}
	for _, m := range mods {
		m(&ws)
	}
	testutil.SeedWorkspace(t, db, ws)
}

func withGitURL(url string) func(*testutil.Workspace) {
	return func(w *testutil.Workspace) { w.GitURL = url }
}

func withOwner(owner string) func(*testutil.Workspace) {
	return func(w *testutil.Workspace) { w.OwnerID = owner }
}

func archived(w *testutil.Workspace) { w.Status = "archived" }

// carryPatch makes the workspace a carry_patch fork of upstream.
func carryPatch(upstream string) func(*testutil.Workspace) {
	return func(w *testutil.Workspace) { w.WorkspaceMode = "carry_patch"; w.UpstreamURL = upstream }
}

// ---------------------------------------------------------------------------
// The forge stub
// ---------------------------------------------------------------------------

// recordedRequest is a request the forge stub received; EscapedPath keeps a %2F.
type recordedRequest struct {
	Method, Path, EscapedPath, Query, Body string
	Header                                 http.Header
}

// forgeStub is a TLS server standing in for GitHub or GitLab. It records every request
// and answers with its handler, 200 {} by default.
type forgeStub struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	handler  http.HandlerFunc
}

func newForgeStub(t *testing.T) *forgeStub {
	t.Helper()
	s := &forgeStub{handler: respond(http.StatusOK, "{}")}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.mu.Lock()
		s.requests = append(s.requests, recordedRequest{r.Method, r.URL.Path, r.URL.EscapedPath(), r.URL.RawQuery, string(body), r.Header.Clone()})
		h := s.handler
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *forgeStub) setHandler(h http.HandlerFunc) { s.mu.Lock(); defer s.mu.Unlock(); s.handler = h }

func (s *forgeStub) received() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedRequest(nil), s.requests...)
}

func (s *forgeStub) count() int { return len(s.received()) }
func (s *forgeStub) forget()    { s.mu.Lock(); defer s.mu.Unlock(); s.requests = nil }

// respond answers every request with status and the JSON body.
func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

// answer answers every request with status and a GitHub error body carrying message,
// after setting the header pairs.
func answer(status int, message string, headers ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		respond(status, fmt.Sprintf(`{"message":%q}`, message))(w, r)
	}
}

// routes answers by "METHOD path" and answers anything else 404, as GitHub does.
func routes(handlers map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handlers[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		respond(http.StatusNotFound, `{"message":"Not Found"}`)(w, r)
	}
}

// bearer is the token a request carried: GitHub's bearer or GitLab's private token.
func bearer(r recordedRequest) string {
	if v := r.Header.Get("Authorization"); v != "" {
		return strings.TrimPrefix(v, "Bearer ")
	}
	return r.Header.Get("PRIVATE-TOKEN")
}

// countingTransport counts the requests that pass through it and fails them.
type countingTransport struct{ n atomic.Int64 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return nil, io.ErrUnexpectedEOF
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ---------------------------------------------------------------------------
// The test environment
// ---------------------------------------------------------------------------

// env is the forge routes and the ingress mounted on an Echo with the test auth
// middleware, over a database with the workspaces, secrets and delivery tables, a stub
// for the forge, and resolvers and a variable lookup that count their calls.
type env struct {
	t       *testing.T
	db      *sql.DB
	echo    *echo.Echo
	stub    *forgeStub
	secrets *secrets.Store
	cfg     Config

	originAuth, upstreamAuth transport.AuthMethod
	originErr, upstreamErr   error

	mu     sync.Mutex
	vars   map[string]string
	varErr error

	originCalls, upstreamCalls, varCalls atomic.Int64
}

// newEnv mounts the routes; the modifiers change the Config before it is used.
func newEnv(t *testing.T, mods ...func(*Config)) *env {
	t.Helper()
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "GITHUB_API_URL", "GITLAB_API_URL"} {
		t.Setenv(name, "")
	}
	e := &env{t: t, db: testutil.OpenDB(t), stub: newForgeStub(t), vars: map[string]string{}}
	for name, init := range map[string]func(*sql.DB) error{"workspace": workspace.InitSchema, "secrets": secrets.InitSchema, "forge": InitSchema} {
		if err := init(e.db); err != nil {
			t.Fatalf("%s.InitSchema: %v", name, err)
		}
	}
	e.secrets = secrets.NewStore(e.db)
	e.originAuth = &githttp.BasicAuth{Username: patUsername, Password: "origin-token"}
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
		GetVariable: func(_, owner, key string) (string, error) {
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
		Transport:   e.stub.Client().Transport,
		Secrets:     e.secrets,
		ExternalURL: "https://hub.example.com",
	}
	for _, m := range mods {
		m(&cfg)
	}
	var api *echo.Group
	e.echo, api = testutil.API()
	e.echo.HTTPErrorHandler = apikit.HTTPErrorHandler
	RegisterRoutes(api, cfg)
	MountWebhookIngress(e.echo, cfg)
	e.cfg = cfg
	return e
}

// widgetsEnv is newEnv with the workspace widgets, whose API is the forge stub.
func widgetsEnv(t *testing.T, mods ...func(*Config)) *env {
	t.Helper()
	e := newEnv(t, mods...)
	addWorkspace(t, e.db, "widgets")
	e.setVar("widgets", "FORGE_API_URL", e.stub.URL)
	return e
}

// gitlabEnv is newEnv with the workspace lab on gitlab.com/acme/widgets, whose API is the stub.
func gitlabEnv(t *testing.T, mods ...func(*Config)) *env {
	t.Helper()
	e := newEnv(t, mods...)
	addWorkspace(t, e.db, "lab", withGitURL("https://gitlab.com/acme/widgets"))
	e.setVar("lab", "FORGE_API_URL", e.stub.URL)
	return e
}

// memberEnv is widgetsEnv with widgets in an org that memberID is a member of.
func memberEnv(t *testing.T) *env {
	t.Helper()
	e := widgetsEnv(t)
	testutil.SeedUser(t, e.db, memberID)
	testutil.SeedOrg(t, e.db, "org-1", "acme", "")
	testutil.SeedOrgMember(t, e.db, "org-1", memberID)
	testutil.Exec(t, e.db, `UPDATE workspaces SET org_id = 'org-1' WHERE slug = 'widgets'`)
	return e
}

func (e *env) setVar(slug, key, value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.vars[slug+"/"+key] = value
}

// do serves one request on /api/v1<path> as auth (nil for none) with body and headers.
func (e *env) do(method, path string, auth *apikit.AuthInfo, body string, header http.Header) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, values := range header {
		req.Header[name] = values
	}
	if auth != nil {
		raw, _ := json.Marshal(auth)
		req.Header.Set(testutil.AuthHeader, string(raw))
	}
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	return rec
}

func (e *env) get(path string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	return e.do(http.MethodGet, path, auth, "", nil)
}

func (e *env) send(method, path string, auth *apikit.AuthInfo, body string) *httptest.ResponseRecorder {
	return e.do(method, path, auth, body, nil)
}

// route is one route under /workspaces/:slug: path holds %s where the slug goes, and
// body is a valid body for it.
type route struct{ name, method, path, body string }

func (r route) url(slug string) string { return strings.Replace(r.path, "%s", slug, 1) }

func (e *env) sendRoute(r route, slug string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	return e.send(r.method, r.url(slug), auth, r.body)
}

// readRoutes are the diagnostic and the nine read routes; writeRoutes the nine writes;
// otherRoutes the deliveries and the webhook routes.
var (
	readRoutes = []route{
		{"forge", http.MethodGet, "/workspaces/%s/forge", ""},
		{"repository", http.MethodGet, "/workspaces/%s/forge/repository", ""},
		{"issues", http.MethodGet, "/workspaces/%s/issues", ""},
		{"issue", http.MethodGet, "/workspaces/%s/issues/1", ""},
		{"comments", http.MethodGet, "/workspaces/%s/issues/1/comments", ""},
		{"pull", http.MethodGet, "/workspaces/%s/pulls/1", ""},
		{"state", http.MethodGet, "/workspaces/%s/pulls/1/state", ""},
		{"files", http.MethodGet, "/workspaces/%s/pulls/1/files", ""},
		{"checks", http.MethodGet, "/workspaces/%s/pulls/1/checks", ""},
		{"reviews", http.MethodGet, "/workspaces/%s/pulls/1/reviews", ""},
	}
	writeRoutes = []route{
		{"create issue", http.MethodPost, "/workspaces/%s/issues", `{"title":"t","body":"b"}`},
		{"update issue", http.MethodPatch, "/workspaces/%s/issues/42", `{"title":"t"}`},
		{"close issue", http.MethodPost, "/workspaces/%s/issues/42/close", `{}`},
		{"comment on issue", http.MethodPost, "/workspaces/%s/issues/42/comments", `{"body":"b"}`},
		{"add labels", http.MethodPost, "/workspaces/%s/issues/42/labels", `["af:ready"]`},
		{"remove label", http.MethodDelete, "/workspaces/%s/issues/42/labels/af:fix", ``},
		{"ensure label", http.MethodPost, "/workspaces/%s/labels", `{"name":"af:triage"}`},
		{"open pull request", http.MethodPost, "/workspaces/%s/pulls", `{"title":"t","head":"feature","base":"main"}`},
		{"comment on pull request", http.MethodPost, "/workspaces/%s/pulls/42/comments", `{"body":"b"}`},
	}
	otherRoutes = []route{
		{"deliveries", http.MethodGet, "/workspaces/%s/forge/deliveries", ""},
		{"set webhook", http.MethodPut, "/workspaces/%s/forge/webhook", `{}`},
		{"unset webhook", http.MethodDelete, "/workspaces/%s/forge/webhook", ``},
	}
)

// namedWrite finds a write route by name.
func namedWrite(name string) route {
	for _, r := range writeRoutes {
		if r.name == name {
			return r
		}
	}
	panic("no write route " + name)
}

// captureLog sends everything logged through slog to the returned buffer.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

// shortDeadline shortens the call deadline for the test.
func shortDeadline(t *testing.T, d time.Duration) {
	prev := callDeadline
	callDeadline = d
	t.Cleanup(func() { callDeadline = prev })
}

// noSleep makes the issuex client skip the wait before it retries a rate-limited call.
func noSleep(cfg *Config) {
	cfg.NewClient = func(o issuex.Options) (issuex.Client, error) {
		c, err := issuex.NewWithOptions(o)
		if err == nil {
			issuex.SetGitHubSleep(c, func(time.Duration) {})
		}
		return c, err
	}
}

// ---------------------------------------------------------------------------
// Fakes of the forge client
// ---------------------------------------------------------------------------

// useClient makes the routes use the client c and counts the clients built in *built.
func useClient(c issuex.Client, built *atomic.Int64) func(*Config) {
	return func(cfg *Config) {
		cfg.NewClient = func(issuex.Options) (issuex.Client, error) {
			if built != nil {
				built.Add(1)
			}
			return c, nil
		}
	}
}

// fakeWriter is a client whose writes are recorded and answered by behave (nil for
// success); a client from Config.NewClient that is also a writer is used as it is.
type fakeWriter struct {
	issuex.Client
	mu    sync.Mutex
	calls []string
	left  []time.Duration // what the context of each call had left
	// behave answers a call; n counts the calls of the method, from 1.
	behave func(ctx context.Context, method string, n int) error
}

func (f *fakeWriter) do(ctx context.Context, method string) error {
	f.mu.Lock()
	n := 1
	for _, c := range f.calls {
		if c == method {
			n++
		}
	}
	var left time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		left = time.Until(deadline)
	}
	f.calls, f.left = append(f.calls, method), append(f.left, left)
	behave := f.behave
	f.mu.Unlock()
	if behave == nil {
		return nil
	}
	return behave(ctx, method, n)
}

func (f *fakeWriter) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeWriter) CreateIssue(ctx context.Context, _ issuex.Repo, _ issuex.CreateIssueRequest) (issuex.Issue, error) {
	return issuex.Issue{Number: 7}, f.do(ctx, "CreateIssue")
}
func (f *fakeWriter) ChangeIssue(ctx context.Context, _ issuex.IssueRef, _ issueUpdate) (issuex.Issue, error) {
	return issuex.Issue{Number: 42}, f.do(ctx, "ChangeIssue")
}
func (f *fakeWriter) AddComment(ctx context.Context, _ issuex.IssueRef, _ string) (string, error) {
	return commentURL, f.do(ctx, "AddComment")
}
func (f *fakeWriter) AddLabels(ctx context.Context, _ issuex.IssueRef, _ []string) error {
	return f.do(ctx, "AddLabels")
}
func (f *fakeWriter) DropLabel(ctx context.Context, _ issuex.IssueRef, _ string) error {
	return f.do(ctx, "DropLabel")
}
func (f *fakeWriter) CheckIssue(ctx context.Context, _ issuex.IssueRef) error {
	return f.do(ctx, "CheckIssue")
}
func (f *fakeWriter) GetLabel(ctx context.Context, _ issuex.Repo, name string) (issuex.Label, error) {
	return issuex.Label{Name: name}, f.do(ctx, "GetLabel")
}
func (f *fakeWriter) MakeLabel(ctx context.Context, _ issuex.Repo, label issuex.Label) (issuex.Label, error) {
	return label, f.do(ctx, "MakeLabel")
}
func (f *fakeWriter) CreatePullRequest(ctx context.Context, _ issuex.Repo, _ issuex.CreatePullRequestRequest) (issuex.PullRequest, error) {
	return issuex.PullRequest{Number: 9}, f.do(ctx, "CreatePullRequest")
}

// notFound is the forge's not found as issuex returns it.
func notFound() error {
	return &issuex.HTTPError{Method: "GET", Path: "/x", Status: http.StatusNotFound, Message: "Not Found"}
}

// ---------------------------------------------------------------------------
// Audit and secrets fakes
// ---------------------------------------------------------------------------

// recordingEmitter keeps the events it is given and the error of each Emit's context.
type recordingEmitter struct {
	mu     sync.Mutex
	events []audit.HubEvent
	ctxErr []error
	err    error
}

func (r *recordingEmitter) Emit(ctx context.Context, event audit.HubEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events, r.ctxErr = append(r.events, event), append(r.ctxErr, ctx.Err())
	return r.err
}

func (r *recordingEmitter) recorded() []audit.HubEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.HubEvent(nil), r.events...)
}

func (r *recordingEmitter) contexts() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.ctxErr...)
}

func withEmitter(em audit.Emitter) func(*Config) { return func(cfg *Config) { cfg.Audit = em } }

// eventTypes lists the types of the events.
func eventTypes(events []audit.HubEvent) []string {
	out := []string{}
	for _, ev := range events {
		out = append(out, ev.EventType)
	}
	return out
}

// fakeSecrets is a secrets store over the real one that counts its calls, can fail them,
// and can play a concurrent request that creates the entry first.
type fakeSecrets struct {
	SecretStore
	mu                           sync.Mutex
	calls                        []string
	getErr, updateErr, deleteErr error
	createRace                   bool
}

func (f *fakeSecrets) note(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeSecrets) called() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, ",")
}

func (f *fakeSecrets) GetSecretValue(ownerType, ownerID, key string) (string, error) {
	f.note("Get")
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.SecretStore.GetSecretValue(ownerType, ownerID, key)
}

func (f *fakeSecrets) CreateSecrets(ownerType, ownerID string, entries []secrets.EntryInput) ([]secrets.SecretEntry, error) {
	f.note("Create")
	if f.createRace {
		if _, err := f.SecretStore.CreateSecrets(ownerType, ownerID, []secrets.EntryInput{{Key: entries[0].Key, Value: "raced"}}); err != nil {
			return nil, err
		}
		return nil, &secrets.ConflictError{Key: entries[0].Key}
	}
	return f.SecretStore.CreateSecrets(ownerType, ownerID, entries)
}

func (f *fakeSecrets) UpdateSecret(ownerType, ownerID, key, value string) (*secrets.SecretEntry, error) {
	f.note("Update")
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return f.SecretStore.UpdateSecret(ownerType, ownerID, key, value)
}

func (f *fakeSecrets) DeleteSecret(ownerType, ownerID, key string) error {
	f.note("Delete")
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.SecretStore.DeleteSecret(ownerType, ownerID, key)
}

// withFakeSecrets puts fake over the Config's store.
func withFakeSecrets(fake *fakeSecrets) func(*Config) {
	return func(cfg *Config) { fake.SecretStore = cfg.Secrets; cfg.Secrets = fake }
}

// storedSecret is the webhook secret of a workspace in the store.
func (e *env) storedSecret(slug string) (string, error) {
	return e.secrets.GetSecretValue("workspace", slug, webhookSecretKey)
}

// ---------------------------------------------------------------------------
// Deliveries
// ---------------------------------------------------------------------------

// signedDelivery is the headers and the body of a delivery.
type signedDelivery struct {
	header http.Header
	body   string
}

func signGitHub(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// githubDelivery is a delivery as GitHub sends it, signed under secret.
func githubDelivery(secret, id, event, body string) signedDelivery {
	h := http.Header{}
	h.Set("X-GitHub-Delivery", id)
	h.Set("X-GitHub-Event", event)
	h.Set("X-Hub-Signature-256", signGitHub(secret, body))
	return signedDelivery{h, body}
}

// gitlabDelivery is a delivery as GitLab sends it, with the token secret.
func gitlabDelivery(secret, id, event, body string) signedDelivery {
	h := http.Header{}
	h.Set("Idempotency-Key", id)
	h.Set("X-Gitlab-Event", event)
	h.Set("X-Gitlab-Token", secret)
	return signedDelivery{h, body}
}

func (d signedDelivery) without(name string) signedDelivery {
	h := d.header.Clone()
	h.Del(name)
	return signedDelivery{h, d.body}
}

func (d signedDelivery) with(name, value string) signedDelivery {
	h := d.header.Clone()
	h.Set(name, value)
	return signedDelivery{h, d.body}
}

// labeledDelivery is a signed GitHub issues labeled delivery of acme/widgets.
func labeledDelivery(id string) signedDelivery {
	return githubDelivery(ingressSecret, id, "issues", issuesPayload("labeled", "acme/widgets"))
}

// deliver posts the delivery to the ingress of the workspace.
func (e *env) deliver(slug string, d signedDelivery) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/forge/"+slug, strings.NewReader(d.body))
	req.Header = d.header.Clone()
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	return rec
}

// outcomeOf reads the outcome of a 202 answer; anything else stops the test.
func outcomeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; want 202\n%s", rec.Code, rec.Body)
	}
	var doc map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || len(doc) != 1 {
		t.Fatalf("the body is not {\"outcome\": ...}: %v\n%s", err, rec.Body)
	}
	return doc["outcome"]
}

// deliveryRecord is a row of forge_deliveries.
type deliveryRecord struct {
	slug, id, forge, event, action string
	target                         *string
	outcome, relayed, receivedAt   string
}

// deliveryRows returns the rows of the delivery log in the order they were recorded.
func (e *env) deliveryRows() []deliveryRecord {
	e.t.Helper()
	rows, err := e.db.Query(`SELECT workspace_slug, delivery_id, forge, event, action, target, outcome, relayed, received_at
		FROM forge_deliveries ORDER BY rowid`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []deliveryRecord
	for rows.Next() {
		var r deliveryRecord
		if err := rows.Scan(&r.slug, &r.id, &r.forge, &r.event, &r.action, &r.target, &r.outcome, &r.relayed, &r.receivedAt); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// addDelivery inserts a row; what is left empty is a relayed GitHub issues delivery.
func addDelivery(t *testing.T, db *sql.DB, r deliveryRecord) {
	t.Helper()
	if r.forge == "" {
		r.forge = "github"
	}
	if r.event == "" {
		r.event = "issues"
	}
	if r.outcome == "" {
		r.outcome = "relayed"
	}
	if r.relayed == "" {
		r.relayed = "[]"
	}
	testutil.Exec(t, db, `INSERT INTO forge_deliveries (workspace_slug, delivery_id, forge, event, action, target, outcome, relayed, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.slug, r.id, r.forge, r.event, r.action, r.target, r.outcome, r.relayed, r.receivedAt)
}

// at is the time minutes after 2026-10-21T09:00:00Z.
func at(minutes int) string {
	return apikit.FormatUTC(time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute))
}

func ago(d time.Duration) string { return apikit.FormatUTC(time.Now().Add(-d)) }

func ptr(s string) *string { return &s }

// webhookWorkspace adds a workspace with the webhook secret ingressSecret.
func (e *env) webhookWorkspace(slug string, mods ...func(*testutil.Workspace)) {
	e.t.Helper()
	addWorkspace(e.t, e.db, slug, mods...)
	if _, err := e.secrets.CreateSecrets("workspace", slug, []secrets.EntryInput{{Key: webhookSecretKey, Value: ingressSecret}}); err != nil {
		e.t.Fatal(err)
	}
}

// ingressEnv is the routes and the ingress with a recording emitter and the workspace
// widgets (origin acme/widgets), which has a webhook secret.
func ingressEnv(t *testing.T, mods ...func(*Config)) (*env, *recordingEmitter) {
	t.Helper()
	em := &recordingEmitter{}
	e := newEnv(t, append([]func(*Config){withEmitter(em)}, mods...)...)
	e.webhookWorkspace("widgets")
	return e, em
}

// ---------------------------------------------------------------------------
// Payloads of deliveries
// ---------------------------------------------------------------------------

func marshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// githubPayload is a GitHub delivery of acme/widgets from octocat with the fields added.
func githubPayload(action string, fields map[string]any) string {
	doc := map[string]any{"repository": map[string]any{"full_name": "acme/widgets"}, "sender": map[string]any{"login": "octocat"}}
	if action != "" {
		doc["action"] = action
	}
	for k, v := range fields {
		doc[k] = v
	}
	return marshal(doc)
}

// issuesPayload is a GitHub issues delivery of repo: issue 7 with two labels.
func issuesPayload(action, repo string) string {
	doc := map[string]any{
		"action": action, "repository": map[string]any{"full_name": repo}, "sender": map[string]any{"login": "octocat"},
		"issue": map[string]any{"number": 7, "title": "Widgets break", "html_url": "https://github.com/" + repo + "/issues/7",
			"labels": []any{map[string]any{"name": "bug"}, map[string]any{"name": "af:ready"}}},
	}
	if action == "labeled" || action == "unlabeled" {
		doc["label"] = map[string]any{"name": "af:ready"}
	}
	return marshal(doc)
}

// pullPayload is a pull_request delivery of pull request 12 from feature to main.
func pullPayload(action string, merged bool) string {
	return githubPayload(action, map[string]any{"pull_request": map[string]any{
		"number": 12, "html_url": "https://github.com/acme/widgets/pull/12", "merged": merged,
		"head": map[string]any{"ref": "feature"}, "base": map[string]any{"ref": "main"}}})
}

// gitlabPayload is a GitLab delivery of the project acme/widgets from alice with the fields added.
func gitlabPayload(fields map[string]any) string {
	doc := map[string]any{"user": map[string]any{"username": "alice"}, "project": map[string]any{"path_with_namespace": "acme/widgets"}}
	for k, v := range fields {
		doc[k] = v
	}
	return marshal(doc)
}

// gitlabIssuePayload is a GitLab Issue Hook of the project at path: issue 7 with one label.
func gitlabIssuePayload(action, path string) string {
	return marshal(map[string]any{
		"user": map[string]any{"username": "alice"}, "project": map[string]any{"path_with_namespace": path},
		"object_attributes": map[string]any{"iid": 7, "title": "Widgets break", "action": action, "url": "https://gitlab.example.com/" + path + "/-/issues/7"},
		"labels":            []any{map[string]any{"title": "bug"}},
	})
}

// titles is the list of labels GitLab sends, each an object with a title.
func titles(names ...string) []any {
	out := []any{}
	for _, name := range names {
		out = append(out, map[string]any{"title": name})
	}
	return out
}

// gitlabUpdateHook is an Issue Hook update of issue 7 with the changes and the labels it has now.
func gitlabUpdateHook(changes map[string]any, labels ...string) string {
	return gitlabPayload(map[string]any{
		"object_attributes": map[string]any{"iid": 7, "title": "Widgets break", "action": "update", "url": "https://gitlab.example.com/acme/widgets/-/issues/7"},
		"changes":           changes, "labels": titles(labels...),
	})
}

// labelChange is the changes.labels object of an update from previous to current.
func labelChange(previous, current []string) map[string]any {
	return map[string]any{"previous": titles(previous...), "current": titles(current...)}
}

// noteHook is a Note Hook of the noteable type with the fields added.
func noteHook(noteable, action string, fields map[string]any) string {
	attrs := map[string]any{"noteable_type": noteable, "url": "https://gitlab.example.com/acme/widgets/-/issues/7#note_1"}
	if action != "" {
		attrs["action"] = action
	}
	doc := map[string]any{"object_attributes": attrs}
	for k, v := range fields {
		doc[k] = v
	}
	return gitlabPayload(doc)
}

// mergeRequestHook is a Merge Request Hook of merge request 9 with the action and extra attributes.
func mergeRequestHook(action string, extra map[string]any) string {
	attrs := map[string]any{"iid": 9, "action": action, "url": "https://gitlab.example.com/acme/widgets/-/merge_requests/9",
		"source_branch": "feature", "target_branch": "main", "state": "opened"}
	for k, v := range extra {
		attrs[k] = v
	}
	return gitlabPayload(map[string]any{"object_attributes": attrs})
}

// ---------------------------------------------------------------------------
// Answers of the forge to writes
// ---------------------------------------------------------------------------

const (
	commentURL      = "https://github.com/acme/widgets/issues/42#issuecomment-9"
	commentBody     = `{"id":9,"body":"done","html_url":"` + commentURL + `"}`
	labelsAnswer    = `[{"name":"af:ready"}]`
	repositoryBody  = `{"full_name":"acme/widgets","default_branch":"main","private":false,"archived":false,"permissions":{"push":true,"pull":true,"admin":false}}`
	gitlabLabelPath = "/api/v4/projects/acme/widgets/labels"
)

// issueBody is a GitHub issue of acme/widgets as the API answers one.
func issueBody(n int, title, body string, labels ...string) string {
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = fmt.Sprintf(`{"name":%q}`, l)
	}
	return fmt.Sprintf(`{"number":%d,"title":%q,"body":%q,"state":"open","html_url":"https://github.com/acme/widgets/issues/%d",`+
		`"user":{"login":"hub"},"labels":[%s],"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`,
		n, title, body, n, strings.Join(names, ","))
}

func closedIssue(n int) string {
	return strings.Replace(issueBody(n, "t", ""), `"state":"open"`, `"state":"closed"`, 1)
}

// gitlabIssueBody is a GitLab issue of acme/widgets as the API answers one.
func gitlabIssueBody(iid int, title, description, state string, labels ...string) string {
	return fmt.Sprintf(`{"id":%d,"iid":%d,"title":%q,"description":%q,"state":%q,"web_url":"https://gitlab.com/acme/widgets/-/issues/%d",`+
		`"author":{"username":"hub"},"labels":%s,"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`,
		1000+iid, iid, title, description, state, iid, marshal(append([]string{}, labels...)))
}

// pullBody is a GitHub pull request as the API answers one.
func pullBody(n int, repo, head, base string, draft bool) string {
	return fmt.Sprintf(`{"number":%d,"title":"t","body":"","state":"open","html_url":"https://github.com/%s/pull/%d","draft":%v,"merged":false,`+
		`"user":{"login":"hub"},"head":{"ref":%q,"sha":"abc123"},"base":{"ref":%q,"sha":"def456"},`+
		`"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`, n, repo, n, draft, head, base)
}

// githubWrites answers the write requests of acme/widgets on GitHub with success.
func githubWrites() http.HandlerFunc {
	return routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues":                    respond(http.StatusCreated, issueBody(7, "t", "b")),
		"PATCH /repos/acme/widgets/issues/42":                respond(http.StatusOK, issueBody(42, "t", "b")),
		"POST /repos/acme/widgets/issues/42/comments":        respond(http.StatusCreated, commentBody),
		"POST /repos/acme/widgets/issues/42/labels":          respond(http.StatusOK, labelsAnswer),
		"DELETE /repos/acme/widgets/issues/42/labels/af:fix": respond(http.StatusOK, labelsAnswer),
		"GET /repos/acme/widgets/labels/af:triage":           respond(http.StatusNotFound, `{"message":"Not Found"}`),
		"POST /repos/acme/widgets/labels":                    respond(http.StatusCreated, `{"name":"af:triage","color":"ededed"}`),
		"POST /repos/acme/widgets/pulls":                     respond(http.StatusCreated, pullBody(9, "acme/widgets", "feature", "main", false)),
	})
}

// labelStub is the labels of a repository: it holds the labels created through it, answers
// a lookup by name and counts the lookups and the creates. prefix is the labels path.
type labelStub struct {
	mu               sync.Mutex
	prefix           string
	labels           map[string]string
	lookups, creates int
	refuseCreate     int // the status a create answers instead of creating, 0 for none
}

func newLabelStub(prefix string, existing ...issuex.Label) *labelStub {
	s := &labelStub{prefix: prefix, labels: map[string]string{}}
	for _, l := range existing {
		s.labels[l.Name] = marshal(l)
	}
	return s
}

func (s *labelStub) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, s.prefix+"/"):
		s.lookups++
		if body, ok := s.labels[strings.TrimPrefix(r.URL.Path, s.prefix+"/")]; ok {
			respond(http.StatusOK, body)(w, r)
			return
		}
		respond(http.StatusNotFound, `{"message":"Not Found"}`)(w, r)
	case r.Method == http.MethodPost && r.URL.Path == s.prefix:
		s.creates++
		if s.refuseCreate != 0 {
			respond(s.refuseCreate, `{"message":"Validation Failed"}`)(w, r)
			return
		}
		var l issuex.Label
		_ = json.NewDecoder(r.Body).Decode(&l)
		s.labels[l.Name] = marshal(l)
		respond(http.StatusCreated, s.labels[l.Name])(w, r)
	default:
		respond(http.StatusNotFound, `{"message":"Not Found"}`)(w, r)
	}
}

// jsonOf decodes a JSON object.
func jsonOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, s)
	}
	return m
}

// lastBody is the body of the last request the stub received, decoded.
func lastBody(t *testing.T, s *forgeStub) map[string]any {
	t.Helper()
	got := s.received()
	if len(got) == 0 {
		t.Fatal("the forge received no request")
	}
	return jsonOf(t, got[len(got)-1].Body)
}

// eventually polls cond for up to five seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// githubFixture answers the requests of the nine read routes with minimal GitHub JSON for
// acme/widgets: two issues and a pull request in the list, one issue with two comments,
// one pull request whose head is abc123, one changed file, one check run and one approval.
func githubFixture(w http.ResponseWriter, r *http.Request) {
	const (
		user  = `{"login":"alice"}`
		when  = `"2026-01-02T03:04:05Z"`
		issue = `{"number":1,"title":"Widgets break","body":"it breaks","state":"open","html_url":"https://github.com/acme/widgets/issues/1","user":` + user +
			`,"labels":[{"name":"af:triage"}],"created_at":` + when + `,"updated_at":` + when + `}`
		other = `{"number":2,"title":"Second","body":"","state":"open","html_url":"https://github.com/acme/widgets/issues/2","user":` + user + `,"labels":[],"created_at":` + when + `,"updated_at":` + when + `}`
		asPR  = `{"number":3,"title":"A pull request","state":"open","user":` + user + `,"pull_request":{"url":"x"},"created_at":` + when + `,"updated_at":` + when + `}`
	)
	body := map[string]string{
		"/repos/acme/widgets":                           repositoryBody,
		"/repos/acme/widgets/issues":                    `[` + issue + `,` + other + `,` + asPR + `]`,
		"/repos/acme/widgets/issues/1":                  issue,
		"/repos/acme/widgets/issues/1/comments":         `[{"id":10,"body":"first","user":` + user + `,"created_at":` + when + `,"updated_at":` + when + `},{"id":11,"body":"second","user":` + user + `,"created_at":` + when + `,"updated_at":` + when + `}]`,
		"/repos/acme/widgets/pulls/1":                   pullBody(1, "acme/widgets", "feature", "main", false),
		"/repos/acme/widgets/pulls/1/files":             `[{"filename":"a.go","status":"modified","additions":3,"deletions":1}]`,
		"/repos/acme/widgets/commits/abc123/check-runs": `{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"success","html_url":"https://ci.example/1","output":{"title":"ok","summary":"all green"}}]}`,
		"/repos/acme/widgets/pulls/1/reviews":           `[{"user":` + user + `,"state":"APPROVED","body":"lgtm","submitted_at":` + when + `}]`,
	}[r.URL.Path]
	if body == "" {
		http.NotFound(w, r)
		return
	}
	respond(http.StatusOK, body)(w, r)
}
