package carrypatch

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// isBranchResolveKind checks if an error is a branch resolve error with the
// given kind.
func isBranchResolveKind(err error, kind string) bool {
	var bre *branchResolveError
	if errors.As(err, &bre) {
		return bre.kind == kind
	}
	return false
}

// ===========================================================================
// Git helpers for branch resolver tests
// ===========================================================================

// initBareRepo creates a bare git repository at the given path.
func initBareRepo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	runGitCmd(t, "", "init", "--bare", path)
}

// cloneRepo clones src into dst.
func cloneRepo(t *testing.T, src, dst string) {
	t.Helper()
	runGitCmd(t, "", "clone", src, dst)
	configGitUserCmd(t, dst)
}

// commitFile creates or overwrites a file and commits it in the given repo.
func commitFile(t *testing.T, dir, filename, content, message string) string {
	t.Helper()
	writeFileHelper(t, filepath.Join(dir, filename), content)
	runGitCmd(t, dir, "add", filename)
	runGitCmd(t, dir, "commit", "-m", message)
	return runGitCmd(t, dir, "rev-parse", "HEAD")
}

// createBranchOnBare creates a branch on a bare repo by pushing from a
// temporary working clone.
func createBranchOnBare(t *testing.T, bareDir, branchName, filename, content, message string) string {
	t.Helper()
	tmpDir := t.TempDir()
	cloneDir := filepath.Join(tmpDir, "work")
	cloneRepo(t, bareDir, cloneDir)
	runGitCmd(t, cloneDir, "checkout", "-b", branchName)
	sha := commitFile(t, cloneDir, filename, content, message)
	runGitCmd(t, cloneDir, "push", "origin", branchName)
	return sha
}

// setupForkAndTrunk creates a bare "fork" repo with a main branch and an
// initial commit, then clones it as the "trunk" at <workspaceRoot>/<slug>/trunk.
// Returns the fork dir, trunk dir, and the initial commit SHA.
func setupForkAndTrunk(t *testing.T, workspaceRoot, slug string) (forkDir, trunkDir, initialSHA string) {
	t.Helper()
	forkDir = filepath.Join(t.TempDir(), "fork.git")
	initBareRepo(t, forkDir)

	// Create an initial commit on the bare repo via a temp clone.
	tmpDir := t.TempDir()
	tmpClone := filepath.Join(tmpDir, "init-clone")
	cloneRepo(t, forkDir, tmpClone)
	initialSHA = commitFile(t, tmpClone, "README.md", "initial", "initial commit")
	runGitCmd(t, tmpClone, "push", "origin", "main")

	// Clone the fork as the trunk.
	trunkDir = filepath.Join(workspaceRoot, slug, "trunk")
	if err := os.MkdirAll(filepath.Dir(trunkDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cloneRepo(t, forkDir, trunkDir)
	return forkDir, trunkDir, initialSHA
}

// revParse runs git rev-parse in the given dir.
func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", ref)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitHead returns the current HEAD ref name.
func gitHead(t *testing.T, dir string) string {
	t.Helper()
	return runGitCmd(t, dir, "symbolic-ref", "HEAD")
}

// gitStatus returns the working tree status.
func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	return runGitCmd(t, dir, "status", "--porcelain")
}

// gitConfigGet returns a git config value, or "" if not set.
func gitConfigGet(t *testing.T, dir, key string) string {
	t.Helper()
	cmd := exec.Command("git", "config", "--get", key)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ===========================================================================
// Counting stubs
// ===========================================================================

type countingFetchStub struct {
	calls int
}

func (s *countingFetchStub) fetch(_ context.Context, _, _ string, _ transport.AuthMethod) error {
	s.calls++
	return nil
}

type countingCredStub struct {
	calls int
}

func (s *countingCredStub) resolve(_ string) (transport.AuthMethod, error) {
	s.calls++
	return nil, nil
}

// ===========================================================================
// Recording runner wrapper
// ===========================================================================

// recordingRunner wraps a real GitRunner and records all Run calls.
type recordingRunner struct {
	inner GitRunner
	calls []runCall
}

func (r *recordingRunner) Run(ctx context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, runCall{Args: append([]string{}, args...)})
	return r.inner.Run(ctx, args...)
}

func (r *recordingRunner) CherryPick(ctx context.Context, commitSHA string) error {
	return r.inner.CherryPick(ctx, commitSHA)
}

func (r *recordingRunner) MergeNoFF(ctx context.Context, ref, message string) error {
	return r.inner.MergeNoFF(ctx, ref, message)
}

func (r *recordingRunner) MergeTree(ctx context.Context, base, head string) (string, error) {
	return r.inner.MergeTree(ctx, base, head)
}

func (r *recordingRunner) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	return r.inner.IsAncestor(ctx, ancestor, descendant)
}

