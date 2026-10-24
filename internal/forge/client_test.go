package forge

import (
	"errors"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// parseRemote reads the forges issuex knows through its parser and splits any other
// URL or scp-style remote into host, owner and name.
func TestParseRemote(t *testing.T) {
	for _, tc := range []struct {
		remote string
		want   issuex.Repo
		ok     bool
	}{
		{"https://github.com/acme/widgets.git", issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}, true},
		{"git@github.com:acme/widgets.git", issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}, true},
		{"https://gitlab.example.com/group/subgroup/widgets", issuex.Repo{Host: "gitlab.example.com", Owner: "group/subgroup", Name: "widgets"}, true},
		{"https://tok:en@github.com/acme/widgets", issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}, true},
		{"https://Git.Internal.Example:8443/a/b/c.git", issuex.Repo{Host: "git.internal.example", Owner: "a/b", Name: "c"}, true},
		{"git@git.internal.example:acme/widgets.git", issuex.Repo{Host: "git.internal.example", Owner: "acme", Name: "widgets"}, true},
		{"ssh://git@git.internal.example:2222/acme/widgets", issuex.Repo{Host: "git.internal.example", Owner: "acme", Name: "widgets"}, true},
		{"https://user:s3cret@host", issuex.Repo{}, false},
		{"https://host/onlyone", issuex.Repo{}, false},
		{"ftp://host/acme/widgets", issuex.Repo{}, false},
		{"git@host", issuex.Repo{}, false},
		{"widgets", issuex.Repo{}, false},
		{"https://%zz/acme/widgets", issuex.Repo{}, false},
	} {
		if got, ok := parseRemote(tc.remote); ok != tc.ok || got != tc.want {
			t.Errorf("parseRemote(%q) = %+v, %v; want %+v, %v", tc.remote, got, ok, tc.want, tc.ok)
		}
	}
}

// redactRemote removes the userinfo of a URL and of an scp-style remote; shownRemote also
// cuts what a password that ends the authority leaves behind.
func TestRedactRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:s3cret@host/acme/widgets": "https://host/acme/widgets",
		"ssh://git@host:2222/acme/widgets":      "ssh://host:2222/acme/widgets",
		"git@github.com:acme/widgets.git":       "github.com:acme/widgets.git",
		"user:s3cret@host":                      "host",
		"github.com:acme/at@sign.git":           "github.com:acme/at@sign.git",
		"https://host/path?x=1#frag@ment":       "https://host/path?x=1#frag@ment",
		"https://a:b@c:d@host/acme/widgets":     "https://host/acme/widgets",
	} {
		if got := redactRemote(in); got != want {
			t.Errorf("redactRemote(%q) = %q; want %q", in, got, want)
		}
	}
	if got := shownRemote("https://user:s3cret/x@host/foo/bar"); got != "host/foo/bar" {
		t.Errorf("shownRemote = %q; want host/foo/bar", got)
	}
}

func TestValidAPIURL(t *testing.T) {
	for _, ok := range []string{"https://api.github.com", "https://gitlab.example.com:8443/api/v4/", "https://[::1]:9000/api"} {
		if !validAPIURL(ok) {
			t.Errorf("validAPIURL(%q) = false; want true", ok)
		}
	}
	for _, bad := range []string{"", "http://x.example", "relative/path", "https://u:p@h.example", "https://h.example/?q=1",
		"https://h.example/#f", "https:///nohost", "https:nohost", "ftp://h.example", "https://h.example/a b", "https://%zz"} {
		if validAPIURL(bad) {
			t.Errorf("validAPIURL(%q) = true; want false", bad)
		}
	}
}

// The username decides the kind of a credential: x-token-auth is a PAT, any other makes
// the password a basic token, and no basic auth is none.
func TestCredentialOf(t *testing.T) {
	var nilBasic *githttp.BasicAuth
	for _, tc := range []struct {
		auth  transport.AuthMethod
		kind  credentialKind
		token string
	}{
		{nil, credentialNone, ""},
		{nilBasic, credentialNone, ""},
		{&githttp.TokenAuth{Token: "t"}, credentialNone, ""},
		{&githttp.BasicAuth{Username: patUsername, Password: "p"}, credentialPAT, "p"},
		{&githttp.BasicAuth{Username: "alice", Password: "q"}, credentialBasic, "q"},
	} {
		if kind, token := credentialOf(tc.auth); kind != tc.kind || token != tc.token {
			t.Errorf("credentialOf(%v) = %q, %q; want %q, %q", tc.auth, kind, token, tc.kind, tc.token)
		}
	}
}

