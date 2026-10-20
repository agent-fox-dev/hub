package forge

import (
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

const apiURLRule = "absolute https:// URL"

// TS-30-11: Absent target and target=origin read the repository parsed from git_url
// with the origin resolver and FORGE_API_URL.
// Requirement: 30-REQ-3.1
func TestOriginTargetReadsGitURL_TS_30_11(t *testing.T) {
	e := newEnv(t)
	e.stub.answerRepository()
	addWorkspace(t, e.db, "widgets")
	e.setVar("widgets", "FORGE_API_URL", e.stub.URL)

	for _, query := range []string{"", "?target=origin"} {
		before := e.stub.count()
		rec := e.get("/workspaces/widgets/forge/repository"+query, apiKeyAuth())
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d; want 200\n%s", query, rec.Code, rec.Body)
		}
		var got issuex.Repository
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.FullName != "acme/widgets" {
			t.Errorf("%q: body = %s; want the repository the forge answered", query, rec.Body)
		}
		reqs := e.stub.received()
		if len(reqs) != before+1 {
			t.Fatalf("%q: the stub received %d requests; want one more than %d", query, len(reqs), before)
		}
		if last := reqs[len(reqs)-1]; last.Path != "/repos/acme/widgets" || bearer(last) != "origin-token" {
			t.Errorf("%q: the stub saw %s with token %q; want /repos/acme/widgets with the origin token", query, last.Path, bearer(last))
		}
	}
	if n := e.upstreamCalls.Load(); n != 0 {
		t.Errorf("the upstream resolver was called %d times; want none", n)
	}
}

// TS-30-12: target=upstream on a carry_patch workspace uses upstream_url, the
// upstream credential and UPSTREAM_FORGE_API_URL only.
// Requirement: 30-REQ-3.2
func TestUpstreamTargetUsesUpstreamOnly_TS_30_12(t *testing.T) {
	e := newEnv(t)
	stubA, stubB := e.stub, newForgeStub(t)
	stubB.answerRepository()
	e.upstreamAuth = &githttp.BasicAuth{Username: "x-token-auth", Password: "upstream-token"}

	addWorkspace(t, e.db, "fork", func(r *workspaceRow) {
		r.gitURL = "https://github.com/me/widgets"
		carryPatch("https://github.com/up/widgets")(r)
	})
	e.setVar("fork", "FORGE_API_URL", stubA.URL)
	e.setVar("fork", "UPSTREAM_FORGE_API_URL", stubB.URL)

	for _, tc := range []struct{ route, path string }{
		{"/forge/repository", "/repos/up/widgets"},
		{"/pulls/1/state", "/repos/up/widgets/pulls/1"},
	} {
		before := len(stubB.received())
		rec := e.get("/workspaces/fork"+tc.route+"?target=upstream", apiKeyAuth())
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d; want 200\n%s", tc.route, rec.Code, rec.Body)
		}
		reqs := stubB.received()
		if len(reqs) != before+1 {
			t.Fatalf("%s: stub B received %d requests; want %d", tc.route, len(reqs), before+1)
		}
		if last := reqs[len(reqs)-1]; last.Path != tc.path || bearer(last) != "upstream-token" {
			t.Errorf("%s: stub B saw %s with token %q; want %s with the upstream token", tc.route, last.Path, bearer(last), tc.path)
		}
	}
	if n := stubA.count(); n != 0 {
		t.Errorf("stub A (FORGE_API_URL) received %d requests; want none", n)
	}
	if n := e.originCalls.Load(); n != 0 {
		t.Errorf("the origin resolver was called %d times; want none", n)
	}

	// The upstream does not fall back to FORGE_API_URL: a host that names neither
	// forge stays unknown although the origin has an API URL.
	addWorkspace(t, e.db, "fork2", func(r *workspaceRow) {
		carryPatch("https://git.other.example/up/widgets")(r)
	})
	e.setVar("fork2", "FORGE_API_URL", stubA.URL)
	rec := e.get("/workspaces/fork2/forge/repository?target=upstream", apiKeyAuth())
	if rec.Code != http.StatusUnprocessableEntity || decodeError(t, rec).ErrorType != "forge_unknown" {
		t.Errorf("status = %d, body %s; want 422 forge_unknown", rec.Code, rec.Body)
	}
	if n := stubA.count(); n != 0 {
		t.Errorf("stub A received %d requests after the fallback case; want none", n)
	}
}

