package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/aflib/issuex"
)

// fakeWriter is a client whose writes are recorded and answered by behave, in place of
// the forge's: a client from Config.NewClient that is also a writer is used as it is.
// Every other method of issuex.Client panics.
type fakeWriter struct {
	issuex.Client

	mu    sync.Mutex
	calls []fakeCall
	// behave answers a call: the error it fails with, nil for success. It may take time.
	// n counts the calls of the method, from 1.
	behave func(ctx context.Context, method string, n int) error
}

// fakeCall is a call the fake received and how long its context had left.
type fakeCall struct {
	method string
	left   time.Duration
}

func (f *fakeWriter) do(ctx context.Context, method string) error {
	f.mu.Lock()
	n := 1
	for _, c := range f.calls {
		if c.method == method {
			n++
		}
	}
	var left time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		left = time.Until(deadline)
	}
	f.calls = append(f.calls, fakeCall{method, left})
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
	var out []string
	for _, c := range f.calls {
		out = append(out, c.method)
	}
	return out
}

func (f *fakeWriter) call(i int) fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

func (f *fakeWriter) CreateIssue(ctx context.Context, _ issuex.Repo, _ issuex.CreateIssueRequest) (issuex.Issue, error) {
	return issuex.Issue{Number: 7}, f.do(ctx, "CreateIssue")
}

func (f *fakeWriter) ChangeIssue(ctx context.Context, _ issuex.IssueRef, _ issueUpdate) (issuex.Issue, error) {
	return issuex.Issue{Number: 42}, f.do(ctx, "ChangeIssue")
}

