package carrypatch

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/aflib/jobqueue"
)

// recovery is a RecoveryService over a real fork and trunk, with a database
// for the origin state and the rebuild queue.
type recovery struct {
	f     *forkTrunk
	root  string
	vars  map[string]string
	svc   *RecoveryService
	wraps func(GitRunner) GitRunner
}

func newRecovery(t *testing.T) (*recovery, *RecoveryService) {
	t.Helper()
	r := &recovery{root: t.TempDir(), vars: map[string]string{}}
	r.f = newForkTrunk(t, r.root, "ws")
	r.svc = &RecoveryService{
		NewGitRunner: func(path string) (GitRunner, error) {
			g, err := NewGitRunnerFactory()(path)
			if err == nil && r.wraps != nil {
				g = r.wraps(g)
			}
			return g, err
		},
		WorkspaceRoot: r.root, GetVariable: varsOf(r.vars, nil), ResolveAuth: noAuth,
		Fetch: DefaultSingleBranchFetch(), LockFunc: testLocks.TryLock,
	}
	return r, r.svc
}

func (r *recovery) reset(t *testing.T, branch, status string) (ResetResult, error) {
	t.Helper()
	return r.svc.RunReset(context.Background(), "ws", ResetPatchInfo{ID: "p1", BranchName: branch, Status: status, IntegrationBranch: "integration"}, alice)
}

func resetKind(err error) ResetErrorKind {
	var re *ResetError
	if errors.As(err, &re) {
		return re.Kind
	}
	return ""
}

// ReadReplacedSHA answers the backup ref when it exists, "not found" for a
// missing ref or trunk, and an error for a lookup that failed.
func TestRecovery_ReadReplacedSHA(t *testing.T) {
	r, svc := newRecovery(t)
	ctx := context.Background()
	if _, found, err := svc.ReadReplacedSHA(ctx, "ws", "feature/a"); err != nil || found {
		t.Errorf("no ref = %v/%v", found, err)
	}
	runGit(t, r.f.trunk, "update-ref", "refs/hub/replaced/feature/a", r.f.base)
	if sha, found, err := svc.ReadReplacedSHA(ctx, "ws", "feature/a"); err != nil || !found || sha != r.f.base {
		t.Errorf("present = %s/%v/%v", sha, found, err)
	}
	if _, found, err := svc.ReadReplacedSHA(ctx, "no-trunk", "feature/a"); err != nil || found {
		t.Errorf("missing trunk = %v/%v", found, err)
	}
	r.wraps = func(g GitRunner) GitRunner {
		return &spyGit{GitRunner: g, onRun: func([]string) (string, error, bool) { return "", errors.New("git died"), true }}
	}
	if _, found, err := svc.ReadReplacedSHA(ctx, "ws", "feature/a"); err == nil || found {
		t.Errorf("failed lookup = %v/%v, want an error", found, err)
	}
	svc.NewGitRunner = func(string) (GitRunner, error) { return nil, errors.New("no runner") }
	if _, _, err := svc.ReadReplacedSHA(ctx, "ws", "feature/a"); err == nil {
		t.Error("runner failure not returned")
	}
}

// RemoveBackup deletes the backup ref, logging the removed SHA; a missing ref
// or trunk is success and logs no removal; other failures are returned.
func TestRecovery_RemoveBackup(t *testing.T) {
	r, svc := newRecovery(t)
	ctx := context.Background()
	sink := captureDefaultLog(t)
	runGit(t, r.f.trunk, "update-ref", "refs/hub/replaced/feature/a", r.f.base)
	if err := svc.RemoveBackup(ctx, "ws", "feature/a"); err != nil || refOf(r.f.trunk, "refs/hub/replaced/feature/a") != "" {
		t.Errorf("remove: %v, ref=%s", err, refOf(r.f.trunk, "refs/hub/replaced/feature/a"))
	}
	if !strings.Contains(sink.String(), "removed backup ref") || !strings.Contains(sink.String(), r.f.base) {
		t.Error("removal not logged with the SHA")
	}
	if err := svc.RemoveBackup(ctx, "ws", "feature/a"); err != nil || strings.Count(sink.String(), "removed backup ref") != 1 {
		t.Errorf("missing ref: %v (must not log a removal)", err)
	}
	if err := svc.RemoveBackup(ctx, "no-trunk", "feature/a"); err != nil || !strings.Contains(sink.String(), "trunk not on disk") {
		t.Errorf("missing trunk: %v", err)
	}
	runGit(t, r.f.trunk, "update-ref", "refs/hub/replaced/feature/a", r.f.base)
	r.wraps = func(g GitRunner) GitRunner {
		return &spyGit{GitRunner: g, onRun: func(args []string) (string, error, bool) {
			if args[0] == "update-ref" {
				return "", errors.New("refs locked"), true
			}
			return "", nil, false
		}}
	}
	if err := svc.RemoveBackup(ctx, "ws", "feature/a"); err == nil || !strings.Contains(err.Error(), "delete ref") {
		t.Errorf("failed delete = %v", err)
	}
	svc.NewGitRunner = func(string) (GitRunner, error) { return nil, errors.New("no runner") }
	if err := svc.RemoveBackup(ctx, "ws", "feature/a"); err == nil {
		t.Error("runner failure not returned")
	}
	if lockHeld("ws") {
		t.Error("backup removal took the lock")
	}
}