func TestDefaultAPIURL(t *testing.T) {
	for _, tc := range []struct{ forge, host, want string }{
		{"github", "github.com", "https://api.github.com"},
		{"github", "github.corp.example", "https://github.corp.example/api/v3"},
		{"gitlab", "gitlab.com", "https://gitlab.com/api/v4"},
	} {
		if got := defaultAPIURL(issuex.ForgeType(tc.forge), tc.host); got != tc.want {
			t.Errorf("defaultAPIURL(%s, %s) = %q; want %q", tc.forge, tc.host, got, tc.want)
		}
	}
}

// resolveTarget takes an absent target and origin everywhere and upstream only on a
// carry_patch workspace with an upstream_url; anything else is refused.
func TestResolveTarget(t *testing.T) {
	fork := workspaceRemotes{gitURL: "https://github.com/me/widgets", mode: "carry_patch", upstreamURL: "https://github.com/up/widgets"}
	standard := workspaceRemotes{gitURL: "https://github.com/me/widgets", mode: "standard", upstreamURL: "https://github.com/up/widgets"}
	for _, tc := range []struct {
		query     string
		ws        workspaceRemotes
		want      target
		errorType string
	}{
		{"", standard, targetOrigin, ""},
		{"target=origin", standard, targetOrigin, ""},
		{"", fork, targetOrigin, ""},
		{"target=upstream", fork, targetUpstream, ""},
		{"target=upstream", standard, "", "workspace_mode_mismatch"},
		{"target=upstream", workspaceRemotes{mode: "carry_patch"}, "", "workspace_mode_mismatch"},
		{"target=bogus", fork, "", "refused"},
		{"target=", fork, "", "refused"},
		{"target=ORIGIN", fork, "", "refused"},
		{"target=origin&target=upstream", fork, "", "refused"},
	} {
		q, _ := url.ParseQuery(tc.query)
		got, ref := resolveTarget(q, tc.ws)
		switch {
		case tc.errorType == "" && (ref != nil || got != tc.want):
			t.Errorf("%q: = %q, %+v; want %q", tc.query, got, ref, tc.want)
		case tc.errorType == "refused" && (ref == nil || ref.status != 400 || !strings.Contains(ref.message, `"origin" or "upstream"`)):
			t.Errorf("%q: = %+v; want a 400 listing the values", tc.query, ref)
		case tc.errorType == "workspace_mode_mismatch" && (ref == nil || ref.status != 400 || ref.errorType != tc.errorType):
			t.Errorf("%q: = %+v; want 400 %s", tc.query, ref, tc.errorType)
		}
	}
}

