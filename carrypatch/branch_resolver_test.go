package carrypatch

import (
	"context"
	"errors"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// resolver builds the production resolver over a fork and trunk, recording
// the fetches and credential lookups it makes.
type resolver struct {
	f        *forkTrunk
	root     string
	vars     map[string]string
	reads    map[string]int
	fetches  []string
	authed   int
	fetchErr error
	resolve  BranchResolverFunc
}

func newResolver(t *testing.T) *resolver {
	t.Helper()
	r := &resolver{root: t.TempDir(), vars: map[string]string{}, reads: map[string]int{}}
	r.f = newForkTrunk(t, r.root, "ws")
	real := DefaultSingleBranchFetch()
	fetch := func(ctx context.Context, repoPath, branch string, auth transport.AuthMethod) error {
		r.fetches = append(r.fetches, branch)
		if r.fetchErr != nil {
			return r.fetchErr
		}
		return real(ctx, repoPath, branch, auth)
	}
	resolveAuth := func(string) (transport.AuthMethod, error) { r.authed++; return nil, nil }
	r.resolve = NewBranchResolverHook(NewGitRunnerFactory(), r.root, varsOf(r.vars, r.reads), resolveAuth, fetch)
	return r
}

func kindOf(err error) string {
	kind, _ := branchResolveKindOf(err)
	return kind
}

// A local branch resolves as local without a lock; a branch only on the
// tracking ref gets a local branch created at its tip; in hub mode a branch
// found nowhere is not found and the fork is never contacted.
func TestBranchResolver_LocalAndTrackingWithoutOrigin(t *testing.T) {
	r := newResolver(t)
	ctx := context.Background()
	tip := r.f.pushBranch(t, "tracked", "t.txt", "t\n")
	runGit(t, r.f.trunk, "fetch", "-q", "origin")

	if method, err := r.resolve(ctx, "ws", "main"); err != nil || method != ResolutionLocal {
		t.Errorf("local = %s, %v", method, err)
	}
	if method, err := r.resolve(ctx, "ws", "tracked"); err != nil || method != ResolutionOriginTracking || refOf(r.f.trunk, "tracked") != tip {
		t.Errorf("tracking = %s, %v, ref=%s", method, err, refOf(r.f.trunk, "tracked"))
	}
	if out, err := gitErr(r.f.trunk, "config", "--get-regexp", "branch\\.tracked\\..*"); err == nil && out != "" {
		t.Errorf("tracking configuration written: %s", out)
	}
	if method, err := r.resolve(ctx, "ws", "tracked"); err != nil || method != ResolutionLocal {
		t.Errorf("second resolution = %s, %v", method, err)
	}
	r.f.pushBranch(t, "fork-only", "f.txt", "f\n")
	if _, err := r.resolve(ctx, "ws", "fork-only"); kindOf(err) != BranchResolveKindNotFound {
		t.Errorf("hub mode fork-only = %v", err)
	}
	for _, name := range []string{"v1.0", r.f.base, "origin/main", "no-such"} {
		if _, err := r.resolve(ctx, "ws", name); kindOf(err) != BranchResolveKindNotFound {
			t.Errorf("%q = %v, want not found", name, err)
		}
	}
	if len(r.fetches) != 0 || r.authed != 0 {
		t.Errorf("hub mode contacted the fork: fetches=%v authed=%d", r.fetches, r.authed)
	}
	if lockHeld("ws") {
		t.Error("lock left held")
	}
}

// In origin mode a branch neither local nor tracked is fetched from the fork
// (credentials first) and created locally; the fork lacking it is not found,
// any other fetch failure is an origin fetch failure.
func TestBranchResolver_OriginModeFetchesFromFork(t *testing.T) {
	r := newResolver(t)
	r.vars["PATCH_BRANCH_SOURCE"] = "origin"
	ctx := context.Background()
	tip := r.f.pushBranch(t, "fork-only", "f.txt", "f\n")

	method, err := r.resolve(ctx, "ws", "fork-only")
	if err != nil || method != ResolutionOriginFetch || refOf(r.f.trunk, "fork-only") != tip || r.authed != 1 || len(r.fetches) != 1 {
		t.Errorf("origin fetch = %s, %v, authed=%d fetches=%v", method, err, r.authed, r.fetches)
	}
	if method, _ := r.resolve(ctx, "ws", "fork-only"); method != ResolutionLocal {
		t.Error("resolved branch not local afterwards")
	}
	if _, err := r.resolve(ctx, "ws", "nowhere"); kindOf(err) != BranchResolveKindNotFound {
		t.Errorf("missing on fork = %v", err)
	}
	r.fetchErr = errors.New("connection refused")
	if _, err := r.resolve(ctx, "ws", "other"); kindOf(err) != BranchResolveKindOriginFetchFailed {
		t.Errorf("fetch failure = %v", err)
	}
	r.fetchErr = git.NoErrAlreadyUpToDate
	if _, err := r.resolve(ctx, "ws", "other"); kindOf(err) != BranchResolveKindNotFound {
		t.Errorf("up-to-date fetch without the ref = %v", err)
	}
	r.fetchErr = nil
	if lockHeld("ws") {
		t.Error("lock left held")
	}

	failing := NewBranchResolverHook(NewGitRunnerFactory(), r.root, varsOf(r.vars, nil),
		func(string) (transport.AuthMethod, error) { return nil, errors.New("no creds") }, DefaultSingleBranchFetch())
	if _, err := failing(ctx, "ws", "another"); kindOf(err) != BranchResolveKindOriginCredentials {
		t.Errorf("credential failure = %v", err)
	}
	if _, err := r.resolve(ctx, "missing-ws", "x"); err == nil || kindOf(err) != "" {
		t.Errorf("missing trunk = %v, want an unclassified error", err)
	}
}

// Writing takes the workspace lock: a busy workspace answers workspace_busy,
// a local branch still resolves, and the lock is re-checked once taken.
func TestBranchResolver_LockAndCASRace(t *testing.T) {
	r := newResolver(t)
	ctx := context.Background()
	tip := r.f.pushBranch(t, "tracked", "t.txt", "t\n")
	runGit(t, r.f.trunk, "fetch", "-q", "origin")

	release := holdLock(t, "ws")
	if _, err := r.resolve(ctx, "ws", "tracked"); kindOf(err) != BranchResolveKindWorkspaceBusy {
		t.Errorf("busy = %v", err)
	}
	if method, err := r.resolve(ctx, "ws", "main"); err != nil || method != ResolutionLocal {
		t.Errorf("local while locked = %s, %v", method, err)
	}
	release()

	// Another writer creates the branch between the check and the write: the
	// resolver reports local and leaves the ref alone.
	other := commitFile(t, r.f.trunk, "x.txt", "x\n", "elsewhere")
	racing := newBranchResolverHookWithLockFunc(func(path string) (GitRunner, error) {
		g, err := NewGitRunnerFactory()(path)
		if err != nil {
			return nil, err
		}
		return &spyGit{GitRunner: g, onRun: func(args []string) (string, error, bool) {
			if args[0] == "update-ref" {
				runGit(t, r.f.trunk, "update-ref", "refs/heads/tracked", other)
			}
			return "", nil, false
		}}, nil
	}, r.root, varsOf(r.vars, nil), noAuth, DefaultSingleBranchFetch(), testLocks.TryLock)
	if method, err := racing(ctx, "ws", "tracked"); err != nil || method != ResolutionLocal || refOf(r.f.trunk, "tracked") != other {
		t.Errorf("lost race = %s, %v, ref=%s", method, err, refOf(r.f.trunk, "tracked"))
	}
	runGit(t, r.f.trunk, "update-ref", "-d", "refs/heads/tracked")
	broken := newBranchResolverHookWithLockFunc(func(path string) (GitRunner, error) {
		g, err := NewGitRunnerFactory()(path)
		if err != nil {
			return nil, err
		}
		return &spyGit{GitRunner: g, onRun: func(args []string) (string, error, bool) {
			if args[0] == "update-ref" {
				return "", errors.New("refs locked"), true
			}
			return "", nil, false
		}}, nil
	}, r.root, varsOf(r.vars, nil), noAuth, DefaultSingleBranchFetch(), testLocks.TryLock)
	if _, err := broken(ctx, "ws", "tracked"); err == nil || kindOf(err) != "" {
		t.Errorf("failed write = %v, want an unclassified error", err)
	}
	_ = tip
}

// PATCH_BRANCH_SOURCE is read once per request scope, so every element of a
// batch resolves in the same mode; without a scope every call reads it.
func TestBranchResolver_ReadsSourceOncePerRequest(t *testing.T) {
	r := newResolver(t)
	ctx := withRequestScope(context.Background())
	if withRequestScope(ctx) != ctx {
		t.Error("nested scope replaced the request's scope")
	}
	for i := 0; i < 3; i++ {
		if _, err := r.resolve(ctx, "ws", "main"); err != nil {
			t.Fatal(err)
		}
	}
	if r.reads["PATCH_BRANCH_SOURCE"] != 1 {
		t.Errorf("reads within one scope = %d, want 1", r.reads["PATCH_BRANCH_SOURCE"])
	}
	for i := 0; i < 2; i++ {
		if _, err := r.resolve(context.Background(), "ws", "main"); err != nil {
			t.Fatal(err)
		}
	}
	if r.reads["PATCH_BRANCH_SOURCE"] != 3 {
		t.Errorf("reads without a scope = %d, want 3", r.reads["PATCH_BRANCH_SOURCE"])
	}
	for value, want := range map[string]string{"origin": "origin", "Origin": "hub", "hub": "hub", "": "hub"} {
		vars := map[string]string{}
		if value != "" {
			vars["PATCH_BRANCH_SOURCE"] = value
		}
		if got := ParsePatchBranchSource(varsOf(vars, nil), "ws"); got != want {
			t.Errorf("ParsePatchBranchSource(%q) = %s", value, got)
		}
	}
	if ParsePatchBranchSource(nil, "ws") != "hub" {
		t.Error("nil lookup must mean hub")
	}
}
