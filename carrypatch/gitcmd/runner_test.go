package gitcmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNew_RejectsUnusableWorkDir(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, parent, "file", "x")
	for _, dir := range []string{"", filepath.Join(parent, "missing"), filepath.Join(parent, "file")} {
		if r, err := New(dir, nil); err == nil || r != nil {
			t.Errorf("New(%q) = %v, %v; want nil, error", dir, r, err)
		}
	}
}

// New needs a directory, not a repository.
func TestNew_AcceptsNonGitDirectory(t *testing.T) {
	newRunner(t, t.TempDir())
}

func TestNew_RequiresGitOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if r, err := New(t.TempDir(), nil); err == nil || r != nil {
		t.Fatalf("New = %v, %v without git on PATH; want nil, error", r, err)
	}
}

func TestNew_RejectsOldOrUnparsableGit(t *testing.T) {
	tests := []struct {
		name, output string
		wantInErr    []string
	}{
		{"below 2.38", "git version 2.37.0", []string{"2.37", "2.38"}},
		{"unparsable", "totally bogus output", []string{"unrecognized", "totally bogus output"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeGit(t, "echo '"+tt.output+"'")
			r, err := New(t.TempDir(), nil)
			if err == nil || r != nil {
				t.Fatalf("New = %v, %v; want nil, error", r, err)
			}
			for _, want := range tt.wantInErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// The subprocess environment drops variables that redirect git elsewhere,
// defaults the committer identity only where nothing set one, and appends the
// safety variables last so they beat any caller-supplied value.
func TestAssembleEnv_StripsRedirectsDefaultsIdentityPinsSafetyVars(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_WORK_TREE", "/elsewhere")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.hooksPath=/tmp/evil'")
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "ops@example.com")

	env := assembleEnv([]string{
		"GIT_AUTHOR_EMAIL=extra@example.com",
		"GIT_ALLOW_PROTOCOL=https",
		"GIT_TERMINAL_PROMPT=1", "GIT_TERMINAL_PROMPT=2",
		"GIT_CONFIG_NOSYSTEM=0",
	})

	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_PARAMETERS"} {
		if _, found := lastEnvValue(env, key); found {
			t.Errorf("%s must be stripped from the git environment", key)
		}
	}
	want := map[string]string{
		"GIT_AUTHOR_NAME":     "",                  // set (empty) by the process: kept
		"GIT_AUTHOR_EMAIL":    "extra@example.com", // from extraEnv: kept
		"GIT_COMMITTER_EMAIL": "ops@example.com",   // from the process: kept
		"GIT_COMMITTER_NAME":  DefaultIdentityName, // absent everywhere: defaulted
		"GIT_EDITOR":          "true",
		"GIT_PAGER":           "cat",
		"LC_ALL":              "C",
		"GIT_ALLOW_PROTOCOL":  "file:https:ssh", // safety vars win over extraEnv
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_CONFIG_NOSYSTEM": "1",
	}
	for key, w := range want {
		if got, found := lastEnvValue(env, key); !found || got != w {
			t.Errorf("%s = %q (found=%v), want %q", key, got, found, w)
		}
	}
}

// Every git the runner spawns gets the assembled environment.
func TestRun_SubprocessSeesSafetyEnv(t *testing.T) {
	dir := initRepo(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "env")
	fakeGit(t, "env > '"+capture+"'\nexec '"+realGit+"' \"$@\"")

	if _, err := newRunner(t, dir).Run(context.Background(), "rev-parse", "HEAD"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(capture)
	for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_ALLOW_PROTOCOL=file:https:ssh", "LC_ALL=C"} {
		if !strings.Contains(string(got), want+"\n") {
			t.Errorf("subprocess environment lacks %s", want)
		}
	}
}

func TestRun_ReturnsTrimmedStdout(t *testing.T) {
	out, err := newRunner(t, initRepo(t)).Run(context.Background(), "rev-parse", "--git-dir")
	if err != nil || out != ".git" {
		t.Fatalf("Run(rev-parse --git-dir) = %q, %v; want .git", out, err)
	}
}

// A failing git command becomes a *GitError carrying the arguments, exit code
// and trimmed stderr, all of which its message repeats.
func TestRun_NonZeroExitIsGitError(t *testing.T) {
	r := newRunner(t, initRepo(t))
	out, err := r.Run(context.Background(), "cat-file", "-t", "deadbeef")
	if out != "" {
		t.Errorf("stdout = %q on failure, want empty", out)
	}
	ge := asGitError(t, err)
	if ge.Args[0] != "cat-file" {
		t.Errorf("Args = %v, want the cat-file invocation", ge.Args)
	}
	if ge.Stderr == "" || ge.Stderr != strings.TrimSpace(ge.Stderr) {
		t.Errorf("Stderr = %q, want trimmed and non-empty", ge.Stderr)
	}
	msg := ge.Error()
	for _, want := range []string{"cat-file", strconv.Itoa(ge.ExitCode), ge.Stderr} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want it to contain %q", msg, want)
		}
	}
	// git with no arguments fails the same way.
	_, err = r.Run(context.Background())
	asGitError(t, err)
}

