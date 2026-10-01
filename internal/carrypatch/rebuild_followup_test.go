package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// Spec 01, task 7: a follow-up rebuild is enqueued when patch tips or the
// upstream base moved during the run (01-REQ-7).
// ===========================================================================

const (
	followupEvent = "hub.rebuild.followup"
	fuSubmitter   = "system:stale-snapshot"
	fuNewBaseSHA  = "dddd000000000000000000000000000000000001"
)

// installMovement wraps the trunk runner so that, after the first resolution
// of each branch (the snapshot), the named branches resolve to a different
// SHA, and so that the upstream base resolves to a new SHA from its second
// resolution on when upstreamMoves is set.
func (e *wtEnv) installMovement(movedTips []string, upstreamMoves bool) {
	moved := map[string]bool{}
	for _, b := range movedTips {
		moved[b] = true
	}
	var mu sync.Mutex
	counts := map[string]int{}
	upstreamCount := 0
	base := e.trunk.RunFunc
	e.trunk.RunFunc = func(ctx context.Context, args ...string) (string, error) {
		if br, ok := snapshotArgs(args); ok {
			mu.Lock()
			n := counts[br]
			counts[br]++
			mu.Unlock()
			if n > 0 && moved[br] {
				return "moved-" + br, nil
			}
			return "snap-" + br, nil
		}
		if len(args) == 3 && args[0] == "rev-parse" && args[1] == "--verify" && args[2] == "refs/remotes/upstream/HEAD^{commit}" {
			mu.Lock()
			n := upstreamCount
			upstreamCount++
			mu.Unlock()
			if n > 0 && upstreamMoves {
				return fuNewBaseSHA, nil
			}
			return wtBaseSHA, nil
		}
		return base(ctx, args...)
	}
}

// snapshotCalls counts the `rev-parse --verify refs/heads/<b>^{commit}` calls
// the trunk runner has seen.
func (e *wtEnv) snapshotCalls() int {
	e.trunk.mu.Lock()
	defer e.trunk.mu.Unlock()
	n := 0
	for _, c := range e.trunk.RunCalls {
		if _, ok := snapshotArgs(c.Args); ok {
			n++
		}
	}
	return n
}

func (e *wtEnv) setVariables(vars map[string]string) {
	e.h.GetVariable = func(_, _, key string) (string, error) {
		if v, ok := vars[key]; ok {
			return v, nil
		}
		return "", errors.New("not found")
	}
}

// fuEnv is a wtEnv with a real job queue, a running rebuild job and an audit
// capture.
type fuEnv struct {
	*wtEnv
	q      *jobqueue.Queue
	db     *sql.DB
	jobID  string
	ctx    context.Context
	audit  *cpAuditEmitter
	nonce  string
	branch string // integration branch used in payloads
}

const fuOrigNonce = "nonce-original"

// newFuEnv builds the environment and enqueues a running rebuild job whose
// group key is "g1".
func newFuEnv(t *testing.T, patches []Patch) *fuEnv {
	t.Helper()
	f := &fuEnv{wtEnv: newWtEnv(t, patches), audit: newCPAuditEmitter(), nonce: fuOrigNonce, branch: "integration-x"}
	f.q, f.db = newTestQueue(t)
	if err := RegisterRebuildJob(f.q, f.h); err != nil {
		t.Fatalf("RegisterRebuildJob: %v", err)
	}
	f.h.Queue = f.q
	f.h.Audit = f.audit
	id, _, err := f.q.Enqueue(jobqueue.EnqueueParams{
		Type: "rebuild", Key: f.slug, Nonce: f.nonce, Payload: f.fuPayload(StrategyMerge, FailModeContinue),
		SubmittedBy: "operator", Group: "g1",
	})
	if err != nil {
		t.Fatalf("Enqueue original: %v", err)
	}
	if _, err := f.db.Exec("UPDATE jobs SET status = 'running' WHERE id = ?", id); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	f.jobID = id
	f.ctx = jobqueue.ContextWithJobID(context.Background(), id)
	return f
}

