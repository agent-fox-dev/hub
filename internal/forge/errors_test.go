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
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/aflib/issuex"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// failingEnv is an environment whose workspace "widgets" reads the forge stub, which
// answers with h.
func failingEnv(t *testing.T, h http.HandlerFunc, mods ...func(*Config)) *env {
	t.Helper()
	e := newEnv(t, mods...)
	e.stub.setHandler(h)
	addWorkspace(t, e.db, "widgets")
	e.setVar("widgets", "FORGE_API_URL", e.stub.URL)
	return e
}

// answer makes the stub answer every request with the status and a GitHub error body
// carrying the message.
func answer(status int, message string, headers ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"message":%q}`, message)
	}
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

// TS-30-38: A forge 404 maps to 404 forge_not_found.
// Requirement: 30-REQ-8.1
func TestForge404_TS_30_38(t *testing.T) {
	e := failingEnv(t, answer(404, "Not Found"))
	rec := e.get("/workspaces/widgets/issues/9", apiKeyAuth())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404\n%s", rec.Code, rec.Body)
	}
	body := decodeError(t, rec)
	if body.ErrorType != "forge_not_found" || !strings.Contains(body.Message, "Not Found") {
		t.Errorf("error = %+v; want forge_not_found carrying the forge's message", body)
	}
}

// TS-30-39: A forge 401 maps to 502 forge_unauthenticated with a token hint for a basic
// credential.
// Requirement: 30-REQ-8.2
func TestForge401_TS_30_39(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth *githttp.BasicAuth
		hint bool
	}{
		{"pat", &githttp.BasicAuth{Username: patUsername, Password: "pat-token"}, false},
		{"basic", &githttp.BasicAuth{Username: "alice", Password: "basic-password"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := failingEnv(t, answer(401, "Bad credentials"))
			e.originAuth = tc.auth
			rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d; want 502\n%s", rec.Code, rec.Body)
			}
			body := decodeError(t, rec)
			if body.ErrorType != "forge_unauthenticated" || !strings.Contains(body.Message, "Bad credentials") {
				t.Errorf("error = %+v; want forge_unauthenticated carrying the forge's message", body)
			}
			hasHint := strings.Contains(body.Message, "GIT_PAT") && strings.Contains(body.Message, "GIT_PASSWORD")
			if hasHint != tc.hint || (!tc.hint && strings.Contains(body.Message, "GIT_PA")) {
				t.Errorf("message %q: hint = %v; want %v", body.Message, hasHint, tc.hint)
			}
		})
	}
}

// TS-30-40: A forge 403 that is not a rate limit maps to 502 forge_forbidden.
// Requirement: 30-REQ-8.3
func TestForge403_TS_30_40(t *testing.T) {
	e := failingEnv(t, answer(403, "Resource not accessible by personal access token"))
	rec := e.get("/workspaces/widgets/pulls/3", apiKeyAuth())
	if rec.Code != http.StatusBadGateway || decodeError(t, rec).ErrorType != "forge_forbidden" {
		t.Errorf("status %d, body %s; want 502 forge_forbidden", rec.Code, rec.Body)
	}
}

// TS-30-41: A rate limit issuex cannot wait out maps to 429 with Retry-After rounded up.
// Requirement: 30-REQ-8.4
func TestForgeRateLimit_TS_30_41(t *testing.T) {
	t.Run("retry-after known", func(t *testing.T) {
		// Retry-After: 1 makes issuex wait 2 seconds (it adds one) and try once more; the
		// forge limits again, and the answer carries the 2 seconds.
		e := failingEnv(t, answer(429, "API rate limit exceeded", "Retry-After", "1"), noSleep)
		rec := e.get("/workspaces/widgets/issues", apiKeyAuth())
		if rec.Code != http.StatusTooManyRequests || decodeError(t, rec).ErrorType != "forge_rate_limited" {
			t.Fatalf("status %d, body %s; want 429 forge_rate_limited", rec.Code, rec.Body)
		}
		if got := rec.Header().Get("Retry-After"); got != "2" {
			t.Errorf("Retry-After = %q; want 2", got)
		}
		if n := e.stub.count(); n != 2 {
			t.Errorf("the forge saw %d requests; want 2 (issuex waits once and tries again)", n)
		}
	})
	t.Run("retry-after unknown", func(t *testing.T) {
		e := failingEnv(t, answer(429, "API rate limit exceeded"))
		rec := e.get("/workspaces/widgets/issues", apiKeyAuth())
		if rec.Code != http.StatusTooManyRequests || decodeError(t, rec).ErrorType != "forge_rate_limited" {
			t.Fatalf("status %d, body %s; want 429 forge_rate_limited", rec.Code, rec.Body)
		}
		if _, present := rec.Header()["Retry-After"]; present {
			t.Errorf("Retry-After = %q; want none", rec.Header().Get("Retry-After"))
		}
	})
	t.Run("a backoff too long to wait", func(t *testing.T) {
		e := failingEnv(t, answer(429, "API rate limit exceeded", "Retry-After", "300"))
		rec := e.get("/workspaces/widgets/issues", apiKeyAuth())
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "301" {
			t.Errorf("status %d, Retry-After %q; want 429 and 301", rec.Code, rec.Header().Get("Retry-After"))
		}
		if n := e.stub.count(); n != 1 {
			t.Errorf("the forge saw %d requests; want 1", n)
		}
	})
	t.Run("a 403 with no requests left", func(t *testing.T) {
		e := failingEnv(t, answer(403, "API rate limit exceeded", "X-RateLimit-Remaining", "0", "Retry-After", "400"))
		rec := e.get("/workspaces/widgets/issues", apiKeyAuth())
		if rec.Code != http.StatusTooManyRequests || decodeError(t, rec).ErrorType != "forge_rate_limited" || rec.Header().Get("Retry-After") != "401" {
			t.Errorf("status %d, Retry-After %q, body %s; want 429 forge_rate_limited and 401", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
		}
	})
}

// TS-30-42: Other forge 4xx statuses map to 422 forge_rejected.
// Requirement: 30-REQ-8.5
func TestForge4xx_TS_30_42(t *testing.T) {
	for _, code := range []int{400, 405, 409, 410, 418, 422} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			e := failingEnv(t, answer(code, "the forge says no"))
			rec := e.get("/workspaces/widgets/issues", apiKeyAuth())
			if rec.Code != http.StatusUnprocessableEntity || decodeError(t, rec).ErrorType != "forge_rejected" {
				t.Errorf("status %d, body %s; want 422 forge_rejected", rec.Code, rec.Body)
			}
		})
	}
}

// TS-30-43: A forge 5xx or an unreadable response maps to 502 forge_error.
// Requirement: 30-REQ-8.6
func TestForge5xxAndGarbage_TS_30_43(t *testing.T) {
	garbage := func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>not json</html>") }
	for name, tc := range map[string]struct {
		h    http.HandlerFunc
		text string
	}{
		"500":        {answer(500, "boom"), "boom"},
		"503":        {answer(503, "unavailable"), "unavailable"},
		"502":        {answer(502, "bad gateway"), "bad gateway"},
		"garbage200": {garbage, "decoding response"},
	} {
		t.Run(name, func(t *testing.T) {
			e := failingEnv(t, tc.h)
			rec := e.get("/workspaces/widgets/issues/1", apiKeyAuth())
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d; want 502\n%s", rec.Code, rec.Body)
			}
			body := decodeError(t, rec)
			if body.ErrorType != "forge_error" || !strings.Contains(body.Message, tc.text) {
				t.Errorf("error = %+v; want forge_error saying %q", body, tc.text)
			}
		})
	}
}

// TS-30-44: A connection failure maps to 502 forge_unreachable.
// Requirement: 30-REQ-8.7
func TestConnectionFailure_TS_30_44(t *testing.T) {
	t.Run("closed port", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		e := newEnv(t)
		addWorkspace(t, e.db, "widgets")
		e.setVar("widgets", "FORGE_API_URL", "https://"+addr)
		rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
		if rec.Code != http.StatusBadGateway || decodeError(t, rec).ErrorType != "forge_unreachable" {
			t.Errorf("status %d, body %s; want 502 forge_unreachable", rec.Code, rec.Body)
		}
	})
	t.Run("certificate the transport does not trust", func(t *testing.T) {
		e := failingEnv(t, answer(200, "ok"), func(cfg *Config) { cfg.Transport = nil })
		rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
		if rec.Code != http.StatusBadGateway || decodeError(t, rec).ErrorType != "forge_unreachable" {
			t.Errorf("status %d, body %s; want 502 forge_unreachable", rec.Code, rec.Body)
		}
		if n := e.stub.count(); n != 0 {
			t.Errorf("the stub saw %d requests through an untrusted handshake", n)
		}
	})
	t.Run("name that does not resolve", func(t *testing.T) {
		e := newEnv(t)
		addWorkspace(t, e.db, "widgets")
		e.setVar("widgets", "FORGE_API_URL", "https://forge.invalid")
		rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
		if rec.Code != http.StatusBadGateway || decodeError(t, rec).ErrorType != "forge_unreachable" {
			t.Errorf("status %d, body %s; want 502 forge_unreachable", rec.Code, rec.Body)
		}
	})
}

// TS-30-46: A 5xx or 429 answer logs one line with slug, target, operation and forge
// status and no secrets or bodies.
// Requirement: 30-REQ-9.2
func TestFailureLogLine_TS_30_46(t *testing.T) {
	const token = "origin-token"
	forgeLines := func(log *syncBuffer) []string {
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(log.String()), "\n") {
			if strings.Contains(line, "widgets") {
				out = append(out, line)
			}
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		h      http.HandlerFunc
		status string
	}{
		{"500", answer(500, "boom "+token+" BODYTEXT"), "500"},
		{"429", answer(429, "slow down "+token+" BODYTEXT"), "429"},
		{"503 on a pull request", answer(503, "later "+token+" BODYTEXT"), "503"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := captureLog(t)
			e := failingEnv(t, tc.h)
			rec := e.get("/workspaces/widgets/issues", apiKeyAuth())
			if rec.Code < 429 {
				t.Fatalf("status = %d; want a failure\n%s", rec.Code, rec.Body)
			}
			lines := forgeLines(log)
			if len(lines) != 1 {
				t.Fatalf("%d log lines name the workspace; want exactly one:\n%s", len(lines), log)
			}
			for _, want := range []string{"widgets", "target=origin", "operation=ListIssues", "forge_status=" + tc.status} {
				if !strings.Contains(lines[0], want) {
					t.Errorf("the log line lacks %q: %s", want, lines[0])
				}
			}
			for _, leaked := range []string{token, "BODYTEXT", "boom", "later", "slow down"} {
				if strings.Contains(lines[0], leaked) {
					t.Errorf("the log line holds %q: %s", leaked, lines[0])
				}
			}
		})
	}

	t.Run("a 4xx answer is not logged", func(t *testing.T) {
		log := captureLog(t)
		e := failingEnv(t, answer(404, "Not Found"))
		e.get("/workspaces/widgets/issues/1", apiKeyAuth())
		if lines := forgeLines(log); len(lines) != 0 {
			t.Errorf("a 404 was logged: %v", lines)
		}
	})
	t.Run("the target is named", func(t *testing.T) {
		log := captureLog(t)
		e := newEnv(t)
		e.stub.setHandler(answer(500, "boom"))
		addWorkspace(t, e.db, "widgets", carryPatch("https://github.com/up/widgets"))
		e.setVar("widgets", "UPSTREAM_FORGE_API_URL", e.stub.URL)
		e.get("/workspaces/widgets/pulls/3/state?target=upstream", apiKeyAuth())
		lines := forgeLines(log)
		if len(lines) != 1 || !strings.Contains(lines[0], "target=upstream") || !strings.Contains(lines[0], "operation=GetPRState") {
			t.Errorf("log lines %v; want one naming target=upstream and operation=GetPRState", lines)
		}
	})
}

// httpError is an error of the forge as issuex builds it, with the secrets a hostile
// forge could echo in its message.
func httpError(status int, token string) *issuex.HTTPError {
	return &issuex.HTTPError{
		Method: "GET", Path: "/repos/acme/widgets", Status: status,
		Message: "denied for " + token + " see https://user:s3cr3t@forge.example/docs",
	}
}

// TS-30-47: No response body or log line across all error classes contains the credential
// or userinfo.
// Requirement: 30-REQ-9.3
func TestNoSecretInAnyAnswerOrLog_TS_30_47(t *testing.T) {
	const token = "origin-token"
	userinfo := regexp.MustCompile(`://[^/\s]*:[^/\s]*@`)
	leak := func(s string) string {
		switch {
		case strings.Contains(s, token):
			return "the token"
		case userinfo.MatchString(s):
			return "userinfo"
		case strings.Contains(s, "s3cr3t"):
			return "a password"
		}
		return ""
	}

	classes := map[string]error{
		"404":         fmt.Errorf("%w: %w", issuex.ErrNotFound, httpError(404, token)),
		"401":         httpError(401, token),
		"403":         httpError(403, token),
		"429":         fmt.Errorf("%w: %w", issuex.ErrRateLimited, func() *issuex.HTTPError { e := httpError(429, token); e.RetryAfterHeader = "3"; return e }()),
		"422":         httpError(422, token),
		"500":         httpError(500, token),
		"502":         httpError(502, token),
		"unreachable": &url.Error{Op: "Get", URL: "https://user:s3cr3t@forge.example/x?token=" + token, Err: &net.OpError{Op: "dial", Err: errors.New("refused for " + token)}},
		"untrusted":   &url.Error{Op: "Get", URL: "https://user:s3cr3t@forge.example/x", Err: x509.UnknownAuthorityError{}},
		"timeout":     &url.Error{Op: "Get", URL: "https://user:s3cr3t@forge.example/x?token=" + token, Err: context.DeadlineExceeded},
		"redirect":    &url.Error{Op: "Get", URL: "https://user:s3cr3t@forge.example/x", Err: fmt.Errorf("%w to https://user:s3cr3t@other/%s", errRedirectRefused, token)},
		"garbage":     fmt.Errorf("GET /x: decoding response: invalid character %q in %s https://user:s3cr3t@h/", '<', token),
	}

	log := captureLog(t)
	rc := &recordingClient{}
	e := newEnv(t, useClient(rc, nil))
	addWorkspace(t, e.db, "widgets")
	addWorkspace(t, e.db, "tokenurl", func(r *workspaceRow) { r.gitURL = "https://user:s3cr3t@git.internal.example/acme/widgets" })
	addWorkspace(t, e.db, "badvar")
	e.setVar("badvar", "FORGE_API_URL", "http://user:s3cr3t@"+token+".example")

	check := func(what string, rec interface {
		String() string
	}) {
		t.Helper()
		if l := leak(rec.String()); l != "" {
			t.Errorf("%s: the response holds %s: %s", what, l, rec)
		}
	}
	// These carry the forge's own message, in which the token was: the answer shows it
	// redacted, so the check below is not satisfied by a message that says nothing.
	echoes := map[string]bool{"404": true, "401": true, "403": true, "429": true, "422": true, "500": true, "502": true}
	for class, err := range classes {
		rc.err = err
		for _, path := range readPaths {
			rec := e.get("/workspaces/widgets"+path, apiKeyAuth())
			if rec.Code < 400 {
				t.Errorf("%s %s: status = %d; want an error", class, path, rec.Code)
			}
			check(class+" "+path, rec.Body)
			if echoes[class] && !strings.Contains(rec.Body.String(), "[redacted]") {
				t.Errorf("%s %s: the answer does not carry the forge's message with the token redacted: %s", class, path, rec.Body)
			}
		}
	}
	rc.err = nil
	for _, path := range readPaths {
		check("unparseable n "+path, e.get("/workspaces/widgets"+strings.Replace(path, "/1", "/abc", 1), apiKeyAuth()).Body)
		check("userinfo in git_url "+path, e.get("/workspaces/tokenurl"+path, apiKeyAuth()).Body)
		check("bad variable "+path, e.get("/workspaces/badvar"+path, apiKeyAuth()).Body)
		e.originErr = errors.New("store: " + token + " https://user:s3cr3t@x/")
		check("store failure "+path, e.get("/workspaces/widgets"+path, apiKeyAuth()).Body)
		e.originErr = nil
	}
	if l := leak(log.String()); l != "" {
		t.Errorf("the log holds %s:\n%s", l, log)
	}
}

