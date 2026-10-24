package gitserver

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/txsvc/apikit"
)

// Only names under refs/hub/replaced/ are protected; near misses are not.
func TestIsProtectedRef(t *testing.T) {
	for name, want := range map[string]bool{
		"refs/hub/replaced/feature":       true,
		"refs/hub/replaced/deep/branch":   true,
		"refs/hub/replaced":               false,
		"refs/hub/replacedx":              false,
		"refs/hub/forward/x":              false,
		"refs/heads/hub/replaced/feature": false,
		"refs/heads/main":                 false,
		"refs/tags/v1":                    false,
	} {
		if got := isProtectedRef(plumbing.ReferenceName(name)); got != want {
			t.Errorf("isProtectedRef(%s) = %v; want %v", name, got, want)
		}
	}
}

// The storer consults the pre-receive hook, with the workspace, the actor
// and the old and new hashes, before every reference write, swap and
// removal; a rejection leaves the reference as it was and is remembered. A
// protected ref is rejected before the hook is consulted.
func TestStorer_ConsultsHookBeforeWritingRefs(t *testing.T) {
	origPre := preReceiveHook
	t.Cleanup(func() { preReceiveHook = origPre })
	h1, h2 := plumbing.NewHash(strings.Repeat("1", 40)), plumbing.NewHash(strings.Repeat("2", 40))
	actor := &apikit.AuthInfo{UserID: "owner"}
	var calls []RefUpdate
	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, slug string, a *apikit.AuthInfo, upd RefUpdate) error {
		if slug != "ws" || a != actor {
			t.Errorf("hook called for %s as %v; want ws as the actor", slug, a)
		}
		calls = append(calls, upd)
		if upd.Name == "refs/heads/no" {
			return errors.New("no")
		}
		return nil
	})
	mem := memory.NewStorage()
	s := &thinPackSafeStorer{Storer: mem, slug: "ws", actor: actor}
	main := plumbing.NewHashReference("refs/heads/main", h1)
	if err := s.SetReference(main); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReference(plumbing.NewHashReference("refs/heads/main", h2)); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckAndSetReference(plumbing.NewHashReference("refs/heads/main", h1), plumbing.NewHashReference("refs/heads/main", h2)); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveReference("refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetReference(plumbing.NewHashReference("refs/heads/no", h1)); err == nil || err.Error() != "no" {
		t.Errorf("rejected write: %v; want the hook's error", err)
	}
	want := []RefUpdate{
		{Name: "refs/heads/main", Old: plumbing.ZeroHash, New: h1},
		{Name: "refs/heads/main", Old: h1, New: h2},
		{Name: "refs/heads/main", Old: h2, New: h1},
		{Name: "refs/heads/main", Old: h1, New: plumbing.ZeroHash},
		{Name: "refs/heads/no", Old: plumbing.ZeroHash, New: h1},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("hook calls %v; want %v", calls, want)
	}
	for _, name := range []plumbing.ReferenceName{"refs/heads/main", "refs/heads/no"} {
		if _, err := mem.Reference(name); err == nil {
			t.Errorf("%s exists; want removed or never written", name)
		}
	}
	protected := plumbing.NewHashReference("refs/hub/replaced/x", h1)
	for name, err := range map[string]error{
		"set":    s.SetReference(protected),
		"swap":   s.CheckAndSetReference(protected, nil),
		"remove": s.RemoveReference(protected.Name()),
	} {
		if !errors.Is(err, errProtectedRef) {
			t.Errorf("%s of a protected ref: %v; want errProtectedRef", name, err)
		}
	}
	if len(calls) != len(want) {
		t.Errorf("the hook was consulted for a protected ref: %v", calls[len(want):])
	}
	if got := s.RejectedRefs(); !reflect.DeepEqual(got, map[plumbing.ReferenceName]bool{"refs/heads/no": true, "refs/hub/replaced/x": true}) {
		t.Errorf("rejected refs %v", got)
	}
}

// A client cannot create, update or delete a ref under refs/hub/replaced/:
// the push is rejected with the fixed message, the ref keeps its value, the
// pre-receive hook is not consulted, and only the other refs of the push
// drive the post-push hook and the audit event.
func TestPush_ProtectedNamespaceRejected(t *testing.T) {
	var protectedCalls int
	_, s, trunk, local, _ := pushSetup(t, func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, upd RefUpdate) error {
		if isProtectedRef(upd.Name) {
			protectedCalls++
		}
		return nil
	})
	initial := gitRun(t, trunk, "rev-parse", "HEAD")
	gitRun(t, trunk, "update-ref", "refs/hub/replaced/feature", initial)
	sha := addCommit(t, local, "new.txt", "new")
	for _, refspecs := range [][]string{
		{sha + ":refs/hub/replaced/new"},
		{sha + ":refs/hub/replaced/feature"},
		{sha + ":refs/heads/branch", ":refs/hub/replaced/feature"},
	} {
		out, err := gitTry(local, append([]string{"push", "origin"}, refspecs...)...)
		if err == nil || !strings.Contains(out, protectedRefMessage) {
			t.Errorf("push %v: %v\n%s\nwant a rejection with %q", refspecs, err, out, protectedRefMessage)
		}
	}
	if refValue(trunk, "refs/hub/replaced/new") != "" || refValue(trunk, "refs/hub/replaced/feature") != initial || refValue(trunk, "refs/heads/branch") != sha {
		t.Errorf("trunk: new %q, feature %q, branch %q; want none, %s, %s", refValue(trunk, "refs/hub/replaced/new"), refValue(trunk, "refs/hub/replaced/feature"), refValue(trunk, "refs/heads/branch"), initial, sha)
	}
	if protectedCalls != 0 {
		t.Errorf("the hook was consulted %d times for protected refs", protectedCalls)
	}
	if got := s.Pushed(t); !reflect.DeepEqual(got, []string{"branch"}) {
		t.Errorf("first post-push call %v; want [branch]", got)
	}
	if events := s.Events(); len(events) != 1 || !reflect.DeepEqual(refsUpdated(t, events[0]), []string{"refs/heads/branch"}) {
		t.Errorf("audit events %+v; want one for refs/heads/branch", events)
	}
}