// TS-30-15: A rotated GIT_PAT applies to the next call because no client is cached.
// Requirement: 30-REQ-4.1
func TestRotatedTokenAppliesToTheNextCall_TS_30_15(t *testing.T) {
	var store *secrets.Store
	e := newEnv(t, func(cfg *Config) {
		if err := secrets.InitSchema(cfg.DB); err != nil {
			t.Fatalf("secrets schema: %v", err)
		}
		store = secrets.NewStore(cfg.DB)
		cfg.ResolveOriginAuth = func(slug string) (transport.AuthMethod, error) {
			return workspace.ResolveCloneAuth(store, slug)
		}
		cfg.GetVariable = store.GetVariableValue
	})
	e.stub.answerRepository()
	addWorkspace(t, e.db, "widgets")
	if _, err := store.CreateVariables("workspace", "widgets", []secrets.EntryInput{{Key: "FORGE_API_URL", Value: e.stub.URL}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSecrets("workspace", "widgets", []secrets.EntryInput{{Key: "GIT_PAT", Value: "old"}}); err != nil {
		t.Fatal(err)
	}

	call := func() {
		t.Helper()
		if rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth()); rec.Code != http.StatusOK {
			t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
		}
	}
	call()
	if _, err := store.UpdateSecret("workspace", "widgets", "GIT_PAT", "new"); err != nil {
		t.Fatal(err)
	}
	call()

	var tokens []string
	for _, r := range e.stub.received() {
		tokens = append(tokens, bearer(r))
	}
	if strings.Join(tokens, ",") != "old,new" {
		t.Errorf("the stub saw the tokens %v; want [old new]", tokens)
	}
}

// TS-30-16: An unparseable remote URL answers 422 forge_unknown without userinfo.
// Requirement: 30-REQ-4.2
func TestUnparseableRemoteIsForgeUnknown_TS_30_16(t *testing.T) {
	for _, gitURL := range []string{
		"https://user:s3cret@host",
		"https://user:s3cret@host/onlyone",
		"git@host",
		"user:s3cret@host",
	} {
		t.Run(gitURL, func(t *testing.T) {
			e := newEnv(t)
			addWorkspace(t, e.db, "widgets", func(r *workspaceRow) { r.gitURL = gitURL })

			rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d; want 422\n%s", rec.Code, rec.Body)
			}
			body := decodeError(t, rec)
			if body.ErrorType != "forge_unknown" {
				t.Errorf("error_type = %q; want forge_unknown", body.ErrorType)
			}
			for _, leaked := range []string{"user", "s3cret", "@"} {
				if strings.Contains(body.Message, leaked) {
					t.Errorf("the message %q contains %q", body.Message, leaked)
				}
			}
			if !strings.Contains(body.Message, "host") {
				t.Errorf("the message %q does not name the URL", body.Message)
			}
			assertNothingSent(t, e)
		})
	}
}