func (f *fuEnv) fuPayload(strategy, failMode string) json.RawMessage {
	b, _ := json.Marshal(RebuildPayload{
		WorkspaceSlug:     f.slug,
		Strategy:          strategy,
		FailMode:          failMode,
		IntegrationBranch: f.branch,
		SubmittedBy:       "operator",
	})
	return b
}

func (f *fuEnv) run(strategy, failMode string) (any, bool, error) {
	f.t.Helper()
	return f.h.HandleRebuildJob(f.ctx, f.fuPayload(strategy, failMode))
}

type followupRow struct {
	ID, Key, Type, SubmittedBy, Group, Status, Nonce string
	Payload                                          RebuildPayload
}

// followUps returns every job submitted by the stale-snapshot system user.
func (f *fuEnv) followUps() []followupRow {
	f.t.Helper()
	rows, err := f.db.Query(`SELECT id, key, type, submitted_by, COALESCE(group_key,''), status, nonce, payload
		FROM jobs WHERE submitted_by = ?`, fuSubmitter)
	if err != nil {
		f.t.Fatalf("query follow-ups: %v", err)
	}
	defer rows.Close()
	var out []followupRow
	for rows.Next() {
		var r followupRow
		var payload string
		if err := rows.Scan(&r.ID, &r.Key, &r.Type, &r.SubmittedBy, &r.Group, &r.Status, &r.Nonce, &payload); err != nil {
			f.t.Fatalf("scan: %v", err)
		}
		if err := json.Unmarshal([]byte(payload), &r.Payload); err != nil {
			f.t.Fatalf("payload: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func (f *fuEnv) jobCount() int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&n); err != nil {
		f.t.Fatalf("count: %v", err)
	}
	return n
}

func (f *fuEnv) followupEvents() []audit.HubEvent {
	var out []audit.HubEvent
	for _, ev := range f.audit.Events() {
		if ev.EventType == followupEvent {
			out = append(out, ev)
		}
	}
	return out
}

func twoPatches() []Patch {
	return []Patch{
		{ID: "p1", BranchName: "feature/a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", BranchName: "feature/b", Position: 2, Status: PatchStatusConflict},
	}
}

// ===========================================================================
// TS-01-46: the stale-input check runs after the worktree is removed and with
// the lock free, on success and on a fail-fast conflict. Only attempted
// patches are checked; upstream is checked too.
// Requirement: 01-REQ-7.1
// ===========================================================================

func TestFollowup_TS_01_46_CheckRunsAfterRemovalAndUnlock(t *testing.T) {
	patches := append(twoPatches(),
		Patch{ID: "p3", BranchName: "feature/merged", Position: 3, Status: PatchStatusMergedUpstream},
		Patch{ID: "p4", BranchName: "feature/off", Position: 4, Status: PatchStatusDisabled},
	)
	moved := []string{"feature/a", "feature/b", "feature/merged", "feature/off"}

	for _, mode := range []string{"success", "fail-fast conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := newFuEnv(t, patches)
			f.installMovement(moved, true)
			f.trackState()
			failFast := mode == "fail-fast conflict"
			if failFast {
				var n int
				f.wt.CherryPickFunc = func(_ context.Context, _ string) error {
					n++
					if n == 2 {
						return &CherryPickConflictError{Files: []string{"x.txt"}}
					}
					return nil
				}
			}

			res, retryable, err := f.run(StrategyRebase, FailModeFailFast)
			if failFast {
				if err == nil || retryable || res != nil {
					t.Fatalf("want non-retryable conflict error, got res=%v retryable=%v err=%v", res, retryable, err)
				}
			} else if err != nil {
				t.Fatalf("HandleRebuildJob: retryable=%v err=%v", retryable, err)
			}

			events := f.events.snapshot()
			removeIdx := -1
			for i, ev := range events {
				if strings.HasPrefix(ev, "trunk:WorktreeRemove") {
					removeIdx = i
				}
			}
			if removeIdx < 0 {
				t.Fatalf("worktree never removed: %v", events)
			}
			// Every re-resolution of a patch tip (a second resolution of the
			// same branch) happens after the removal with the lock free.
			seen := map[string]int{}
			rechecks := 0
			for i, ev := range events {
				if !strings.HasPrefix(ev, "trunk:run rev-parse --verify refs/heads/") || !strings.Contains(ev, "^{commit}|") {
					continue
				}
				key := strings.SplitN(ev, "|", 2)[0]
				seen[key]++
				if seen[key] < 2 {
					continue
				}
				rechecks++
				if i < removeIdx {
					t.Errorf("tip re-resolved before the worktree was removed: %s", ev)
				}
				if !strings.Contains(ev, "locked=false") {
					t.Errorf("tip re-resolved with the lock held: %s", ev)
				}
			}
			if rechecks != 2 {
				t.Errorf("re-resolved %d tips, want 2 (the success and conflict patches); events=%v", rechecks, events)
			}
			for _, ev := range events {
				if strings.Contains(ev, "refs/heads/feature/merged") || strings.Contains(ev, "refs/heads/feature/off") {
					t.Errorf("a skipped patch was resolved: %s", ev)
				}
			}
			// The upstream base is resolved again after the removal.
			ups := 0
			for i, ev := range events {
				if strings.HasPrefix(ev, "trunk:run rev-parse --verify refs/remotes/upstream/HEAD^{commit}") {
					ups++
					if ups == 2 {
						if i < removeIdx || !strings.Contains(ev, "locked=false") {
							t.Errorf("upstream re-resolved too early or under the lock: %s", ev)
						}
					}
				}
			}
			if ups != 2 {
				t.Errorf("upstream resolved %d times, want 2", ups)
			}

			fe := f.followupEvents()
			if len(fe) != 1 {
				t.Fatalf("follow-up events = %d, want 1", len(fe))
			}
			if got := fe[0].Metadata["stale_patches"]; !reflect.DeepEqual(got, []string{"feature/a", "feature/b"}) {
				t.Errorf("stale_patches = %v, want [feature/a feature/b]", got)
			}
			if fe[0].Metadata["upstream_stale"] != true {
				t.Errorf("upstream_stale = %v, want true", fe[0].Metadata["upstream_stale"])
			}
		})
	}
}

