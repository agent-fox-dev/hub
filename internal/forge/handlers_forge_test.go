package forge

import (
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// diagnostic is the body of GET /workspaces/:slug/forge.
type diagnostic struct {
	Forge      string  `json:"forge"`
	APIURL     *string `json:"api_url"`
	Repository *struct {
		Host  string `json:"host"`
		Owner string `json:"owner"`
		Name  string `json:"name"`
	} `json:"repository"`
	Credential string `json:"credential"`
	Upstream   *struct {
		Forge      string  `json:"forge"`
		APIURL     *string `json:"api_url"`
		Repository *struct {
			Host  string `json:"host"`
			Owner string `json:"owner"`
			Name  string `json:"name"`
		} `json:"repository"`
		Credential string `json:"credential"`
	} `json:"upstream"`
}

// getDiagnostic calls GET /forge as the owner and decodes the answer.
func getDiagnostic(t *testing.T, e *env, slug string) (diagnostic, string) {
	t.Helper()
	rec := e.get("/workspaces/"+slug+"/forge", apiKeyAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d; want 200\n%s", slug, rec.Code, rec.Body)
	}
	var d diagnostic
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("%s: the body is not the diagnostic: %v\n%s", slug, err, rec.Body)
	}
	return d, strings.TrimSpace(rec.Body.String())
}

// countingEnv is an environment whose transport counts, and fails, every request the
// forge would receive.
func countingEnv(t *testing.T) (*env, *countingTransport) {
	t.Helper()
	counting := &countingTransport{}
	return newEnv(t, func(cfg *Config) { cfg.Transport = counting }), counting
}

