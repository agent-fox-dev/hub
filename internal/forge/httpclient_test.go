package forge

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/txsvc/apikit"
)

// TS-30-21: The caller's Authorization header and body never reach the forge.
// Requirement: 30-REQ-4.7
func TestCallerCredentialsNeverReachTheForge_TS_30_21(t *testing.T) {
	e := githubEnv(t)
	rng := rand.New(rand.NewPCG(30, 21))
	random := func(prefix string) string {
		const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~"
		b := make([]byte, 16+rng.IntN(40))
		for i := range b {
			b[i] = alphabet[rng.IntN(len(alphabet))]
		}
		return prefix + string(b)
	}

	for i := range 60 {
		callerToken, callerBody := random("BEARER-"), random("BODY-")
		extra := random("EXTRA-")
		header := http.Header{
			"Authorization":   {"Bearer " + callerToken},
			"Private-Token":   {callerToken},
			"X-Extra":         {extra},
			"Cookie":          {"session=" + callerToken},
			"X-Forwarded-For": {extra},
		}
		path := "/workspaces/widgets" + readPaths[rng.IntN(len(readPaths))]
		if rec := e.request(path, apiKeyAuth(), header, callerBody); rec.Code != http.StatusOK {
			t.Fatalf("%d: %s: status = %d; want 200\n%s", i, path, rec.Code, rec.Body)
		}
	}

	reqs := e.stub.received()
	if len(reqs) == 0 {
		t.Fatal("the stub received no request")
	}
	for _, r := range reqs {
		dump := fmt.Sprint(r.Header)
		if strings.Contains(dump, "BEARER-") || strings.Contains(dump, "EXTRA-") || strings.Contains(r.Body, "BODY-") || strings.Contains(r.Query, "BEARER-") {
			t.Fatalf("a request to the forge carries what the caller sent: %+v", r)
		}
		if got := bearer(r); got != "origin-token" {
			t.Fatalf("a request to the forge carries the token %q; want the workspace's", got)
		}
		if r.Header.Get("Cookie") != "" {
			t.Fatalf("a request to the forge carries a cookie: %v", r.Header)
		}
	}
}

// TS-30-22: Requests go through Config.Transport when it is set.
// Requirement: 30-REQ-5.1
func TestConfigTransportIsUsed_TS_30_22(t *testing.T) {
	counting := &countingTransport{}
	e := newEnv(t, func(cfg *Config) { cfg.Transport = counting })
	addWorkspace(t, e.db, "widgets")
	e.setVar("widgets", "FORGE_API_URL", "https://api.example.test")

	e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	if n := counting.n.Load(); n != 1 {
		t.Errorf("the transport saw %d requests; want 1", n)
	}
}

// Without Config.Transport the client's transport is a clone of the default one, so that
// it follows the environment's proxy settings as hub's git transport does, and a change to
// the default does not reach it.
// Requirement: 30-REQ-5.1
func TestDefaultTransportIsAClone(t *testing.T) {
	client := newHTTPClient(nil)
	ft, ok := client.Transport.(forgeTransport)
	if !ok {
		t.Fatalf("the client's transport is %T; want the forge transport", client.Transport)
	}
	base, ok := ft.base.(*http.Transport)
	if !ok || base == http.DefaultTransport {
		t.Fatalf("the base transport is %T (the default itself: %v); want a clone of it", ft.base, ft.base == http.DefaultTransport)
	}
	if base.Proxy == nil {
		t.Error("the clone does not follow the environment's proxy settings")
	}
	if newHTTPClient(nil) == client {
		t.Error("newHTTPClient returned the same client twice")
	}
}

// TS-30-23: Every outbound request carries User-Agent af-hub/<apikit.Version>.
// Requirement: 30-REQ-5.2
func TestUserAgent_TS_30_23(t *testing.T) {
	prev := apikit.Version
	apikit.Version = "1.2.3"
	t.Cleanup(func() { apikit.Version = prev })

	e := githubEnv(t)
	for _, path := range readPaths {
		if rec := e.get("/workspaces/widgets"+path, apiKeyAuth()); rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d; want 200\n%s", path, rec.Code, rec.Body)
		}
	}
	reqs := e.stub.received()
	if len(reqs) < len(readPaths)+2 {
		t.Fatalf("the stub received %d requests; want the several the routes make", len(reqs))
	}
	for _, r := range reqs {
		if got := r.Header.Get("User-Agent"); got != "af-hub/1.2.3" {
			t.Errorf("%s %s: User-Agent = %q; want af-hub/1.2.3", r.Method, r.Path, got)
		}
	}

	// The version is read for each request: a change is seen by the next one.
	apikit.Version = "9.9.9"
	before := e.stub.count()
	e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	if reqs := e.stub.received(); reqs[before].Header.Get("User-Agent") != "af-hub/9.9.9" {
		t.Errorf("User-Agent = %q after the version changed; want af-hub/9.9.9", reqs[before].Header.Get("User-Agent"))
	}
}

