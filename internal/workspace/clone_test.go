package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// cloneState reads the clone columns of slug.
func cloneState(t *testing.T, v *env, slug string) (status, sha, errText string) {
	t.Helper()
	s, h, e, err := getCloneFields(v.db, slug)
	if err != nil {
		t.Fatal(err)
	}
	if h != nil {
		sha = *h
	}
	if e != nil {
		errText = *e
	}
	return s, sha, errText
}

// TestClone_FromOrigin: the clone job clones the workspace's origin into its
// trunk, the named branch only when one is set, and records HEAD and ready;
// a branch origin lacks fails the job and removes the partial directory.
func TestClone_FromOrigin(t *testing.T) {
	v := newEnv(t)
	swap(t, &cloneFn, CloneFuncType(defaultCloneFn))
	bare := bareRepo(t, "feature")
	v.seed(t, &Workspace{Slug: "default", OwnerID: owner, GitURL: bare})
	v.seed(t, &Workspace{Slug: "feature", OwnerID: owner, GitURL: bare, Branch: strPtr("feature")})
	v.seed(t, &Workspace{Slug: "missing", OwnerID: owner, GitURL: bare, Branch: strPtr("nope")})

	processCloneJob(context.Background(), v.db, v.root, CloneJob{Slug: "default", GitURL: bare})
	if status, sha, _ := cloneState(t, v, "default"); status != "ready" || sha != bareRef(t, bare, "refs/heads/main") {
		t.Errorf("default: %s %s", status, sha)
	}
	if gitRun(t, v.trunk("default"), "rev-parse", "--abbrev-ref", "HEAD") != "main" || bareRef(t, v.trunk("default")+"/.git", "refs/remotes/origin/feature") == "" {
		t.Error("the default clone is not on main with every branch")
	}
	processCloneJob(context.Background(), v.db, v.root, CloneJob{Slug: "feature", GitURL: bare, Branch: strPtr("feature")})
	if status, sha, _ := cloneState(t, v, "feature"); status != "ready" || sha != bareRef(t, bare, "refs/heads/feature") {
		t.Errorf("feature: %s %s", status, sha)
	}
	if gitRun(t, v.trunk("feature"), "rev-parse", "--abbrev-ref", "HEAD") != "feature" || bareRef(t, v.trunk("feature")+"/.git", "refs/remotes/origin/main") != "" {
		t.Error("the single-branch clone has more than its branch")
	}
	processCloneJob(context.Background(), v.db, v.root, CloneJob{Slug: "missing", GitURL: bare, Branch: strPtr("nope")})
	if status, _, errText := cloneState(t, v, "missing"); status != "failed" || !strings.Contains(errText, "clone") {
		t.Errorf("missing branch: %s %q", status, errText)
	}
	if _, err := os.Stat(filepath.Join(v.root, "missing")); !os.IsNotExist(err) {
		t.Error("the failed clone's directory survived")
	}
}

