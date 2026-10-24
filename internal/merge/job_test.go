package merge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/aflib/jobqueue"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

func payload(source string) json.RawMessage {
	b, _ := json.Marshal(MergePayload{WorkspaceSlug: ws, TargetBranch: "main", SourceRef: source, SubmittedBy: owner})
	return b
}

// A merge job fetches, rebases the source onto the target, fast-forwards
// the target, deletes the source and answers both SHAs with
// hub.merge.complete; consecutive jobs leave the trunk on the target branch
// with a clean tree, so the next one works.
func TestHandleMergeJob_MergesAndLeavesTrunkOnTarget(t *testing.T) {
	root := t.TempDir()
	trunk := setupTrunk(t, root, ws)
	var fetched []string
	h := newHandler(root, &fetched)
	rec := &recorder{}
	h.Audit = rec
	for _, source := range []string{"feature/clean", "feature/a"} {
		base := gitIn(t, trunk, "rev-parse", "main")
		result, retryable, err := h.HandleMergeJob(context.Background(), payload(source))
		if err != nil || retryable {
			t.Fatalf("%s: %v retryable %v", source, err, retryable)
		}
		merged := gitIn(t, trunk, "rev-parse", "main")
		if r := result.(*MergeResult); r.BaseSHA != base || r.MergedSHA != merged || merged == base {
			t.Errorf("%s: result %+v, main %s -> %s", source, r, base, merged)
		}
		if gitIn(t, trunk, "symbolic-ref", "--short", "HEAD") != "main" || gitIn(t, trunk, "status", "--porcelain") != "" ||
			gitIn(t, trunk, "branch", "--list", source) != "" {
			t.Errorf("%s: trunk on %s, status %q, branches %s", source, gitIn(t, trunk, "symbolic-ref", "--short", "HEAD"), gitIn(t, trunk, "status", "--porcelain"), gitIn(t, trunk, "branch", "--list"))
		}
	}
	if _, err := os.Stat(filepath.Join(trunk, "feature-a.txt")); err != nil {
		t.Error("main's tree lacks the merged file")
	}
	if len(fetched) != 2 || fetched[0] != filepath.Join(root, ws, "trunk")+" main" {
		t.Errorf("fetched %v", fetched)
	}
	evs := rec.all()
	if len(evs) != 2 || evs[0].EventType != "hub.merge.complete" || evs[0].ResourceType != "merge" || evs[0].ActorType != "system" || evs[0].Workspace != ws ||
		evs[0].Metadata["merged_sha"] != gitIn(t, trunk, "rev-parse", "main^") {
		t.Errorf("events %+v", evs)
	}
}

