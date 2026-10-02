package gitserver

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/txsvc/apikit"
)

// TS-22-30: A mixed push calls the post-push hook with accepted branches
// only and audits only the accepted ref.
// Verifies: 22-REQ-5.2
func TestPushHook_TS22_30_MixedPushAcceptedOnly(t *testing.T) {
	_, trunk, remote := newPreReceiveClientServer(t)

	// Set up a recording audit emitter.
	auditMock := newGitAuditEmitter()
	origEmitter := defaultAuditEmitter
	defaultAuditEmitter = auditMock
	t.Cleanup(func() { defaultAuditEmitter = origEmitter })

	// Set up a recording post-push hook.
	var mu sync.Mutex
	var hookBranches []string
	RegisterPostPushHook(func(_ *sql.DB, _ string, branches []string) {
		mu.Lock()
		defer mu.Unlock()
		hookBranches = append(hookBranches, branches...)
	})

	// Pre-receive hook: reject refs/heads/p1 only.
	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, upd RefUpdate) error {
		if string(upd.Name) == "refs/heads/p1" {
			return errRejected("p1 is blocked")
		}
		return nil
	})

	// Clone and set up branches.
	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)

	// Create branch "old" on the hub so we can delete it.
	runGitCmd(t, clone, "checkout", "-b", "old")
	addCommit(t, clone, "old.txt", "old commit")
	runGitCmd(t, clone, "push", "origin", "old")

	// Clear recorded data from setup push.
	mu.Lock()
	hookBranches = nil
	mu.Unlock()
	auditMock.mu.Lock()
	auditMock.events = nil
	auditMock.mu.Unlock()

	// Create branch "p1" (will be rejected) and "feature" (will be accepted).
	runGitCmd(t, clone, "checkout", "-b", "p1")
	addCommit(t, clone, "p1.txt", "p1 commit")
	runGitCmd(t, clone, "checkout", "-b", "feature")
	addCommit(t, clone, "feature.txt", "feature commit")

	// Push p1, feature, and delete old in one push.
	_, _ = gitCmdOutput(clone, "push", "origin", "p1", "feature", ":old")

	// Wait briefly for the async post-push hook goroutine.
	time.Sleep(200 * time.Millisecond)

	// Verify: feature should exist on trunk, p1 should not.
	featureSha := runGitCmd(t, clone, "rev-parse", "feature")
	trunkFeature := runGitCmd(t, trunk, "rev-parse", "refs/heads/feature")
	if trunkFeature != featureSha {
		t.Errorf("feature ref: trunk = %s; want %s", trunkFeature, featureSha)
	}

	_, p1Err := gitCmdOutput(trunk, "rev-parse", "--verify", "refs/heads/p1")
	if p1Err == nil {
		t.Error("refs/heads/p1 should not exist in trunk after rejected push")
	}

	// Post-push hook should receive only "feature" (delete excluded, p1 rejected).
	mu.Lock()
	gotBranches := make([]string, len(hookBranches))
	copy(gotBranches, hookBranches)
	mu.Unlock()

	if len(gotBranches) != 1 || gotBranches[0] != "feature" {
		t.Errorf("post-push hook branches = %v; want [feature]", gotBranches)
	}

	// Audit event should list only accepted refs (feature and the delete of old),
	// not p1.
	events := auditMock.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	ev := events[0]
	if ev.EventType != "hub.git.push" {
		t.Errorf("event_type = %q; want hub.git.push", ev.EventType)
	}
	refsUpdated, ok := ev.Metadata["refs_updated"].([]string)
	if !ok {
		t.Fatalf("refs_updated not a []string: %T", ev.Metadata["refs_updated"])
	}
	for _, ref := range refsUpdated {
		if ref == "refs/heads/p1" {
			t.Error("refs_updated should not contain refs/heads/p1")
		}
	}
	if !containsName(refsUpdated, "refs/heads/feature") {
		t.Error("refs_updated should contain refs/heads/feature")
	}
}