func (r *recordingRunner) Cherry(ctx context.Context, upstream, head string) ([]string, []string, error) {
	return r.inner.Cherry(ctx, upstream, head)
}

func (r *recordingRunner) HardReset(ctx context.Context, ref string) error {
	return r.inner.HardReset(ctx, ref)
}

func (r *recordingRunner) WorktreeAdd(ctx context.Context, path, commit string) error {
	return r.inner.WorktreeAdd(ctx, path, commit)
}

func (r *recordingRunner) WorktreeRemove(ctx context.Context, path string) error {
	return r.inner.WorktreeRemove(ctx, path)
}

func (r *recordingRunner) WorktreePrune(ctx context.Context) error {
	return r.inner.WorktreePrune(ctx)
}

func (r *recordingRunner) UpdateRef(ctx context.Context, ref, sha string) error {
	return r.inner.UpdateRef(ctx, ref, sha)
}

var _ GitRunner = (*recordingRunner)(nil)

// ===========================================================================
// TS-21-1: A branch that already exists locally resolves as local with no
// write, lock or fetch.
// Verifies: 21-REQ-1.1
// ===========================================================================

func TestTS21_1_LocalBranchResolvesAsLocal(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "ts21-1"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a branch on the fork and fetch it into the trunk as a local branch.
	branchSHA := createBranchOnBare(t, forkDir, "feat", "feat.txt", "feat content", "add feat")
	runGitCmd(t, trunkDir, "fetch", "origin")
	runGitCmd(t, trunkDir, "branch", "feat", "origin/feat")

	before := revParse(t, trunkDir, "refs/heads/feat")
	if before == "" {
		t.Fatal("refs/heads/feat should exist before the test")
	}
	if before != branchSHA {
		t.Fatalf("refs/heads/feat = %s; want %s", before, branchSHA)
	}

	fetchStub := &countingFetchStub{}
	credStub := &countingCredStub{}

	// Set PATCH_BRANCH_SOURCE=origin so we can verify no fetch happens.
	getVar := func(scope, s, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "origin", nil
		}
		return "", fmt.Errorf("not found")
	}

	hook := NewBranchResolverHook(
		NewGitRunnerFactory(),
		workspaceRoot,
		getVar,
		credStub.resolve,
		fetchStub.fetch,
	)

	method, err := hook(context.Background(), slug, "feat")
	if err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if method != "local" {
		t.Errorf("method = %q; want %q", method, "local")
	}
	if fetchStub.calls != 0 {
		t.Errorf("fetch calls = %d; want 0", fetchStub.calls)
	}
	if credStub.calls != 0 {
		t.Errorf("credential calls = %d; want 0", credStub.calls)
	}

	after := revParse(t, trunkDir, "refs/heads/feat")
	if after != before {
		t.Errorf("refs/heads/feat changed from %s to %s", before, after)
	}
}

// ===========================================================================
// TS-21-2: A tracking-only branch gets a local ref in both modes without
// touching HEAD or the working tree.
// Verifies: 21-REQ-1.2, 21-REQ-2.1
// ===========================================================================

