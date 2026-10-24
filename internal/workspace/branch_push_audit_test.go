package workspace

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// stubPush makes the shared push return err without contacting anything.
func stubPush(t *testing.T, err error) {
	t.Helper()
	orig := pushToOriginFn
	pushToOriginFn = func(context.Context, string, string, transport.AuthMethod, []config.RefSpec, bool, io.Writer) error {
		return err
	}
	t.Cleanup(func() { pushToOriginFn = orig })
}

// pushOutcome is a push attempt outcome made with a stubbed push.
type pushOutcome struct {
	name      string
	err       error
	body      string
	status    int
	result    string
	errorType string
}

// stubbedOutcomes are every push attempt outcome of 38-REQ-8.2.
var stubbedOutcomes = []pushOutcome{
	{"pushed", nil, "", 200, "pushed", ""},
	{"forced", nil, `{"force":true}`, 200, "forced", ""},
	{"up_to_date", git.NoErrAlreadyUpToDate, "", 200, "up_to_date", ""},
	{"non_fast_forward", errors.New("non-fast-forward update: refs/heads/feat"), "", 409, "", "non_fast_forward"},
	{"forge_not_found", fmt.Errorf("%w: not found", transport.ErrRepositoryNotFound), "", 404, "", "forge_not_found"},
	{"forge_rejected", errors.New("command error on refs/heads/feat: pre-receive hook declined"), "", 422, "", "forge_rejected"},
	{"forge_unauthenticated", transport.ErrAuthenticationRequired, "", 502, "", "forge_unauthenticated"},
	{"forge_forbidden", transport.ErrAuthorizationFailed, "", 502, "", "forge_forbidden"},
	{"forge_unreachable", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, "", 502, "", "forge_unreachable"},
	{"forge_error", errors.New("unexpected EOF"), "", 502, "", "forge_error"},
	{"forge_timeout", context.DeadlineExceeded, "", 504, "", "forge_timeout"},
}

// TS-38-8: Own error types carry their type as a message prefix while shared
// answers keep shared messages.
// Requirement: 38-REQ-1.8
func TestBranchPushMessagePrefixes_TS_38_8(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedPushWorkspace(t, &Workspace{Slug: "msgs", OwnerID: "user-a", Branch: strPtr("main")}, "feat")
	env.seedPushWorkspace(t, &Workspace{Slug: "msgs-cp", OwnerID: "user-a", WorkspaceMode: "carry_patch"}, "p1")
	seedPatchRow(t, env, "msgs-cp", "p1", "active", 0)
	user := userAuth("user-a")

	own := []struct {
		rec       *httptest.ResponseRecorder
		errorType string
	}{
		{env.push(t, "msgs", "ghost", "", user), "branch_not_found"},
		{env.push(t, "msgs", "-x", "", user), "branch_not_found"},
		{env.push(t, "msgs-cp", "deploy", "", user), "integration_branch"},
		{env.push(t, "msgs-cp", "p1", "", user), "patch_branch"},
		{env.push(t, "msgs", "main", `{"force":true}`, user), "protected_branch"},
	}
	for _, o := range stubbedOutcomes {
		if o.errorType == "" {
			continue
		}
		stubPush(t, o.err)
		own = append(own, struct {
			rec       *httptest.ResponseRecorder
			errorType string
		}{env.push(t, "msgs", "feat", o.body, user), o.errorType})
	}
	for _, o := range own {
		e := parseErrorEnvelope(t, o.rec)
		if e.Error.ErrorType != o.errorType || !strings.HasPrefix(e.Error.Message, o.errorType+": ") {
			t.Errorf("%s: message %q (type %q); want it to start with %q", o.errorType, e.Error.Message, e.Error.ErrorType, o.errorType+": ")
		}
	}

	env.seedOrg(t, "org-msgs", "Msgs", "msgs-org")
	env.seedOrgMember(t, "org-msgs", "user-m")
	org := "org-msgs"
	env.seedWorkspace(t, &Workspace{Slug: "msgs-org", GitURL: "https://x/y", OwnerID: "user-a", OrgID: &org, Status: "ready"})
	assertRefusal(t, env.push(t, "nope", "feat", "", user), 404, "workspace not found", "")
	assertRefusal(t, env.push(t, "msgs-org", "feat", "", userAuth("user-m")), 403, "org members have read-only access to this workspace", "forbidden")
	assertRefusal(t, env.push(t, "msgs", "feat", "", patAuth("user-a", "workspaces:read")), 403, "PAT requires workspaces:write scope to push branches", "")
	holdLock(t, "msgs")
	assertRefusal(t, env.push(t, "msgs", "feat", "", user), 409, "another operation is running on this workspace; retry later", "workspace_busy")
}