// TS-22-31 (from TS-22-30 spec): A rejected-only push enqueues no rebuild,
// emits no audit event and calls no post-push hook.
// Verifies: 22-REQ-5.3
func TestPushHook_TS22_31_RejectedOnlyPushNoSideEffects(t *testing.T) {
	_, _, remote := newPreReceiveClientServer(t)

	// Set up a recording audit emitter.
	auditMock := newGitAuditEmitter()
	origEmitter := defaultAuditEmitter
	defaultAuditEmitter = auditMock
	t.Cleanup(func() { defaultAuditEmitter = origEmitter })

	// Set up a recording post-push hook.
	var mu sync.Mutex
	var hookCalled bool
	RegisterPostPushHook(func(_ *sql.DB, _ string, branches []string) {
		mu.Lock()
		defer mu.Unlock()
		hookCalled = true
	})

	// Pre-receive hook: reject everything.
	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, _ RefUpdate) error {
		return errRejected("all blocked")
	})

	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)
	addCommit(t, clone, "x.txt", "x commit")
	runGitCmd(t, clone, "checkout", "-b", "rejected-branch")

	_, _ = gitCmdOutput(clone, "push", "origin", "rejected-branch")

	// Wait briefly for any async hook.
	time.Sleep(200 * time.Millisecond)

	// No post-push hook should have been called.
	mu.Lock()
	called := hookCalled
	mu.Unlock()
	if called {
		t.Error("post-push hook should not be called for rejected-only push")
	}

	// No audit event should have been emitted.
	events := auditMock.Events()
	if len(events) != 0 {
		t.Errorf("expected 0 audit events for rejected-only push, got %d", len(events))
	}
}

// TS-22-32: updateHeadSHA still reads the trunk HEAD for every push
// including rejected-only pushes.
// Verifies: 22-REQ-5.4
func TestPushHook_TS22_32_HeadSHAUpdatedForEveryPush(t *testing.T) {
	env, trunk, remote := newPreReceiveClientServer(t)

	// --- Push 1: accepted push ---
	RegisterPreReceiveHook(nil)

	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)
	addCommit(t, clone, "accepted.txt", "accepted commit")
	runGitCmd(t, clone, "push", "origin", "HEAD:refs/heads/main")

	// head_sha should equal the trunk HEAD after the accepted push.
	trunkHEAD := runGitCmd(t, trunk, "rev-parse", "HEAD")
	var headSHA string
	err := env.db.QueryRow(
		`SELECT COALESCE(head_sha, '') FROM workspaces WHERE slug = ?`, "myws",
	).Scan(&headSHA)
	if err != nil {
		t.Fatalf("failed to query head_sha: %v", err)
	}
	if headSHA != trunkHEAD {
		t.Errorf("after accepted push: head_sha = %q; want %q", headSHA, trunkHEAD)
	}

	// --- Push 2: rejected-only push ---
	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, _ RefUpdate) error {
		return errRejected("blocked")
	})

	addCommit(t, clone, "rejected.txt", "rejected commit")
	runGitCmd(t, clone, "checkout", "-b", "rejected-branch")
	_, _ = gitCmdOutput(clone, "push", "origin", "rejected-branch")

	// head_sha should still equal the trunk HEAD (unchanged).
	trunkHEAD2 := runGitCmd(t, trunk, "rev-parse", "HEAD")
	var headSHA2 string
	err = env.db.QueryRow(
		`SELECT COALESCE(head_sha, '') FROM workspaces WHERE slug = ?`, "myws",
	).Scan(&headSHA2)
	if err != nil {
		t.Fatalf("failed to query head_sha: %v", err)
	}
	if headSHA2 != trunkHEAD2 {
		t.Errorf("after rejected push: head_sha = %q; want trunk HEAD %q", headSHA2, trunkHEAD2)
	}
	// The trunk HEAD should not have changed.
	if trunkHEAD2 != trunkHEAD {
		t.Errorf("trunk HEAD changed after rejected push: %q -> %q", trunkHEAD, trunkHEAD2)
	}
}