// The reset fetches the one branch from the fork and applies the first
// matching rule: create a missing local branch, leave equal tips, fast-forward
// an ancestor, or back up and replace anything else.
func TestRecovery_RunReset_Actions(t *testing.T) {
	cases := []struct {
		name   string
		local  func(r *recovery, forkTip string) string // returns the local tip, "" for none
		action ResetAction
	}{
		{"created", func(*recovery, string) string { return "" }, ResetActionCreated},
		{"none", func(r *recovery, forkTip string) string {
			runGit(t, r.f.trunk, "fetch", "-q", "origin")
			runGit(t, r.f.trunk, "branch", "feat", forkTip)
			return forkTip
		}, ResetActionNone},
		{"fast-forwarded", func(r *recovery, _ string) string {
			runGit(t, r.f.trunk, "branch", "feat", r.f.base)
			return r.f.base
		}, ResetActionFastForwarded},
		{"replaced", func(r *recovery, _ string) string { return r.f.localBranch(t, "feat", "l.txt", "local\n") }, ResetActionReplaced},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newRecovery(t)
			forkTip := r.f.pushBranch(t, "feat", "f.txt", "fork\n")
			local := c.local(r, forkTip)
			res, err := r.reset(t, "feat", "active")
			if err != nil {
				t.Fatalf("reset: %v", err)
			}
			if res.Action != c.action || res.OriginSHA != forkTip || res.LocalSHA != local || refOf(r.f.trunk, "feat") != forkTip {
				t.Errorf("result = %+v, ref = %s", res, refOf(r.f.trunk, "feat"))
			}
			backup := refOf(r.f.trunk, "refs/hub/replaced/feat")
			if c.action == ResetActionReplaced && (res.ReplacedSHA != local || backup != local) {
				t.Errorf("replaced: replaced_sha=%s backup=%s want %s", res.ReplacedSHA, backup, local)
			}
			if c.action != ResetActionReplaced && (res.ReplacedSHA != "" || backup != "") {
				t.Errorf("%s wrote a backup", c.name)
			}
			if res.RebuildTriggered {
				t.Error("rebuild triggered without a queue")
			}
			if lockHeld("ws") {
				t.Error("lock left held")
			}
		})
	}
}