func TestTS21_2_TrackingOnlyBranchCreatesLocalRef(t *testing.T) {
	for _, mode := range []string{"hub", "origin"} {
		t.Run("mode="+mode, func(t *testing.T) {
			workspaceRoot := t.TempDir()
			slug := "ts21-2-" + mode

			forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

			// Create a branch on the fork so it appears as a tracking ref.
			createBranchOnBare(t, forkDir, "feat", "feat.txt", "feat content", "add feat")
			runGitCmd(t, trunkDir, "fetch", "origin")

			// Verify refs/heads/feat does NOT exist but refs/remotes/origin/feat does.
			if revParse(t, trunkDir, "refs/heads/feat") != "" {
				t.Fatal("refs/heads/feat should not exist before the test")
			}
			trackingTip := revParse(t, trunkDir, "refs/remotes/origin/feat")
			if trackingTip == "" {
				t.Fatal("refs/remotes/origin/feat should exist")
			}

			headBefore := gitHead(t, trunkDir)
			statusBefore := gitStatus(t, trunkDir)

			fetchStub := &countingFetchStub{}
			credStub := &countingCredStub{}

			getVar := func(scope, s, key string) (string, error) {
				if key == "PATCH_BRANCH_SOURCE" {
					return mode, nil
				}
				return "", fmt.Errorf("not found")
			}

			// Use a recording runner to verify no checkout/branch/reset.
			realFactory := NewGitRunnerFactory()
			var recorder *recordingRunner
			wrappedFactory := func(repoPath string) (GitRunner, error) {
				real, err := realFactory(repoPath)
				if err != nil {
					return nil, err
				}
				recorder = &recordingRunner{inner: real}
				return recorder, nil
			}

			hook := NewBranchResolverHook(
				wrappedFactory,
				workspaceRoot,
				getVar,
				credStub.resolve,
				fetchStub.fetch,
			)

			method, err := hook(context.Background(), slug, "feat")
			if err != nil {
				t.Fatalf("hook returned error: %v", err)
			}
			if method != "origin_tracking" {
				t.Errorf("method = %q; want %q", method, "origin_tracking")
			}

			// Verify refs/heads/feat was created at the tracking tip.
			localRef := revParse(t, trunkDir, "refs/heads/feat")
			if localRef != trackingTip {
				t.Errorf("refs/heads/feat = %s; want %s (tracking tip)", localRef, trackingTip)
			}

			// Verify HEAD and working tree are unchanged.
			headAfter := gitHead(t, trunkDir)
			if headAfter != headBefore {
				t.Errorf("HEAD changed from %s to %s", headBefore, headAfter)
			}
			statusAfter := gitStatus(t, trunkDir)
			if statusAfter != statusBefore {
				t.Errorf("git status changed from %q to %q", statusBefore, statusAfter)
			}

			// Verify no fetch occurred.
			if fetchStub.calls != 0 {
				t.Errorf("fetch calls = %d; want 0", fetchStub.calls)
			}

			// Verify no checkout/branch/reset/switch command was run.
			if recorder != nil {
				for _, call := range recorder.calls {
					if len(call.Args) > 0 {
						cmd := call.Args[0]
						if cmd == "checkout" || cmd == "branch" || cmd == "reset" || cmd == "switch" {
							t.Errorf("unexpected command: %v", call.Args)
						}
					}
				}
			}
		})
	}
}

// ===========================================================================
// TS-21-4: Hub mode, including for unset, erroring or unrecognised variable
// values, never fetches or resolves credentials.
// Verifies: 21-REQ-1.4
// ===========================================================================