func (f *fakeWriter) AddComment(ctx context.Context, _ issuex.IssueRef, _ string) (string, error) {
	return "https://github.com/acme/widgets/issues/42#issuecomment-1", f.do(ctx, "AddComment")
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

// useWriter makes the routes use f.
func useWriter(f *fakeWriter) func(*Config) {
	return func(cfg *Config) {
		cfg.NewClient = func(issuex.Options) (issuex.Client, error) { return f, nil }
	}
}

// notFound is what the forge's not found is to a call.
func notFound() error {
	return &issuex.HTTPError{Method: "GET", Path: "/x", Status: http.StatusNotFound, Message: "Not Found"}
}

// TS-31-43: A forge failure of a write is answered with the status and error_type of the
// mapping of the reads, 429 with Retry-After passed through, and the message is scrubbed
// of the token and of URL userinfo.
// Requirement: 31-REQ-8.1
func TestWriteFailures_UseTheSharedMapping_TS_31_43(t *testing.T) {
	logs := captureLog(t)
	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
		wantStatus int
		wantType   string
	}{
		{"401", 401, "", 502, "forge_unauthenticated"},
		{"403", 403, "", 502, "forge_forbidden"},
		{"404", 404, "", 404, "forge_not_found"},
		{"422", 422, "", 422, "forge_rejected"},
		{"429", 429, "30", 429, "forge_rate_limited"},
		{"500", 500, "", 502, "forge_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			em := &recordingEmitter{}
			e := newWriteEnv(t, withEmitter(em), noSleep)
			e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"message":"no good for origin-token at https://bot:hunter2@github.com/acme/widgets"}`)
			})
			rec := e.send(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(), `{"title":"t"}`)
			got := decodeError(t, rec)
			if rec.Code != tc.wantStatus || got.ErrorType != tc.wantType {
				t.Errorf("answer = %d %+v; want %d %s", rec.Code, got, tc.wantStatus, tc.wantType)
			}
			if strings.Contains(rec.Body.String(), "origin-token") || strings.Contains(rec.Body.String(), "hunter2") {
				t.Errorf("the message was not scrubbed: %s", rec.Body)
			}
			if tc.status == 429 {
				if got := rec.Header().Get("Retry-After"); got != "31" {
					t.Errorf("Retry-After = %q; want 31, the forge's 30 seconds and the second issuex adds", got)
				}
			} else if rec.Header().Get("Retry-After") != "" {
				t.Errorf("Retry-After = %q on a %s", rec.Header().Get("Retry-After"), tc.name)
			}
			events := em.recorded()
			if len(events) != 1 || events[0].Metadata["error_type"] != tc.wantType || events[0].Metadata["ok"] != false {
				t.Errorf("events = %+v; want one failure with error_type %s", events, tc.wantType)
			}
		})
	}
	if out := logs.String(); strings.Contains(out, "origin-token") || strings.Contains(out, "hunter2") {
		t.Errorf("the log holds the token or the password:\n%s", out)
	}
	// A write that fails with a 5xx or a 429 of hub's is logged once, as for the reads: a
	// forge 401, 403 and 500 are hub's 502, and a forge 429 is hub's 429.
	if n := strings.Count(logs.String(), "forge: request failed"); n != 4 {
		t.Errorf("%d warnings about failed requests; want 4 (the 401, 403, 429 and 500)", n)
	}
}

// TS-31-44: Hub adds no retry to a write: with every call failing, each issuex write method
// is called at most once per step of the request, and a step that failed ends the request.
// Requirement: 31-REQ-8.2
func TestWritesAreNotRetried_TS_31_44(t *testing.T) {
	failures := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
	}{
		{"a 5xx", &issuex.HTTPError{Method: "POST", Path: "/x", Status: 503, Message: "unavailable"}, 502, "forge_error"},
		{"a timeout", fmt.Errorf("POST /x: %w", context.DeadlineExceeded), 504, "forge_timeout"},
		{"an unreachable forge", &url.Error{Op: "Post", URL: "https://api.github.com/x", Err: errors.New("connection refused")}, 502, "forge_unreachable"},
	}
	closeWithComment := writeRouteCase{"close issue with a comment", http.MethodPost, "/workspaces/%s/issues/42/close", `{"comment":"done"}`}
	routes := append(append([]writeRouteCase(nil), writeRouteCases...), closeWithComment)
	want := map[string][]string{
		"create issue":               {"CreateIssue"},
		"update issue":               {"ChangeIssue"},
		"close issue":                {"ChangeIssue"},
		"close issue with a comment": {"AddComment"},
		"comment on issue":           {"AddComment"},
		"add labels":                 {"AddLabels"},
		"remove label":               {"DropLabel"},
		"ensure label":               {"GetLabel"},
		"open pull request":          {"CreatePullRequest"},
		"comment on pull request":    {"AddComment"},
	}

	for _, route := range routes {
		for _, failure := range failures {
			fake := &fakeWriter{behave: func(context.Context, string, int) error { return failure.err }}
			em := &recordingEmitter{}
			e := newWriteEnv(t, useWriter(fake), withEmitter(em))
			rec := e.sendRoute(route, "widgets", apiKeyAuth())
			if got := decodeError(t, rec); rec.Code != failure.wantStatus || got.ErrorType != failure.wantType {
				t.Errorf("%s with %s: answer = %d %+v; want %d %s", route.name, failure.name, rec.Code, got, failure.wantStatus, failure.wantType)
			}
			if got := fmt.Sprint(fake.names()); got != fmt.Sprint(want[route.name]) {
				t.Errorf("%s with %s: calls = %s; want %v", route.name, failure.name, got, want[route.name])
			}
			if n := len(em.recorded()); n != 1 {
				t.Errorf("%s with %s: %d events recorded; want 1", route.name, failure.name, n)
			}
		}
	}
}

// TS-31-45: A write the caller walks away from still reaches the forge and is recorded
// once, for a request issuex sends, one hub sends itself, and the second step of a close.
// Requirement: 31-REQ-8.3
func TestWriteContinuesAfterTheCallerLeaves_TS_31_45(t *testing.T) {
	for _, tc := range []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantSteps  int
	}{
		{"create issue", http.MethodPost, "/workspaces/widgets/issues", `{"title":"t"}`, http.StatusCreated, 1},
		{"update issue", http.MethodPatch, "/workspaces/widgets/issues/42", `{"title":"t"}`, http.StatusOK, 1},
		{"close with a comment", http.MethodPost, "/workspaces/widgets/issues/42/close", `{"comment":"done"}`, http.StatusOK, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			em := &recordingEmitter{}
			e := newWriteEnv(t, withEmitter(em))
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var mu sync.Mutex
			stayed := true
			e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
				first := false
				once.Do(func() { first = true; close(started) })
				if first {
					<-release
					mu.Lock()
					stayed = r.Context().Err() == nil // the hub did not hang up
					mu.Unlock()
				}
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
					respond(http.StatusCreated, commentBody)(w, r)
				case r.Method == http.MethodPost:
					respond(http.StatusCreated, issueBody(7, "t", ""))(w, r)
				default:
					respond(http.StatusOK, closedIssue(42))(w, r)
				}
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest(tc.method, "/api/v1"+tc.path, strings.NewReader(tc.body)).WithContext(ctx)
			req.Header.Set("X-Test-Auth", authHeader(t, apiKeyAuth()))
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { e.echo.ServeHTTP(rec, req); close(done) }()

			<-started
			cancel() // the caller leaves while the forge is answering
			time.Sleep(50 * time.Millisecond)
			close(release)
			<-done

			mu.Lock()
			defer mu.Unlock()
			if !stayed {
				t.Error("hub hung up on the forge when the caller left")
			}
			if n := e.stub.count(); n != tc.wantSteps {
				t.Errorf("the forge received %d requests; want %d", n, tc.wantSteps)
			}
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d; want %d\n%s", rec.Code, tc.wantStatus, rec.Body)
			}
			if n := len(em.recorded()); n != 1 {
				t.Errorf("%d events recorded; want 1", n)
			}
			em.mu.Lock()
			defer em.mu.Unlock()
			if len(em.ctxErr) != 1 || em.ctxErr[0] != nil {
				t.Errorf("the event was emitted on a cancelled context: %v", em.ctxErr)
			}
		})
	}
}

// TS-31-46: Every request of a write that takes several gets the whole per-call timeout
// from its own start: the comment and the close, the lookup, the create and the second
// lookup, the removal and the read of the issue.
// Requirement: 31-REQ-8.4
func TestEachRequestGetsItsOwnTimeout_TS_31_46(t *testing.T) {
	previous := callDeadline
	callDeadline = 600 * time.Millisecond
	t.Cleanup(func() { callDeadline = previous })
	const used = 200 * time.Millisecond // taken by each request before the last

	for _, tc := range []struct {
		name   string
		route  writeRouteCase
		behave func(ctx context.Context, method string, n int) error
		calls  []string
	}{
		{"close with a comment", writeRouteCase{"", http.MethodPost, "/workspaces/%s/issues/42/close", `{"comment":"done"}`},
			func(ctx context.Context, method string, n int) error { time.Sleep(used); return nil },
			[]string{"AddComment", "ChangeIssue"}},
		{"label lookup, create and lookup again", writeRouteCase{"", http.MethodPost, "/workspaces/%s/labels", `{"name":"af:triage"}`},
			func(ctx context.Context, method string, n int) error {
				time.Sleep(used)
				switch {
				case method == "GetLabel" && n == 1:
					return notFound()
				case method == "MakeLabel":
					return &issuex.HTTPError{Status: 422, Message: "Validation Failed"}
				}
				return nil
			},
			[]string{"GetLabel", "MakeLabel", "GetLabel"}},
		{"label removal and read", writeRouteCase{"", http.MethodDelete, "/workspaces/%s/issues/42/labels/af:fix", ``},
			func(ctx context.Context, method string, n int) error {
				time.Sleep(used)
				if method == "DropLabel" {
					return notFound()
				}
				return nil
			},
			[]string{"DropLabel", "CheckIssue"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeWriter{behave: tc.behave}
			e := newWriteEnv(t, useWriter(fake))
			if rec := e.sendRoute(tc.route, "widgets", apiKeyAuth()); rec.Code >= 300 {
				t.Fatalf("status = %d\n%s", rec.Code, rec.Body)
			}
			if got := fmt.Sprint(fake.names()); got != fmt.Sprint(tc.calls) {
				t.Fatalf("calls = %s; want %v", got, tc.calls)
			}
			// A deadline shared by the requests would leave the last one 600 - 2*200 = 200ms
			// or less; its own leaves nearly all of 600ms.
			last := fake.call(len(tc.calls) - 1)
			if last.left < callDeadline-100*time.Millisecond {
				t.Errorf("the last request had %s left; want nearly the whole %s", last.left, callDeadline)
			}
			if first := fake.call(0); first.left > callDeadline {
				t.Errorf("the first request had %s left; want at most %s", first.left, callDeadline)
			}
		})
	}
}

// TS-31-47: No response body, header, log line or event holds the workspace's token or the
// userinfo of its git URL, for any write route and any outcome, the forge's messages
// echoing both included.
// Requirement: 31-REQ-8.5
func TestNoCredentialInResponsesLogsOrEvents_TS_31_47(t *testing.T) {
	logs := captureLog(t)
	// The forge echoes the token and the URL it was given, userinfo and all. (A bare
	// user:pass@ outside a URL is not URL userinfo, and the scrubber leaves it.)
	const echoed = `bad credentials origin-token for https://user:pass@github.com/acme/widgets`
	forbidden := []string{"origin-token", "user:pass"}

	outcomes := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"success", githubWrites()},
		{"401", func(w http.ResponseWriter, r *http.Request) { respondMessage(w, 401, echoed) }},
		{"403", func(w http.ResponseWriter, r *http.Request) { respondMessage(w, 403, echoed) }},
		{"404", func(w http.ResponseWriter, r *http.Request) { respondMessage(w, 404, echoed) }},
		{"422", func(w http.ResponseWriter, r *http.Request) { respondMessage(w, 422, echoed) }},
		{"429", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "5")
			respondMessage(w, 429, echoed)
		}},
		{"500", func(w http.ResponseWriter, r *http.Request) { respondMessage(w, 500, echoed) }},
	}
	em := &recordingEmitter{}
	var seen []string
	check := func(where string, texts ...string) {
		for _, text := range texts {
			for _, f := range forbidden {
				if strings.Contains(text, f) {
					t.Errorf("%s holds %q:\n%s", where, f, text)
				}
			}
		}
	}

	for _, outcome := range outcomes {
		e := newEnv(t, withEmitter(em), noSleep)
		addWorkspace(t, e.db, "widgets", func(r *workspaceRow) { r.gitURL = "https://user:pass@github.com/acme/widgets" })
		e.setVar("widgets", "FORGE_API_URL", e.stub.URL)
		e.stub.setHandler(outcome.handler)
		for _, route := range writeRouteCases {
			rec := e.sendRoute(route, "widgets", apiKeyAuth())
			seen = append(seen, fmt.Sprintf("%s %s %d", outcome.name, route.name, rec.Code))
			headers := fmt.Sprint(rec.Header())
			check(fmt.Sprintf("the response to %s on %s (%d)", route.name, outcome.name, rec.Code), rec.Body.String(), headers)
		}
	}

	// A forge that cannot be reached, whose transport error names the token.
	e := newEnv(t, withEmitter(em), func(cfg *Config) {
		cfg.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial https://user:pass@github.com failed for origin-token")
		})
	})
	addWorkspace(t, e.db, "widgets", func(r *workspaceRow) { r.gitURL = "https://user:pass@github.com/acme/widgets" })
	for _, route := range writeRouteCases {
		rec := e.sendRoute(route, "widgets", apiKeyAuth())
		if rec.Code < 500 {
			t.Errorf("%s on an unreachable forge: status = %d; want a failure", route.name, rec.Code)
		}
		check("the response to "+route.name+" on an unreachable forge", rec.Body.String(), fmt.Sprint(rec.Header()))
	}

	check("the log", logs.String())
	events := em.recorded()
	if len(events) < len(outcomes)*len(writeRouteCases) {
		t.Fatalf("%d events recorded; the run proves nothing\n%v", len(events), seen)
	}
	for _, ev := range events {
		check("an event", fmt.Sprintf("%+v", ev))
	}
}

// respondMessage answers with the status and a GitHub error body carrying message.
func respondMessage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"message":%q}`, message)
}