// TS-30-17: An invalid FORGE_API_URL or UPSTREAM_FORGE_API_URL answers 422
// forge_api_url_invalid without echoing the value.
// Requirement: 30-REQ-4.3
func TestInvalidAPIURLIsRefused_TS_30_17(t *testing.T) {
	bad := []string{
		"http://x.example",
		"relative/path",
		"https://u:p@h.example",
		"https://h.example/?q=1",
		"https://h.example/#f",
		"https:///nohost",
		"",
		"ftp://x.example",
	}
	for _, tc := range []struct{ variable, query string }{
		{"FORGE_API_URL", ""},
		{"UPSTREAM_FORGE_API_URL", "?target=upstream"},
	} {
		for _, value := range bad {
			t.Run(tc.variable+"="+value, func(t *testing.T) {
				e := newEnv(t)
				addWorkspace(t, e.db, "fork", carryPatch("https://github.com/up/widgets"))
				e.setVar("fork", tc.variable, value)

				rec := e.get("/workspaces/fork/forge/repository"+tc.query, apiKeyAuth())
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status = %d; want 422\n%s", rec.Code, rec.Body)
				}
				body := decodeError(t, rec)
				if body.ErrorType != "forge_api_url_invalid" {
					t.Errorf("error_type = %q; want forge_api_url_invalid", body.ErrorType)
				}
				if !strings.Contains(body.Message, tc.variable) || !strings.Contains(body.Message, apiURLRule) {
					t.Errorf("the message %q does not name the variable and the rule", body.Message)
				}
				if value != "" && strings.Contains(body.Message, value) {
					t.Errorf("the message %q echoes the value", body.Message)
				}
				assertNothingSent(t, e)
			})
		}
	}
}

// TS-30-18: An unclassifiable host without an API URL answers 422 forge_unknown
// naming the host and the variable.
// Requirement: 30-REQ-4.4
func TestUnclassifiableHostIsForgeUnknown_TS_30_18(t *testing.T) {
	counting := &countingTransport{}
	e := newEnv(t, func(cfg *Config) { cfg.Transport = counting })
	addWorkspace(t, e.db, "widgets", func(r *workspaceRow) { r.gitURL = "https://git.internal.example/acme/widgets" })
	addWorkspace(t, e.db, "fork", func(r *workspaceRow) {
		r.gitURL = "https://git.internal.example/me/widgets"
		carryPatch("git@git.upstream.example:up/widgets.git")(r)
	})

	for _, tc := range []struct{ slug, query, host, variable string }{
		{"widgets", "", "git.internal.example", "FORGE_API_URL"},
		{"fork", "?target=upstream", "git.upstream.example", "UPSTREAM_FORGE_API_URL"},
	} {
		rec := e.get("/workspaces/"+tc.slug+"/forge/repository"+tc.query, apiKeyAuth())
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d; want 422\n%s", tc.slug, rec.Code, rec.Body)
		}
		body := decodeError(t, rec)
		if body.ErrorType != "forge_unknown" {
			t.Errorf("%s: error_type = %q; want forge_unknown", tc.slug, body.ErrorType)
		}
		if !strings.Contains(body.Message, tc.host) || !strings.Contains(body.Message, tc.variable) {
			t.Errorf("%s: the message %q does not name %s and %s", tc.slug, body.Message, tc.host, tc.variable)
		}
		if strings.Contains(body.Message, "neither") {
			t.Errorf("%s: the message %q says the variable is set; it is not", tc.slug, body.Message)
		}
	}
	if n := counting.n.Load(); n != 0 {
		t.Errorf("the transport saw %d requests; want none", n)
	}
}