// TestClone_StatesAndCredentials: the job marks the workspace cloning first,
// skips a directory that already exists, hands the stored credential to the
// clone, and fails the job when the credential cannot be read.
func TestClone_StatesAndCredentials(t *testing.T) {
	v := newEnv(t)
	var seen []transport.AuthMethod
	var during string
	swap(t, &cloneFn, CloneFuncType(func(_ context.Context, path, url string, depth int, single bool, ref string, auth transport.AuthMethod) (string, error) {
		seen = append(seen, auth)
		during, _, _ = cloneState(t, v, filepath.Base(filepath.Dir(path)))
		if depth != 0 || single != (ref != "") {
			t.Errorf("clone options depth %d single %v ref %q", depth, single, ref)
		}
		if url == "fail" {
			return "", errors.New("boom")
		}
		return headSHA, nil
	}))
	v.seed(t, &Workspace{Slug: "pat", OwnerID: owner})
	v.seed(t, &Workspace{Slug: "basic", OwnerID: owner})
	v.seed(t, &Workspace{Slug: "half", OwnerID: owner})
	v.seed(t, &Workspace{Slug: "public", OwnerID: owner})
	v.seed(t, &Workspace{Slug: "exists", OwnerID: owner})
	storeSecrets(t, v.db, "pat", "GIT_PAT", "tok")
	storeSecrets(t, v.db, "basic", "GIT_USERNAME", "bob", "GIT_PASSWORD", "pw")
	storeSecrets(t, v.db, "half", "GIT_USERNAME", "bob")
	markerDir(t, v, "exists")

	for _, slug := range []string{"pat", "basic", "half", "public"} {
		processCloneJob(context.Background(), v.db, v.root, CloneJob{Slug: slug, GitURL: "ok"})
		if status, sha, _ := cloneState(t, v, slug); status != "ready" || sha != headSHA {
			t.Errorf("%s: %s %s", slug, status, sha)
		}
	}
	if during != "cloning" {
		t.Errorf("clone_status during the clone %q; want cloning", during)
	}
	if len(seen) != 4 || *seen[0].(*githttp.BasicAuth) != (githttp.BasicAuth{Username: "x-token-auth", Password: "tok"}) ||
		*seen[1].(*githttp.BasicAuth) != (githttp.BasicAuth{Username: "bob", Password: "pw"}) || seen[2] != nil || seen[3] != nil {
		t.Errorf("credentials %+v", seen)
	}
	processCloneJob(context.Background(), v.db, v.root, CloneJob{Slug: "exists", GitURL: "ok"})
	if status, _, _ := cloneState(t, v, "exists"); status != "ready" || len(seen) != 4 {
		t.Error("an existing directory was cloned again")
	}
	v.seed(t, &Workspace{Slug: "fails", OwnerID: owner})
	processCloneJob(context.Background(), v.db, v.root, CloneJob{Slug: "fails", GitURL: "fail"})
	if status, _, errText := cloneState(t, v, "fails"); status != "failed" || errText != "boom" {
		t.Errorf("fails: %s %q", status, errText)
	}
	v.seed(t, &Workspace{Slug: "no-store", OwnerID: owner})
	testutil.Exec(t, v.db, `DROP TABLE secrets`)
	processCloneJob(context.Background(), v.db, v.root, CloneJob{Slug: "no-store", GitURL: "ok"})
	if status, _, errText := cloneState(t, v, "no-store"); status != "failed" || !strings.Contains(errText, "credential lookup failed") {
		t.Errorf("no-store: %s %q", status, errText)
	}
	// A job for a row that is gone does nothing.
	processCloneJob(context.Background(), v.db, v.root, CloneJob{Slug: "ghost", GitURL: "ok"})
}

// TestClone_CarryPatchSetup: after cloning a carry_patch workspace the job adds
// the upstream remote, fetches it, enables rerere and creates the integration
// branch at upstream's HEAD; without a reachable upstream the branch starts at
// the fork's HEAD and the workspace is ready all the same.
func TestClone_CarryPatchSetup(t *testing.T) {
	v := newEnv(t)
	swap(t, &cloneFn, CloneFuncType(defaultCloneFn))
	swap(t, &upstreamFetchFn, FetchUpstream)
	fork := bareRepo(t)
	upstream := bareRepo(t)
	v.seed(t, &Workspace{Slug: "cp", OwnerID: owner, GitURL: fork, WorkspaceMode: "carry_patch", UpstreamURL: strPtr(upstream), IntegrationBranch: strPtr("deploy")})
	v.seed(t, &Workspace{Slug: "cp-dark", OwnerID: owner, GitURL: fork, WorkspaceMode: "carry_patch", UpstreamURL: strPtr("https://unreachable.invalid/u.git"), IntegrationBranch: strPtr("deploy")})
	v.seed(t, &Workspace{Slug: "std", OwnerID: owner, GitURL: fork})

	processCloneJob(context.Background(), v.db, v.root, CloneJob{Slug: "cp", GitURL: fork})
	trunk := v.trunk("cp")
	if status, _, _ := cloneState(t, v, "cp"); status != "ready" {
		t.Fatalf("cp: %s", status)
	}
	if gitRun(t, trunk, "remote", "get-url", "upstream") != upstream || gitRun(t, trunk, "config", "rerere.enabled") != "true" || gitRun(t, trunk, "config", "rerere.autoupdate") != "true" {
		t.Error("the upstream remote or rerere are not configured")
	}
	if gitRun(t, trunk, "rev-parse", "deploy") != bareRef(t, upstream, "refs/heads/main") || gitRun(t, trunk, "rev-parse", UpstreamHeadRef) != bareRef(t, upstream, "refs/heads/main") {
		t.Error("deploy does not start at upstream's HEAD")
	}

	logs := captureLog(t)
	processCloneJob(context.Background(), v.db, v.root, CloneJob{Slug: "cp-dark", GitURL: fork})
	trunk = v.trunk("cp-dark")
	if status, _, _ := cloneState(t, v, "cp-dark"); status != "ready" || !strings.Contains(logs.String(), "upstream fetch") {
		t.Errorf("cp-dark: %s; logs %s", status, logs)
	}
	if gitRun(t, trunk, "rev-parse", "deploy") != bareRef(t, fork, "refs/heads/main") {
		t.Error("deploy does not fall back to the fork's HEAD")
	}

	processCloneJob(context.Background(), v.db, v.root, CloneJob{Slug: "std", GitURL: fork})
	if out, err := gitTry(v.trunk("std"), "remote", "get-url", "upstream"); err == nil {
		t.Errorf("a standard workspace got an upstream remote: %s", out)
	}
}