// TS-38-31: A stalled forge yields 504 forge_timeout at the two-minute
// deadline, and a disconnect does not cancel the push.
// Requirement: 38-REQ-6.3
func TestBranchPushDeadlineAndDisconnect_TS_38_31(t *testing.T) {
	env := newBranchPushEnv(t)
	logs := captureBranchPushLog(t)

	// The push runs on a two-minute deadline from its start.
	env.seedPushWorkspace(t, &Workspace{Slug: "deadline", OwnerID: "user-a"}, "feat")
	orig := pushToOriginFn
	var start, deadline time.Time
	pushToOriginFn = func(ctx context.Context, p, u string, a transport.AuthMethod, r []config.RefSpec, f bool, w io.Writer) error {
		start = time.Now()
		deadline, _ = ctx.Deadline()
		return orig(ctx, p, u, a, r, f, w)
	}
	pushedOK(t, env.push(t, "deadline", "feat", "", userAuth("user-a")))
	pushToOriginFn = orig
	if d := deadline.Sub(start); d > 2*time.Minute || d < 2*time.Minute-5*time.Second {
		t.Errorf("the push's deadline is %v after its start; want two minutes", d)
	}

	// A forge that stalls past the deadline is a timeout.
	forge := newStandInForge(t)
	forge.setAnswer(func(w http.ResponseWriter, r *http.Request) bool {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return true
	})
	env.seedPushWorkspace(t, &Workspace{Slug: "stalled", OwnerID: "user-a"}, "feat")
	setGitURL(t, env, "stalled", forge.repoURL())
	setBranchPushDeadline(t, 300*time.Millisecond)
	refusedWithType(t, env.push(t, "stalled", "feat", "", userAuth("user-a")), http.StatusGatewayTimeout, "forge_timeout")
	setBranchPushDeadline(t, 2*time.Minute)

	// A caller that goes away does not cancel the push, which keeps the lock
	// until it ends and is recorded as if the caller were still there.
	bare := env.seedPushWorkspace(t, &Workspace{Slug: "gone-caller", OwnerID: "user-a"}, "feat")
	writeHook(t, bare, "pre-receive", "#!/bin/sh\nsleep 1\nexit 0\n")
	before := len(env.branchEvents())
	rec := pushThenDisconnect(t, env, "gone-caller", "feat", 300*time.Millisecond)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want the push's own 200: %s", rec.Code, rec.Body)
	}
	if got := bareRef(t, bare, "refs/heads/feat"); got != env.trunkTip(t, "gone-caller", "feat") {
		t.Errorf("origin's feat = %q; the push did not complete", got)
	}
	if evs := env.branchEvents()[before:]; len(evs) != 1 || evs[0].Metadata["ok"] != true {
		t.Errorf("events after the disconnect: %+v; want one with ok true", evs)
	}
	if strings.Contains(logs.String(), `"workspace":"gone-caller","branch"`) {
		t.Errorf("a failure was logged for the completed push: %s", logs)
	}
	assertLockFree(t, "gone-caller")
}

// setBranchPushDeadline sets the push deadline for the rest of the test.
func setBranchPushDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	old := branchPushDeadline
	branchPushDeadline = d
	t.Cleanup(func() { branchPushDeadline = old })
}

// pushThenDisconnect sends the push, cancels its request context after delay,
// checks that the workspace lock is still held then, and waits for the
// handler to return.
func pushThenDisconnect(t *testing.T, env *branchPushEnv, slug, name string, delay time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+slug+"/branches/"+name+"/push", nil).WithContext(ctx)
	authJSON, _ := json.Marshal(userAuth("user-a"))
	req.Header.Set("X-Test-Auth", string(authJSON))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		env.echo.ServeHTTP(rec, req)
		close(done)
	}()
	time.Sleep(delay)
	cancel()
	if unlock, ok := wslock.TryLock(slug); ok {
		unlock()
		t.Errorf("the workspace lock of %s was free while the push ran", slug)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the handler did not return")
	}
	return rec
}