// A set API URL that still names no forge is not asked for again: the message says
// so, because setting the variable is not the answer.
// Requirement: 30-REQ-4.4
func TestAPIURLThatNamesNoForgeSaysSo(t *testing.T) {
	e := newEnv(t)
	addWorkspace(t, e.db, "widgets", func(r *workspaceRow) { r.gitURL = "https://git.internal.example/acme/widgets" })
	e.setVar("widgets", "FORGE_API_URL", e.stub.URL)

	rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d; want 422\n%s", rec.Code, rec.Body)
	}
	body := decodeError(t, rec)
	if body.ErrorType != "forge_unknown" || !strings.Contains(body.Message, "git.internal.example") || !strings.Contains(body.Message, "github") {
		t.Errorf("error %+v; want forge_unknown naming the host and what an API URL must contain", body)
	}
	if !strings.Contains(body.Message, "neither") || strings.Contains(body.Message, "set FORGE_API_URL") {
		t.Errorf("the message %q asks to set a variable that is set; want it to say what is wrong with its value", body.Message)
	}
	if strings.Contains(body.Message, e.stub.URL) {
		t.Errorf("the message echoes the variable's value: %q", body.Message)
	}
	assertNothingSent(t, e)

	// An API URL that names the forge classifies a host that does not.
	e2 := newEnv(t)
	e2.stub.answerRepository()
	addWorkspace(t, e2.db, "widgets", func(r *workspaceRow) { r.gitURL = "https://git.internal.example/acme/widgets" })
	e2.setVar("widgets", "FORGE_API_URL", e2.stub.URL+"/github/api")
	rec = e2.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200: an API URL that names GitHub classifies the host\n%s", rec.Code, rec.Body)
	}
	if reqs := e2.stub.received(); len(reqs) != 1 || reqs[0].Path != "/github/api/repos/acme/widgets" {
		t.Errorf("the stub saw %+v; want one request to /github/api/repos/acme/widgets", reqs)
	}
}

// TS-30-20: A store failure from the resolver or the variable lookup answers 500 and
// logs no secret.
// Requirement: 30-REQ-4.6
func TestStoreFailureIsA500WithoutSecrets_TS_30_20(t *testing.T) {
	const secret = "SECRET-VALUE-9f2c"
	for name, setup := range map[string]func(*env){
		"the resolver fails": func(e *env) { e.originErr = errors.New("lookup GIT_PAT: stored value " + secret) },
		"the variable lookup fails": func(e *env) {
			e.varErr = errors.New("database is locked: " + secret)
		},
	} {
		t.Run(name, func(t *testing.T) {
			log := captureLog(t)
			e := newEnv(t)
			addWorkspace(t, e.db, "widgets")
			setup(e)

			rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d; want 500\n%s", rec.Code, rec.Body)
			}
			decodeError(t, rec)
			if strings.Contains(rec.Body.String(), secret) {
				t.Errorf("the response holds the secret: %s", rec.Body)
			}
			if !strings.Contains(log.String(), "widgets") {
				t.Errorf("the failure was not logged with the workspace:\n%s", log)
			}
			if strings.Contains(log.String(), secret) {
				t.Errorf("the log holds the secret:\n%s", log)
			}
			assertNothingSent(t, e)
		})
	}
}