func TestGitError_RedactsURLUserinfo(t *testing.T) {
	e := &GitError{
		Args:     []string{"remote", "add", "upstream", "https://bot:ghp_secret@github.com/o/r.git"},
		ExitCode: 128,
		Stderr:   "fatal: unable to access 'https://bot:ghp_secret@github.com/o/r.git/': boom",
	}
	msg := e.Error()
	if strings.Contains(msg, "ghp_secret") || !strings.Contains(msg, "https://***@github.com/o/r.git") {
		t.Fatalf("GitError.Error() = %q; want the userinfo redacted", msg)
	}
}

// A deadline, from the context or from DefaultTimeout when the context has
// none, kills git and the helpers it spawned (here a shell holding the pipes
// open), so Run returns the context error promptly.
func TestRun_DeadlineKillsProcessGroup(t *testing.T) {
	r := newRunner(t, t.TempDir())
	deadline, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	old := DefaultTimeout
	DefaultTimeout = 300 * time.Millisecond
	t.Cleanup(func() { DefaultTimeout = old })

	for name, ctx := range map[string]context.Context{"context deadline": deadline, "DefaultTimeout": context.Background()} {
		start := time.Now()
		out, err := r.Run(ctx, "-c", "alias.hang=!sleep 30", "hang")
		if !errors.Is(err, context.DeadlineExceeded) || out != "" {
			t.Errorf("%s: Run = %q, %v; want \"\", context.DeadlineExceeded", name, out, err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("%s: Run returned after %s; git's child processes were not killed", name, elapsed)
		}
	}
}

func TestRun_IsSafeForConcurrentUse(t *testing.T) {
	dir := initRepo(t)
	want := runGit(t, dir, "rev-parse", "HEAD")
	r := newRunner(t, dir)
	results := make([]string, 20)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = r.Run(context.Background(), "rev-parse", "HEAD")
		}()
	}
	wg.Wait()
	for i, got := range results {
		if got != want {
			t.Errorf("goroutine %d: HEAD = %q, want %q", i, got, want)
		}
	}
}

func TestResetInProgressState_ClearsConflictAndDirtyTree(t *testing.T) {
	dir, mainSHA, featureSHA := divergedRepo(t, true)
	if _, err := tryGit(dir, "cherry-pick", featureSHA); err == nil {
		t.Fatal("cherry-pick did not conflict")
	}
	newRunner(t, dir).ResetInProgressState(context.Background())

	if exists(filepath.Join(dir, ".git", "CHERRY_PICK_HEAD")) {
		t.Error("CHERRY_PICK_HEAD still exists")
	}
	if st := runGit(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("working tree not clean:\n%s", st)
	}
	if head := runGit(t, dir, "rev-parse", "HEAD"); head != mainSHA {
		t.Errorf("HEAD = %s, want %s", head, mainSHA)
	}
}