func TestTS21_4_HubModeNeverFetchesOrResolvesCredentials(t *testing.T) {
	type varCase struct {
		name    string
		getVar  GetVariableFunc
	}

	cases := []varCase{
		{
			name: "unset",
			getVar: func(_, _, key string) (string, error) {
				return "", fmt.Errorf("not found")
			},
		},
		{
			name: "error",
			getVar: func(_, _, key string) (string, error) {
				return "", fmt.Errorf("store error")
			},
		},
		{
			name: "hub",
			getVar: func(_, _, key string) (string, error) {
				if key == "PATCH_BRANCH_SOURCE" {
					return "hub", nil
				}
				return "", fmt.Errorf("not found")
			},
		},
		{
			name: "Origin_case_sensitive",
			getVar: func(_, _, key string) (string, error) {
				if key == "PATCH_BRANCH_SOURCE" {
					return "Origin", nil
				}
				return "", fmt.Errorf("not found")
			},
		},
		{
			name: "ORIGIN_case_sensitive",
			getVar: func(_, _, key string) (string, error) {
				if key == "PATCH_BRANCH_SOURCE" {
					return "ORIGIN", nil
				}
				return "", fmt.Errorf("not found")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspaceRoot := t.TempDir()
			slug := "ts21-4-" + tc.name

			// Create a fork and trunk with no branch "feat" anywhere.
			_, _, _ = setupForkAndTrunk(t, workspaceRoot, slug)

			fetchStub := &countingFetchStub{}
			credStub := &countingCredStub{}

			hook := NewBranchResolverHook(
				NewGitRunnerFactory(),
				workspaceRoot,
				tc.getVar,
				credStub.resolve,
				fetchStub.fetch,
			)

			_, err := hook(context.Background(), slug, "feat")
			if err == nil {
				t.Fatal("expected error for missing branch, got nil")
			}

			// Verify it's a not_found error.
			if !isBranchResolveKind(err, "not_found") {
				t.Errorf("expected not_found error, got: %v", err)
			}

			if fetchStub.calls != 0 {
				t.Errorf("fetch calls = %d; want 0", fetchStub.calls)
			}
			if credStub.calls != 0 {
				t.Errorf("credential calls = %d; want 0", credStub.calls)
			}
		})
	}

	// Also test when the branch exists only as a tracking ref (hub mode
	// should still resolve it without fetching).
	t.Run("tracking_ref_no_fetch", func(t *testing.T) {
		workspaceRoot := t.TempDir()
		slug := "ts21-4-tracking"

		forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)
		createBranchOnBare(t, forkDir, "feat", "feat.txt", "feat content", "add feat")
		runGitCmd(t, trunkDir, "fetch", "origin")

		fetchStub := &countingFetchStub{}
		credStub := &countingCredStub{}

		getVar := func(_, _, key string) (string, error) {
			if key == "PATCH_BRANCH_SOURCE" {
				return "hub", nil
			}
			return "", fmt.Errorf("not found")
		}

		hook := NewBranchResolverHook(
			NewGitRunnerFactory(),
			workspaceRoot,
			getVar,
			credStub.resolve,
			fetchStub.fetch,
		)

		method, err := hook(context.Background(), slug, "feat")
		if err != nil {
			t.Fatalf("hook returned error: %v", err)
		}
		if method != "origin_tracking" {
			t.Errorf("method = %q; want %q", method, "origin_tracking")
		}
		if fetchStub.calls != 0 {
			t.Errorf("fetch calls = %d; want 0", fetchStub.calls)
		}
		if credStub.calls != 0 {
			t.Errorf("credential calls = %d; want 0", credStub.calls)
		}
	})
}

// ===========================================================================
// TS-21-5: PATCH_BRANCH_SOURCE is read once per request. Several hook calls
// that share one request scope (the elements of a batch) read it once and
// resolve in the mode of that first read; hook calls outside a request scope
// read it on every call.
// Verifies: 21-REQ-1.5
// ===========================================================================