// Each failure of the reset is classified: configuration, a busy workspace,
// credentials, the fetch, a branch the fork lacks, a lost race on the ref and
// anything else.
func TestRecovery_RunReset_Failures(t *testing.T) {
	r, svc := newRecovery(t)
	forkTip := r.f.pushBranch(t, "feat", "f.txt", "fork\n")
	local := r.f.localBranch(t, "feat", "l.txt", "local\n")

	svc.LockFunc = nil
	if _, err := r.reset(t, "feat", "active"); resetKind(err) != ResetErrOther || !strings.Contains(err.Error(), "lock function") {
		t.Errorf("no lock function = %v", err)
	}
	svc.LockFunc = testLocks.TryLock
	fetch := svc.Fetch
	svc.Fetch = nil
	if _, err := r.reset(t, "feat", "active"); resetKind(err) != ResetErrOther || !strings.Contains(err.Error(), "origin fetch") {
		t.Errorf("no fetch = %v", err)
	}
	svc.Fetch = fetch

	release := holdLock(t, "ws")
	_, err := r.reset(t, "feat", "active")
	release()
	if resetKind(err) != ResetErrBusy {
		t.Errorf("busy = %v", err)
	}
	svc.ResolveAuth = func(string) (transport.AuthMethod, error) { return nil, errors.New("no creds") }
	if _, err := r.reset(t, "feat", "active"); resetKind(err) != ResetErrCredentialFailed {
		t.Errorf("credentials = %v", err)
	}
	svc.ResolveAuth = noAuth
	svc.Fetch = func(context.Context, string, string, transport.AuthMethod) error { return errors.New("fork down") }
	if _, err := r.reset(t, "feat", "active"); resetKind(err) != ResetErrFetchFailed || !strings.Contains(err.Error(), "fork down") {
		t.Errorf("fetch failure = %v", err)
	}
	svc.Fetch = fetch
	if _, err := r.reset(t, "missing", "active"); resetKind(err) != ResetErrMissingOnOrigin {
		t.Errorf("missing on origin = %v", err)
	}
	svc.Fetch = func(context.Context, string, string, transport.AuthMethod) error { return nil }
	if _, err := r.reset(t, "never-fetched", "active"); resetKind(err) != ResetErrMissingOnOrigin {
		t.Errorf("no tracking ref after the fetch = %v", err)
	}
	svc.Fetch = fetch
	if refOf(r.f.trunk, "feat") != local {
		t.Fatal("a failed reset moved the branch")
	}

	// Another writer moves the branch between the read and the write.
	other := commitFile(t, r.f.trunk, "x.txt", "x\n", "elsewhere")
	runGit(t, r.f.trunk, "checkout", "-q", "main")
	r.wraps = func(g GitRunner) GitRunner {
		return &spyGit{GitRunner: g, onRun: func(args []string) (string, error, bool) {
			if args[0] == "update-ref" && args[1] == "refs/heads/feat" {
				runGit(t, r.f.trunk, "update-ref", "refs/heads/feat", other)
			}
			return "", nil, false
		}}
	}
	if _, err := r.reset(t, "feat", "active"); resetKind(err) != ResetErrRefChanged {
		t.Errorf("lost race = %v", err)
	}
	if refOf(r.f.trunk, "refs/hub/replaced/feat") != local || refOf(r.f.trunk, "feat") != other {
		t.Error("the backup written before the lost race must be kept")
	}
	r.wraps = func(g GitRunner) GitRunner {
		return &spyGit{GitRunner: g, onRun: func(args []string) (string, error, bool) {
			if args[0] == "update-ref" {
				return "", errors.New("refs locked"), true
			}
			return "", nil, false
		}}
	}
	runGit(t, r.f.trunk, "update-ref", "refs/heads/feat", local)
	if _, err := r.reset(t, "feat", "active"); resetKind(err) != ResetErrOther {
		t.Errorf("failed backup write = %v", err)
	}
	r.wraps = func(g GitRunner) GitRunner {
		return &spyGit{GitRunner: g, onIsAncestor: func(string, string) error { return errors.New("corrupt") }}
	}
	if _, err := r.reset(t, "feat", "active"); resetKind(err) != ResetErrOther || !strings.Contains(err.Error(), "ancestry") {
		t.Errorf("ancestry failure = %v", err)
	}
	r.wraps = nil
	svc.NewGitRunner = func(string) (GitRunner, error) { return nil, errors.New("no runner") }
	if _, err := r.reset(t, "feat", "active"); resetKind(err) != ResetErrOther {
		t.Errorf("runner failure = %v", err)
	}
	_ = forkTip
}

// A hard reset that fails after the ref moved is logged, not returned: the
// move is the reset's effect.
func TestRecovery_RunReset_HardResetFailureAfterMoveIsLogged(t *testing.T) {
	r, _ := newRecovery(t)
	forkTip := r.f.pushBranch(t, "feat", "f.txt", "fork\n")
	runGit(t, r.f.trunk, "branch", "feat", r.f.base)
	runGit(t, r.f.trunk, "checkout", "-q", "feat")
	r.wraps = func(g GitRunner) GitRunner {
		return &spyGit{GitRunner: g, onHardReset: func(string) error { return errors.New("dirty tree") }}
	}
	sink := captureDefaultLog(t)
	res, err := r.reset(t, "feat", "active")
	if err != nil || res.Action != ResetActionFastForwarded || refOf(r.f.trunk, "feat") != forkTip {
		t.Errorf("result = %+v, %v", res, err)
	}
	if !strings.Contains(sink.String(), "hard reset failed after the branch moved") {
		t.Error("failed reset not logged")
	}
	// Without the failure the checked-out work tree follows the branch.
	r.wraps = nil
	runGit(t, r.f.trunk, "update-ref", "refs/heads/feat", r.f.base)
	runGit(t, r.f.trunk, "reset", "-q", "--hard")
	if _, err := r.reset(t, "feat", "active"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.f.trunk, "f.txt")); err != nil {
		t.Error("work tree not reset to the new tip")
	}
}

