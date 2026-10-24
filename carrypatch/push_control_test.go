package carrypatch

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/aflib/jobqueue"
)

// pushEnv is a carry_patch workspace "ws" whose trunk's origin is a real
// bare fork, with feature/a registered as a patch.
type pushEnv struct {
	db    *sql.DB
	f     *forkTrunk
	root  string
	vars  map[string]string
	reads map[string]int
	logs  *logSink
	deps  PreReceiveHookDeps
}

func newPushEnv(t *testing.T) *pushEnv {
	t.Helper()
	p := &pushEnv{db: openDB(t), root: t.TempDir(), vars: map[string]string{}, reads: map[string]int{}}
	p.f = newForkTrunk(t, p.root, "ws")
	seedWS(t, p.db, ws{slug: "ws", gitURL: "https://alice:secret@github.com/example/fork.git"})
	seedWS(t, p.db, ws{slug: "std", mode: "standard"})
	seedPatch(t, p.db, "ws", "p1", "feature/a", 1, "active")
	seedPatch(t, p.db, "ws", "p2", "feature/gone", -2, "deleted")
	seedPatch(t, p.db, "std", "p3", "feature/a", 1, "active")
	logger, sink := newLogger()
	p.logs = sink
	p.deps = PreReceiveHookDeps{GetVariable: varsOf(p.vars, p.reads), ResolveAuth: noAuth, WorkspaceRoot: p.root, Logger: logger}
	return p
}

func (p *pushEnv) update(branch string, newSHA string) RefUpdate {
	upd := RefUpdate{Name: plumbing.ReferenceName("refs/heads/" + branch)}
	if newSHA != "" {
		upd.New = plumbing.NewHash(newSHA)
	}
	return upd
}

// Push control applies only to registered, non-deleted patch branches of a
// carry_patch workspace in origin mode; everything else passes, and a lookup
// failure fails open. A standard workspace reads no setting.
func TestPreReceiveHook_Applicability(t *testing.T) {
	p := newPushEnv(t)
	hook := NewPreReceiveHook(p.deps)
	ctx := context.Background()
	p.vars["PATCH_BRANCH_SOURCE"] = "origin"
	cases := map[string]struct {
		slug string
		upd  RefUpdate
	}{
		"tag":                    {"ws", RefUpdate{Name: "refs/tags/v1"}},
		"unknown workspace":      {"nope", p.update("feature/a", p.f.base)},
		"standard workspace":     {"std", p.update("feature/a", p.f.base)},
		"integration branch":     {"ws", p.update("integration", p.f.base)},
		"unregistered branch":    {"ws", p.update("feature/other", p.f.base)},
		"soft-deleted patch":     {"ws", p.update("feature/gone", p.f.base)},
		"registered in hub mode": {"ws", p.update("feature/a", p.f.base)},
	}
	for name, c := range cases {
		if name == "registered in hub mode" {
			p.vars["PATCH_BRANCH_SOURCE"] = "hub"
		}
		if err := hook(ctx, p.db, c.slug, alice, c.upd); err != nil {
			t.Errorf("%s: rejected: %v", name, err)
		}
	}
	if p.reads["PATCH_BRANCH_SOURCE"] != 1 || p.reads["PUSH_PATCHES_TO_ORIGIN"] != 0 {
		t.Errorf("settings read for non-applicable updates: %v", p.reads)
	}
	if !strings.Contains(p.logs.String(), "failed to load workspace info") {
		t.Error("unknown workspace not logged as a fail-open")
	}
	p.db.Close()
	if err := hook(ctx, p.db, "ws", alice, p.update("feature/a", p.f.base)); err != nil {
		t.Errorf("closed database must fail open: %v", err)
	}
}

