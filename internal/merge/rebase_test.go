package merge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/carrypatch/gitcmd"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// RebaseBranch resolves the source, checks it out and rebases it onto the
// target, answering the new head; a conflict comes back as the runner's
// RebaseConflictError (the runner has aborted); an unresolvable source stops
// before any git operation; a cancelled context aborts the rebase.
func TestRebaseBranch(t *testing.T) {
	r := &fakeRunner{rebaseSHA: "abc"}
	sha, err := RebaseBranch(context.Background(), r, "feature/a", "main")
	if err != nil || sha != "abc" || strings.Join(r.calls, ", ") != "rev-parse feature/a, checkout feature/a --, rebase main" {
		t.Errorf("success: %q, %v, calls %v", sha, err, r.calls)
	}
	r = &fakeRunner{rebaseErr: &gitcmd.RebaseConflictError{ConflictingFiles: []string{"a.txt"}}}
	var conflict *gitcmd.RebaseConflictError
	if _, err := RebaseBranch(context.Background(), r, "feature/a", "main"); !errors.As(err, &conflict) || conflict.ConflictingFiles[0] != "a.txt" || len(r.calls) != 3 {
		t.Errorf("conflict: %v, calls %v", err, r.calls)
	}
	r = &fakeRunner{revParseErr: errors.New("unknown revision")}
	if _, err := RebaseBranch(context.Background(), r, "nope", "main"); err == nil || len(r.calls) != 1 {
		t.Errorf("unknown source: %v, calls %v", err, r.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = &fakeRunner{rebaseErr: context.Canceled}
	if _, err := RebaseBranch(ctx, r, "feature/a", "main"); !errors.Is(err, context.Canceled) || r.calls[len(r.calls)-1] != "rebase --abort" {
		t.Errorf("cancelled: %v, calls %v", err, r.calls)
	}
}

// BatchRebase rebases each branch in turn, going on past conflicts and
// errors with a result per branch, and puts the trunk back on the branch it
// was on; an empty list is an error.
func TestBatchRebase_ContinuesPastConflicts(t *testing.T) {
	if _, err := BatchRebase(context.Background(), &fakeRunner{}, "main", nil); err == nil {
		t.Error("empty list accepted")
	}
	root := t.TempDir()
	trunk := setupTrunk(t, root, ws)
	cfg := NewMergeAPIConfig(nil, nil, nil, root)
	results, err := cfg.BatchRebase(context.Background(), ws, "main", []string{"feature/clean", "feature/conflict", "feature/missing", "feature/a"})
	if err != nil || len(results) != 4 {
		t.Fatalf("BatchRebase: %v, %v", results, err)
	}
	main := gitIn(t, trunk, "rev-parse", "main")
	for i, want := range []RebaseResult{
		{Branch: "feature/clean", Status: "ok", NewHead: gitIn(t, trunk, "rev-parse", "feature/clean")},
		{Branch: "feature/conflict", Status: "conflict", ConflictFiles: []string{"base.txt"}},
		{Branch: "feature/missing", Status: "error"},
		{Branch: "feature/a", Status: "ok", NewHead: gitIn(t, trunk, "rev-parse", "feature/a")},
	} {
		got := results[i]
		if got.Branch != want.Branch || got.Status != want.Status || got.NewHead != want.NewHead || strings.Join(got.ConflictFiles, ",") != strings.Join(want.ConflictFiles, ",") {
			t.Errorf("result %d: %+v, want %+v", i, got, want)
		}
	}
	if gitIn(t, trunk, "rev-parse", "feature/clean^") != main || gitIn(t, trunk, "rev-parse", "feature/conflict^") == main {
		t.Error("the clean branch was not rebased onto main, or the conflicting one was")
	}
	if head := gitIn(t, trunk, "symbolic-ref", "--short", "HEAD"); head != "main" {
		t.Errorf("trunk left on %s, want main", head)
	}
	if exists, err := DefaultBranchChecker(root)(ws, "feature/a"); !exists || err != nil {
		t.Errorf("DefaultBranchChecker(feature/a) = %v, %v", exists, err)
	}
	if exists, _ := DefaultBranchChecker(root)(ws, "feature/missing"); exists {
		t.Error("DefaultBranchChecker found a missing branch")
	}
}

// POST /rebase runs the configured batch rebase for the owner (or an admin
// token) of an active, cloned workspace, under the workspace lock, and
// answers the per-branch results; the request is validated like a merge.
func TestBatchRebaseRoute(t *testing.T) {
	v := newEnv(t)
	var got []string
	v.rebase = func(_ context.Context, slug, target string, branches []string) ([]RebaseResult, error) {
		got = append(got, slug+" "+target+" "+strings.Join(branches, ","))
		return []RebaseResult{{Branch: branches[0], Status: "ok", NewHead: "abc"}, {Branch: branches[1], Status: "conflict", ConflictFiles: []string{"x"}}}, nil
	}
	path := "/api/v1/workspaces/ws/rebase"
	rec := v.do(t, http.MethodPost, path, `{"target_ref":"main","branches":["feature/a","feature/b"]}`, testutil.PAT(owner, "merges:write"))
	if rec.Code != http.StatusOK || strings.Join(got, ";") != "ws main feature/a,feature/b" ||
		strings.TrimSpace(rec.Body.String()) != `{"results":[{"branch":"feature/a","status":"ok","new_head":"abc"},{"branch":"feature/b","status":"conflict","conflict_files":["x"]}]}` {
		t.Errorf("rebase: %d %s, calls %v", rec.Code, rec.Body, got)
	}
	for _, c := range []struct {
		name, body    string
		auth          *apikit.AuthInfo
		status        int
		message, kind string
	}{
		{"no target", `{"branches":["a"]}`, testutil.User(owner), http.StatusBadRequest, "target_ref is required", ""},
		{"no branches", `{"target_ref":"main","branches":[]}`, testutil.User(owner), http.StatusBadRequest, "branches list must not be empty", ""},
		{"option-like target", `{"target_ref":"-x","branches":["a"]}`, testutil.User(owner), http.StatusBadRequest, "invalid target_ref: ref name must not start with '-'", ""},
		{"option-like branch", `{"target_ref":"main","branches":["a","-x"]}`, testutil.User(owner), http.StatusBadRequest, "invalid branches[1]: ref name must not start with '-'", ""},
		{"malformed", `{`, testutil.User(owner), http.StatusBadRequest, "invalid request body: unexpected EOF", ""},
		{"PAT without scope", `{"target_ref":"main","branches":["a"]}`, testutil.PAT(owner, "merges:read"), http.StatusForbidden, "PAT requires merges:write scope", ""},
		{"member", `{"target_ref":"main","branches":["a"]}`, testutil.User(member), http.StatusForbidden, "org members have read-only access to this workspace", "forbidden"},
		{"no credential", `{"target_ref":"main","branches":["a"]}`, nil, http.StatusUnauthorized, "authentication required", ""},
	} {
		wantError(t, v.do(t, http.MethodPost, path, c.body, c.auth), c.status, c.message, c.kind)
	}
	v.rebase = func(context.Context, string, string, []string) ([]RebaseResult, error) {
		return nil, errors.New("boom")
	}
	wantError(t, v.do(t, http.MethodPost, path, `{"target_ref":"main","branches":["a"]}`, testutil.User(owner)), http.StatusInternalServerError, "batch rebase failed: boom", "")
	unlock := wslock.Lock(ws)
	wantError(t, v.do(t, http.MethodPost, path, `{"target_ref":"main","branches":["a"]}`, testutil.User(owner)), http.StatusConflict, "another operation is running on this workspace; retry later", "workspace_busy")
	unlock()
	e, api := testutil.API()
	RegisterMergeRoutes(api, MergeAPIConfig{DB: v.db, Queue: v.queue})
	wantError(t, testutil.Request(t, e, http.MethodPost, path, `{"target_ref":"main","branches":["a"]}`, testutil.User(owner)), http.StatusInternalServerError, "batch rebase not configured", "")
}