// The job's other outcomes: an already merged source is an idempotent
// success at the target's head; a conflict is a permanent, structured
// failure naming the files; a source at the target's head is retryable; a
// failed fetch is retryable; a payload that is not JSON is a permanent
// invalid_payload. Every failure emits hub.merge.fail with the reason.
func TestHandleMergeJob_Outcomes(t *testing.T) {
	root := t.TempDir()
	trunk := setupTrunk(t, root, ws)
	h := newHandler(root, nil)
	rec := &recorder{}
	h.Audit = rec
	main := gitIn(t, trunk, "rev-parse", "main")
	result, retryable, err := h.HandleMergeJob(context.Background(), payload("feature/already-merged"))
	if r, ok := result.(*MergeResult); err != nil || retryable || !ok || r.BaseSHA != main || r.MergedSHA != main {
		t.Errorf("already merged: (%v, %v, %v)", result, retryable, err)
	}
	cases := []struct {
		name      string
		payload   json.RawMessage
		fetchErr  error
		retryable bool
		want      string // the error
	}{
		{"conflict", payload("feature/conflict"), nil, false, `{"reason":"WouldConflict","conflict_files":["base.txt"]}`},
		{"not ready", payload("feature/not-ready"), nil, true, `{"reason":"BranchNotReady"}`},
		{"fetch fails", payload("feature/clean"), errors.New("remote down"), true, "remote down"},
		{"bad payload", json.RawMessage(`nope`), nil, false, `{"reason":"invalid_payload"}`},
	}
	for i, c := range cases {
		fetch := h.Fetch
		if c.fetchErr != nil {
			h.Fetch = func(context.Context, string, string, transport.AuthMethod) error { return c.fetchErr }
		}
		result, retryable, err := h.HandleMergeJob(context.Background(), c.payload)
		h.Fetch = fetch
		if result != nil || retryable != c.retryable || err == nil || err.Error() != c.want {
			t.Errorf("%s: (%v, %v, %v), want retryable %v error %s", c.name, result, retryable, err, c.retryable, c.want)
		}
		if evs := rec.all(); i < 3 && (len(evs) != i+1 || evs[i].EventType != "hub.merge.fail" || evs[i].Metadata["reason"] != c.want) {
			t.Errorf("%s: events %+v", c.name, evs)
		}
	}
	if gitIn(t, trunk, "rev-parse", "main") != main || gitIn(t, trunk, "status", "--porcelain") != "" {
		t.Error("a failed job moved main or left the tree dirty")
	}
	h.Audit = &recorder{err: errors.New("down")}
	if _, _, err := h.HandleMergeJob(context.Background(), payload("feature/already-merged")); err != nil {
		t.Errorf("with a failing emitter: %v", err)
	}
}

// RegisterHandler registers the merge job type once; without a Handler the
// registered job fails as not configured. EnqueueMergeJob keys the job by
// workspace, target and source and reports a duplicate.
func TestRegisterHandler_EnqueueMergeJob(t *testing.T) {
	v := newEnv(t)
	if err := RegisterHandler(v.queue, nil); err == nil {
		t.Error("a second registration of the merge type succeeded")
	}
	id, duplicate, err := EnqueueMergeJob(v.queue, ws, "main", "feature/a", owner)
	if err != nil || duplicate || id == "" {
		t.Fatalf("EnqueueMergeJob: %q, %v, %v", id, duplicate, err)
	}
	job, _ := v.queue.GetByID(id)
	var p MergePayload
	if json.Unmarshal(job.Payload, &p) != nil || p != (MergePayload{WorkspaceSlug: ws, TargetBranch: "main", SourceRef: "feature/a", SubmittedBy: owner}) ||
		job.Key != "ws:main:feature/a" || job.GroupKey != "ws:main" || job.Status != jobqueue.StatusQueued {
		t.Errorf("job %+v payload %+v", job, p)
	}
	if _, duplicate, err := EnqueueMergeJob(v.queue, ws, "main", "feature/a", member); err != nil || !duplicate {
		t.Errorf("second enqueue: duplicate %v, %v", duplicate, err)
	}
	if _, duplicate, _ := EnqueueMergeJob(v.queue, ws, "main", "feature/b", owner); duplicate {
		t.Error("another source counted as a duplicate")
	}
	q, _ := jobqueue.New(testutil.OpenDB(t), nil)
	if err := RegisterHandler(q, nil); err != nil {
		t.Fatal(err)
	}
}

// submitted_by is the user of an API key or PAT and "admin" for an admin
// token; a nil credential or one without a user cannot be attributed.
func TestResolveSubmittedBy(t *testing.T) {
	for _, c := range []struct {
		auth *apikit.AuthInfo
		want string
	}{
		{testutil.User("alice"), "alice"},
		{testutil.PAT("bob", "merges:write"), "bob"},
		{testutil.Admin(), "admin"},
		{&apikit.AuthInfo{CredentialType: "api_key"}, ""},
		{nil, ""},
	} {
		got, err := ResolveSubmittedBy(c.auth)
		if got != c.want || (err == nil) != (c.want != "") {
			t.Errorf("ResolveSubmittedBy(%+v) = %q, %v; want %q", c.auth, got, err, c.want)
		}
	}
}