// TestClone_Credentials: the clone credential is GIT_PAT as a token, else
// GIT_USERNAME with GIT_PASSWORD, else none; the upstream credential is the
// UPSTREAM_ variants first and the clone credential as the fallback; a store
// failure is returned.
func TestClone_Credentials(t *testing.T) {
	v := newEnv(t)
	store := secrets.NewStore(v.db)
	storeSecrets(t, v.db, "both", "GIT_PAT", "tok", "GIT_USERNAME", "bob", "GIT_PASSWORD", "pw", "UPSTREAM_GIT_PAT", "utok")
	storeSecrets(t, v.db, "basic", "GIT_USERNAME", "bob", "GIT_PASSWORD", "pw", "UPSTREAM_GIT_USERNAME", "ubob", "UPSTREAM_GIT_PASSWORD", "upw")
	storeSecrets(t, v.db, "half", "GIT_PAT", "tok", "UPSTREAM_GIT_USERNAME", "ubob")
	for slug, want := range map[string][2]*githttp.BasicAuth{
		"both":  {{Username: "x-token-auth", Password: "tok"}, {Username: "x-token-auth", Password: "utok"}},
		"basic": {{Username: "bob", Password: "pw"}, {Username: "ubob", Password: "upw"}},
		"half":  {{Username: "x-token-auth", Password: "tok"}, {Username: "x-token-auth", Password: "tok"}},
		"none":  {nil, nil},
	} {
		clone, err1 := ResolveCloneAuth(store, slug)
		upstream, err2 := ResolveUpstreamAuthMethod(store, slug)
		if err1 != nil || err2 != nil || ResolveUpstreamAuth(store, slug) != nil {
			t.Fatalf("%s: %v %v", slug, err1, err2)
		}
		for i, got := range []transport.AuthMethod{clone, upstream} {
			if want[i] == nil && got != nil || want[i] != nil && (got == nil || *got.(*githttp.BasicAuth) != *want[i]) {
				t.Errorf("%s[%d] = %+v; want %+v", slug, i, got, want[i])
			}
		}
	}
	testutil.Exec(t, v.db, `DROP TABLE secrets`)
	if _, err := resolveCloneAuth(store, "both"); err == nil {
		t.Error("no error from a failing store")
	}
	if err := ResolveUpstreamAuth(store, "both"); err == nil {
		t.Error("no upstream error from a failing store")
	}
}