// sseEvents subscribes to the audit event stream of the hub at url as an
// admin and counts the hub.branch.push events it receives.
type sseEvents struct {
	mu    sync.Mutex
	lines []string
}

func subscribeBranchEvents(t *testing.T, url string, mgr *audit.SSEManager) *sseEvents {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/v1/events", nil)
	authJSON, _ := json.Marshal(adminAuth())
	req.Header.Set("X-Test-Auth", string(authJSON))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events: %d", resp.StatusCode)
	}
	for deadline := time.Now().Add(3 * time.Second); mgr.ConnCount() != 1 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	s := &sseEvents{}
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") && strings.Contains(line, `"event_type":"hub.branch.push"`) {
				s.mu.Lock()
				s.lines = append(s.lines, line)
				s.mu.Unlock()
			}
		}
	}()
	return s
}

func (s *sseEvents) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lines)
}

// capturingEmitter records every event and passes it on.
type capturingEmitter struct {
	*mockAuditEmitter
	next audit.Emitter
}

func (e *capturingEmitter) Emit(ctx context.Context, event audit.HubEvent) error {
	_ = e.mockAuditEmitter.Emit(ctx, event)
	return e.next.Emit(ctx, event)
}

// TS-38-42: Every push attempt outcome emits exactly one hub.branch.push event
// with the right identity fields, also on the SSE stream.
// Requirement: 38-REQ-8.2
func TestBranchPushEventPerAttempt_TS_38_42(t *testing.T) {
	env := newBranchPushEnv(t)
	duck, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { duck.Close() })
	if err := audit.InitSchema(duck); err != nil {
		t.Fatal(err)
	}
	store := audit.NewStore(duck)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := audit.NewSSEManager(10)
	go mgr.Run(ctx.Done())
	capture := &capturingEmitter{newMockAuditEmitter(), audit.NewEmitter(store, mgr)}
	defaultAuditEmitter = capture

	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	if err := RegisterRoutes(api, env.db); err != nil {
		t.Fatal(err)
	}
	audit.RegisterAuditQueryRoutes(api, store, mgr, env.db)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	stream := subscribeBranchEvents(t, srv.URL, mgr)

	env.seedPushWorkspace(t, &Workspace{Slug: "events", OwnerID: "user-a"}, "feat")
	for _, o := range stubbedOutcomes {
		stubPush(t, o.err)
		before := len(capture.Events())
		rec := env.push(t, "events", "feat", o.body, userAuth("user-a"))
		if rec.Code != o.status {
			t.Errorf("%s: status %d; want %d: %s", o.name, rec.Code, o.status, rec.Body)
		}
		evs := capture.Events()[before:]
		if len(evs) != 1 {
			t.Errorf("%s: %d events; want exactly one", o.name, len(evs))
			continue
		}
		ev := evs[0]
		if ev.EventType != "hub.branch.push" || ev.ResourceType != "branch" || ev.ResourceID != "feat" || ev.Action != "push" ||
			ev.Workspace != "events" || ev.ActorID != "user-a" || ev.ActorType != "api_key" {
			t.Errorf("%s: event %+v", o.name, ev)
		}
	}
	for deadline := time.Now().Add(5 * time.Second); stream.count() < len(stubbedOutcomes) && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if n := stream.count(); n != len(stubbedOutcomes) {
		t.Errorf("the SSE stream carried %d hub.branch.push events; want %d", n, len(stubbedOutcomes))
	}
}