// TS-30-24: A redirect to another host is refused and the other host receives nothing.
// Requirement: 30-REQ-5.3
func TestCrossHostRedirectIsRefused_TS_30_24(t *testing.T) {
	other := newForgeStub(t)
	other.answerRepository()
	e := failingEnv(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	})

	rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	if rec.Code != http.StatusBadGateway || decodeError(t, rec).ErrorType != "forge_error" {
		t.Errorf("status %d, body %s; want 502 forge_error", rec.Code, rec.Body)
	}
	if reqs := other.received(); len(reqs) != 0 {
		t.Errorf("the other host received %d requests, the first with the token %q; want none", len(reqs), bearer(reqs[0]))
	}
	if n := e.stub.count(); n != 1 {
		t.Errorf("the forge saw %d requests; want the one that was redirected", n)
	}

	// A GitLab token is not one Go removes on a redirect, so the refusal is what keeps it.
	e = newEnv(t)
	other.setHandler(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "{}") })
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	addWorkspace(t, e.db, "widgets", func(r *workspaceRow) { r.gitURL = "https://gitlab.example.com/group/widgets" })
	e.setVar("widgets", "FORGE_API_URL", e.stub.URL+"/gitlab/api/v4")
	if rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth()); rec.Code != http.StatusBadGateway {
		t.Errorf("gitlab: status = %d; want 502\n%s", rec.Code, rec.Body)
	}
	if n := len(other.received()); n != 0 {
		t.Errorf("gitlab: the other host received %d requests; want none", n)
	}
}

// A redirect on the same host and scheme is followed, up to ten hops; a change of host,
// port or scheme is refused, and so is the eleventh hop.
// Requirement: 30-REQ-5.3
func TestRedirectPolicy(t *testing.T) {
	hop := func(rawURL string) *http.Request {
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Request{URL: u}
	}
	first := hop("https://api.example.test/repos/a/b")
	cases := []struct {
		name    string
		to      string
		via     []*http.Request
		refused bool
	}{
		{"same host and scheme", "https://api.example.test/repos/a/renamed", []*http.Request{first}, false},
		{"same host, other case", "https://API.example.test/x", []*http.Request{first}, false},
		{"another host", "https://other.example.test/x", []*http.Request{first}, true},
		{"a subdomain", "https://evil.api.example.test/x", []*http.Request{first}, true},
		{"another port", "https://api.example.test:8443/x", []*http.Request{first}, true},
		{"plain http", "http://api.example.test/x", []*http.Request{first}, true},
		{"the tenth hop", "https://api.example.test/x", append(make([]*http.Request, 0), repeat(first, 9)...), false},
		{"the eleventh hop", "https://api.example.test/x", repeat(first, 10), true},
		{"checked against the first request", "https://api.example.test/x", []*http.Request{first, hop("https://api.example.test/y"), hop("https://api.example.test/z")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseCrossHost(hop(tc.to), tc.via)
			if (err != nil) != tc.refused {
				t.Fatalf("refuseCrossHost = %v; refused should be %v", err, tc.refused)
			}
			if err != nil && !errors.Is(err, errRedirectRefused) {
				t.Errorf("the error %v does not match errRedirectRefused", err)
			}
		})
	}
}

func repeat(r *http.Request, n int) []*http.Request {
	out := make([]*http.Request, n)
	for i := range out {
		out[i] = r
	}
	return out
}