func TestTS21_5_PatchBranchSourceReadOncePerRequestScope(t *testing.T) {
	const calls = 4

	// setup returns a hook whose PATCH_BRANCH_SOURCE getter alternates
	// origin, hub, origin, ... on successive reads, so a repeated read is
	// visible both in the read count and in the mode a call resolves in.
	// The branch names used never exist, so each call ends in not_found; what
	// differs is the path: origin mode resolves credentials and fetches, hub
	// mode does neither. The credential stub therefore counts the calls that
	// resolved in origin mode.
	type fixture struct {
		hook  func(context.Context, string, string) (string, error)
		slug  string
		reads *int
		cred  *countingCredStub
	}
	setup := func(t *testing.T) fixture {
		t.Helper()
		workspaceRoot := t.TempDir()
		slug := "ts21-5-scope"
		setupForkAndTrunk(t, workspaceRoot, slug)

		reads := 0
		getVar := func(_, _, key string) (string, error) {
			if key != "PATCH_BRANCH_SOURCE" {
				return "", fmt.Errorf("not found")
			}
			reads++
			if reads%2 == 1 {
				return "origin", nil
			}
			return "hub", nil
		}
		cred := &countingCredStub{}
		hook := NewBranchResolverHook(
			NewGitRunnerFactory(),
			workspaceRoot,
			getVar,
			cred.resolve,
			(&countingFetchStub{}).fetch,
		)
		return fixture{hook: hook, slug: slug, reads: &reads, cred: cred}
	}

	t.Run("scoped_context_reads_once_and_keeps_first_mode", func(t *testing.T) {
		f := setup(t)
		ctx := wsaccess.WithRequestScope(context.Background())

		for i := 0; i < calls; i++ {
			_, err := f.hook(ctx, f.slug, fmt.Sprintf("missing-%d", i))
			if !isBranchResolveKind(err, "not_found") {
				t.Fatalf("call %d: err = %v; want not_found", i, err)
			}
		}
		if *f.reads != 1 {
			t.Errorf("PATCH_BRANCH_SOURCE reads = %d; want 1 for %d hook calls in one scope", *f.reads, calls)
		}
		if f.cred.calls != calls {
			t.Errorf("origin-mode resolutions = %d; want %d (the first read said origin)", f.cred.calls, calls)
		}
	})

	t.Run("unscoped_context_reads_every_call", func(t *testing.T) {
		f := setup(t)

		for i := 0; i < calls; i++ {
			_, err := f.hook(context.Background(), f.slug, fmt.Sprintf("missing-%d", i))
			if !isBranchResolveKind(err, "not_found") {
				t.Fatalf("call %d: err = %v; want not_found", i, err)
			}
		}
		if *f.reads != calls {
			t.Errorf("PATCH_BRANCH_SOURCE reads = %d; want %d for %d unscoped hook calls", *f.reads, calls, calls)
		}
		// Reads alternate origin/hub, so half the calls resolved in origin mode.
		if f.cred.calls != calls/2 {
			t.Errorf("origin-mode resolutions = %d; want %d", f.cred.calls, calls/2)
		}
	})

	t.Run("separate_scopes_read_separately", func(t *testing.T) {
		f := setup(t)

		for i := 0; i < 2; i++ {
			ctx := wsaccess.WithRequestScope(context.Background())
			_, _ = f.hook(ctx, f.slug, "missing-a")
			_, _ = f.hook(ctx, f.slug, "missing-b")
		}
		if *f.reads != 2 {
			t.Errorf("PATCH_BRANCH_SOURCE reads = %d; want 2 (one per request scope)", *f.reads)
		}
		// First scope read origin (2 resolutions), second read hub (0).
		if f.cred.calls != 2 {
			t.Errorf("origin-mode resolutions = %d; want 2", f.cred.calls)
		}
	})
}

// ===========================================================================
// TS-21-8: The local branch is created with a compare-and-swap update-ref
// carrying the all-zero old value.
// Verifies: 21-REQ-2.1
// ===========================================================================

func TestTS21_8_CompareAndSwapUpdateRef(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "ts21-8"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)
	createBranchOnBare(t, forkDir, "feat", "feat.txt", "feat content", "add feat")
	runGitCmd(t, trunkDir, "fetch", "origin")

	trackingTip := revParse(t, trunkDir, "refs/remotes/origin/feat")
	if trackingTip == "" {
		t.Fatal("tracking ref should exist")
	}

	fetchStub := &countingFetchStub{}
	credStub := &countingCredStub{}

	getVar := func(_, _, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "hub", nil
		}
		return "", fmt.Errorf("not found")
	}

	realFactory := NewGitRunnerFactory()
	var recorder *recordingRunner
	wrappedFactory := func(repoPath string) (GitRunner, error) {
		real, err := realFactory(repoPath)
		if err != nil {
			return nil, err
		}
		recorder = &recordingRunner{inner: real}
		return recorder, nil
	}

	hook := NewBranchResolverHook(
		wrappedFactory,
		workspaceRoot,
		getVar,
		credStub.resolve,
		fetchStub.fetch,
	)

	method, err := hook(context.Background(), slug, "feat")
	if err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if method != "origin_tracking" {
		t.Errorf("method = %q; want %q", method, "origin_tracking")
	}

	// Verify exactly one update-ref call with the all-zero old value.
	zeroSHA := "0000000000000000000000000000000000000000"
	foundUpdateRef := false
	for _, call := range recorder.calls {
		if len(call.Args) >= 1 && call.Args[0] == "update-ref" {
			foundUpdateRef = true
			// Expected: update-ref refs/heads/feat <sha> 0000...
			if len(call.Args) < 4 {
				t.Errorf("update-ref call has %d args; want at least 4: %v", len(call.Args), call.Args)
				continue
			}
			if call.Args[1] != "refs/heads/feat" {
				t.Errorf("update-ref ref = %q; want %q", call.Args[1], "refs/heads/feat")
			}
			if call.Args[2] != trackingTip {
				t.Errorf("update-ref sha = %q; want %q", call.Args[2], trackingTip)
			}
			if call.Args[3] != zeroSHA {
				t.Errorf("update-ref old = %q; want %q", call.Args[3], zeroSHA)
			}
		}
	}
	if !foundUpdateRef {
		t.Error("no update-ref call found in recorded commands")
	}

	// Verify no checkout, branch, reset, or switch command was run.
	for _, call := range recorder.calls {
		if len(call.Args) > 0 {
			cmd := call.Args[0]
			if cmd == "checkout" || cmd == "branch" || cmd == "reset" || cmd == "switch" {
				t.Errorf("unexpected command: %v", call.Args)
			}
		}
	}
}

