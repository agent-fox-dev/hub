package forge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// recordingEmitter is an audit.Emitter that keeps what it is given.
type recordingEmitter struct {
	mu     sync.Mutex
	events []audit.HubEvent
	err    error
}

func (r *recordingEmitter) Emit(_ context.Context, event audit.HubEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return r.err
}

func (r *recordingEmitter) recorded() []audit.HubEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.HubEvent(nil), r.events...)
}

// withEmitter makes the routes record their events with em.
func withEmitter(em audit.Emitter) func(*Config) {
	return func(cfg *Config) { cfg.Audit = em }
}

// TS-31-53: With no emitter configured the writes behave as with one, and record nothing.
// Requirement: 31-REQ-9.6
func TestWrites_WithoutAnEmitter_TS_31_53(t *testing.T) {
	handler := routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues":          respond(http.StatusCreated, issueBody(7, "t", "b", "af:triage")),
		"GET /repos/acme/widgets/labels/af:triage": respond(http.StatusNotFound, `{"message":"Not Found"}`),
		"POST /repos/acme/widgets/labels":          respond(http.StatusCreated, `{"name":"af:triage","color":"ededed"}`),
	})
	em := &recordingEmitter{}
	with := newWriteEnv(t, withEmitter(em))
	without := newWriteEnv(t)
	with.stub.setHandler(handler)
	without.stub.setHandler(handler)

	for _, call := range []struct{ name, path, body string }{
		{"create issue", "/workspaces/widgets/issues", `{"title":"t","body":"b","labels":["af:triage"]}`},
		{"ensure label", "/workspaces/widgets/labels", `{"name":"af:triage"}`},
	} {
		a := with.send(http.MethodPost, call.path, apiKeyAuth(), call.body)
		b := without.send(http.MethodPost, call.path, apiKeyAuth(), call.body)
		if a.Code != b.Code || a.Body.String() != b.Body.String() {
			t.Errorf("%s: with an emitter %d %s; without %d %s; want the same", call.name, a.Code, a.Body, b.Code, b.Body)
		}
	}
	if len(em.recorded()) == 0 {
		t.Error("the control environment recorded no event: the comparison proves nothing")
	}
}