// The target picks the remote, the resolver and the variable: origin reads git_url with
// the origin credential and FORGE_API_URL, upstream reads upstream_url with the upstream
// credential and UPSTREAM_FORGE_API_URL only, without falling back to the origin's.
func TestBuildClient_TargetSelectsRemoteCredentialAndVariable(t *testing.T) {
	e := newEnv(t)
	origin, upstream := e.stub, newForgeStub(t)
	origin.setHandler(respond(http.StatusOK, repositoryBody))
	upstream.setHandler(respond(http.StatusOK, repositoryBody))
	e.upstreamAuth = &githttp.BasicAuth{Username: patUsername, Password: "upstream-token"}
	addWorkspace(t, e.db, "fork", withGitURL("https://github.com/me/widgets"), carryPatch("https://github.com/up/widgets"))
	e.setVar("fork", "FORGE_API_URL", origin.URL)
	e.setVar("fork", "UPSTREAM_FORGE_API_URL", upstream.URL)

	for _, tc := range []struct{ query, path, token string }{
		{"", "/repos/me/widgets", "origin-token"},
		{"?target=origin", "/repos/me/widgets", "origin-token"},
		{"?target=upstream", "/repos/up/widgets", "upstream-token"},
	} {
		origin.forget()
		upstream.forget()
		if rec := e.get("/workspaces/fork/forge/repository"+tc.query, apiKeyAuth()); rec.Code != http.StatusOK {
			t.Fatalf("%q: status = %d\n%s", tc.query, rec.Code, rec.Body)
		}
		reqs := append(origin.received(), upstream.received()...)
		if len(reqs) != 1 || reqs[0].Path != tc.path || bearer(reqs[0]) != tc.token {
			t.Errorf("%q: the forges saw %+v; want one %s with the token %s", tc.query, reqs, tc.path, tc.token)
		}
	}
	if e.originCalls.Load() != 2 || e.upstreamCalls.Load() != 1 {
		t.Errorf("origin resolver %d, upstream resolver %d; want 2 and 1", e.originCalls.Load(), e.upstreamCalls.Load())
	}
	// A target the workspace does not have, and an unknown one, are refused before anything is looked up.
	addWorkspace(t, e.db, "standard")
	for _, q := range []string{"?target=upstream", "?target=bogus", "?target="} {
		rec := e.get("/workspaces/standard/issues"+q, apiKeyAuth())
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400", q, rec.Code)
		}
	}
	if rec := e.get("/workspaces/fork/forge?target=bogus", apiKeyAuth()); rec.Code != http.StatusOK {
		t.Errorf("the diagnostic takes no target: status = %d; want 200", rec.Code)
	}
	// The upstream of a host that names no forge does not borrow FORGE_API_URL.
	addWorkspace(t, e.db, "fork2", carryPatch("https://git.other.example/up/widgets"))
	e.setVar("fork2", "FORGE_API_URL", origin.URL+"/github")
	rec := e.get("/workspaces/fork2/forge/repository?target=upstream", apiKeyAuth())
	if rec.Code != http.StatusUnprocessableEntity || testutil.Error(t, rec).ErrorType != "forge_unknown" {
		t.Errorf("an upstream without its own API URL: %d %s; want 422 forge_unknown", rec.Code, rec.Body)
	}
}

