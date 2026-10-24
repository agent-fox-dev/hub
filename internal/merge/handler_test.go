package merge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// The pre-check: a source that would conflict is a permanent rejection with
// the conflicting files; a source already in the target is a success with
// AlreadyMerged; a source at the target's head is a retryable BranchNotReady;
// an unknown ref and a missing trunk are plain errors.
func TestPreCheck(t *testing.T) {
	root := t.TempDir()
	setupTrunk(t, root, ws)
	h := newHandler(root, nil)
	cases := []struct {
		source    string
		reason    RejectionReason
		permanent bool
		files     string
		ok        bool // a result, not an error
	}{
		{"feature/conflict", WouldConflict, true, "base.txt", false},
		{"feature/already-merged", AlreadyMerged, false, "", true},
		{"feature/not-ready", BranchNotReady, false, "", false},
		{"feature/clean", "", false, "", true},
	}
	for _, c := range cases {
		result, err := h.PreCheck(context.Background(), ws, "main", c.source)
		if c.ok {
			if err != nil || (c.reason != "" && (result == nil || result.Reason != c.reason)) || (c.reason == "" && result != nil) {
				t.Errorf("%s: (%v, %v), want a result with reason %q", c.source, result, err, c.reason)
			}
			continue
		}
		var rejection *MergeRejection
		if result != nil || !errors.As(err, &rejection) || rejection.Reason != c.reason || rejection.Permanent != c.permanent || strings.Join(rejection.ConflictFiles, ",") != c.files {
			t.Errorf("%s: (%v, %v), want rejection %s permanent %v files %q", c.source, result, err, c.reason, c.permanent, c.files)
		} else if rejection.Error() != "merge rejected: "+string(c.reason) {
			t.Errorf("%s: message %q", c.source, rejection.Error())
		}
	}
	var rejection *MergeRejection
	if _, err := h.PreCheck(context.Background(), ws, "main", "feature/missing"); err == nil || errors.As(err, &rejection) {
		t.Errorf("unknown source: %v, want a plain error", err)
	}
	if _, err := h.PreCheck(context.Background(), "nowhere", "main", "feature/a"); err == nil || !strings.Contains(err.Error(), "trunk dir not found") {
		t.Errorf("missing trunk: %v", err)
	}
}

// FetchTarget fetches the trunk with the credentials ResolveAuth answers and
// fails when they cannot be resolved.
func TestFetchTarget_UsesResolvedCredentials(t *testing.T) {
	var fetched []string
	var seen transport.AuthMethod
	h := newHandler("/root", &fetched)
	auth := &http.BasicAuth{Username: "u", Password: "p"}
	h.ResolveAuth = func(slug string) (transport.AuthMethod, error) { return auth, nil }
	fetch := h.Fetch
	h.Fetch = func(ctx context.Context, trunk, target string, a transport.AuthMethod) error {
		seen = a
		return fetch(ctx, trunk, target, a)
	}
	if err := h.FetchTarget(context.Background(), ws, "main"); err != nil || seen != auth || strings.Join(fetched, ",") != "/root/ws/trunk main" {
		t.Errorf("FetchTarget: %v, auth %v, fetched %v", err, seen, fetched)
	}
	h.ResolveAuth = func(string) (transport.AuthMethod, error) { return nil, errors.New("no credentials") }
	if err := h.FetchTarget(context.Background(), ws, "main"); err == nil || !strings.Contains(err.Error(), "no credentials") || len(fetched) != 1 {
		t.Errorf("unresolvable credentials: %v, fetched %v", err, fetched)
	}
}

// RebaseSource rebases the source onto the target in the trunk; a conflict
// is a permanent rejection with the files and leaves the branch where it was.
func TestRebaseSource(t *testing.T) {
	root := t.TempDir()
	trunk := setupTrunk(t, root, ws)
	h := newHandler(root, nil)
	before := gitIn(t, trunk, "rev-parse", "feature/conflict")
	var rejection *MergeRejection
	err := h.RebaseSource(context.Background(), ws, "main", "feature/conflict")
	if !errors.As(err, &rejection) || rejection.Reason != WouldConflict || !rejection.Permanent || strings.Join(rejection.ConflictFiles, ",") != "base.txt" {
		t.Errorf("conflict: %v", err)
	}
	if gitIn(t, trunk, "rev-parse", "feature/conflict") != before || gitIn(t, trunk, "status", "--porcelain") != "" {
		t.Error("the conflicting branch moved or the tree is dirty")
	}
	if err := h.RebaseSource(context.Background(), ws, "main", "feature/clean"); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if gitIn(t, trunk, "rev-parse", "feature/clean^") != gitIn(t, trunk, "rev-parse", "main") {
		t.Error("feature/clean is not on top of main")
	}
	if err := h.RebaseSource(context.Background(), ws, "main", "feature/missing"); err == nil || errors.As(err, &rejection) {
		t.Errorf("unknown source: %v, want a plain error", err)
	}
}

// UpdateTargetRef fast-forwards the target ref locally (a bad object is a
// retryable rejection); DeleteSourceBranch removes a ref; Finalize checks
// the SHAs.
func TestUpdateTargetRef_DeleteSourceBranch_Finalize(t *testing.T) {
	root := t.TempDir()
	trunk := setupTrunk(t, root, ws)
	h := newHandler(root, nil)
	clean := gitIn(t, trunk, "rev-parse", "feature/clean")
	if err := h.UpdateTargetRef(context.Background(), ws, "main", clean); err != nil || gitIn(t, trunk, "rev-parse", "main") != clean {
		t.Errorf("UpdateTargetRef: %v, main at %s", err, gitIn(t, trunk, "rev-parse", "main"))
	}
	var rejection *MergeRejection
	if err := h.UpdateTargetRef(context.Background(), ws, "main", strings.Repeat("1", 40)); !errors.As(err, &rejection) || rejection.Permanent {
		t.Errorf("bad object: %v, want a retryable rejection", err)
	}
	if err := h.UpdateTargetRef(context.Background(), "nowhere", "main", clean); !errors.As(err, &rejection) || rejection.Permanent {
		t.Errorf("missing trunk: %v, want a retryable rejection", err)
	}
	if err := h.DeleteSourceBranch(context.Background(), ws, "feature/a"); err != nil || gitIn(t, trunk, "branch", "--list", "feature/a") != "" {
		t.Errorf("DeleteSourceBranch: %v, branches %s", err, gitIn(t, trunk, "branch", "--list"))
	}
	if err := h.DeleteSourceBranch(context.Background(), "nowhere", "feature/a"); err == nil {
		t.Error("deleting in a missing trunk: nil error")
	}
	if r, err := h.Finalize(clean, clean); err != nil || r.BaseSHA != clean || r.MergedSHA != clean {
		t.Errorf("Finalize: %v, %v", r, err)
	}
	for _, c := range [][2]string{{"short", clean}, {clean, ""}} {
		if _, err := h.Finalize(c[0], c[1]); err == nil {
			t.Errorf("Finalize(%q, %q): nil error", c[0], c[1])
		}
	}
}