// TestClone_Queue: the queue's workers process the enqueued jobs and stop when
// the context is cancelled; a job enqueued afterwards is discarded.
func TestClone_Queue(t *testing.T) {
	v := newEnv(t)
	var cloned atomic.Int32
	swap(t, &cloneFn, CloneFuncType(func(context.Context, string, string, int, bool, string, transport.AuthMethod) (string, error) {
		cloned.Add(1)
		return headSHA, nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	q := NewJobQueue(ctx, v.db, v.root, 2)
	for _, slug := range []string{"a", "b", "c"} {
		v.seed(t, &Workspace{Slug: slug, OwnerID: owner})
		q.Enqueue(CloneJob{Slug: slug, GitURL: "x"})
	}
	waitFor(t, "the jobs", func() bool {
		s, _, _ := cloneState(t, v, "c")
		return cloned.Load() == 3 && s == "ready"
	})
	cancel()
	q.Wait()
	q.Enqueue(CloneJob{Slug: "late", GitURL: "x"})
	if cloned.Load() != 3 {
		t.Error("a job was processed after the cancellation")
	}
	var nilQueue *JobQueue
	nilQueue.Wait()
}

// TestClone_Boot: the workspace root is created with its parents, and
// InitCloneQueue installs the production git functions and the queue.
func TestClone_Boot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a", "b", "workspaces")
	if err := EnsureWorkspaceRoot(root); err != nil {
		t.Fatal(err)
	}
	if err := EnsureWorkspaceRoot(root); err != nil {
		t.Errorf("second call: %v", err)
	}
	if err := EnsureWorkspaceRoot(filepath.Join(root, "a", "b")); err != nil {
		t.Error(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o644)
	if err := EnsureWorkspaceRoot(filepath.Join(file, "x")); err == nil {
		t.Error("a root under a file was created")
	}

	swap(t, &cloneFn, nil)
	swap(t, &archiveOpenAndPushFn, nil)
	swap(t, &archiveHeadFn, nil)
	swap(t, &validateCredentialsFn, nil)
	swap(t, &upstreamFetchFn, nil)
	swap(t, &defaultWorkspaceRoot, "")
	swap(t, &defaultQueue, nil)
	ctx, cancel := context.WithCancel(context.Background())
	InitCloneQueue(ctx, testutil.OpenDB(t), root, 1)
	cancel()
	defaultQueue.Wait()
	if cloneFn == nil || archiveOpenAndPushFn == nil || archiveHeadFn == nil || validateCredentialsFn == nil || upstreamFetchFn == nil || defaultWorkspaceRoot != root {
		t.Error("InitCloneQueue left a seam unset")
	}
	swap(t, &syncFetchAndCompareFn, nil)
	swap(t, &syncUpdateLocalRefFn, nil)
	InitSyncFunctions()
	if syncFetchAndCompareFn == nil || syncUpdateLocalRefFn == nil {
		t.Error("InitSyncFunctions left a seam unset")
	}
}

// TestClone_ArchivePush: the archive's push sends every local branch to origin
// through the shared push, reports an origin that has everything, and reports
// a trunk it cannot open or without origin as the error itself; HEAD is read
// from the trunk.
func TestClone_ArchivePush(t *testing.T) {
	trunk := filepath.Join(t.TempDir(), "trunk")
	bare := newTrunk(t, trunk, "a", "b")
	calls := recordPushes(t)
	if err := defaultArchiveOpenAndPushFn(trunk, bare, nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].refSpecs[0] != "refs/heads/*:refs/heads/*" || (*calls)[0].force || (*calls)[0].deadline {
		t.Errorf("push %+v", *calls)
	}
	for _, b := range []string{"main", "a", "b"} {
		if bareRef(t, bare, "refs/heads/"+b) != gitRun(t, trunk, "rev-parse", b) {
			t.Errorf("origin's %s differs", b)
		}
	}
	if err := defaultArchiveOpenAndPushFn(trunk, bare, nil); !errors.Is(err, ErrAlreadyUpToDate) {
		t.Errorf("second push: %v; want ErrAlreadyUpToDate", err)
	}
	if sha, err := defaultArchiveHeadFn(trunk); err != nil || sha != gitRun(t, trunk, "rev-parse", "HEAD") {
		t.Errorf("HEAD %s %v", sha, err)
	}
	if _, err := defaultArchiveHeadFn(t.TempDir()); err == nil {
		t.Error("HEAD of no repository")
	}
	gitRun(t, trunk, "remote", "remove", "origin")
	var te *trunkError
	if err := defaultArchiveOpenAndPushFn(trunk, bare, nil); err == nil || errors.As(err, &te) || !strings.Contains(err.Error(), "remote not found") {
		t.Errorf("without origin: %v; want go-git's error unwrapped", err)
	}
	if err := defaultArchiveOpenAndPushFn(t.TempDir(), bare, nil); err == nil {
		t.Error("push of no repository succeeded")
	}
}

// TestClone_CredentialValidation: the production credential check lists the
// remote and fails for a repository that is not there.
func TestClone_CredentialValidation(t *testing.T) {
	bare := bareRepo(t)
	if err := defaultValidateCredentialsFn(context.Background(), bare, nil); err != nil {
		t.Errorf("listing a local repository: %v", err)
	}
	if err := defaultValidateCredentialsFn(context.Background(), filepath.Join(t.TempDir(), "none"), nil); err == nil {
		t.Error("listing a missing repository succeeded")
	}
}