// The client is built from the credential and variable of the moment, with the API URL
// trimmed, the remote's userinfo dropped, the shared HTTP client, and the token of the
// credential's kind: a PAT or basic password is sent, no credential sends no token at
// all, not even the one in hub's environment. A rotated secret applies to the next call.
func TestBuildClient_CredentialAndOptions(t *testing.T) {
	var got []issuex.Options
	var mu sync.Mutex
	record := func(cfg *Config) {
		cfg.NewClient = func(o issuex.Options) (issuex.Client, error) {
			mu.Lock()
			got = append(got, o)
			mu.Unlock()
			return &recordingClient{}, nil
		}
	}
	e := newEnv(t, record)
	e.originAuth = nil
	addWorkspace(t, e.db, "widgets", withGitURL("https://tok:en@github.com/acme/widgets"))
	e.setVar("widgets", "FORGE_API_URL", "  https://api.example.test/v3\n")
	if rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth()); rec.Code != http.StatusOK {
		t.Fatalf("status = %d\n%s", rec.Code, rec.Body)
	}
	want := issuex.Options{BaseURL: "https://api.example.test/v3", RemoteURL: "https://github.com/acme/widgets",
		Repo: issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}}
	if len(got) != 1 || got[0].HTTPClient == nil || got[0].Token != "" || got[0].BaseURL != want.BaseURL || got[0].RemoteURL != want.RemoteURL || got[0].Repo != want.Repo {
		t.Errorf("Options = %+v; want %+v with the shared HTTP client", got, want)
	}

	t.Setenv("GITHUB_TOKEN", "env-github")
	t.Setenv("GITLAB_TOKEN", "env-gitlab")
	for _, tc := range []struct {
		name   string
		gitURL string
		api    string
		auth   transport.AuthMethod
		token  string
	}{
		{"github pat", "https://github.com/acme/widgets", "", &githttp.BasicAuth{Username: patUsername, Password: "tok-pat"}, "tok-pat"},
		{"github basic", "https://github.com/acme/widgets", "", &githttp.BasicAuth{Username: "alice", Password: "tok-pass"}, "tok-pass"},
		{"github none", "https://github.com/acme/widgets", "", nil, ""},
		{"gitlab pat", "https://gitlab.example.com/group/widgets", "/gitlab/api/v4", &githttp.BasicAuth{Username: patUsername, Password: "tok-pat"}, "tok-pat"},
		{"gitlab none", "https://gitlab.example.com/group/widgets", "/gitlab/api/v4", nil, ""},
	} {
		e := newEnv(t)
		e.stub.setHandler(respond(http.StatusOK, repositoryBody))
		e.originAuth = tc.auth
		addWorkspace(t, e.db, "widgets", withGitURL(tc.gitURL))
		e.setVar("widgets", "FORGE_API_URL", e.stub.URL+tc.api)
		if rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth()); rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d\n%s", tc.name, rec.Code, rec.Body)
		}
		reqs := e.stub.received()
		if len(reqs) != 1 || bearer(reqs[0]) != tc.token {
			t.Errorf("%s: the stub saw %d requests with the token %q; want 1 with %q", tc.name, len(reqs), bearer(reqs[0]), tc.token)
		}
	}

	// Nothing is cached: a rotated GIT_PAT in the real store applies to the next call.
	var store *secrets.Store
	rotated := newEnv(t, func(cfg *Config) {
		store = secrets.NewStore(cfg.DB)
		cfg.ResolveOriginAuth = func(slug string) (transport.AuthMethod, error) { return workspace.ResolveCloneAuth(store, slug) }
		cfg.GetVariable = store.GetVariableValue
	})
	rotated.stub.setHandler(respond(http.StatusOK, repositoryBody))
	addWorkspace(t, rotated.db, "widgets")
	if _, err := store.CreateVariables("workspace", "widgets", []secrets.EntryInput{{Key: "FORGE_API_URL", Value: rotated.stub.URL}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSecrets("workspace", "widgets", []secrets.EntryInput{{Key: "GIT_PAT", Value: "old"}}); err != nil {
		t.Fatal(err)
	}
	rotated.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	if _, err := store.UpdateSecret("workspace", "widgets", "GIT_PAT", "new"); err != nil {
		t.Fatal(err)
	}
	rotated.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	var tokens []string
	for _, r := range rotated.stub.received() {
		tokens = append(tokens, bearer(r))
	}
	if strings.Join(tokens, ",") != "old,new" {
		t.Errorf("the stub saw the tokens %v; want old then new", tokens)
	}
}

// What hub cannot build a client for is refused before the forge is asked: an unparseable
// remote and an unclassifiable host are 422 forge_unknown naming the host and the variable
// but never userinfo or the variable's value; an invalid API URL is 422
// forge_api_url_invalid naming the variable and the rule; a failing store is 500 without
// the secret in the answer or the log.
func TestBuildClient_Refusals(t *testing.T) {
	const secret = "SECRET-VALUE-9f2c"
	log := captureLog(t)
	e := newEnv(t)
	addWorkspace(t, e.db, "userinfo", withGitURL("https://user:s3cret#x@host/foo/bar"))
	addWorkspace(t, e.db, "onlyone", withGitURL("https://user:s3cret@host/onlyone"))
	addWorkspace(t, e.db, "internal", withGitURL("https://git.internal.example/acme/widgets"))
	addWorkspace(t, e.db, "named", withGitURL("https://git.internal.example/acme/widgets"))
	e.setVar("named", "FORGE_API_URL", e.stub.URL+"/neither")
	addWorkspace(t, e.db, "both", withGitURL("https://git.internal.example/acme/widgets"))
	e.setVar("both", "FORGE_API_URL", e.stub.URL+"/github-gitlab")
	addWorkspace(t, e.db, "fork", carryPatch("git@git.upstream.example:up/widgets.git"))
	addWorkspace(t, e.db, "badvar")
	e.setVar("badvar", "FORGE_API_URL", "http://u:p@h.example/?q="+secret)
	addWorkspace(t, e.db, "badupstream", carryPatch("https://github.com/up/widgets"))
	e.setVar("badupstream", "UPSTREAM_FORGE_API_URL", "ftp://x")
	addWorkspace(t, e.db, "widgets")

	for _, tc := range []struct {
		name, path, errorType string
		contains, lacks       []string
		setup                 func()
	}{
		{"an unparseable remote", "/workspaces/userinfo/forge/repository", "forge_unknown", []string{"host/foo/bar"}, []string{"user", "s3cret", "@"}, nil},
		{"a remote with one segment", "/workspaces/onlyone/issues", "forge_unknown", []string{"host"}, []string{"user", "s3cret", "@"}, nil},
		{"an unclassifiable host", "/workspaces/internal/forge/repository", "forge_unknown", []string{"git.internal.example", "set FORGE_API_URL"}, []string{"neither"}, nil},
		{"an unclassifiable upstream", "/workspaces/fork/pulls/1/state?target=upstream", "forge_unknown", []string{"git.upstream.example", "UPSTREAM_FORGE_API_URL"}, nil, nil},
		{"an API URL that names no forge", "/workspaces/named/forge/repository", "forge_unknown", []string{"neither", "github"}, []string{e.stub.URL, "set FORGE_API_URL"}, nil},
		{"an API URL that names both", "/workspaces/both/forge/repository", "forge_unknown", []string{"exactly one"}, nil, nil},
		{"an invalid FORGE_API_URL", "/workspaces/badvar/forge/repository", "forge_api_url_invalid", []string{"FORGE_API_URL", "absolute https:// URL"}, []string{secret}, nil},
		{"an invalid UPSTREAM_FORGE_API_URL", "/workspaces/badupstream/issues?target=upstream", "forge_api_url_invalid", []string{"UPSTREAM_FORGE_API_URL"}, []string{"ftp"}, nil},
		{"a resolver that fails", "/workspaces/widgets/forge/repository", "", nil, []string{secret}, func() { e.originErr = errors.New("lookup GIT_PAT: " + secret) }},
		{"a variable lookup that fails", "/workspaces/widgets/forge/repository", "", nil, []string{secret}, func() { e.originErr, e.varErr = nil, errors.New("locked: "+secret) }},
	} {
		if tc.setup != nil {
			tc.setup()
		}
		rec := e.get(tc.path, apiKeyAuth())
		want := http.StatusUnprocessableEntity
		if tc.errorType == "" {
			want = http.StatusInternalServerError
		}
		got := testutil.Error(t, rec)
		if rec.Code != want || got.ErrorType != tc.errorType {
			t.Errorf("%s: %d %+v; want %d %s", tc.name, rec.Code, got, want, tc.errorType)
		}
		for _, s := range tc.contains {
			if !strings.Contains(got.Message, s) {
				t.Errorf("%s: message %q lacks %q", tc.name, got.Message, s)
			}
		}
		for _, s := range tc.lacks {
			if strings.Contains(got.Message, s) {
				t.Errorf("%s: message %q holds %q", tc.name, got.Message, s)
			}
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge received %d requests; want none", n)
	}
	if strings.Contains(log.String(), secret) || !strings.Contains(log.String(), "widgets") {
		t.Errorf("the log holds the secret or not the workspace:\n%s", log)
	}

	// An API URL that names the forge classifies a host that does not.
	e.varErr = nil
	e.stub.setHandler(respond(http.StatusOK, repositoryBody))
	e.setVar("internal", "FORGE_API_URL", e.stub.URL+"/github/api")
	if rec := e.get("/workspaces/internal/forge/repository", apiKeyAuth()); rec.Code != http.StatusOK {
		t.Fatalf("an API URL naming GitHub: status = %d\n%s", rec.Code, rec.Body)
	}
	if reqs := e.stub.received(); len(reqs) != 1 || reqs[0].Path != "/github/api/repos/acme/widgets" {
		t.Errorf("the stub saw %+v; want one request under /github/api", reqs)
	}
}

// A host that names both forges is not classified by asking the git repository hub runs
// in: issuex would fall back to the origin of the working directory.
func TestBuildClient_AmbiguousHostNeverAsksTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/evil/repo.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)
	e := newEnv(t)
	addWorkspace(t, e.db, "widgets", withGitURL("https://github.gitlab.example/acme/widgets"))
	rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
	if rec.Code != http.StatusUnprocessableEntity || testutil.Error(t, rec).ErrorType != "forge_unknown" || e.stub.count() != 0 {
		t.Errorf("read: %d %s, %d forge requests; want 422 forge_unknown and none", rec.Code, rec.Body, e.stub.count())
	}
	if body := e.get("/workspaces/widgets/forge", apiKeyAuth()).Body.String(); !strings.Contains(body, `"forge":"unknown","api_url":null`) {
		t.Errorf("diagnostic = %s; want unknown with a null api_url", body)
	}
}
