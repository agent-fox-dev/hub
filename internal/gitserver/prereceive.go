package gitserver

import (
	"context"
	"database/sql"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/txsvc/apikit"
)

// RefUpdate describes a single reference update in a receive-pack request.
type RefUpdate struct {
	Name plumbing.ReferenceName
	Old  plumbing.Hash
	New  plumbing.Hash
}

// PreReceiveHookFunc is called once per ref update after the pack has been
// unpacked into the trunk and before the hub writes that ref. Returning a
// non-nil error rejects the update: the hub does not write the ref and the
// error text becomes the per-ref status message the client sees.
type PreReceiveHookFunc func(ctx context.Context, db *sql.DB, slug string, actor *apikit.AuthInfo, upd RefUpdate) error

// preReceiveHook is the registered pre-receive hook. When non-nil, it is
// called for every ref update during receive-pack.
var preReceiveHook PreReceiveHookFunc

// RegisterPreReceiveHook registers a function to be called before each ref
// write during a git push. Called from main.go during server initialization.
// Registering nil clears the hook. With no hook registered, the receive-pack
// flow behaves as before this spec.
func RegisterPreReceiveHook(fn PreReceiveHookFunc) {
	preReceiveHook = fn
}
