package forge

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/cli"
	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// These smoke tests run the hub as main.go wires the forge: the real forge routes behind a
// stand-in for apikit's authentication, the real workspaces and secrets tables with the
// resolvers of internal/workspace, the real audit store and emitter (DuckDB), the issuex
// client, and, where the path starts at a person, the real afc command tree against the
// hub's HTTP server. Only the forge is a stand-in: a TLS test server.

const (
	smokeKey      = "smoke-api-key-31"
	smokeAdminKey = "smoke-admin-key-31"
)

// hubReply is an answer the hub gave.
type hubReply struct {
	Method string
	Path   string
	Status int
	Body   string
}

// writeSmoke is a hub, its audit store and a forge stub.
type writeSmoke struct {
	t     *testing.T
	db    *sql.DB // the hub's database
	store *secrets.Store
	audit *sql.DB // the audit store's DuckDB database
	hub   *httptest.Server
	forge *forgeStub // the forge

	mu      sync.Mutex
	replies []hubReply
}

func newWriteSmoke(t *testing.T) *writeSmoke {
	t.Helper()
	isolateForgeEnv(t)
	db, store := smokeDB(t)
	s := &writeSmoke{t: t, db: db, store: store, forge: newForgeStub(t)}

	auditDB, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { auditDB.Close() })
	if err := audit.InitSchema(auditDB); err != nil {
		t.Fatal(err)
	}
	s.audit = auditDB
	auditStore := audit.NewStore(auditDB)
	sse := audit.NewSSEManager(5)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go sse.Run(done)

	router := echo.New()
	router.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := router.Group("/api/v1")
	api.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			switch c.Request().Header.Get("Authorization") {
			case "Bearer " + smokeKey:
				apikit.SetAuthInfo(c, &apikit.AuthInfo{UserID: "user-1", CredentialType: "api_key"})
			case "Bearer " + smokeAdminKey:
				apikit.SetAuthInfo(c, &apikit.AuthInfo{UserID: "admin-1", CredentialType: "admin_token"})
			}
			return next(c)
		}
	})
	RegisterRoutes(api, Config{
		DB:                db,
		ResolveOriginAuth: func(slug string) (transport.AuthMethod, error) { return workspace.ResolveCloneAuth(store, slug) },
		ResolveUpstreamAuth: func(slug string) (transport.AuthMethod, error) {
			return workspace.ResolveUpstreamAuthMethod(store, slug)
		},
		GetVariable: store.GetVariableValue,
		Transport:   s.forge.Client().Transport,
		Audit:       audit.NewEmitterWithBroadcast(auditStore, sse),
	})
	audit.RegisterAuditQueryRoutes(api, auditStore, sse, db)

	s.hub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		s.mu.Lock()
		s.replies = append(s.replies, hubReply{r.Method, r.URL.Path, rec.Code, rec.Body.String()})
		s.mu.Unlock()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(s.hub.Close)
	return s
}

