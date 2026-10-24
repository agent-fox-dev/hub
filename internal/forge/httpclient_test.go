package forge

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// Every request to the forge goes through Config.Transport, carries hub's user agent of
// the moment, and carries nothing of the caller: not its bearer, cookie, body or headers.
func TestHTTPClient_UserAgentAndNothingOfTheCaller(t *testing.T) {
	prev := apikit.Version
	apikit.Version = "1.2.3"
	t.Cleanup(func() { apikit.Version = prev })
	e := widgetsEnv(t)
	e.stub.setHandler(respond(http.StatusOK, repositoryBody))
	header := http.Header{"Authorization": {"Bearer CALLER-TOKEN"}, "Private-Token": {"CALLER-TOKEN"}, "Cookie": {"session=CALLER"}, "X-Extra": {"CALLER-EXTRA"}}
	if rec := e.do(http.MethodGet, "/workspaces/widgets/forge/repository", apiKeyAuth(), `{"caller":"BODY"}`, header); rec.Code != http.StatusOK {
		t.Fatalf("status = %d\n%s", rec.Code, rec.Body)
	}
	apikit.Version = "9.9.9"
	e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	reqs := e.stub.received()
	if len(reqs) != 2 || reqs[0].Header.Get("User-Agent") != "af-hub/1.2.3" || reqs[1].Header.Get("User-Agent") != "af-hub/9.9.9" {
		t.Errorf("the stub saw %d requests with the agents %q, %q; want af-hub/1.2.3 then af-hub/9.9.9", len(reqs), reqs[0].Header.Get("User-Agent"), reqs[len(reqs)-1].Header.Get("User-Agent"))
	}
	if dump := fmt.Sprint(reqs[0].Header, reqs[0].Body, reqs[0].Query); strings.Contains(dump, "CALLER") || strings.Contains(dump, "BODY") || bearer(reqs[0]) != "origin-token" {
		t.Errorf("the request to the forge carries what the caller sent: %s", dump)
	}

	counting := &countingTransport{}
	c := newEnv(t, func(cfg *Config) { cfg.Transport = counting })
	addWorkspace(t, c.db, "widgets")
	c.setVar("widgets", "FORGE_API_URL", "https://api.example.test")
	if c.get("/workspaces/widgets/forge/repository", apiKeyAuth()); counting.n.Load() != 1 {
		t.Errorf("Config.Transport saw %d requests; want 1", counting.n.Load())
	}
}

// Without Config.Transport the client uses a clone of the default transport, which
// follows the environment's proxy settings; the clone is not the default itself.
func TestHTTPClient_DefaultTransportIsAClone(t *testing.T) {
	ft, ok := newHTTPClient(nil).Transport.(forgeTransport)
	if !ok {
		t.Fatalf("the transport is %T; want the forge transport", newHTTPClient(nil).Transport)
	}
	base, ok := ft.base.(*http.Transport)
	if !ok || base == http.DefaultTransport || base.Proxy == nil {
		t.Errorf("the base is %T (the default itself: %v); want a clone with the proxy settings", ft.base, ft.base == http.DefaultTransport)
	}
}

// The transport sends a copy of the request, so the one the client may reuse keeps its
// headers, and strips the token headers when the workspace has no credential.
func TestHTTPClient_TransportSendsACopy(t *testing.T) {
	var sent *http.Request
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent = r
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
	})
	req, _ := http.NewRequestWithContext(withCredential(t.Context(), ""), http.MethodGet, "https://api.example.test/x", nil)
	req.Header.Set("User-Agent", "agent-fox")
	req.Header.Set("Authorization", "Bearer env-token")
	resp, err := forgeTransport{base: base}.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if sent == req || req.Header.Get("User-Agent") != "agent-fox" || req.Header.Get("Authorization") != "Bearer env-token" {
		t.Error("the caller's request was changed or sent itself")
	}
	if sent.Header.Get("Authorization") != "" || !strings.HasPrefix(sent.Header.Get("User-Agent"), "af-hub/") {
		t.Errorf("the request sent has %v", sent.Header)
	}
}

// The redirect policy follows a redirect to the same host and scheme, up to ten hops, and
// refuses any other; through a route the refusal is 502 forge_error and the other host
// receives nothing, GitLab's private token included.
func TestHTTPClient_RedirectPolicy(t *testing.T) {
	hop := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	first := hop("https://api.example.test/repos/a/b")
	via := func(n int) []*http.Request {
		out := make([]*http.Request, n)
		for i := range out {
			out[i] = first
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		to      string
		via     []*http.Request
		refused bool
	}{
		{"same host and scheme", "https://api.example.test/renamed", via(1), false},
		{"same host, other case", "https://API.example.test/x", via(1), false},
		{"another host", "https://other.example.test/x", via(1), true},
		{"another port", "https://api.example.test:8443/x", via(1), true},
		{"plain http", "http://api.example.test/x", via(1), true},
		{"the tenth hop", "https://api.example.test/x", via(9), false},
		{"the eleventh hop", "https://api.example.test/x", via(10), true},
	} {
		err := refuseCrossHost(hop(tc.to), tc.via)
		if (err != nil) != tc.refused || (err != nil && !errors.Is(err, errRedirectRefused)) {
			t.Errorf("%s: refuseCrossHost = %v; refused should be %v", tc.name, err, tc.refused)
		}
	}

	other := newForgeStub(t)
	for _, tc := range []struct{ gitURL, api string }{
		{"https://github.com/acme/widgets", ""},
		{"https://gitlab.example.com/group/widgets", "/gitlab/api/v4"},
	} {
		e := newEnv(t)
		e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
		})
		addWorkspace(t, e.db, "widgets", withGitURL(tc.gitURL))
		e.setVar("widgets", "FORGE_API_URL", e.stub.URL+tc.api)
		rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
		if rec.Code != http.StatusBadGateway || testutil.Error(t, rec).ErrorType != "forge_error" || e.stub.count() != 1 {
			t.Errorf("%s: %d %s after %d requests; want 502 forge_error after one", tc.gitURL, rec.Code, rec.Body, e.stub.count())
		}
	}
	if n := other.count(); n != 0 {
		t.Errorf("the other host received %d requests; want none", n)
	}
}

// A call that outlives the deadline is 504 forge_timeout, its outbound request is
// cancelled, and issuex's wait for a rate limit runs inside the same deadline.
func TestHTTPClient_CallDeadline(t *testing.T) {
	if defaultCallDeadline != 30*time.Second {
		t.Errorf("the default deadline is %v; want 30s", defaultCallDeadline)
	}
	shortDeadline(t, 50*time.Millisecond)
	var cancelled atomic.Bool
	e := widgetsEnv(t)
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			cancelled.Store(true)
		case <-time.After(3 * time.Second):
		}
	})
	start := time.Now()
	rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	if rec.Code != http.StatusGatewayTimeout || testutil.Error(t, rec).ErrorType != "forge_timeout" || time.Since(start) > 2*time.Second {
		t.Fatalf("%d %s after %v; want 504 forge_timeout within the deadline", rec.Code, rec.Body, time.Since(start))
	}
	eventually(t, "the cancellation of the outbound request", cancelled.Load)

	e.stub.setHandler(answer(429, "slow down", "Retry-After", "60"))
	if rec := e.get("/workspaces/widgets/issues", apiKeyAuth()); rec.Code != http.StatusGatewayTimeout {
		t.Errorf("a rate limit waited out past the deadline: %d %s; want 504", rec.Code, rec.Body)
	}
}
