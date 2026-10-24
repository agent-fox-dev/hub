package forge

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/aflib/issuex"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// httpError is an error of the forge as issuex builds it, echoing secrets in its message.
func httpError(status int, token string) *issuex.HTTPError {
	return &issuex.HTTPError{Method: "GET", Path: "/x", Status: status, Message: "denied for " + token + " see https://user:s3cr3t@forge.example/docs"}
}

// mapForgeError gives each class of error of issuex and of the transport its one answer,
// with the forge's status, a Retry-After rounded up, the basic credential's hint on a 401
// only, and a scrubbed message cut to size.
func TestMapForgeError(t *testing.T) {
	const token = "tok-123456"
	rateLimited := func(h *issuex.HTTPError) error { return fmt.Errorf("%w: %w", issuex.ErrRateLimited, h) }
	for _, tc := range []struct {
		name                    string
		err                     error
		status                  int
		errorType               string
		forgeStatus, retryAfter int
	}{
		{"404", fmt.Errorf("%w: %w", issuex.ErrNotFound, httpError(404, token)), 404, "forge_not_found", 404, 0},
		{"401", httpError(401, token), 502, "forge_unauthenticated", 401, 0},
		{"403", httpError(403, token), 502, "forge_forbidden", 403, 0},
		{"429 with retry-after", rateLimited(&issuex.HTTPError{Status: 429, RetryAfterHeader: "5"}), 429, "forge_rate_limited", 429, 6},
		{"bare ErrRateLimited", issuex.ErrRateLimited, 429, "forge_rate_limited", 0, 0},
		{"403 with no requests left", &issuex.HTTPError{Status: 403, RateLimitRemaining: "0", RetryAfterHeader: "9"}, 429, "forge_rate_limited", 403, 10},
		{"400", httpError(400, token), 422, "forge_rejected", 400, 0},
		{"bare ErrConflict", issuex.ErrConflict, 422, "forge_rejected", 0, 0},
		{"500", httpError(500, token), 502, "forge_error", 500, 0},
		{"unexpected 302", httpError(302, token), 502, "forge_error", 302, 0},
		{"decode", fmt.Errorf("GET /x: decoding response: %w", errors.New("invalid character")), 502, "forge_error", 0, 0},
		{"refused", &url.Error{Op: "Get", URL: "https://h/x", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}, 502, "forge_unreachable", 0, 0},
		{"tls", &url.Error{Op: "Get", URL: "https://h/x", Err: x509.HostnameError{Host: "h"}}, 502, "forge_unreachable", 0, 0},
		{"deadline", &url.Error{Op: "Get", URL: "https://h/x", Err: context.DeadlineExceeded}, 504, "forge_timeout", 0, 0},
		{"redirect", &url.Error{Op: "Get", URL: "https://h/x", Err: errRedirectRefused}, 502, "forge_error", 0, 0},
		{"canceled", &url.Error{Op: "Get", URL: "https://h/x", Err: context.Canceled}, 502, "forge_error", 0, 0},
		{"something else", errors.New("odd"), 502, "forge_error", 0, 0},
	} {
		got := mapForgeError(tc.err, credentialPAT, token)
		if got.status != tc.status || got.errorType != tc.errorType || got.forgeStatus != tc.forgeStatus || got.retryAfter != tc.retryAfter {
			t.Errorf("%s: %d %s (forge %d, retry-after %d); want %d %s (forge %d, retry-after %d)", tc.name,
				got.status, got.errorType, got.forgeStatus, got.retryAfter, tc.status, tc.errorType, tc.forgeStatus, tc.retryAfter)
		}
		if got.message == "" || strings.Contains(got.message, token) || strings.Contains(got.message, "s3cr3t") {
			t.Errorf("%s: message %q is empty or holds a secret", tc.name, got.message)
		}
	}
	for _, kind := range []credentialKind{credentialPAT, credentialNone, credentialBasic} {
		for _, status := range []int{401, 403} {
			msg := mapForgeError(httpError(status, token), kind, token).message
			if want := kind == credentialBasic && status == 401; strings.Contains(msg, "GIT_PASSWORD") != want {
				t.Errorf("%s credential, status %d: message %q; hint wanted %v", kind, status, msg, want)
			}
		}
	}
	long := mapForgeError(&issuex.HTTPError{Status: 401, Message: strings.Repeat("é", 2000)}, credentialBasic, token).message
	if !strings.Contains(long, "GIT_PAT") || len(long) > 1024+len(basicTokenHint)+100 {
		t.Errorf("a long 401 message: %d bytes, hint present %v", len(long), strings.Contains(long, "GIT_PAT"))
	}
	for d, want := range map[time.Duration]int{1200 * time.Millisecond: 2, 2 * time.Second: 2, time.Nanosecond: 1, 0: 0} {
		if got := retryAfterSeconds(d); got != want {
			t.Errorf("retryAfterSeconds(%v) = %d; want %d", d, got, want)
		}
	}
}