// TS-38-43: The event metadata has branch, sha, force, ok, remote, remote_url
// and exactly one of result or error_type, never secrets or forge text.
// Requirement: 38-REQ-8.3
func TestBranchPushEventMetadata_TS_38_43(t *testing.T) {
	env := newBranchPushEnv(t)
	forge := newStandInForge(t)
	env.seedPushWorkspace(t, &Workspace{Slug: "meta", OwnerID: "user-a"}, "feat", "other")
	gitURL := strings.Replace(forge.repoURL(), "http://", "http://u:urlpw@", 1)
	setGitURL(t, env, "meta", gitURL)
	storeGitSecrets(t, env, "meta", "GIT_PAT", "tok-12345")

	pushedOK(t, env.push(t, "meta", "feat", "", userAuth("user-a")))
	pushedOK(t, env.push(t, "meta", "other", `{"force":true}`, userAuth("user-a")))
	writeHook(t, forge.bare, "pre-receive", "#!/bin/sh\necho 'no: tok-12345 at http://u:urlpw@forge/x' >&2\nexit 1\n")
	pushCommit(t, filepath.Join(env.root, "meta", "trunk"), "more.txt", "more")
	refusedWithType(t, env.push(t, "meta", "main", "", userAuth("user-a")), http.StatusUnprocessableEntity, "forge_rejected")

	evs := env.branchEvents()
	if len(evs) != 3 {
		t.Fatalf("%d events; want 3", len(evs))
	}
	for i, ev := range evs {
		m := ev.Metadata
		for _, key := range []string{"branch", "sha", "force", "ok", "remote", "remote_url"} {
			if _, ok := m[key]; !ok {
				t.Errorf("event %d lacks %s: %v", i, key, m)
			}
		}
		_, hasResult := m["result"]
		_, hasType := m["error_type"]
		if hasResult == hasType {
			t.Errorf("event %d: result %v, error_type %v; want exactly one", i, m["result"], m["error_type"])
		}
		if sha, _ := m["sha"].(string); !fortyHex.MatchString(sha) {
			t.Errorf("event %d: sha %v", i, m["sha"])
		}
		if m["remote"] != "origin" || m["remote_url"] != forge.repoURL() {
			t.Errorf("event %d: remote %v, remote_url %v; want origin and %s", i, m["remote"], m["remote_url"], forge.repoURL())
		}
		for k, v := range m {
			s := fmt.Sprint(v)
			if strings.Contains(s, "tok-12345") || strings.Contains(s, "urlpw") || strings.Contains(s, "no:") || strings.Contains(s, "declined") {
				t.Errorf("event %d: %s = %q holds a secret or forge text", i, k, s)
			}
		}
	}
	if evs[0].Metadata["ok"] != true || evs[0].Metadata["result"] != "pushed" || evs[0].Metadata["force"] != false {
		t.Errorf("first event %v", evs[0].Metadata)
	}
	if evs[1].Metadata["force"] != true || evs[1].Metadata["result"] != "forced" {
		t.Errorf("forced event %v", evs[1].Metadata)
	}
	if evs[2].Metadata["ok"] != false || evs[2].Metadata["error_type"] != "forge_rejected" {
		t.Errorf("failed event %v", evs[2].Metadata)
	}
}