// parseRemote reads the repository of the remote URLs a workspace can have: through
// issuex's parser for the forges it knows, and by splitting the URL for any other
// host, which issuex then fails to classify.
func TestParseRemote(t *testing.T) {
	cases := []struct {
		remote string
		want   issuex.Repo
		ok     bool
	}{
		{"https://github.com/acme/widgets", issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}, true},
		{"https://github.com/acme/widgets.git", issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}, true},
		{"git@github.com:acme/widgets.git", issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}, true},
		{"https://gitlab.example.com/group/subgroup/widgets", issuex.Repo{Host: "gitlab.example.com", Owner: "group/subgroup", Name: "widgets"}, true},
		{"https://tok:en@github.com/acme/widgets", issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}, true},
		{"https://git.internal.example/acme/widgets", issuex.Repo{Host: "git.internal.example", Owner: "acme", Name: "widgets"}, true},
		{"https://Git.Internal.Example:8443/a/b/c.git", issuex.Repo{Host: "git.internal.example", Owner: "a/b", Name: "c"}, true},
		{"git@git.internal.example:acme/widgets.git", issuex.Repo{Host: "git.internal.example", Owner: "acme", Name: "widgets"}, true},
		{"ssh://git@git.internal.example:2222/acme/widgets", issuex.Repo{Host: "git.internal.example", Owner: "acme", Name: "widgets"}, true},
		{"https://user:s3cret@host", issuex.Repo{}, false},
		{"https://host/onlyone", issuex.Repo{}, false},
		{"https://host//widgets", issuex.Repo{}, false},
		{"ftp://host/acme/widgets", issuex.Repo{}, false},
		{"git@host", issuex.Repo{}, false},
		{"widgets", issuex.Repo{}, false},
		{"", issuex.Repo{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.remote, func(t *testing.T) {
			got, ok := parseRemote(tc.remote)
			if ok != tc.ok || got != tc.want {
				t.Errorf("parseRemote(%q) = %+v, %v; want %+v, %v", tc.remote, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// redactRemote removes the userinfo of a URL and of an scp-style remote.
func TestRedactRemote(t *testing.T) {
	cases := map[string]string{
		"https://user:s3cret@host/acme/widgets": "https://host/acme/widgets",
		"https://tok@github.com/acme/widgets":   "https://github.com/acme/widgets",
		"ssh://git@host:2222/acme/widgets":      "ssh://host:2222/acme/widgets",
		"git@github.com:acme/widgets.git":       "github.com:acme/widgets.git",
		"user:s3cret@host":                      "host",
		"https://github.com/acme/widgets":       "https://github.com/acme/widgets",
		"github.com:acme/at@sign.git":           "github.com:acme/at@sign.git",
		"https://host/path?x=1#frag@ment":       "https://host/path?x=1#frag@ment",
		"https://a:b@c:d@host/acme/widgets":     "https://host/acme/widgets",
	}
	for in, want := range cases {
		if got := redactRemote(in); got != want {
			t.Errorf("redactRemote(%q) = %q; want %q", in, got, want)
		}
	}
}

// validAPIURL accepts an absolute https URL with a host and nothing in the way of
// userinfo, a query or a fragment.
func TestValidAPIURL(t *testing.T) {
	for _, ok := range []string{
		"https://api.github.com",
		"https://gitlab.example.com/api/v4",
		"https://gitlab.example.com:8443/api/v4/",
		"https://127.0.0.1:9000",
		"https://[::1]:9000/api",
	} {
		if !validAPIURL(ok) {
			t.Errorf("validAPIURL(%q) = false; want true", ok)
		}
	}
	for _, bad := range []string{
		"", "http://x.example", "relative/path", "https://u:p@h.example", "https://u@h.example",
		"https://h.example/?q=1", "https://h.example?", "https://h.example/#f", "https://h.example#",
		"https:///nohost", "https:nohost", "//h.example", "ftp://h.example", "https://", "https://h.example/a b",
	} {
		if validAPIURL(bad) {
			t.Errorf("validAPIURL(%q) = true; want false", bad)
		}
	}
}

// assertNothingSent fails when the forge stub received a request.
func assertNothingSent(t *testing.T, e *env) {
	t.Helper()
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
}

// A host that names both forges cannot be classified, and issuex must not then fall back
// to the origin of the git repository the hub happens to run in: the read is refused and
// the diagnostic says unknown, whatever the working directory holds.
// Requirement: 30-REQ-4.4, 30-REQ-6.3
func TestAmbiguousHostNeverAsksTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "https://github.com/evil/repo.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)

	e := newEnv(t)
	addWorkspace(t, e.db, "widgets", func(r *workspaceRow) { r.gitURL = "https://github.gitlab.example/acme/widgets" })

	rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	if rec.Code != http.StatusUnprocessableEntity || decodeError(t, rec).ErrorType != "forge_unknown" {
		t.Errorf("read: status %d, body %s; want 422 forge_unknown", rec.Code, rec.Body)
	}
	d, _ := getDiagnostic(t, e, "widgets")
	if d.Forge != "unknown" || d.APIURL != nil {
		t.Errorf("diagnostic: forge, api_url = %q, %v; want unknown and null", d.Forge, d.APIURL)
	}
	assertNothingSent(t, e)
}