// ReadIssue answers 200 when the comments fail: the error it carries for them is the
// forge's, and the token and userinfo it could hold are taken out.
// Requirement: 30-REQ-9.3
func TestIssueCommentsErrorIsScrubbed(t *testing.T) {
	const token = "origin-token"
	e := failingEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comments") {
			answer(500, "comments broke for "+token+" https://user:s3cr3t@forge.example/x")(w, r)
			return
		}
		githubFixture(w, r)
	})
	rec := e.get("/workspaces/widgets/issues/1", apiKeyAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"comments_err"`) {
		t.Fatalf("the thread does not carry the comments error: %s", body)
	}
	for _, leaked := range []string{token, "s3cr3t", "user:"} {
		if strings.Contains(body, leaked) {
			t.Errorf("the body holds %q: %s", leaked, body)
		}
	}
	if !strings.Contains(body, "[redacted]") {
		t.Errorf("the body does not show the redaction: %s", body)
	}
}

// retryAfterSeconds rounds up to whole seconds.
// Requirement: 30-REQ-8.4
func TestRetryAfterSeconds(t *testing.T) {
	for d, want := range map[time.Duration]int{
		1200 * time.Millisecond: 2, 2 * time.Second: 2, time.Nanosecond: 1, 999 * time.Millisecond: 1, 61 * time.Second: 61, 0: 0,
	} {
		if got := retryAfterSeconds(d); got != want {
			t.Errorf("retryAfterSeconds(%v) = %d; want %d", d, got, want)
		}
	}
}

// mapForgeError on every class of error issuex and the transport can return.
// Requirement: 30-REQ-8
func TestMapForgeError(t *testing.T) {
	const token = "tok-123456"
	rateLimited := func(h *issuex.HTTPError) error { return fmt.Errorf("%w: %w", issuex.ErrRateLimited, h) }
	cases := []struct {
		name        string
		err         error
		kind        credentialKind
		status      int
		errorType   string
		forgeStatus int
		retryAfter  int
	}{
		{"404", fmt.Errorf("%w: %w", issuex.ErrNotFound, httpError(404, token)), credentialPAT, 404, "forge_not_found", 404, 0},
		{"bare ErrNotFound", issuex.ErrNotFound, credentialPAT, 404, "forge_not_found", 0, 0},
		{"401", httpError(401, token), credentialPAT, 502, "forge_unauthenticated", 401, 0},
		{"403", httpError(403, token), credentialPAT, 502, "forge_forbidden", 403, 0},
		{"429 with retry-after", rateLimited(&issuex.HTTPError{Status: 429, RetryAfterHeader: "5"}), credentialPAT, 429, "forge_rate_limited", 429, 6},
		{"429 without", rateLimited(&issuex.HTTPError{Status: 429}), credentialPAT, 429, "forge_rate_limited", 429, 0},
		{"bare ErrRateLimited", issuex.ErrRateLimited, credentialPAT, 429, "forge_rate_limited", 0, 0},
		{"403 with no requests left", &issuex.HTTPError{Status: 403, RateLimitRemaining: "0", RetryAfterHeader: "9"}, credentialPAT, 429, "forge_rate_limited", 403, 10},
		{"secondary rate limit", &issuex.HTTPError{Status: 403, Message: "You have exceeded a secondary rate limit"}, credentialPAT, 429, "forge_rate_limited", 403, 0},
		{"400", httpError(400, token), credentialPAT, 422, "forge_rejected", 400, 0},
		{"409", fmt.Errorf("%w: %w", issuex.ErrConflict, httpError(409, token)), credentialPAT, 422, "forge_rejected", 409, 0},
		{"bare ErrConflict", issuex.ErrConflict, credentialPAT, 422, "forge_rejected", 0, 0},
		{"500", httpError(500, token), credentialPAT, 502, "forge_error", 500, 0},
		{"599", httpError(599, token), credentialPAT, 502, "forge_error", 599, 0},
		{"unexpected 302", httpError(302, token), credentialPAT, 502, "forge_error", 302, 0},
		{"decode", fmt.Errorf("GET /x: decoding response: %w", errors.New("invalid character '<'")), credentialPAT, 502, "forge_error", 0, 0},
		{"read", fmt.Errorf("GET /x: reading response: %w", io.ErrUnexpectedEOF), credentialPAT, 502, "forge_error", 0, 0},
		{"refused", &url.Error{Op: "Get", URL: "https://h/x", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}, credentialPAT, 502, "forge_unreachable", 0, 0},
		{"dns", fmt.Errorf("GET /x: %w", &url.Error{Op: "Get", URL: "https://h/x", Err: &net.DNSError{Err: "no such host", Name: "h"}}), credentialPAT, 502, "forge_unreachable", 0, 0},
		{"tls", &url.Error{Op: "Get", URL: "https://h/x", Err: x509.HostnameError{Host: "h"}}, credentialPAT, 502, "forge_unreachable", 0, 0},
		{"reset", &url.Error{Op: "Get", URL: "https://h/x", Err: io.EOF}, credentialPAT, 502, "forge_unreachable", 0, 0},
		{"deadline", &url.Error{Op: "Get", URL: "https://h/x", Err: context.DeadlineExceeded}, credentialPAT, 504, "forge_timeout", 0, 0},
		{"deadline during the rate-limit wait", context.DeadlineExceeded, credentialPAT, 504, "forge_timeout", 0, 0},
		{"redirect", &url.Error{Op: "Get", URL: "https://h/x", Err: errRedirectRefused}, credentialPAT, 502, "forge_error", 0, 0},
		{"canceled", &url.Error{Op: "Get", URL: "https://h/x", Err: context.Canceled}, credentialPAT, 502, "forge_error", 0, 0},
		{"something else", errors.New("odd"), credentialPAT, 502, "forge_error", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapForgeError(tc.err, tc.kind, token)
			if got == nil {
				t.Fatal("mapForgeError returned nil")
			}
			if got.status != tc.status || got.errorType != tc.errorType || got.forgeStatus != tc.forgeStatus || got.retryAfter != tc.retryAfter {
				t.Errorf("got status %d %s (forge %d, retry-after %d); want %d %s (forge %d, retry-after %d)",
					got.status, got.errorType, got.forgeStatus, got.retryAfter, tc.status, tc.errorType, tc.forgeStatus, tc.retryAfter)
			}
			if got.message == "" {
				t.Error("the message is empty")
			}
			if strings.Contains(got.message, token) || strings.Contains(got.message, "s3cr3t") {
				t.Errorf("the message holds a secret: %q", got.message)
			}
		})
	}

	// The basic credential's hint is added to a 401 only.
	for _, kind := range []credentialKind{credentialPAT, credentialNone, credentialBasic} {
		for _, status := range []int{401, 403} {
			msg := mapForgeError(httpError(status, token), kind, token).message
			if want := kind == credentialBasic && status == 401; strings.Contains(msg, "GIT_PASSWORD") != want {
				t.Errorf("%s credential, status %d: message %q; hint wanted: %v", kind, status, msg, want)
			}
		}
	}
	// A long message is cut, and the hint of a basic credential is still there.
	long := &issuex.HTTPError{Status: 401, Message: strings.Repeat("é", 2000)}
	msg := mapForgeError(long, credentialBasic, token).message
	if !strings.Contains(msg, "GIT_PAT") || len(msg) > 1024+300 {
		t.Errorf("long 401 message: %d bytes, hint present %v", len(msg), strings.Contains(msg, "GIT_PAT"))
	}
}
