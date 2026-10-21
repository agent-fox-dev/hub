package workspace

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
)

var fortyHex = regexp.MustCompile(`^[0-9a-f]{40}$`)

// divergeBare adds a commit the trunk lacks on branch of the bare repository,
// which must already have the branch, and returns its id.
func divergeBare(t *testing.T, bare, branch string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "w")
	pushGit(t, filepath.Dir(work), "clone", "-q", "-b", branch, bare, work)
	sha := pushCommit(t, work, "forge-only.txt", "made on the forge")
	pushGit(t, work, "push", "-q", "origin", branch)
	return sha
}

// TS-38-1: An owner's push of an existing branch to a local bare origin answers
// 200 with branch, sha, remote, remote_url and result.
// Requirement: 38-REQ-1.1
func TestBranchPushOwnerPushes_TS_38_1(t *testing.T) {
	env := newBranchPushEnv(t)
	bare := env.seedPushWorkspace(t, &Workspace{Slug: "first", OwnerID: "user-a"}, "fix")

	a := pushedOK(t, env.push(t, "first", "fix", "", userAuth("user-a")))
	tip := env.trunkTip(t, "first", "fix")
	if a.Branch != "fix" || a.SHA != tip || !fortyHex.MatchString(a.SHA) || a.Remote != "origin" || a.Result != "pushed" || a.RemoteURL != bare {
		t.Errorf("answer %+v; want branch fix, sha %s, remote origin, remote_url %s, result pushed", a, tip, bare)
	}
	if got := bareRef(t, bare, "refs/heads/fix"); got != a.SHA {
		t.Errorf("origin's fix = %q; want %q", got, a.SHA)
	}
}

// genBranchName makes a valid branch name of up to three segments of letters,
// digits, '-', '_', '.' and '%', unique to i through its first segment.
func genBranchName(r *rand.Rand, i int) string {
	const chars = "abcxyz0189-_%"
	segments := []string{fmt.Sprintf("n%d", i)}
	for n := r.Intn(3); n > 0; n-- {
		seg := make([]byte, 1+r.Intn(6))
		for j := range seg {
			seg[j] = chars[r.Intn(len(chars))]
		}
		segments = append(segments, string(seg))
	}
	if r.Intn(2) == 0 {
		segments[len(segments)-1] += "%25" + string(chars[r.Intn(len(chars))])
	}
	return strings.Join(segments, "/")
}

// TS-38-2: Any valid branch name sent as one escaped segment is decoded
// exactly once.
// Requirement: 38-REQ-1.2
func TestBranchPushNameDecodedOnce_TS_38_2(t *testing.T) {
	env := newBranchPushEnv(t)
	bare := env.seedPushWorkspace(t, &Workspace{Slug: "names", OwnerID: "user-a"})
	trunk := filepath.Join(env.root, "names", "trunk")

	names := []string{"fix/42-nil-map", "a%25b", "100%", "x%2Fy"}
	r := rand.New(rand.NewSource(382))
	for i := 0; len(names) < 24; i++ {
		if name := genBranchName(r, i); gitcmd.ValidateRefName(name) == nil {
			names = append(names, name)
		}
	}
	for _, name := range names {
		pushGit(t, trunk, "branch", name)
		sent := url.PathEscape(name)
		a := pushedOK(t, env.push(t, "names", sent, "", userAuth("user-a")))
		if a.Branch != name {
			t.Errorf("sent %s: branch %q; want %q", sent, a.Branch, name)
		}
		if got := bareRef(t, bare, "refs/heads/"+name); got == "" || got != a.SHA {
			t.Errorf("sent %s: origin's refs/heads/%s = %q; want %q", sent, name, got, a.SHA)
		}
	}
	if sent := url.PathEscape("fix/42-nil-map"); sent != "fix%2F42-nil-map" {
		t.Errorf("escaping fix/42-nil-map gives %s", sent)
	}
	if bareRef(t, bare, "refs/heads/a%b") != "" {
		t.Errorf("a%%2525b was decoded twice: origin has %s", "refs/heads/a%b")
	}
}

