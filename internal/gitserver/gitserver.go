// Package gitserver implements a git smart HTTP server for af-hub,
// exposing workspace repositories over HTTP at /git/<org-slug>/<workspace-slug>.git/
// on the same port as the REST API.
package gitserver

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/txsvc/apikit"
)

// WorkspaceLoader implements the go-git server.Loader interface,
// resolving a transport.Endpoint to a repository storer for the
// requested workspace. It extracts the org slug and workspace slug from
// the endpoint path, verifies the workspace exists, the org matches,
// and clone_status is 'ready', then opens the repository via PlainOpen
// and returns its storer.Storer.
type WorkspaceLoader struct {
	db            *sql.DB
	workspaceRoot string

	// Per-request fields, set by the receive-pack handler before the
	// go-git server calls Load. These are bound into the thinPackSafeStorer
	// so the pre-receive hook has access to request context and actor.
	reqCtx   context.Context
	reqActor *apikit.AuthInfo

	// lastStorer holds the thinPackSafeStorer created by Load on a
	// per-request loader (one built by forRequest). The receive-pack handler
	// reads it to retrieve hook-rejected refs after the session completes.
	//
	// It is deliberately never written on the shared base loader: that
	// loader backs the startup transport and serves concurrent info/refs
	// requests, so an unsynchronised write there is a data race. A
	// per-request loader is used by a single goroutine.
	lastStorer *thinPackSafeStorer
}

// NewWorkspaceLoader creates a new WorkspaceLoader that resolves
// workspace repositories under the given workspaceRoot directory.
func NewWorkspaceLoader(db *sql.DB, workspaceRoot string) *WorkspaceLoader {
	return &WorkspaceLoader{db: db, workspaceRoot: workspaceRoot}
}

// forRequest returns a shallow copy of the loader with per-request context
// and actor bound. The returned loader is used for a single receive-pack
// session so the storer it creates can consult the pre-receive hook with
// the correct request context and actor identity.
//
// A nil ctx is replaced by context.Background() so that a non-nil reqCtx
// reliably marks a per-request loader (see lastStorer). The storer treats a
// nil ctx as Background anyway, so behaviour is unchanged.
func (l *WorkspaceLoader) forRequest(ctx context.Context, actor *apikit.AuthInfo) *WorkspaceLoader {
	if ctx == nil {
		ctx = context.Background()
	}
	return &WorkspaceLoader{
		db:            l.db,
		workspaceRoot: l.workspaceRoot,
		reqCtx:        ctx,
		reqActor:      actor,
	}
}

// Load resolves a transport.Endpoint to a storer.Storer for the
// workspace repository. Returns transport.ErrRepositoryNotFound if the
// workspace is not found, not ready, or the org slug does not match.
//
// Returns a wrapped I/O error (not ErrRepositoryNotFound) when PlainOpen
// fails, so the HTTP handler can distinguish 404 from 500.
func (l *WorkspaceLoader) Load(ep *transport.Endpoint) (storer.Storer, error) {
	// Parse org and workspace slug from the endpoint path.
	orgSlug, slug, err := parseEndpointPath(ep.Path)
	if err != nil {
		return nil, transport.ErrRepositoryNotFound
	}

	// Resolve the workspace from the database using shared resolution logic.
	ws, err := resolveWorkspaceFromDB(l.db, orgSlug, slug)
	if err != nil {
		return nil, fmt.Errorf("workspace resolution: %w", err)
	}
	if ws == nil {
		return nil, transport.ErrRepositoryNotFound
	}

	// Verify clone_status is 'ready' (06-REQ-4.4).
	if ws.CloneStatus != "ready" {
		return nil, transport.ErrRepositoryNotFound
	}

	// Open the repository at <workspaceRoot>/<slug>/trunk/.
	repoPath := filepath.Join(l.workspaceRoot, slug, "trunk")
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repository at %s: %w", repoPath, err)
	}

	// Wrap the storer so it does NOT satisfy storer.PackfileWriter.
	// go-git's packfile.UpdateObjectStorage takes a raw-copy shortcut
	// when the storer implements PackfileWriter, but that path parses
	// the incoming pack without access to the existing object store,
	// so REF_DELTA objects in thin packs (sent by git push) fail with
	// "reference delta not found". The wrapper forces the parser-with-
	// storage path, which can resolve deltas against existing objects.
	wrapper := &thinPackSafeStorer{
		Storer: repo.Storer,
		ctx:    l.reqCtx,
		db:     l.db,
		slug:   slug,
		actor:  l.reqActor,
	}
	// Record the storer only on a per-request loader; see lastStorer.
	if l.reqCtx != nil {
		l.lastStorer = wrapper
	}
	return wrapper, nil
}