// ===========================================================================
// TS-01-47: retryable errors, cancellation and other failures skip the check.
// Requirement: 01-REQ-7.2
// ===========================================================================

func TestFollowup_TS_01_47_FailuresSkipTheCheck(t *testing.T) {
	scenarios := []struct {
		name  string
		setup func(f *fuEnv, cancel context.CancelFunc)
	}{
		{"transient error", func(f *fuEnv, _ context.CancelFunc) {
			f.wt.CherryPickFunc = func(context.Context, string) error { return errors.New("boom") }
		}},
		{"cancellation", func(f *fuEnv, cancel context.CancelFunc) {
			f.wt.CherryPickFunc = func(ctx context.Context, _ string) error {
				cancel()
				return ctx.Err()
			}
		}},
		{"other failure", func(f *fuEnv, _ context.CancelFunc) {
			f.trunk.UpdateRefErr = errors.New("cannot lock ref")
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			f := newFuEnv(t, twoPatches())
			f.installMovement([]string{"feature/a", "feature/b"}, true)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			f.ctx = ctx
			sc.setup(f, cancel)

			res, _, err := f.run(StrategyRebase, FailModeFailFast)
			if err == nil || res != nil {
				t.Fatalf("want a failed run, got res=%v err=%v", res, err)
			}
			if n := len(f.followUps()); n != 0 {
				t.Errorf("%d follow-up jobs exist, want 0", n)
			}
			if n := len(f.followupEvents()); n != 0 {
				t.Errorf("%d follow-up events emitted, want 0", n)
			}
			// Nothing was re-resolved: one snapshot per attempted patch.
			if n := f.snapshotCalls(); n != 2 {
				t.Errorf("tips resolved %d times, want 2 (no re-resolution)", n)
			}
		})
	}
}

