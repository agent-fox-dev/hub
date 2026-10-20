package forge

import (
	"net/http"
	"testing"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// TS-30-19: Credential kinds pat, basic and none produce the right token handling.
// Requirement: 30-REQ-4.5
func TestCredentialKinds_TS_30_19(t *testing.T) {
	cases := []struct {
		name      string
		auth      transport.AuthMethod
		wantToken string
		wantKind  credentialKind
	}{
		{"pat", &githttp.BasicAuth{Username: "x-token-auth", Password: "tok-pat"}, "tok-pat", credentialPAT},
		{"basic", &githttp.BasicAuth{Username: "alice", Password: "tok-pass"}, "tok-pass", credentialBasic},
		{"none", nil, "", credentialNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []issuex.Options
			e := newEnv(t, optionsRecorder(&got))
			e.originAuth = tc.auth
			addWorkspace(t, e.db, "widgets")
			e.setVar("widgets", "FORGE_API_URL", "https://api.example.test/v3")

			rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth())
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
			}
			if len(got) != 1 {
				t.Fatalf("NewClient was called %d times; want 1", len(got))
			}
			o := got[0]
			if o.Token != tc.wantToken {
				t.Errorf("Options.Token = %q; want %q", o.Token, tc.wantToken)
			}
			if o.BaseURL != "https://api.example.test/v3" {
				t.Errorf("Options.BaseURL = %q; want the FORGE_API_URL value", o.BaseURL)
			}
			if o.RemoteURL != "https://github.com/acme/widgets" || o.Repo != (issuex.Repo{Host: "github.com", Owner: "acme", Name: "widgets"}) {
				t.Errorf("Options.RemoteURL, Repo = %q, %+v; want the git_url and its repository", o.RemoteURL, o.Repo)
			}
			if o.HTTPClient == nil {
				t.Error("Options.HTTPClient is nil; want the shared forge client")
			}
			if kind, _ := credentialOf(tc.auth); kind != tc.wantKind {
				t.Errorf("credentialOf = %q; want %q", kind, tc.wantKind)
			}
		})
	}

	// With the real client, a workspace without a credential sends no token header
	// and the other two send theirs.
	for _, tc := range cases {
		t.Run("stub/"+tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.stub.answerRepository()
			e.originAuth = tc.auth
			addWorkspace(t, e.db, "widgets")
			e.setVar("widgets", "FORGE_API_URL", e.stub.URL)

			if rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth()); rec.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
			}
			reqs := e.stub.received()
			if len(reqs) != 1 {
				t.Fatalf("the stub received %d requests; want 1", len(reqs))
			}
			if got := bearer(reqs[0]); got != tc.wantToken {
				t.Errorf("the stub saw the token %q; want %q", got, tc.wantToken)
			}
			if tc.wantToken == "" && (reqs[0].Header.Get("Authorization") != "" || reqs[0].Header.Get("PRIVATE-TOKEN") != "") {
				t.Errorf("an unauthenticated workspace sent a token header: %v", reqs[0].Header)
			}
		})
	}
}

// The username decides the kind: x-token-auth is a PAT, any other username makes the
// password a token of a basic credential, and no credential is none. A method that is
// not basic auth is none.
// Requirement: 30-REQ-4.5
func TestCredentialOf(t *testing.T) {
	var nilBasic *githttp.BasicAuth
	cases := []struct {
		name      string
		auth      transport.AuthMethod
		wantKind  credentialKind
		wantToken string
	}{
		{"nil", nil, credentialNone, ""},
		{"typed nil", nilBasic, credentialNone, ""},
		{"x-token-auth", &githttp.BasicAuth{Username: "x-token-auth", Password: "p"}, credentialPAT, "p"},
		{"another username", &githttp.BasicAuth{Username: "x-token-auth2", Password: "q"}, credentialBasic, "q"},
		{"empty username", &githttp.BasicAuth{Password: "r"}, credentialBasic, "r"},
		{"another method", &githttp.TokenAuth{Token: "t"}, credentialNone, ""},
	}
	for _, tc := range cases {
		kind, token := credentialOf(tc.auth)
		if kind != tc.wantKind || token != tc.wantToken {
			t.Errorf("%s: credentialOf = %q, %q; want %q, %q", tc.name, kind, token, tc.wantKind, tc.wantToken)
		}
	}
}

// The API URL variable is trimmed before it is checked and passed on, and the
// userinfo a git_url may carry is neither used as a credential nor handed to issuex.
// Requirement: 30-REQ-4.3, 30-REQ-4.5
func TestAPIURLIsTrimmedAndRemoteUserinfoIsDropped(t *testing.T) {
	var got []issuex.Options
	e := newEnv(t, optionsRecorder(&got))
	e.originAuth = nil
	addWorkspace(t, e.db, "widgets", func(r *workspaceRow) { r.gitURL = "https://tok:en@github.com/acme/widgets" })
	e.setVar("widgets", "FORGE_API_URL", "  https://api.example.test/v3\n")

	if rec := e.get("/workspaces/widgets/forge/repository", apiKeyAuth()); rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	if len(got) != 1 {
		t.Fatalf("NewClient was called %d times; want 1", len(got))
	}
	if got[0].BaseURL != "https://api.example.test/v3" {
		t.Errorf("Options.BaseURL = %q; want the trimmed value", got[0].BaseURL)
	}
	if got[0].RemoteURL != "https://github.com/acme/widgets" || got[0].Token != "" {
		t.Errorf("Options.RemoteURL, Token = %q, %q; want the URL without userinfo and no token", got[0].RemoteURL, got[0].Token)
	}
}