// TS-30-26: GET /forge reports forge, API URL, repository and credential kind without
// contacting the forge.
// Requirement: 30-REQ-6.1
func TestDiagnosticReportsWithoutContactingTheForge_TS_30_26(t *testing.T) {
	e, counting := countingEnv(t)
	addWorkspace(t, e.db, "widgets")

	_, body := getDiagnostic(t, e, "widgets")
	const want = `{"forge":"github","api_url":"https://api.github.com","repository":{"host":"github.com","owner":"acme","name":"widgets"},"credential":"pat","upstream":null}`
	if body != want {
		t.Errorf("body\n%s\nwant\n%s", body, want)
	}
	if n := counting.n.Load(); n != 0 {
		t.Errorf("the transport saw %d requests; want none", n)
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
}

// TS-30-27: GitLab subgroup namespaces are reported as the full owner path.
// Requirement: 30-REQ-6.2
func TestDiagnosticKeepsGitLabSubgroups_TS_30_27(t *testing.T) {
	e, _ := countingEnv(t)
	addWorkspace(t, e.db, "widgets", func(r *workspaceRow) { r.gitURL = "https://gitlab.example.com/group/subgroup/widgets" })
	e.setVar("widgets", "FORGE_API_URL", "https://gitlab.example.com/api/v4")

	d, _ := getDiagnostic(t, e, "widgets")
	if d.Forge != "gitlab" || d.APIURL == nil || *d.APIURL != "https://gitlab.example.com/api/v4" {
		t.Errorf("forge, api_url = %q, %v; want gitlab and the variable's value", d.Forge, d.APIURL)
	}
	if d.Repository == nil || d.Repository.Owner != "group/subgroup" || d.Repository.Name != "widgets" || d.Repository.Host != "gitlab.example.com" {
		t.Errorf("repository = %+v; want gitlab.example.com group/subgroup widgets", d.Repository)
	}
}

// TS-30-28: GET /forge reports unknown instead of failing for an unclassifiable or an
// unparseable remote.
// Requirement: 30-REQ-6.3
func TestDiagnosticReportsUnknown_TS_30_28(t *testing.T) {
	e, _ := countingEnv(t)
	addWorkspace(t, e.db, "unclassifiable", func(r *workspaceRow) { r.gitURL = "https://git.internal.example/acme/widgets" })
	addWorkspace(t, e.db, "unparseable", func(r *workspaceRow) { r.gitURL = "https://user:s3cret@host" })

	a, body := getDiagnostic(t, e, "unclassifiable")
	if a.Forge != "unknown" || a.APIURL != nil {
		t.Errorf("unclassifiable: forge, api_url = %q, %v; want unknown and null", a.Forge, a.APIURL)
	}
	if !strings.Contains(body, `"api_url":null`) {
		t.Errorf("unclassifiable: the body %s does not hold a null api_url", body)
	}
	if a.Repository == nil || a.Repository.Host != "git.internal.example" || a.Repository.Name != "widgets" {
		t.Errorf("unclassifiable: repository = %+v; want the repository the URL names", a.Repository)
	}
	if a.Credential != "pat" {
		t.Errorf("unclassifiable: credential = %q; want pat", a.Credential)
	}

	b, body := getDiagnostic(t, e, "unparseable")
	if b.Forge != "unknown" || b.APIURL != nil || b.Repository != nil {
		t.Errorf("unparseable: forge, api_url, repository = %q, %v, %+v; want unknown, null, null", b.Forge, b.APIURL, b.Repository)
	}
	if !strings.Contains(body, `"repository":null`) || strings.Contains(body, "s3cret") || strings.Contains(body, "user") {
		t.Errorf("unparseable: body %s", body)
	}
}

// TS-30-29: The upstream block is filled on carry_patch workspaces and null on standard
// ones.
// Requirement: 30-REQ-6.4
func TestDiagnosticUpstreamBlock_TS_30_29(t *testing.T) {
	e, counting := countingEnv(t)
	e.originAuth = &githttp.BasicAuth{Username: "alice", Password: "pw"}
	e.upstreamAuth = &githttp.BasicAuth{Username: "x-token-auth", Password: "up-token"}
	addWorkspace(t, e.db, "fork", func(r *workspaceRow) {
		r.gitURL = "https://github.com/me/widgets"
		carryPatch("https://github.com/up/widgets")(r)
	})
	addWorkspace(t, e.db, "standard")
	addWorkspace(t, e.db, "forkunknown", func(r *workspaceRow) {
		carryPatch("https://git.other.example/up/widgets")(r)
	})
	e.setVar("fork", "UPSTREAM_FORGE_API_URL", "https://api.github.example/enterprise")
	e.setVar("forkunknown", "FORGE_API_URL", "https://api.github.example/origin")

	d, _ := getDiagnostic(t, e, "fork")
	if d.Credential != "basic" || d.Repository == nil || d.Repository.Owner != "me" {
		t.Errorf("fork origin block: credential %q, repository %+v; want basic and me/widgets", d.Credential, d.Repository)
	}
	up := d.Upstream
	if up == nil {
		t.Fatal("fork: upstream is null; want an object")
	}
	if up.Forge != "github" || up.Credential != "pat" || up.Repository == nil || up.Repository.Owner != "up" || up.Repository.Name != "widgets" {
		t.Errorf("fork upstream block = %+v; want github, pat, up/widgets", up)
	}
	if up.APIURL == nil || *up.APIURL != "https://api.github.example/enterprise" {
		t.Errorf("fork upstream api_url = %v; want UPSTREAM_FORGE_API_URL's value", up.APIURL)
	}

	// An upstream on a host that names no forge does not inherit FORGE_API_URL.
	d, _ = getDiagnostic(t, e, "forkunknown")
	if d.Upstream == nil || d.Upstream.Forge != "unknown" || d.Upstream.APIURL != nil {
		t.Errorf("forkunknown upstream block = %+v; want unknown and a null api_url", d.Upstream)
	}

	d, body := getDiagnostic(t, e, "standard")
	if d.Upstream != nil || !strings.HasSuffix(body, `"upstream":null}`) {
		t.Errorf("standard: upstream = %+v, body %s; want null", d.Upstream, body)
	}
	if n := counting.n.Load(); n != 0 {
		t.Errorf("the transport saw %d requests; want none", n)
	}
}

// TS-30-30: GET /forge rejects an invalid FORGE_API_URL or UPSTREAM_FORGE_API_URL with
// 422.
// Requirement: 30-REQ-6.5
func TestDiagnosticRejectsInvalidAPIURL_TS_30_30(t *testing.T) {
	e, _ := countingEnv(t)
	addWorkspace(t, e.db, "origin")
	addWorkspace(t, e.db, "fork", carryPatch("https://github.com/up/widgets"))
	addWorkspace(t, e.db, "standard")
	e.setVar("origin", "FORGE_API_URL", "http://insecure")
	e.setVar("fork", "FORGE_API_URL", "https://api.example.test")
	e.setVar("fork", "UPSTREAM_FORGE_API_URL", "ftp://x")
	// A standard workspace has no upstream block, so its variable is not consulted.
	e.setVar("standard", "UPSTREAM_FORGE_API_URL", "ftp://x")

	for _, slug := range []string{"origin", "fork"} {
		rec := e.get("/workspaces/"+slug+"/forge", apiKeyAuth())
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d; want 422\n%s", slug, rec.Code, rec.Body)
		}
		if got := decodeError(t, rec).ErrorType; got != "forge_api_url_invalid" {
			t.Errorf("%s: error_type = %q; want forge_api_url_invalid", slug, got)
		}
	}
	if rec := e.get("/workspaces/standard/forge", apiKeyAuth()); rec.Code != http.StatusOK {
		t.Errorf("standard: status = %d; want 200: its upstream variable is not consulted", rec.Code)
	}
}