// TS-38-4: An empty body, a body without force and a body with unknown fields
// all push non-forced.
// Requirement: 38-REQ-1.4
func TestBranchPushBodyDefaults_TS_38_4(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedPushWorkspace(t, &Workspace{Slug: "bodies", OwnerID: "user-a"}, "fix")
	for i, body := range []string{"", "{}", `{"colour":"red"}`, `{"force":false}`} {
		pushedOK(t, env.push(t, "bodies", "fix", body, userAuth("user-a")))
		call := (*env.pushes)[i]
		if len(call.refSpecs) != 1 || call.refSpecs[0] != "refs/heads/fix:refs/heads/fix" || call.force {
			t.Errorf("body %q: push %+v; want refs/heads/fix:refs/heads/fix without force", body, call)
		}
	}
}

// TS-38-6: result is pushed, then up_to_date on repeat, up_to_date even with
// force, and forced when force overwrote the forge.
// Requirement: 38-REQ-1.6
func TestBranchPushResults_TS_38_6(t *testing.T) {
	env := newBranchPushEnv(t)
	forge := newStandInForge(t)
	env.seedPushWorkspace(t, &Workspace{Slug: "results", OwnerID: "user-a"}, "feat", "other")
	setGitURL(t, env, "results", forge.repoURL())

	receivePacks := func() int { _, u := forge.received(); return len(u) }
	if a := pushedOK(t, env.push(t, "results", "feat", "", userAuth("user-a"))); a.Result != "pushed" {
		t.Errorf("first push: %+v; want pushed", a)
	}
	sent := receivePacks()
	if a := pushedOK(t, env.push(t, "results", "feat", "", userAuth("user-a"))); a.Result != "up_to_date" {
		t.Errorf("second push: %+v; want up_to_date", a)
	}
	if a := pushedOK(t, env.push(t, "results", "feat", `{"force":true}`, userAuth("user-a"))); a.Result != "up_to_date" {
		t.Errorf("forced push of an unchanged branch: %+v; want up_to_date", a)
	}
	if n := receivePacks(); n != sent {
		t.Errorf("the up_to_date pushes sent %d receive-pack requests; want none", n-sent)
	}

	pushedOK(t, env.push(t, "results", "other", "", userAuth("user-a")))
	divergeBare(t, forge.bare, "other")
	a := pushedOK(t, env.push(t, "results", "other", `{"force":true}`, userAuth("user-a")))
	if a.Result != "forced" {
		t.Errorf("forced push over a diverged branch: %+v; want forced", a)
	}
	if got := bareRef(t, forge.bare, "refs/heads/other"); got != env.trunkTip(t, "results", "other") {
		t.Errorf("origin's other = %q; want the trunk's tip", got)
	}
}

// TS-38-7: remote_url has userinfo stripped for scheme URLs and is unchanged
// for scp-style addresses.
// Requirement: 38-REQ-1.7
func TestBranchPushRemoteURL_TS_38_7(t *testing.T) {
	env := newBranchPushEnv(t)
	pushToOriginFn = func(context.Context, string, string, transport.AuthMethod, []config.RefSpec, bool, io.Writer) error {
		return nil
	}
	env.seedPushWorkspace(t, &Workspace{Slug: "url-http", OwnerID: "user-a"}, "feat")
	env.seedPushWorkspace(t, &Workspace{Slug: "url-scp", OwnerID: "user-a"}, "feat")
	setGitURL(t, env, "url-http", "http://user:secret@127.0.0.1:9/r.git")
	setGitURL(t, env, "url-scp", "git@github.com:acme/widgets.git")

	a := pushedOK(t, env.push(t, "url-http", "feat", "", userAuth("user-a")))
	if a.RemoteURL != "http://127.0.0.1:9/r.git" {
		t.Errorf("remote_url = %q; want http://127.0.0.1:9/r.git", a.RemoteURL)
	}
	a = pushedOK(t, env.push(t, "url-scp", "feat", "", userAuth("user-a")))
	if a.RemoteURL != "git@github.com:acme/widgets.git" {
		t.Errorf("remote_url = %q; want git@github.com:acme/widgets.git", a.RemoteURL)
	}
}