// A moved active or conflict patch enqueues the same rebuild job as a sync
// (deduplicated, honouring AUTO_REBUILD_AFTER_SYNC); in origin mode the
// patch's origin state is recorded as in sync.
func TestRecovery_RunReset_OriginStateAndRebuild(t *testing.T) {
	r, svc := newRecovery(t)
	db := openDB(t)
	seedWS(t, db, ws{slug: "ws"})
	seedPatch(t, db, "ws", "p1", "feat", 1, "active")
	q := newQueue(t, db, nil)
	svc.PatchStore, svc.Queue = NewSQLPatchStore(db), q
	svc.GetVariable = varsOf(r.vars, nil)
	r.vars["REBUILD_STRATEGY"] = StrategyMerge
	forkTip := r.f.pushBranch(t, "feat", "f.txt", "fork\n")
	count := func() int { jobs, _ := q.ListByKey("rebuild", "ws"); return len(jobs) }
	state := func() string { st, _ := originState(t, db, "p1"); return st }

	res, err := r.reset(t, "feat", "active")
	if err != nil || !res.RebuildTriggered || res.RebuildJobID == "" || count() != 1 {
		t.Fatalf("hub mode reset: %+v, %v, jobs=%d", res, err, count())
	}
	jobs, _ := q.ListByKey("rebuild", "ws")
	if jobs[0].SubmittedBy != "alice" || jobs[0].GroupKey != "ws:integration" || !strings.Contains(string(jobs[0].Payload), `"strategy":"merge"`) {
		t.Errorf("job = %+v", jobs[0])
	}
	if state() != "" {
		t.Error("origin state written in hub mode")
	}

	r.vars["PATCH_BRANCH_SOURCE"] = "origin"
	runGit(t, r.f.trunk, "update-ref", "refs/heads/feat", r.f.base)
	res, err = r.reset(t, "feat", "conflict")
	if err != nil || res.RebuildTriggered || count() != 1 {
		t.Errorf("deduplicated: %+v, %v, jobs=%d", res, err, count())
	}
	if st, sha := originState(t, db, "p1"); st != StateInSync || sha.String != forkTip {
		t.Errorf("origin state = %s/%s", st, sha.String)
	}
	mustExec(t, db, `DELETE FROM jobs`)
	if res, _ := r.reset(t, "feat", "active"); res.Action != ResetActionNone || res.RebuildTriggered || count() != 0 {
		t.Errorf("no move: %+v", res)
	}
	runGit(t, r.f.trunk, "update-ref", "refs/heads/feat", r.f.base)
	if res, _ := r.reset(t, "feat", "disabled"); !res.RebuildTriggered && count() != 0 {
		t.Errorf("disabled: %+v", res)
	} else if res.RebuildTriggered {
		t.Error("disabled patch triggered a rebuild")
	}
	runGit(t, r.f.trunk, "update-ref", "refs/heads/feat", r.f.base)
	r.vars["AUTO_REBUILD_AFTER_SYNC"] = "false"
	if res, _ := r.reset(t, "feat", "active"); res.RebuildTriggered || count() != 0 {
		t.Error("rebuild enqueued although auto rebuild is off")
	}
	delete(r.vars, "AUTO_REBUILD_AFTER_SYNC")
	runGit(t, r.f.trunk, "update-ref", "refs/heads/feat", r.f.base)
	svc.Queue = failingEnqueuer{}
	sink := captureDefaultLog(t)
	if res, err := r.reset(t, "feat", "active"); err != nil || res.RebuildTriggered || !strings.Contains(sink.String(), "failed to enqueue rebuild") {
		t.Errorf("enqueue failure: %+v, %v", res, err)
	}
	if res, err := svc.RunReset(context.Background(), "ws", ResetPatchInfo{ID: "p1", BranchName: "feat", Status: "active"}, nil); err != nil || res.Action != ResetActionNone {
		t.Errorf("anonymous caller: %+v, %v", res, err)
	}
}