// In origin mode without forwarding the hub refuses the push and names the
// fork without its credentials; the two settings are read on every update.
func TestPreReceiveHook_RejectsPushInOriginMode(t *testing.T) {
	p := newPushEnv(t)
	hook := NewPreReceiveHook(p.deps)
	p.vars["PATCH_BRANCH_SOURCE"] = "origin"
	for _, value := range []string{"", "false", "TRUE", "yes"} {
		if value != "" {
			p.vars["PUSH_PATCHES_TO_ORIGIN"] = value
		}
		err := hook(context.Background(), p.db, "ws", alice, p.update("feature/a", p.f.base))
		if err == nil || err.Error() != "branch is synced from origin; push to https://github.com/example/fork.git instead" {
			t.Errorf("PUSH_PATCHES_TO_ORIGIN=%q: %v", value, err)
		}
	}
	if p.reads["PATCH_BRANCH_SOURCE"] != 4 || p.reads["PUSH_PATCHES_TO_ORIGIN"] != 4 {
		t.Errorf("reads = %v, want one of each per update", p.reads)
	}
	if !strings.Contains(p.logs.String(), "rejecting push") || !strings.Contains(p.logs.String(), "user=alice") {
		t.Errorf("rejection not logged with the actor: %s", p.logs.String())
	}
	if err := hook(context.Background(), p.db, "ws", nil, p.update("feature/a", p.f.base)); err == nil || !strings.Contains(p.logs.String(), "user=unknown") {
		t.Error("anonymous actor not logged as unknown")
	}
	for value, want := range map[string]bool{"true": true, "false": false, "True": false, "": false} {
		vars := map[string]string{}
		if value != "" {
			vars["PUSH_PATCHES_TO_ORIGIN"] = value
		}
		if got := pushPatchesEnabled(varsOf(vars, nil), "ws"); got != want {
			t.Errorf("pushPatchesEnabled(%q) = %v", value, got)
		}
	}
	if pushPatchesEnabled(nil, "ws") {
		t.Error("nil lookup enabled forwarding")
	}
}

// With forwarding on, the pushed commit is pushed to the fork's branch
// through a temporary ref before the hub writes its own; deletes and
// credential failures are refused without contacting the fork; a refused
// forward refuses the push with the sanitised reason.
func TestPreReceiveHook_ForwardsToOrigin(t *testing.T) {
	p := newPushEnv(t)
	p.vars["PATCH_BRANCH_SOURCE"] = "origin"
	p.vars["PUSH_PATCHES_TO_ORIGIN"] = "true"
	ctx := context.Background()
	// The commit exists in the trunk but on no branch, as after an unpack.
	runGit(t, p.f.trunk, "checkout", "-q", "--detach", p.f.base)
	pushed := commitFile(t, p.f.trunk, "a.txt", "a\n", "pushed to the hub")
	runGit(t, p.f.trunk, "checkout", "-q", "main")

	var opts *git.PushOptions
	recording := func(ctx context.Context, repo *git.Repository, o *git.PushOptions) error {
		opts = o
		return defaultPushContext(ctx, repo, o)
	}
	p.deps.Push = recording
	hook := NewPreReceiveHook(p.deps)
	if err := hook(ctx, p.db, "ws", alice, p.update("feature/a", pushed)); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if refOf(p.f.fork, "feature/a") != pushed {
		t.Error("fork branch not updated")
	}
	if opts == nil || opts.Force || opts.RemoteName != "origin" || len(opts.RefSpecs) != 1 || string(opts.RefSpecs[0]) != "refs/hub/forward/feature/a:refs/heads/feature/a" {
		t.Errorf("push options = %+v", opts)
	}
	if refOf(p.f.trunk, "refs/hub/forward/feature/a") != "" {
		t.Error("temporary ref not removed")
	}
	if err := hook(ctx, p.db, "ws", alice, p.update("feature/a", pushed)); err != nil {
		t.Errorf("up-to-date forward: %v", err)
	}

	// A non-fast-forward is refused by the fork, so the hub refuses too.
	if err := hook(ctx, p.db, "ws", alice, p.update("feature/a", p.f.base)); err == nil || !strings.HasPrefix(err.Error(), "origin rejected push: ") {
		t.Errorf("rejected forward = %v", err)
	}
	if refOf(p.f.fork, "feature/a") != pushed {
		t.Error("fork moved on a refused forward")
	}
	p.deps.Push = func(context.Context, *git.Repository, *git.PushOptions) error {
		return errors.New("https://alice:secret@github.com/x: forbidden")
	}
	hook = NewPreReceiveHook(p.deps)
	err := hook(ctx, p.db, "ws", alice, p.update("feature/a", pushed))
	if err == nil || err.Error() != "origin rejected push: https://github.com/x: forbidden" || strings.Contains(p.logs.String(), "secret") {
		t.Errorf("sanitised failure = %v", err)
	}

	contacted := false
	p.deps.Push = func(context.Context, *git.Repository, *git.PushOptions) error { contacted = true; return nil }
	hook = NewPreReceiveHook(p.deps)
	if err := hook(ctx, p.db, "ws", alice, p.update("feature/a", "")); err == nil || err.Error() != "branch is synced from origin; delete it on the fork instead" {
		t.Errorf("delete = %v", err)
	}
	p.deps.ResolveAuth = func(string) (transport.AuthMethod, error) { return nil, errors.New("no creds") }
	if err := NewPreReceiveHook(p.deps)(ctx, p.db, "ws", alice, p.update("feature/a", pushed)); err == nil || err.Error() != "failed to resolve origin credentials" {
		t.Errorf("credential failure = %v", err)
	}
	p.deps.ResolveAuth = nil
	if err := NewPreReceiveHook(p.deps)(ctx, p.db, "ws", alice, p.update("feature/a", pushed)); err == nil || err.Error() != "failed to resolve origin credentials" {
		t.Errorf("no resolver = %v", err)
	}
	if contacted {
		t.Error("fork contacted for a delete or without credentials")
	}
	p.deps.ResolveAuth, p.deps.WorkspaceRoot = noAuth, t.TempDir()
	if err := NewPreReceiveHook(p.deps)(ctx, p.db, "ws", alice, p.update("feature/a", pushed)); err == nil || !strings.Contains(err.Error(), "open trunk") {
		t.Errorf("missing trunk = %v", err)
	}
}

