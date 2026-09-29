package gitserver

import (
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runGitCmd runs a git command with an isolated config and fails the test on
// error. It returns trimmed combined output.
func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmdOutput(dir, args...)
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return out
}

func gitCmdOutput(dir string, args ...string) (string, error) {
	full := append([]string{"-c", "user.name=test", "-c", "user.email=test@test.com"}, args...)
	cmd := exec.Command("git", full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// newGitClientServer starts a real HTTP server exposing the git routes and
// returns an authenticated remote URL for the workspace "myws" in org "myorg".
func newGitClientServer(t *testing.T) (*gitTestEnv, string, string) {
	t.Helper()
	env := newGitTestEnv(t)
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "myws", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.setCloneStatus(t, "myws", "ready")
	trunk := env.initWorkspaceRepo(t, "myws")

	srv := httptest.NewServer(env.echo)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("x-token-auth", "af_key_user1")
	u.Path = "/git/myorg/myws.git"
	return env, trunk, u.String()
}

// TestGitClient_Clone verifies a real git client can clone from the hub.
func TestGitClient_Clone(t *testing.T) {
	_, trunk, remote := newGitClientServer(t)
	dst := t.TempDir()
	runGitCmd(t, "", "clone", remote, dst)

	want := runGitCmd(t, trunk, "rev-parse", "HEAD")
	got := runGitCmd(t, dst, "rev-parse", "HEAD")
	if got != want {
		t.Errorf("cloned HEAD = %s; want %s", got, want)
	}
}

// TestGitClient_FetchWithHaves reproduces the incremental-fetch scenario:
// the local repository already has part of the history (so the client sends
// "have" lines and negotiates over multiple stateless-rpc rounds) and fetches
// a new branch from the hub. Previously this failed with
// "fatal: protocol error: bad line length character: PACK" because the
// server sent a packfile in response to a negotiation-only request.
func TestGitClient_FetchWithHaves(t *testing.T) {
	for _, proto := range []string{"0", "2"} {
		t.Run("protocol.version="+proto, func(t *testing.T) {
			testFetchWithHaves(t, proto)
		})
	}
}

func testFetchWithHaves(t *testing.T, proto string) {
	_, trunk, remote := newGitClientServer(t)

	// Local repo shares the initial history with the hub.
	local := t.TempDir()
	runGitCmd(t, "", "clone", remote, local)

	// Create a "deploy" branch on the hub with extra commits, plus local
	// commits the hub does not know about (so negotiation has haves that
	// the server cannot ACK).
	runGitCmd(t, trunk, "checkout", "-q", "-b", "deploy")
	for i := 0; i < 3; i++ {
		f := trunk + "/deploy" + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, trunk, "add", ".")
		runGitCmd(t, trunk, "commit", "-q", "-m", "deploy commit")
	}
	// Enough local-only commits that the client needs several negotiation
	// rounds and gzip-encodes the request body.
	for i := 0; i < 120; i++ {
		f := local + "/local" + string(rune('a'+i%26)) + ".txt"
		if err := os.WriteFile(f, []byte(strings.Repeat("y", i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitCmd(t, local, "add", ".")
		runGitCmd(t, local, "commit", "-q", "-m", "local commit")
	}

	runGitCmd(t, local, "remote", "add", "hub", remote)
	if out, err := gitCmdOutput(local, "-c", "protocol.version="+proto, "fetch", "hub", "deploy"); err != nil {
		t.Fatalf("git fetch hub deploy failed: %v\n%s", err, out)
	}

	want := runGitCmd(t, trunk, "rev-parse", "deploy")
	got := runGitCmd(t, local, "rev-parse", "hub/deploy")
	if got != want {
		t.Errorf("hub/deploy = %s; want %s", got, want)
	}

	// Checking out the fetched branch must work.
	runGitCmd(t, local, "checkout", "-q", "-b", "deploy", "hub/deploy")
}