type failingEnqueuer struct{}

func (failingEnqueuer) Enqueue(jobqueue.EnqueueParams) (string, bool, error) {
	return "", false, errors.New("queue down")
}

// classifyCASError reads the ref after a failed compare-and-swap: a ref that
// moved, appeared or vanished is a lost race; one as expected, or unreadable,
// is another failure.
func TestClassifyCASError_FromRefState(t *testing.T) {
	r, _ := newRecovery(t)
	g := realGit(t, r.f.trunk)
	ctx := context.Background()
	cause := errors.New("update-ref failed")
	other := r.f.localBranch(t, "feat", "f.txt", "f\n")
	cases := []struct {
		name, ref, expected string
		want                ResetErrorKind
	}{
		{"moved", "refs/heads/feat", r.f.base, ResetErrRefChanged},
		{"appeared", "refs/heads/feat", zeroSHA, ResetErrRefChanged},
		{"vanished", "refs/heads/none", other, ResetErrRefChanged},
		{"unchanged", "refs/heads/feat", other, ResetErrOther},
		{"still absent", "refs/heads/none", zeroSHA, ResetErrOther},
	}
	for _, c := range cases {
		if got := classifyCASError(ctx, g, "feat", c.ref, c.expected, cause); got.Kind != c.want {
			t.Errorf("%s: %v, want %s", c.name, got, c.want)
		}
	}
	broken := newFakeGit()
	broken.run = func(...string) (string, error) { return "", errors.New("git died") }
	if got := classifyCASError(ctx, broken, "feat", "refs/heads/feat", other, cause); got.Kind != ResetErrOther || !errors.Is(got, cause) {
		t.Errorf("unreadable ref = %v", got)
	}
}

// The purge removes the backup ref of each expired soft-deleted row and then
// the row, only while it is still deleted; a failed ref removal keeps the
// row for the next purge; a listing failure stops everything.
func TestPurgeExpiredDeletedPatchesWithRefs(t *testing.T) {
	r, _ := newRecovery(t)
	db := openDB(t)
	seedWS(t, db, ws{slug: "ws"})
	seedWS(t, db, ws{slug: "no-trunk"})
	old := apikit.FormatUTC(time.Now().Add(-8 * 24 * time.Hour))
	for i, p := range []struct{ id, slug, branch, deletedAt string }{
		{"expired", "ws", "feature/old", old}, {"recent", "ws", "feature/new", apikit.NowUTC()},
		{"notrunk", "no-trunk", "feature/x", old}, {"restored", "ws", "feature/back", old},
	} {
		seedPatch(t, db, p.slug, p.id, p.branch, -(i + 1), "deleted")
		mustExec(t, db, `UPDATE patches SET deleted_at = ? WHERE id = ?`, p.deletedAt, p.id)
	}
	for _, b := range []string{"feature/old", "feature/new", "feature/back"} {
		runGit(t, r.f.trunk, "update-ref", "refs/hub/replaced/"+b, r.f.base)
	}
	store := NewSQLPatchStore(db)
	ctx := context.Background()

	// A row restored between the listing and the delete is not purged, but
	// its backup is gone by then.
	racing := &racingStore{PatchStore: store}
	n, err := PurgeExpiredDeletedPatchesWithRefs(ctx, racing, r.root, NewGitRunnerFactory())
	if err != nil || n != 2 {
		t.Fatalf("purged = %d, %v", n, err)
	}
	rows := patchStatuses(t, db, "ws")
	if _, ok := rows["feature/old"]; ok {
		t.Error("expired row kept")
	}
	if rows["feature/new"] != "deleted" || rows["feature/back"] != "active" || len(patchStatuses(t, db, "no-trunk")) != 0 {
		t.Errorf("rows after purge: ws=%v no-trunk=%v", rows, patchStatuses(t, db, "no-trunk"))
	}
	if refOf(r.f.trunk, "refs/hub/replaced/feature/old") != "" || refOf(r.f.trunk, "refs/hub/replaced/feature/back") != "" || refOf(r.f.trunk, "refs/hub/replaced/feature/new") == "" {
		t.Error("backup refs: expired ones removed, the recent one kept")
	}

	mustExec(t, db, `UPDATE patches SET deleted_at = ? WHERE id = 'recent'`, old)
	failing := func(path string) (GitRunner, error) {
		g, err := NewGitRunnerFactory()(path)
		return &spyGit{GitRunner: g, onRun: func(args []string) (string, error, bool) {
			if args[0] == "update-ref" {
				return "", errors.New("refs locked"), true
			}
			return "", nil, false
		}}, err
	}
	if n, err := PurgeExpiredDeletedPatchesWithRefs(ctx, store, r.root, failing); err != nil || n != 0 || patchStatuses(t, db, "ws")["feature/new"] != "deleted" {
		t.Errorf("failed ref removal: purged=%d err=%v (row must be kept)", n, err)
	}
	if _, err := PurgeExpiredDeletedPatchesWithRefs(ctx, &listFailStore{store}, r.root, NewGitRunnerFactory()); err == nil || !strings.Contains(err.Error(), "list expired deleted patches") {
		t.Errorf("listing failure = %v", err)
	}
}