// ===========================================================================
// TS-01-48: a stale patch tip enqueues one follow-up unless
// AUTO_REBUILD_AFTER_PUSH is "false".
// Requirement: 01-REQ-7.3
// ===========================================================================

func TestFollowup_TS_01_48_StalePatchHonoursAutoRebuildAfterPush(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
		want int
	}{
		{"unset", nil, 1},
		{"push true", map[string]string{"AUTO_REBUILD_AFTER_PUSH": "true"}, 1},
		{"push false", map[string]string{"AUTO_REBUILD_AFTER_PUSH": "false"}, 0},
		{"sync false only", map[string]string{"AUTO_REBUILD_AFTER_SYNC": "false"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFuEnv(t, twoPatches())
			f.installMovement([]string{"feature/b"}, false)
			f.setVariables(tc.vars)

			if _, retryable, err := f.run(StrategyRebase, FailModeFailFast); err != nil {
				t.Fatalf("HandleRebuildJob: retryable=%v err=%v", retryable, err)
			}
			if n := len(f.followUps()); n != tc.want {
				t.Errorf("follow-ups = %d, want %d", n, tc.want)
			}
			if n := len(f.followupEvents()); n != tc.want {
				t.Errorf("follow-up events = %d, want %d", n, tc.want)
			}
		})
	}
}

// ===========================================================================
// TS-01-49: a stale upstream base enqueues one follow-up unless
// AUTO_REBUILD_AFTER_SYNC is "false".
// Requirement: 01-REQ-7.4
// ===========================================================================

func TestFollowup_TS_01_49_StaleUpstreamHonoursAutoRebuildAfterSync(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
		want int
	}{
		{"unset", nil, 1},
		{"sync false", map[string]string{"AUTO_REBUILD_AFTER_SYNC": "false"}, 0},
		{"push false only", map[string]string{"AUTO_REBUILD_AFTER_PUSH": "false"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFuEnv(t, twoPatches())
			f.installMovement(nil, true)
			f.setVariables(tc.vars)

			if _, retryable, err := f.run(StrategyRebase, FailModeFailFast); err != nil {
				t.Fatalf("HandleRebuildJob: retryable=%v err=%v", retryable, err)
			}
			if n := len(f.followUps()); n != tc.want {
				t.Errorf("follow-ups = %d, want %d", n, tc.want)
			}
			if tc.want == 1 {
				fe := f.followupEvents()
				if len(fe) != 1 || fe[0].Metadata["upstream_stale"] != true {
					t.Fatalf("follow-up events = %+v, want one with upstream_stale", fe)
				}
				if p, _ := fe[0].Metadata["stale_patches"].([]string); len(p) != 0 {
					t.Errorf("stale_patches = %v, want none", p)
				}
			}
		})
	}

	t.Run("patch follow-up suppressed but upstream still enqueues", func(t *testing.T) {
		f := newFuEnv(t, twoPatches())
		f.installMovement([]string{"feature/a"}, true)
		f.setVariables(map[string]string{"AUTO_REBUILD_AFTER_PUSH": "false"})
		if _, _, err := f.run(StrategyRebase, FailModeFailFast); err != nil {
			t.Fatalf("HandleRebuildJob: %v", err)
		}
		fe := f.followupEvents()
		if len(f.followUps()) != 1 || len(fe) != 1 {
			t.Fatalf("follow-ups=%d events=%d, want 1/1", len(f.followUps()), len(fe))
		}
		if p, _ := fe[0].Metadata["stale_patches"].([]string); len(p) != 0 {
			t.Errorf("stale_patches = %v, want none: the push variable is false", p)
		}
	})
}

// ===========================================================================
// TS-01-50: at most one follow-up, with the original payload, the system
// submitter, a fresh nonce and the running job's group key; it stays queued.
// Requirement: 01-REQ-7.5
// ===========================================================================