// TS-31-54: An emitter that fails is logged, with the event type and the workspace, and
// the response of the write is unchanged.
// Requirement: 31-REQ-9.7
func TestWrites_EmitFailureIsLogged_TS_31_54(t *testing.T) {
	logs := captureLog(t)
	em := &recordingEmitter{err: errors.New("store is down")}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues": respond(http.StatusCreated, issueBody(7, "t", "b", "af:triage")),
	}))

	rec := e.send(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(), `{"title":"t","body":"b","labels":["af:triage"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201\n%s", rec.Code, rec.Body)
	}
	var issue issuex.Issue
	if err := json.Unmarshal(rec.Body.Bytes(), &issue); err != nil || issue.Number != 7 {
		t.Errorf("body = %s; want the created issue 7 (%v)", rec.Body, err)
	}
	if n := len(em.recorded()); n != 1 {
		t.Fatalf("the emitter was called %d times; want 1", n)
	}

	var lines []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "audit: failed to emit") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("%d log lines about the emit failure; want 1\n%s", len(lines), logs)
	}
	for _, want := range []string{"level=ERROR", audit.EventForgeIssueCreate, "widgets"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("the log line %q does not hold %q", lines[0], want)
		}
	}
}

// TS-31-55: The nine event types are documented constants with the hub.forge.* values,
// and a write made on a hub wired like main.go is found by the audit query and reaches
// the SSE stream of its workspace.
// Requirement: 31-REQ-9.8
func TestWrites_AuditConstantsAndQuery_TS_31_55(t *testing.T) {
	want := map[string]string{
		"EventForgeIssueCreate":        "hub.forge.issue.create",
		"EventForgeIssueUpdate":        "hub.forge.issue.update",
		"EventForgeIssueClose":         "hub.forge.issue.close",
		"EventForgeCommentCreate":      "hub.forge.comment.create",
		"EventForgeLabelAdd":           "hub.forge.label.add",
		"EventForgeLabelRemove":        "hub.forge.label.remove",
		"EventForgeLabelCreate":        "hub.forge.label.create",
		"EventForgePullRequestCreate":  "hub.forge.pull_request.create",
		"EventForgePullRequestComment": "hub.forge.pull_request.comment",
	}
	got := map[string]string{
		"EventForgeIssueCreate":        audit.EventForgeIssueCreate,
		"EventForgeIssueUpdate":        audit.EventForgeIssueUpdate,
		"EventForgeIssueClose":         audit.EventForgeIssueClose,
		"EventForgeCommentCreate":      audit.EventForgeCommentCreate,
		"EventForgeLabelAdd":           audit.EventForgeLabelAdd,
		"EventForgeLabelRemove":        audit.EventForgeLabelRemove,
		"EventForgeLabelCreate":        audit.EventForgeLabelCreate,
		"EventForgePullRequestCreate":  audit.EventForgePullRequestCreate,
		"EventForgePullRequestComment": audit.EventForgePullRequestComment,
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("audit.%s = %q; want %q", name, got[name], value)
		}
	}
	assertDocumented(t, "../audit/types.go", want)

	// The hub as main.go wires it: a real audit store and SSE manager behind
	// audit.NewEmitterWithBroadcast, the forge routes and the audit query routes.
	db, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := audit.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	store := audit.NewStore(db)
	mgr := audit.NewSSEManager(5)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go mgr.Run(done)

	e := newWriteEnv(t, withEmitter(audit.NewEmitterWithBroadcast(store, mgr)))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues": respond(http.StatusCreated, issueBody(7, "t", "b", "af:triage")),
	}))
	query := e.echo.Group("/api/v1")
	query.Use(testAuthMiddleware())
	audit.RegisterAuditQueryRoutes(query, store, mgr, e.db)
	hub := httptest.NewServer(e.echo)
	t.Cleanup(hub.Close)

	// An SSE client of the workspace, connected before the write.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := http.NewRequestWithContext(ctx, http.MethodGet, hub.URL+"/api/v1/events?workspace=widgets", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream.Header.Set("X-Test-Auth", authHeader(t, adminAuth()))
	sse, err := http.DefaultClient.Do(stream)
	if err != nil || sse.StatusCode != http.StatusOK {
		t.Fatalf("SSE connect: %v %v", sse, err)
	}
	defer sse.Body.Close()
	frames := make(chan [2]string, 8)
	go func() {
		scanner := bufio.NewScanner(sse.Body)
		var event, data string
		for scanner.Scan() {
			switch line := scanner.Text(); {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "":
				frames <- [2]string{event, data}
				event, data = "", ""
			}
		}
	}()
	if first := <-frames; first[0] != "heartbeat" {
		t.Fatalf("first SSE frame = %q; want the heartbeat", first[0])
	}

	post, err := http.NewRequest(http.MethodPost, hub.URL+"/api/v1/workspaces/widgets/issues", strings.NewReader(`{"title":"t","body":"b","labels":["af:triage"]}`))
	if err != nil {
		t.Fatal(err)
	}
	post.Header.Set("X-Test-Auth", authHeader(t, apiKeyAuth()))
	resp, err := http.DefaultClient.Do(post)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /issues: %v %v", resp, err)
	}
	resp.Body.Close()

	// The audit query returns the event, already: it is recorded before the answer.
	q, err := http.NewRequest(http.MethodGet, hub.URL+"/api/v1/audit?workspace=widgets", nil)
	if err != nil {
		t.Fatal(err)
	}
	q.Header.Set("X-Test-Auth", authHeader(t, adminAuth()))
	qr, err := http.DefaultClient.Do(q)
	if err != nil || qr.StatusCode != http.StatusOK {
		t.Fatalf("GET /audit: %v %v", qr, err)
	}
	defer qr.Body.Close()
	var result struct {
		Events []struct {
			EventType  string `json:"event_type"`
			Workspace  string `json:"workspace"`
			ResourceID string `json:"resource_id"`
		} `json:"events"`
	}
	if err := json.NewDecoder(qr.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	var found int
	for _, ev := range result.Events {
		if ev.EventType == audit.EventForgeIssueCreate && ev.Workspace == "widgets" && ev.ResourceID == "7" {
			found++
		}
	}
	if found != 1 {
		t.Errorf("the audit query returned %d hub.forge.issue.create events for issue 7; want 1\n%+v", found, result.Events)
	}

	// The stream carries it.
	for {
		select {
		case frame := <-frames:
			if frame[0] != "audit_event" {
				continue
			}
			var ev audit.HubEvent
			if err := json.Unmarshal([]byte(frame[1]), &ev); err != nil {
				t.Fatal(err)
			}
			if ev.EventType != audit.EventForgeIssueCreate || ev.Workspace != "widgets" {
				t.Errorf("SSE event = %s in %s; want %s in widgets", ev.EventType, ev.Workspace, audit.EventForgeIssueCreate)
			}
			return
		case <-ctx.Done():
			t.Fatal("the SSE stream of the workspace did not receive the event")
		}
	}
}

// assertDocumented checks that each constant of want is declared in file with a doc
// comment that starts with its name.
func assertDocumented(t *testing.T, file string, want map[string]string) {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]string{}
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			comment := vs.Doc
			if comment == nil {
				comment = gen.Doc
			}
			for _, name := range vs.Names {
				if comment != nil {
					doc[name.Name] = comment.Text()
				}
			}
		}
	}
	for name := range want {
		if !strings.HasPrefix(doc[name], name+" is the hub audit event") {
			t.Errorf("%s has no doc comment that starts %q: %q", name, name+" is the hub audit event", doc[name])
		}
	}
}

// authHeader is the X-Test-Auth value that makes testAuthMiddleware inject auth.
func authHeader(t *testing.T, auth *apikit.AuthInfo) string {
	t.Helper()
	b, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