// workspace adds a workspace of user-1, active and cloned, with a git credential and the
// API URL variable that points at stub.
func (s *writeSmoke) workspace(slug, gitURL, mode string, upstream *string, pat string, stub *forgeStub) {
	s.t.Helper()
	_, err := s.db.Exec(`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, upstream_url, created_at, updated_at)
		VALUES (?, ?, 'user-1', 'active', 'ready', ?, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, slug, gitURL, mode, upstream)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.store.CreateSecrets("workspace", slug, []secrets.EntryInput{{Key: "GIT_PAT", Value: pat}}); err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.store.CreateVariables("workspace", slug, []secrets.EntryInput{{Key: "FORGE_API_URL", Value: stub.URL}}); err != nil {
		s.t.Fatal(err)
	}
}

// afc runs the real afc command tree against the hub and returns stdout and the error.
func (s *writeSmoke) afc(args ...string) (string, error) {
	s.t.Helper()
	home := s.t.TempDir()
	s.t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ak"), 0o700); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ak", "config.toml"), []byte("endpoint_url = \"\"\nuser_id = \"\"\napi_key = \"\"\n"), 0o600); err != nil {
		s.t.Fatal(err)
	}
	cmd := cli.BuildRootCommand()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"--endpoint-url", s.hub.URL, "--api-key", smokeKey}, args...))
	err := cmd.Execute()
	return out.String(), err
}

// repliesTo returns what the hub answered to requests with method and a path ending in suffix.
func (s *writeSmoke) repliesTo(method, suffix string) []hubReply {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []hubReply
	for _, r := range s.replies {
		if r.Method == method && strings.HasSuffix(r.Path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

// auditEvent is a row of hub_audit_events.
type auditEvent struct {
	EventType, ActorID, ActorType, ResourceType, ResourceID, Action, Workspace string
	Metadata                                                                   map[string]any
}

// events reads the hub events of a workspace from the audit store, oldest first.
func (s *writeSmoke) events(slug string) []auditEvent {
	s.t.Helper()
	rows, err := s.audit.Query(`SELECT event_type, actor_id, actor_type, resource_type, resource_id, action, workspace, metadata
		FROM hub_audit_events WHERE workspace = ? ORDER BY timestamp, id`, slug)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditEvent
	for rows.Next() {
		var ev auditEvent
		var metadata string
		if err := rows.Scan(&ev.EventType, &ev.ActorID, &ev.ActorType, &ev.ResourceType, &ev.ResourceID, &ev.Action, &ev.Workspace, &metadata); err != nil {
			s.t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(metadata), &ev.Metadata); err != nil {
			s.t.Fatalf("metadata %q: %v", metadata, err)
		}
		out = append(out, ev)
	}
	return out
}

// TS-31-62: An API-key caller creates an issue; the forge sees the workspace's GIT_PAT and
// nothing of the caller; one event is recorded and found through the audit query.
// Requirement: 31-PATH-1, 31-REQ-9.1, 31-REQ-9.3
func TestSmoke_APIKeyCallerCreatesAnIssueAndFindsItsEvent_TS_31_62(t *testing.T) {
	s := newWriteSmoke(t)
	s.workspace("widgets", "https://github.com/acme/widgets", "standard", nil, "smoke-gitpat-31", s.forge)
	s.forge.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues": respond(http.StatusCreated, issueBody(7, "t", "b", "af:triage")),
	}))

	req, err := http.NewRequest(http.MethodPost, s.hub.URL+"/api/v1/workspaces/widgets/issues", strings.NewReader(`{"title":"t","body":"b","labels":["af:triage"]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+smokeKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var issue struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || issue.Number != 7 {
		t.Fatalf("answer = %d %+v; want 201 and the forge's issue 7", resp.StatusCode, issue)
	}

	got := s.forge.received()
	if len(got) != 1 || bearer(got[0]) != "smoke-gitpat-31" {
		t.Fatalf("the forge received %+v; want one request with the workspace's GIT_PAT", got)
	}
	if dump := fmt.Sprint(got[0].Header, got[0].Query, got[0].Body); strings.Contains(dump, smokeKey) {
		t.Errorf("the forge saw the caller's key: %s", dump)
	}

	// The audit query finds exactly one event of the write.
	query, _ := http.NewRequest(http.MethodGet, s.hub.URL+"/api/v1/audit?workspace=widgets", nil)
	query.Header.Set("Authorization", "Bearer "+smokeKey)
	qr, err := http.DefaultClient.Do(query)
	if err != nil || qr.StatusCode != http.StatusOK {
		t.Fatalf("GET /audit: %v %v", qr, err)
	}
	defer qr.Body.Close()
	var result struct {
		Events []struct {
			EventType string `json:"event_type"`
			Workspace string `json:"workspace"`
		} `json:"events"`
	}
	if err := json.NewDecoder(qr.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].EventType != audit.EventForgeIssueCreate || result.Events[0].Workspace != "widgets" {
		t.Fatalf("the audit query returned %+v; want exactly one hub.forge.issue.create in widgets", result.Events)
	}
	ev := s.events("widgets")[0]
	if ev.ActorID != "user-1" || ev.ActorType != "api_key" || ev.ResourceType != "issue" || ev.ResourceID != "7" || ev.Action != "create" {
		t.Errorf("event = %+v; want the create of issue 7 by user-1 (api_key)", ev)
	}
	if ev.Metadata["target"] != "origin" || ev.Metadata["ok"] != true || ev.Metadata["number"] != float64(7) ||
		ev.Metadata["url"] != "https://github.com/acme/widgets/issues/7" || len(ev.Metadata) != 4 {
		t.Errorf("metadata = %v; want target origin, ok true, number 7 and the issue url", ev.Metadata)
	}
}

// TS-31-63: A user closes a GitLab issue with a comment through afc: the comment on iid 42,
// then the close, one close event with the comment's URL, and the closed issue printed.
// Requirement: 31-PATH-2, 31-REQ-3.2, 31-REQ-10.1
func TestSmoke_AfcClosesAGitLabIssueWithAComment_TS_31_63(t *testing.T) {
	s := newWriteSmoke(t)
	s.workspace("widgets", "https://gitlab.com/acme/widgets", "standard", nil, "smoke-gitpat-31", s.forge)
	s.forge.setHandler(routes(map[string]http.HandlerFunc{
		"POST /api/v4/projects/acme/widgets/issues/42/notes": respond(http.StatusCreated, `{"id":5,"body":"done","web_url":"https://gitlab.com/acme/widgets/-/issues/42#note_5"}`),
		"PUT /api/v4/projects/acme/widgets/issues/42":        respond(http.StatusOK, gitlabIssueBody(42, "t", "", "closed")),
	}))

	stdout, err := s.afc("issue", "close", "widgets", "42", "--comment", "done")
	if err != nil || cli.ExitCode(err) != 0 {
		t.Fatalf("afc issue close: %v (exit %d)\n%s", err, cli.ExitCode(err), stdout)
	}
	got := s.forge.received()
	if len(got) != 2 || got[0].Method != http.MethodPost || !strings.HasSuffix(got[0].Path, "/issues/42/notes") ||
		got[1].Method != http.MethodPut || got[1].Path != "/api/v4/projects/acme/widgets/issues/42" {
		t.Fatalf("GitLab received %+v; want the note on iid 42, then the close", got)
	}
	for _, r := range got {
		if r.Header.Get("PRIVATE-TOKEN") != "smoke-gitpat-31" {
			t.Errorf("%s %s carried the token %q; want the workspace's", r.Method, r.Path, r.Header.Get("PRIVATE-TOKEN"))
		}
	}
	if jsonOf(t, got[1].Body)["state_event"] != "close" {
		t.Errorf("the close carried %s", got[1].Body)
	}

	events := s.events("widgets")
	if len(events) != 1 || events[0].EventType != audit.EventForgeIssueClose || events[0].ResourceID != "42" {
		t.Fatalf("events = %+v; want one hub.forge.issue.close for 42", events)
	}
	if events[0].Metadata["ok"] != true || events[0].Metadata["comment_url"] != "https://gitlab.com/acme/widgets/-/issues/42#note_5" {
		t.Errorf("metadata = %v; want ok true and the note's url as comment_url", events[0].Metadata)
	}
	doc, ok := decodeJSONDocument(t, stdout).(map[string]any)
	if !ok || doc["number"] != float64(42) || doc["state"] != "closed" {
		t.Errorf("stdout = %s; want the closed issue 42 as one document", stdout)
	}
}

// decodeJSONDocument decodes stdout, which must hold exactly one JSON document.
func decodeJSONDocument(t *testing.T, stdout string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var doc any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%q", err, stdout)
	}
	if dec.More() {
		t.Errorf("stdout holds more than one JSON document: %q", stdout)
	}
	return doc
}

// TS-31-64: A user ensures a label exists twice through afc: 201, then 200, one create at
// the forge with the default color, and two events, created true and then false.
// Requirement: 31-PATH-3, 31-REQ-5.5, 31-REQ-9.4
func TestSmoke_AfcEnsuresALabelTwice_TS_31_64(t *testing.T) {
	s := newWriteSmoke(t)
	s.workspace("widgets", "https://github.com/acme/widgets", "standard", nil, "smoke-gitpat-31", s.forge)
	stub := newLabelStub()
	s.forge.setHandler(stub.handler)

	for i := 0; i < 2; i++ {
		stdout, err := s.afc("label", "create", "widgets", "af:triage")
		if err != nil || cli.ExitCode(err) != 0 {
			t.Fatalf("afc label create (run %d): %v\n%s", i+1, err, stdout)
		}
		if doc, ok := decodeJSONDocument(t, stdout).(map[string]any); !ok || doc["name"] != "af:triage" || doc["color"] != "ededed" {
			t.Errorf("run %d: stdout = %s; want the label af:triage with the color ededed", i+1, stdout)
		}
	}
	replies := s.repliesTo(http.MethodPost, "/workspaces/widgets/labels")
	if len(replies) != 2 || replies[0].Status != http.StatusCreated || replies[1].Status != http.StatusOK {
		t.Errorf("the hub answered %+v; want 201, then 200", replies)
	}
	if stub.creates != 1 {
		t.Errorf("the forge saw %d creates; want 1", stub.creates)
	}
	var created *recordedRequest
	for _, r := range s.forge.received() {
		if r.Method == http.MethodPost {
			r := r
			created = &r
		}
	}
	if created == nil || jsonOf(t, created.Body)["color"] != "ededed" {
		t.Errorf("the create carried %+v; want the color ededed", created)
	}
	events := s.events("widgets")
	if len(events) != 2 || events[0].EventType != audit.EventForgeLabelCreate || events[1].EventType != audit.EventForgeLabelCreate ||
		events[0].Metadata["created"] != true || events[1].Metadata["created"] != false || events[0].ResourceID != "af:triage" {
		t.Errorf("events = %+v; want two hub.forge.label.create events, created true then false", events)
	}
}

// TS-31-65: A user opens a pull request on the upstream of a carry-patch fork through afc:
// the upstream sees the create with head verbatim and its own token, the origin nothing.
// Requirement: 31-PATH-4, 31-REQ-6.2
func TestSmoke_AfcOpensAnUpstreamPullRequest_TS_31_65(t *testing.T) {
	s := newWriteSmoke(t)
	upstreamForge := newForgeStub(t)
	upstreamForge.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/up/widgets/pulls": respond(http.StatusCreated, pullBody(3, "up/widgets", "feature", "main", false)),
	}))
	upstream := "https://github.com/up/widgets"
	s.workspace("fork", "https://github.com/me/widgets", "carry_patch", &upstream, "origin-token-31", s.forge)
	if _, err := s.store.CreateSecrets("workspace", "fork", []secrets.EntryInput{{Key: "UPSTREAM_GIT_PAT", Value: "upstream-token-31"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.CreateVariables("workspace", "fork", []secrets.EntryInput{{Key: "UPSTREAM_FORGE_API_URL", Value: upstreamForge.URL}}); err != nil {
		t.Fatal(err)
	}

	stdout, err := s.afc("pr", "create", "fork", "--title", "t", "--head", "me:feature", "--base", "main", "--target", "upstream")
	if err != nil || cli.ExitCode(err) != 0 {
		t.Fatalf("afc pr create: %v (exit %d)\n%s", err, cli.ExitCode(err), stdout)
	}
	got := upstreamForge.received()
	if len(got) != 1 || got[0].Method != http.MethodPost || got[0].Path != "/repos/up/widgets/pulls" || bearer(got[0]) != "upstream-token-31" {
		t.Fatalf("the upstream received %+v; want the create with the upstream token", got)
	}
	if sent := jsonOf(t, got[0].Body); sent["head"] != "me:feature" || sent["base"] != "main" {
		t.Errorf("the upstream received %s; want head me:feature verbatim", got[0].Body)
	}
	if n := s.forge.count(); n != 0 {
		t.Errorf("the origin received %d requests; want none", n)
	}
	events := s.events("fork")
	if len(events) != 1 || events[0].EventType != audit.EventForgePullRequestCreate || events[0].Metadata["target"] != "upstream" ||
		events[0].Metadata["head"] != "me:feature" || events[0].Metadata["base"] != "main" || events[0].Metadata["draft"] != false {
		t.Errorf("events = %+v; want one pull_request.create with target upstream, head, base and draft false", events)
	}
	if doc, ok := decodeJSONDocument(t, stdout).(map[string]any); !ok || doc["number"] != float64(3) {
		t.Errorf("stdout = %s; want the pull request 3 as one document", stdout)
	}
}

// TS-31-66: A forge failure on a label removal is mapped, recorded and reported by afc: the
// forge says not found to the removal and to issue 99, the hub answers 404 forge_not_found,
// one event records the failure and afc exits non-zero.
// Requirement: 31-PATH-5, 31-REQ-4.5, 31-REQ-10.6
func TestSmoke_AfcUnlabelOfAMissingIssue_TS_31_66(t *testing.T) {
	s := newWriteSmoke(t)
	const token = "smoke-gitpat-31-notfound"
	s.workspace("widgets", "https://github.com/acme/widgets", "standard", nil, token, s.forge)
	s.forge.setHandler(respond(http.StatusNotFound, `{"message":"Not Found for token `+token+`"}`))

	stdout, err := s.afc("issue", "unlabel", "widgets", "99", "af:fix")
	if err == nil || cli.ExitCode(err) == 0 {
		t.Fatalf("afc issue unlabel succeeded on a missing issue (exit %d)\n%s", cli.ExitCode(err), stdout)
	}
	replies := s.repliesTo(http.MethodDelete, "/issues/99/labels/af:fix")
	if len(replies) != 1 || replies[0].Status != http.StatusNotFound {
		t.Fatalf("the hub answered %+v; want 404", replies)
	}
	var envelope struct {
		Error struct {
			Message   string `json:"message"`
			ErrorType string `json:"error_type"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(replies[0].Body), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.ErrorType != "forge_not_found" || strings.Contains(replies[0].Body, token) || !strings.Contains(envelope.Error.Message, "[redacted]") {
		t.Errorf("the hub's error = %+v; want forge_not_found with the token scrubbed", envelope.Error)
	}
	if strings.Contains(stdout, token) || !strings.Contains(stdout, "forge answered 404") {
		t.Errorf("stdout = %q; want afc's error envelope with the hub's message, token scrubbed", stdout)
	}
	if got := s.forge.received(); len(got) != 2 || got[0].Method != http.MethodDelete || got[1].Method != http.MethodGet || got[1].Path != "/repos/acme/widgets/issues/99" {
		t.Errorf("the forge received %+v; want the removal, then one read of issue 99", got)
	}
	events := s.events("widgets")
	if len(events) != 1 || events[0].EventType != audit.EventForgeLabelRemove || events[0].Metadata["ok"] != false ||
		events[0].Metadata["error_type"] != "forge_not_found" || events[0].Metadata["label"] != "af:fix" {
		t.Errorf("events = %+v; want one failed hub.forge.label.remove with error_type forge_not_found", events)
	}
	if strings.Contains(fmt.Sprint(events), token) {
		t.Errorf("an event holds the token: %+v", events)
	}
}

// On GitLab the assignees of a create and of an update are accepted and checked, and not
// sent: issuex sends none, and GitLab takes user ids, not names. The erratum for spec 31
// says so; this pins it, so that a change in issuex shows.
// Requirement: 31-REQ-2.1, 31-REQ-2.3
func TestGitLabAssigneesAreAcceptedAndNotSent(t *testing.T) {
	e := newGitLabWriteEnv(t)
	e.stub.setHandler(writesAnyWhere)

	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, "/workspaces/lab/issues", `{"title":"t","assignees":["octocat"]}`},
		{http.MethodPatch, "/workspaces/lab/issues/42", `{"title":"t","assignees":["octocat"]}`},
	} {
		if rec := e.send(call.method, call.path, apiKeyAuth(), call.body); rec.Code >= 300 {
			t.Fatalf("%s %s: status = %d\n%s", call.method, call.path, rec.Code, rec.Body)
		}
	}
	for _, r := range e.stub.received() {
		if strings.Contains(r.Body, "assignee") || strings.Contains(r.Body, "octocat") {
			t.Errorf("%s %s sent the assignees: %s", r.Method, r.Path, r.Body)
		}
	}
	if rec := e.send(http.MethodPost, "/workspaces/lab/issues", apiKeyAuth(), `{"title":"t","assignees":[" "]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a blank assignee: status = %d; want 400, the assignees are checked", rec.Code)
	}
}