func TestStripUserinfo_SingleURL(t *testing.T) {
	cases := map[string]string{
		"https://alice:tok@github.com/o/r.git": "https://github.com/o/r.git",
		"https://github.com/o/r.git":           "https://github.com/o/r.git",
		"ssh://git@host:22/r":                  "ssh://host:22/r",
		"http://a:b@h/x with space":            "http://h/x with space",
	}
	for in, want := range cases {
		if got := stripUserinfo(in); got != want {
			t.Errorf("stripUserinfo(%q) = %q, want %q", in, got, want)
		}
	}
}

// The post-push hook enqueues one rebuild when a pushed branch is a
// registered patch of a carry_patch workspace, unless AUTO_REBUILD_AFTER_PUSH
// is "false"; a queued or running rebuild suppresses another.
func TestPostPushHook_EnqueuesRebuildForRegisteredPatch(t *testing.T) {
	p := newPushEnv(t)
	q := newQueue(t, p.db, nil)
	p.vars["REBUILD_STRATEGY"] = StrategyMerge
	hook := NewPostPushRebuildHook(q, varsOf(p.vars, nil), PostPushMirrorDeps{})
	count := func() int { jobs, _ := q.ListByKey("rebuild", "ws"); return len(jobs) }

	hook(p.db, "ws", []string{"main", "feature/other"})
	hook(p.db, "std", []string{"feature/a"})
	hook(p.db, "nope", []string{"feature/a"})
	if count() != 0 {
		t.Fatal("rebuild enqueued for an unregistered branch, a standard or unknown workspace")
	}
	hook(p.db, "ws", []string{"main", "feature/a"})
	jobs, _ := q.ListByKey("rebuild", "ws")
	if len(jobs) != 1 || jobs[0].SubmittedBy != "system:push-hook" || jobs[0].GroupKey != "ws:integration" || !strings.Contains(string(jobs[0].Payload), `"strategy":"merge"`) {
		t.Fatalf("jobs = %+v", jobs)
	}
	hook(p.db, "ws", []string{"feature/a"})
	if count() != 1 {
		t.Error("duplicate rebuild enqueued")
	}
	mustExec(t, p.db, `DELETE FROM jobs`)
	p.vars["AUTO_REBUILD_AFTER_PUSH"] = "false"
	hook(p.db, "ws", []string{"feature/a"})
	if count() != 0 {
		t.Error("rebuild enqueued although auto rebuild after push is off")
	}
	delete(p.vars, "AUTO_REBUILD_AFTER_PUSH")
	NewPostPushRebuildHook(q, nil, PostPushMirrorDeps{})(p.db, "ws", []string{"feature/a"})
	if count() != 1 {
		t.Error("nil lookup must enqueue")
	}
}