// ===========================================================================
// TS-21-9: Any created local branch points at the tracking tip and writes no
// tracking config.
// Verifies: 21-REQ-2.4
// ===========================================================================

func TestTS21_9_LocalBranchPointsAtTrackingTipNoConfig(t *testing.T) {
	// Property test over generated valid branch names.
	validNames := []string{
		"feat",
		"feature/my-patch",
		"fix/bug-123",
		"release/v1.0",
		"hotfix/urgent",
	}

	// Generate additional random valid branch names.
	rng := rand.New(rand.NewSource(42))
	chars := "abcdefghijklmnopqrstuvwxyz0123456789-_/"
	for i := 0; i < 5; i++ {
		length := 3 + rng.Intn(20)
		var name strings.Builder
		// First char must not be a dot or slash.
		name.WriteByte(chars[rng.Intn(len(chars)-1)]) // exclude '/'
		for j := 1; j < length; j++ {
			c := chars[rng.Intn(len(chars))]
			// Avoid consecutive slashes and ending with slash.
			if c == '/' && (j == length-1 || (name.Len() > 0 && name.String()[name.Len()-1] == '/')) {
				c = 'x'
			}
			name.WriteByte(c)
		}
		validNames = append(validNames, name.String())
	}

	for _, branchName := range validNames {
		t.Run("branch="+branchName, func(t *testing.T) {
			workspaceRoot := t.TempDir()
			slug := "ts21-9"

			forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

			// Create the branch on the fork.
			createBranchOnBare(t, forkDir, branchName, "file-"+strings.ReplaceAll(branchName, "/", "_")+".txt", "content", "add branch")
			runGitCmd(t, trunkDir, "fetch", "origin")

			trackingTip := revParse(t, trunkDir, "refs/remotes/origin/"+branchName)
			if trackingTip == "" {
				t.Fatalf("tracking ref for %s should exist", branchName)
			}

			fetchStub := &countingFetchStub{}
			credStub := &countingCredStub{}

			getVar := func(_, _, key string) (string, error) {
				if key == "PATCH_BRANCH_SOURCE" {
					return "hub", nil
				}
				return "", fmt.Errorf("not found")
			}

			hook := NewBranchResolverHook(
				NewGitRunnerFactory(),
				workspaceRoot,
				getVar,
				credStub.resolve,
				fetchStub.fetch,
			)

			method, err := hook(context.Background(), slug, branchName)
			if err != nil {
				t.Fatalf("hook returned error: %v", err)
			}
			if method != "origin_tracking" {
				t.Errorf("method = %q; want %q", method, "origin_tracking")
			}

			// Verify refs/heads/<name> equals the tracking tip.
			localRef := revParse(t, trunkDir, "refs/heads/"+branchName)
			if localRef != trackingTip {
				t.Errorf("refs/heads/%s = %s; want %s", branchName, localRef, trackingTip)
			}

			// Verify no tracking config was written.
			remote := gitConfigGet(t, trunkDir, "branch."+branchName+".remote")
			if remote != "" {
				t.Errorf("branch.%s.remote = %q; want empty", branchName, remote)
			}
			merge := gitConfigGet(t, trunkDir, "branch."+branchName+".merge")
			if merge != "" {
				t.Errorf("branch.%s.merge = %q; want empty", branchName, merge)
			}
		})
	}
}

// ===========================================================================
// TS-21-26: After acquiring the lock step 1 is re-checked and a branch that
// appeared meanwhile resolves as local without writing.
// Verifies: 21-REQ-7.1, 21-REQ-7.2
// ===========================================================================

