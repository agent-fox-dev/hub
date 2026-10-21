package gitserver

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TS-33-60 (smoke, 33-PATH-5): A bound token fetches its own repository and
// is refused another through the running git server, with the git binary.
// Real: Echo server, git auth middleware, git resolver, authorizeGitAccess,
// git, SQLite.
func TestSmoke_BoundTokenGitFetch_TS_33_60(t *testing.T) {
	env := newBindingGitEnv(t)
	seedGitPAT(t, env.db, "smokebound", "user-a", "git:read")
	bindGitToken(t, env.db, "smokebound", "s")
	srv := httptest.NewServer(env.echo)
	defer srv.Close()

	remote := func(slug string) string {
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		u.User = url.UserPassword("x-token-auth", "af_pat_smokebound")
		u.Path = "/git/myorg/" + slug + ".git"
		return u.String()
	}

	clone := filepath.Join(t.TempDir(), "s")
	runGitCmd(t, "", "clone", remote("s"), clone)
	if _, err := os.Stat(filepath.Join(clone, "README.md")); err != nil {
		t.Errorf("the clone of s lacks README.md: %v", err)
	}

	out, err := gitCmdOutput("", "ls-remote", remote("x"))
	if err == nil {
		t.Fatalf("ls-remote of x succeeded: %s; want it refused", out)
	}
	if !strings.Contains(out, "repository not found") {
		t.Errorf("ls-remote of x: %q; want the pkt-line repository not found", out)
	}
}