// TS-38-44: Refusals and hub-internal failures emit no hub.branch.push event.
// Requirement: 38-REQ-8.4
func TestBranchPushRefusalsEmitNothing_TS_38_44(t *testing.T) {
	env := newBranchPushEnv(t)
	user := userAuth("user-a")
	env.seedPushWorkspace(t, &Workspace{Slug: "quiet", OwnerID: "user-a", Branch: strPtr("main")}, "feat")
	env.seedPushWorkspace(t, &Workspace{Slug: "quiet-cp", OwnerID: "user-a", WorkspaceMode: "carry_patch"}, "p1")
	seedPatchRow(t, env, "quiet-cp", "p1", "active", 0)
	env.seedWorkspace(t, &Workspace{Slug: "quiet-archived", GitURL: "x", OwnerID: "user-a", Status: "archived"})
	env.seedWorkspace(t, &Workspace{Slug: "quiet-cloning", GitURL: "x", OwnerID: "user-a", Status: "cloning"})
	env.seedWorkspace(t, &Workspace{Slug: "quiet-no-trunk", GitURL: "x", OwnerID: "user-a", Status: "ready"})
	env.seedPushWorkspace(t, &Workspace{Slug: "quiet-no-origin", OwnerID: "user-a"}, "feat")
	pushGit(t, filepath.Join(env.root, "quiet-no-origin", "trunk"), "remote", "remove", "origin")
	env.seedOrg(t, "org-q", "Q", "q")
	env.seedOrgMember(t, "org-q", "user-m")
	org := "org-q"
	env.seedWorkspace(t, &Workspace{Slug: "quiet-org", GitURL: "x", OwnerID: "user-a", OrgID: &org, Status: "ready"})

	for _, c := range []struct {
		slug, name, body string
		auth             *apikit.AuthInfo
		status           int
	}{
		{"quiet", "feat", "", nil, 401},
		{"quiet", "feat", "", patAuth("user-a", "workspaces:read"), 403},
		{"quiet-org", "feat", "", userAuth("user-m"), 403},
		{"nope", "feat", "", user, 404},
		{"quiet", "-x", "", user, 404},
		{"quiet", "ghost", "", user, 404},
		{"quiet", "feat", "{bad", user, 400},
		{"quiet-archived", "feat", "", user, 400},
		{"quiet-cloning", "feat", "", user, 400},
		{"quiet-cp", "deploy", "", user, 400},
		{"quiet-cp", "p1", "", user, 400},
		{"quiet", "main", `{"force":true}`, user, 400},
		{"quiet-no-trunk", "feat", "", user, 500},
		{"quiet-no-origin", "feat", "", user, 500},
	} {
		if rec := env.push(t, c.slug, c.name, c.body, c.auth); rec.Code != c.status {
			t.Errorf("%s %s %s: status %d; want %d: %s", c.slug, c.name, c.body, rec.Code, c.status, rec.Body)
		}
	}
	release := holdLock(t, "quiet")
	if rec := env.push(t, "quiet", "feat", "", user); rec.Code != 409 {
		t.Errorf("busy: status %d", rec.Code)
	}
	release()
	if _, err := env.db.Exec(`DROP TABLE secrets`); err != nil {
		t.Fatal(err)
	}
	if rec := env.push(t, "quiet", "feat", "", user); rec.Code != 500 {
		t.Errorf("secrets failure: status %d", rec.Code)
	}
	if evs := env.branchEvents(); len(evs) != 0 {
		t.Errorf("%d hub.branch.push events; want none: %+v", len(evs), evs)
	}
}

// ctxEmitter records each event and whether its context was already done.
type ctxEmitter struct {
	mu     sync.Mutex
	events []audit.HubEvent
	errs   []error
}

func (e *ctxEmitter) Emit(ctx context.Context, event audit.HubEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
	e.errs = append(e.errs, ctx.Err())
	return nil
}

// TS-38-45: When the caller disconnects mid-push the event is still emitted
// once with the real outcome.
// Requirement: 38-REQ-8.5, 38-REQ-6.3
func TestBranchPushDisconnectEvent_TS_38_45(t *testing.T) {
	env := newBranchPushEnv(t)
	for _, v := range []struct {
		slug, hook, result, errorType string
		status                        int
	}{
		{"late-ok", "#!/bin/sh\nsleep 1\nexit 0\n", "pushed", "", 200},
		{"late-no", "#!/bin/sh\nsleep 1\necho refused >&2\nexit 1\n", "", "forge_rejected", 422},
	} {
		emitter := &ctxEmitter{}
		defaultAuditEmitter = emitter
		bare := env.seedPushWorkspace(t, &Workspace{Slug: v.slug, OwnerID: "user-a"}, "feat")
		writeHook(t, bare, "pre-receive", v.hook)
		rec := pushThenDisconnect(t, env, v.slug, "feat", 300*time.Millisecond)
		if rec.Code != v.status {
			t.Errorf("%s: status %d; want %d", v.slug, rec.Code, v.status)
		}
		emitter.mu.Lock()
		if len(emitter.events) != 1 {
			t.Errorf("%s: %d events; want one", v.slug, len(emitter.events))
		} else {
			m := emitter.events[0].Metadata
			if v.result != "" && (m["ok"] != true || m["result"] != v.result) || v.errorType != "" && (m["ok"] != false || m["error_type"] != v.errorType) {
				t.Errorf("%s: metadata %v", v.slug, m)
			}
			if emitter.errs[0] != nil {
				t.Errorf("%s: the event was emitted on a done context: %v", v.slug, emitter.errs[0])
			}
		}
		emitter.mu.Unlock()
		assertLockFree(t, v.slug)
	}
}