func TestTS21_26_LockRecheck(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "ts21-26"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a branch on the fork so it appears as a tracking ref.
	createBranchOnBare(t, forkDir, "feat", "feat.txt", "feat content", "add feat")
	runGitCmd(t, trunkDir, "fetch", "origin")

	// Verify refs/heads/feat does NOT exist.
	if revParse(t, trunkDir, "refs/heads/feat") != "" {
		t.Fatal("refs/heads/feat should not exist before the test")
	}

	fetchStub := &countingFetchStub{}
	credStub := &countingCredStub{}

	getVar := func(_, _, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "hub", nil
		}
		return "", fmt.Errorf("not found")
	}

	// Create a new commit in the trunk itself to use as the "pushed" branch tip.
	// This simulates a hub push that created the branch while the lock was being acquired.
	newSHA := commitFile(t, trunkDir, "pushed.txt", "pushed content", "pushed commit")
	// Go back to main so the test doesn't interfere.
	runGitCmd(t, trunkDir, "checkout", "main")

	realFactory := NewGitRunnerFactory()
	var recorder *recordingRunner
	wrappedFactory := func(repoPath string) (GitRunner, error) {
		real, err := realFactory(repoPath)
		if err != nil {
			return nil, err
		}
		recorder = &recordingRunner{inner: real}
		return recorder, nil
	}

	hook := NewBranchResolverHook(
		wrappedFactory,
		workspaceRoot,
		getVar,
		credStub.resolve,
		fetchStub.fetch,
	)

	// Inject a lock hook that creates refs/heads/feat while the lock is
	// being acquired (simulating a concurrent push).
	hook = newBranchResolverHookWithLockFunc(
		wrappedFactory,
		workspaceRoot,
		getVar,
		credStub.resolve,
		fetchStub.fetch,
		func(s string) (func(), bool) {
			// Before returning the lock, create the branch at newSHA.
			runGitCmd(t, trunkDir, "update-ref", "refs/heads/feat", newSHA)
			// Return a real lock.
			return func() {}, true
		},
	)

	method, err := hook(context.Background(), slug, "feat")
	if err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if method != "local" {
		t.Errorf("method = %q; want %q", method, "local")
	}

	// Verify no update-ref was issued by the resolver (the branch was
	// created by the lock hook, not by the resolver).
	if recorder != nil {
		for _, call := range recorder.calls {
			if len(call.Args) >= 1 && call.Args[0] == "update-ref" {
				t.Errorf("unexpected update-ref call: %v", call.Args)
			}
		}
	}

	// Verify no fetch occurred.
	if fetchStub.calls != 0 {
		t.Errorf("fetch calls = %d; want 0", fetchStub.calls)
	}

	// Verify refs/heads/feat is unchanged (the one created by the lock hook).
	finalSHA := revParse(t, trunkDir, "refs/heads/feat")
	if finalSHA != newSHA {
		t.Errorf("refs/heads/feat = %s; want %s", finalSHA, newSHA)
	}
}

// ===========================================================================
// Additional tests for tag/SHA rejection (21-REQ-1.6)
// ===========================================================================

func TestTS21_TagAndSHARejected(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "ts21-tag-sha"

	_, trunkDir, initialSHA := setupForkAndTrunk(t, workspaceRoot, slug)

	// Create a tag.
	runGitCmd(t, trunkDir, "tag", "v1.0")

	fetchStub := &countingFetchStub{}
	credStub := &countingCredStub{}

	getVar := func(_, _, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "hub", nil
		}
		return "", fmt.Errorf("not found")
	}

	hook := NewBranchResolverHook(
		NewGitRunnerFactory(),
		workspaceRoot,
		getVar,
		credStub.resolve,
		fetchStub.fetch,
	)

	// A tag name should be rejected.
	_, err := hook(context.Background(), slug, "v1.0")
	if err == nil {
		t.Error("expected error for tag name, got nil")
	}
	if !isBranchResolveKind(err, "not_found") {
		t.Errorf("expected not_found error for tag, got: %v", err)
	}

	// A SHA should be rejected.
	_, err = hook(context.Background(), slug, initialSHA)
	if err == nil {
		t.Error("expected error for SHA, got nil")
	}
	if !isBranchResolveKind(err, "not_found") {
		t.Errorf("expected not_found error for SHA, got: %v", err)
	}

	// A remote-qualified name should be rejected.
	_, err = hook(context.Background(), slug, "origin/main")
	if err == nil {
		t.Error("expected error for origin/main, got nil")
	}
	if !isBranchResolveKind(err, "not_found") {
		t.Errorf("expected not_found error for origin/main, got: %v", err)
	}
}