// Through a route, the forge's answers map to the error table: 404, 401 with the hint
// for a basic credential, 403, 429 with Retry-After rounded up (after the one retry issuex
// makes when it can wait), other 4xx, 5xx, a response that is not JSON, and the
// connection failures. The forge's message is carried, scrubbed.
func TestForgeErrors_ThroughARoute(t *testing.T) {
	basic := &githttp.BasicAuth{Username: "alice", Password: "basic-password"}
	for _, tc := range []struct {
		name       string
		handler    http.HandlerFunc
		auth       *githttp.BasicAuth
		path       string
		status     int
		errorType  string
		text       string
		retryAfter string
		requests   int
	}{
		{"404", answer(404, "Not Found"), nil, "/issues/9", 404, "forge_not_found", "Not Found", "", 1},
		{"401 with a pat", answer(401, "Bad credentials"), nil, "/forge/repository", 502, "forge_unauthenticated", "Bad credentials", "", 1},
		{"401 with a basic credential", answer(401, "Bad credentials"), basic, "/forge/repository", 502, "forge_unauthenticated", "GIT_PASSWORD", "", 1},
		{"403", answer(403, "Resource not accessible"), nil, "/pulls/3", 502, "forge_forbidden", "Resource not accessible", "", 1},
		{"429 waited out once", answer(429, "API rate limit exceeded", "Retry-After", "1"), nil, "/issues", 429, "forge_rate_limited", "rate limit", "2", 2},
		{"429 without retry-after", answer(429, "API rate limit exceeded"), nil, "/issues", 429, "forge_rate_limited", "", "", 1},
		{"429 too long to wait", answer(429, "API rate limit exceeded", "Retry-After", "300"), nil, "/issues", 429, "forge_rate_limited", "", "301", 1},
		{"403 with no requests left", answer(403, "exceeded", "X-RateLimit-Remaining", "0", "Retry-After", "400"), nil, "/issues", 429, "forge_rate_limited", "", "401", 1},
		{"409", answer(409, "the forge says no"), nil, "/issues", 422, "forge_rejected", "says no", "", 1},
		{"503", answer(503, "unavailable"), nil, "/issues/1", 502, "forge_error", "unavailable", "", 1},
		{"garbage", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>") }, nil, "/issues/1", 502, "forge_error", "decoding response", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := widgetsEnv(t, noSleep)
			e.stub.setHandler(tc.handler)
			if tc.auth != nil {
				e.originAuth = tc.auth
			}
			rec := e.get("/workspaces/widgets"+tc.path, apiKeyAuth())
			got := testutil.Error(t, rec)
			if rec.Code != tc.status || got.ErrorType != tc.errorType || !strings.Contains(got.Message, tc.text) {
				t.Errorf("%d %+v; want %d %s carrying %q", rec.Code, got, tc.status, tc.errorType, tc.text)
			}
			if h := rec.Header().Get("Retry-After"); h != tc.retryAfter {
				t.Errorf("Retry-After = %q; want %q", h, tc.retryAfter)
			}
			if tc.auth == nil && strings.Contains(got.Message, "GIT_PA") {
				t.Errorf("a PAT got the basic hint: %q", got.Message)
			}
			if n := e.stub.count(); n != tc.requests {
				t.Errorf("the forge saw %d requests; want %d", n, tc.requests)
			}
		})
	}

	for _, tc := range []struct {
		name string
		api  func(e *env) string
	}{
		{"a closed port", func(*env) string {
			l, _ := net.Listen("tcp", "127.0.0.1:0")
			l.Close()
			return "https://" + l.Addr().String()
		}},
		{"a name that does not resolve", func(*env) string { return "https://forge.invalid" }},
	} {
		e := newEnv(t)
		addWorkspace(t, e.db, "widgets")
		e.setVar("widgets", "FORGE_API_URL", tc.api(e))
		rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
		if rec.Code != http.StatusBadGateway || testutil.Error(t, rec).ErrorType != "forge_unreachable" {
			t.Errorf("%s: %d %s; want 502 forge_unreachable", tc.name, rec.Code, rec.Body)
		}
	}
	untrusted := widgetsEnv(t, func(cfg *Config) { cfg.Transport = nil })
	if rec := untrusted.get("/workspaces/widgets/forge/repository", apiKeyAuth()); rec.Code != http.StatusBadGateway || untrusted.stub.count() != 0 {
		t.Errorf("a certificate the transport does not trust: %d after %d requests; want 502 and none", rec.Code, untrusted.stub.count())
	}
}