// mirrorEnv returns the post-push env with mirror dependencies in hub mode
// with forwarding on, and an emitter.
func mirrorEnv(t *testing.T) (*pushEnv, PostPushMirrorDeps, *recordingEmitter) {
	t.Helper()
	p := newPushEnv(t)
	p.vars["PUSH_PATCHES_TO_ORIGIN"] = "true"
	emitter := &recordingEmitter{}
	return p, PostPushMirrorDeps{ResolveAuth: noAuth, WorkspaceRoot: p.root, Audit: emitter}, emitter
}

// In hub mode with forwarding on, the post-push hook force-mirrors each
// registered patch branch to the fork after the rebuild enqueue; the
// integration branch and unregistered branches are skipped.
func TestPostPushHook_MirrorsPatchBranchesToFork(t *testing.T) {
	p, deps, emitter := mirrorEnv(t)
	q := newQueue(t, p.db, nil)
	logger, _ := newLogger()
	hook := newPostPushRebuildHookWithLogger(q, varsOf(p.vars, p.reads), deps, logger)
	first := p.f.localBranch(t, "feature/a", "a.txt", "one\n")
	p.f.localBranch(t, "integration", "i.txt", "i\n")
	p.f.localBranch(t, "feature/other", "o.txt", "o\n")

	hook(p.db, "ws", []string{"feature/a", "integration", "feature/other"})
	if refOf(p.f.fork, "feature/a") != first || refOf(p.f.fork, "integration") != "" || refOf(p.f.fork, "feature/other") != "" {
		t.Errorf("fork refs: a=%s integration=%s other=%s", refOf(p.f.fork, "feature/a"), refOf(p.f.fork, "integration"), refOf(p.f.fork, "feature/other"))
	}
	if jobs, _ := q.ListByKey("rebuild", "ws"); len(jobs) != 1 {
		t.Error("rebuild not enqueued alongside the mirror")
	}
	// A rewritten branch is force-mirrored.
	runGit(t, p.f.trunk, "update-ref", "refs/heads/feature/a", p.f.base)
	hook(p.db, "ws", []string{"feature/a"})
	if refOf(p.f.fork, "feature/a") != p.f.base {
		t.Error("rewrite not mirrored")
	}
	if len(emitter.events) != 0 {
		t.Errorf("events on success: %v", emitter.events)
	}
}