func TestFollowup_TS_01_50_OneFollowupWithOriginalPayloadAndGroupKey(t *testing.T) {
	f := newFuEnv(t, twoPatches())
	f.installMovement([]string{"feature/a", "feature/b"}, true)

	if _, retryable, err := f.run(StrategyMerge, FailModeContinue); err != nil {
		t.Fatalf("HandleRebuildJob: retryable=%v err=%v", retryable, err)
	}

	if n := f.jobCount(); n != 2 {
		t.Fatalf("jobs = %d, want the original plus exactly one follow-up", n)
	}
	fu := f.followUps()
	if len(fu) != 1 {
		t.Fatalf("follow-ups = %d, want 1", len(fu))
	}
	got := fu[0]
	if got.Type != "rebuild" || got.Key != f.slug || got.SubmittedBy != fuSubmitter {
		t.Errorf("follow-up = %+v", got)
	}
	if got.Payload.Strategy != StrategyMerge || got.Payload.FailMode != FailModeContinue ||
		got.Payload.IntegrationBranch != f.branch || got.Payload.WorkspaceSlug != f.slug {
		t.Errorf("payload = %+v, want the original strategy, fail mode and integration branch", got.Payload)
	}
	if got.Nonce == "" || got.Nonce == fuOrigNonce {
		t.Errorf("nonce = %q, want a fresh one", got.Nonce)
	}
	if got.Group != "g1" {
		t.Errorf("group_key = %q, want the running job's g1", got.Group)
	}
	if got.Status != jobqueue.StatusQueued {
		t.Errorf("status = %q, want queued", got.Status)
	}
	// The original job is untouched: still running, and the run itself
	// does not finalise it.
	orig, err := f.q.GetByID(f.jobID)
	if err != nil || orig.Status != jobqueue.StatusRunning {
		t.Errorf("original job = %+v err=%v, want still running", orig, err)
	}

	// A later enqueue (push hook, sync) is deduplicated against the queued
	// follow-up.
	id, dup, err := f.q.Enqueue(jobqueue.EnqueueParams{
		Type: "rebuild", Key: f.slug, Nonce: "nonce-later", Payload: f.fuPayload(StrategyRebase, ""),
	})
	if err != nil || !dup || id != got.ID {
		t.Errorf("later enqueue = (%q, dup=%v, err=%v), want (%q, true, nil)", id, dup, err, got.ID)
	}
}

func TestFollowup_TS_01_50_GroupKeyFallback(t *testing.T) {
	f := newFuEnv(t, twoPatches())
	f.installMovement([]string{"feature/a"}, false)
	// The running job is not in the queue: GetByID fails, so the group key
	// falls back to FormatGroupKey(slug, integration branch). The original
	// row is removed so it cannot dedup the follow-up.
	if _, err := f.db.Exec("DELETE FROM jobs WHERE id = ?", f.jobID); err != nil {
		t.Fatal(err)
	}

	if _, retryable, err := f.run(StrategyRebase, FailModeFailFast); err != nil {
		t.Fatalf("HandleRebuildJob: retryable=%v err=%v", retryable, err)
	}
	fu := f.followUps()
	if len(fu) != 1 {
		t.Fatalf("follow-ups = %d, want 1", len(fu))
	}
	if want := FormatGroupKey(f.slug, f.branch); fu[0].Group != want {
		t.Errorf("group_key = %q, want %q", fu[0].Group, want)
	}
}

// ===========================================================================
// TS-01-52: hub.rebuild.followup carries stale_patches, upstream_stale and
// follow_up_job_id.
// Requirement: 01-REQ-7.7
// ===========================================================================