// TS-30-25: A forge call that outlives the deadline answers 504 forge_timeout.
// Requirement: 30-REQ-5.4
func TestCallDeadline_TS_30_25(t *testing.T) {
	if defaultCallDeadline != 30*time.Second {
		t.Errorf("the default deadline is %v; want 30s", defaultCallDeadline)
	}
	prev := callDeadline
	callDeadline = 50 * time.Millisecond
	t.Cleanup(func() { callDeadline = prev })

	var cancelled atomic.Bool
	e := failingEnv(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			cancelled.Store(true)
		case <-time.After(3 * time.Second):
		}
	})

	start := time.Now()
	rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	if rec.Code != http.StatusGatewayTimeout || decodeError(t, rec).ErrorType != "forge_timeout" {
		t.Fatalf("status %d, body %s; want 504 forge_timeout", rec.Code, rec.Body)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the answer took %v; the deadline is 50ms", took)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !cancelled.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !cancelled.Load() {
		t.Error("the outbound request's context was not cancelled")
	}
}

// issuex's retries run inside the deadline: a rate limit it waits out for longer than the
// deadline ends as a timeout.
// Requirement: 30-REQ-5.4
func TestDeadlineCoversTheRateLimitWait(t *testing.T) {
	prev := callDeadline
	callDeadline = 100 * time.Millisecond
	t.Cleanup(func() { callDeadline = prev })

	e := failingEnv(t, answer(429, "slow down", "Retry-After", "60"))
	start := time.Now()
	rec := e.get("/workspaces/widgets/issues", apiKeyAuth())
	if rec.Code != http.StatusGatewayTimeout || decodeError(t, rec).ErrorType != "forge_timeout" {
		t.Errorf("status %d, body %s; want 504 forge_timeout", rec.Code, rec.Body)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the answer took %v; issuex waited out the rate limit past the deadline", took)
	}
}

// A workspace without a credential sends no token: issuex falls back to the token in the
// hub's own environment when it is given none, and that one belongs to the hub, not to the
// workspace. A workspace that has a credential sends its own.
// Requirement: 30-REQ-4.5
func TestNoCredentialSendsNoEnvironmentToken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gitURL string
		api    string
		env    map[string]string
	}{
		{"github", "https://github.com/acme/widgets", "", map[string]string{"GITHUB_TOKEN": "env-github", "GH_TOKEN": "env-gh"}},
		{"gitlab", "https://gitlab.example.com/group/widgets", "/gitlab/api/v4", map[string]string{"GITLAB_TOKEN": "env-gitlab"}},
	} {
		for _, withCredential := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/credential=%v", tc.name, withCredential), func(t *testing.T) {
				e := newEnv(t)
				for k, v := range tc.env {
					t.Setenv(k, v)
				}
				e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, githubRepositoryBody) })
				if !withCredential {
					e.originAuth = nil
				}
				addWorkspace(t, e.db, "widgets", func(r *workspaceRow) { r.gitURL = tc.gitURL })
				e.setVar("widgets", "FORGE_API_URL", e.stub.URL+tc.api)

				if rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth()); rec.Code != http.StatusOK {
					t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
				}
				reqs := e.stub.received()
				if len(reqs) != 1 {
					t.Fatalf("the stub received %d requests; want 1", len(reqs))
				}
				want := ""
				if withCredential {
					want = "origin-token"
				}
				if got := bearer(reqs[0]); got != want {
					t.Errorf("the request carried the token %q; want %q (headers %v)", got, want, reqs[0].Header)
				}
			})
		}
	}
}

// The transport sends a copy: the request it was given, which the client may reuse for a
// redirect or a retry, keeps its own headers.
// Requirement: 30-REQ-5.2
func TestForgeTransportDoesNotChangeTheRequest(t *testing.T) {
	var sent *http.Request
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent = r
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
	})
	req, err := http.NewRequestWithContext(withCredential(t.Context(), ""), http.MethodGet, "https://api.example.test/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "agent-fox")
	req.Header.Set("Authorization", "Bearer env-token")

	resp, err := forgeTransport{base: base}.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sent == req {
		t.Error("the base transport was given the caller's request itself")
	}
	if got := req.Header.Get("User-Agent"); got != "agent-fox" {
		t.Errorf("the caller's request now has User-Agent %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer env-token" {
		t.Errorf("the caller's request now has Authorization %q", got)
	}
	if sent.Header.Get("Authorization") != "" || !strings.HasPrefix(sent.Header.Get("User-Agent"), "af-hub/") {
		t.Errorf("the request sent has %v", sent.Header)
	}
}

// roundTripFunc is a RoundTripper made of a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