// thinPackSafeStorer wraps a storer.Storer without implementing
// storer.PackfileWriter, forcing go-git to use the parser path
// that can resolve thin pack deltas against the existing object store.
//
// When a pre-receive hook is registered, it intercepts SetReference and
// RemoveReference to consult the hook before delegating to the underlying
// storer. A hook error is returned without modifying the underlying storer,
// and go-git records it as a per-ref status in the report.
type thinPackSafeStorer struct {
	storer.Storer

	// Per-request fields for the pre-receive hook.
	ctx   context.Context
	db    *sql.DB
	slug  string
	actor *apikit.AuthInfo

	// rejectedRefs tracks ref names rejected by the pre-receive hook.
	// Used by the accepted-refs logic in a later task.
	rejectedRefs map[plumbing.ReferenceName]bool
}

// SetReference intercepts reference writes to consult the pre-receive hook.
// If the hook rejects the update, the error is returned and the underlying
// storer is not modified. go-git's server records this error as the per-ref
// status in the report.
func (s *thinPackSafeStorer) SetReference(ref *plumbing.Reference) error {
	// 23-REQ-6.1: Reject refs under the protected backup-ref namespace
	// before the pre-receive hook is consulted.
	if isProtectedRef(ref.Name()) {
		s.markRejected(ref.Name())
		return errProtectedRef
	}

	hook := preReceiveHook
	if hook != nil {
		// Determine the old hash: look up the current value of the ref.
		oldHash := plumbing.ZeroHash
		if existing, err := s.Storer.Reference(ref.Name()); err == nil {
			oldHash = existing.Hash()
		}

		ctx := s.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		upd := RefUpdate{
			Name: ref.Name(),
			Old:  oldHash,
			New:  ref.Hash(),
		}
		if err := hook(ctx, s.db, s.slug, s.actor, upd); err != nil {
			s.markRejected(ref.Name())
			return err
		}
	}
	return s.Storer.SetReference(ref)
}

// CheckAndSetReference intercepts compare-and-swap reference writes to
// consult the pre-receive hook. Although go-git's server does not currently
// call this method, we intercept it for completeness.
func (s *thinPackSafeStorer) CheckAndSetReference(new, old *plumbing.Reference) error {
	// 23-REQ-6.1: Reject refs under the protected backup-ref namespace
	// before the pre-receive hook is consulted.
	if isProtectedRef(new.Name()) {
		s.markRejected(new.Name())
		return errProtectedRef
	}

	hook := preReceiveHook
	if hook != nil {
		oldHash := plumbing.ZeroHash
		if old != nil {
			oldHash = old.Hash()
		}

		ctx := s.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		upd := RefUpdate{
			Name: new.Name(),
			Old:  oldHash,
			New:  new.Hash(),
		}
		if err := hook(ctx, s.db, s.slug, s.actor, upd); err != nil {
			s.markRejected(new.Name())
			return err
		}
	}
	return s.Storer.CheckAndSetReference(new, old)
}

// RemoveReference intercepts reference removals to consult the pre-receive
// hook. If the hook rejects the removal, the error is returned and the
// underlying storer is not modified.
func (s *thinPackSafeStorer) RemoveReference(name plumbing.ReferenceName) error {
	// 23-REQ-6.1: Reject refs under the protected backup-ref namespace
	// before the pre-receive hook is consulted.
	if isProtectedRef(name) {
		s.markRejected(name)
		return errProtectedRef
	}

	hook := preReceiveHook
	if hook != nil {
		oldHash := plumbing.ZeroHash
		if existing, err := s.Storer.Reference(name); err == nil {
			oldHash = existing.Hash()
		}

		ctx := s.ctx
		if ctx == nil {
			ctx = context.Background()
		}

		upd := RefUpdate{
			Name: name,
			Old:  oldHash,
			New:  plumbing.ZeroHash,
		}
		if err := hook(ctx, s.db, s.slug, s.actor, upd); err != nil {
			s.markRejected(name)
			return err
		}
	}
	return s.Storer.RemoveReference(name)
}

// markRejected records a ref name as rejected by the pre-receive hook.
func (s *thinPackSafeStorer) markRejected(name plumbing.ReferenceName) {
	if s.rejectedRefs == nil {
		s.rejectedRefs = make(map[plumbing.ReferenceName]bool)
	}
	s.rejectedRefs[name] = true
}

// RejectedRefs returns the set of ref names rejected by the pre-receive hook.
func (s *thinPackSafeStorer) RejectedRefs() map[plumbing.ReferenceName]bool {
	return s.rejectedRefs
}
