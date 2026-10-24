package gitserver

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// Load opens the repository of a ready workspace named by either endpoint
// form, as a storer that hides PackfileWriter so thin packs are resolved
// against the existing objects.
func TestLoader_OpensReadyWorkspace(t *testing.T) {
	v := newEnv(t)
	loader := NewWorkspaceLoader(v.db, v.root)
	head := gitRun(t, v.trunk("ws"), "rev-parse", "HEAD")
	for _, path := range []string{"/git/myorg/ws.git", "/myorg/ws.git"} {
		s, err := loader.Load(&transport.Endpoint{Path: path})
		if err != nil {
			t.Fatalf("Load(%s): %v", path, err)
		}
		if _, ok := s.(storer.PackfileWriter); ok {
			t.Error("the storer implements PackfileWriter")
		}
		ref, err := storer.ResolveReference(s, plumbing.HEAD)
		if err != nil || ref.Hash().String() != head {
			t.Errorf("Load(%s): HEAD %v, %v; want %s", path, ref, err, head)
		}
	}
}

// Load answers ErrRepositoryNotFound for a slug it does not know, a wrong org,
// a workspace outside every org, a clone that is not ready and a path without
// org and slug; a repository missing on disk is a different error, so the
// handler can tell a 500 from a 404.
func TestLoader_NotFound(t *testing.T) {
	v := newEnv(t)
	testutil.SeedOrg(t, v.db, "org-2", "otherorg", "owner")
	v.workspace(t, "noorg", "owner", "", "ready")
	v.workspace(t, "cloning", "owner", orgID, "cloning")
	v.workspace(t, "nodisk", "owner", orgID, "ready")
	loader := NewWorkspaceLoader(v.db, v.root)
	for _, path := range []string{"/git/myorg/unknown.git", "/git/otherorg/ws.git", "/git/myorg/noorg.git", "/git/myorg/cloning.git", "", "/", "/git", "/git/myorg"} {
		if _, err := loader.Load(&transport.Endpoint{Path: path}); !errors.Is(err, transport.ErrRepositoryNotFound) {
			t.Errorf("Load(%q): %v; want ErrRepositoryNotFound", path, err)
		}
	}
	if _, err := loader.Load(&transport.Endpoint{Path: "/git/myorg/nodisk.git"}); err == nil || errors.Is(err, transport.ErrRepositoryNotFound) {
		t.Errorf("Load of a workspace without a repository: %v; want an open error", err)
	}
}

// Only a per-request loader records the storer it returned (the receive-pack
// handler reads the hook-rejected refs from it); the shared base loader,
// which serves concurrent requests, never writes it.
func TestLoader_RecordsStorerPerRequestOnly(t *testing.T) {
	v := newEnv(t)
	base := NewWorkspaceLoader(v.db, v.root)
	ep := &transport.Endpoint{Path: "/git/myorg/ws.git"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := base.Load(ep); err != nil {
				t.Errorf("concurrent Load: %v", err)
			}
		}()
	}
	wg.Wait()
	if base.lastStorer != nil {
		t.Error("the base loader recorded a storer")
	}
	for name, ctx := range map[string]context.Context{"with context": context.Background(), "nil context": nil} {
		req := base.forRequest(ctx, nil) //nolint:staticcheck // a nil context is tolerated
		s, err := req.Load(ep)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if req.lastStorer == nil || storer.Storer(req.lastStorer) != s || base.lastStorer != nil {
			t.Errorf("%s: request loader recorded %p for %p, base %p", name, req.lastStorer, s, base.lastStorer)
		}
	}
}