// TS-22-33: For any push, side effects cover a subset of ref updates whose
// status is ok.
// Verifies: 22-REQ-5.5
func TestPushHook_TS22_33_PropertyAcceptedSubset(t *testing.T) {
	// Property test: for various combinations of commands and statuses,
	// the acceptedCommands function returns only commands with ok status.

	type testCase struct {
		name     string
		cmds     []*packp.Command
		statuses []*packp.CommandStatus
		rejected map[plumbing.ReferenceName]bool
		wantOK   []string // expected accepted ref names
	}

	cases := []testCase{
		{
			name: "all_ok",
			cmds: []*packp.Command{
				{Name: "refs/heads/a", New: plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")},
				{Name: "refs/heads/b", New: plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")},
			},
			statuses: []*packp.CommandStatus{
				{ReferenceName: "refs/heads/a", Status: "ok"},
				{ReferenceName: "refs/heads/b", Status: "ok"},
			},
			wantOK: []string{"refs/heads/a", "refs/heads/b"},
		},
		{
			name: "all_error",
			cmds: []*packp.Command{
				{Name: "refs/heads/a", New: plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")},
				{Name: "refs/heads/b", New: plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")},
			},
			statuses: []*packp.CommandStatus{
				{ReferenceName: "refs/heads/a", Status: "failed"},
				{ReferenceName: "refs/heads/b", Status: "rejected"},
			},
			wantOK: nil,
		},
		{
			name: "mixed_branches_and_tags",
			cmds: []*packp.Command{
				{Name: "refs/heads/main", New: plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")},
				{Name: "refs/tags/v1.0", New: plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")},
				{Name: "refs/heads/feature", New: plumbing.NewHash("cccccccccccccccccccccccccccccccccccccccc")},
			},
			statuses: []*packp.CommandStatus{
				{ReferenceName: "refs/heads/main", Status: "ok"},
				{ReferenceName: "refs/tags/v1.0", Status: "ok"},
				{ReferenceName: "refs/heads/feature", Status: "error"},
			},
			wantOK: []string{"refs/heads/main", "refs/tags/v1.0"},
		},
		{
			name: "delete_ok",
			cmds: []*packp.Command{
				{Name: "refs/heads/old", Old: plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), New: plumbing.ZeroHash},
			},
			statuses: []*packp.CommandStatus{
				{ReferenceName: "refs/heads/old", Status: "ok"},
			},
			wantOK: []string{"refs/heads/old"},
		},
		{
			name: "no_report_no_rejections",
			cmds: []*packp.Command{
				{Name: "refs/heads/a", New: plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")},
				{Name: "refs/heads/b", New: plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")},
			},
			statuses: nil, // no report
			rejected: nil,
			wantOK:   []string{"refs/heads/a", "refs/heads/b"},
		},
		{
			name: "no_report_some_rejected",
			cmds: []*packp.Command{
				{Name: "refs/heads/a", New: plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")},
				{Name: "refs/heads/b", New: plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")},
				{Name: "refs/heads/c", New: plumbing.NewHash("cccccccccccccccccccccccccccccccccccccccc")},
			},
			statuses: nil,
			rejected: map[plumbing.ReferenceName]bool{
				"refs/heads/b": true,
			},
			wantOK: []string{"refs/heads/a", "refs/heads/c"},
		},
		{
			name: "empty_commands",
			cmds: nil,
			statuses: []*packp.CommandStatus{
				{ReferenceName: "refs/heads/a", Status: "ok"},
			},
			wantOK: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rs *packp.ReportStatus
			if tc.statuses != nil {
				rs = &packp.ReportStatus{
					UnpackStatus:    "ok",
					CommandStatuses: tc.statuses,
				}
			}

			accepted := acceptedCommands(tc.cmds, rs, tc.rejected)
			gotNames := cmdNames(accepted)

			// Verify accepted is a subset of ok commands.
			if len(gotNames) != len(tc.wantOK) {
				t.Fatalf("accepted = %v; want %v", gotNames, tc.wantOK)
			}
			for _, want := range tc.wantOK {
				if !containsName(gotNames, want) {
					t.Errorf("expected %q in accepted; got %v", want, gotNames)
				}
			}

			// Verify no non-ok command appears in accepted.
			if rs != nil {
				okSet := make(map[string]bool)
				for _, cs := range rs.CommandStatuses {
					if cs.Status == "ok" {
						okSet[string(cs.ReferenceName)] = true
					}
				}
				for _, name := range gotNames {
					if !okSet[name] {
						t.Errorf("accepted contains %q which is not ok in report", name)
					}
				}
			}

			// Verify: when ok set is empty, no events should be emitted.
			if len(tc.wantOK) == 0 && len(accepted) != 0 {
				t.Error("expected no accepted commands when all are rejected")
			}

			// Verify extractPushedBranches on accepted excludes deletes.
			branches := extractPushedBranches(accepted)
			for _, b := range branches {
				// Find the command for this branch.
				for _, cmd := range accepted {
					if strings.TrimPrefix(string(cmd.Name), "refs/heads/") == b {
						if cmd.New == plumbing.ZeroHash {
							t.Errorf("extractPushedBranches included delete for %q", b)
						}
					}
				}
			}
		})
	}
}

// errRejected returns a simple error for test hook rejections.
func errRejected(msg string) error {
	return &rejectError{msg: msg}
}

type rejectError struct {
	msg string
}

func (e *rejectError) Error() string {
	return e.msg
}