func TestFollowup_TS_01_52_AuditEvent(t *testing.T) {
	patches := []Patch{
		{ID: "p1", BranchName: "a", Position: 1, Status: PatchStatusActive},
		{ID: "p2", BranchName: "b", Position: 2, Status: PatchStatusActive},
	}
	f := newFuEnv(t, patches)
	f.installMovement([]string{"a", "b"}, true)

	if _, retryable, err := f.run(StrategyRebase, FailModeFailFast); err != nil {
		t.Fatalf("HandleRebuildJob: retryable=%v err=%v", retryable, err)
	}
	fe := f.followupEvents()
	if len(fe) != 1 {
		t.Fatalf("follow-up events = %d, want 1", len(fe))
	}
	ev := fe[0]
	if !reflect.DeepEqual(ev.Metadata["stale_patches"], []string{"a", "b"}) {
		t.Errorf("stale_patches = %v", ev.Metadata["stale_patches"])
	}
	if ev.Metadata["upstream_stale"] != true {
		t.Errorf("upstream_stale = %v", ev.Metadata["upstream_stale"])
	}
	fu := f.followUps()
	if len(fu) != 1 || ev.Metadata["follow_up_job_id"] != fu[0].ID {
		t.Errorf("follow_up_job_id = %v, want the follow-up job %v", ev.Metadata["follow_up_job_id"], fu)
	}
	if ev.Workspace != f.slug {
		t.Errorf("event workspace = %q, want %q", ev.Workspace, f.slug)
	}
	// The event precedes the completion event.
	var types []string
	for _, e := range f.audit.Events() {
		types = append(types, e.EventType)
	}
	if fmt.Sprint(types) != "[hub.rebuild.followup hub.rebuild.complete]" {
		t.Errorf("audit events = %v, want [hub.rebuild.followup hub.rebuild.complete]", types)
	}
}

// ===========================================================================
// TS-01-53: a nil Queue or an enqueue error logs a warning and leaves the job
// outcome unchanged.
// Requirement: 01-REQ-7.8
// ===========================================================================

func TestFollowup_TS_01_53_NilQueueOrEnqueueErrorKeepsOutcome(t *testing.T) {
	runOutcome := func(t *testing.T, e *wtEnv, ctx context.Context) (string, bool, string) {
		t.Helper()
		res, retryable, err := e.h.HandleRebuildJob(ctx, e.payload(StrategyRebase, FailModeFailFast))
		raw, _ := json.Marshal(res)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		return string(raw), retryable, msg
	}

	baseline := newWtEnv(t, twoPatches())
	baseline.installMovement(nil, false)
	wantRes, wantRetry, wantErr := runOutcome(t, baseline, context.Background())
	if wantErr != "" {
		t.Fatalf("baseline run failed: %s", wantErr)
	}

	t.Run("nil queue", func(t *testing.T) {
		e := newWtEnv(t, twoPatches())
		e.installMovement([]string{"feature/a"}, true)
		e.h.Queue = nil
		gotRes, gotRetry, gotErr := runOutcome(t, e, context.Background())
		if gotRes != wantRes || gotRetry != wantRetry || gotErr != wantErr {
			t.Errorf("outcome changed: res=%s retry=%v err=%q", gotRes, gotRetry, gotErr)
		}
		if !e.logs.has(slog.LevelWarn, "follow-up") {
			t.Error("no warning logged for the missing queue")
		}
	})

	t.Run("enqueue error", func(t *testing.T) {
		e := newWtEnv(t, twoPatches())
		e.installMovement([]string{"feature/a"}, true)
		aud := newCPAuditEmitter()
		e.h.Audit = aud
		// A queue without the rebuild type registered rejects every enqueue.
		e.h.Queue, _ = newTestQueue(t)
		gotRes, gotRetry, gotErr := runOutcome(t, e, context.Background())
		if gotRes != wantRes || gotRetry != wantRetry || gotErr != wantErr {
			t.Errorf("outcome changed: res=%s retry=%v err=%q", gotRes, gotRetry, gotErr)
		}
		if !e.logs.has(slog.LevelWarn, "follow-up") {
			t.Error("no warning logged for the enqueue error")
		}
		for _, ev := range aud.Events() {
			if ev.EventType == followupEvent {
				t.Error("hub.rebuild.followup emitted although nothing was enqueued")
			}
		}
	})
}