// TS-30-31: GET /forge never contains a secret value, only the credential kind.
// Requirement: 30-REQ-6.6
func TestDiagnosticNeverHoldsASecret_TS_30_31(t *testing.T) {
	e, _ := countingEnv(t)
	addWorkspace(t, e.db, "fork", func(r *workspaceRow) {
		r.gitURL = "https://github.com/me/widgets"
		carryPatch("https://github.com/up/widgets")(r)
	})

	rng := rand.New(rand.NewPCG(30, 31))
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~!$&'()*+,;=:@é世"
	random := func() string {
		runes := []rune(alphabet)
		out := make([]rune, 12+rng.IntN(30))
		for i := range out {
			out[i] = runes[rng.IntN(len(runes))]
		}
		return "S" + string(out)
	}
	auth := func(kind int) transport.AuthMethod {
		switch kind {
		case 0:
			return &githttp.BasicAuth{Username: patUsername, Password: random()}
		case 1:
			return &githttp.BasicAuth{Username: random(), Password: random()}
		}
		return nil
	}

	for i := range 300 {
		origin, upstream := auth(rng.IntN(3)), auth(rng.IntN(3))
		e.originAuth, e.upstreamAuth = origin, upstream
		_, body := getDiagnostic(t, e, "fork")
		for _, a := range []transport.AuthMethod{origin, upstream} {
			if basic, ok := a.(*githttp.BasicAuth); ok {
				for _, secret := range []string{basic.Password, basic.Username} {
					if secret != patUsername && strings.Contains(body, secret) {
						t.Fatalf("iteration %d: the body holds %q: %s", i, secret, body)
					}
				}
			}
		}
		var d diagnostic
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{d.Credential, d.Upstream.Credential} {
			if kind != "pat" && kind != "basic" && kind != "none" {
				t.Fatalf("iteration %d: credential = %q; want pat, basic or none", i, kind)
			}
		}
	}
}

// The API URL a forge is reached at when no variable names one: the public API of
// github.com and gitlab.com, and /api/v3 or /api/v4 on the forge's own host.
// Requirement: 30-REQ-6.1
func TestDefaultAPIURL(t *testing.T) {
	cases := []struct {
		forge issuex.ForgeType
		host  string
		want  string
	}{
		{issuex.ForgeTypeGitHub, "github.com", "https://api.github.com"},
		{issuex.ForgeTypeGitHub, "github.corp.example", "https://github.corp.example/api/v3"},
		{issuex.ForgeTypeGitLab, "gitlab.com", "https://gitlab.com/api/v4"},
		{issuex.ForgeTypeGitLab, "gitlab.corp.example", "https://gitlab.corp.example/api/v4"},
	}
	for _, tc := range cases {
		if got := defaultAPIURL(tc.forge, tc.host); got != tc.want {
			t.Errorf("defaultAPIURL(%q, %q) = %q; want %q", tc.forge, tc.host, got, tc.want)
		}
	}
}

// The answer describes what a read would do: a host the diagnostic classifies as GitHub
// is read as GitHub by the routes (the same API URL reaches the same stub).
// Requirement: 30-REQ-6.1
func TestDiagnosticAgreesWithTheReads(t *testing.T) {
	e := newEnv(t)
	e.stub.answerRepository()
	addWorkspace(t, e.db, "widgets")
	e.setVar("widgets", "FORGE_API_URL", e.stub.URL)

	d, _ := getDiagnostic(t, e, "widgets")
	if d.Forge != "github" || d.APIURL == nil || *d.APIURL != e.stub.URL {
		t.Fatalf("diagnostic = %+v %v; want github at the stub", d.Forge, d.APIURL)
	}
	if e.stub.count() != 0 {
		t.Fatalf("the diagnostic sent %d requests", e.stub.count())
	}
	if rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth()); rec.Code != http.StatusOK {
		t.Fatalf("the read answered %d: %s", rec.Code, rec.Body)
	}
	if reqs := e.stub.received(); len(reqs) != 1 || !strings.HasPrefix(reqs[0].Path, "/repos/") {
		t.Errorf("the read sent %+v; want one GitHub request", reqs)
	}
}

// The API URL is shown as issuex uses it, without a trailing slash; an invalid variable
// is reported before anything else about the remote, an unparseable URL included.
// Requirement: 30-REQ-6.1, 30-REQ-6.5
func TestDiagnosticTrimsTheAPIURLAndChecksTheVariableFirst(t *testing.T) {
	e, _ := countingEnv(t)
	addWorkspace(t, e.db, "slash", func(r *workspaceRow) { r.gitURL = "https://gitlab.example.com/group/widgets" })
	e.setVar("slash", "FORGE_API_URL", "https://gitlab.example.com/api/v4/")
	d, _ := getDiagnostic(t, e, "slash")
	if d.APIURL == nil || *d.APIURL != "https://gitlab.example.com/api/v4" {
		t.Errorf("api_url = %v; want the value without its trailing slash", d.APIURL)
	}

	addWorkspace(t, e.db, "bad", func(r *workspaceRow) { r.gitURL = "https://host" })
	e.setVar("bad", "FORGE_API_URL", "http://insecure")
	if rec := e.get("/workspaces/bad/forge", apiKeyAuth()); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d; want 422 although the remote is unparseable too", rec.Code)
	}
}