// A 5xx or 429 answer logs one line with the slug, the target, the operation and the
// forge's status, and no message, token or body; a 4xx is not logged.
func TestForgeErrors_FailureLogLine(t *testing.T) {
	lines := func(log *syncBuffer) []string {
		var out []string
		for _, line := range strings.Split(log.String(), "\n") {
			if strings.Contains(line, "forge: request failed") {
				out = append(out, line)
			}
		}
		return out
	}
	for _, tc := range []struct {
		handler        http.HandlerFunc
		path, forgeSts string
		want           []string
	}{
		{answer(500, "boom origin-token BODYTEXT"), "/issues", "500", []string{"slug=widgets", "target=origin", "operation=ListIssues", "forge_status=500"}},
		{answer(429, "slow origin-token BODYTEXT"), "/pulls/3/state?target=upstream", "429", []string{"target=upstream", "operation=GetPRState", "forge_status=429"}},
		{answer(404, "gone origin-token BODYTEXT"), "/issues/1", "", nil},
	} {
		log := captureLog(t)
		e := newEnv(t)
		e.stub.setHandler(tc.handler)
		addWorkspace(t, e.db, "widgets", carryPatch("https://github.com/up/widgets"))
		e.setVar("widgets", "FORGE_API_URL", e.stub.URL)
		e.setVar("widgets", "UPSTREAM_FORGE_API_URL", e.stub.URL)
		e.get("/workspaces/widgets"+tc.path, apiKeyAuth())
		got := lines(log)
		if tc.want == nil {
			if len(got) != 0 {
				t.Errorf("%s: a 4xx was logged: %v", tc.path, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%s: %d log lines; want 1:\n%s", tc.path, len(got), log)
		}
		for _, want := range tc.want {
			if !strings.Contains(got[0], want) {
				t.Errorf("%s: the line lacks %q: %s", tc.path, want, got[0])
			}
		}
		for _, leaked := range []string{"origin-token", "BODYTEXT", "boom", "slow"} {
			if strings.Contains(got[0], leaked) {
				t.Errorf("%s: the line holds %q: %s", tc.path, leaked, got[0])
			}
		}
	}
}

// No answer and no log line of any error class holds the token or URL userinfo, however
// the forge or the store echoes them, and the forge's message is shown redacted.
func TestForgeErrors_NoSecretInAnswersOrLogs(t *testing.T) {
	const token = "origin-token"
	leaks := func(s string) bool {
		return strings.Contains(s, token) || strings.Contains(s, "s3cr3t") || strings.Contains(s, "user:")
	}
	log := captureLog(t)
	rc := &recordingClient{}
	e := newEnv(t, useClient(rc, nil))
	addWorkspace(t, e.db, "widgets")
	addWorkspace(t, e.db, "tokenurl", withGitURL("https://user:s3cr3t@git.internal.example/acme/widgets"))
	classes := map[string]error{
		"404":         fmt.Errorf("%w: %w", issuex.ErrNotFound, httpError(404, token)),
		"401":         httpError(401, token),
		"429":         fmt.Errorf("%w: %w", issuex.ErrRateLimited, httpError(429, token)),
		"500":         httpError(500, token),
		"unreachable": &url.Error{Op: "Get", URL: "https://user:s3cr3t@forge.example/x?token=" + token, Err: &net.OpError{Op: "dial", Err: errors.New("refused for " + token)}},
		"redirect":    &url.Error{Op: "Get", URL: "https://user:s3cr3t@forge.example/x", Err: fmt.Errorf("%w to https://user:s3cr3t@other/%s", errRedirectRefused, token)},
		"garbage":     fmt.Errorf("GET /x: decoding response: %s https://user:s3cr3t@h/", token),
	}
	for class, err := range classes {
		rc.err = err
		for _, r := range readRoutes[1:] {
			rec := e.sendRoute(r, "widgets", apiKeyAuth())
			if rec.Code < 400 || leaks(rec.Body.String()) {
				t.Errorf("%s %s: %d %s; want an error without the secrets", class, r.name, rec.Code, rec.Body)
			}
			if class[0] == '4' || class[0] == '5' {
				if !strings.Contains(rec.Body.String(), "[redacted]") {
					t.Errorf("%s %s: the forge's message is not carried redacted: %s", class, r.name, rec.Body)
				}
			}
		}
	}
	rc.err = nil
	e.originErr = errors.New("store: " + token + " https://user:s3cr3t@x/")
	for _, path := range []string{"/workspaces/tokenurl/issues/1", "/workspaces/widgets/issues/abc", "/workspaces/widgets/issues"} {
		if body := e.get(path, apiKeyAuth()).Body.String(); leaks(body) {
			t.Errorf("%s: the answer holds a secret: %s", path, body)
		}
	}
	if leaks(log.String()) {
		t.Errorf("the log holds a secret:\n%s", log)
	}
}

// An issue whose comments could not be read is answered 200 with the forge's error for
// them, scrubbed of the token and the userinfo.
func TestReadIssue_CommentsErrorIsScrubbed(t *testing.T) {
	e := widgetsEnv(t)
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comments") {
			answer(500, "comments broke for origin-token https://user:s3cr3t@forge.example/x")(w, r)
			return
		}
		respond(http.StatusOK, issueBody(1, "t", ""))(w, r)
	})
	rec := e.get("/workspaces/widgets/issues/1", apiKeyAuth())
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"comments_err"`) || !strings.Contains(body, "[redacted]") {
		t.Fatalf("%d %s; want 200 with the redacted comments error", rec.Code, body)
	}
	if strings.Contains(body, "origin-token") || strings.Contains(body, "s3cr3t") || strings.Contains(body, "user:") {
		t.Errorf("the body holds a secret: %s", body)
	}
	if got := scrubbedCommentsErr(errors.New("plain origin-token"), "origin-token").Error(); got != "plain [redacted]" {
		t.Errorf("a plain error became %q; want it scrubbed", got)
	}
}