// TS-38-29: The push sends exactly the one refspec (plus form when forced) to
// origin at git_url with the stored credential.
// Requirement: 38-REQ-6.1
func TestBranchPushCredentialAndRefspec_TS_38_29(t *testing.T) {
	env := newBranchPushEnv(t)
	for _, tc := range []struct {
		slug    string
		secrets []string
		want    string
		force   bool
	}{
		{"cred-pat", []string{"GIT_PAT", "pat-1"}, "x-token-auth:pat-1", false},
		{"cred-basic", []string{"GIT_USERNAME", "u", "GIT_PASSWORD", "pw"}, "u:pw", false},
		{"cred-both", []string{"GIT_PAT", "pat-2", "GIT_USERNAME", "u", "GIT_PASSWORD", "pw"}, "x-token-auth:pat-2", false},
		{"cred-none", nil, "", false},
		{"cred-forced", []string{"GIT_PAT", "pat-3"}, "x-token-auth:pat-3", true},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			forge := newStandInForge(t)
			env.seedPushWorkspace(t, &Workspace{Slug: tc.slug, OwnerID: "user-a"}, "feat", "other")
			setGitURL(t, env, tc.slug, forge.repoURL())
			if tc.secrets != nil {
				storeGitSecrets(t, env, tc.slug, tc.secrets...)
			}
			*env.pushes = nil
			pushedOK(t, env.push(t, tc.slug, "feat", fmt.Sprintf(`{"force":%v}`, tc.force), userAuth("user-a")))

			auths, updates := forge.received()
			if len(updates) != 1 || !slices.Equal(updates[0], []string{"refs/heads/feat"}) {
				t.Errorf("ref updates %v; want one update of refs/heads/feat", updates)
			}
			for _, got := range auths {
				if got != tc.want {
					t.Errorf("a request carried credential %q; want %q", got, tc.want)
				}
			}
			want := config.RefSpec("refs/heads/feat:refs/heads/feat")
			if tc.force {
				want = "+" + want
			}
			if call := (*env.pushes)[0]; len(call.refSpecs) != 1 || call.refSpecs[0] != want || call.force != tc.force {
				t.Errorf("push %+v; want %s", call, want)
			}
			if got := bareRef(t, forge.bare, "refs/heads/other"); got != "" {
				t.Errorf("origin received other at %s", got)
			}
		})
	}
}

// TS-38-30: A secrets-store failure answers 500 internal server error without
// contacting the forge and without logging secrets.
// Requirement: 38-REQ-6.2
func TestBranchPushSecretsFailure_TS_38_30(t *testing.T) {
	env := newBranchPushEnv(t)
	logs := captureBranchPushLog(t)
	bare := env.seedPushWorkspace(t, &Workspace{Slug: "no-secrets", OwnerID: "user-a"}, "feat")
	storeGitSecrets(t, env, "no-secrets", "GIT_PAT", "tok-12345")
	if _, err := env.db.Exec(`DROP TABLE secrets`); err != nil {
		t.Fatal(err)
	}
	assertRefusal(t, env.push(t, "no-secrets", "feat", "", userAuth("user-a")), http.StatusInternalServerError, "internal server error", "")
	if refs := bareRefs(t, bare); len(refs) != 0 {
		t.Errorf("origin received %v", refs)
	}
	env.assertNothingHappened(t)
	assertLockFree(t, "no-secrets")
	if out := logs.String(); !strings.Contains(out, "no-secrets") || strings.Contains(out, "tok-12345") {
		t.Errorf("log %q; want the failure logged without the secret", out)
	}
}
