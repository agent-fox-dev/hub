package gitcmd

import (
	"context"
	"testing"
)

func TestConfigSet_WritesRepositoryConfig(t *testing.T) {
	dir := initRepo(t)
	if err := newRunner(t, dir).ConfigSet(context.Background(), "rerere.enabled", "true"); err != nil {
		t.Fatalf("ConfigSet: %v", err)
	}
	if got := runGit(t, dir, "config", "--local", "rerere.enabled"); got != "true" {
		t.Errorf("rerere.enabled = %q, want true", got)
	}
}

// An invalid key, or an empty key or value, is a GitError.
func TestConfigSet_RejectsBadInput(t *testing.T) {
	r := newRunner(t, initRepo(t))
	for _, c := range [][2]string{{"--invalid-key", "v"}, {"", "v"}, {"rerere.enabled", ""}} {
		asGitError(t, r.ConfigSet(context.Background(), c[0], c[1]))
	}
}
