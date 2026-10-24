package gitcmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit runs git with args in dir (the current directory when dir is "")
// and returns its trimmed combined output, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := tryGit(dir, args...)
	if err != nil {
		t.Fatalf("git %v (dir=%s): %v\n%s", args, dir, err, out)
	}
	return out
}

// tryGit runs git like runGit but reports failure instead of ending the test.
func tryGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// commit writes files into dir, stages everything and commits with msg,
// returning the new HEAD SHA.
func commit(t *testing.T, dir, msg string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		writeFile(t, dir, name, content)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", msg)
	return runGit(t, dir, "rev-parse", "HEAD")
}

// initRepo creates a repository on branch main with one commit ("base") that
// adds base.txt and second.txt. gc.auto is off so no background gc races the
// TempDir cleanup.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, "", "init", "-q", "-b", "main", dir)
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "gc.auto", "0")
	commit(t, dir, "base", map[string]string{"base.txt": "base", "second.txt": "second"})
	return dir
}

// divergedRepo is initRepo with a feature branch and main that each add one
// commit after base. With conflict=false they touch different files
// (feature.txt, main.txt); with conflict=true both rewrite base.txt and
// second.txt. HEAD is left on main.
func divergedRepo(t *testing.T, conflict bool) (dir, mainSHA, featureSHA string) {
	t.Helper()
	dir = initRepo(t)
	featureFiles := map[string]string{"feature.txt": "feature"}
	mainFiles := map[string]string{"main.txt": "main"}
	if conflict {
		featureFiles = map[string]string{"base.txt": "feature", "second.txt": "feature"}
		mainFiles = map[string]string{"base.txt": "main", "second.txt": "main"}
	}
	runGit(t, dir, "checkout", "-q", "-b", "feature")
	featureSHA = commit(t, dir, "feature change", featureFiles)
	runGit(t, dir, "checkout", "-q", "main")
	mainSHA = commit(t, dir, "main change", mainFiles)
	return dir, mainSHA, featureSHA
}

// newRunner constructs a GitRunner on dir with the default environment.
func newRunner(t *testing.T, dir string) *GitRunner {
	t.Helper()
	r, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New(%q): %v", dir, err)
	}
	return r
}

// fakeGit puts a shell script named git first on PATH for the rest of the
// test; body is the shell snippet it runs in place of the real git.
func fakeGit(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// lastEnvValue returns the value of the last KEY=VALUE entry for key, which is
// the one git sees when a key is repeated.
func lastEnvValue(env []string, key string) (val string, found bool) {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			val, found = v, true
		}
	}
	return val, found
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// asGitError fails the test unless err is a *GitError with a non-zero exit
// code (git failed, or the runner refused the call before running git).
func asGitError(t *testing.T, err error) *GitError {
	t.Helper()
	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error is %T (%v), want *GitError", err, err)
	}
	if ge.ExitCode == 0 {
		t.Errorf("GitError.ExitCode = 0, want non-zero: %v", ge)
	}
	return ge
}