// Mirroring needs every dependency, a carry_patch workspace, forwarding on,
// hub mode and an applicable branch; only then are credentials resolved.
func TestMirror_NotApplicableCases(t *testing.T) {
	p, deps, _ := mirrorEnv(t)
	logger, _ := newLogger()
	authed := 0
	deps.ResolveAuth = func(string) (transport.AuthMethod, error) { authed++; return nil, nil }
	p.f.localBranch(t, "feature/a", "a.txt", "a\n")
	run := func(d PostPushMirrorDeps, slug string, branches []string) {
		mirrorBranches(p.db, slug, branches, d, varsOf(p.vars, p.reads), logger)
		if refOf(p.f.fork, "feature/a") != "" {
			t.Fatalf("mirrored for slug=%s branches=%v deps=%+v vars=%v", slug, branches, d, p.vars)
		}
	}
	run(PostPushMirrorDeps{WorkspaceRoot: p.root, Audit: deps.Audit}, "ws", []string{"feature/a"})
	run(PostPushMirrorDeps{ResolveAuth: noAuth, Audit: deps.Audit}, "ws", []string{"feature/a"})
	run(PostPushMirrorDeps{ResolveAuth: noAuth, WorkspaceRoot: p.root}, "ws", []string{"feature/a"})
	run(deps, "std", []string{"feature/a"})
	run(deps, "nope", []string{"feature/a"})
	if p.reads["PUSH_PATCHES_TO_ORIGIN"] != 0 {
		t.Error("setting read for a standard or unknown workspace")
	}
	p.vars["PATCH_BRANCH_SOURCE"] = "origin"
	run(deps, "ws", []string{"feature/a"})
	p.vars["PATCH_BRANCH_SOURCE"] = "hub"
	p.vars["PUSH_PATCHES_TO_ORIGIN"] = "false"
	run(deps, "ws", []string{"feature/a"})
	p.vars["PUSH_PATCHES_TO_ORIGIN"] = "true"
	run(deps, "ws", []string{"integration", "feature/other", "feature/gone"})
	if authed != 0 {
		t.Error("credentials resolved without an applicable branch")
	}
	if !deps.mirrorEnabled() || (PostPushMirrorDeps{}).mirrorEnabled() {
		t.Error("mirrorEnabled")
	}
}

// A failed mirror is logged with credentials removed and audited per branch
// as hub.patch.mirror_failed; a credential failure fails every applicable
// branch; an emitter failure is logged only.
func TestMirror_FailuresAreAuditedPerBranch(t *testing.T) {
	p, deps, emitter := mirrorEnv(t)
	logger, sink := newLogger()
	p.f.localBranch(t, "feature/a", "a.txt", "a\n")
	seedPatch(t, p.db, "ws", "p4", "feature/b", 2, "conflict")
	p.f.localBranch(t, "feature/b", "b.txt", "b\n")

	deps.ResolveAuth = func(string) (transport.AuthMethod, error) { return nil, errors.New("vault at https://u:pw@vault/x down") }
	mirrorBranches(p.db, "ws", []string{"feature/a", "feature/b"}, deps, varsOf(p.vars, nil), logger)
	events := emitter.ofType(EventPatchMirrorFailed)
	if len(events) != 2 || events[0].Metadata["branch_name"] != "feature/a" || events[1].Metadata["branch_name"] != "feature/b" || events[0].ActorType != "system" || events[0].Action != "mirror" {
		t.Errorf("credential failure events = %+v", events)
	}
	if msg := events[0].Metadata["error"].(string); !strings.HasPrefix(msg, "failed to resolve origin credentials: ") || strings.Contains(msg, "pw") || strings.Contains(sink.String(), "pw") {
		t.Errorf("unsanitised error: %q / %s", msg, sink.String())
	}

	emitter.events = nil
	deps.ResolveAuth = noAuth
	deps.WorkspaceRoot = t.TempDir()
	mirrorBranches(p.db, "ws", []string{"feature/a"}, deps, varsOf(p.vars, nil), logger)
	if events := emitter.ofType(EventPatchMirrorFailed); len(events) != 1 || !strings.Contains(events[0].Metadata["error"].(string), "open trunk") {
		t.Errorf("push failure events = %+v", events)
	}
	if !strings.Contains(sink.String(), "failed to push branch to origin") {
		t.Error("push failure not logged")
	}
	emitter.err = errors.New("audit down")
	mirrorBranches(p.db, "ws", []string{"feature/a"}, deps, varsOf(p.vars, nil), logger)
	if !strings.Contains(sink.String(), "failed to emit mirror_failed event") {
		t.Error("emitter failure not logged")
	}
	emitMirrorFailedEvent(nil, "ws", "feature/a", "x", logger)
	_ = jobqueue.StatusQueued
}