// ===========================================================================
// Test for CAS failure retry (21-REQ-2.2)
// ===========================================================================

func TestTS21_CASFailureRetry(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "ts21-cas"

	forkDir, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)
	createBranchOnBare(t, forkDir, "feat", "feat.txt", "feat content", "add feat")
	runGitCmd(t, trunkDir, "fetch", "origin")

	fetchStub := &countingFetchStub{}
	credStub := &countingCredStub{}

	getVar := func(_, _, key string) (string, error) {
		if key == "PATCH_BRANCH_SOURCE" {
			return "hub", nil
		}
		return "", fmt.Errorf("not found")
	}

	// Create a factory that on the first update-ref call, creates the branch
	// first (simulating a concurrent push), causing the CAS to fail.
	casAttempts := 0
	realFactory := NewGitRunnerFactory()
	wrappedFactory := func(repoPath string) (GitRunner, error) {
		real, err := realFactory(repoPath)
		if err != nil {
			return nil, err
		}
		return &brCASInterceptRunner{
			inner:    real,
			trunkDir: trunkDir,
			onCAS: func() {
				casAttempts++
				if casAttempts == 1 {
					// Create the branch before the CAS, simulating a concurrent push.
					trackingTip := revParse(t, trunkDir, "refs/remotes/origin/feat")
					runGitCmd(t, trunkDir, "update-ref", "refs/heads/feat", trackingTip)
				}
			},
		}, nil
	}

	hook := NewBranchResolverHook(
		wrappedFactory,
		workspaceRoot,
		getVar,
		credStub.resolve,
		fetchStub.fetch,
	)

	method, err := hook(context.Background(), slug, "feat")
	if err != nil {
		t.Fatalf("hook returned error: %v", err)
	}
	if method != "local" {
		t.Errorf("method = %q; want %q (after CAS retry)", method, "local")
	}
}

// brCASInterceptRunner intercepts update-ref calls to simulate CAS failures.
type brCASInterceptRunner struct {
	inner    GitRunner
	trunkDir string
	onCAS    func()
}

func (r *brCASInterceptRunner) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) >= 1 && args[0] == "update-ref" {
		if r.onCAS != nil {
			r.onCAS()
		}
	}
	return r.inner.Run(ctx, args...)
}

func (r *brCASInterceptRunner) CherryPick(ctx context.Context, commitSHA string) error {
	return r.inner.CherryPick(ctx, commitSHA)
}
func (r *brCASInterceptRunner) MergeNoFF(ctx context.Context, ref, message string) error {
	return r.inner.MergeNoFF(ctx, ref, message)
}
func (r *brCASInterceptRunner) MergeTree(ctx context.Context, base, head string) (string, error) {
	return r.inner.MergeTree(ctx, base, head)
}
func (r *brCASInterceptRunner) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	return r.inner.IsAncestor(ctx, ancestor, descendant)
}
func (r *brCASInterceptRunner) Cherry(ctx context.Context, upstream, head string) ([]string, []string, error) {
	return r.inner.Cherry(ctx, upstream, head)
}
func (r *brCASInterceptRunner) HardReset(ctx context.Context, ref string) error {
	return r.inner.HardReset(ctx, ref)
}
func (r *brCASInterceptRunner) WorktreeAdd(ctx context.Context, path, commit string) error {
	return r.inner.WorktreeAdd(ctx, path, commit)
}
func (r *brCASInterceptRunner) WorktreeRemove(ctx context.Context, path string) error {
	return r.inner.WorktreeRemove(ctx, path)
}
func (r *brCASInterceptRunner) WorktreePrune(ctx context.Context) error {
	return r.inner.WorktreePrune(ctx)
}
func (r *brCASInterceptRunner) UpdateRef(ctx context.Context, ref, sha string) error {
	return r.inner.UpdateRef(ctx, ref, sha)
}

var _ GitRunner = (*brCASInterceptRunner)(nil)