// racingStore restores feature/back after it was listed, as a user would
// between the listing and the delete.
type racingStore struct{ PatchStore }

func (s *racingStore) ListExpiredDeletedPatches(ctx context.Context, cutoff string) ([]ExpiredDeletedPatch, error) {
	rows, err := s.PatchStore.ListExpiredDeletedPatches(ctx, cutoff)
	if err == nil {
		_ = s.PatchStore.RestorePatch(ctx, "restored")
	}
	return rows, err
}

type listFailStore struct{ PatchStore }

func (listFailStore) ListExpiredDeletedPatches(context.Context, string) ([]ExpiredDeletedPatch, error) {
	return nil, errors.New("db down")
}

// At startup every <slug>/rebuild directory left by a crashed run is removed
// and the trunk's worktree registrations pruned; a missing trunk or root and
// per-workspace failures are logged and the rest is still processed.
func TestCleanupStaleRebuildWorktrees(t *testing.T) {
	root := t.TempDir()
	trunk, base := newRebuildRepo(t, root, "ws")
	stale := filepath.Join(root, "ws", "rebuild", "job-1")
	runGit(t, trunk, "worktree", "add", "-q", "--detach", stale, base)
	writeFile(t, filepath.Join(root, "orphan", "rebuild", "job-2", "f"), "x")
	writeFile(t, filepath.Join(root, "plain", "trunk", "f"), "x")
	writeFile(t, filepath.Join(root, "stray-file"), "x")
	logger, sink := newLogger()

	CleanupStaleRebuildWorktrees(context.Background(), root, logger)
	if _, err := os.Stat(filepath.Join(root, "ws", "rebuild")); !os.IsNotExist(err) {
		t.Error("rebuild directory not removed")
	}
	if _, err := os.Stat(filepath.Join(root, "orphan", "rebuild")); !os.IsNotExist(err) {
		t.Error("orphan rebuild directory not removed")
	}
	if strings.Count(runGit(t, trunk, "worktree", "list", "--porcelain"), "worktree ") != 1 {
		t.Error("worktree registration not pruned")
	}
	logs := sink.String()
	for _, want := range []string{"removed stale rebuild worktree directory", "pruned stale rebuild worktree registrations", "trunk is missing"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q: %s", want, logs)
		}
	}
	CleanupStaleRebuildWorktrees(context.Background(), "", logger)
	CleanupStaleRebuildWorktrees(context.Background(), filepath.Join(root, "none"), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writeFile(t, filepath.Join(root, "ws", "rebuild", "job-3", "f"), "x")
	CleanupStaleRebuildWorktrees(ctx, root, slog.New(slog.DiscardHandler))
	if _, err := os.Stat(filepath.Join(root, "ws", "rebuild")); err != nil {
		t.Error("a cancelled context must stop the cleanup")
	}
	writeFile(t, filepath.Join(root, "broken", "rebuild", "x", "f"), "x")
	writeFile(t, filepath.Join(root, "broken", "trunk", "not-a-repo"), "x")
	CleanupStaleRebuildWorktrees(context.Background(), root, logger)
	if !strings.Contains(sink.String(), "worktree prune failed") && !strings.Contains(sink.String(), "cannot create git runner") {
		t.Errorf("non-repository trunk not logged: %s", sink.String())
	}
}
