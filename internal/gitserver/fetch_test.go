package gitserver

import (
	"strings"
	"testing"
)

// A git client clones the workspace repository from the hub.
func TestFetch_Clone(t *testing.T) {
	v := newEnv(t)
	dst := clone(t, v.remote(t, v.key(t, "owner")))
	if want, got := gitRun(t, v.trunk("ws"), "rev-parse", "HEAD"), gitRun(t, dst, "rev-parse", "HEAD"); got != want {
		t.Errorf("cloned HEAD = %s; want %s", got, want)
	}
}

// A client that already holds part of the history negotiates over several
// stateless rounds (sending haves the hub cannot acknowledge, gzip-encoded
// once large) and receives only the new objects, with either protocol
// version.
func TestFetch_IncrementalWithHaves(t *testing.T) {
	for _, proto := range []string{"0", "2"} {
		t.Run("protocol "+proto, func(t *testing.T) {
			v := newEnv(t)
			remote := v.remote(t, v.key(t, "owner"))
			trunk := v.trunk("ws")
			local := clone(t, remote)
			gitRun(t, trunk, "checkout", "-q", "-b", "deploy")
			for i := 0; i < 3; i++ {
				addCommit(t, trunk, "deploy"+string(rune('a'+i))+".txt", "deploy")
			}
			for i := 0; i < 120; i++ {
				addCommit(t, local, "local"+string(rune('a'+i%26))+".txt", strings.Repeat("y", i+1))
			}
			if out, err := gitTry(local, "-c", "protocol.version="+proto, "fetch", remote, "deploy"); err != nil {
				t.Fatalf("fetch: %v\n%s", err, out)
			}
			if want, got := gitRun(t, trunk, "rev-parse", "deploy"), gitRun(t, local, "rev-parse", "FETCH_HEAD"); got != want {
				t.Errorf("FETCH_HEAD = %s; want %s", got, want)
			}
		})
	}
}

// The hub's backup refs are advertised and a client fetches one by name.
func TestFetch_BackupRefByName(t *testing.T) {
	v := newEnv(t)
	remote := v.remote(t, v.key(t, "owner"))
	trunk := v.trunk("ws")
	backup := addCommit(t, trunk, "backup.txt", "backup only")
	gitRun(t, trunk, "update-ref", "refs/hub/replaced/feature", backup)
	gitRun(t, trunk, "reset", "-q", "--hard", "HEAD~1")
	if out := gitRun(t, "", "ls-remote", remote); !strings.Contains(out, backup+"\trefs/hub/replaced/feature") {
		t.Errorf("ls-remote:\n%s\nwant %s refs/hub/replaced/feature", out, backup)
	}
	dst := t.TempDir()
	gitRun(t, dst, "init", "-q")
	gitRun(t, dst, "fetch", "-q", remote, "refs/hub/replaced/feature")
	if got := gitRun(t, dst, "rev-parse", "FETCH_HEAD"); got != backup {
		t.Errorf("FETCH_HEAD = %s; want %s", got, backup)
	}
}
